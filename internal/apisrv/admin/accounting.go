package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
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
		if errors.Is(err, errAcctNoFxRate) || errors.Is(err, errAcctFoldRange) {
			return nil, status.Error(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't fold journal line amounts to base", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load costing fx rates")
	}
	ins.SourceType = entity.AcctSourceManual
	ins.SourceKey = "manual:" + uuid.NewString()
	ins.CreatedBy = authsrv.GetAdminUsername(ctx)

	// AP discipline: a manual line on 2010 (Accounts Payable) must carry the supplier tag —
	// untagged 2010 movements pile up as an anonymous "(untagged)" row in GetPayables that nobody
	// can chase or pay down, which is the exact failure the AP-by-supplier subledger exists to
	// prevent. Automated postings stay exempt: a material receipt tags itself from the movement,
	// and a production run's 2010 credit has no single supplier to name (it aggregates the run's
	// manual costs) — those land in "(untagged)" by design until they get their own tagging.
	if !ins.SupplierID.Valid {
		for _, ln := range ins.Lines {
			if ln.AccountCode == acctrules.Acc2010 {
				return nil, status.Errorf(codes.InvalidArgument,
					"a line posts to %s Accounts Payable — supplier_id is required so the payable is tracked per supplier (pick one in ap / ar → suppliers, or create it there first)",
					acctrules.Acc2010)
			}
		}
	}

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
		// A dangling supplier_id trips the FK on insert — that is a bad request, not a server fault.
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Errorf(codes.InvalidArgument, "supplier_id %d does not exist", ins.SupplierID.Int64)
		}
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
	// Close in one SERIALIZABLE tx so the readiness gates and the close write are atomic — a concurrent
	// post landing between a gate check and the close can no longer slip a month shut with unprocessed
	// activity (D-2, store-contract TOCTOU). ErrAcctPeriodNotReady is a domain (non-retryable) error, so
	// Tx returns it unwrapped and the errors.Is below still matches.
	err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		return rep.Accounting().ClosePeriod(ctx, month, authsrv.GetAdminUsername(ctx))
	})
	if err != nil {
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
	// Wave-3 CONDITIONAL caveats (replacing the two former permanent phase-1 caveats): the structural
	// gaps they described are now closed by machinery (8010 Corporation Tax + 6030 actual shipping), so
	// each is surfaced only when it is actually present in the period (docs/plan-accounting-phase2/
	// 03-wave3-pnl-completion.md §3.1/§3.4). They still head the list; the store's own per-period
	// has_caveat count follows.
	var permanent []string
	if !acctPLSectionNonZero(pl, string(entity.AcctSectionTax)) {
		permanent = append(permanent, "pre-tax profit: no corporation tax (8010) posted for this period")
	}
	if acctPLAccountNonZero(pl, string(entity.AcctSectionRevenue), "4110") &&
		!acctPLAccountNonZero(pl, string(entity.AcctSectionOpex), "6030") {
		permanent = append(permanent, "carrier shipping cost not booked for this period (4110 shipping income has no 6030 expense pair)")
	}
	if len(permanent) > 0 {
		pl.Caveats = append(permanent, pl.Caveats...)
	}
	return dto.ConvertAcctProfitLossToPb(*pl), nil
}

// acctPLSectionNonZero reports whether a P&L section has any account row with a non-zero period total
// (used to decide whether the period actually carries tax — a wave-3 conditional caveat).
func acctPLSectionNonZero(pl *entity.AcctProfitLoss, section string) bool {
	for _, sec := range pl.Sections {
		if sec.Section != section {
			continue
		}
		for _, r := range sec.Rows {
			if !r.Total.IsZero() {
				return true
			}
		}
	}
	return false
}

// acctPLAccountNonZero reports whether a specific account code appears in a P&L section with a non-zero
// period total (used for the wave-3 conditional shipping caveat: 4110 income present, 6030 expense not).
func acctPLAccountNonZero(pl *entity.AcctProfitLoss, section, code string) bool {
	for _, sec := range pl.Sections {
		if sec.Section != section {
			continue
		}
		for _, r := range sec.Rows {
			if r.Code == code && !r.Total.IsZero() {
				return true
			}
		}
	}
	return false
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

// GetCashFlowStatement returns the indirect-method cash-flow statement over [from, to) (wave 5, §5.1).
func (s *Server) GetCashFlowStatement(ctx context.Context, req *pb_admin.GetCashFlowStatementRequest) (*pb_admin.GetCashFlowStatementResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	cf, err := s.repo.Accounting().GetCashFlowStatement(ctx, from, to)
	if err != nil {
		return nil, mapAcctErr(ctx, "get cash flow statement", err)
	}
	return dto.ConvertAcctCashFlowStatementToPb(*cf), nil
}

// GetFinancialHealth returns the financial-health ratio set over [from, to) (wave 5, §5.2).
func (s *Server) GetFinancialHealth(ctx context.Context, req *pb_admin.GetFinancialHealthRequest) (*pb_admin.GetFinancialHealthResponse, error) {
	from, to, err := dto.ParseAcctDateRange(req.GetFrom(), req.GetTo())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	fh, err := s.repo.Accounting().GetFinancialHealth(ctx, from, to)
	if err != nil {
		return nil, mapAcctErr(ctx, "get financial health", err)
	}
	return dto.ConvertAcctFinancialHealthToPb(*fh), nil
}

// --- FX folding for manual amount_src lines ---

// errAcctNoFxRate distinguishes "no costing FX rate for this currency" (InvalidArgument) from a
// genuine failure loading the rate table (Internal) inside foldJournalLinesToBase.
var errAcctNoFxRate = errors.New("accounting: no costing fx rate")

// errAcctFoldRange flags a folded amount that overflows the DECIMAL(12,2) column bound (D-3): a bad
// request (InvalidArgument), distinct from a rate-table load failure (Internal).
var errAcctFoldRange = errors.New("accounting: folded amount out of range")

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
		base, ferr := dto.FoldJournalLineAmountToBase(*fx, lines[i].AmountSrc.Decimal, lines[i].CurrencySrc.String)
		if errors.Is(ferr, dto.ErrNoFxRate) {
			return fmt.Errorf("%w: add %s costing fx rate first", errAcctNoFxRate, lines[i].CurrencySrc.String)
		}
		if ferr != nil {
			// D-3: an out-of-range folded amount is a bad request, not an Internal — flag it so the
			// caller returns InvalidArgument with the reason instead of an opaque store overflow.
			return fmt.Errorf("%w: account %s: %v", errAcctFoldRange, lines[i].AccountCode, ferr)
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

// ListAcctEventsNeedingReview lists posting-outbox events flagged for manual review (H-1/H-2/B-5) —
// the dead-letter / manual-entry queue an operator must clear before the affected months can close.
func (s *Server) ListAcctEventsNeedingReview(ctx context.Context, req *pb_admin.ListAcctEventsNeedingReviewRequest) (*pb_admin.ListAcctEventsNeedingReviewResponse, error) {
	events, err := s.repo.Accounting().ListEventsNeedingReview(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, mapAcctErr(ctx, "list events needing review", err)
	}
	return &pb_admin.ListAcctEventsNeedingReviewResponse{Events: dto.ConvertAcctEventsToPb(events)}, nil
}

// ReprocessAcctEvent resets an event so the posting worker re-attempts it from scratch (used after the
// operator fixed the cause, e.g. added the missing vat_rate).
func (s *Server) ReprocessAcctEvent(ctx context.Context, req *pb_admin.ReprocessAcctEventRequest) (*pb_admin.ReprocessAcctEventResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Accounting().ReprocessAcctEvent(ctx, req.GetId()); err != nil {
		return nil, mapAcctErr(ctx, "reprocess acct event", err)
	}
	return &pb_admin.ReprocessAcctEventResponse{}, nil
}

// ResolveAcctEvent clears the review flag on an event handled manually (a manual journal entry posted).
func (s *Server) ResolveAcctEvent(ctx context.Context, req *pb_admin.ResolveAcctEventRequest) (*pb_admin.ResolveAcctEventResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.Accounting().ResolveAcctEvent(ctx, req.GetId()); err != nil {
		return nil, mapAcctErr(ctx, "resolve acct event", err)
	}
	return &pb_admin.ResolveAcctEventResponse{}, nil
}
