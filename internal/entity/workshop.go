package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// WorkshopSettings is the shop-floor configuration — «дом настроек цеха» (Ф2.5), one singleton row
// in workshop_settings (0272). It holds the physical constants of the workshop itself, the things
// that are true of the room and its equipment rather than of any one tech card or раскладка.
//
// Первый жилец is the cutting table length. Until now the nesting modal made the operator type it in
// on every single раскладка even though it never changes between them; it is a property of the ЦЕХ,
// so it lives here and the раскладка merely overrides it when a particular lay is spread elsewhere.
// Ф3.2 (припуск по умолчанию), Ф4.8 (предел высоты стопки) and 08-cut-out (минимальный зазор) move
// in next, each as its own typed column.
type WorkshopSettings struct {
	// CuttingTableLengthCm is the usable length of the cutting/spreading table, in centimetres.
	//
	// INVALID (NULL in the column) means the workshop has not configured a table, and that is NOT
	// the same as zero. A reader must render it as "no length verdict available", never as a
	// comparison against 0 — a workshop that simply has not filled this in must not be told that
	// every marker it lays is too long. Callers: check .Valid before comparing.
	CuttingTableLengthCm decimal.NullDecimal `db:"cutting_table_length_cm"`

	// DefaultSeamAllowanceCm is the shop's ПРИПУСК НА ШОВ ПО УМОЛЧАНИЮ, in centimetres (Ф3.2) — the
	// second tenant, and the one the header above predicted.
	//
	// INVALID (NULL) means «not configured», the same law as the table length. But ZERO IS LEGAL HERE
	// and states something specific — «our выкройки carry the cut line, no offset needed» — which is
	// why its validator (ValidateSeamAllowanceStandardCm) accepts 0 while the table length's rejects
	// it. Two settings in one k/v table could not have held two opposite floors; that is precisely
	// the argument 0272 made for a typed column per setting.
	DefaultSeamAllowanceCm decimal.NullDecimal `db:"default_seam_allowance_cm"`

	UpdatedBy string    `db:"updated_by"`
	UpdatedAt time.Time `db:"updated_at"`
}

// WorkshopSettingsPatch is a PARTIAL write of the workshop settings: a nil field means the request
// did not mention that setting and the stored value is carried forward untouched.
//
// Each setting is therefore TRI-STATE, and all three states are reachable:
//
//	nil                                → leave the stored value alone
//	&decimal.NullDecimal{Valid: false} → clear the setting back to "not configured"
//	&decimal.NullDecimal{Valid: true}  → set it to that value
//
// The middle state is not decoration: an operator who mistyped the table length must be able to
// return it to "unknown" rather than leave a wrong number standing, because a wrong number produces
// confident false verdicts while an unset one produces none.
type WorkshopSettingsPatch struct {
	CuttingTableLengthCm   *decimal.NullDecimal
	DefaultSeamAllowanceCm *decimal.NullDecimal
}

// IsEmpty reports whether the patch names no setting at all. Such a request is rejected rather than
// executed: it would write nothing but still stamp updated_by/updated_at, putting a fake edit in the
// audit trail.
func (p WorkshopSettingsPatch) IsEmpty() bool {
	return p.CuttingTableLengthCm == nil && p.DefaultSeamAllowanceCm == nil
}

// Plausibility band for a cutting/spreading table length, in centimetres.
//
// The ceiling is a data-integrity statement and is repeated verbatim by the named CHECK
// chk_workshop_settings_table_length in migration 0272 — the two must move together. The longest
// industrial spreading tables in existence run 10-30 m, with custom installations reaching about
// 50 m; past that the input is a unit mistake or a stray zero, not a table.
//
// The floor lives ONLY here, deliberately, and is not a DB CHECK. It is a judgement about operator
// input, not an invariant about stored data: the single most likely mistake on this field is typing
// METRES into a field labelled centimetres ("6" for a 6 m table), and a bare "> 0" rule accepts 6 cm
// happily and then declares every раскладка too long — the exact silent-wrong-verdict failure this
// setting exists to prevent. Rejecting anything under half a metre catches every such typo up to a
// 50 m table while still admitting the smallest bench anyone could plausibly cut on. Keeping it out
// of the schema means a stored row that predates or sidesteps this rule stays readable, and the
// number can be retuned without a migration.
const (
	MinCuttingTableLengthCm = 50
	MaxCuttingTableLengthCm = 5000
)

// ValidateCuttingTableLengthCm checks an incoming table length against the plausibility band above.
// An INVALID (unset) value is accepted — clearing the setting is legal; only a value that is being
// SET has to be plausible.
func ValidateCuttingTableLengthCm(v decimal.NullDecimal) error {
	const field = "cutting_table_length_cm"
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -2 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 2 decimal places — the column stores no more, so the extra digits would be lost silently")
	}
	if v.Decimal.LessThanOrEqual(decimal.Zero) {
		return NewFieldViolation(field, "must_be_positive", v.Decimal.String(),
			"enter the table length in centimetres; to record that the workshop has no configured table, clear the setting instead of entering 0")
	}
	if v.Decimal.LessThan(decimal.NewFromInt(MinCuttingTableLengthCm)) {
		return NewFieldViolation(field, "implausibly_short", v.Decimal.String(),
			"the value is in CENTIMETRES — a 6 m table is 600, not 6")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxCuttingTableLengthCm)) {
		return NewFieldViolation(field, "implausibly_long", v.Decimal.String(),
			"the value is in CENTIMETRES and the longest spreading tables that exist are about 50 m (5000); check for a stray zero or millimetres")
	}
	return nil
}
