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
//   - A line cut to length takes its strips' norm through ResolveCutStripUsages, as colourways do.
//     Aux is size-less, so only a one-length strip gives a norm; a graded strip gives none.
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
	usages = ResolveCutStripUsages(usages, tc.BomItems, CutStripLineNorms(tc.BomItems, tc.CutStrips, nil))
	for i := range usages {
		// Aux is size-less: a per-size strip norm has no size to answer for.
		usages[i].SizeConsumptions = nil
	}
	recipe.Usages = usages
	return recipe
}

// ValidateAuxBomNorms holds the write rules of consumption_per_unit that need the whole card: it is
// for auxiliary cards only, for measured sections only, and never on a line cut to length (the
// strips give that norm). Only a value present on the wire is judged; an omitted one keeps the
// stored value and is inert wherever AuxRecipe does not read it. Range and scale are checked at
// parse time.
func ValidateAuxBomNorms(tc *TechCardInsert) error {
	if tc == nil {
		return nil
	}
	stripLines := make(map[string]bool, len(tc.CutStrips))
	for i := range tc.CutStrips {
		stripLines[CutStripLineKey(tc.CutStrips[i].BomLineKey)] = true
	}
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
		case b.CutToLength || (b.LineKey != "" && stripLines[CutStripLineKey(b.LineKey)]):
			return NewFieldViolation(field, "cut_to_length", "",
				"a line cut to length takes its norm from its strips")
		}
	}
	return nil
}
