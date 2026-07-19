package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// EnsurePeriodOpen lazily creates the period row for month (default status 'open') and returns
// ErrAcctPeriodClosed if the period exists and is closed. Called by CreateJournalEntry before every
// insert.
func (s *Store) EnsurePeriodOpen(ctx context.Context, month time.Time) error {
	p := firstOfMonthUTC(month).Format(dateLayout)
	if err := storeutil.ExecNamed(ctx, s.DB,
		`INSERT INTO acct_period (period, status) VALUES (:p, 'open')
		 ON DUPLICATE KEY UPDATE period = period`,
		map[string]any{"p": p}); err != nil {
		return fmt.Errorf("accounting: ensure period %s: %w", p, err)
	}
	st, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, s.DB, `SELECT status FROM acct_period WHERE period = :p`, map[string]any{"p": p})
	if err != nil {
		return fmt.Errorf("accounting: read period %s: %w", p, err)
	}
	if st.Status == entity.AcctPeriodStatusClosed {
		return fmt.Errorf("%w: %s", ErrAcctPeriodClosed, p)
	}
	return nil
}

// isPeriodClosed reports whether month's period is closed. A missing row is treated as open (periods
// are created lazily), so the zero state never blocks.
func (s *Store) isPeriodClosed(ctx context.Context, month time.Time) (bool, error) {
	p := firstOfMonthUTC(month).Format(dateLayout)
	st, err := storeutil.QueryNamedOne[struct {
		Status string `db:"status"`
	}](ctx, s.DB, `SELECT status FROM acct_period WHERE period = :p`, map[string]any{"p": p})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("accounting: period status %s: %w", p, err)
	}
	return st.Status == entity.AcctPeriodStatusClosed, nil
}

// ClosePeriod closes a fully-past month after asserting it is reconciled (docs/plan-accounting/02):
// no pending order events in the month, the pull sources (material movements, production receives,
// costed opex) are posted through the month, and the month's ledger is balanced. Any failure returns
// ErrAcctPeriodNotReady with the specific reason. The caller wraps this in repo.Tx.
func (s *Store) ClosePeriod(ctx context.Context, month time.Time, adminUsername string) error {
	m := firstOfMonthUTC(month)
	next := m.AddDate(0, 1, 0)
	from := m.Format(dateLayout)
	to := next.Format(dateLayout)

	// 1) must be a fully-past month (cannot close the current or a future month).
	if !m.Before(firstOfMonthUTC(s.Now())) {
		return fmt.Errorf("%w: cannot close current or future month %s", ErrAcctPeriodNotReady, m.Format("2006-01"))
	}

	// 2) no unprocessed order events occurring in the month.
	pendingEvents, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM acct_event
		WHERE processed_at IS NULL AND occurred_at >= :from AND occurred_at < :to`,
		map[string]any{"from": from, "to": to})
	if err != nil {
		return fmt.Errorf("accounting: close period pending events: %w", err)
	}
	if pendingEvents > 0 {
		return fmt.Errorf("%w: %d unprocessed event(s) in %s", ErrAcctPeriodNotReady, pendingEvents, m.Format("2006-01"))
	}

	// 3a) material movements scanned through the end of the month (checkpoint advanced past every
	//     movement created before the next month). Uncosted movements advance the checkpoint too, so a
	//     caught-up worker leaves count == 0.
	cp, err := s.GetCheckpoint(ctx, "material_movement")
	if err != nil {
		return fmt.Errorf("accounting: close period movement checkpoint: %w", err)
	}
	lastMovementID := int64(0)
	if cp.LastId.Valid {
		lastMovementID = cp.LastId.Int64
	}
	unpostedMovements, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM material_stock_movement
		WHERE id > :last_id AND created_at < :to`,
		map[string]any{"last_id": lastMovementID, "to": to})
	if err != nil {
		return fmt.Errorf("accounting: close period unposted movements: %w", err)
	}
	if unpostedMovements > 0 {
		return fmt.Errorf("%w: %d unposted material movement(s) through %s", ErrAcctPeriodNotReady, unpostedMovements, m.Format("2006-01"))
	}

	// 3b) production receives in the month all posted.
	unpostedRuns, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM production_run r
		WHERE r.received_at >= :from AND r.received_at < :to
		  AND NOT EXISTS (SELECT 1 FROM acct_journal_entry e
		                  WHERE e.source_type = 'production_receive' AND e.source_key = CAST(r.id AS CHAR) COLLATE utf8mb4_unicode_ci)`,
		map[string]any{"from": from, "to": to})
	if err != nil {
		return fmt.Errorf("accounting: close period unposted runs: %w", err)
	}
	if unpostedRuns > 0 {
		return fmt.Errorf("%w: %d received production run(s) unposted in %s", ErrAcctPeriodNotReady, unpostedRuns, m.Format("2006-01"))
	}

	// 3c) if the month has costed opex lines, its opex_month entry must exist (and not be reversed).
	costedOpexLines, err := storeutil.QueryCountNamed(ctx, s.DB, `
		SELECT COUNT(*) FROM opex_line WHERE month = :from AND amount_base IS NOT NULL`,
		map[string]any{"from": from})
	if err != nil {
		return fmt.Errorf("accounting: close period opex lines: %w", err)
	}
	if costedOpexLines > 0 {
		opexEntries, err := storeutil.QueryCountNamed(ctx, s.DB, `
			SELECT COUNT(*) FROM acct_journal_entry
			WHERE source_type = 'opex_month' AND source_key LIKE :prefix AND reversed_by IS NULL`,
			map[string]any{"prefix": m.Format("2006-01") + "%"})
		if err != nil {
			return fmt.Errorf("accounting: close period opex entry: %w", err)
		}
		if opexEntries == 0 {
			return fmt.Errorf("%w: opex for %s not posted", ErrAcctPeriodNotReady, m.Format("2006-01"))
		}
	}

	// 4) the month's ledger is balanced (an invariant, but verified — it is the trust check).
	bal, err := storeutil.QueryNamedOne[struct {
		Dr string `db:"dr"`
		Cr string `db:"cr"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN l.side='debit'  THEN l.amount ELSE 0 END), 0) AS dr,
		       COALESCE(SUM(CASE WHEN l.side='credit' THEN l.amount ELSE 0 END), 0) AS cr
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		WHERE e.occurred_at >= :from AND e.occurred_at < :to`,
		map[string]any{"from": from, "to": to})
	if err != nil {
		return fmt.Errorf("accounting: close period trial balance: %w", err)
	}
	if bal.Dr != bal.Cr {
		return fmt.Errorf("%w: month %s unbalanced (debit %s != credit %s)", ErrAcctPeriodNotReady, m.Format("2006-01"), bal.Dr, bal.Cr)
	}

	// All checks passed — close (creating the row if it did not exist yet).
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO acct_period (period, status, closed_at, closed_by)
		VALUES (:p, 'closed', NOW(), :admin)
		ON DUPLICATE KEY UPDATE status = 'closed', closed_at = NOW(), closed_by = VALUES(closed_by)`,
		map[string]any{"p": from, "admin": adminUsername}); err != nil {
		return fmt.Errorf("accounting: close period %s: %w", from, err)
	}
	return nil
}

// ReopenPeriod re-opens a closed month (creating the row open if it did not exist), clearing the
// closed_at / closed_by markers. adminUsername is accepted for a uniform call surface and audit
// logging by the caller; the schema records only the closer.
func (s *Store) ReopenPeriod(ctx context.Context, month time.Time, adminUsername string) error {
	p := firstOfMonthUTC(month).Format(dateLayout)
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO acct_period (period, status) VALUES (:p, 'open')
		ON DUPLICATE KEY UPDATE status = 'open', closed_at = NULL, closed_by = NULL`,
		map[string]any{"p": p}); err != nil {
		return fmt.Errorf("accounting: reopen period %s: %w", p, err)
	}
	return nil
}

// ListPeriods returns every accounting period, newest first.
func (s *Store) ListPeriods(ctx context.Context) ([]entity.AcctPeriod, error) {
	periods, err := storeutil.QueryListNamed[entity.AcctPeriod](ctx, s.DB, `
		SELECT period, status, closed_at, closed_by
		FROM acct_period
		ORDER BY period DESC`, nil)
	if err != nil {
		return nil, fmt.Errorf("accounting: list periods: %w", err)
	}
	return periods, nil
}
