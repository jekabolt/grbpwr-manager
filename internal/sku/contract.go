// Package sku holds the canonical, machine-readable SKU contract (grbpwr-sku-v1, decision R7 in
// tmp/finish-rework/01-contract-decisions.md): the ordinal dictionaries and worked golden/negative
// examples that internal/store/product's builder (sku.go) implements. sku-contract-v1.json is the
// single source of truth — the Go golden tests in this package read it, and any other language's
// tests (TypeScript storefront/admin/analytics) should read the same file rather than re-typing the
// ordinal tables by hand.
//
// The mapping in the fixture is FROZEN once shipped (R7): existing size ordinals never change or get
// reassigned — a new size takes a free ordinal in its system, and exhausting 1..99 in a system means a
// new SKU contract version (sku-contract-v2.json), not an edit to this file.
package sku

import (
	"embed"
	"encoding/json"
	"fmt"
	"sync"
)

//go:embed sku-contract-v1.json
var contractFS embed.FS

// ContractFileName is the canonical fixture file name, exposed so other tooling (e.g. a script that
// copies this file into a sibling TypeScript repo) does not have to hardcode it twice.
const ContractFileName = "sku-contract-v1.json"

// Contract is the decoded shape of sku-contract-v1.json.
type Contract struct {
	Version          string                `json:"version"`
	Description      string                `json:"description"`
	BaseSKUShape     string                `json:"base_sku_shape"`
	BaseSKULength    int                   `json:"base_sku_length"`
	VariantSKUShape  string                `json:"variant_sku_shape"`
	VariantSKULength int                   `json:"variant_sku_length"`
	Season           SeasonContract        `json:"season"`
	ModelNo          ModelNoContract       `json:"model_no"`
	ColorCode        ColorCodeContract     `json:"color_code"`
	SizeOrdinal      SizeOrdinalContract   `json:"size_ordinal"`
	SizeSystems      map[string]SizeSystem `json:"size_systems"`
	GoldenVectors    GoldenVectors         `json:"golden_vectors"`
	NegativeVectors  NegativeVectors       `json:"negative_vectors"`
}

// SeasonContract is the valid season code set and year window.
type SeasonContract struct {
	ValidCodes []string `json:"valid_codes"`
	YearMin    int      `json:"year_min"`
	YearMax    int      `json:"year_max"`
}

// ModelNoContract is the model-number range plus the R7 ceiling-alert thresholds.
type ModelNoContract struct {
	Min               int `json:"min"`
	Max               int `json:"max"`
	WarnThreshold     int `json:"warn_threshold"`
	CriticalThreshold int `json:"critical_threshold"`
}

// ColorCodeContract is the canonical colour-segment shape.
type ColorCodeContract struct {
	Pattern string `json:"pattern"`
}

// SizeOrdinalContract is the size-ordinal segment's numeric range.
type SizeOrdinalContract struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// SizeSystem is one controlled size family's ordinal dictionary (apparel/shoe/composite_ta/composite_bo).
// Ordinals is keyed by the size's dictionary name (e.g. "m", "48", "m_38ta_f").
type SizeSystem struct {
	Description string         `json:"description"`
	Formula     string         `json:"formula,omitempty"`
	Ordinals    map[string]int `json:"ordinals"`
}

// BaseVector is one golden (season, year, model, color) -> base SKU example.
type BaseVector struct {
	Season    string `json:"season"`
	Year      int    `json:"year"`
	ModelNo   int    `json:"model_no"`
	ColorCode string `json:"color_code"`
	Want      string `json:"want"`
	Note      string `json:"note,omitempty"`
}

// VariantVector is one golden (base, size ordinal) -> variant SKU example.
type VariantVector struct {
	Base    string `json:"base"`
	SizeOrd int    `json:"size_ord"`
	Want    string `json:"want"`
	Note    string `json:"note,omitempty"`
}

// NegativeBaseVector is a base-SKU input that MUST be rejected by the strict builder, tagged with why.
type NegativeBaseVector struct {
	Season    string `json:"season"`
	Year      int    `json:"year"`
	ModelNo   int    `json:"model_no"`
	ColorCode string `json:"color_code"`
	Reason    string `json:"reason"`
}

// NegativeVariantVector is a variant-SKU input that MUST be rejected by the strict builder.
type NegativeVariantVector struct {
	Base    string `json:"base"`
	SizeOrd int    `json:"size_ord"`
	Reason  string `json:"reason"`
}

// GoldenVectors are inputs the strict builder MUST accept, paired with their expected output.
type GoldenVectors struct {
	Base    []BaseVector    `json:"base"`
	Variant []VariantVector `json:"variant"`
}

// NegativeVectors are inputs the strict builder MUST reject.
type NegativeVectors struct {
	Base    []NegativeBaseVector    `json:"base"`
	Variant []NegativeVariantVector `json:"variant"`
}

var (
	versionOnce sync.Once
	cachedVer   string
)

// ContractVersion returns the canonical SKU contract version (R7, e.g. "grbpwr-sku-v1") from the single
// embedded fixture, cached. It is the one source both the SKU builder tests and the Dictionary contract
// read, so the wire value can never drift from the fixture. Empty only if the embedded fixture fails to
// decode (contract_test.go guards that it does not).
func ContractVersion() string {
	versionOnce.Do(func() {
		if c, err := Load(); err == nil {
			cachedVer = c.Version
		}
	})
	return cachedVer
}

// Load decodes the embedded sku-contract-v1.json fixture.
func Load() (*Contract, error) {
	data, err := contractFS.ReadFile(ContractFileName)
	if err != nil {
		return nil, fmt.Errorf("sku: read contract fixture: %w", err)
	}
	var c Contract
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("sku: decode contract fixture: %w", err)
	}
	return &c, nil
}
