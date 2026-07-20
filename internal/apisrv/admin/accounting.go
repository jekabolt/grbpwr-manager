package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Step 6 (admin API) handlers for the double-entry accounting module (docs/plan-accounting/
// 05-admin-api.md). Pattern: validate (dto) → store → convert (dto) → respond, mirroring
// internal/apisrv/admin/inventory.go. Journal mutations (CreateJournalEntry/ReverseJournalEntry)
// run inside s.repo.Tx because they are multi-row writes (entry header + lines) and the store
// deliberately never opens its own transaction (02-store-layer.md); every other write here is a
// single store call and needs no wrapper. created_by/closed_by is always
// authsrv.GetAdminUsername(ctx), matching inventory.go's admin_username extraction.

// --- chart of accounts ---

// ListAcctAccounts returns the chart of accounts.
func (s *Server) ListAcctAccounts(ctx context.Context, req *pb_admin.ListAcctAccountsRequest) (*pb_admin.ListAcctAccountsResponse, error) {
	accts, err := s.repo.Accounting().ListAccounts(ctx, req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list accounting accounts", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list accounting accounts")
	}
	return &pb_admin.ListAcctAccountsResponse{Accounts: dto.ConvertAcctAccountListToPb(accts)}, nil
}

// CreateAcctAccount adds a custom (non-system) chart-of-accounts entry.
func (s *Server) CreateAcctAccount(ctx context.Context, req *pb_admin.CreateAcctAccountRequest) (*pb_admin.CreateAcctAccountResponse, error) {
	ins, err := dto.ConvertPbCreateAcctAccount(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	id, err := s.repo.Accounting().CreateAccount(ctx, ins)
	if err != nil {
		if s.repo.IsErrUniqueViolation(err) {
			return nil, status.Errorf(codes.InvalidArgument, "account code %q already exists", ins.Code)
		}
		slog.Default().ErrorContext(ctx, "can't create accounting account", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create accounting account")
	}
	// A freshly inserted custom account is always is_system=false, archived=false (the store's
	// INSERT hard-codes both to FALSE), so building the row from what we already know is exact —
	// no need for the findAcctAccount re-fetch UpdateAcctAccount uses.
	acct := entity.AcctAccount{Id: id, Code: ins.Code, Name: ins.Name, Section: ins.Section, Statement: ins.Statement}
	return &pb_admin.CreateAcctAccountResponse{Account: dto.ConvertAcctAccountToPb(acct)}, nil
}

// UpdateAcctAccount renames a custom (non-system) account; code and section are immutable.
func (s *Server) UpdateAcctAccount(ctx context.Context, req *pb_admin.UpdateAcctAccountRequest) (*pb_admin.UpdateAcctAccountResponse, error) {
	code, name, err := dto.ConvertPbUpdateAcctAccount(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.Accounting().UpdateAccountName(ctx, code, name); err != nil {
		return nil, mapAcctErr(ctx, "update accounting account", err)
	}
	acct, err := s.findAcctAccount(ctx, code)
	if err != nil {
		return nil, mapAcctErr(ctx, "reload accounting account", err)
	}
	return &pb_admin.UpdateAcctAccountResponse{Account: dto.ConvertAcctAccountToPb(*acct)}, nil
}

// ArchiveAcctAccount archives/unarchives a custom (non-system) account.
func (s *Server) ArchiveAcctAccount(ctx context.Context, req *pb_admin.ArchiveAcctAccountRequest) (*pb_admin.ArchiveAcctAccountResponse, error) {
	code := strings.ToUpper(strings.TrimSpace(req.GetCode()))
	if code == "" {
		return nil, status.Error(codes.InvalidArgument, "code is required")
	}
	if err := s.repo.Accounting().SetAccountArchived(ctx, code, req.GetArchived()); err != nil {
		return nil, mapAcctErr(ctx, "archive accounting account", err)
	}
	return &pb_admin.ArchiveAcctAccountResponse{}, nil
}

// findAcctAccount looks up one account by code via ListAccounts — dependency.Accounting has no
// single-account getter, only the list. Used by UpdateAcctAccount to return the row it just wrote.
func (s *Server) findAcctAccount(ctx context.Context, code string) (*entity.AcctAccount, error) {
	accts, err := s.repo.Accounting().ListAccounts(ctx, true)
	if err != nil {
		return nil, err
	}
	for i := range accts {
		if accts[i].Code == code {
			return &accts[i], nil
		}
	}
	return nil, fmt.Errorf("%w: %s", entity.ErrAcctUnknownAccount, code)
}

// --- journal ---

// CreateJournalEntry posts a manual double-entry entry. Lines that supplied amount_src+
// currency_src instead of a base-currency amount are folded to EUR here (via the costing FX
// rates) before the entry reaches the store, which never touches FX. The entry header + lines are
// written inside one Tx (05-admin-api.md's Tx snippet) since CreateJournalEntry never opens its
// own transaction.
func (s *Server) CreateJournalEntry(ctx context.Context, req *pb_admin.CreateJournalEntryRequest) (*pb_admin.CreateJournalEntryResponse, error) {
	ins, err := dto.ConvertPbCreateJournalEntry(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.foldJournalLinesToBase(ctx, ins.Lines); err != nil {
		if errors.Is(err, errAcctNoFxRate) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't fold journal line amounts to base", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load costing fx rates")
	}
	ins.SourceType = entity.AcctSourceManual
	ins.SourceKey = "manual:" + uuid.NewString()
	ins.CreatedBy = authsrv.GetAdminUsername(ctx)

	var full *entity.AcctJournalEntryFull
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		id, _, txErr := rep.Accounting().CreateJournalEntry(ctx, ins)
		if txErr != nil {
			return txErr
		}
		full, txErr = rep.Accounting().GetJournalEntry(ctx, id)
		return txErr
	})
	if err != nil {
		return nil, mapAcctErr(ctx, "create journal entry", err)
	}
	return &pb_admin.CreateJournalEntryResponse{Entry: dto.ConvertAcctJournalEntryFullToPb(*full)}, nil
}

// ReverseJournalEntry posts the mirror (sides swapped) of an existing entry inside one Tx, then
// reloads it with its lines for the response.
func (s *Server) ReverseJournalEntry(ctx context.Context, req *pb_admin.ReverseJournalEntryRequest) (*pb_admin.ReverseJournalEntryResponse, error) {
	if req.GetEntryId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "entry_id is required")
	}
	adminUsername := authsrv.GetAdminUsername(ctx)

	var full *entity.AcctJournalEntryFull
	err := s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		newID, txErr := rep.Accounting().ReverseJournalEntry(ctx, int(req.GetEntryId()), req.GetReason(), adminUsername)
		if txErr != nil {
			return txErr
		}
		full, txErr = rep.Accounting().GetJournalEntry(ctx, newID)
		return txErr
	})
	if err != nil {
		return nil, mapAcctErr(ctx, "reverse journal entry", err)
	}
	return &pb_admin.ReverseJournalEntryResponse{Entry: dto.ConvertAcctJournalEntryFullToPb(*full)}, nil
}

// ListJournalEntries returns a page of journal-entry headers (no lines) matching the filter.
func (s *Server) ListJournalEntries(ctx context.Context, req *pb_admin.ListJournalEntriesRequest) (*pb_admin.ListJournalEntriesResponse, error) {
	f, err := dto.ConvertPbAcctEntryFilter(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	entries, total, err := s.repo.Accounting().ListJournalEntries(ctx, f)
	if err != nil {
		return nil, mapAcctErr(ctx, "list journal entries", err)
	}
	return &pb_admin.ListJournalEntriesResponse{Entries: dto.ConvertAcctJournalEntryListToPb(entries), Total: int32(total)}, nil
}

// GetJournalEntry returns one entry with its lines.
func (s *Server) GetJournalEntry(ctx context.Context, req *pb_admin.GetJournalEntryRequest) (*pb_admin.GetJournalEntryResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	full, err := s.repo.Accounting().GetJournalEntry(ctx, int(req.GetId()))
	if err != nil {
		return nil, mapAcctErr(ctx, "get journal entry", err)
	}
	return &pb_admin.GetJournalEntryResponse{Entry: dto.ConvertAcctJournalEntryFullToPb(*full)}, nil
}

// --- periods ---

// ListAcctPeriods returns every accounting period, newest first.
func (s *Server) ListAcctPeriods(ctx context.Context, _ *pb_admin.ListAcctPeriodsRequest) (*pb_admin.ListAcctPeriodsResponse, error) {
	periods, err := s.repo.Accounting().ListPeriods(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list accounting periods", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list accounting periods")
	}
	return &pb_admin.ListAcctPeriodsResponse{Periods: dto.ConvertAcctPeriodListToPb(periods)}, nil
}

// CloseAcctPeriod closes a fully-past, reconciled month. A failed readiness check
// (ErrAcctPeriodNotReady) is deliberately NOT a gRPC error: it comes back as closed=false with the
// reason in not_ready so the UI can render a checklist (05-admin-api.md). The store's ClosePeriod
// returns exactly one reason per call (it fails fast on the first unmet condition), so not_ready
// always holds a single element here.
func (s *Server) CloseAcctPeriod(ctx context.Context, req *pb_admin.CloseAcctPeriodRequest) (*pb_admin.CloseAcctPeriodResponse, error) {
	month, err := dto.ParseAcctMonth(req.GetMonth())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.Accounting().ClosePeriod(ctx, month, authsrv.GetAdminUsername(ctx)); err != nil {
		if errors.Is(err, entity.ErrAcctPeriodNotReady) {
			return &pb_admin.CloseAcctPeriodResponse{Closed: false, NotReady: []string{err.Error()}}, nil
		}
		return nil, mapAcctErr(ctx, "close accounting period", err)
	}
	return &pb_admin.CloseAcctPeriodResponse{Closed: true}, nil
}

// ReopenAcctPeriod re-opens a closed month.
func (s *Server) ReopenAcctPeriod(ctx context.Context, req *pb_admin.ReopenAcctPeriodRequest) (*pb_admin.ReopenAcctPeriodResponse, error) {
	month, err := dto.ParseAcctMonth(req.GetMonth())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.Accounting().ReopenPeriod(ctx, month, authsrv.GetAdminUsername(ctx)); err != nil {
		return nil, mapAcctErr(ctx, "reopen accounting period", err)
	}
	return &pb_admin.ReopenAcctPeriodResponse{}, nil
}

// --- reports (docs/plan-accounting/06-reports.md) ---
//
// The five handlers below call the real store methods, implemented in step 7
// (internal/store/accounting/reports.go + reconcile.go). Any unexpected store error falls through
// mapAcctErr to a logged Internal.

// GetTrialBalance returns per-account turnover + closing balance over [from, to).
func (s *Server) GetTrialBalance(ctx context.Context, req *pb_admin.GetTrialBalanceRequest) (*pb_admin.GetTrialBalanceResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	tb, err := s.repo.Accounting().GetTrialBalance(ctx, from, to)
	if err != nil {
		return nil, mapAcctErr(ctx, "get trial balance", err)
	}
	return dto.ConvertAcctTrialBalanceToPb(*tb), nil
}

// GetProfitLossStatement returns the monthly Income Statement over [from, to).
func (s *Server) GetProfitLossStatement(ctx context.Context, req *pb_admin.GetProfitLossStatementRequest) (*pb_admin.GetProfitLossStatementResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	pl, err := s.repo.Accounting().GetProfitLoss(ctx, from, to)
	if err != nil {
		return nil, mapAcctErr(ctx, "get profit and loss statement", err)
	}
	// Two permanent phase-1 caveats, injected here so they always head the list regardless of what
	// the store computed (docs/plan-accounting/06-reports.md; wording mirrors the P&L response's proto
	// doc-comment). These are the deliberate, structural gaps of the phase-1 P&L — the store's own
	// caveats (the per-period has_caveat count) follow.
	pl.Caveats = append([]string{
		"pre-tax profit (no corporate tax accrual)",
		"carrier shipping cost not booked (4110 has no 6030 expense pair yet)",
	}, pl.Caveats...)
	return dto.ConvertAcctProfitLossToPb(*pl), nil
}

// GetBalanceSheet returns assets/liabilities/equity balances from inception through as_of.
func (s *Server) GetBalanceSheet(ctx context.Context, req *pb_admin.GetBalanceSheetRequest) (*pb_admin.GetBalanceSheetResponse, error) {
	asOf, err := dto.ParseAcctAsOf(req.GetAsOf())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	bs, err := s.repo.Accounting().GetBalanceSheet(ctx, asOf)
	if err != nil {
		return nil, mapAcctErr(ctx, "get balance sheet", err)
	}
	return dto.ConvertAcctBalanceSheetToPb(*bs), nil
}

// GetAccountLedger is the drill-down: a paginated statement for one account with a running balance.
func (s *Server) GetAccountLedger(ctx context.Context, req *pb_admin.GetAccountLedgerRequest) (*pb_admin.GetAccountLedgerResponse, error) {
	code, f, err := dto.ConvertPbAcctLedgerFilter(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ledger, err := s.repo.Accounting().GetAccountLedger(ctx, code, f)
	if err != nil {
		return nil, mapAcctErr(ctx, "get account ledger", err)
	}
	return dto.ConvertAcctAccountLedgerToPb(*ledger), nil
}

// GetAcctReconciliation proves the derived ledger matches operational truth over [from, to).
func (s *Server) GetAcctReconciliation(ctx context.Context, req *pb_admin.GetAcctReconciliationRequest) (*pb_admin.GetAcctReconciliationResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	rec, err := s.repo.Accounting().GetReconciliation(ctx, from, to)
	if err != nil {
		return nil, mapAcctErr(ctx, "get accounting reconciliation", err)
	}
	return dto.ConvertAcctReconciliationToPb(*rec), nil
}

// --- FX folding for manual amount_src lines ---

// errAcctNoFxRate distinguishes "no costing FX rate for this currency" (InvalidArgument) from a
// genuine failure loading the rate table (Internal) inside foldJournalLinesToBase.
var errAcctNoFxRate = errors.New("accounting: no costing fx rate")

// acctFxToBase loads the effective manual FX rates (as-of today — 09-implementation-notes.md FAQ
// 17: not historical as-of occurred_at, a deliberate phase-1 simplification) for converting a
// manual journal line's amount_src into the base currency. Unlike Server.costingFx (techcard.go),
// which degrades to no rates on a load failure, this surfaces the error: a foreign-currency line
// here must not silently masquerade as "no rate configured".
func (s *Server) acctFxToBase(ctx context.Context) (dto.CostingFx, error) {
	rates, err := s.repo.TechCards().GetCostingFxRatesToBase(ctx)
	if err != nil {
		return dto.CostingFx{}, fmt.Errorf("load costing fx rates: %w", err)
	}
	return dto.CostingFx{ToBase: rates, Base: cache.GetBaseCurrency()}, nil
}

// foldJournalLinesToBase fills Amount on every line that supplied amount_src+currency_src instead
// (dto leaves Amount at its zero value on those), converting via the costing FX rates. The rate
// table is loaded at most once, and only if at least one line needs it.
func (s *Server) foldJournalLinesToBase(ctx context.Context, lines []entity.AcctJournalLineInsert) error {
	var fx *dto.CostingFx
	for i := range lines {
		if !lines[i].AmountSrc.Valid {
			continue
		}
		if fx == nil {
			loaded, err := s.acctFxToBase(ctx)
			if err != nil {
				return err
			}
			fx = &loaded
		}
		base, ok := dto.FoldJournalLineAmountToBase(*fx, lines[i].AmountSrc.Decimal, lines[i].CurrencySrc.String)
		if !ok {
			return fmt.Errorf("%w: add %s costing fx rate first", errAcctNoFxRate, lines[i].CurrencySrc.String)
		}
		lines[i].Amount = base
	}
	return nil
}

// --- error mapping ---

// mapAcctErr maps accounting store errors to gRPC codes per docs/plan-accounting/05-admin-api.md's
// table. ErrAcctArchivedAccount and ErrAcctCannotReverseReversal are not explicitly listed in that
// table; they are classified here by analogy with their listed siblings (ErrAcctUnknownAccount and
// ErrAcctAlreadyReversed respectively) since they are the same family of guard. Anything else is a
// logged Internal — deliberately not special-cased, never a fabricated Unimplemented.
func mapAcctErr(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, entity.ErrAcctUnbalanced),
		errors.Is(err, entity.ErrAcctUnknownAccount),
		errors.Is(err, entity.ErrAcctArchivedAccount):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, entity.ErrAcctPeriodClosed),
		errors.Is(err, entity.ErrAcctPeriodNotReady),
		errors.Is(err, entity.ErrAcctAlreadyReversed),
		errors.Is(err, entity.ErrAcctCannotReverseReversal),
		errors.Is(err, entity.ErrAcctSystemAccount):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "accounting: not found")
	}
	slog.Default().ErrorContext(ctx, "can't "+what, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+what)
}
