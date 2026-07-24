package admin

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Wave-4 (money side) admin handlers: the Revolut bank inbox (§4.1) and the AP/AR subledgers (§4.4).
// Stripe disputes (§4.3) have no admin RPC — they flow webhook → outbox → worker and surface on the
// dashboard (acct_dispute_open). Pattern matches accounting.go: validate (dto) → store → convert.

// bankParserFor returns the parser for a bank source ("" / "revolut" → Revolut; the only bank today).
func bankParserFor(source string) (acctrules.BankCsvParser, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", "revolut":
		return acctrules.NewRevolutParser(), nil
	default:
		return nil, status.Errorf(codes.InvalidArgument, "unknown bank source %q", source)
	}
}

// ImportBankCsv parses a bank CSV export into the inbox and reports parsed/imported/skipped counts.
func (s *Server) ImportBankCsv(ctx context.Context, req *pb_admin.ImportBankCsvRequest) (*pb_admin.ImportBankCsvResponse, error) {
	parser, err := bankParserFor(req.GetSource())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetCsvText()) == "" {
		return nil, status.Error(codes.InvalidArgument, "csv_text is required")
	}
	txns, err := parser.Parse(req.GetCsvText())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	res, err := s.repo.Accounting().ImportBankTxns(ctx, txns)
	if err != nil {
		return nil, mapAcctErr(ctx, "import bank csv", err)
	}
	return dto.ConvertAcctBankImportResultToPb(res), nil
}

// ListBankTxns returns inbox lines filtered by state.
func (s *Server) ListBankTxns(ctx context.Context, req *pb_admin.ListBankTxnsRequest) (*pb_admin.ListBankTxnsResponse, error) {
	state := strings.TrimSpace(req.GetState())
	if state != "" && !entity.ValidAcctBankTxnStates[entity.AcctBankTxnState(state)] {
		return nil, status.Errorf(codes.InvalidArgument, "invalid state %q", state)
	}
	txns, err := s.repo.Accounting().ListBankTxns(ctx, state, int(req.GetLimit()))
	if err != nil {
		return nil, mapAcctErr(ctx, "list bank txns", err)
	}
	return &pb_admin.ListBankTxnsResponse{Txns: dto.ConvertAcctBankTxnListToPb(txns)}, nil
}

// PostBankTxn books a manual-provenance journal entry for an inbox line (Dr/Cr by the signed amount) and
// marks the line posted, atomically. A non-EUR line's amount_src is folded to EUR base before posting.
func (s *Server) PostBankTxn(ctx context.Context, req *pb_admin.PostBankTxnRequest) (*pb_admin.PostBankTxnResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	accountCode := strings.ToUpper(strings.TrimSpace(req.GetAccountCode()))
	if accountCode == "" {
		return nil, status.Error(codes.InvalidArgument, "account_code is required")
	}

	txn, err := s.repo.Accounting().GetBankTxn(ctx, int(req.GetId()))
	if err != nil {
		return nil, mapAcctErr(ctx, "get bank txn", err)
	}
	if txn.State == entity.AcctBankTxnPosted {
		return nil, status.Error(codes.FailedPrecondition, "bank txn already posted")
	}

	occurredAt := txn.BookedAt
	if strings.TrimSpace(req.GetOccurredAt()) != "" {
		occurredAt, err = dto.ParseAcctAsOf(req.GetOccurredAt())
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
	}

	entry, err := acctrules.BuildBankTxnEntry(*txn, accountCode, occurredAt)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entry.CreatedBy = authsrv.GetAdminUsername(ctx)
	// AP-by-supplier: tag the entry so a 2010 payment lands under its supplier in GetPayables
	// instead of the anonymous "(untagged)" row. Optional (0 = untagged) — but when the
	// counter-account IS 2010, an untagged post is exactly the anonymous-AP failure the subledger
	// exists to prevent, so require the tag there (mirrors CreateJournalEntry's manual-2010 rule).
	if sid := req.GetSupplierId(); sid > 0 {
		entry.SupplierID = sql.NullInt64{Int64: int64(sid), Valid: true}
	} else if accountCode == acctrules.Acc2010 {
		return nil, status.Errorf(codes.InvalidArgument,
			"posting to %s Accounts Payable — supplier_id is required so the payable is tracked per supplier (pick one in ap / ar → suppliers)",
			acctrules.Acc2010)
	}

	// Fold any non-EUR amount_src legs to EUR base on the pool before the write (the store never does FX).
	if err := s.foldJournalLinesToBase(ctx, entry.Lines); err != nil {
		if errors.Is(err, errAcctNoFxRate) || errors.Is(err, errAcctFoldRange) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		return nil, mapAcctErr(ctx, "fold bank txn amounts", err)
	}

	var full *entity.AcctJournalEntryFull
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		id, _, txErr := rep.Accounting().CreateJournalEntry(ctx, entry)
		if txErr != nil {
			return txErr
		}
		if txErr = rep.Accounting().SetBankTxnPosted(ctx, txn.Id, id); txErr != nil {
			return txErr
		}
		full, txErr = rep.Accounting().GetJournalEntry(ctx, id)
		return txErr
	})
	if err != nil {
		// A dangling supplier_id trips the FK on insert — a bad request, not a server fault.
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Errorf(codes.InvalidArgument, "supplier_id %d does not exist", req.GetSupplierId())
		}
		return nil, mapAcctErr(ctx, "post bank txn", err)
	}
	return &pb_admin.PostBankTxnResponse{Entry: dto.ConvertAcctJournalEntryFullToPb(*full)}, nil
}

// IgnoreBankTxn marks a not-yet-posted inbox line ignored and persists the operator's reason —
// an ignored line books nothing, so the reason is its only trace (audit F-7).
func (s *Server) IgnoreBankTxn(ctx context.Context, req *pb_admin.IgnoreBankTxnRequest) (*pb_admin.IgnoreBankTxnResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	reason := strings.TrimSpace(req.GetReason())
	if len([]rune(reason)) > 255 { // rune count — the column is VARCHAR(255) characters, not bytes
		return nil, status.Error(codes.InvalidArgument, "reason must be at most 255 characters")
	}
	if err := s.repo.Accounting().SetBankTxnIgnored(ctx, int(req.GetId()), reason); err != nil {
		return nil, mapAcctErr(ctx, "ignore bank txn", err)
	}
	return &pb_admin.IgnoreBankTxnResponse{}, nil
}

// ListBankRules returns the substring→account suggestion rules.
func (s *Server) ListBankRules(ctx context.Context, _ *pb_admin.ListBankRulesRequest) (*pb_admin.ListBankRulesResponse, error) {
	rules, err := s.repo.Accounting().ListBankRules(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "list bank rules", err)
	}
	return &pb_admin.ListBankRulesResponse{Rules: dto.ConvertAcctBankRuleListToPb(rules)}, nil
}

// CreateBankRule adds a substring→account suggestion rule.
func (s *Server) CreateBankRule(ctx context.Context, req *pb_admin.CreateBankRuleRequest) (*pb_admin.CreateBankRuleResponse, error) {
	pattern := strings.TrimSpace(req.GetPattern())
	code := strings.ToUpper(strings.TrimSpace(req.GetAccountCode()))
	if pattern == "" {
		return nil, status.Error(codes.InvalidArgument, "pattern is required")
	}
	if code == "" {
		return nil, status.Error(codes.InvalidArgument, "account_code is required")
	}
	id, err := s.repo.Accounting().CreateBankRule(ctx, pattern, code)
	if err != nil {
		return nil, mapAcctErr(ctx, "create bank rule", err)
	}
	return &pb_admin.CreateBankRuleResponse{Rule: dto.ConvertAcctBankRuleToPb(entity.AcctBankRule{Id: id, Pattern: pattern, AccountCode: code})}, nil
}

// DeleteBankRule removes a suggestion rule.
func (s *Server) DeleteBankRule(ctx context.Context, req *pb_admin.DeleteBankRuleRequest) (*pb_admin.DeleteBankRuleResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Accounting().DeleteBankRule(ctx, int(req.GetId())); err != nil {
		return nil, mapAcctErr(ctx, "delete bank rule", err)
	}
	return &pb_admin.DeleteBankRuleResponse{}, nil
}

// CreateSupplier adds a supplier to the catalog.
func (s *Server) CreateSupplier(ctx context.Context, req *pb_admin.CreateSupplierRequest) (*pb_admin.CreateSupplierResponse, error) {
	ins, err := dto.ConvertPbCreateSupplier(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id, err := s.repo.Accounting().CreateSupplier(ctx, ins)
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Errorf(codes.InvalidArgument, "supplier %q already exists", ins.Name)
		}
		return nil, mapAcctErr(ctx, "create supplier", err)
	}
	return &pb_admin.CreateSupplierResponse{
		Supplier: dto.ConvertSupplierToPb(entity.Supplier{Id: id, Name: ins.Name, VatId: ins.VatId, Notes: ins.Notes, CreatedAt: s.repo.Now()}),
	}, nil
}

// ListSuppliers returns the supplier catalog.
func (s *Server) ListSuppliers(ctx context.Context, _ *pb_admin.ListSuppliersRequest) (*pb_admin.ListSuppliersResponse, error) {
	suppliers, err := s.repo.Accounting().ListSuppliers(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "list suppliers", err)
	}
	return &pb_admin.ListSuppliersResponse{Suppliers: dto.ConvertSupplierListToPb(suppliers)}, nil
}

// GetPayables returns the open Accounts-Payable position per supplier.
func (s *Server) GetPayables(ctx context.Context, _ *pb_admin.GetPayablesRequest) (*pb_admin.GetPayablesResponse, error) {
	rows, err := s.repo.Accounting().GetPayables(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "get payables", err)
	}
	return &pb_admin.GetPayablesResponse{Rows: dto.ConvertAcctPayableListToPb(rows)}, nil
}

// GetReceivables returns the open Accounts-Receivable position per bank-invoice order.
func (s *Server) GetReceivables(ctx context.Context, _ *pb_admin.GetReceivablesRequest) (*pb_admin.GetReceivablesResponse, error) {
	rows, err := s.repo.Accounting().GetReceivables(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "get receivables", err)
	}
	return &pb_admin.GetReceivablesResponse{Rows: dto.ConvertAcctReceivableListToPb(rows)}, nil
}

// acctAlertCentTolerance mirrors the reconciliation UI's "within a cent reads as matched" rule.
var acctAlertCentTolerance = decimal.NewFromFloat(0.01)

// GetAcctAlerts aggregates the accounting section's attention flags (the UI tab dots) from the
// same reads the individual tabs run — one request instead of six. Reads only. The bank backlog is
// capped at the list page size (500): the dot needs zero-vs-some, not an exact census.
func (s *Server) GetAcctAlerts(ctx context.Context, _ *pb_admin.GetAcctAlertsRequest) (*pb_admin.GetAcctAlertsResponse, error) {
	acc := s.repo.Accounting()
	var a entity.AcctAlerts

	pay, err := acc.GetPayables(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (payables)", err)
	}
	a.OpenPayables = len(pay)
	for _, r := range pay {
		if r.SupplierId == 0 || r.Balance.IsNegative() {
			a.ApAnomalies++
		}
	}

	rec, err := acc.GetReceivables(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (receivables)", err)
	}
	a.OpenReceivables = len(rec)

	periods, err := acc.ListPeriods(ctx)
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (periods)", err)
	}
	now := time.Now().UTC()
	curMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	for _, p := range periods {
		if p.Status == "open" && p.Period.Before(curMonth) {
			a.OpenPastMonths = append(a.OpenPastMonths, p.Period.Format("2006-01"))
		}
	}
	sort.Strings(a.OpenPastMonths)

	txns, err := acc.ListBankTxns(ctx, string(entity.AcctBankTxnUnmatched), 500)
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (bank)", err)
	}
	a.BankUnmatched = len(txns)

	events, err := acc.ListEventsNeedingReview(ctx, 100)
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (events)", err)
	}
	a.EventsNeedReview = len(events)

	// Current-month reconciliation, non-advisory blocks only: finished-goods drift is expected
	// (live cost vs sale-time snapshots) and pending / unposted movements have their own signals
	// (events dot, close checklist) — same legend the reconciliation tab uses.
	recon, err := acc.GetReconciliation(ctx, curMonth, curMonth.AddDate(0, 1, 0))
	if err != nil {
		return nil, mapAcctErr(ctx, "get acct alerts (reconciliation)", err)
	}
	watched := []struct {
		name  string
		block *entity.AcctReconBlock
	}{
		{"revenue", &recon.Revenue},
		{"fees", &recon.Fees},
		{"cogs", &recon.COGS},
		{"materials", &recon.Materials},
		{"vat", recon.Vat},
		{"prepayments", recon.Prepayments},
		{"shipping", recon.Shipping},
		{"bank", recon.Bank},
	}
	for _, w := range watched {
		if w.block == nil {
			continue
		}
		if w.block.Delta.Abs().Cmp(acctAlertCentTolerance) >= 0 {
			a.ReconMismatch = append(a.ReconMismatch, w.name)
		}
	}

	return dto.ConvertAcctAlertsToPb(a), nil
}
