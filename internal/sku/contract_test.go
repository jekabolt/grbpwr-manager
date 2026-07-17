package sku

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
)

func TestLoad(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if c.Version != "grbpwr-sku-v1" {
		t.Errorf("Version = %q, want %q", c.Version, "grbpwr-sku-v1")
	}
	if c.BaseSKULength != product.BaseSKULen {
		t.Errorf("BaseSKULength = %d, want product.BaseSKULen = %d", c.BaseSKULength, product.BaseSKULen)
	}
	if c.VariantSKULength != product.VariantSKULen {
		t.Errorf("VariantSKULength = %d, want product.VariantSKULen = %d", c.VariantSKULength, product.VariantSKULen)
	}
	if len(c.GoldenVectors.Base) == 0 || len(c.GoldenVectors.Variant) == 0 {
		t.Fatal("contract fixture has no golden vectors")
	}
	if len(c.NegativeVectors.Base) == 0 || len(c.NegativeVectors.Variant) == 0 {
		t.Fatal("contract fixture has no negative vectors")
	}
}

// TestGoldenBaseVectors feeds every golden base vector in the fixture through the real strict
// builder (internal/store/product.BuildBaseSKU) and asserts it is accepted and matches exactly. This
// is the cross-language single source of truth (R7): a TypeScript port reads the same fixture.
func TestGoldenBaseVectors(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.GoldenVectors.Base {
		t.Run(v.Note, func(t *testing.T) {
			got, err := product.BuildBaseSKU(product.SKUSegments{
				Season:    entity.SeasonEnum(v.Season),
				Year:      v.Year,
				ModelNo:   v.ModelNo,
				ColorCode: v.ColorCode,
			})
			if err != nil {
				t.Fatalf("BuildBaseSKU(%+v) unexpected error: %v", v, err)
			}
			if got != v.Want {
				t.Errorf("BuildBaseSKU(%+v) = %q, want %q", v, got, v.Want)
			}
			if len(got) != c.BaseSKULength {
				t.Errorf("BuildBaseSKU(%+v) length = %d, want %d", v, len(got), c.BaseSKULength)
			}
		})
	}
}

// TestGoldenVariantVectors is the variant-SKU counterpart of TestGoldenBaseVectors, covering the
// apparel/shoe/composite ordinal examples pinned in the fixture (including the canon-v1 M=25 and
// shoe-EU48=76 values that superseded the doc-drift M=04 / 48=77 examples).
func TestGoldenVariantVectors(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.GoldenVectors.Variant {
		t.Run(v.Note, func(t *testing.T) {
			got, err := product.BuildVariantSKU(v.Base, v.SizeOrd)
			if err != nil {
				t.Fatalf("BuildVariantSKU(%+v) unexpected error: %v", v, err)
			}
			if got != v.Want {
				t.Errorf("BuildVariantSKU(%+v) = %q, want %q", v, got, v.Want)
			}
			if len(got) != c.VariantSKULength {
				t.Errorf("BuildVariantSKU(%+v) length = %d, want %d", v, len(got), c.VariantSKULength)
			}
		})
	}
}

// TestNegativeBaseVectors feeds every negative base vector through the strict builder and asserts it
// is REJECTED (problem 045: the old builder silently masked exactly these ranges).
func TestNegativeBaseVectors(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.NegativeVectors.Base {
		t.Run(v.Reason, func(t *testing.T) {
			got, err := product.BuildBaseSKU(product.SKUSegments{
				Season:    entity.SeasonEnum(v.Season),
				Year:      v.Year,
				ModelNo:   v.ModelNo,
				ColorCode: v.ColorCode,
			})
			if err == nil {
				t.Errorf("BuildBaseSKU(%+v) = %q, want an error (reason %s)", v, got, v.Reason)
			}
		})
	}
}

// TestNegativeVariantVectors is the variant-SKU counterpart of TestNegativeBaseVectors.
func TestNegativeVariantVectors(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range c.NegativeVectors.Variant {
		t.Run(v.Reason, func(t *testing.T) {
			got, err := product.BuildVariantSKU(v.Base, v.SizeOrd)
			if err == nil {
				t.Errorf("BuildVariantSKU(%+v) = %q, want an error (reason %s)", v, got, v.Reason)
			}
		})
	}
}

// TestSizeSystemOrdinalsAreWellFormed sanity-checks the fixture's own dictionaries: every ordinal
// must be in the 1..99 window and unique within its size system (the DB enforces the same two
// invariants via chk_size_sku_contract + uniq_size_sku_system_ord in migration 0147).
func TestSizeSystemOrdinalsAreWellFormed(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.SizeSystems) == 0 {
		t.Fatal("contract fixture has no size systems")
	}
	for systemName, system := range c.SizeSystems {
		seen := make(map[int]string, len(system.Ordinals))
		for sizeName, ord := range system.Ordinals {
			if ord < c.SizeOrdinal.Min || ord > c.SizeOrdinal.Max {
				t.Errorf("%s.%s: ordinal %d out of [%d,%d]", systemName, sizeName, ord, c.SizeOrdinal.Min, c.SizeOrdinal.Max)
			}
			if other, dup := seen[ord]; dup {
				t.Errorf("%s: ordinal %d shared by %q and %q", systemName, ord, other, sizeName)
			}
			seen[ord] = sizeName
		}
	}
}

// TestCanonExamplesSupersedeDocDrift pins the two literal values 61-digest/045 called out as
// contradicting the plan prose (M=04 vs M=25; shoe 48 -> prose 77 vs formula 76). Canon v1 keeps the
// seed-table values; this test fails loudly if a future edit reintroduces the doc-drift numbers.
func TestCanonExamplesSupersedeDocDrift(t *testing.T) {
	base, err := product.BuildBaseSKU(product.SKUSegments{
		Season: entity.SeasonSS, Year: 2026, ModelNo: 21, ColorCode: "BLK",
	})
	if err != nil {
		t.Fatalf("BuildBaseSKU: %v", err)
	}

	apparelM, err := product.BuildVariantSKU(base, 25)
	if err != nil {
		t.Fatalf("BuildVariantSKU(apparel M): %v", err)
	}
	if apparelM != "SS26-00021-BLK-25" {
		t.Errorf("apparel M ordinal = %q, want ...-25 (canon v1; NOT -04)", apparelM)
	}

	shoe48, err := product.BuildVariantSKU(base, 76)
	if err != nil {
		t.Fatalf("BuildVariantSKU(shoe EU48): %v", err)
	}
	if shoe48 != "SS26-00021-BLK-76" {
		t.Errorf("shoe EU48 ordinal = %q, want ...-76 (canon v1; NOT -77)", shoe48)
	}
}
