package accounting

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// VAT-regime resolver (docs/plan-accounting-phase2/01-wave1-vat.md §1.1). A pure function of an
// order's ship-from / ship-to countries and B2B status → an entity.VatRegime, translating the Excel
// VAT&Crypto scenarios (1–8). It classifies; the rate itself is looked up per regime and the posting
// is done downstream (BuildOrderSaleEntry). It survives wave 2 unchanged.

// euCountries is the EU-27 (ISO 3166-1 alpha-2). GB is deliberately absent — the UK is a third country
// for VAT since Brexit (a UK destination is export; UK-stock popup sales are a separate origin rule).
// The list lives here because the codebase had no EU set (07 §7.1).
var euCountries = map[string]struct{}{
	"AT": {}, "BE": {}, "BG": {}, "HR": {}, "CY": {}, "CZ": {}, "DK": {}, "EE": {}, "FI": {},
	"FR": {}, "DE": {}, "GR": {}, "HU": {}, "IE": {}, "IT": {}, "LV": {}, "LT": {}, "LU": {},
	"MT": {}, "NL": {}, "PL": {}, "PT": {}, "RO": {}, "SK": {}, "SI": {}, "ES": {}, "SE": {},
}

// countryPL / countryGB are the two origin-relevant codes referenced by the rules.
const (
	countryPL = "PL"
	countryGB = "GB"
)

// Resolver caveats. They are advisory (not blockers): the sale still posts, and the caveat surfaces in
// the entry + reconciliation so a mis-set country / B2B flag is visible rather than silently 0-rated.
const (
	// CaveatUnknownDestination flags an empty / malformed ship-to country: the order is treated as
	// export (0% VAT) as a fail-safe rather than assuming an EU rate (07 §7.4.1).
	CaveatUnknownDestination = "unknown destination"
	// CaveatWdtWithoutVatID flags an EU B2B order that would be an intra-community supply (wdt) but
	// carries no VAT id: it falls back to OSS (destination VAT) instead (§1.3).
	CaveatWdtWithoutVatID = "wdt without vat id"
)

// VatFacts is the resolver input (§1.1). DestCountry is the shipping country (country_code, fallback
// country); OriginCountry is the ship-from (cfg ShipFromAddress, or 'GB' for cash / UK-stock sales);
// IsB2B is set when a non-empty BuyerVatID is present.
type VatFacts struct {
	DestCountry   string
	OriginCountry string
	IsB2B         bool
	BuyerVatID    string
	PaymentMethod entity.PaymentMethodName
}

// ResolveVatRegime maps VatFacts to the VAT regime plus any advisory caveats. Order of the checks
// mirrors the Excel scenarios:
//
//	cash / UK-stock origin        → uk_stock_domestic (UK domestic 20%)   [scenario 6]
//	missing / malformed dest      → export + "unknown destination"        [guard, 07 §7.4.1]
//	B2B, domestic PL              → pl_domestic (reverse charge is cross-border only)
//	B2B, EU (≠PL), with VAT id    → wdt (0% intra-community)              [scenario 4]
//	B2B, EU (≠PL), no VAT id      → oss + "wdt without vat id"            [guard, §1.3]
//	B2B, non-EU (incl. UK)        → export                                [scenario 5]
//	B2C, non-EU (incl. UK)        → export                                [scenario 3]
//	B2C, PL                       → pl_domestic (23%)                     [scenario 2]
//	B2C, EU (≠PL)                 → oss (destination rate)                [scenario 1]
func ResolveVatRegime(f VatFacts) (entity.VatRegime, []string) {
	var caveats []string
	origin := normalizeCountry(f.OriginCountry)
	dest := normalizeCountry(f.DestCountry)

	// Cash / UK-stock popup: sold out of UK stock, UK domestic VAT regardless of the destination.
	if f.PaymentMethod == entity.CASH || origin == countryGB {
		return entity.VatRegimeUKStockDomestic, caveats
	}

	// Destination drives the EU/export split. A missing/malformed country is fail-safe export + caveat
	// (never silently 0-rate an EU sale — the caveat surfaces in reconciliation).
	if !isCountryCode(dest) {
		caveats = append(caveats, CaveatUnknownDestination)
		return entity.VatRegimeExport, caveats
	}

	_, inEU := euCountries[dest]

	if f.IsB2B {
		switch {
		case dest == countryPL:
			// Domestic PL B2B is still PL-rated (reverse charge applies to cross-border intra-EU supply).
			return entity.VatRegimePLDomestic, caveats
		case inEU:
			// EU B2B with a VAT id → intra-community supply, 0% reverse charge (wdt). Without an id it is
			// not a valid wdt: fall back to OSS (charge destination VAT) and flag it.
			if strings.TrimSpace(f.BuyerVatID) == "" {
				caveats = append(caveats, CaveatWdtWithoutVatID)
				return entity.VatRegimeOSS, caveats
			}
			return entity.VatRegimeWDT, caveats
		default:
			// Non-EU B2B (incl. UK shipped from PL) → export.
			return entity.VatRegimeExport, caveats
		}
	}

	// B2C.
	switch {
	case !inEU:
		return entity.VatRegimeExport, caveats
	case dest == countryPL:
		return entity.VatRegimePLDomestic, caveats
	default:
		return entity.VatRegimeOSS, caveats
	}
}

// RegimeHasVAT reports whether a regime posts a VAT line at a positive rate (oss / pl_domestic /
// uk_stock_domestic). export / wdt / none never do.
func RegimeHasVAT(r entity.VatRegime) bool {
	switch r {
	case entity.VatRegimeOSS, entity.VatRegimePLDomestic, entity.VatRegimeUKStockDomestic:
		return true
	default:
		return false
	}
}

// RegimeRateCountry is the country whose vat_rate the regime charges: the destination for OSS, PL for
// pl_domestic, GB for uk_stock_domestic. Empty for the no-VAT regimes (export / wdt / none). The
// worker looks this rate up and skips the order with a "vat rate missing" alert if it is absent
// (07 §7.4.14) rather than posting a zero-rate (a wrong declaration is worse than a delayed one).
func RegimeRateCountry(r entity.VatRegime, dest, origin string) string {
	switch r {
	case entity.VatRegimeOSS:
		return normalizeCountry(dest)
	case entity.VatRegimePLDomestic:
		return countryPL
	case entity.VatRegimeUKStockDomestic:
		return countryGB
	default:
		return ""
	}
}

// normalizeCountry upper-cases and trims a country code / name for comparison.
func normalizeCountry(c string) string { return strings.ToUpper(strings.TrimSpace(c)) }

// isCountryCode reports whether s is a plausible ISO 3166-1 alpha-2 code (exactly two ASCII letters).
// A full country name ("Germany") or empty string is rejected — the resolver treats it as unknown.
func isCountryCode(s string) bool {
	if len(s) != 2 {
		return false
	}
	for i := 0; i < 2; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}
