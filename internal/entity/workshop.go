package entity

import (
	"database/sql"
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
// Ф3.2 (припуск по умолчанию), Ф6.9 (режим гейта готовности) and Ф4.8 (предел высоты стопки) have
// since moved in, each as its own typed column; 08-cut-out (минимальный зазор) is the next tenant.
type WorkshopSettings struct {
	// CuttingTableLengthCm is the usable length of the cutting/spreading table, in centimetres.
	//
	// INVALID (NULL in the column) means the workshop has not configured a table, and that is NOT
	// the same as zero. A reader must render it as "no length verdict available", never as a
	// comparison against 0 — a workshop that simply has not filled this in must not be told that
	// every marker it lays is too long. Callers: check .Valid before comparing.
	CuttingTableLengthCm decimal.NullDecimal `db:"cutting_table_length_cm"`

	// DefaultSeamAllowanceMm is the shop's ПРИПУСК НА ШОВ ПО УМОЛЧАНИЮ, in centimetres (Ф3.2) — the
	// second tenant, and the one the header above predicted.
	//
	// INVALID (NULL) means «not configured», the same law as the table length. But ZERO IS LEGAL HERE
	// and states something specific — «our выкройки carry the cut line, no offset needed» — which is
	// why its validator (ValidateSeamAllowanceStandardCm) accepts 0 while the table length's rejects
	// it. Two settings in one k/v table could not have held two opposite floors; that is precisely
	// the argument 0272 made for a typed column per setting.
	DefaultSeamAllowanceMm decimal.NullDecimal `db:"default_seam_allowance_mm"`

	// RunReadinessBlocking is the mode of the production-run readiness gate (Ф6.1/Ф6.9, 0279) — the
	// third tenant, and the one that breaks this house's own rule on purpose.
	//
	// EVERY OTHER SETTING HERE MEANS «NO VERDICT» WHEN UNSET, AND THIS ONE DOES NOT. A cutting table
	// of unknown length lets a consumer say nothing; a create-run command has no «say nothing»
	// available — it either refuses or it does not. So INVALID (and false) mean REPORT-ONLY: compute
	// everything, show everything, log it, and let the run through.
	//
	// The reason is arithmetic rather than taste. On the day Ф6 ships, not one card in the portfolio
	// carries a norm with recorded measurement conditions (Ф3 declared every pre-existing раскладка
	// «старая норма»), so a rule of «no verdict ⇒ refuse» would have refused every run in the shop.
	// A setting whose default stops the factory does not get created.
	//
	// Readers go through entity.RunReadinessBlocking, never through this field directly, so the
	// unset→report-only reading exists in exactly one place.
	RunReadinessBlocking sql.NullBool `db:"run_readiness_blocking"`

	// MaxStackHeightCm is the ПРЕДЕЛ ВЫСОТЫ СТОПКИ, in centimetres (Ф4.8, 0283) — the fourth tenant,
	// and the house rule returns to normal after the one above broke it: INVALID (NULL) means «not
	// configured» and therefore NO VERDICT, never a comparison against zero.
	//
	// CENTIMETRES, NOT A PLY COUNT, and that is why it belongs to the room rather than to a marker:
	// 30 слоёв шифона is 2 cm and 30 слоёв драпа is 30 cm, so a ply limit would mean four different
	// things on four fabrics. What actually stops the лежание is the blade, and a blade cuts a HEIGHT.
	//
	// ONE limit for the whole shop (решение Р5). Пер-ножевые пределы are a refinement with no data
	// behind it today, and this house does not create a column nothing can fill. The article's half of
	// the check is Material.FabricThicknessMm; either half missing ⇒ UNKNOWN.
	MaxStackHeightCm decimal.NullDecimal `db:"max_stack_height_cm"`

	UpdatedBy string    `db:"updated_by"`
	UpdatedAt time.Time `db:"updated_at"`
}

// EffectiveMaxStackHeightCm is the shop's stack-height limit, or INVALID when none is configured.
// The invalid answer is the whole point and must be carried through to the operator as «предел
// стопки не настроен» with a way to go and set it — a caller that folds it to zero turns «мы этого
// не знаем» into «ни один настил не проходит».
//
// A stored value at or below zero reads as unset: the named CHECK in 0283 already refuses it, and
// this guards in-memory rows that never went through the column.
func (s *WorkshopSettings) EffectiveMaxStackHeightCm() decimal.NullDecimal {
	if s == nil || !s.MaxStackHeightCm.Valid || s.MaxStackHeightCm.Decimal.LessThanOrEqual(decimal.Zero) {
		return decimal.NullDecimal{}
	}
	return s.MaxStackHeightCm
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
	DefaultSeamAllowanceMm *decimal.NullDecimal
	// RunReadinessBlocking is TWO-state rather than three, and the missing state is deliberate:
	//
	//	nil    → the request did not mention the mode; leave it alone
	//	&true  → blocking
	//	&false → report-only
	//
	// There is no «clear it back to unset», because unset and false behave identically (see
	// WorkshopSettings.RunReadinessBlocking) — a third state that changes nothing is a control an
	// operator can only be confused by. The column stays NULLable so the DEFAULT state of a workshop
	// that never touched the switch is distinguishable from one that turned it off deliberately, which
	// is an audit fact, not a behavioural one.
	RunReadinessBlocking *bool
	// MaxStackHeightCm (Ф4.8) is three-state like its decimal neighbours above. The middle state is
	// load-bearing here too: a shop that typed 3 when it meant 30 must be able to go back to «не
	// настроено», because a wrong limit fails настилы that are perfectly fine while an unset one
	// simply declines to judge them.
	MaxStackHeightCm *decimal.NullDecimal
}

// IsEmpty reports whether the patch names no setting at all. Such a request is rejected rather than
// executed: it would write nothing but still stamp updated_by/updated_at, putting a fake edit in the
// audit trail.
//
// EVERY NEW TENANT MUST BE ADDED HERE. The list is hand-written and there is no compiler help: a
// setting missing from it makes a request that names ONLY that setting look empty, and the write is
// refused with «name at least one setting» while the client is quite sure it named one.
// TestWorkshopSettingsPatchIsEmptyCoversEveryField holds the line by reflection.
func (p WorkshopSettingsPatch) IsEmpty() bool {
	return p.CuttingTableLengthCm == nil && p.DefaultSeamAllowanceMm == nil &&
		p.RunReadinessBlocking == nil && p.MaxStackHeightCm == nil
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

// Plausibility band for the предел высоты стопки, in centimetres (Ф4.8).
//
// The ceiling is repeated verbatim by the named CHECK chk_workshop_settings_stack_height in 0283 —
// the two must move together. The longest straight knife reaches about 30 cm, so 100 is not «бывает
// редко», it is «столько не бывает»: past it the input is a unit mistake, not a workshop.
//
// There is NO floor beyond «positive», unlike the table length, and the asymmetry is deliberate. The
// table's floor exists because metres typed into a centimetre field (6 for a 6 m table) is both
// likely and silently catastrophic. Here the analogous slip has no room to hide: a stack limit is a
// single-digit-to-low-double-digit number in centimetres already, so any floor worth writing would
// start rejecting real workshops — 2 cm is a perfectly honest limit for someone cutting chiffon with
// a light blade.
const MaxStackHeightCm = 100

// ValidateMaxStackHeightCm checks an incoming stack limit against the band above. An INVALID (unset)
// value is accepted: clearing the setting is legal and is the ONLY way to say «у нас нет предела».
//
// ZERO IS REFUSED, and this is the fork where this setting sides with the table length rather than
// with the seam allowance. A 0 cm allowance states something real («наши выкройки несут линию
// кроя»); a 0 cm stack limit states that no настил may ever be laid, which is not a configuration
// anybody means to enter. The way to stop the check is to remove the limit, not to set it to a
// number that fails everything — the first withholds a verdict, the second manufactures a false one.
func ValidateMaxStackHeightCm(v decimal.NullDecimal) error {
	const field = "max_stack_height_cm"
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -2 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 2 decimal places — the column stores no more, so the extra digits would be lost silently")
	}
	if v.Decimal.LessThanOrEqual(decimal.Zero) {
		return NewFieldViolation(field, "must_be_positive", v.Decimal.String(),
			"enter the limit in centimetres; to record that the workshop has no stack limit, clear the setting instead of entering 0 — an unset limit withholds the verdict, a zero one fails every настил")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxStackHeightCm)) {
		return NewFieldViolation(field, "implausibly_tall", v.Decimal.String(),
			"the value is in CENTIMETRES and the longest cutting knives reach about 30 cm, so anything past 100 is a unit mistake")
	}
	return nil
}
