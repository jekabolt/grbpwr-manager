package dto

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// hardwareCostCard builds a minimal write payload with the given BOM sections and hardware_cost.
func hardwareCostCard(hardwareCost string, sections ...pb_common.TechCardBomSection) *pb_common.TechCardInsert {
	bom := make([]*pb_common.TechCardBomItem, 0, len(sections))
	for i, s := range sections {
		bom = append(bom, &pb_common.TechCardBomItem{Section: s, Name: []string{"shell", "zip", "thread"}[i%3]})
	}
	in := &pb_common.TechCardInsert{
		StyleNumber: "ST-HW",
		Name:        "Hardware costing",
		BomItems:    bom,
		Costing:     &pb_common.TechCardCosting{Currency: "EUR"},
	}
	if hardwareCost != "" {
		in.Costing.HardwareCost = dec(hardwareCost)
	}
	return in
}

// TestHardwareCostRejectedBesideHardwareBom locks audit #33: hardware_cost is "hardware if OUTSIDE
// the BOM", and hardware is also a first-class BOM section priced through the recipe. A card with
// both silently double-counts in every unit-cost rollup, so the write is refused with a field tag
// the costing tab can point at.
func TestHardwareCostRejectedBesideHardwareBom(t *testing.T) {
	_, err := ConvertPbTechCardInsertToEntity(hardwareCostCard("2.50",
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE))
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected field-tagged validation error, got %T: %v", err, err)
	}
	if ve.Field != "costing.hardware_cost" {
		t.Errorf("violation field = %q, want costing.hardware_cost", ve.Field)
	}
	if ve.HowToFix == "" {
		t.Error("violation must tell the user which of the two places to price hardware in")
	}
}

// TestHardwareCostAcceptedWithoutHardwareBom covers the condition the field was always documented
// with: no hardware line in the BOM, so the manual article is the only place hardware is priced.
func TestHardwareCostAcceptedWithoutHardwareBom(t *testing.T) {
	if _, err := ConvertPbTechCardInsertToEntity(hardwareCostCard("2.50",
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD)); err != nil {
		t.Fatalf("hardware_cost with no hardware BOM line must be accepted: %v", err)
	}
}

// TestZeroHardwareCostAcceptedBesideHardwareBom: a zero (or absent) article adds nothing to any
// rollup, so there is nothing to double-count and nothing to refuse. Clearing the field is exactly
// how a card that had both is fixed, and that save must go through.
func TestZeroHardwareCostAcceptedBesideHardwareBom(t *testing.T) {
	for name, amount := range map[string]string{"zero": "0", "absent": ""} {
		t.Run(name, func(t *testing.T) {
			if _, err := ConvertPbTechCardInsertToEntity(hardwareCostCard(amount,
				pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE)); err != nil {
				t.Fatalf("hardware BOM line with a %s hardware_cost must be accepted: %v", name, err)
			}
		})
	}
}
