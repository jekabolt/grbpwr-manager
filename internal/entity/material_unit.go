package entity

import (
	"strings"

	"github.com/shopspring/decimal"
)

// MaterialUnit is the CLOSED vocabulary a material quantity may be measured in (Ф5а.3). Until this
// existed every unit was free text and every consumer compared it as a raw string: the production
// material plan held its own hard-coded metre synonym set and degraded into a caveat on any other
// mismatch, so a slot spelled «м» against an article spelled "m" was treated as two different units
// — the number kept the slot's meaning while being compared against the article's stock. That is
// the silently-wrong addition this vocabulary cuts.
//
// The canonical spelling of each unit IS the stored value: `unit` stays one free-text column and the
// vocabulary is a normalisation over it, so a value nobody can map keeps working exactly as before
// (see NormalizeMaterialUnit). The proto enum is derived from this table, never the other way round.
type MaterialUnit string

const (
	MaterialUnitM    MaterialUnit = "m"    // running metre — the unit roll goods are bought and cut in
	MaterialUnitCm   MaterialUnit = "cm"   //
	MaterialUnitMm   MaterialUnit = "mm"   //
	MaterialUnitM2   MaterialUnit = "m2"   // square metre (sheet goods)
	MaterialUnitG    MaterialUnit = "g"    //
	MaterialUnitKg   MaterialUnit = "kg"   // fabric bought by weight (Ф5а.4)
	MaterialUnitPcs  MaterialUnit = "pcs"  // countable trim
	MaterialUnitPair MaterialUnit = "pair" //
	MaterialUnitSet  MaterialUnit = "set"  //
	MaterialUnitCone MaterialUnit = "cone" // thread cone (length_per_cone_m metres each)
	MaterialUnitRoll MaterialUnit = "roll" //
)

// materialUnitSynonyms maps a lower-cased, trimmed free-text unit to its canonical MaterialUnit.
//
// The METRE row is the pre-existing synonym set lifted verbatim out of dto.planLengthUnits (the
// production material plan's private map) — it is deliberately NOT re-derived or extended, because
// that set is what every legacy metre value was already being matched against. The other rows are
// the canonical spelling plus the unambiguous same-word Cyrillic abbreviation; nothing is inferred
// from context, and anything absent here stays unmapped rather than being guessed into a unit whose
// quantities would then be summed with somebody else's.
var materialUnitSynonyms = map[string]MaterialUnit{
	// metres — the existing set (dto.planLengthUnits), unchanged
	"m": MaterialUnitM, "м": MaterialUnitM,
	"meter": MaterialUnitM, "meters": MaterialUnitM,
	"metre": MaterialUnitM, "metres": MaterialUnitM,

	"cm": MaterialUnitCm, "см": MaterialUnitCm,
	"mm": MaterialUnitMm, "мм": MaterialUnitMm,
	"m2": MaterialUnitM2, "m²": MaterialUnitM2, "sqm": MaterialUnitM2,
	"g": MaterialUnitG, "г": MaterialUnitG,
	"kg": MaterialUnitKg, "кг": MaterialUnitKg,
	"pcs": MaterialUnitPcs, "pc": MaterialUnitPcs, "шт": MaterialUnitPcs, "шт.": MaterialUnitPcs,
	"pair": MaterialUnitPair, "пара": MaterialUnitPair,
	"set":  MaterialUnitSet,
	"cone": MaterialUnitCone, "бобина": MaterialUnitCone,
	"roll": MaterialUnitRoll, "рулон": MaterialUnitRoll,
}

// MaterialUnitSynonyms returns a copy of the vocabulary's synonym → canonical table. It exists so
// the migration lint can diff migration 0271's CASE mapping against this one file — the migration is
// what rewrote the stored catalogue values, and a synonym added here but not there (or the reverse)
// would leave the DB and the server disagreeing about what a unit is.
func MaterialUnitSynonyms() map[string]MaterialUnit {
	out := make(map[string]MaterialUnit, len(materialUnitSynonyms))
	for k, v := range materialUnitSynonyms {
		out[k] = v
	}
	return out
}

// NormalizeMaterialUnit resolves a free-text unit to its canonical MaterialUnit. ok=false for an
// empty value and for anything the vocabulary does not know — the caller must then keep treating
// the raw string as opaque. Guessing here would be worse than the free text it replaces: a wrong
// canonical unit makes two genuinely different quantities addable.
func NormalizeMaterialUnit(s string) (MaterialUnit, bool) {
	u, ok := materialUnitSynonyms[strings.ToLower(strings.TrimSpace(s))]
	return u, ok
}

// IsCountableMaterialUnit reports whether quantities in s must be whole numbers.
func IsCountableMaterialUnit(s string) bool {
	u, ok := NormalizeMaterialUnit(s)
	if !ok {
		return false
	}
	switch u {
	case MaterialUnitPcs, MaterialUnitPair, MaterialUnitSet, MaterialUnitCone, MaterialUnitRoll:
		return true
	default:
		return false
	}
}

// CanonicalMaterialUnit returns the canonical spelling of a free-text unit, or the trimmed original
// when the vocabulary does not know it — the Go twin of migration 0271's CASE plus its `ELSE unit`
// arm, which is why it is worth having and testing even though nothing calls it at runtime.
//
// NO WRITE PATH MAY CALL THIS, and that is deliberate. A version of Ф5а.3 did canonicalise
// material.unit on save; it was reverted. Nothing in the codebase branches on a literal unit string
// — every consumer (pbMaterialUnit, checkMaterialUnitChange, the UpsertOutputVariant sibling check,
// pinShadowBom, the material plan) normalises on READ through NormalizeMaterialUnit /
// SameMaterialUnit — so rewriting stored text buys no behaviour at all, while a caller that saves
// "pc" and reads back "pcs" has had its data silently altered. It is the same trade that already
// keeps tech_card_bom_item.unit alone. Canonical storage is 0271's ONE-TIME job on legacy rows, not
// a standing policy.
func CanonicalMaterialUnit(s string) string {
	if u, ok := NormalizeMaterialUnit(s); ok {
		return string(u)
	}
	return strings.TrimSpace(s)
}

// SameMaterialUnit reports whether two free-text units denote the SAME unit. Both known → compare
// canonical forms («м» == "m"); otherwise fall back to the case-insensitive raw compare that was
// the only rule before, so an unmapped pair behaves exactly as it did. An empty side is never
// "same" — the caller decides what a missing unit means.
func SameMaterialUnit(a, b string) bool {
	at, bt := strings.TrimSpace(a), strings.TrimSpace(b)
	if at == "" || bt == "" {
		return false
	}
	ua, aok := NormalizeMaterialUnit(at)
	ub, bok := NormalizeMaterialUnit(bt)
	if aok && bok {
		return ua == ub
	}
	return strings.EqualFold(at, bt)
}

// ResolveBomLineUnit applies the one exception to a linked BOM line's usual article-first identity
// rule. A THREAD slot authored in metres keeps its slot unit because its norm is always metres; the
// linked article may be bought in cones, pieces, centimetres or another purchase unit. Every other
// line keeps the catalog article's unit when present, falling back to the stored slot unit.
func ResolveBomLineUnit(section TechCardBomSection, slotUnit, articleUnit string) string {
	if section == BomSectionThread {
		if u, ok := NormalizeMaterialUnit(slotUnit); ok && u == MaterialUnitM {
			return slotUnit
		}
	}
	if strings.TrimSpace(articleUnit) != "" {
		return articleUnit
	}
	return slotUnit
}

type threadArticleUnitKind uint8

const (
	threadArticleUnitInvalid threadArticleUnitKind = iota
	threadArticleUnitExactLength
	threadArticleUnitPackage
)

// threadArticleUnit classifies only what is safe for a metre-based thread slot. Known linear units
// have exact factors; count/package units use length_per_cone_m as metres per purchase unit. Unknown
// free text is treated as a package label unless it is recognisably an area, volume or mass unit.
// This preserves labels such as spool, bobbin and box without ever interpreting kg or m² as thread
// length merely because a stale length_per_cone_m happens to exist.
func threadArticleUnit(raw string) threadArticleUnitKind {
	unit := strings.ToLower(strings.TrimSpace(raw))
	if unit == "" {
		return threadArticleUnitInvalid
	}
	if u, ok := NormalizeMaterialUnit(unit); ok {
		switch u {
		case MaterialUnitM, MaterialUnitCm, MaterialUnitMm:
			return threadArticleUnitExactLength
		case MaterialUnitM2, MaterialUnitG, MaterialUnitKg:
			return threadArticleUnitInvalid
		default:
			return threadArticleUnitPackage
		}
	}
	if incompatibleThreadPhysicalUnits[unit] || strings.Contains(unit, "square") ||
		strings.Contains(unit, "cubic") {
		return threadArticleUnitInvalid
	}
	return threadArticleUnitPackage
}

var incompatibleThreadPhysicalUnits = map[string]bool{
	// area
	"cm2": true, "cm²": true, "mm2": true, "mm²": true, "km2": true, "km²": true,
	"sq m": true, "sq cm": true, "sq mm": true, "sq ft": true, "sq yd": true,
	// mass
	"mg": true, "milligram": true, "milligrams": true,
	"gram": true, "grams": true, "kilogram": true, "kilograms": true,
	"t": true, "ton": true, "tons": true, "tonne": true, "tonnes": true,
	"oz": true, "ounce": true, "ounces": true, "lb": true, "lbs": true, "pound": true, "pounds": true,
	// volume
	"l": true, "lt": true, "ltr": true, "litre": true, "litres": true, "liter": true, "liters": true,
	"ml": true, "cl": true, "dl": true, "m3": true, "m³": true, "cm3": true, "cm³": true,
	"mm3": true, "mm³": true, "gal": true, "gallon": true, "gallons": true,
}

// ThreadArticleUsesMetreSlot reports whether a new THREAD line linked to article is authored in
// metres. Package units qualify even while their length is missing: the slot is still born in
// metres, but its price remains empty until MetresPerArticleUnit can resolve a factor.
func ThreadArticleUsesMetreSlot(article MaterialWithPrice) bool {
	return threadArticleUnit(article.Unit.String) != threadArticleUnitInvalid
}

// MetresPerArticleUnit returns how many metres one purchase unit of a thread article contains.
// Exact linear units do not need thread attributes. Count/package/free-text units use the thread
// length attribute as metres per purchase unit. Area, volume, mass and empty units never convert.
func MetresPerArticleUnit(article MaterialWithPrice) (decimal.Decimal, bool) {
	switch threadArticleUnit(article.Unit.String) {
	case threadArticleUnitExactLength:
		u, _ := NormalizeMaterialUnit(article.Unit.String)
		switch u {
		case MaterialUnitM:
			return decimal.NewFromInt(1), true
		case MaterialUnitCm:
			return decimal.New(1, -2), true
		case MaterialUnitMm:
			return decimal.New(1, -3), true
		}
	case threadArticleUnitPackage:
		if article.ThreadAttr != nil && article.ThreadAttr.LengthPerConeM.Valid &&
			article.ThreadAttr.LengthPerConeM.Decimal.IsPositive() {
			return article.ThreadAttr.LengthPerConeM.Decimal, true
		}
	}
	return decimal.Zero, false
}

// PricePerMetre converts a purchase-unit price to the DECIMAL(…,4) scale stored by BOM unit_price.
// shopspring's Round is half away from zero; prices are non-negative, so this is decimal half-up.
func PricePerMetre(price, metresPerArticleUnit decimal.Decimal) decimal.Decimal {
	if !metresPerArticleUnit.IsPositive() {
		return decimal.Zero
	}
	return price.Div(metresPerArticleUnit).Round(4)
}
