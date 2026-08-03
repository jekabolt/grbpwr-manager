package dto

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func TestConvertTechCardCostingRequiresCurrencyForMoney(t *testing.T) {
	tests := []struct {
		name string
		set  func(*pb_common.TechCardCosting)
	}{
		{"cmt_cost", func(c *pb_common.TechCardCosting) { c.CmtCost = dec("0") }},
		{"logistics_cost", func(c *pb_common.TechCardCosting) { c.LogisticsCost = dec("1") }},
		{"overhead_cost", func(c *pb_common.TechCardCosting) { c.OverheadCost = dec("1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			costing := &pb_common.TechCardCosting{}
			tt.set(costing)
			_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
				StyleNumber: "ST-CURRENCY",
				Name:        "Currency validation",
				Costing:     costing,
			})
			var ve *entity.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected field-tagged validation error, got %T: %v", err, err)
			}
			if ve.Field != "costing.currency" {
				t.Errorf("violation field = %q, want costing.currency", ve.Field)
			}
		})
	}

	_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "ST-PERCENTAGES",
		Name:        "Percentage-only costing",
		Costing: &pb_common.TechCardCosting{
			DefectPercent:   dec("5"),
			TargetMarginPct: dec("40"),
		},
	})
	if err != nil {
		t.Fatalf("percentage-only costing should not require a currency: %v", err)
	}
}
