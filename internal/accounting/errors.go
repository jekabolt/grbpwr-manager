package accounting

import (
	"errors"
	"strings"
)

// Sentinel outcomes returned by the builders. They are not failures of the builder — they tell the
// worker how to treat a fact it cannot (or should not) post. The worker matches them with
// errors.Is and, in every case, advances its cursor / marks the event so the fact is not retried
// forever; the divergence is surfaced by the reconciliation report (docs/plan-accounting/06),
// never invented into a fake posting.
var (
	// ErrNotReady means a Stripe order's authoritative EUR settlement (total_settled_base) has not
	// arrived yet (topUpSettledBase is asynchronous). The worker should defer and retry — this is
	// transient, not terminal.
	ErrNotReady = errors.New("accounting: order sale not ready (settled base pending)")

	// ErrSkipNonEUR means a non-Stripe, non-EUR order cannot be converted to base without an FX
	// rate, so S1 is not posted (S2 for such an order is skipped too). Resolution is a manual entry.
	ErrSkipNonEUR = errors.New("accounting: non-stripe non-eur order not posted (manual entry required)")

	// ErrDegenerateAmounts means an amount that must be positive is not (total_price <= 0, gross
	// <= 0, or a refund <= 0). Guarding this also keeps the k = G/total_price division safe. In
	// practice unreachable (the custom-order flow requires a positive total), but never divide by
	// zero by accident.
	ErrDegenerateAmounts = errors.New("accounting: degenerate amounts, entry not built")

	// ErrSkipUncosted means a material movement has no base valuation (unit_cost_base is NULL) or a
	// zero value — there is nothing to post. Uncosted movements are expected and surfaced by
	// reconciliation, not posted at a made-up cost.
	ErrSkipUncosted = errors.New("accounting: movement uncosted or zero value, nothing to post")

	// ErrSkipEmpty means an event has no positive lines to post (a production receive with nothing
	// costed, an OPEX month with no costed lines). The worker records the source as processed.
	ErrSkipEmpty = errors.New("accounting: nothing to post")

	// ErrUnknownMovementType means a material movement carried a type outside the closed enum — a
	// data or schema drift the builder refuses to guess at.
	ErrUnknownMovementType = errors.New("accounting: unknown material movement type")
)

// EmptyReceiptError is ErrSkipEmpty (errors.Is-compatible via Unwrap) that carries the caveats the
// empty build accumulated. An empty production-receive outcome can still be ledger-relevant news —
// "FG already over-transferred, the clawback is not representable", "manual costs shrank below the
// capitalised total" — and with no entry to pin a caveat on, this error is the only carrier; the
// worker must log it, not discard it (adversarial #3).
type EmptyReceiptError struct{ Caveats []string }

func (e *EmptyReceiptError) Error() string {
	return "accounting: nothing to post; caveats: " + strings.Join(e.Caveats, "; ")
}

func (e *EmptyReceiptError) Unwrap() error { return ErrSkipEmpty }
