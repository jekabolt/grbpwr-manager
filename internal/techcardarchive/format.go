package techcardarchive

import "time"

// FORMAT.md in this directory is the prose half of this file and the document both sides of the
// feature are written against. Every constant and every struct below is a transcription of it; a
// change here without a change there is a defect, not a shortcut.

// Format identity. FormatName sits in manifest.json as `format` so a stray zip is not mistaken for
// ours, and MoneyPolicyStrippedV1 is the flag next to the check: an import refuses an archive that
// does not carry it, which is what stops a hand-made or pre-versioned bundle with costing in it
// from sliding in quietly.
const (
	FormatName            = "grbpwr-techcard-archive"
	FormatVersion         = "1.0" // MAJOR.MINOR, see FormatMajor / FormatMinor
	MoneyPolicyStrippedV1 = "stripped-v1"

	// FormatMajor breaks parsing: an archive of another MAJOR is refused whole. FormatMinor is
	// additive — a server reads every MINOR of its own MAJOR (card.json with DiscardUnknown,
	// unknown files listed as unknown_entry). Parsing the string into the pair belongs to the
	// reader; these are what it compares against.
	FormatMajor = 1
	FormatMinor = 0

	// ArchiveNameTimeLayout formats the timestamp in the archive's own file name,
	// techcard-<style_number>-<yyyymmdd-hhmm>.zip.
	ArchiveNameTimeLayout = "20060102-1504"
)

// Entry names inside the ZIP. Dir* keep their trailing slash so a name can be built by
// concatenation and matched by prefix without a join step; `file` fields inside the indexes carry
// exactly these root-relative names (FORMAT.md §1.1).
const (
	FileManifest  = "manifest.json"
	FileCard      = "card.json"
	FileSizeChart = "sizechart.json"
	FileAssembly  = "assembly.json"
	FileColorways = "colorways.json"

	DirMaterials = "materials/"
	DirMedia     = "media/"
	DirPatterns  = "patterns/"
	DirMarkers   = "markers/"

	FileMaterialsIndex = DirMaterials + "index.json"
	FileMediaIndex     = DirMedia + "index.json"
	FilePatternsIndex  = DirPatterns + "index.json"
	FileMarkersIndex   = DirMarkers + "index.json"
)

// Ceilings from FORMAT.md §1.3 — the reader's, not a caller's. They live here so that every side
// of the feature reads ONE number: a reader minting its own limit is how two answers to "is this
// archive too big" appear, and the loser is whichever check runs second.
//
// MB in §1.3 is 2^20, as it is everywhere in this codebase (bucket/pattern.go's "40 MB",
// http.go's "4 MB"). MaxMarkerFileBytes is the ONE ceiling here that is deliberately NOT equal to
// the live path's number — see the comment on it.
//
// MaxUncompressedBytes and MaxZipEntries are the zip-bomb pair and only work together: a cap on
// output bytes alone is defeated by a million empty entries, and a cap on entries alone by one
// entry that inflates to a terabyte. Both are counted while streaming — a total read from the ZIP
// directory is a claim by the archive about itself.
const (
	MaxZipEntries        = 4096
	MaxUncompressedBytes = 1 * 1024 * 1024 * 1024 // 1 GiB, sum over all entries
	MaxCardJSONBytes     = 16 * 1024 * 1024       // card.json
	// One markers/<slug>-<n>.json. THREE MiB, not two, and the extra megabyte is the whole
	// point: the live save path (admin.maxMarkerLayoutBytes) caps the LAYOUT blob at 2 MiB,
	// while a marker file here is protojson(summary + that layout). A legally saved 2 MiB
	// layout would therefore produce an entry over a 2 MiB ceiling, and OpenArchive refuses the
	// WHOLE archive on the directory pass — no hole, no reason code, and an export that said
	// nothing. Headroom here is what makes every savable marker representable by construction,
	// which is why the export needs no ceiling of its own.
	MaxMarkerFileBytes      = 3 * 1024 * 1024
	MaxUploadedArchiveBytes = 256 * 1024 * 1024 // the uploaded body on the import route
)

// Bucket segments the feature owns. The segment (no slash) is what a key's first path element is
// compared against by the bucket's allowlist guard; the prefix (with slash) is what a key is built
// from. Archive objects are PRIVATE — unlike pattern objects, which are historically public with
// entropy in the key.
//
// internal/bucket ALIASES these rather than declaring its own copies (bucket.ArchiveSegment,
// bucket.ImportSegment, bucket.MaxArchiveObjectBytes): the bucket may import this package — it is
// a leaf and imports nothing of ours — so the two names are one truth the compiler keeps, not two
// strings a reviewer has to notice drifting apart.
const (
	BucketSegmentArchives = "techcard-archives"
	BucketSegmentImports  = "techcard-imports"

	BucketPrefixArchives = BucketSegmentArchives + "/"
	BucketPrefixImports  = BucketSegmentImports + "/"
)

// Manifest is the archive's passport: manifest.json, read first and read alone — a reader parses it
// before touching any other entry, because it is what decides whether the rest may be read at all.
type Manifest struct {
	Format        string    `json:"format"`         // must equal FormatName
	FormatVersion string    `json:"format_version"` // "MAJOR.MINOR"
	ExportedAt    time.Time `json:"exported_at"`
	ExportedBy    string    `json:"exported_by"` // admin username, provenance only
	Source        Source    `json:"source"`
	// MoneyPolicy must equal MoneyPolicyStrippedV1. Its absence is not a hole — it fails the whole
	// import: an archive that does not say its money was cut is an archive nobody promises it was.
	MoneyPolicy string   `json:"money_policy"`
	IDMaps      IDMaps   `json:"id_maps"`
	Contents    Contents `json:"contents"`
	// ExportHoles is what the EXPORT could not put in the archive. The import re-reports these
	// alongside its own holes on purpose: the operator sees where the data was already thin before
	// it travelled.
	ExportHoles []ExportHole `json:"export_holes"`
}

// Source is provenance, never instruction. Nothing may be resolved through it: TechCardID and
// LockVersion belong to the exporting instance and mean nothing in the target base.
type Source struct {
	Host                  string `json:"host"`
	TechCardID            int32  `json:"tech_card_id"`
	StyleNumber           string `json:"style_number"`
	LockVersion           int32  `json:"lock_version"`
	ApprovalStateAtExport string `json:"approval_state_at_export"`
	AppVersion            string `json:"app_version"`
}

// IDMaps are the only dictionaries that survive the trip — the source's ids paired with the names
// that identify the same thing anywhere.
type IDMaps struct {
	// Sizes maps EVERY size id that appears ANYWHERE in the archive — card.json, every sidecar and
	// every marker blob — to size names (size.name is UNIQUE in every instance). A superset is
	// legal and preferred; a subset is not: FORMAT.md §5.7 remaps every size_id inside a marker
	// blob through this table, and a mixed lay (смешанный настил) names sizes that card.json need
	// never mention — a miss is a size_unknown hole that drops the whole marker.
	//
	// JSON object keys are strings, so the id is decimal text.
	Sizes map[string]string `json:"sizes"`
	// CategoryPath is the category triple by name, top level first. Empty = the card had none.
	CategoryPath []string `json:"category_path"`
	// Colorways maps colourway id (= product.id on the source) to its color_code. Reference only:
	// a colourway is a product and does not travel — see colorways.json and reason
	// colorways_not_applied.
	Colorways map[string]string `json:"colorways"`
}

// Contents is what the archive CLAIMS to contain, and it is a positive control, not decoration: an
// import that parsed zero media out of an archive claiming fourteen has a broken parser, not a
// clean card, and must fail instead of reporting success.
type Contents struct {
	Media     int `json:"media"`
	Patterns  int `json:"patterns"`
	Markers   int `json:"markers"`
	Materials int `json:"materials"`
}

// ExportHole is one thing the export could not carry. It is not an error: the export completes and
// the archive is valid — the hole travels so nobody has to guess later why a slot is empty.
type ExportHole struct {
	// Entity is the human word for what this happened to: media, material, bom_line, pattern,
	// marker, size, measurement, operation, assembly, colorway, card, archive. Use the Entity*
	// constants in report.go — that list and this one are one vocabulary (FORMAT.md §7).
	Entity string `json:"entity"`
	// Ref names the row inside its own file — "bom_line_key=…", "media_id=…", "size_name=…".
	Ref string `json:"ref"`
	// Reason is a code from reasons.go — the closed half. Detail is free text — the open half,
	// carrying no contract and safe to reword.
	Reason Reason `json:"reason"`
	Detail string `json:"detail,omitempty"`
}

// MediaIndexEntry is one row of media/index.json: ONE PER MEDIA ID, not per slot — the same photo
// in a sketch slot and a callout is one entry. The usage lives in card.json, where slots reference
// Ref; this index says only "these bytes are this media".
type MediaIndexEntry struct {
	// Ref is the media_id as it appears in card.json — the key the import remaps.
	Ref int32 `json:"ref"`
	// File is the root-relative ZIP entry name, "media/<sha256>.<ext>". Two media ids with
	// identical bytes share one File; that is content-addressing doing its job.
	File string `json:"file"`
	// SHA256 of the FULL-SIZE object as it lies in the bucket — the same bytes media.content_hash
	// is computed over, which is what makes import-side dedup possible at all.
	SHA256 string `json:"sha256"`
	// Kind and Caption are display sugar from the first card slot that names the media: the
	// TechCardMediaKind enum name for the sketch lists, empty for media reached through callouts,
	// details or operations.
	Kind    string `json:"kind,omitempty"`
	Caption string `json:"caption,omitempty"`
	Width   int32  `json:"w,omitempty"`
	Height  int32  `json:"h,omitempty"`
}

// PatternIndexEntry is one row of patterns/index.json — one pattern sheet.
type PatternIndexEntry struct {
	// LineKey is the sheet's stable identity across saves and file replacement; it travels verbatim
	// and is valid on the imported card without any remap.
	LineKey string `json:"line_key"`
	File    string `json:"file"` // "patterns/<sha256>.<dxf|pdf>"
	SHA256  string `json:"sha256"`
	// SizeName nil = the sheet is filed under no size, which is legal and common: such a sheet is
	// graded inside the DXF itself.
	SizeName *string `json:"size_name"`
	Version  int32   `json:"version,omitempty"`
	Name     string  `json:"name,omitempty"`     // operator-entered display name
	Filename string  `json:"filename,omitempty"` // original upload file name
	// FabricPurpose is the binding that matters (TechCardBomPurpose enum name); BomLineKey is the
	// legacy half, kept for sheets on cards nobody has sorted. Resolve purpose first — never read
	// BomLineKey alone.
	FabricPurpose string `json:"fabric_purpose,omitempty"`
	BomLineKey    string `json:"bom_line_key,omitempty"`
}

// MarkerIndexEntry is one row of markers/index.json. The file it names is protojson
// common.TechCardMarker — summary AND layout, geometry self-contained.
//
// THE MARKER BLOB IS THE ONE ENTRY THAT TRAVELS RAW, so it is also the one place where the
// package doc's "no foreign ids written as ids" is kept by the IMPORT rather than by the shape of
// the file: the JSON on disk holds the source instance's numbers verbatim. FORMAT.md §5.7 is the
// contract and this is its summary — inside the blob,
//
//   - summary.id / summary.tech_card_id are ignored and re-minted on the imported card;
//   - every size_id (legacy summary.size_id, both composition lists, layout.pieces[].size_id) is
//     remapped through the same id_maps.sizes name table, and a size the target does not have is
//     ReasonSizeUnknown with the WHOLE marker dropped — a раскладка missing a size of its состав
//     no longer describes the lay that was measured;
//   - summary.colorway_id is ZEROED with a report line (entity=marker,
//     ReasonColorwaysNotApplied): colourways are products, an import creates none, and there is
//     nothing to remap onto;
//   - summary.production_run_id is 0 by construction — only card markers travel;
//   - layout.pieces[].source_url is blanked like every other URL of the exporting instance;
//   - piece_id on pieces/placements is layout-local, not an identity, and is left alone.
type MarkerIndexEntry struct {
	// File is "markers/<slug>-<n>.json". The name is display sugar: a reader locates a marker
	// through this index, never by parsing the file name.
	File string `json:"file"`
	// SizeName nil = the marker has no single size (смешанный настил); its composition lives inside
	// the blob.
	SizeName   *string `json:"size_name"`
	MarkerName string  `json:"marker_name"`
	// BomLineKey is the fabric line the marker was measured for; empty = not linked.
	BomLineKey string `json:"bom_line_key,omitempty"`
}

// MaterialPassport is one row of materials/index.json: everything needed to FIND the same article
// in another catalogue, and nothing that would let anyone price it. The import matches, it never
// creates — an unmatched passport leaves the BOM line's material_id empty and the line imports
// anyway, carrying its own name/supplier/unit.
type MaterialPassport struct {
	// Ref is the source material_id — the key card.json's BOM lines and colorways.json's pins point
	// at, and the whole reason this passport exists. Never written as an id anywhere.
	Ref int64 `json:"ref"`
	// Code is the internal article code and the first matching key. It is unique only among live
	// rows and only in the application — the schema does not enforce it, which is why two live
	// matches are an ambiguity, not a pick.
	Code        string `json:"code,omitempty"`
	Name        string `json:"name"`
	Supplier    string `json:"supplier,omitempty"`
	SupplierRef string `json:"supplier_ref,omitempty"`
	// Composition is the legacy free text; CompositionEntries is the structural one (fibre code +
	// percent). Both travel when both exist.
	Composition        string             `json:"composition,omitempty"`
	CompositionEntries []CompositionEntry `json:"composition_entries,omitempty"`
	Spec               string             `json:"spec,omitempty"` // ширина / плотность
	// Unit is the free text stored on the article; UnitCode is the server's normalisation of it
	// (MaterialUnit enum name). A code match compares the unit, because a material with movements
	// has its unit locked and a mismatch means it is not the same article.
	Unit     string `json:"unit,omitempty"`
	UnitCode string `json:"unit_code,omitempty"`
	// Class is the MaterialClass enum name; it selects which member of Attributes applies.
	Class   string `json:"class,omitempty"`
	Color   string `json:"color,omitempty"`
	Pantone string `json:"pantone,omitempty"`
	// CuttingCoefficient is the roll-reality multiplier (1.03 = +3%), decimal as a string. It is
	// carried because it is a property OF THE ARTICLE that a norm cannot contain, not because it is
	// money — it is a dial, and prices are what does not travel.
	CuttingCoefficient string `json:"cutting_coefficient,omitempty"`
	// FabricThicknessMm is the single-ply thickness in millimetres; empty = never measured, which
	// is not zero.
	FabricThicknessMm string `json:"fabric_thickness_mm,omitempty"`
	Notes             string `json:"notes,omitempty"`
	// Attributes is the CTI typed attribute set — at most one member is populated, chosen by Class.
	Attributes *MaterialAttributes `json:"attributes,omitempty"`
}

// CompositionEntry is one fibre of a structural composition; percents sum to 100 when present.
type CompositionEntry struct {
	FiberCode string `json:"fiber_code"`
	Percent   string `json:"percent"` // decimal as a string
}

// MaterialAttributes carries the typed attributes of whichever class the article belongs to. At
// most one member is set; Other is the raw JSON object used only by MATERIAL_CLASS_OTHER.
type MaterialAttributes struct {
	Fabric    *MaterialFabricAttrs    `json:"fabric,omitempty"`
	Hardware  *MaterialHardwareAttrs  `json:"hardware,omitempty"`
	Thread    *MaterialThreadAttrs    `json:"thread,omitempty"`
	Packaging *MaterialPackagingAttrs `json:"packaging,omitempty"`
	Other     string                  `json:"other,omitempty"`
}

// MaterialFabricAttrs mirrors common.MaterialFabricAttrs. WidthCm is the FULL roll width, кромка
// INCLUDED; the usable cutting width is that minus 2 × SelvedgeCm.
type MaterialFabricAttrs struct {
	WidthCm         string `json:"width_cm,omitempty"`
	WeightGsm       string `json:"weight_gsm,omitempty"`
	FabricDirection string `json:"fabric_direction,omitempty"` // lengthwise|crosswise|any
	ShrinkagePct    string `json:"shrinkage_pct,omitempty"`
	RollLengthM     string `json:"roll_length_m,omitempty"`
	SelvedgeCm      string `json:"selvedge_cm,omitempty"` // per EDGE
}

// MaterialHardwareAttrs mirrors common.MaterialHardwareAttrs.
type MaterialHardwareAttrs struct {
	DiameterMm   string `json:"diameter_mm,omitempty"`
	Dimensions   string `json:"dimensions,omitempty"`
	Finish       string `json:"finish,omitempty"`
	BaseMaterial string `json:"base_material,omitempty"`
	WeightG      string `json:"weight_g,omitempty"`
}

// MaterialThreadAttrs mirrors common.MaterialThreadAttrs.
type MaterialThreadAttrs struct {
	TicketTex      string `json:"ticket_tex,omitempty"`
	LengthPerConeM string `json:"length_per_cone_m,omitempty"`
	NeedleReco     string `json:"needle_reco,omitempty"`
}

// MaterialPackagingAttrs mirrors common.MaterialPackagingAttrs.
type MaterialPackagingAttrs struct {
	Substrate   string `json:"substrate,omitempty"`
	Dimensions  string `json:"dimensions,omitempty"`
	Gsm         string `json:"gsm,omitempty"`
	PrintMethod string `json:"print_method,omitempty"`
}

// AssemblyLink is one line of assembly.json — an auxiliary item (label, tag, packaging) attached to
// the garment. The component travels BY STYLE NUMBER, never by id.
type AssemblyLink struct {
	ComponentStyleNumber string `json:"component_style_number"`
	// SizeName nil = the line applies to all sizes (size_id 0 on the source).
	SizeName     *string `json:"size_name"`
	Qty          string  `json:"qty"` // decimal as a string, > 0
	PrintNote    string  `json:"print_note,omitempty"`
	PositionNote string  `json:"position_note,omitempty"`
	Active       bool    `json:"active"`
}

// ColorwayPayload is one element of colorways.json. Colourways are PRODUCTS and an import does not
// create products: this file travels as reference, so a later explicit action can build draft
// colourways and their recipes from it, and so a human can read what the source card's colourways
// were. Until that action runs, the import reports colorways_not_applied.
type ColorwayPayload struct {
	ColorCode      string              `json:"color_code"`
	BaseSKU        string              `json:"base_sku,omitempty"`
	Recipe         []RecipeLine        `json:"recipe"`
	PieceMaterials []PieceMaterialLine `json:"piece_materials"`
}

// RecipeLine is one row of a colourway's material recipe. It addresses the card by the stable
// line_key family, which travels verbatim and is valid on the imported card without a remap.
//
// A row with PieceLineKey set is a MATERIAL ASSIGNMENT («деталь X кроится из артикула Y»), never a
// norm: the consumption norm lives only on the garment-level row, with the piece unset.
type RecipeLine struct {
	BomLineKey   string `json:"bom_line_key,omitempty"`
	PieceLineKey string `json:"piece_line_key,omitempty"`
	Placement    string `json:"placement,omitempty"`
	Color        string `json:"color,omitempty"`
	Pantone      string `json:"pantone,omitempty"`
	// Consumption is the per-garment rate for measured materials; Quantity is the count for
	// countable trims. Decimals as strings.
	Consumption string `json:"consumption,omitempty"`
	Quantity    string `json:"quantity,omitempty"`
	// SizeConsumptions is the per-size rate keyed BY SIZE NAME (ids of the source base mean nothing
	// in the target).
	SizeConsumptions map[string]string `json:"size_consumptions,omitempty"`
	// MaterialRef is the source material_id this row PINS for the slot, resolved through
	// materials/index.json against the target catalogue. 0 = the row inherits the slot's default
	// article.
	MaterialRef int64 `json:"material_ref,omitempty"`
	// ConsumptionSource is the norm's provenance: "" / "manual" — typed by a person; "marker" — the
	// measured length already contains the cutting waste, so the slot's wastage percent must not
	// gross it up a second time.
	ConsumptionSource string `json:"consumption_source,omitempty"`
	// WasteSelvedgePct / WasteCutPct are the DISPLAY decomposition of a marker-sourced norm's waste.
	// Never multiplied into anything — the marker length already pays for both.
	WasteSelvedgePct string `json:"waste_selvedge_pct,omitempty"`
	WasteCutPct      string `json:"waste_cut_pct,omitempty"`
	// No line_total, no size_run_total, no prices: not "cleaned afterwards" but never asked for.
	// No norm_marker_id either — the stamp points at a marker of the source instance, and a norm
	// whose marker cannot be re-sewn degrades honestly (norm_marker_lost) instead of pointing at a
	// stranger.
}

// PieceMaterialLine is one row of a colourway's piece→cloth mapping (tech_card_piece_material).
// The piece is named here rather than owning the row, because in the archive the COLOURWAY owns
// the mapping — on the wire it hangs off the piece instead.
type PieceMaterialLine struct {
	PieceLineKey     string `json:"piece_line_key"`
	BomLineKey       string `json:"bom_line_key,omitempty"`        // the fabric
	FusingBomLineKey string `json:"fusing_bom_line_key,omitempty"` // the клеевая, if any
	Note             string `json:"note,omitempty"`
}

// SizeChart is sizechart.json: the style's measurement grid and the grade rule it was authored
// from, with BOTH axes by name — size names and measurement names are UNIQUE in every instance,
// their ids are not the same thing twice.
//
// It mirrors common.StyleSizeChart field for field with two deliberate differences, which is why
// the file is not raw protojson: ids are replaced by names (protojson cannot put a name in an
// int32 field), and style_id / lock_version do not travel because they are the source instance's.
type SizeChart struct {
	Cells []SizeChartCell `json:"cells"`
	// GradeBaseSizeName empty together with empty GradeSteps means the chart was typed cell by
	// cell, which is what every pre-existing chart is.
	GradeBaseSizeName string               `json:"grade_base_size_name,omitempty"`
	GradeSteps        []SizeChartGradeStep `json:"grade_steps,omitempty"`
}

// SizeChartCell is one measurement of one size. Value is a decimal as a string.
type SizeChartCell struct {
	SizeName    string `json:"size_name"`
	Measurement string `json:"measurement"`
	Value       string `json:"value"`
}

// SizeChartGradeStep is one measurement's grade increment per size position; may be negative.
type SizeChartGradeStep struct {
	Measurement string `json:"measurement"`
	Step        string `json:"step"`
}
