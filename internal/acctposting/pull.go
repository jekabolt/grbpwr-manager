package acctposting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	entries := make([]entity.AcctJournalEntryInsert, 0, len(movements))
	maxID := lastID
	for _, m := range movements {
		if int64(m.Id) > maxID {
			maxID = int64(m.Id)
		}
		entry, berr := accounting.BuildMaterialMovementEntry(m, w.startDate)
		if berr != nil {
			switch {
			case errors.Is(berr, accounting.ErrSkipUncosted):
				slog.Default().DebugContext(ctx, "acctposting: skip uncosted movement", slog.Int("movement_id", m.Id))
			default:
				// ErrUnknownMovementType or an unexpected builder error: skip but advance the cursor so
				// the queue behind it is not blocked; the movement is visible via reconciliation.
				slog.Default().ErrorContext(ctx, "acctposting: skip unbuildable movement",
					slog.Int("movement_id", m.Id), slog.String("err", berr.Error()))
			}
			continue
		}
		entries = append(entries, entry)
	}

	// One short Tx: post every built entry + advance the checkpoint atomically. Source tables are not
	// read inside the Tx (the lock rule), and idempotency makes a crash-and-replay a no-op.
	txErr := w.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		for i := range entries {
			if e := createMovementEntry(ctx, rep, &entries[i], clampTo); e != nil {
				return e
			}
		}
		return rep.Accounting().SetCheckpoint(ctx, checkpointMaterialMovement,
			sql.NullInt64{Int64: maxID, Valid: true}, sql.NullTime{})
	})
	if txErr != nil {
		return fmt.Errorf("post movements batch: %w", txErr)
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

	if err := acc.SetCheckpoint(ctx, checkpointOpexLine, sql.NullInt64{}, sql.NullTime{Time: tickStart, Valid: true}); err != nil {
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
