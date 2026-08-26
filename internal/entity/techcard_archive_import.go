package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrImportAlreadyCommitted is the answer to a SECOND commit of the same uploaded archive.
//
// The upload dialogue is two calls (upload → report → commit) with a row of tech_card_import
// between them, so a double click, a retried request and two operators pressing the same button
// are all the same event: two commits naming one import_id. The store claims the row inside its
// transaction — `status = 'uploaded'` is the guard — so the loser of that race never inserts a
// card at all. It is a FailedPrecondition upstream, not an error the operator has to act on: the
// card the first commit created is already there, and the handler answers with its id.
var ErrImportAlreadyCommitted = errors.New("this archive has already been imported")

// TechCardArchiveImport is everything ONE import writes, gathered before the transaction opens.
//
// WHY IT IS ONE STRUCT AND NOT TEN ARGUMENTS: the write is one transaction by requirement, and a
// method per section would be a method per transaction — an import that half-succeeded, with a
// card in the catalogue whose size chart never landed. Every field below is therefore prepared by
// the handler (Ф3.3) BEFORE the call and written after it only as a whole.
//
// THE ONE RULE THAT GOVERNS EVERY FIELD: no id in here is the source instance's. The archive's
// numbers name rows of a base nobody here administers, and Ф2.3 has already turned each of them
// into a local id or dropped it with a line in the report. What still travels as a foreign KEY is
// the `line_key` family — `bom_line_key`, `piece_line_key`, `scope_key` — which is stable by
// design, valid on the imported card verbatim, and resolved against THIS card's just-inserted rows
// inside the transaction.
type TechCardArchiveImport struct {
	// ImportID is the key of the upload dialogue — the tech_card_import row the commit claims.
	// Empty is refused: without it the import cannot be marked committed and the same archive could
	// be imported twice.
	//
	// It is NOT a ULID, though it wears a ULID's shape: 26 characters of base32 over 128 random
	// bits, minted by tcupMintImportID (internal/apisrv/admin/techcard_archive_upload.go), with no
	// time component and therefore no order. Nothing sorts by it — the import rows are read by id
	// and by tech_card_id — so the missing half costs nothing; the WORD cost something, because a
	// reader who believed it would take the largest string for the newest import.
	ImportID string
	// Actor is the admin username the whole write is stamped with: created_by/updated_by on the
	// card, the assembly lines and the markers, and the author of the journal entry. The archive's
	// own created_by/updated_by travel as text on the card (FORMAT.md §4) and are NOT this.
	Actor string
	// SourceStyleNumber / SourceHost come from manifest.source and are PROVENANCE ONLY — they are
	// spelled into the journal sentence and resolve nothing.
	SourceStyleNumber string
	SourceHost        string

	// Card is the converted payload, already through dto.ConvertPbTechCardInsertToEntity and every
	// wire gate CreateTechCard applies. The store forces it to draft and drops its sign-offs anyway
	// — see the top of ImportTechCardArchive for why that duplication is deliberate.
	Card *TechCardInsert

	// Style is the catalogue half of the card (fit/composition/care/model-wears). It is separate
	// from Card because TechCardInsert cannot carry it: those columns are UpdateStyle's alone on
	// every other path, and neither the tech-card converter nor the create pipeline writes them.
	Style TechCardArchiveStyleFacts

	// SizeChart is the measurement grid with BOTH axes already local (size ids and
	// measurement_name ids resolved by Ф2.3 against this base's two dictionaries). StyleID and
	// LockVersion on it are ignored: the card does not exist yet when this struct is built.
	SizeChart StyleSizeChart

	// Assembly is the auxiliary bill with component_tech_card_id already resolved by style number.
	Assembly []StyleAssemblyInsert

	// Markers are the card's раскладки. Each carries its own BomLineKey — the cloth it was
	// measured on, re-sewn to the imported BOM inside the transaction — and a Composition whose
	// sizes are already local. ProductionRunId is not a member of an import: only card markers
	// travel (FORMAT.md §5.7), and a run's marker belongs to its run.
	Markers []TechCardMarkerInsert

	// PieceAreas are the measured contour areas of the cut pieces, carried so an imported card can
	// state its cloth consumption on arrival instead of waiting for somebody to re-parse the DXF.
	PieceAreas []TechCardArchivePieceArea

	// Labels re-sews the label → BOM line link the resolver had to translate into a key. See
	// TechCardArchiveLabelLink: without this list the link is lost SILENTLY.
	Labels []TechCardArchiveLabelLink

	// Report is the DRY RUN's import report as JSON — the same bytes the RPC answers with. It is
	// stored in the same transaction as the card on purpose: a committed import whose report went
	// missing is a card with unexplained gaps and nothing to explain them.
	//
	// It is NOT stored verbatim. The write drops rows of its own — only the transaction knows the
	// imported card's size range or what a component is in this base — and it adds them to this
	// report before stamping it, in the same transaction (see importLosses in the store). A report
	// stamped as it arrived would count those rows as imported, which is a lie an operator reads
	// exactly once and then believes.
	Report []byte
}

// TechCardArchiveStyleFacts is the style's catalogue half as the archive carries it.
//
// These five columns live on tech_card and are written by UpdateStyle on every other path — the
// tech-card create pipeline does not touch them, so an import that only ran the create pipeline
// would land a card whose fit, composition and care were silently blank. They are read off the
// OUTER TechCard message of card.json (fields 15/16/17/20/21), not off its writable half.
type TechCardArchiveStyleFacts struct {
	Fit                sql.NullString
	Composition        sql.NullString
	CareInstructions   sql.NullString
	ModelWearsHeightCm sql.NullInt32
	// ModelWearsSizeId must be a size id of THIS base, remapped through manifest.id_maps.sizes like
	// every other size in the archive — card.json carries the SOURCE's id in field 21 and nothing
	// remaps it on the way here. The store CLEARS one that is not in the imported card's own size
	// range: «the model wears a size this style does not make» is either a foreign id worn as a
	// local one or a fact about nothing, and one display line is not worth failing an import over.
	ModelWearsSizeId sql.NullInt32
}

// TechCardArchivePieceArea is one measured contour area as it travels in card.json.
//
// It has no sidecar of its own: the areas ride the OUTER TechCard message (piece_area_scopes,
// field 27) — an output-only projection the export does not strip — so they arrive with the card
// rather than beside it. That message carries no sheet fingerprint, which is why the store mints a
// provenance token of its own (see insertImportedPieceAreas).
//
// Both keys travel verbatim and neither is an id: ScopeKey is the fabric scope
// (COALESCE(назначение, bom line_key)) and PieceLineKey is the cut piece's stable key. SizeId is
// the exception and IS an id — remapped like every other size, INVALID meaning «the piece does not
// grade and enters every size's set whole».
//
// PARSED-BY / PARSED-AT ARE THE SOURCE'S AND ARE STORED AS THEY STAND: who measured this geometry
// and when is a fact about the measurement, not about the import, and re-stamping it with today's
// date and this operator's name would claim a measurement nobody took. A ParsedAt that MySQL cannot
// hold — an unset one arrives as the Unix epoch, one second below what a TIMESTAMP column accepts —
// therefore costs the ROW, with a line in the report, and not the import: re-dating it is the very
// claim this paragraph refuses.
type TechCardArchivePieceArea struct {
	ScopeKey     string
	PieceLineKey string
	SizeId       sql.NullInt64
	AreaCm2      decimal.Decimal
	PerimeterCm  decimal.NullDecimal
	ContourLayer string
	// SeamAllowanceMm is one of the measurement's CONDITIONS, per scope on the wire and per row in
	// the table — the same value repeats across a scope's rows.
	SeamAllowanceMm decimal.Decimal
	Hulled          bool
	AmbiguousPick   bool
	ParsedBy        string
	ParsedAt        time.Time
}

// TechCardArchivePieceMaterial is ONE row of a colourway's piece→cloth mapping as the archive
// carries it (colorways.json, FORMAT.md §5.3): «деталь X этого цвета кроится из артикула Y», with
// the клеевая beside it when the piece is fused.
//
// EVERY REFERENCE IS A STABLE LINE KEY AND NOT AN ID, which is the whole reason this type exists
// next to TechCardPieceMaterial rather than being it: the wire type carries the resolved FKs and
// the legacy positional indexes, and both are the SOURCE base's numbers here. The keys travel
// verbatim and are valid on the imported card without any remap, so the store resolves them against
// the card it is writing to and never has anything else to go on.
//
// It is written by the explicit «create colourways from archive» action (Ф6.2) and by nothing else.
// The card save owns this table on every other path.
type TechCardArchivePieceMaterial struct {
	PieceLineKey string
	// BomLineKey is the fabric; empty is legal and means the row states only a note.
	BomLineKey string
	// FusingBomLineKey is the клеевая, empty when the piece is not fused.
	FusingBomLineKey string
	Note             string
}

// TechCardArchiveLabelLink re-sews ONE label to the BOM line it prints on.
//
// TechCardLabel.bom_item_id is a REAL input FK and the archive carries the source base's row id in
// it. Written as it stands it would break the foreign key or — worse — bind the label to another
// card's BOM line, so Ф2.3 translates it into the line's stable key and clears the id off the
// payload. That translation is only half of the transfer: without the re-sew below, the label
// imports with a NULL link and nothing anywhere says so, because there is no hole to report — the
// resolver deliberately wrote none, having handed the second half to the write path.
//
// LabelIndex is the label's position in the payload, which is the only identity a label has: labels
// are a full-replace child with no key of their own, and the store writes that position into
// display_order.
type TechCardArchiveLabelLink struct {
	LabelIndex int
	BomLineKey string
}

// Statuses of a tech_card_import row. The set is held in Go on purpose — 0336 deliberately declines
// a CHECK, because a dictionary CHECK costs a copy of the table on every future extension and fires
// as a raw MySQL 3819 from the middle of an unrelated UPDATE.
const (
	// TechCardImportStatusUploaded — the archive is in the bucket and parsed; no card exists yet.
	TechCardImportStatusUploaded = "uploaded"
	// TechCardImportStatusCommitted — a card was created from it; tech_card_id and report are set.
	TechCardImportStatusCommitted = "committed"
	// TechCardImportStatusFailed — RESERVED: no code path writes it (checked at Ф3.3 review). A
	// rolled-back commit deliberately leaves the row 'uploaded' — the claim happens INSIDE the
	// transaction, so the rollback returns the row with everything else and the operator simply
	// presses commit again, which is strictly better than a stuck upload. The word stays in the
	// vocabulary as a manual quarantine: a row hand-set to 'failed' is refused by the commit
	// pre-flight with «upload the file again», and the expiry sweep never repaints it.
	TechCardImportStatusFailed = "failed"
	// TechCardImportStatusExpired — the bucket object aged out before anybody committed it.
	TechCardImportStatusExpired = "expired"
)

// TechCardArchiveImportRecord is one row of tech_card_import as the read paths need it.
//
// It carries the two raw JSON payloads because both are answers to questions asked after the
// bucket object is gone: ArchiveManifest is «what was in the ZIP», and ColorwaysPayload is what the
// later, explicit «create colourways from archive» action builds from (FORMAT.md §5.3). Both are
// stored and returned VERBATIM — a round trip through a struct of this server's version would drop
// the fields a newer MINOR carries, under a label claiming to be what the archive said.
type TechCardArchiveImportRecord struct {
	Id         int           `db:"id"`
	ImportID   string        `db:"import_id"`
	TechCardID sql.NullInt32 `db:"tech_card_id"`
	ObjectKey  string        `db:"object_key"`
	Status     string        `db:"status"`
	ImportedBy string        `db:"imported_by"`
	CreatedAt  time.Time     `db:"created_at"`
	// CommittedAt / AcknowledgedAt are INVALID until the commit and the operator's «read» happen —
	// they are two different events and the banner on the card reads the second one.
	CommittedAt      sql.NullTime `db:"committed_at"`
	AcknowledgedAt   sql.NullTime `db:"acknowledged_at"`
	ArchiveManifest  []byte       `db:"archive_manifest"`
	ColorwaysPayload []byte       `db:"colorways_payload"`
	Report           []byte       `db:"report"`
}
