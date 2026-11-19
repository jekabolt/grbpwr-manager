package platform

import (
	"context"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// monthStart is the first day of the current month in the owner's budget zone, as a date (the
// rollup's day keys are budget-zone days).
func (s *Server) monthStart() time.Time {
	tz := ""
	if s.budgetZone != nil {
		tz = s.budgetZone()
	}
	if tz == "" {
		tz = entity.DefaultBudgetTimezone
	}
	today, err := time.Parse(time.DateOnly, entity.BudgetDayKey(s.nowUTC(), tz))
	if err != nil {
		today = s.nowUTC()
	}
	return time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// monthSpend is this month's AI spend of the given teams, by team id, in one control-plane query
// that reads only those teams' rows.
func (s *Server) monthSpend(ctx context.Context, teamIDs ...int64) (map[int64]platformstore.AISpend, error) {
	return s.st.AISpendFor(ctx, s.monthStart(), teamIDs)
}
