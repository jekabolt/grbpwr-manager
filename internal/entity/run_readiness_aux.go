package entity

import "fmt"

// AuxRunReadinessFindings is the readiness of an auxiliary card's run. The cloth-norm gate stays
// off (an aux card has no colourways and makes a material); what is judged instead is what the
// aux run itself needs:
//
//   - an output to receipt into — BLOCKER without one, the same degree as a sellable card's blocker;
//   - made by us: a norm per unit on every measured BOM line that is not cut to length — WARNING
//     per line, since the run still makes sense and the line is only left uncosted and unissued;
//   - bought: a manual or purchase price on every output material — WARNING per material, the run
//     cost then gets no supplier price article and the operator types it.
//
// supplierPrices holds the supplier price of each output material that has one (a bought card
// only; a missing key is «no price»). Every row carries the card id; a gap row also names its line
// or material.
func AuxRunReadinessFindings(card *TechCard, supplierPrices map[int]MaterialPrice) []RunReadinessFinding {
	if card == nil || card.Purpose != TechCardPurposeAuxiliary {
		return nil
	}
	target := RunReadinessTarget{TechCardId: card.Id}
	rows := []RunReadinessFinding{{
		Key:      RunReadinessKeyCardAuxiliary,
		Severity: RunReadinessOK,
		Label:    "auxiliary card — the cloth norm gate does not apply to it",
		Target:   target,
	}}

	outputs := auxRunOutputs(card)
	if len(outputs) == 0 {
		rows = append(rows, RunReadinessFinding{
			Key:      RunReadinessKeyAuxOutputMaterial,
			Severity: RunReadinessBlocker,
			Label:    "no output material",
			Detail:   "the card has no active output colour and no output material",
			Target:   target,
		})
	} else {
		rows = append(rows, RunReadinessFinding{
			Key:      RunReadinessKeyAuxOutputMaterial,
			Severity: RunReadinessOK,
			Label:    "output material set",
			Target:   target,
		})
	}

	if card.AuxBoughtFinished() {
		// Row 1 holds: a price row needs an output to price.
		if len(outputs) == 0 {
			return rows
		}
		missing := 0
		for _, o := range outputs {
			if _, ok := supplierPrices[o.materialId]; ok {
				continue
			}
			missing++
			detail := "the output material has no manual or purchase price"
			if o.name != "" {
				detail = fmt.Sprintf("%q has no manual or purchase price", o.name)
			}
			rows = append(rows, RunReadinessFinding{
				Key:      RunReadinessKeyAuxSupplierPrice,
				Severity: RunReadinessWarning,
				Label:    "no supplier price",
				Detail:   detail,
				Target:   RunReadinessTarget{TechCardId: card.Id, MaterialId: int64(o.materialId)},
			})
		}
		if missing == 0 {
			rows = append(rows, RunReadinessFinding{
				Key:      RunReadinessKeyAuxSupplierPrice,
				Severity: RunReadinessOK,
				Label:    "supplier price set",
				Target:   target,
			})
		}
		return rows
	}

	missing := AuxBomLinesMissingNorm(&card.TechCardInsert)
	for _, i := range missing {
		b := &card.BomItems[i]
		t := RunReadinessTarget{TechCardId: card.Id, BomLineKey: b.LineKey}
		if b.Id > 0 {
			t.BomItemId = int64(b.Id)
		}
		if b.MaterialId.Valid {
			t.MaterialId = b.MaterialId.Int64
		}
		detail := fmt.Sprintf("BOM line %d has no norm per unit", i+1)
		if b.Name != "" {
			detail = fmt.Sprintf("%q has no norm per unit", b.Name)
		}
		rows = append(rows, RunReadinessFinding{
			Key:      RunReadinessKeyAuxNormPerUnit,
			Severity: RunReadinessWarning,
			Label:    "no norm per unit",
			Detail:   detail,
			Target:   t,
		})
	}
	if len(missing) == 0 {
		rows = append(rows, RunReadinessFinding{
			Key:      RunReadinessKeyAuxNormPerUnit,
			Severity: RunReadinessOK,
			Label:    "norms per unit set",
			Target:   target,
		})
	}
	return rows
}

type auxRunOutput struct {
	materialId int
	name       string
}

// AuxRunOutputMaterialIds lists the materials an aux card's run receipts into: its active output
// colours' materials, or the single output material when it has no active colour. Deduplicated,
// in colour order.
func AuxRunOutputMaterialIds(card *TechCard) []int {
	outs := auxRunOutputs(card)
	ids := make([]int, 0, len(outs))
	for _, o := range outs {
		ids = append(ids, o.materialId)
	}
	return ids
}

func auxRunOutputs(card *TechCard) []auxRunOutput {
	if card == nil {
		return nil
	}
	var out []auxRunOutput
	seen := map[int]bool{}
	for i := range card.OutputVariants {
		v := &card.OutputVariants[i]
		if !v.Active || v.MaterialId <= 0 || seen[v.MaterialId] {
			continue
		}
		seen[v.MaterialId] = true
		out = append(out, auxRunOutput{materialId: v.MaterialId, name: v.MaterialName})
	}
	if len(out) == 0 && card.OutputMaterialId.Valid && card.OutputMaterialId.Int64 > 0 {
		out = append(out, auxRunOutput{materialId: int(card.OutputMaterialId.Int64)})
	}
	return out
}
