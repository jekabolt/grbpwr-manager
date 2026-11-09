package entity

import (
	"database/sql"
	"fmt"
)

// Who makes an auxiliary card's item (tech_card.aux_made_by, 0456).
const (
	// AuxMadeByUs: made from the card's BOM. Its norms are costed, planned, reserved and issued.
	AuxMadeByUs = "us"
	// AuxMadeBySupplier: bought finished. The supplier's price is the cost; nothing is reserved or
	// issued, even when norms are filled.
	AuxMadeBySupplier = "supplier"
)

// ValidAuxMadeBy is the value set of tech_card.aux_made_by (chk_tech_card_aux_made_by).
var ValidAuxMadeBy = map[string]bool{
	AuxMadeByUs:       true,
	AuxMadeBySupplier: true,
}

// IsAuxiliary reports whether the card produces an auxiliary item rather than a product.
func (tc *TechCardInsert) IsAuxiliary() bool {
	return tc != nil && tc.Purpose == TechCardPurposeAuxiliary
}

// AuxBoughtFinished reports whether an auxiliary card's item is bought from a supplier. Only an
// explicit 'supplier' says so: NULL on an aux card reads as made by us.
func (tc *TechCardInsert) AuxBoughtFinished() bool {
	return tc.IsAuxiliary() && tc.AuxMadeBy.Valid && tc.AuxMadeBy.String == AuxMadeBySupplier
}

// AuxRecipe is an auxiliary card's input recipe in the shape every colourway reader already takes:
// one garment-level usage per BOM line. An aux card has no colourways, so its BOM is its recipe and
// the norm per output unit lives on the line (0456).
//
//   - nil for a sellable card: its recipes are its colourways.
//   - Made by supplier: a recipe with no usages. The item is bought, so no material is costed,
//     planned or issued even when norms are filled.
//   - Measured section: Consumption = consumption_per_unit. NULL stays NULL, so the line reads as
//     unpriced and as a NoNorm blocker, never as zero.
//   - Counted section: the usage carries no number, and the slot's qty_per_garment + spare_qty
//     answer through the countable pair resolver, exactly as on a colourway row.
//   - A geometry-derived line takes its composed norm through ResolveDerivedLineUsages.
//     Aux is size-less, so only size-independent contributors give a norm.
//
// The recipe is built fresh and never written; tc is not modified. Usage Id 0 = not persisted.
func AuxRecipe(tc *TechCardInsert) *TechCardColorway {
	if !tc.IsAuxiliary() {
		return nil
	}
	recipe := &TechCardColorway{Name: tc.Name}
	if tc.AuxBoughtFinished() {
		return recipe
	}
	usages := make([]TechCardColorwayUsage, 0, len(tc.BomItems))
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		u := TechCardColorwayUsage{
			BomLineKey:   b.LineKey,
			BomItemIndex: sql.NullInt32{Int32: int32(i), Valid: true},
		}
		if b.Id > 0 {
			u.BomItemId = sql.NullInt64{Int64: int64(b.Id), Valid: true}
		}
		if !IsCountableSection(b.Section) {
			u.Consumption = b.ConsumptionPerUnit
		}
		usages = append(usages, u)
	}
	topstitch := TopstitchLines(tc.Operations)
	usages = ResolveDerivedLineUsages(usages, tc.BomItems, DerivedLineNorms(tc.BomItems, tc.CutStrips, topstitch, nil))
	for i := range usages {
		// Aux is size-less: a per-size derived norm has no size to answer for.
		usages[i].SizeConsumptions = nil
	}
	recipe.Usages = usages
	return recipe
}

// ValidateAuxBomNorms holds the write rules of consumption_per_unit that need the whole card: it is
// for auxiliary cards only, for measured sections only, and never on a geometry-derived line.
// Only a value present on the wire is judged; an omitted one keeps the
// stored value and is inert wherever AuxRecipe does not read it. Range and scale are checked at
// parse time.
func ValidateAuxBomNorms(tc *TechCardInsert) error {
	if tc == nil {
		return nil
	}
	stripLines := auxCutStripLines(tc)
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if b.ConsumptionPerUnitOmitted || !b.ConsumptionPerUnit.Valid {
			continue
		}
		field := fmt.Sprintf("bom_items[%d].consumption_per_unit", i)
		switch {
		case !tc.IsAuxiliary():
			return NewFieldViolation(field, "aux_only", "",
				"a norm per unit is only for auxiliary cards; a sellable card's norms live on its colourways")
		case IsCountableSection(b.Section):
			return NewFieldViolation(field, "measured_only", "",
				"a counted line takes qty_per_garment and spare_qty")
		case auxLineCutToLength(b, stripLines):
			return NewFieldViolation(field, "cut_to_length", "",
				"a line cut to length takes its norm from its strips")
		}
	}
	return nil
}

// AuxBomLinesMissingNorm returns the indices of an own-made aux card's BOM lines that its recipe
// gives no norm. It is judged on the RESOLVED recipe (AuxRecipe), not on the line's flags: a line
// cut to length has a norm only when its strips give a size-less one (every strip one length, its
// length known, bought in metres), so an incomplete or graded strip line is reported like a
// measured line with no consumption_per_unit. A counted line that is not cut to length is skipped:
// its qty_per_garment answers. nil for a sellable or a bought card.
func AuxBomLinesMissingNorm(tc *TechCardInsert) []int {
	if tc.AuxBoughtFinished() {
		return nil
	}
	recipe := AuxRecipe(tc)
	if recipe == nil {
		return nil
	}
	byID := make(map[int]int, len(tc.BomItems))
	for i := range tc.BomItems {
		if tc.BomItems[i].Id > 0 {
			byID[tc.BomItems[i].Id] = i
		}
	}
	normed := make([]bool, len(tc.BomItems))
	for i := range recipe.Usages {
		u := &recipe.Usages[i]
		idx := -1
		switch {
		case u.BomItemIndex.Valid:
			idx = int(u.BomItemIndex.Int32)
		case u.BomItemId.Valid:
			if j, ok := byID[int(u.BomItemId.Int64)]; ok {
				idx = j
			}
		}
		if idx >= 0 && idx < len(normed) && u.Consumption.Valid {
			normed[idx] = true
		}
	}
	stripLines := auxCutStripLines(tc)
	var out []int
	for i := range tc.BomItems {
		b := &tc.BomItems[i]
		if normed[i] || (IsCountableSection(b.Section) && !auxLineCutToLength(b, stripLines)) {
			continue
		}
		out = append(out, i)
	}
	return out
}

// auxCutStripLines is the set of BOM lines with any geometry-derived contributor.
func auxCutStripLines(tc *TechCardInsert) map[string]bool {
	stripLines := make(map[string]bool, len(tc.CutStrips)+len(TopstitchLines(tc.Operations)))
	for i := range tc.CutStrips {
		stripLines[CutStripLineKey(tc.CutStrips[i].BomLineKey)] = true
	}
	for _, line := range TopstitchLines(tc.Operations) {
		stripLines[CutStripLineKey(line.BomLineKey)] = true
	}
	return stripLines
}

// auxLineCutToLength reports a line whose norm comes from measured geometry.
func auxLineCutToLength(b *TechCardBomItem, stripLines map[string]bool) bool {
	return b.CutToLength || (b.LineKey != "" && stripLines[CutStripLineKey(b.LineKey)])
}
