package metrics

import "math"

// proportionMarginOfError returns the 95% Wald confidence-interval half-width for a
// proportion, expressed in the same 0–100 percentage units as ratePct. For a rate p over n
// independent trials the half-width is 1.96·√(p(1−p)/n); this is what lets the UI render
// "12.0% ± 2.4" and decide significance from the data rather than an arbitrary display floor.
// Returns 0 when n <= 0 (no sample) so callers can treat 0 as "not computed".
func proportionMarginOfError(ratePct float64, n int) float64 {
	if n <= 0 {
		return 0
	}
	p := ratePct / 100
	if p < 0 {
		p = 0
	} else if p > 1 {
		p = 1
	}
	return 1.96 * math.Sqrt(p*(1-p)/float64(n)) * 100
}
