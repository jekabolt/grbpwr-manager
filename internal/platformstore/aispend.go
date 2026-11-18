package platformstore

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// AISpend is one team's AI spend over a span of days (team_ai_spend_daily, written by the A1 rollup
// from the team's own ai_usage_event). CostUSD sums the priced calls only; Unpriced counts the calls
// whose price is unknown (cost_usd NULL) — an unknown price is never read as zero, so the console
// shows it beside the dollars.
type AISpend struct {
	CostUSD  decimal.Decimal
	Unpriced int64
	Calls    int64
}

// dayLayout is a DATE as the control plane stores it: the owner's budget-zone calendar day.
const dayLayout = "2006-01-02"

// UpsertAISpend writes one team's figures for one day (the calendar day of day as given — the
// rollup passes the owner budget-zone day at midnight UTC). The row is replaced whole: the rollup
// recomputes the day from the team's ledger every time, so the last write is the truth.
func (s *Store) UpsertAISpend(ctx context.Context, teamID int64, day time.Time, v AISpend) error {
	if teamID <= 0 {
		return fmt.Errorf("platformstore: upsert ai spend: bad team %d", teamID)
	}
	if day.IsZero() {
		return fmt.Errorf("platformstore: upsert ai spend: no day")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO team_ai_spend_daily (team_id, day, cost_usd, unpriced_calls, calls, refreshed_at)
VALUES (?, ?, ?, ?, ?, UTC_TIMESTAMP(3)) AS nv
ON DUPLICATE KEY UPDATE cost_usd = nv.cost_usd, unpriced_calls = nv.unpriced_calls, calls = nv.calls, refreshed_at = nv.refreshed_at`,
		teamID, day.Format(dayLayout), v.CostUSD.StringFixed(6), v.Unpriced, v.Calls)
	if err != nil {
		return fmt.Errorf("platformstore: upsert ai spend: %w", err)
	}
	return nil
}

// AISpendSince sums every team's rows from day (inclusive) on, keyed by team id. A team without rows
// in the span is absent from the map. The console passes the first day of the current month.
func (s *Store) AISpendSince(ctx context.Context, day time.Time) (map[int64]AISpend, error) {
	var rows []struct {
		TeamID   int64           `db:"team_id"`
		CostUSD  decimal.Decimal `db:"cost_usd"`
		Unpriced int64           `db:"unpriced_calls"`
		Calls    int64           `db:"calls"`
	}
	if err := s.db.SelectContext(ctx, &rows, `SELECT team_id, COALESCE(SUM(cost_usd), 0) AS cost_usd,
       CAST(COALESCE(SUM(unpriced_calls), 0) AS SIGNED) AS unpriced_calls, CAST(COALESCE(SUM(calls), 0) AS SIGNED) AS calls
FROM team_ai_spend_daily WHERE day >= ? GROUP BY team_id`, day.Format(dayLayout)); err != nil {
		return nil, fmt.Errorf("platformstore: ai spend since %s: %w", day.Format(dayLayout), err)
	}
	out := make(map[int64]AISpend, len(rows))
	for _, r := range rows {
		out[r.TeamID] = AISpend{CostUSD: r.CostUSD, Unpriced: r.Unpriced, Calls: r.Calls}
	}
	return out, nil
}
