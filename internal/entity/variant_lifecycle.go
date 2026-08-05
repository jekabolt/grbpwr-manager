package entity

import "errors"

// ErrVariantExists is returned by CreateVariant when the colourway already carries a variant for that
// size (UNIQUE(product_id, size_id)). The API layer maps it to AlreadyExists.
var ErrVariantExists = errors.New("variant already exists for this size")

// ErrColorwayArchived is returned when a write targets an archived colourway. The API layer maps it to
// FailedPrecondition.
var ErrColorwayArchived = errors.New("colourway is archived")

// ErrVariantPriceNotSeconds is returned by SetVariantPrice when the target variant is not grade 'B'
// (0251): manual per-variant prices exist only for factory seconds — A prices live on the colourway
// (product_price). The API layer maps it to FailedPrecondition.
var ErrVariantPriceNotSeconds = errors.New("manual variant prices apply to B-grade (seconds) variants only")

// SecondsVariant is one B-grade (factory seconds) variant with its manual price list (0251). An
// empty Prices list means the variant is not sellable (fail-closed).
type SecondsVariant struct {
	Variant
	Prices []ColorwayPriceInsert
}

// VariantStatus is a variant's (product_size) stored lifecycle (R2, migration 0155). A variant is
// archive-not-delete: an order line, waitlist row and stock-history row all address the immutable
// product_size.id, so a referenced variant is retired by flipping this flag, never physically removed.
// Wire enum common.VariantLifecycleStatus has the identical numbers.
type VariantStatus uint8

const (
	VariantStatusUnknown  VariantStatus = 0 // never written; an unknown value read from the DB is fail-closed
	VariantStatusActive   VariantStatus = 1 // default; the only state a fresh variant is created in
	VariantStatusArchived VariantStatus = 2 // retired; excluded from the storefront, rejects stock writes
)

// Valid reports whether s is a storable variant status (UNKNOWN=0 is never persisted).
func (s VariantStatus) Valid() bool {
	return s == VariantStatusActive || s == VariantStatusArchived
}

// String renders the status for logs and error messages.
func (s VariantStatus) String() string {
	switch s {
	case VariantStatusActive:
		return "active"
	case VariantStatusArchived:
		return "archived"
	default:
		return "unknown"
	}
}
