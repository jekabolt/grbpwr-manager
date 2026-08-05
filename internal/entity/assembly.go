package entity

import (
	"database/sql"
	"errors"
	"strings"

	"github.com/shopspring/decimal"
)

// ErrStyleAssemblyInvalid is returned when a style-assembly payload is malformed (missing/duplicate
// component, non-positive qty, a component that is not an auxiliary card, or a self-reference).
var ErrStyleAssemblyInvalid = errors.New("invalid style assembly")

// StyleAssembly is one stored line of a garment style's ASSEMBLY bill (WS7, §2.8): an auxiliary item
// (a tech card, purpose=auxiliary — a brand/care/size label, hangtag, sticker, dust bag…) that physically
// goes on/into the garment, with a quantity and print/position notes. It is distinct from packaging
// (WS2 packaging_recipe, on the shipment): assembly is on the garment and the component's output material
// is consumed in the garment's production run via the existing BOM/material path. The Component*/Output*/
// SizeName fields are resolved on read (List) for display and ignored on write.
type StyleAssembly struct {
	Id                  int             `db:"id"`
	StyleId             int             `db:"style_id"`
	ComponentTechCardId int             `db:"component_tech_card_id"`
	SizeId              sql.NullInt32   `db:"size_id"` // NULL = applies to all garment sizes
	Qty                 decimal.Decimal `db:"qty"`
	PrintNote           sql.NullString  `db:"print_note"`
	PositionNote        sql.NullString  `db:"position_note"`
	Active              bool            `db:"active"`
	CreatedBy           string          `db:"created_by"`
	UpdatedBy           string          `db:"updated_by"`
	// Resolved on read for display (List / packing spec):
	ComponentName       string         `db:"component_name"`        // auxiliary card name
	ComponentAuxSubtype sql.NullString `db:"component_aux_subtype"` // auxiliary card aux_subtype
	OutputMaterialId    sql.NullInt32  `db:"output_material_id"`    // component's warehouse material (COGS link)
	OutputMaterialName  sql.NullString `db:"output_material_name"`  // resolved material name
	// OutputMaterialArchived is that material's catalog state — archived nomenclature is never
	// prescribed to a packer, even when it is the card's only output.
	OutputMaterialArchived sql.NullBool   `db:"output_material_archived"`
	SizeName               sql.NullString `db:"size_name"` // resolved when SizeId set
	// OutputVariantCount is how many ACTIVE colour variants (0252) the component card has. 0 is legacy
	// single-output mode, in which OutputMaterialId above IS the bucket; > 0 means the card produces one
	// bucket per colour and OutputMaterialId is a stale leftover nobody should consume.
	OutputVariantCount int `db:"output_variant_count"`
	// AssemblyOutputResolution is filled per ORDER ITEM by the packing spec only — a bill read on its
	// own (ListStyleAssembly) has no colourway to resolve against and leaves it zero.
	AssemblyOutputResolution
}

// AssemblyResolutionBasis records WHY the packing spec named (or refused to name) a bucket for one
// assembly line of one order item. Mirrors the common.AssemblyResolutionBasis proto enum. It is a
// read-model discriminator only — never stored, never accepted on write.
type AssemblyResolutionBasis string

const (
	// AssemblyResolutionNotAttempted is the zero value: a bill read on its own has no item to resolve
	// against.
	AssemblyResolutionNotAttempted AssemblyResolutionBasis = ""
	// Resolved, strongest first.
	AssemblyResolutionColorMatch   AssemblyResolutionBasis = "color_match"
	AssemblyResolutionSoleVariant  AssemblyResolutionBasis = "sole_variant"
	AssemblyResolutionLegacyOutput AssemblyResolutionBasis = "legacy_output"
	// Unresolved, each naming its own fix.
	AssemblyResolutionRetiredColor     AssemblyResolutionBasis = "retired_color"
	AssemblyResolutionNoColorMatch     AssemblyResolutionBasis = "no_color_match"
	AssemblyResolutionArchivedMaterial AssemblyResolutionBasis = "archived_material"
	AssemblyResolutionNoOutput         AssemblyResolutionBasis = "no_output"
)

// ValidAssemblyResolutionBases is the closed set of resolved/unresolved outcomes, used by the
// entity<->proto drift guard. AssemblyResolutionNotAttempted is deliberately absent: it is the "not
// asked" zero value, not an outcome.
var ValidAssemblyResolutionBases = map[AssemblyResolutionBasis]bool{
	AssemblyResolutionColorMatch:       true,
	AssemblyResolutionSoleVariant:      true,
	AssemblyResolutionLegacyOutput:     true,
	AssemblyResolutionRetiredColor:     true,
	AssemblyResolutionNoColorMatch:     true,
	AssemblyResolutionArchivedMaterial: true,
	AssemblyResolutionNoOutput:         true,
}

// AssemblyOutputResolution names the ONE warehouse bucket a given order item consumes for one assembly
// line — "the black jacket ships the black dust bag". It is computed at read time from the component
// card's colour variants and the item's colourway (ResolveAssemblyOutput); nothing is stored, because
// the answer depends on the order line, not on the bill.
type AssemblyOutputResolution struct {
	ResolvedColorCode    string // "" when the card has no colours (legacy single-output) or nothing resolved
	ResolvedColorName    string
	ResolvedMaterialId   int // 0 when unresolved
	ResolvedMaterialName string
	// Unresolved is the explicit "the server refuses to guess" flag; Basis says which refusal it is.
	// The packer gets a warning instead of a plausible wrong bucket.
	Unresolved bool
	// Basis separates a colour MATCH from a sole-variant SUBSTITUTION — both are "resolved", and they
	// must not read the same to whoever is putting the bag in the box.
	Basis AssemblyResolutionBasis
}

// AssemblyLegacyOutput is the component card's pre-colour single warehouse output
// (tech_card.output_material_id, 0111) as the resolution rule sees it: the id, its name for display,
// and whether the catalog has archived it.
type AssemblyLegacyOutput struct {
	MaterialId   sql.NullInt32
	MaterialName sql.NullString
	Archived     bool
}

// ColorCodeUnknown is the colour dictionary's placeholder code (0130). It is a legitimate colour on a
// product and on a variant, but it asserts nothing about what the thing actually looks like, so it is
// never allowed to *match* — an UNK jacket does not "want" the UNK dust bag any more than the black one.
const ColorCodeUnknown = "UNK"

// ResolveAssemblyOutput picks the warehouse bucket one assembly line contributes to one order item, in
// a fixed priority order that never guesses:
//
//	(a)  the ACTIVE colour variant whose code equals the item's colourway code — the real answer;
//	(a2) the item's colour exists on the card but is RETIRED → UNRESOLVED. The colour is not missing,
//	     it is switched off, and only a human can say whether to switch it back on or ship something
//	     else. Auto-substituting here is the worst outcome the rule can produce: a confident, wrong,
//	     plausible colour (a white bag prescribed for a black jacket whose black bucket is merely
//	     retired) — so (a2) deliberately pre-empts (b);
//	(b)  the SOLE active variant, whatever its colour — one bucket means no choice to get wrong
//	     (a single-colour dust bag ships with every colourway of the garment);
//	(c)  the card's legacy single output material, but ONLY when the card has NO variant rows at all,
//	     active or retired. A card that ever had a colour is in variant mode, and its
//	     tech_card.output_material_id is stale by construction — serving it would be the same confident
//	     wrong answer wearing a different hat;
//	(d)  otherwise unresolved.
//
// Whatever wins, an ARCHIVED bucket is downgraded to unresolved: archived nomenclature is stock the
// catalog has withdrawn, and prescribing it confidently sends a packer to a shelf that should not be
// used. The operator decides — un-archive it or repoint the colour.
//
// UNK is excluded from colour matching in BOTH directions (an UNK item matches no variant, an UNK
// variant matches no item), in (a) and in (a2) alike; an UNK variant can still win (b), because there
// the colour is not what is being decided.
//
// It takes the FULL variant list, active and retired, because three of the branches above are decisions
// about the RETIRED rows — a caller that pre-filters to active cannot express (a2) or (c) at all.
//
// It is deliberately free of the store and of the transport: this is the rule, and it is the thing the
// table test pins.
func ResolveAssemblyOutput(itemColorCode string, variants []TechCardOutputVariant, legacy AssemblyLegacyOutput) AssemblyOutputResolution {
	resolved := func(v TechCardOutputVariant, basis AssemblyResolutionBasis) AssemblyOutputResolution {
		if v.MaterialArchived {
			return AssemblyOutputResolution{Unresolved: true, Basis: AssemblyResolutionArchivedMaterial}
		}
		return AssemblyOutputResolution{
			ResolvedColorCode:    v.ColorCode,
			ResolvedColorName:    v.ColorName,
			ResolvedMaterialId:   v.MaterialId,
			ResolvedMaterialName: v.MaterialName,
			Basis:                basis,
		}
	}
	unresolved := func(basis AssemblyResolutionBasis) AssemblyOutputResolution {
		return AssemblyOutputResolution{Unresolved: true, Basis: basis}
	}

	code := normalizeColorCode(itemColorCode)
	// An absent (archived/unloadable colourway) or UNK item colour can never match; it does not stop
	// the sole-variant branch, where the colour is not what is being decided.
	matchable := code != "" && code != ColorCodeUnknown

	// One pass: the live buckets, and whether the item's own colour is sitting there switched off.
	active := make([]TechCardOutputVariant, 0, len(variants))
	retiredMatch := false
	for _, v := range variants {
		if v.Active {
			// A variant with no bucket is not a bucket (the FK makes this unreachable; the rule does
			// not rely on that). It is still ACTIVE, so it is not a retired-colour gap either — it is
			// simply nothing, and the branches below fall through to a plain no-match.
			if v.MaterialId > 0 {
				active = append(active, v)
			}
			continue
		}
		vc := normalizeColorCode(v.ColorCode)
		if matchable && vc != ColorCodeUnknown && vc == code {
			retiredMatch = true
		}
	}

	if matchable {
		for _, v := range active {
			vc := normalizeColorCode(v.ColorCode)
			if vc != ColorCodeUnknown && vc == code {
				return resolved(v, AssemblyResolutionColorMatch) // (a)
			}
		}
	}
	if retiredMatch {
		return unresolved(AssemblyResolutionRetiredColor) // (a2)
	}
	if len(active) == 1 {
		return resolved(active[0], AssemblyResolutionSoleVariant) // (b)
	}
	if len(active) > 1 {
		return unresolved(AssemblyResolutionNoColorMatch)
	}
	// No live bucket. If the card has variant rows at all it is in variant mode, and none of its
	// colours applies here — the legacy material is NOT a fallback (an all-retired card used to serve
	// it silently, which was the same confident wrong answer).
	if len(variants) > 0 {
		return unresolved(AssemblyResolutionNoColorMatch)
	}
	if legacy.MaterialId.Valid && legacy.MaterialId.Int32 > 0 { // (c)
		if legacy.Archived {
			return unresolved(AssemblyResolutionArchivedMaterial)
		}
		return AssemblyOutputResolution{
			ResolvedMaterialId:   int(legacy.MaterialId.Int32),
			ResolvedMaterialName: legacy.MaterialName.String,
			Basis:                AssemblyResolutionLegacyOutput,
		}
	}
	return unresolved(AssemblyResolutionNoOutput) // (d)
}

// normalizeColorCode folds a dictionary code to the shape codes are stored in (CHAR(3), upper case).
func normalizeColorCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// StyleAssemblyInsert is one writable assembly line (full-replace per style; the style is carried by the
// UpsertStyleAssembly call, not per line).
type StyleAssemblyInsert struct {
	ComponentTechCardId int
	SizeId              sql.NullInt32
	Qty                 decimal.Decimal
	PrintNote           sql.NullString
	PositionNote        sql.NullString
	Active              bool
}

// OrderPackingSpec is the packer/QC-readable composition of an order (WS7 scope 3): the garments that
// ship, the on-garment assembly (labels/tags) to verify per line, and the packaging the whole order needs.
// It is a READ-ONLY projection; it neither reserves nor consumes anything (WS2 owns the reservation ledger).
type OrderPackingSpec struct {
	OrderUUID string
	Items     []OrderPackingSpecItem
	Packaging []OrderPackingSpecPackaging
}

// OrderPackingSpecItem is one garment line: the colourway/variant, its quantity, and the assembly bill
// (size-resolved to this line's variant size).
type OrderPackingSpecItem struct {
	OrderItemId int
	ProductId   int
	VariantId   int
	StyleId     int
	StyleName   string
	SKU         string
	SizeName    string
	// ColorCode/ColorName are the GARMENT's own colour — the code every assembly line below was matched
	// against — echoed so the packer can put garment colour and component colour side by side. Empty
	// when the colourway could not be loaded, which is also why nothing colour-matched.
	ColorCode string
	ColorName string
	Quantity  decimal.Decimal
	Assembly  []StyleAssembly
}

// OrderPackingSpecPackaging is one packaging material the order needs, resolved from WS2 packaging_recipe
// (product → style → global) and summed across the order.
type OrderPackingSpecPackaging struct {
	MaterialId   int
	MaterialName string
	MaterialUnit sql.NullString
	Qty          decimal.Decimal
}
