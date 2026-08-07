package entity

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestFabricLengthToKg locks the length→weight conversion (Ф5а.4) — and specifically that it is
// computed on the FULL roll width, кромка included, because the selvedge is bought and it weighs.
func TestFabricLengthToKg(t *testing.T) {
	for _, c := range []struct {
		name   string
		metres string
		width  decimal.NullDecimal
		gsm    decimal.NullDecimal
		want   string // "" = invalid
	}{
		{
			// 100 m × 1.50 m = 150 m²; × 220 g/m² = 33 000 g = 33 kg
			name: "textbook roll", metres: "100", width: nd("150"), gsm: nd("220"), want: "33",
		},
		{
			name: "narrow roll", metres: "50", width: nd("110"), gsm: nd("180"), want: "9.9",
		},
		{
			name: "no width is not a guess", metres: "100", width: decimal.NullDecimal{}, gsm: nd("220"), want: "",
		},
		{
			name: "no density is not a guess", metres: "100", width: nd("150"), gsm: decimal.NullDecimal{}, want: "",
		},
		{
			name: "zero width is not a measurement", metres: "100", width: nd("0"), gsm: nd("220"), want: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := FabricLengthToKg(nd(c.metres).Decimal, c.width, c.gsm)
			if (c.want == "") != !got.Valid {
				t.Fatalf("validity mismatch: %+v", got)
			}
			if c.want != "" && got.Decimal.String() != c.want {
				t.Fatalf("kg = %s, want %s", got.Decimal, c.want)
			}
		})
	}
}

// TestFabricLengthToKgUsesFullWidthNotCuttingWidth is the trap the spec names: billing by the
// cutting width (full − 2×selvedge) understates the invoiced weight by a couple of percent, every
// time, in the same direction.
func TestFabricLengthToKgUsesFullWidthNotCuttingWidth(t *testing.T) {
	m := Material{MaterialInsert: MaterialInsert{
		FabricAttr: &MaterialFabricAttr{WidthCm: nd("150"), WeightGsm: nd("220"), SelvedgeCm: nd("2").Decimal},
	}}
	full := FabricLengthToKg(nd("100").Decimal, m.EffectiveFabricWidthCm(), m.EffectiveFabricWeightGsm())
	usable := FabricLengthToKg(nd("100").Decimal, m.UsableFabricWidthCm(), m.EffectiveFabricWeightGsm())
	if !full.Valid || !usable.Valid {
		t.Fatal("both conversions should be valid")
	}
	if !full.Decimal.GreaterThan(usable.Decimal) {
		t.Fatalf("full-width weight %s must exceed cutting-width weight %s", full.Decimal, usable.Decimal)
	}
	if full.Decimal.String() != "33" {
		t.Fatalf("full width 150 cm must be used, got %s kg", full.Decimal)
	}
}

// TestEffectiveFabricWeightGsm mirrors EffectiveFabricWidthCm's CTI-over-flat rule. The BOM line's
// own fabric_weight_gsm is deliberately not part of this resolution — it is a different number with
// no consumer, and folding the two would silently make a card's spec snapshot drive warehouse maths.
func TestEffectiveFabricWeightGsm(t *testing.T) {
	flat := Material{MaterialInsert: MaterialInsert{FabricWeightGsm: nd("180")}}
	if got := flat.EffectiveFabricWeightGsm(); !got.Valid || got.Decimal.String() != "180" {
		t.Fatalf("flat fallback = %+v", got)
	}
	typed := Material{MaterialInsert: MaterialInsert{
		FabricWeightGsm: nd("180"),
		FabricAttr:      &MaterialFabricAttr{WeightGsm: nd("240")},
	}}
	if got := typed.EffectiveFabricWeightGsm(); !got.Valid || got.Decimal.String() != "240" {
		t.Fatalf("CTI attr must win: %+v", got)
	}
	halfFilled := Material{MaterialInsert: MaterialInsert{
		FabricWeightGsm: nd("180"),
		FabricAttr:      &MaterialFabricAttr{WeightGsm: nd("0")},
	}}
	if got := halfFilled.EffectiveFabricWeightGsm(); !got.Valid || got.Decimal.String() != "180" {
		t.Fatalf("a zero typed value means 'not really set' and must fall through: %+v", got)
	}
	var empty Material
	if got := empty.EffectiveFabricWeightGsm(); got.Valid {
		t.Fatalf("no density anywhere must stay invalid: %+v", got)
	}
}

// TestEffectiveCuttingCoefficient: unset means "multiply by nothing", never a guessed default —
// backfilling one would have silently inflated every existing material plan.
func TestEffectiveCuttingCoefficient(t *testing.T) {
	if got := (&Material{}).EffectiveCuttingCoefficient(); got.Valid {
		t.Fatalf("unset coefficient must stay invalid: %+v", got)
	}
	set := Material{MaterialInsert: MaterialInsert{CuttingCoefficient: nd("1.06")}}
	if got := set.EffectiveCuttingCoefficient(); !got.Valid || got.Decimal.String() != "1.06" {
		t.Fatalf("set coefficient = %+v", got)
	}
	// A coefficient can only add to a norm. Below 1 is data the DB CHECK refuses; in memory it is
	// treated as unset rather than silently shaving a requirement.
	shaving := Material{MaterialInsert: MaterialInsert{CuttingCoefficient: nd("0.9")}}
	if got := shaving.EffectiveCuttingCoefficient(); got.Valid {
		t.Fatalf("a sub-unit coefficient must not shave a requirement: %+v", got)
	}
}
