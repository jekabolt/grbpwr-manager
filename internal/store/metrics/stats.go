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

// associationMetrics turns a co-occurrence count into the standard market-basket association
// measures for a product pair (A,B), so the client gets a normalized signal instead of a raw
// count it has to threshold arbitrarily:
//
//   - support    = P(A∧B)  = coCount / totalOrders          — how common the pair is overall
//   - confidence = P(B|A)  = coCount / ordersWithA          — given A, how often B is added too
//   - lift       = support / (P(A)·P(B)) = coCount·total / (ordersWithA·ordersWithB)
//     lift > 1 ⇒ bought together more than independent chance; = 1 ⇒ independent; < 1 ⇒ less.
//
// Any degenerate denominator (no orders, or a product that never sold on its own) yields 0 for
// the affected measure so callers can treat 0 as "not computable" rather than a real signal.
func associationMetrics(coCount, ordersWithA, ordersWithB, totalOrders int) (support, confidence, lift float64) {
	if totalOrders > 0 {
		support = float64(coCount) / float64(totalOrders)
	}
	if ordersWithA > 0 {
		confidence = float64(coCount) / float64(ordersWithA)
	}
	if ordersWithA > 0 && ordersWithB > 0 && totalOrders > 0 {
		lift = float64(coCount) * float64(totalOrders) / (float64(ordersWithA) * float64(ordersWithB))
	}
	return support, confidence, lift
}
