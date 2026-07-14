package admin

import (
	"math"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ga4CacheWindow is how much history GA4's cache holds. A metrics period longer than this has
// incomplete session data, so conversion is suppressed (sessions treated as unavailable) rather than
// shown understated. Matches the analytics-cache note (ga4sync keeps a rolling ~90-day cache).
const ga4CacheWindow = 90 * 24 * time.Hour

// demandDirectionalNote is the standing caveat on every country conversion: it is comparable across
// countries but must never be read as an absolute or benchmarked rate.
const demandDirectionalNote = "Conversion is directional — sessions are geo-IP while orders are by " +
	"shipping address, and consent/ad-blockers undercount sessions, so the rate is over-stated. " +
	"Compare countries to each other, not against external benchmarks."

// mergeCountryDemand folds GA4 sessions (keyed by country NAME) into the DB-side demand rows (keyed by
// ISO-2 country) and computes conversion. GA4 country names are mapped to ISO-2 via the entity
// dictionary; a name with no mapping is accumulated into a "(unmatched)" row so unattributable traffic
// is visible rather than silently dropped. When the period exceeds GA4's cache window, sessions are
// treated as unavailable (0) and every row carries the window caveat — an understated conversion would
// be worse than none. Pure (no I/O) so it is unit-tested directly.
func mergeCountryDemand(base []entity.CountryDemandRow, sessions []entity.GeographySessionMetric, ga4WindowOK bool) []entity.CountryDemandRow {
	bySessionISO := map[string]int{}
	unmatched := 0
	if ga4WindowOK {
		for _, s := range sessions {
			if s.Sessions <= 0 {
				continue
			}
			if iso, ok := entity.CountryNameToISO2(s.Country); ok {
				bySessionISO[iso] += s.Sessions
			} else {
				unmatched += s.Sessions
			}
		}
	}

	rows := make([]entity.CountryDemandRow, len(base))
	copy(rows, base)
	for i := range rows {
		rows[i].Sessions = bySessionISO[rows[i].Country]
		if rows[i].Sessions > 0 {
			rows[i].ConversionRatePct = round2f(float64(rows[i].Orders) / float64(rows[i].Sessions) * 100)
		}
		rows[i].Caveat = demandCaveat(ga4WindowOK, rows[i].Sessions)
	}
	if unmatched > 0 {
		rows = append(rows, entity.CountryDemandRow{
			Country:  "(unmatched)",
			Sessions: unmatched,
			Caveat:   "GA4 reported these sessions under a country name with no ISO mapping; they can't be tied to orders.",
		})
	}
	return rows
}

// demandCaveat picks the caveat for a demand row given the GA4 window state and whether any sessions
// mapped to the country.
func demandCaveat(ga4WindowOK bool, sessions int) string {
	if !ga4WindowOK {
		return "The period exceeds GA4's 90-day data window, so session-based conversion is unavailable here."
	}
	if sessions == 0 {
		return "No GA4 sessions mapped to this country, so conversion is unavailable. " + demandDirectionalNote
	}
	return demandDirectionalNote
}

func round2f(x float64) float64 { return math.Round(x*100) / 100 }

// applyGeographyGrowth fills each current-period country row's compare value/count and period-over-
// period revenue growth (task 10) from the compare-period breakdown, so the frontend gets a ready
// growth rate instead of dividing decimal strings itself. A country absent from the compare period, or
// with zero compare-period revenue, keeps ChangePct 0 and no CompareValue — the frontend reads
// compare_count == 0 as a genuinely new country (infinite growth), not a flat one. Mutates current in
// place; a no-op without a compare period. Pure (no I/O) so it is unit-tested directly.
func applyGeographyGrowth(current, compare []entity.GeographyMetric) {
	if len(compare) == 0 {
		return
	}
	byCountry := make(map[string]entity.GeographyMetric, len(compare))
	for _, c := range compare {
		byCountry[c.Country] = c
	}
	for i := range current {
		c, ok := byCountry[current[i].Country]
		if !ok {
			continue
		}
		cv := c.Value
		current[i].CompareValue = &cv
		cc := c.Count
		current[i].CompareCount = &cc
		if c.Value.GreaterThan(decimal.Zero) {
			current[i].ChangePct = current[i].Value.Sub(c.Value).Div(c.Value).
				Mul(decimal.NewFromInt(100)).Round(2).InexactFloat64()
		}
	}
}
