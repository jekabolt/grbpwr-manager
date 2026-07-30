package materialcode

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

func TestComposeArticle(t *testing.T) {
	tests := []struct {
		name string
		in   entity.MaterialInsert
		want string
	}{
		{
			name: "fabric",
			in: entity.MaterialInsert{
				MaterialClass:      "fabric",
				Supplier:           stringValue("Certex"),
				Color:              stringValue("BLK"),
				CompositionEntries: []entity.CompositionEntry{{FiberCode: "ctn"}, {FiberCode: "ELA"}},
				FabricAttr: &entity.MaterialFabricAttr{
					WeightGsm: decimalValue("239.6"),
					WidthCm:   decimalValue("149.5"),
				},
			},
			want: "CER·FAB·CTN240·W150·BLK",
		},
		{
			name: "hardware",
			in: entity.MaterialInsert{
				MaterialClass: "hardware",
				HardwareAttr: &entity.MaterialHardwareAttr{
					BaseMaterial: stringValue("cork"),
					DiameterMm:   decimalValue("17.6"),
					Finish:       stringValue("matte"),
				},
			},
			want: "HW·COR·18·MAT",
		},
		{
			name: "thread",
			in: entity.MaterialInsert{
				MaterialClass:      "thread",
				Supplier:           stringValue("A-1 B&C"),
				Color:              stringValue("navy"),
				CompositionEntries: []entity.CompositionEntry{{FiberCode: "POL"}},
			},
			want: "ABC·THR·POL·NAV",
		},
		{
			name: "packaging",
			in: entity.MaterialInsert{
				MaterialClass: "packaging",
				Supplier:      stringValue("Supplier"),
				Color:         stringValue("white"),
				PackagingAttr: &entity.MaterialPackagingAttr{Gsm: decimalValue("119.5")},
			},
			want: "SUP·PKG·G120·WHI",
		},
		{
			name: "other and unknown normalize to other token",
			in: entity.MaterialInsert{
				Color: stringValue("red"),
			},
			want: "OTH·RED",
		},
		{
			name: "empty attribute sources are dropped",
			in: entity.MaterialInsert{
				MaterialClass: "fabric",
				FabricAttr: &entity.MaterialFabricAttr{
					WeightGsm: decimalValue("0"),
					WidthCm:   decimalValue("0"),
				},
			},
			want: "FAB",
		},
		{
			name: "legacy fabric dimensions remain a fallback",
			in: entity.MaterialInsert{
				MaterialClass:   "FABRIC",
				FabricWeightGsm: decimalValue("180.4"),
				FabricWidth:     decimalValue("140.4"),
			},
			want: "FAB·180·W140",
		},
		{
			name: "unknown finish uses its first three letters",
			in: entity.MaterialInsert{
				MaterialClass: "hardware",
				HardwareAttr:  &entity.MaterialHardwareAttr{Finish: stringValue("oxidised")},
			},
			want: "HW·OXI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComposeArticle(&tt.in); got != tt.want {
				t.Fatalf("ComposeArticle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComposeArticleUsesFirstCompositionEntry(t *testing.T) {
	in := entity.MaterialInsert{
		MaterialClass: "fabric",
		CompositionEntries: []entity.CompositionEntry{
			{FiberCode: "COT", Percent: decimal.NewFromInt(40)},
			{FiberCode: "POL", Percent: decimal.NewFromInt(60)},
		},
	}
	if got := ComposeArticle(&in); got != "FAB·COT" {
		t.Fatalf("ComposeArticle() = %q, want first composition entry FAB·COT", got)
	}
}

func stringValue(value string) sql.NullString {
	return sql.NullString{String: value, Valid: true}
}

func decimalValue(value string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(value), Valid: true}
}
