package entity

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"

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

	// CuttingTableWidthCm is the usable WIDTH of the cutting/spreading table, in centimetres (0413) —
	// the length's partner, so the marker modal can draw the table to scale and show whether the
	// fabric width fits across it. Same house law: INVALID (NULL) = не настроено ⇒ no width verdict,
	// never a comparison against zero.
	CuttingTableWidthCm decimal.NullDecimal `db:"cutting_table_width_cm"`

	// Costing / label DEFAULTS (0414). Unlike the tenants above these are not physical constants of
	// the room but the shop's house numbers, read ONLY to PREFILL a new costing and the «made in»
	// line of a label — never as a fallback at compute time. A tech card's own costing stays the
	// source of truth once written; changing a default here re-prices nothing.
	//
	// Same house law: INVALID (NULL) = не настроено ⇒ no prefill, the field is left blank for the
	// operator. A zero percentage IS a real setting («у нас нет брака в смете»), unlike the rate.
	//
	// CostingCurrency is the ISO 4217 code every money default below is denominated in (any expense
	// currency, so USDT is legal — the column is VARCHAR(4) like every currency since 0185).
	CostingCurrency sql.NullString `db:"costing_currency"`
	// LabourRatePerMin is the cost of one production minute (SAM) in CostingCurrency.
	LabourRatePerMin decimal.NullDecimal `db:"labour_rate_per_min"`
	// OverheadPct is overhead as a percentage of the direct cost, 0..1000.
	OverheadPct decimal.NullDecimal `db:"overhead_pct"`
	// DefectPercent is the брак / запас allowance, 0..100 — same band as TechCardCosting.defect_percent.
	DefectPercent decimal.NullDecimal `db:"defect_percent"`
	// TargetMarginPct is the gross-margin target, 0..100 — same band as TechCardCosting.target_margin_pct.
	TargetMarginPct decimal.NullDecimal `db:"target_margin_pct"`
	// CountryOfOrigin is the ISO 3166-1 alpha-2 country the shop produces in («made in»), uppercase.
	CountryOfOrigin sql.NullString `db:"country_of_origin"`

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
	// CuttingTableWidthCm (0413) is three-state like the table length it sits next to.
	CuttingTableWidthCm *decimal.NullDecimal
	// Costing / label defaults (0414) — all three-state. For the two strings an INVALID NullString
	// is the clear.
	CostingCurrency  *sql.NullString
	LabourRatePerMin *decimal.NullDecimal
	OverheadPct      *decimal.NullDecimal
	DefectPercent    *decimal.NullDecimal
	TargetMarginPct  *decimal.NullDecimal
	CountryOfOrigin  *sql.NullString
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
		p.RunReadinessBlocking == nil && p.MaxStackHeightCm == nil &&
		p.CuttingTableWidthCm == nil &&
		p.CostingCurrency == nil && p.LabourRatePerMin == nil && p.OverheadPct == nil &&
		p.DefectPercent == nil && p.TargetMarginPct == nil && p.CountryOfOrigin == nil
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
			"enter the limit in centimetres; to record that the workshop has no stack limit, clear the setting instead of entering 0 — an unset limit withholds the verdict, a zero one fails every lay")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxStackHeightCm)) {
		return NewFieldViolation(field, "implausibly_tall", v.Decimal.String(),
			"the value is in CENTIMETRES and the longest cutting knives reach about 30 cm, so anything past 100 is a unit mistake")
	}
	return nil
}

// Plausibility band for the cutting/spreading table WIDTH, in centimetres (0413).
//
// The ceiling is repeated verbatim by the named CHECK chk_workshop_settings_table_width in 0413 — the
// two must move together. Real spreading tables are 120–250 cm wide (fabric up to ~320 cm); past
// 10 m it is a stray zero or millimetres, not a table. The floor lives only here, for the same reason
// as the length's: metres typed into a centimetre field ("1.8" for a 1.8 m table) must be caught.
const (
	MinCuttingTableWidthCm = 50
	MaxCuttingTableWidthCm = 1000
)

// ValidateCuttingTableWidthCm checks an incoming table width against the band above. An INVALID
// (unset) value is accepted — clearing the setting is legal.
func ValidateCuttingTableWidthCm(v decimal.NullDecimal) error {
	const field = "cutting_table_width_cm"
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -2 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 2 decimal places — the column stores no more, so the extra digits would be lost silently")
	}
	if v.Decimal.LessThanOrEqual(decimal.Zero) {
		return NewFieldViolation(field, "must_be_positive", v.Decimal.String(),
			"enter the table width in centimetres; to record that the width is not configured, clear the setting instead of entering 0")
	}
	if v.Decimal.LessThan(decimal.NewFromInt(MinCuttingTableWidthCm)) {
		return NewFieldViolation(field, "implausibly_narrow", v.Decimal.String(),
			"the value is in CENTIMETRES — a 1.8 m wide table is 180, not 1.8")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxCuttingTableWidthCm)) {
		return NewFieldViolation(field, "implausibly_wide", v.Decimal.String(),
			"the value is in CENTIMETRES and spreading tables are about 120–250 cm wide; check for a stray zero or millimetres")
	}
	return nil
}

// Bands for the costing / label defaults (0414). They live ONLY here, not in the schema: the owner's
// rule for this table's later tenants is «no CHECK on an existing table», and a band is a judgement
// about operator input that must stay retunable without a migration.
const (
	// MaxLabourRatePerMin is a stray-zero ceiling, generous enough for the weakest supported currency
	// (a KRW minute is a few hundred).
	MaxLabourRatePerMin = 100000
	// MaxOverheadPct: overhead on direct cost can honestly exceed 100 % in a small atelier; past
	// 1000 % it is a typo.
	MaxOverheadPct = 1000
	// MaxDefectPercent / MaxTargetMarginPct repeat the tech-card costing bands (dto parseTechCardCosting)
	// so a prefilled value is always one the costing form will accept on save.
	MaxDefectPercent   = 100
	MaxTargetMarginPct = 100
)

// ValidateWorkshopCostingCurrency accepts an unset value (clear) or an upper-case ISO 4217 code the
// shop can denominate an expense in — the same predicate the tech-card costing currency uses.
func ValidateWorkshopCostingCurrency(v sql.NullString) error {
	const field = "costing_currency"
	if !v.Valid {
		return nil
	}
	if v.String != strings.ToUpper(strings.TrimSpace(v.String)) || !currency.IsExpenseCurrency(v.String) {
		return NewFieldViolation(field, "unsupported_currency", v.String,
			"pick one of the shop's currencies (ISO 4217, e.g. EUR) or clear the setting")
	}
	return nil
}

// ValidateWorkshopCountryOfOrigin accepts an unset value (clear) or an upper-case ISO 3166-1 alpha-2
// code present in the country dictionary seed — the same set product.country_code can hold.
func ValidateWorkshopCountryOfOrigin(v sql.NullString) error {
	const field = "country_of_origin"
	if !v.Valid {
		return nil
	}
	if _, ok := iso2ToISO3[v.String]; !ok {
		return NewFieldViolation(field, "unknown_country", v.String,
			"enter an ISO 3166-1 alpha-2 country code, e.g. PT, or clear the setting")
	}
	return nil
}

// ValidateLabourRatePerMin: positive (a zero rate would prefill a costing with free labour — clear the
// setting instead), at most 4 decimal places (DECIMAL(12,4)), under the stray-zero ceiling.
func ValidateLabourRatePerMin(v decimal.NullDecimal) error {
	const field = "labour_rate_per_min"
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -4 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 4 decimal places — the column stores no more, so the extra digits would be lost silently")
	}
	if v.Decimal.LessThanOrEqual(decimal.Zero) {
		return NewFieldViolation(field, "must_be_positive", v.Decimal.String(),
			"enter the cost of one production minute; to have no default, clear the setting instead of entering 0")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(MaxLabourRatePerMin)) {
		return NewFieldViolation(field, "implausibly_high", v.Decimal.String(),
			"the rate is PER MINUTE, not per hour or per garment; check for a stray zero")
	}
	return nil
}

// ValidateWorkshopPercent checks a percentage default: 0 is legal, at most 2 decimal places
// (DECIMAL(6,2)), not above max.
func ValidateWorkshopPercent(field string, v decimal.NullDecimal, max int64) error {
	if !v.Valid {
		return nil
	}
	if v.Decimal.Exponent() < -2 {
		return NewFieldViolation(field, "too_many_decimal_places", v.Decimal.String(),
			"round to at most 2 decimal places — the column stores no more, so the extra digits would be lost silently")
	}
	if v.Decimal.LessThan(decimal.Zero) {
		return NewFieldViolation(field, "must_not_be_negative", v.Decimal.String(),
			"enter a percentage from 0 up, or clear the setting")
	}
	if v.Decimal.GreaterThan(decimal.NewFromInt(max)) {
		return NewFieldViolation(field, "out_of_range", v.Decimal.String(),
			fmt.Sprintf("the value is a PERCENTAGE and must be at most %d", max))
	}
	return nil
}
