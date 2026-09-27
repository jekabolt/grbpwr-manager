package entity

import (
	"testing"
	"time"
)

// TestBudgetDayKeyIsComputedInTheOrgTimezone — the design budget and the AI ledger share this day.
// MUTATION: return now.UTC().Format(...) → the Warsaw rows go red.
func TestBudgetDayKeyIsComputedInTheOrgTimezone(t *testing.T) {
	at := func(s string) time.Time {
		t.Helper()
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, c := range []struct {
		name, when, tz, want string
	}{
		{"winter: 23:30Z is already tomorrow in Warsaw (UTC+1)", "2026-03-01T23:30:00Z", "Europe/Warsaw", "2026-03-02"},
		{"summer: 22:30Z is already tomorrow in Warsaw (UTC+2)", "2026-09-27T22:30:00Z", "Europe/Warsaw", "2026-09-28"},
		{"summer: 21:59Z is still today in Warsaw", "2026-09-27T21:59:00Z", "Europe/Warsaw", "2026-09-27"},
		{"UTC is UTC", "2026-03-01T23:30:00Z", "UTC", "2026-03-01"},
		{"an unloadable zone falls back to UTC, never to the server's local day", "2026-03-01T23:30:00Z", "Mars/Olympus", "2026-03-01"},
		{"the default zone is Warsaw's", "2026-09-27T22:30:00Z", DefaultBudgetTimezone, "2026-09-28"},
	} {
		if got := BudgetDayKey(at(c.when), c.tz); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
