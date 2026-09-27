package entity

import "time"

// DefaultBudgetTimezone is the organisation's zone when design_settings says nothing (0344's column
// default, and the fallback store/design reads when the singleton row is gone).
const DefaultBudgetTimezone = "Europe/Warsaw"

// BudgetDayKey is the day key (YYYY-MM-DD) of an instant in the organisation's timezone — the ONE
// computation behind design_budget_day.day (store/design.DesignBudgetDayKey delegates here) and
// ai_usage_event.day_local (aiprov.Ledger).
//
// ⚠ IT LIVES IN entity, NOT IN store/design, BECAUSE OF WHO MUST CALL IT. The ledger writer in
// aiprov needs this exact day, and aiprov may not import store/design: that package imports the
// openrouter client (wave2.go), and the clients are to import aiprov once their transports speak
// its CallError (02-PLAN §3) — aiprov → store/design → openrouter → aiprov would be a cycle. One
// function both can reach keeps "the two must not compute it differently" true by construction
// rather than by a copy and a test that compares them.
func BudgetDayKey(now time.Time, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		// An unloadable zone name must not silently become the server's own local day, which
		// would move the reset by hours without telling anyone. UTC is the neutral fallback and
		// it is the one the column's own default day would agree with.
		loc = time.UTC
	}
	return now.In(loc).Format("2006-01-02")
}
