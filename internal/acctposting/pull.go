package acctposting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Pull-source checkpoint names (acct_checkpoint.source).
const (
	checkpointMaterialMovement = "material_movement"
	checkpointOpexLine         = "opex_line"
	// checkpointShipmentCost is the timestamp cursor for the wave-3 shipping_actual pull (3.1). Dev
	// expenses (3.2) have no checkpoint — that source is a full reconcile scan each tick.
	checkpointShipmentCost = "shipment_cost"
)

// caveatMaxLen bounds the caveat column (VARCHAR(512)) so an appended note cannot overflow it.
const caveatMaxLen = 512

// processMovements is phase 2: scan material movements after the checkpoint on the pool, build one
// entry per movement (M1–M8), and write the whole batch + the advanced checkpoint in ONE short Tx.
// Uncosted / unbuildable movements are skipped but still advance the cursor ("skipped deliberately" ≠
// "unprocessed"). A movement backdated into a closed period is clamped to the current month with a
// caveat, so the append-ordered queue behind it never stalls (docs/plan-accounting/03, FAQ 25).
func (w *Worker) processMovements(ctx context.Context) error {
	acc := w.repo.Accounting()

	cp, err := acc.GetCheckpoint(ctx, checkpointMaterialMovement)
	if err != nil {
		return fmt.Errorf("get movement checkpoint: %w", err)
	}
	lastID := int64(0)
	if cp.LastId.Valid {
		lastID = cp.LastId.Int64
	}

	movements, err := acc.ListUnpostedMovements(ctx, lastID, w.startDate, w.c.BatchSize)
	if err != nil {
		return fmt.Errorf("list movements: %w", err)
	}
	if len(movements) == 0 {
		return nil
	}

	// clampTo is the first of the current month — always an open period (ClosePeriod refuses to close
	// the current or a future month), so the backdated-movement clamp retry below always lands.
	clampTo := firstOfMonthUTC(w.repo.Now().UTC())

	// Post each built movement entry in its OWN short Tx (B-3): a single poison movement (a non-period
	// post error) must not roll back and re-scan the whole batch. The checkpoint advances to the highest
	// movement id that was either posted OR intentionally skipped at build time, and STOPS at the first
	// movement that FAILED to post — so that one retries next tick while its later siblings still post
	// (idempotently), instead of one bad row clogging the queue and every future close. Source tables
	// are not read inside the Tx (the lock rule); idempotency makes a crash-and-replay a no-op.
	checkpointID := lastID
	postFailed := false
	for _, m := range movements {
		mid := int64(m.Id)
		entry, berr := accounting.BuildMaterialMovementEntry(m, w.startDate)
		if berr != nil {
			switch {
			case errors.Is(berr, accounting.ErrSkipUncosted):
				slog.Default().DebugContext(ctx, "acctposting: skip uncosted movement", slog.Int("movement_id", m.Id))
			default:
				// ErrUnknownMovementType or an unexpected builder error: skip; the movement is surfaced
				// via reconciliation.
				slog.Default().ErrorContext(ctx, "acctposting: skip unbuildable movement",
					slog.Int("movement_id", m.Id), slog.String("err", berr.Error()))
			}
			if !postFailed {
				checkpointID = mid // an intentional skip does not block the cursor
			}
			continue
		}
		e := entry
		if txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			return createMovementEntry(ctx, rep, &e, clampTo)
		}); txErr != nil {
			// Poison movement (non-period post error): log + alert; do NOT advance past it (retried next
			// tick), but keep posting the rest so one bad row cannot clog the queue and every close.
			slog.Default().ErrorContext(ctx, "acctposting: skip unpostable movement",
				slog.Int("movement_id", m.Id), slog.String("err", txErr.Error()))
			postFailed = true
			continue
		}
		if !postFailed {
			checkpointID = mid
		}
	}

	if checkpointID > lastID {
		if err := acc.SetCheckpoint(ctx, checkpointMaterialMovement,
			sql.NullInt64{Int64: checkpointID, Valid: true}, sql.NullTime{}); err != nil {
			return fmt.Errorf("advance movement checkpoint: %w", err)
		}
	}
	return nil
}

// createMovementEntry posts one movement entry, clamping a backdated occurred_at that lands in a
// closed period up to the current (open) month and flagging it, then retrying once. The entry is
// mutated in place, which is safe under Tx retry: a re-run finds occurred_at already clamped (an open
// period), so it does not re-enter the clamp branch, and appendCaveat de-dupes the note.
func createMovementEntry(ctx context.Context, rep dependency.Repository, entry *entity.AcctJournalEntryInsert, clampTo time.Time) error {
	_, _, err := rep.Accounting().CreateJournalEntry(ctx, *entry)
	if err == nil {
		return nil
	}
	if !errors.Is(err, entity.ErrAcctPeriodClosed) {
		return err
	}
	entry.OccurredAt = clampTo
	appendCaveat(entry, "backdated movement")
	_, _, err = rep.Accounting().CreateJournalEntry(ctx, *entry)
	return err
}

// processRuns is phase 3: scan received production runs without a receive entry (idempotency IS the
// checkpoint) and post each (P1) in its own short Tx. A run with nothing costed to post (ErrSkipEmpty)
// is left unposted and re-seen each tick — accepted, runs are few (docs/plan-accounting/07). Per-run
// errors are logged and skipped so one bad run neither stalls the others nor fails the tick; only the
// list read is a phase-level error.
func (w *Worker) processRuns(ctx context.Context) error {
	acc := w.repo.Accounting()

	ids, err := acc.ListUnpostedReceivedRuns(ctx, w.startDate, w.c.BatchSize)
	if err != nil {
		return fmt.Errorf("list unposted received runs: %w", err)
	}

	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		facts, err := acc.GetRunFactsForPosting(ctx, id)
		if err != nil {
			slog.Default().ErrorContext(ctx, "acctposting: get run facts",
				slog.Int("run_id", id), slog.String("err", err.Error()))
			continue
		}
		entry, berr := accounting.BuildProductionReceiveEntry(*facts, w.startDate)
		if berr != nil {
			if errors.Is(berr, accounting.ErrSkipEmpty) {
				slog.Default().DebugContext(ctx, "acctposting: production run has nothing to post", slog.Int("run_id", id))
				continue
			}
			slog.Default().ErrorContext(ctx, "acctposting: build run receive",
				slog.Int("run_id", id), slog.String("err", berr.Error()))
			continue
		}
		txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			_, _, e := rep.Accounting().CreateJournalEntry(ctx, entry)
			return e
		})
		if txErr != nil {
			// A closed period (rare — received_at is ~now) or any other posting error: log and continue.
			// The run stays unposted (NOT EXISTS) and retries on the next tick / after a reopen.
			slog.Default().ErrorContext(ctx, "acctposting: post run receive",
				slog.Int("run_id", id), slog.String("err", txErr.Error()))
			continue
		}
	}
	return nil
}

// processOpex is phase 4: OPEX is mutable (upsert/edit/delete), so the granule is the month and any
// change re-posts it (reverse + create a new version). It advances a timestamp checkpoint captured
// BEFORE the scan so a row changed during/after the tick is re-seen (at worst re-processed to a
// no-op), never missed. A month whose period is closed is warned and skipped (an abnormal edit,
// resolved by reopen); an infra error on a month fails the phase without advancing the checkpoint.
func (w *Worker) processOpex(ctx context.Context) error {
	acc := w.repo.Accounting()

	cp, err := acc.GetCheckpoint(ctx, checkpointOpexLine)
	if err != nil {
		return fmt.Errorf("get opex checkpoint: %w", err)
	}
	lastTS := w.startDate
	if cp.LastTs.Valid {
		lastTS = cp.LastTs.Time
	}

	// Capture the horizon before listing: any opex_line updated at/after this instant stays > lastTS
	// on the next tick (re-processed to a no-op), so nothing between the scan and SetCheckpoint is lost.
	tickStart := w.repo.Now().UTC()

	months, err := acc.ListChangedOpexMonths(ctx, lastTS)
	if err != nil {
		return fmt.Errorf("list changed opex months: %w", err)
	}

	startMonth := firstOfMonthUTC(w.startDate)
	for _, month := range months {
		if err := ctx.Err(); err != nil {
			return err
		}
		m := firstOfMonthUTC(month)
		if m.Before(startMonth) {
			// Pre-cutover months are ignored even when their rows are edited (FAQ 27) — their expenses
			// are not in the ledger by the "start from zero" design.
			continue
		}
		if err := w.repostOpexMonth(ctx, m); err != nil {
			// Infrastructure error: do not advance the checkpoint (retry the batch next tick). Months
			// already processed this tick are safely re-processed (they compare equal → no-op).
			return err
		}
	}

	// B-4: store the checkpoint a small margin BEFORE the scan instant. opex_line.updated_at is second-
	// granular and the list query uses a strict `>`, so an edit landing in the SAME second as tickStart
	// (after the scan ran) would be missed forever; the margin re-scans that boundary window next tick,
	// deduped to a no-op by repostOpexMonth for an unchanged month.
	const opexCheckpointMargin = 2 * time.Second
	if err := acc.SetCheckpoint(ctx, checkpointOpexLine, sql.NullInt64{},
		sql.NullTime{Time: tickStart.Add(-opexCheckpointMargin), Valid: true}); err != nil {
		return fmt.Errorf("set opex checkpoint: %w", err)
	}
	return nil
}

// repostOpexMonth reconciles one month's OPEX entry with its current costed lines (O1). It returns an
// error only for infrastructure failures; a closed-period skip and a no-op are handled internally.
func (w *Worker) repostOpexMonth(ctx context.Context, month time.Time) error {
	acc := w.repo.Accounting()

	sums, err := acc.GetOpexMonthFacts(ctx, month)
	if err != nil {
		return fmt.Errorf("opex facts %s: %w", month.Format("2006-01"), err)
	}

	active, versionCount, err := w.loadOpexVersions(ctx, month)
	if err != nil {
		return fmt.Errorf("load opex versions %s: %w", month.Format("2006-01"), err)
	}

	// No entry yet: create v1 unless the month has nothing costed to post.
	if active == nil {
		entry, berr := accounting.BuildOpexMonthEntry(month, sums, 1)
		if errors.Is(berr, accounting.ErrSkipEmpty) {
			return nil
		}
		if berr != nil {
			return fmt.Errorf("build opex %s: %w", month.Format("2006-01"), berr)
		}
		return w.commitOpex(ctx, month, 0, entry)
	}

	// An entry exists. Build the candidate next version; an emptied month reverses the active entry
	// and creates nothing (FAQ 20).
	newVersion := versionCount + 1
	entry, berr := accounting.BuildOpexMonthEntry(month, sums, newVersion)
	if errors.Is(berr, accounting.ErrSkipEmpty) {
		return w.reverseOpex(ctx, month, active.Id)
	}
	if berr != nil {
		return fmt.Errorf("build opex %s: %w", month.Format("2006-01"), berr)
	}

	full, err := acc.GetJournalEntry(ctx, active.Id)
	if err != nil {
		return fmt.Errorf("load active opex entry %d: %w", active.Id, err)
	}
	if sameOpexLines(full.Lines, entry.Lines) {
		return nil // unchanged — no-op
	}
	return w.commitOpex(ctx, month, active.Id, entry)
}

// loadOpexVersions returns the active (un-reversed) opex_month entry for the month, if any, and the
// total count of that month's versions (reversed or not) — the new version number is count+1
// (docs/plan-accounting/09 FAQ 29). It filters ListJournalEntries to opex_month over the month's
// occurred_at window (opex occurred_at is the month-end, always inside it) and matches the 'YYYY-MM'
// source_key prefix in Go. A single month never accumulates enough versions to page.
func (w *Worker) loadOpexVersions(ctx context.Context, month time.Time) (*entity.AcctJournalEntry, int, error) {
	prefix := month.Format("2006-01")
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)

	entries, _, err := w.repo.Accounting().ListJournalEntries(ctx, entity.AcctEntryFilter{
		From:       from,
		To:         to,
		SourceType: entity.AcctSourceOpexMonth,
		Limit:      500,
	})
	if err != nil {
		return nil, 0, err
	}

	var active *entity.AcctJournalEntry
	count := 0
	for i := range entries {
		e := entries[i]
		if !strings.HasPrefix(e.SourceKey, prefix) {
			continue
		}
		count++
		if !e.ReversedBy.Valid {
			ecopy := e
			active = &ecopy
		}
	}
	return active, count, nil
}

// commitOpex writes an opex repost in one short Tx: reverse the prior version (when reverseID != 0),
// then create the new one. A closed period rolls the whole Tx back and is warned + skipped (the
// active entry is left in place); any other error is returned.
func (w *Worker) commitOpex(ctx context.Context, month time.Time, reverseID int, entry entity.AcctJournalEntryInsert) error {
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if reverseID != 0 {
			if _, e := rep.Accounting().ReverseJournalEntry(ctx, reverseID, "opex repost", "system"); e != nil {
				return e
			}
		}
		_, _, e := rep.Accounting().CreateJournalEntry(ctx, entry)
		return e
	})
	if txErr != nil {
		if errors.Is(txErr, entity.ErrAcctPeriodClosed) {
			slog.Default().WarnContext(ctx, "acctposting: opex month period closed; skipping repost",
				slog.String("month", month.Format("2006-01")))
			return nil
		}
		return fmt.Errorf("commit opex %s: %w", month.Format("2006-01"), txErr)
	}
	return nil
}

// reverseOpex reverses an active opex entry whose month emptied (no costed lines left), creating
// nothing. A closed period or an already-reversed entry is a benign no-op.
func (w *Worker) reverseOpex(ctx context.Context, month time.Time, reverseID int) error {
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, e := rep.Accounting().ReverseJournalEntry(ctx, reverseID, "opex month emptied", "system")
		return e
	})
	if txErr != nil {
		if errors.Is(txErr, entity.ErrAcctPeriodClosed) {
			slog.Default().WarnContext(ctx, "acctposting: opex month period closed; skipping reversal",
				slog.String("month", month.Format("2006-01")))
			return nil
		}
		if errors.Is(txErr, entity.ErrAcctAlreadyReversed) {
			return nil
		}
		return fmt.Errorf("reverse opex %s: %w", month.Format("2006-01"), txErr)
	}
	return nil
}

// sameOpexLines reports whether an existing entry's lines equal a freshly built candidate's, as
// multisets of (account code, side, amount). Amounts are compared as fixed 2-dp strings so a stored
// "100.00" and a built "100" match. Used to make an unchanged month a no-op (no needless repost).
func sameOpexLines(existing []entity.AcctJournalLine, candidate []entity.AcctJournalLineInsert) bool {
	if len(existing) != len(candidate) {
		return false
	}
	type key struct {
		code string
		side entity.AcctSide
		amt  string
	}
	counts := make(map[key]int, len(existing))
	for _, l := range existing {
		counts[key{l.AccountCode, l.Side, l.Amount.Round(2).StringFixed(2)}]++
	}
	for _, l := range candidate {
		k := key{l.AccountCode, l.Side, l.Amount.Round(2).StringFixed(2)}
		if counts[k] == 0 {
			return false
		}
		counts[k]--
	}
	for _, v := range counts {
		if v != 0 {
			return false
		}
	}
	return true
}

// appendCaveat adds a note to an entry's caveat (setting has_caveat), de-duplicating the exact note
// and bounding the text to the column width. De-duplication keeps a Tx retry from doubling the note.
func appendCaveat(e *entity.AcctJournalEntryInsert, note string) {
	e.HasCaveat = true
	if e.Caveat.Valid && e.Caveat.String != "" {
		if strings.Contains(e.Caveat.String, note) {
			return
		}
		e.Caveat = sql.NullString{String: truncateRunes(e.Caveat.String+"; "+note, caveatMaxLen), Valid: true}
		return
	}
	e.Caveat = sql.NullString{String: truncateRunes(note, caveatMaxLen), Valid: true}
}

// truncateRunes caps s to maxLen runes without splitting a multi-byte rune.
func truncateRunes(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen])
}

// firstOfMonthUTC normalises t to the 1st of its month at 00:00 UTC.
func firstOfMonthUTC(t time.Time) time.Time {
	y, m, _ := t.UTC().Date()
	return time.Date(y, m, 1, 0, 0, 0, 0, time.UTC)
}

// versionListLimit bounds the per-source version lookups (a single shipment / dev expense never
// accumulates enough reposts to page).
const versionListLimit = 500

// processShipping is the wave-3 shipping_actual pull (3.1): scan shipments whose actual carrier cost
// changed after the checkpoint and repost each shipment's 6030 charge. Like OPEX the source is mutable,
// so the granule is the shipment and any change reposts (reverse + a versioned source_key); the
// timestamp checkpoint is captured BEFORE the scan so a row changed during the tick is re-seen (deduped
// to a no-op), never missed. Pre-cutover shipments (shipping_date before the start month) are skipped, as
// OPEX skips pre-cutover months. An infra error on a shipment fails the phase without advancing the
// checkpoint (the whole batch retries); a closed period is warned and skipped inside repostShipment.
func (w *Worker) processShipping(ctx context.Context) error {
	acc := w.repo.Accounting()

	cp, err := acc.GetCheckpoint(ctx, checkpointShipmentCost)
	if err != nil {
		return fmt.Errorf("get shipping checkpoint: %w", err)
	}
	lastTS := w.startDate
	if cp.LastTs.Valid {
		lastTS = cp.LastTs.Time
	}

	tickStart := w.repo.Now().UTC()

	ships, err := acc.ListChangedShipmentsForActualCost(ctx, lastTS, w.startDate)
	if err != nil {
		return fmt.Errorf("list changed shipments: %w", err)
	}

	startMonth := firstOfMonthUTC(w.startDate)
	for _, sh := range ships {
		if err := ctx.Err(); err != nil {
			return err
		}
		if firstOfMonthUTC(shippingOccurredAt(sh)).Before(startMonth) {
			continue // pre-cutover shipping date — outside the "start from zero" ledger (like OPEX)
		}
		if err := w.repostShipment(ctx, sh); err != nil {
			// Infra error: do not advance the checkpoint (retry the batch next tick). Shipments already
			// processed this tick re-process to a no-op.
			return err
		}
	}

	// Store the checkpoint a small margin BEFORE the scan instant (B-4): shipment.updated_at is
	// second-granular and the scan uses a strict `>`, so an edit landing in the SAME second as tickStart
	// would be missed forever; the margin re-scans that boundary window next tick, deduped to a no-op.
	const shippingCheckpointMargin = 2 * time.Second
	if err := acc.SetCheckpoint(ctx, checkpointShipmentCost, sql.NullInt64{},
		sql.NullTime{Time: tickStart.Add(-shippingCheckpointMargin), Valid: true}); err != nil {
		return fmt.Errorf("set shipping checkpoint: %w", err)
	}
	return nil
}

// shippingOccurredAt mirrors the pure builder's rule: the shipment's posting instant is shipping_date
// when set, else the row's last-update instant. Duplicated here (the builder's copy is unexported) only
// to make the pre-cutover skip decision.
func shippingOccurredAt(sh entity.AcctShipmentCostFacts) time.Time {
	if sh.ShippingDate.Valid {
		return sh.ShippingDate.Time
	}
	return sh.UpdatedAt
}

// repostShipment reconciles one shipment's 6030 charge with its current actual cost (3.1). It builds the
// candidate entry, no-ops when it already matches the active version, reverses when the cost dropped to
// nothing, and otherwise reposts (reverse the prior version + create the next). Returns an error only for
// infrastructure failures; a closed period is swallowed by commitRepost.
func (w *Worker) repostShipment(ctx context.Context, sh entity.AcctShipmentCostFacts) error {
	active, versionCount, err := w.loadShippingVersions(ctx, sh.ShipmentID, shippingOccurredAt(sh))
	if err != nil {
		return err
	}
	candidate, berr := accounting.BuildShippingActualEntry(sh, versionCount+1)
	if errors.Is(berr, accounting.ErrSkipEmpty) {
		// No positive cost left: reverse any active version, create nothing.
		if active != nil {
			return w.reverseEntry(ctx, "shipping "+strconv.Itoa(sh.ShipmentID), "shipping cost cleared", active.Id)
		}
		return nil
	}
	if berr != nil {
		return fmt.Errorf("build shipping %d: %w", sh.ShipmentID, berr)
	}
	if active == nil {
		return w.commitRepost(ctx, "shipping "+strconv.Itoa(sh.ShipmentID), "", 0, candidate) // candidate is v1
	}
	full, err := w.repo.Accounting().GetJournalEntry(ctx, active.Id)
	if err != nil {
		return fmt.Errorf("load active shipping entry %d: %w", active.Id, err)
	}
	if sameEntryLines(full.Lines, candidate.Lines) {
		return nil // unchanged — no-op
	}
	return w.commitRepost(ctx, "shipping "+strconv.Itoa(sh.ShipmentID), "shipping repost", active.Id, candidate)
}

// loadShippingVersions returns the active (un-reversed) shipping_actual entry for a shipment, if any, and
// the total count of that shipment's versions — new version = count+1 (mirrors loadOpexVersions). It
// windows ListJournalEntries to the occurred_at month (shipping_date is stable across cost-only reposts)
// and matches the 'ship:<id>' source_key in Go. If shipping_date is later edited into a different month a
// second version would land in that month; the UNIQUE(source_type, source_key) idempotency makes that a
// no-op rather than a double-post (the shipping recon block surfaces any residual).
func (w *Worker) loadShippingVersions(ctx context.Context, shipmentID int, occurredAt time.Time) (*entity.AcctJournalEntry, int, error) {
	from := firstOfMonthUTC(occurredAt)
	to := from.AddDate(0, 1, 0)
	entries, _, err := w.repo.Accounting().ListJournalEntries(ctx, entity.AcctEntryFilter{
		From:       from,
		To:         to,
		SourceType: entity.AcctSourceShippingActual,
		Limit:      versionListLimit,
	})
	if err != nil {
		return nil, 0, err
	}
	return pickActiveVersion(entries, "ship:", shipmentID)
}

// processDevExpenses is the wave-3 dev_expense pull (3.2). tech_card_dev_expense has no updated_at and a
// DeleteTechCardDevExpense RPC exists, so this is a FULL reconcile scan each tick (dev expenses are few,
// like production runs): post new costed rows, repost changed amounts, and reverse rows that were deleted
// or lost their base cost. Per-row errors are logged and skipped so one bad row neither stalls the others
// nor fails the tick; only the fact/version reads are phase-level errors.
func (w *Worker) processDevExpenses(ctx context.Context) error {
	acc := w.repo.Accounting()

	devs, err := acc.ListDevExpensesForPosting(ctx, w.startDate)
	if err != nil {
		return fmt.Errorf("list dev expenses: %w", err)
	}
	active, versionCount, err := w.loadDevExpenseVersions(ctx)
	if err != nil {
		return fmt.Errorf("load dev expense versions: %w", err)
	}

	seen := make(map[int]bool, len(devs))
	for _, d := range devs {
		if err := ctx.Err(); err != nil {
			return err
		}
		seen[d.Id] = true
		cur := active[d.Id]

		candidate, berr := accounting.BuildDevExpenseEntry(d, versionCount[d.Id]+1)
		if errors.Is(berr, accounting.ErrSkipUncosted) || errors.Is(berr, accounting.ErrSkipEmpty) {
			// Uncosted / zero: reverse a prior version (it lost its cost), else just skip.
			if cur != nil {
				if err := w.reverseEntry(ctx, "dev expense "+strconv.Itoa(d.Id), "dev expense uncosted", cur.Id); err != nil {
					slog.Default().ErrorContext(ctx, "acctposting: reverse uncosted dev expense",
						slog.Int("dev_expense_id", d.Id), slog.String("err", err.Error()))
				}
			} else {
				slog.Default().DebugContext(ctx, "acctposting: skip uncosted dev expense", slog.Int("dev_expense_id", d.Id))
			}
			continue
		}
		if berr != nil {
			slog.Default().ErrorContext(ctx, "acctposting: build dev expense",
				slog.Int("dev_expense_id", d.Id), slog.String("err", berr.Error()))
			continue
		}

		if cur == nil {
			if err := w.commitRepost(ctx, "dev expense "+strconv.Itoa(d.Id), "", 0, candidate); err != nil { // v1
				slog.Default().ErrorContext(ctx, "acctposting: post dev expense",
					slog.Int("dev_expense_id", d.Id), slog.String("err", err.Error()))
			}
			continue
		}
		full, err := acc.GetJournalEntry(ctx, cur.Id)
		if err != nil {
			slog.Default().ErrorContext(ctx, "acctposting: load active dev expense entry",
				slog.Int("dev_expense_id", d.Id), slog.String("err", err.Error()))
			continue
		}
		if sameEntryLines(full.Lines, candidate.Lines) {
			continue // unchanged — no-op
		}
		if err := w.commitRepost(ctx, "dev expense "+strconv.Itoa(d.Id), "dev expense repost", cur.Id, candidate); err != nil {
			slog.Default().ErrorContext(ctx, "acctposting: repost dev expense",
				slog.Int("dev_expense_id", d.Id), slog.String("err", err.Error()))
		}
	}

	// Deletions: an active entry whose dev expense id no longer exists (row deleted) is reversed.
	for id, entry := range active {
		if seen[id] {
			continue
		}
		if err := w.reverseEntry(ctx, "dev expense "+strconv.Itoa(id), "dev expense deleted", entry.Id); err != nil {
			slog.Default().ErrorContext(ctx, "acctposting: reverse deleted dev expense",
				slog.Int("dev_expense_id", id), slog.String("err", err.Error()))
		}
	}
	return nil
}

// loadDevExpenseVersions returns, over ALL dev_expense entries, the active (un-reversed) entry per dev
// expense id and the version count per id (new version = count+1). Unbounded window (occurred_at =
// incurred_at can be any date) filtered to source_type dev_expense — few entries, so a single page.
func (w *Worker) loadDevExpenseVersions(ctx context.Context) (map[int]*entity.AcctJournalEntry, map[int]int, error) {
	entries, _, err := w.repo.Accounting().ListJournalEntries(ctx, entity.AcctEntryFilter{
		SourceType: entity.AcctSourceDevExpense,
		Limit:      versionListLimit,
	})
	if err != nil {
		return nil, nil, err
	}
	active := make(map[int]*entity.AcctJournalEntry)
	count := make(map[int]int)
	for i := range entries {
		e := entries[i]
		id, ok := parseSourceID(e.SourceKey, "dev:")
		if !ok {
			continue
		}
		count[id]++
		if !e.ReversedBy.Valid {
			ecopy := e
			active[id] = &ecopy
		}
	}
	return active, count, nil
}

// pickActiveVersion scans entries whose source_key carries the given prefix + id, returning the
// un-reversed one (if any) and the total count of that id's versions.
func pickActiveVersion(entries []entity.AcctJournalEntry, prefix string, id int) (*entity.AcctJournalEntry, int, error) {
	var active *entity.AcctJournalEntry
	count := 0
	for i := range entries {
		e := entries[i]
		got, ok := parseSourceID(e.SourceKey, prefix)
		if !ok || got != id {
			continue
		}
		count++
		if !e.ReversedBy.Valid {
			ecopy := e
			active = &ecopy
		}
	}
	return active, count, nil
}

// parseSourceID parses the numeric id out of a versioned source_key '<prefix><id>' or
// '<prefix><id>:vN' (e.g. 'ship:12', 'dev:3:v2'). ok is false when the key lacks the prefix or the id
// segment is not an integer.
func parseSourceID(key, prefix string) (int, bool) {
	if !strings.HasPrefix(key, prefix) {
		return 0, false
	}
	rest := key[len(prefix):]
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		rest = rest[:i]
	}
	id, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return id, true
}

// commitRepost writes a repost in one short Tx: reverse the prior version (when reverseID != 0), then
// create the new one. A closed period rolls the whole Tx back and is warned + skipped (the active entry
// stays in place); any other error is returned. Shared by the shipping and dev_expense pulls (mirrors
// commitOpex).
func (w *Worker) commitRepost(ctx context.Context, label, reverseReason string, reverseID int, entry entity.AcctJournalEntryInsert) error {
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if reverseID != 0 {
			if _, e := rep.Accounting().ReverseJournalEntry(ctx, reverseID, reverseReason, "system"); e != nil {
				return e
			}
		}
		_, _, e := rep.Accounting().CreateJournalEntry(ctx, entry)
		return e
	})
	if txErr != nil {
		if errors.Is(txErr, entity.ErrAcctPeriodClosed) {
			slog.Default().WarnContext(ctx, "acctposting: period closed; skipping repost",
				slog.String("source", label))
			return nil
		}
		return fmt.Errorf("commit %s: %w", label, txErr)
	}
	return nil
}

// reverseEntry reverses an active entry (an emptied / removed source), creating nothing. A closed period
// or an already-reversed entry is a benign no-op. Shared by the shipping and dev_expense pulls.
func (w *Worker) reverseEntry(ctx context.Context, label, reason string, reverseID int) error {
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, e := rep.Accounting().ReverseJournalEntry(ctx, reverseID, reason, "system")
		return e
	})
	if txErr != nil {
		if errors.Is(txErr, entity.ErrAcctPeriodClosed) {
			slog.Default().WarnContext(ctx, "acctposting: period closed; skipping reversal",
				slog.String("source", label))
			return nil
		}
		if errors.Is(txErr, entity.ErrAcctAlreadyReversed) {
			return nil
		}
		return fmt.Errorf("reverse %s: %w", label, txErr)
	}
	return nil
}

// sameEntryLines reports whether an existing entry's lines equal a freshly built candidate's, as
// multisets of (account code, side, amount). A thin alias of sameOpexLines — the comparison is generic
// (used by the shipping and dev_expense reposts, not just opex).
func sameEntryLines(existing []entity.AcctJournalLine, candidate []entity.AcctJournalLineInsert) bool {
	return sameOpexLines(existing, candidate)
}
