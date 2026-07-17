package entity

import "fmt"

// NewFieldViolation builds a field-tagged validation error (S24 / PLM-rework). It records the
// structured Field/Reason/Conflicting/HowToFix that the API layer surfaces as a
// google.rpc.BadRequest FieldViolation, and also composes a human-readable Message so Error()
// and logs stay useful without special handling.
//
//   - field       — offending input path (e.g. "bom_items[2].material_id"); required.
//   - reason      — stable machine-readable code (e.g. "material_not_found").
//   - conflicting — the entity that blocks the operation (e.g. `colorway "BLK" recipe`); may be "".
//   - howToFix    — actionable guidance for the caller; may be "".
func NewFieldViolation(field, reason, conflicting, howToFix string) *ValidationError {
	msg := field
	if reason != "" {
		msg = fmt.Sprintf("%s: %s", field, reason)
	}
	if conflicting != "" {
		msg = fmt.Sprintf("%s (used by %s)", msg, conflicting)
	}
	if howToFix != "" {
		msg = fmt.Sprintf("%s; %s", msg, howToFix)
	}
	return &ValidationError{
		Message:     msg,
		Field:       field,
		Reason:      reason,
		Conflicting: conflicting,
		HowToFix:    howToFix,
	}
}
