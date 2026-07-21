package betaseed

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// SeedAccounting lights up the double-entry accounting surface of the beta admin with the only
// ledger data reachable over REST: deterministic MANUAL journal entries. The order_sale / opex_month
// / material postings are the accounting worker's job (derived from operational facts), so they are
// deliberately NOT fabricated here. This phase is self-contained — no catalog/plm dependency — so it
// runs standalone via `--only=accounting`.
//
// It posts a spread of balanced manual entries across revenue + opex + asset/liability (so Trial
// Balance, P&L and Balance Sheet all show non-zero rows), reverses one of them (reversal-not-edit),
// touches the period lifecycle, and finally reads GetTrialBalance / ListJournalEntries /
// GetAcctReconciliation back to PROVE the ledger is balanced, non-empty and queryable.
//
// The chart of accounts (34 system accounts) already exists from migration 0190 — this phase never
// creates accounts. It is idempotent/re-runnable: the ledger is append-only and CreateJournalEntry
// mints a fresh manual source_key server-side, so re-runs just append; s.Run rides in every memo so
// runs stay distinguishable.

// AccountingResult summarises what SeedAccounting posted and exercised, so a caller or a later verify
// phase can reconcile against the accounting screens.
type AccountingResult struct {
	JournalEntries int // manual entries posted via CreateJournalEntry
	Reversed       int // entries mirrored via ReverseJournalEntry
	Periods        int // accounting periods visible after posting
	Warnings       []string
}

func (r *AccountingResult) warn(s *Seeder, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	r.Warnings = append(r.Warnings, msg)
	s.logf("  WARN " + msg)
}

// acctEntry is one balanced manual entry template: a single debit account and a single credit
// account for the same EUR amount (Σdebit == Σcredit, exactly 2 lines).
type acctEntry struct {
	dr, cr string // debit account code / credit account code (chart-of-accounts codes)
	memo   string
	amount string // EUR, base currency
}

// SeedAccounting runs the manual-ledger + acceptance flow. It mirrors the soft-fail steps loop of
// SeedExtras: a failing step is logged and recorded on the result, and the remaining steps still run.
// The chart-resolve step is the one HARD-required step (an empty chart means migration 0190 never
// applied — worth surfacing loudly), so the phase returns an aggregate error only when a required
// step failed. The acceptance readback is the green gate: if the ledger does not read back balanced
// and non-empty, SeedAccounting returns an error.
func (s *Seeder) SeedAccounting(ctx context.Context) (*AccountingResult, error) {
	r := &AccountingResult{}

	// Shared across the soft-fail steps: the resolved chart of accounts (code -> account) and the ids
	// of the entries we mint (so the reversal step can mirror one). Captured by the step closures.
	accounts := map[string]*admin.AcctAccount{}
	var createdIDs []int32

	steps := []struct {
		name     string
		required bool
		fn       func(context.Context, *AccountingResult) error
	}{
		{"accounts", true, func(ctx context.Context, _ *AccountingResult) error { return s.resolveAcctAccounts(ctx, accounts) }},
		{"journal", false, func(ctx context.Context, r *AccountingResult) error {
			return s.seedJournalEntries(ctx, r, accounts, &createdIDs)
		}},
		{"reverse", false, func(ctx context.Context, r *AccountingResult) error { return s.reverseOneEntry(ctx, r, createdIDs) }},
		{"periods", false, func(ctx context.Context, r *AccountingResult) error { return s.touchAcctPeriods(ctx, r) }},
	}
	var failed []string
	for _, st := range steps {
		s.logf("=== SeedAccounting: %s ===", st.name)
		if err := st.fn(ctx, r); err != nil {
			s.logf("ERROR %s: %v", st.name, err)
			r.Warnings = append(r.Warnings, fmt.Sprintf("%s: %v", st.name, err))
			if st.required {
				failed = append(failed, st.name)
			}
		}
	}
	if len(failed) > 0 {
		return r, fmt.Errorf("SeedAccounting: %d required step(s) failed: %s", len(failed), strings.Join(failed, ", "))
	}

	// Acceptance gate (the green gate).
	s.logf("=== SeedAccounting: acceptance ===")
	if err := s.proveAccounting(ctx, r); err != nil {
		return r, err
	}

	// Best-effort read-back of the WORKER's operational postings + the VAT returns (never fails the
	// phase — those entries lag a full seed on the ~1m ticker).
	s.logf("=== SeedAccounting: operational coverage ===")
	s.verifyOperationalCoverage(ctx, r)
	return r, nil
}

// ---------------------------------------------------------------- 1. resolve chart of accounts

// resolveAcctAccounts loads the chart of accounts once into into (code -> account). An empty chart is
// a hard failure — it means migration 0190 (accounting seed CoA) did not apply on this environment.
func (s *Seeder) resolveAcctAccounts(ctx context.Context, into map[string]*admin.AcctAccount) error {
	resp, err := s.C.ListAcctAccounts(ctx, &admin.ListAcctAccountsRequest{})
	if err != nil {
		return fmt.Errorf("ListAcctAccounts: %w", err)
	}
	for _, a := range resp.GetAccounts() {
		into[a.GetCode()] = a
	}
	if len(into) == 0 {
		return fmt.Errorf("chart of accounts is empty — migration 0190 (accounting seed CoA) not applied on this env")
	}
	s.logf("  resolved %d accounting accounts", len(into))
	return nil
}

// acctPostable reports whether code is a known, non-archived account we can post against.
func (s *Seeder) acctPostable(accounts map[string]*admin.AcctAccount, code string) bool {
	a, ok := accounts[code]
	return ok && !a.GetArchived()
}

// ---------------------------------------------------------------- 2. manual journal entries

// seedJournalEntries posts a Volume-scaled spread of balanced MANUAL journal entries across revenue +
// opex + asset/liability, so Trial Balance, P&L and Balance Sheet all show non-zero rows. The admin
// RPC forces source_type=manual server-side and mints the source_key, so we only supply lines + memo.
// Every entry carries s.Run in its description for traceability; every entry has Σdebit == Σcredit and
// exactly 2 lines. Codes are real chart-of-accounts codes from migration 0190.
func (s *Seeder) seedJournalEntries(ctx context.Context, r *AccountingResult, accounts map[string]*admin.AcctAccount, createdIDs *[]int32) error {
	if len(accounts) == 0 {
		return fmt.Errorf("chart of accounts unresolved (resolve step failed)")
	}
	// occurred_at = today: inside the accounting window and never more than 1 day in the future; the
	// current month's period is lazily opened by this posting.
	occurred := time.Now().UTC().Format("2006-01-02")

	// Ordered so the first two already cover revenue + opex (single Volume posts the first two), the
	// first four add a liability, and the full set exercises asset/liability/revenue/cogs-less/opex.
	templates := []acctEntry{
		{"1030", "4020", "DTC web sale settled to Stripe", "480.00"}, // asset ↑ / revenue ↑
		{"6340", "1010", "monthly studio rent", "1800.00"},           // opex ↑ / asset ↓
		{"6350", "2030", "accrued accountant fees", "650.00"},        // opex ↑ / liability ↑
		{"2010", "1010", "supplier invoice paid down", "1240.00"},    // liability ↓ / asset ↓
		{"1010", "4010", "popup cash sale", "320.00"},                // asset ↑ / revenue ↑
		{"6050", "1030", "Stripe processing fees", "58.40"},          // opex ↑ / asset ↓
		{"6110", "1010", "paid social campaign spend", "410.00"},     // opex ↑ / asset ↓
		{"6060", "1010", "monthly bank account fee", "12.00"},        // opex ↑ / asset ↓
	}
	n := s.scaleN(2, 4, 8)
	if n > len(templates) {
		n = len(templates)
	}
	posted := 0
	for i := 0; i < n; i++ {
		t := templates[i]
		if !s.acctPostable(accounts, t.dr) || !s.acctPostable(accounts, t.cr) {
			r.warn(s, "journal entry %d: skipped — account %s or %s missing/archived on this env", i, t.dr, t.cr)
			continue
		}
		desc := fmt.Sprintf("%s (beta seed %s)", t.memo, s.Run)
		resp, err := s.C.CreateJournalEntry(ctx, &admin.CreateJournalEntryRequest{
			OccurredAt:  occurred,
			Description: desc,
			Lines: []*admin.AcctJournalLineInput{
				{AccountCode: t.dr, IsDebit: true, Amount: decv(t.amount), Note: desc},
				{AccountCode: t.cr, IsDebit: false, Amount: decv(t.amount), Note: desc},
			},
		})
		if err != nil {
			r.warn(s, "CreateJournalEntry(Dr %s / Cr %s €%s): %v", t.dr, t.cr, t.amount, err)
			continue
		}
		id := resp.GetEntry().GetId()
		*createdIDs = append(*createdIDs, id)
		r.JournalEntries++
		posted++
		s.logf("  journal entry #%d: Dr %s / Cr %s €%s — %s", id, t.dr, t.cr, t.amount, t.memo)
	}
	if posted == 0 {
		return fmt.Errorf("no manual journal entries posted (all %d templates skipped/failed)", n)
	}
	s.logf("  journal: posted %d/%d manual entries", posted, n)
	return nil
}

// ---------------------------------------------------------------- 3. reversal

// reverseOneEntry mirrors one entry we just created (reversal-not-edit: sides swapped, a new entry
// referencing the original). Reversal can legitimately be refused (already reversed, reversal-of-a-
// reversal), so a failure warns and never aborts the phase.
func (s *Seeder) reverseOneEntry(ctx context.Context, r *AccountingResult, createdIDs []int32) error {
	if len(createdIDs) == 0 {
		r.warn(s, "reverse: no created entry to reverse (journal step posted none)")
		return nil
	}
	id := createdIDs[0]
	resp, err := s.C.ReverseJournalEntry(ctx, &admin.ReverseJournalEntryRequest{
		EntryId: id,
		Reason:  "beta seed reversal — demonstrate reversal-not-edit (" + s.Run + ")",
	})
	if err != nil {
		r.warn(s, "ReverseJournalEntry(%d): %v (soft-skip; reversal can be refused)", id, err)
		return nil
	}
	r.Reversed++
	s.logf("  reversed entry #%d via new mirror entry #%d", id, resp.GetEntry().GetId())
	return nil
}

// ---------------------------------------------------------------- 4. period lifecycle

// touchAcctPeriods reads the accounting periods back (the current month is lazily opened by the
// journal step) and counts them, then attempts a lifecycle touch on a fully-past month. On beta
// ACCOUNTING_START_DATE=2026-07-20 means there is no fully-past in-window month yet, so CloseAcctPeriod
// is expected to soft-skip: it returns closed=false + not_ready (not an error) when it can't close.
// This step never fails the phase on the close.
func (s *Seeder) touchAcctPeriods(ctx context.Context, r *AccountingResult) error {
	lp, err := s.C.ListAcctPeriods(ctx, &admin.ListAcctPeriodsRequest{})
	if err != nil {
		return fmt.Errorf("ListAcctPeriods: %w", err)
	}
	periods := lp.GetPeriods()
	r.Periods = len(periods)
	open := 0
	for _, p := range periods {
		if p.GetStatus() == "open" {
			open++
		}
	}
	s.logf("  accounting periods: %d total (%d open) — the current month is lazily opened by posting", len(periods), open)

	month := time.Now().UTC().AddDate(0, -1, 0).Format("2006-01")
	cr, err := s.C.CloseAcctPeriod(ctx, &admin.CloseAcctPeriodRequest{Month: month})
	if err != nil {
		r.warn(s, "CloseAcctPeriod(%s): %v — soft-skip (no fully-past in-window month on beta yet)", month, err)
		return nil
	}
	if cr.GetClosed() {
		s.logf("  closed accounting period %s", month)
	} else {
		s.logf("  period %s not closable yet: %v (expected on beta — nothing to reconcile before the start date)", month, cr.GetNotReady())
	}
	return nil
}

// ---------------------------------------------------------------- 5. acceptance

// proveAccounting is the green gate. Over a window that covers the entries we just posted it reads
// GetTrialBalance (asserts Σdebit == Σcredit and at least one non-zero account), ListJournalEntries
// (asserts > 0 entries), and GetAcctReconciliation (must not error — its figures are informational on
// a manual-only ledger). A failed assertion returns an error, which SeedAccounting surfaces.
func (s *Seeder) proveAccounting(ctx context.Context, r *AccountingResult) error {
	// Entries are dated now; [from, to) is [now-60d, now+1d) so today's entries fall inside (to is
	// exclusive, occurred_at is a date).
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -60).Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")

	tb, err := s.C.GetTrialBalance(ctx, &admin.GetTrialBalanceRequest{From: from, To: to})
	if err != nil {
		return fmt.Errorf("GetTrialBalance: %w", err)
	}
	totalDebit := decFloat(tb.GetTotalDebit())
	totalCredit := decFloat(tb.GetTotalCredit())
	nonZero := 0
	for _, row := range tb.GetRows() {
		if decFloat(row.GetBalance()) != 0 || decFloat(row.GetDebit()) != 0 || decFloat(row.GetCredit()) != 0 {
			nonZero++
		}
	}

	lj, err := s.C.ListJournalEntries(ctx, &admin.ListJournalEntriesRequest{From: from, To: to, Limit: 500})
	if err != nil {
		return fmt.Errorf("ListJournalEntries: %w", err)
	}
	entries := len(lj.GetEntries())

	// Reconciliation must be queryable (its deltas are expected/non-zero on a manual-only ledger, so
	// we assert only that the RPC succeeds).
	if _, err := s.C.GetAcctReconciliation(ctx, &admin.GetAcctReconciliationRequest{From: from, To: to}); err != nil {
		return fmt.Errorf("GetAcctReconciliation: %w", err)
	}

	s.logf("  ---- ACCEPTANCE ----")
	s.logf("  GetTrialBalance[%s,%s): total_debit=%.2f total_credit=%.2f balanced=%v non-zero-rows=%d",
		from, to, totalDebit, totalCredit, tb.GetBalanced(), nonZero)
	s.logf("  ListJournalEntries: %d entries in range (of %d total)", entries, lj.GetTotal())

	// The gate: the ledger must be balanced (Σdebit == Σcredit), carry at least one non-zero account,
	// and hold at least one journal entry in the window.
	if !tb.GetBalanced() {
		return fmt.Errorf("acceptance FAILED: trial balance not balanced (total_debit=%.2f total_credit=%.2f)", totalDebit, totalCredit)
	}
	if nonZero == 0 {
		return fmt.Errorf("acceptance FAILED: trial balance has no non-zero account rows in [%s,%s)", from, to)
	}
	if entries == 0 {
		return fmt.Errorf("acceptance FAILED: ListJournalEntries returned 0 entries in [%s,%s)", from, to)
	}
	s.logf("  ACCEPTANCE PASSED: balanced ledger, %d non-zero accounts, %d journal entries in range", nonZero, entries)
	return nil
}

// ---------------------------------------------------------------- 6. operational coverage (worker)

// verifyOperationalCoverage is a best-effort read-back of the accounting WORKER's output. The
// acctposting worker posts order_sale / order_refund / opex_month / material_* / production_receive
// entries from the operational facts the other phases seed, on its ~1m ticker — so they lag a full
// seed rather than appearing synchronously with it. This step polls the ledger for a bounded window
// for the worker's (non-manual) entries, buckets them by source_type, then reads the JPK_VAT + OSS
// returns and logs per-regime coverage so a full seed leaves a visibly complete accounting picture.
//
// It NEVER fails the phase: on a standalone --only=accounting run (no orders) or a still-warming worker
// it simply logs what is (not yet) covered, with guidance to re-check shortly. The real gate stays the
// balanced-ledger acceptance above.
func (s *Seeder) verifyOperationalCoverage(ctx context.Context, r *AccountingResult) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -60).Format("2006-01-02")
	to := now.AddDate(0, 0, 1).Format("2006-01-02")

	// Poll (worker interval ~1m) until the worker's order_sale count STABILISES rather than breaking on
	// the first entry — the worker drains the order backlog over a couple of ticks, so an eager break
	// reports a partial picture and misses the late-created wdt/cash orders (their regimes would read 0).
	// Break when order_sale is non-zero and unchanged across two consecutive polls (worker drained), or
	// on the attempt cap (~150s).
	bySource := map[string]int{}
	const attempts = 10
	prevSales := -1
	for a := 0; a < attempts; a++ {
		lj, err := s.C.ListJournalEntries(ctx, &admin.ListJournalEntriesRequest{From: from, To: to, Limit: 2000})
		if err != nil {
			r.warn(s, "coverage: ListJournalEntries: %v", err)
			return
		}
		bySource = map[string]int{}
		for _, e := range lj.GetEntries() {
			st := e.GetSourceType()
			if st == "" {
				st = "(none)"
			}
			bySource[st]++
		}
		sales := bySource["order_sale"]
		if sales > 0 && sales == prevSales {
			break // held steady across two consecutive polls → the worker has drained the backlog
		}
		prevSales = sales
		if a < attempts-1 {
			s.logf("  coverage: order_sale=%d, waiting for the worker to drain… [%d/%d]", sales, a+1, attempts)
			time.Sleep(15 * time.Second)
		}
	}
	s.logf("  ledger entries by source_type: %s", fmtCounts(bySource))
	operational := 0
	for st, n := range bySource {
		if st != "manual" && st != "(none)" {
			operational += n
		}
	}
	if operational == 0 {
		r.warn(s, "coverage: no worker (operational) entries yet — order_sale/opex/material postings lag the seed (~1m); re-run `--only=accounting` shortly to confirm")
	}

	// JPK_VAT (this month): output VAT by regime (pl_domestic / uk_stock) + the wdt / export net bases —
	// non-zero values here prove the worker posted order_sale across the regimes the orders exercise.
	month := now.Format("2006-01-02")
	if v, err := s.C.GetVatReturnPL(ctx, &admin.GetVatReturnPLRequest{Month: month}); err != nil {
		r.warn(s, "coverage: GetVatReturnPL(%s): %v", month, err)
	} else {
		s.logf("  JPK_VAT[%s]: output_domestic=%.2f output_uk_stock=%.2f oss_info=%.2f net_wdt=%.2f net_export=%.2f net_payable=%.2f",
			month, decFloat(v.GetOutputDomestic()), decFloat(v.GetOutputUkStockDomestic()), decFloat(v.GetOssInfoTotal()),
			decFloat(v.GetNetWdt()), decFloat(v.GetNetExport()), decFloat(v.GetNetPayable()))
	}
	// OSS (this quarter): EU B2C destination VAT breakdown (vat_regime=oss).
	if o, err := s.C.GetOssReturn(ctx, &admin.GetOssReturnRequest{Quarter: month}); err != nil {
		r.warn(s, "coverage: GetOssReturn(%s): %v", month, err)
	} else {
		s.logf("  OSS[quarter of %s]: rows=%d total_net=%.2f total_vat=%.2f",
			month, len(o.GetRows()), decFloat(o.GetTotalNet()), decFloat(o.GetTotalVat()))
	}
}

// fmtCounts renders a source_type -> count map as a stable "k=v" list (sorted by key).
func fmtCounts(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}
