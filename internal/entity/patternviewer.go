package entity

import "database/sql"

// PatternViewerCard is the NARROW read behind the public viewer manifest
// (GET /api/pv/{token}): exactly the card facts the viewer page shows and nothing more.
// It deliberately bypasses GetTechCardById — that read pulls the whole card including
// costing (RBAC-stripped per admin account), and this data ends up on an
// unauthenticated endpoint where no account exists to strip for.
type PatternViewerCard struct {
	TechCardId  int
	StyleNumber string
	StyleName   string
	// Sizes is the card's size range with dictionary names, in display order. The viewer
	// needs the NAMES: graded DXF sheets carry sizes as block-name tokens, and the page
	// derives its size switcher tokens from these names.
	Sizes []PatternViewerSize
	// Sheets are ALL tech_card_size_pattern rows of the card in display order, with both
	// halves of the fabric binding — the manifest handler resolves display groups from
	// them (a sheet with an empty url is skipped there, as the печать does).
	Sheets []PatternViewerSheet
	// BomLines are the card's roll-goods BOM lines in display order; groups derive from
	// them (one per назначение present, one per unsorted line) and line-keyed groups are
	// labelled with the line's name.
	BomLines []PatternViewerBomLine
}

// PatternViewerSize is one size of the card's range, named from the dictionary.
type PatternViewerSize struct {
	Id   int    `db:"id"`
	Name string `db:"name"`
}

// PatternViewerSheet is one выкройка row as the viewer manifest needs it. SizeId 0 means
// the sheet is filed under no size (graded DXF, NULL since 0281).
type PatternViewerSheet struct {
	LineKey string `db:"line_key"`
	SizeId  int    `db:"size_id"`
	// SizeName is resolved from the size DICTIONARY, not from the card's range, so a sheet
	// still filed under a size the grade has since dropped is named rather than rendered as
	// «no size». Those two states look identical on the wire otherwise (empty name), and the
	// viewer reads an unnamed sized sheet as sizeless — captioning a plain PDF «градуированный»
	// and promising a size switcher that will not appear. "" only when SizeId is 0.
	SizeName      string       `db:"size_name"`
	URL           string       `db:"url"`
	Filename      string       `db:"filename"`
	Name          string       `db:"name"`
	SizeBytes     int64        `db:"size_bytes"`
	Version       int          `db:"version"`
	UploadedAt    sql.NullTime `db:"uploaded_at"`
	BomLineKey    string       `db:"bom_line_key"`
	FabricPurpose string       `db:"fabric_purpose"`
}

// PatternViewerBomLine is one roll-goods BOM line as the manifest's grouping sees it:
// stable line_key, display name, and its назначение ("" = not sorted yet).
type PatternViewerBomLine struct {
	LineKey string `db:"line_key"`
	Name    string `db:"name"`
	Purpose string `db:"purpose"`
}
