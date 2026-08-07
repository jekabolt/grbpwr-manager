package entity

import "strings"

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
