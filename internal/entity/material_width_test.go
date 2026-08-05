package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

func nd(s string) decimal.NullDecimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return decimal.NullDecimal{Decimal: d, Valid: true}
}

// TestUsableFabricWidth locks the single width resolver (0259): CTI width wins over the legacy
// flat column, usable = effective − 2×selvedge clamped at zero, invalid when no width is known.
func TestUsableFabricWidth(t *testing.T) {
	sel := func(s string) decimal.Decimal { return nd(s).Decimal }

	for _, c := range []struct {
		name       string
		m          Material
		wantEff    string // "" = invalid
		wantUsable string // "" = invalid
	}{
		{
			name:       "CTI wins over flat",
			m:          Material{MaterialInsert: MaterialInsert{FabricWidth: nd("140")}},
			wantEff:    "140",
			wantUsable: "140",
		},
		{
			name: "typed attr overrides flat and subtracts selvedge",
			m: Material{
				MaterialInsert: MaterialInsert{
					FabricWidth: nd("140"),
					FabricAttr:  &MaterialFabricAttr{WidthCm: nd("150"), SelvedgeCm: sel("1.5")},
				},
			},
			wantEff:    "150",
			wantUsable: "147",
		},
		{
			name: "selvedge wider than the roll clamps at zero",
			m: Material{
				MaterialInsert: MaterialInsert{
					FabricAttr: &MaterialFabricAttr{WidthCm: nd("2"), SelvedgeCm: sel("5")},
				},
			},
			wantEff:    "2",
			wantUsable: "0",
		},
		{
			name:       "no width anywhere stays invalid",
			m:          Material{},
			wantEff:    "",
			wantUsable: "",
		},
		{
			name: "attr row with unset width falls back to flat, like preferredDecimal",
			m: Material{
				MaterialInsert: MaterialInsert{
					FabricWidth: nd("140"),
					FabricAttr:  &MaterialFabricAttr{SelvedgeCm: sel("1")},
				},
			},
			wantEff:    "140",
			wantUsable: "138",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			eff := c.m.EffectiveFabricWidthCm()
			if (c.wantEff == "") != !eff.Valid {
				t.Fatalf("effective validity mismatch: %+v", eff)
			}
			if c.wantEff != "" && eff.Decimal.String() != c.wantEff {
				t.Fatalf("effective = %s, want %s", eff.Decimal, c.wantEff)
			}
			usable := c.m.UsableFabricWidthCm()
			if (c.wantUsable == "") != !usable.Valid {
				t.Fatalf("usable validity mismatch: %+v", usable)
			}
			if c.wantUsable != "" && usable.Decimal.String() != c.wantUsable {
				t.Fatalf("usable = %s, want %s", usable.Decimal, c.wantUsable)
			}
		})
	}
}
