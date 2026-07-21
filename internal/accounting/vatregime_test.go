package accounting

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
)

// TestResolveVatRegime_Scenarios covers the Excel VAT&Crypto scenarios (1–8) plus the fail-safe
// guards (07 §7.4.1, §1.3). Origin defaults to PL (the normal ship-from) unless a case overrides it.
func TestResolveVatRegime_Scenarios(t *testing.T) {
	const originPL = "PL"
	tests := []struct {
		name        string
		facts       VatFacts
		wantRegime  entity.VatRegime
		wantCaveats []string
	}{
		{
			name:       "1: EU (≠PL) B2C → oss",
			facts:      VatFacts{DestCountry: "DE", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimeOSS,
		},
		{
			name:       "2: PL B2C → pl_domestic",
			facts:      VatFacts{DestCountry: "PL", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimePLDomestic,
		},
		{
			name:       "3: non-EU B2C → export",
			facts:      VatFacts{DestCountry: "US", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimeExport,
		},
		{
			name:       "3b: UK B2C shipped from PL → export",
			facts:      VatFacts{DestCountry: "GB", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimeExport,
		},
		{
			name: "4: EU (≠PL) B2B with VAT id → wdt",
			facts: VatFacts{DestCountry: "FR", OriginCountry: originPL, IsB2B: true,
				BuyerVatID: "FR12345678901", PaymentMethod: entity.BANK_INVOICE},
			wantRegime: entity.VatRegimeWDT,
		},
		{
			name: "5: UK B2B → export",
			facts: VatFacts{DestCountry: "GB", OriginCountry: originPL, IsB2B: true,
				BuyerVatID: "GB123456789", PaymentMethod: entity.BANK_INVOICE},
			wantRegime: entity.VatRegimeExport,
		},
		{
			name:       "6: cash / popup → uk_stock_domestic",
			facts:      VatFacts{DestCountry: "PL", OriginCountry: originPL, PaymentMethod: entity.CASH},
			wantRegime: entity.VatRegimeUKStockDomestic,
		},
		{
			name:       "6b: UK-stock origin (non-cash) → uk_stock_domestic",
			facts:      VatFacts{DestCountry: "DE", OriginCountry: "GB", PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimeUKStockDomestic,
		},
		{
			name: "7: PL B2B domestic → pl_domestic",
			facts: VatFacts{DestCountry: "PL", OriginCountry: originPL, IsB2B: true,
				BuyerVatID: "PL1234567890", PaymentMethod: entity.BANK_INVOICE},
			wantRegime: entity.VatRegimePLDomestic,
		},
		{
			name:        "guard: empty destination → export + caveat",
			facts:       VatFacts{DestCountry: "", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime:  entity.VatRegimeExport,
			wantCaveats: []string{CaveatUnknownDestination},
		},
		{
			name:        "guard: malformed destination (full name) → export + caveat",
			facts:       VatFacts{DestCountry: "Germany", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime:  entity.VatRegimeExport,
			wantCaveats: []string{CaveatUnknownDestination},
		},
		{
			name: "guard: EU B2B without VAT id → oss + caveat",
			facts: VatFacts{DestCountry: "FR", OriginCountry: originPL, IsB2B: true,
				BuyerVatID: "  ", PaymentMethod: entity.BANK_INVOICE},
			wantRegime:  entity.VatRegimeOSS,
			wantCaveats: []string{CaveatWdtWithoutVatID},
		},
		{
			name:       "case-insensitive destination",
			facts:      VatFacts{DestCountry: "de", OriginCountry: originPL, PaymentMethod: entity.CARD},
			wantRegime: entity.VatRegimeOSS,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			regime, caveats := ResolveVatRegime(tt.facts)
			assert.Equal(t, tt.wantRegime, regime)
			assert.Equal(t, tt.wantCaveats, caveats)
			// every resolved regime must be a storable value (mirrors the DB CHECK).
			assert.Truef(t, entity.ValidVatRegimes[regime], "regime %q is not storable", regime)
		})
	}
}

func TestRegimeHasVATAndRateCountry(t *testing.T) {
	assert.True(t, RegimeHasVAT(entity.VatRegimeOSS))
	assert.True(t, RegimeHasVAT(entity.VatRegimePLDomestic))
	assert.True(t, RegimeHasVAT(entity.VatRegimeUKStockDomestic))
	assert.False(t, RegimeHasVAT(entity.VatRegimeExport))
	assert.False(t, RegimeHasVAT(entity.VatRegimeWDT))
	assert.False(t, RegimeHasVAT(entity.VatRegimeNone))

	assert.Equal(t, "DE", RegimeRateCountry(entity.VatRegimeOSS, "de", "PL"))
	assert.Equal(t, countryPL, RegimeRateCountry(entity.VatRegimePLDomestic, "PL", "PL"))
	assert.Equal(t, countryGB, RegimeRateCountry(entity.VatRegimeUKStockDomestic, "PL", "GB"))
	assert.Equal(t, "", RegimeRateCountry(entity.VatRegimeExport, "US", "PL"))
	assert.Equal(t, "", RegimeRateCountry(entity.VatRegimeWDT, "FR", "PL"))
}
