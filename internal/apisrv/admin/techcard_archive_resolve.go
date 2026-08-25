package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// ─────────────────────────────────────────────────────────────────────────────
// Ф2.3 — THE IDENTITY RESOLVER: what is this archive, HERE?
//
// An archive arrives from a base nobody here administers. Every number in it — sizes, categories,
// media, materials, markers, measurements — names a row of THAT base, and the same number names a
// different row (or no row) here. This file is where each of those foreign names is turned into a
// local one, and where the ones that cannot be turned into anything are DROPPED WITH A LINE IN THE
// REPORT.
//
// THE OWNER'S RULE, WHICH DECIDES EVERY BRANCH BELOW: a gap is a SKIP WITH A REPORT, not a refusal.
// No article with that code here — the BOM line arrives unlinked, carrying its own name, supplier
// and unit (it has had them since 0068), and the operator reads a line saying so. Refusing the whole
// import over one missing article was considered and rejected explicitly. The ONE thing that may
// never pass quietly is a LIE: a row that pretends it imported, and a counter that pretends it is
// zero. That is why holes and counters are two separate outputs here (see Ф2.4's report.go) — a
// success writes no line, so a report built out of lines alone would say "nothing was imported"
// about a clean card.
//
// WHAT THIS FILE MAY NOT DO: write. Not to the database, not to the bucket. It READS the target base
// and returns a plan; Ф3.1 uploads the files and Ф3.2 writes the rows inside one transaction. A
// resolver that wrote would make a dry run (Ф2.5) indistinguishable from an import.
//
// SIX IDENTITY MAPS, and each one is a different mechanism, not six spellings of one:
//
//	sizes        manifest.id_maps.sizes (source id → name) against the target size dictionary
//	measurements sizechart.json names against measurement_name (its SECOND named axis)
//	category     manifest.id_maps.category_path (a triple of names) walked down the category tree
//	media        media/index.json sha256 against media.content_hash — a match by CONTENT, not by id
//	materials    materials/index.json passports against the catalogue by code / supplier pair
//	works        operation.work tokens against the work catalogue (0329) — a string key, not an id
//
// Names in this package: `dec` and the `tcz*` prefix are taken (costing_rbac_test.go, Ф1.2/Ф1.4);
// everything private to the import side is prefixed `tcimp`.
// ─────────────────────────────────────────────────────────────────────────────

// tcimpMediaAction is what the import intends to do about one media file of the archive.
type tcimpMediaAction string

const (
	// tcimpMediaReuse — a media row in THIS base already holds these exact bytes
	// (media.content_hash, migration 0336), so the card points at it and nothing is uploaded.
	//
	// ⚠️ THE HASH DESCRIBES THE FULL-SIZE OBJECT AND ONLY IT. A picture in the bucket is three
	// objects (full size, compressed, thumbnail); a GIF or a video is one. So a hash match means
	// "the same full-size file is already stored", NOT "the same set of variants". Reusing the row
	// is still right — the row is what a slot references, and its variants are whatever this base
	// made of those same bytes — but nobody may read a match as a promise about thumbnails.
	tcimpMediaReuse tcimpMediaAction = "reuse"
	// tcimpMediaUpload — nothing here holds these bytes; Ф3.1 uploads the file out of the ZIP and
	// mints a media row, then substitutes its id for the placeholder below.
	tcimpMediaUpload tcimpMediaAction = "upload"
)

// tcimpMediaPlan is one media file of the archive and what becomes of it.
type tcimpMediaPlan struct {
	// SourceID is media_id as card.json uses it — the key the slots point at.
	SourceID int32
	Action   tcimpMediaAction
	// TargetID is the existing media row to reuse; set only for tcimpMediaReuse.
	TargetID int32
	// Placeholder is the NEGATIVE id that stands in the insert for an upload until Ф3.1 mints the
	// real one. Negative on purpose and not "the old id kept as-is": a source id left in place
	// would, on the day it happens to equal a real row here, point the card at a stranger's
	// picture — silently and forever. A negative id can equal no row at all, so a substitution
	// that Ф3.2 forgets fails loudly on the foreign key instead of importing a lie.
	//
	// Two source media whose bytes are identical share ONE placeholder: the archive stores one file
	// for them (content-addressed naming, FORMAT.md §1.1) and this base should store one row.
	Placeholder int32
	// File is the ZIP entry name; SHA256 is the digest Ф3.1 verifies while streaming it.
	File   string
	SHA256 string
	// Kind / Caption / Width / Height are the index's display sugar, carried through so the row
	// Ф3.1 mints can be described without re-reading the archive.
	Kind    string
	Caption string
	Width   int32
	Height  int32
}

// tcimpPatternPlan is one pattern sheet that WILL be imported: its stable line_key plus the file to
// upload. A sheet with no file in the archive never reaches this list — see resolvePatterns.
type tcimpPatternPlan struct {
	LineKey  string
	File     string
	SHA256   string
	Filename string
}

// tcimpMarkerPlan is one раскладка to insert on the imported card, already re-identified: sizes
// remapped, the source row's own id and card id dropped, the colourway zeroed, urls blanked.
type tcimpMarkerPlan struct {
	// Name is the marker's name, unique per (card, size key) in the target base.
	Name string
	// SizeName is the index's label; empty means a mixed lay (смешанный настил) whose состав lives
	// inside the blob. Carried for the report, not for resolution — the blob is the authority.
	SizeName   string
	BomLineKey string
	Marker     *pb_common.TechCardMarker
}

// tcimpLabelLink re-sews one label's link to the BOM line it prints on, BY NAME.
//
// TechCardLabel.bom_item_id is a real input FK to tech_card_bom_item and it is the source base's
// row id: written as it stands it would either break the foreign key (killing the whole import) or,
// worse, bind the label to another card's BOM line. It is nevertheless not lost, because card.json
// carries both halves — every BOM item travels with its source `id` AND its stable `line_key` — so
// the id is translated into a key here, cleared off the payload, and re-sewn by Ф3.2 once the BOM
// rows have their new ids. Same shape as the norm's marker stamp, and the same reason.
type tcimpLabelLink struct {
	// LabelIndex is the position in Insert.Labels, which is the only identity a label has (labels
	// are a full-replace child with no stable key of their own).
	LabelIndex int
	BomLineKey string
}

// tcimpMaterialMatch is one passport's verdict against this catalogue, kept for every passport in
// the archive — not only for the ones a BOM line uses.
//
// The pins of colorways.json resolve through the SAME passports (FORMAT.md §5.3: material_ref is a
// key into materials/index.json), so resolving every passport once resolves every pin. The result
// travels as a PLAN rather than as report lines because an import creates no colourways: a line
// saying "create this article and link the BOM line by hand" would name a BOM line that does not
// exist. Ф6.2 reports its own holes on the day the colourways are actually applied.
type tcimpMaterialMatch struct {
	TargetID int64
	Verdict  techcardarchive.MaterialVerdict
}

// resolvedTechCardImport is everything the write side needs and nothing it has to re-derive.
type resolvedTechCardImport struct {
	// Insert is the card as it should land HERE: sanitised to draft, money-free, foreign ids either
	// remapped or dropped.
	//
	// ⚠️ IT IS DELIBERATELY NOT CONVERTIBLE YET. Media ids of files still to be uploaded carry the
	// negative placeholders of MediaPlan, and surviving pattern rows carry a blank url — both are
	// substituted after Ф3.1 has moved the bytes. ConvertPbTechCardInsertToEntity refuses either, and
	// that refusal is the safety net: a write path that forgets the substitution fails loudly instead
	// of storing a card that points at nothing.
	Insert *pb_common.TechCardInsert

	// Card is the WHOLE card.json message AFTER the gates — the outer half that Insert is a
	// submessage of, sanitised and money-free by the same two calls.
	//
	// It is published rather than left private because the alternative is worse and was measured:
	// Archive.CardJSON() re-parses the ZIP entry on EVERY call, so any later reader that wants the
	// card's read-side half (its provenance stamps, its catalogue facts) and asks the archive for it
	// gets a SECOND, UNSANITISED message — approvals intact, prices intact — that merely looks like
	// the one the resolver worked on. Handing out the sanitised object is what makes that mistake
	// unnecessary. Its two branches that the write path actually needs are already lifted into
	// StylePlan and PieceAreaPlan below; nothing has to be re-derived from here.
	Card *pb_common.TechCard

	MediaPlan     []tcimpMediaPlan
	PatternPlan   []tcimpPatternPlan
	MarkerPlan    []tcimpMarkerPlan
	SizeChartPlan entity.StyleSizeChart
	AssemblyPlan  []entity.StyleAssemblyInsert
	LabelPlan     []tcimpLabelLink
	// MaterialPlan is keyed by the passport's `ref` (the source material_id).
	MaterialPlan map[int64]tcimpMaterialMatch

	// StylePlan and PieceAreaPlan are the two branches of card.json that do NOT live under
	// TechCardInsert — they ride the OUTER TechCard message and therefore travel beside Insert
	// rather than inside it. See section 13: they are the reason this resolver is handed the whole
	// card message and not only its writable half.
	StylePlan     entity.TechCardArchiveStyleFacts
	PieceAreaPlan []entity.TechCardArchivePieceArea

	// ColorwaysRaw is colorways.json VERBATIM. Colourways are products and an import creates none;
	// the bytes are stored so the later, explicit "create colourways from archive" action (Ф6.2)
	// has the source's recipe to build from. Verbatim rather than re-marshalled for the reason
	// Archive.ManifestRaw states: a newer MINOR carries fields this server has no member for, and a
	// re-marshal would drop them under the label "what the archive said".
	ColorwaysRaw json.RawMessage

	// Holes are what could not be placed cleanly; Counters are how much was seen. TWO SOURCES, kept
	// separate on purpose — see report.go.
	Holes    []techcardarchive.ImportHole
	Counters techcardarchive.Counters
}

// tcimpResolver is the working state of one resolve. It exists so the six maps, the hole list and
// the tally are not eleven parameters threaded through twenty functions.
type tcimpResolver struct {
	s   *Server
	a   *techcardarchive.Archive
	out *resolvedTechCardImport

	// card is the WHOLE card.json message, not just its writable half.
	//
	// It is held because two branches of the archive live on the outer message and nowhere else:
	// the style's catalogue facts (fit/composition/care/model_wears_*) and the measured piece areas
	// (piece_area_scopes). A resolver that kept only Insert could not see either — which is exactly
	// how model_wears_size_id and piece_area_scopes[].areas[].size_id once travelled with the
	// SOURCE base's numbers intact. Also NOT re-read from the archive at the call site:
	// Archive.CardJSON() parses the ZIP entry afresh on every call, so a second reader would get a
	// second, UNSANITISED message and resolve it through a second copy of this mapping.
	card *pb_common.TechCard

	// sizeMapping is source size id → target size id, built from manifest.id_maps.sizes against the
	// target dictionary. sizeNameBySourceID keeps the NAME of every source id the manifest declared,
	// so a miss can be reported as «size_name=xl» (the operator's dictionary entry) rather than as a
	// number of somebody else's base.
	sizeMapping        map[int64]int64
	sizeNameBySourceID map[int64]string
	sizeIDByName       map[string]int
	sizeMissed         map[int64]bool

	// syntheticSizeID keys the negative stand-ins minted for size names the manifest never mapped,
	// so one unknown name counts as one unresolved size however many rows mention it.
	syntheticSizeID map[string]int64

	measurementIDByName map[string]int

	// holeSeen dedupes by (entity, ref, reason). One missing size is referenced by ten fields of the
	// card and would otherwise produce ten identical lines; the tally still counts the rows.
	holeSeen map[string]bool
}

// resolveTechCardImport turns an opened archive into a plan for THIS base.
//
// It reads the target database (dictionaries, the material catalogue, the work catalogue, media
// hashes, the auxiliary-style index) and writes nothing anywhere. Errors returned here are
// INFRASTRUCTURE or a corrupt archive — the whole import fails on them, and only on them; every
// missing reference degrades into a hole.
func (s *Server) resolveTechCardImport(ctx context.Context, a *techcardarchive.Archive) (*resolvedTechCardImport, error) {
	if a == nil || a.Manifest == nil {
		return nil, fmt.Errorf("tech card import: no opened archive to resolve")
	}

	di, err := s.repo.Cache().GetDictionaryInfo(ctx)
	if err != nil {
		return nil, fmt.Errorf("tech card import: load dictionary: %w", err)
	}

	r := &tcimpResolver{
		s: s, a: a,
		out: &resolvedTechCardImport{
			Counters:     techcardarchive.NewCounters(),
			MaterialPlan: map[int64]tcimpMaterialMatch{},
		},
		sizeIDByName:        make(map[string]int, len(di.Sizes)),
		measurementIDByName: make(map[string]int, len(di.Measurements)),
		sizeMapping:         map[int64]int64{},
		sizeNameBySourceID:  map[int64]string{},
		sizeMissed:          map[int64]bool{},
		syntheticSizeID:     map[string]int64{},
		holeSeen:            map[string]bool{},
	}
	for _, sz := range di.Sizes {
		r.sizeIDByName[tcimpKey(sz.Name)] = sz.Id
	}
	for _, m := range di.Measurements {
		r.measurementIDByName[tcimpKey(m.Name)] = m.Id
	}
	r.buildSizeMapping()

	card, err := a.CardJSON()
	if err != nil {
		return nil, err
	}
	insert := card.GetTechCard()
	if insert == nil {
		// Not a hole: card.json is mandatory (FORMAT.md §1) and its writable half is the card. An
		// archive without it has nothing to import, and reporting "0 holes" about it would be the
		// exact false green the positive control exists to catch.
		return nil, fmt.Errorf("%w: %s carries no tech_card payload", techcardarchive.ErrCorrupt, techcardarchive.FileCard)
	}
	r.out.Insert = insert
	r.out.Card = card
	r.card = card

	// FIRST, BEFORE ANY REMAPPING, and in this order (sanitize.go says the same):
	//  1. approval — an imported card may never LOOK signed, and the create pipeline COERCES
	//     sign-offs rather than refusing them, so the only defence is not to hand it any;
	//  2. money — the manifest's money_policy is a CLAIM by the archive about itself. The reader
	//     refuses an archive that does not carry the flag, which stops a pre-versioned bundle; it
	//     cannot stop a hand-made one that types the flag and keeps the prices. Running the export's
	//     own denylist over the incoming card is the check the flag is supposed to sit next to.
	//
	// THE DENYLIST RUNS OVER `card`, NOT OVER `insert`, and that word is load-bearing: the export
	// redacts the WHOLE message (buildArchiveCardJSON), so anything narrower here is an import that
	// redacts less than the export did — a denylist with a hole in exactly the half our own exporter
	// bothers to clean. The half in question is not hypothetical any more: section 13 reads the outer
	// message for the style's catalogue facts and the measured piece areas, and the outer message is
	// also where AdminColorwayRef lives with cost_price / prices / net_prices on it. Our exporter nils
	// that list (sanitizeCardForArchive) — a hand-made archive does not have to. Since `insert` is a
	// submessage of `card`, ONE call covers both halves; running two would be two lists to keep in step.
	techcardarchive.SanitizeImportedCard(insert)
	techcardarchive.RedactFieldsDeep(card.ProtoReflect(), techcardarchive.MoneyFieldNamesArchive)

	// The six compatibility shields, all of them, exactly as the season clone sets them
	// (style.go): this payload is built by the SERVER out of a card our own exporter read with our
	// own converters, so it knows every field by construction. operation_work_aware is not
	// future-proofing but load-bearing today — the wire rule refuses a non-empty `work` from a
	// payload that did not declare support, and an imported card carrying work tokens would be
	// refused its own content.
	insert.AssemblyAware = true
	insert.MachineFieldsAware = true
	insert.MediaAware = true
	insert.OperationKindsAware = true
	insert.OperationWorkAware = true
	insert.BomQtyAware = true

	r.resolveCategory(di.Categories)
	if err := r.resolveMedia(ctx); err != nil {
		return nil, err
	}
	r.resolveSizes()
	// AFTER resolveSizes and not before it, so that a size missing from BOTH the card's own body
	// and the outer half is reported by the line naming the card's field: the hole list dedupes by
	// (entity, ref, reason) and the FIRST line for one missing size is the one the operator reads.
	r.resolveStyleFacts()
	r.resolvePieceAreas()
	if err := r.resolveMaterials(ctx); err != nil {
		return nil, err
	}
	if err := r.resolveWorkTokens(ctx); err != nil {
		return nil, err
	}
	if err := r.resolvePatterns(); err != nil {
		return nil, err
	}
	r.resolveForeignScalars()
	if err := r.resolveSizeChart(); err != nil {
		return nil, err
	}
	if err := r.resolveAssembly(ctx); err != nil {
		return nil, err
	}
	if err := r.resolveColorways(); err != nil {
		return nil, err
	}
	if err := r.resolveMarkers(); err != nil {
		return nil, err
	}
	r.reportUnknownEntries()
	r.tallySizes()

	return r.out, nil
}

// ────────────────────────────── holes and counters ──────────────────────────────

// hole records one thing the import could not do cleanly, deduped by (entity, ref, reason).
//
// The dedupe is about READABILITY, never about the tally: one missing size is referenced by the
// size range, three size-chart rows and a marker, and five identical lines say nothing the first
// does not. The counters are added separately by the caller of this method, so nothing is lost.
func (r *tcimpResolver) hole(entityName, ref, status string, reason techcardarchive.Reason, detail string) {
	key := entityName + "|" + ref + "|" + string(reason)
	if r.holeSeen[key] {
		return
	}
	r.holeSeen[key] = true
	r.out.Holes = append(r.out.Holes, techcardarchive.ImportHole{
		Entity: entityName, Ref: ref, Status: status, Reason: reason, Detail: detail,
	})
}

// tcimpKey normalises a dictionary NAME for lookup: trimmed and case-folded. Size and measurement
// names are UNIQUE per instance, and the uniqueness is the database's — whose collation is
// case-insensitive — so "M" and "m" are one size here as they are there.
func tcimpKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// ────────────────────────────── 1. sizes ──────────────────────────────

// buildSizeMapping turns manifest.id_maps.sizes (source id → name) into source id → LOCAL id.
//
// The manifest maps every size id that appears ANYWHERE in the archive, including inside marker
// blobs (FORMAT.md §5.7), which is why this one table serves the card, the sidecars and the
// раскладки alike. A size the manifest names but this dictionary does not have is remembered as
// missed — the hole is written where the size is actually USED, so that an archive whose manifest
// ships the source's whole dictionary (legal and preferred) does not produce a line per unused size.
func (r *tcimpResolver) buildSizeMapping() {
	for rawID, name := range r.a.Manifest.IDMaps.Sizes {
		id, err := strconv.ParseInt(strings.TrimSpace(rawID), 10, 64)
		if err != nil || id <= 0 {
			// A key that is not a positive decimal names nothing. JSON object keys are strings and
			// this one was supposed to be a number; skipping is right and silent is right, because
			// no field of the card can reference it either.
			continue
		}
		r.sizeNameBySourceID[id] = name
		if local, ok := r.sizeIDByName[tcimpKey(name)]; ok {
			r.sizeMapping[id] = int64(local)
		}
	}
}

// sizeRef names a source size the way an operator can act on it: by the NAME they would add to
// their dictionary when the manifest knows it, and only by the foreign number when it does not
// (an archive whose id_maps.sizes is an incomplete subset — illegal per §2, and it still has to
// produce something readable rather than nothing).
func (r *tcimpResolver) sizeRef(sourceID int64) string {
	if name := r.sizeNameBySourceID[sourceID]; name != "" {
		return fmt.Sprintf("size_name=%s", name)
	}
	return fmt.Sprintf("size_id=%d", sourceID)
}

// resolveSizes remaps every size FK of the card and then removes the rows a cleared FK would have
// turned into nonsense.
//
// The generic walk (Ф0.4) does the arithmetic: 0 is never touched (it means "unset" across the
// whole contract — a sheet filed under no size, no base sample size), a missing entry is DROPPED
// from a repeated field rather than replaced by 0, and a missing scalar is CLEARED rather than left
// pointing at a local row that merely shares the number.
//
// What the walk cannot know is which of those cleared scalars leaves a legal row behind.
// TechCardSizePattern.size_id = 0 is legal and documented ("the sheet is graded inside the DXF");
// TechCardSizeQuantity.size_id = 0 is not — the row says "the typical batch for size X" and without
// X it says nothing — so that row is dropped instead. Hence the pass below.
func (r *tcimpResolver) resolveSizes() {
	ins := r.out.Insert
	techcardarchive.RemapIntFieldsDeep(ins.ProtoReflect(), techcardarchive.SizeFieldNames, r.sizeMapping,
		func(field string, old int64) {
			r.sizeMissed[old] = true
			r.hole(techcardarchive.EntitySize, r.sizeRef(old), techcardarchive.StatusSkipped,
				techcardarchive.ReasonSizeUnknown,
				fmt.Sprintf("the size is not in this base's dictionary; every %s filed under it was dropped", field))
		})

	kept := ins.SizeQuantities[:0]
	for _, q := range ins.SizeQuantities {
		if q.GetSizeId() > 0 {
			kept = append(kept, q)
		}
	}
	ins.SizeQuantities = kept
}

// tallySizes counts the size axis once, at the end: the card's surviving size range on the imported
// side, every source size that could not be resolved anywhere on the skipped side.
//
// Counted from the RANGE and not from the number of remapped fields, because the population an
// operator recognises is "the sizes this style is made in" — a size referenced by nine fields is
// still one size.
func (r *tcimpResolver) tallySizes() {
	r.out.Counters.AddImported(techcardarchive.EntitySize, len(r.out.Insert.GetSizeIds()))
	r.out.Counters.AddSkipped(techcardarchive.EntitySize, len(r.sizeMissed))
}

// ────────────────────────────── 2. category ──────────────────────────────

// resolveCategory walks the manifest's triple of NAMES down this base's category tree and writes
// the id of the most specific node it reaches.
//
// The card's own category_id travels in card.json and is ALWAYS overwritten here — it is the source
// base's row id and matches nothing but by accident. An unresolvable path lands as 0, which the
// contract states outright as "unset" (techcard.proto:3104), plus a hole so the operator knows to
// set it. The card imports either way: a category is a filing decision, not a fact about the
// garment. Only a card that DEMONSTRABLY had no category at the source is silent — see below.
//
// The walk is parent → child and never positional, because the tree is not uniformly three deep:
// `dresses` hangs its level-3 types directly off the level-1 top (see DeriveStyleCategoryPath), so
// a legal path can be two names long. Following parent_id answers correctly in both shapes.
func (r *tcimpResolver) resolveCategory(categories []entity.Category) {
	ins := r.out.Insert
	source := ins.GetCategoryId()
	ins.CategoryId = 0

	path := r.a.Manifest.IDMaps.CategoryPath
	if len(path) == 0 {
		if source == 0 {
			// The card had none. Nothing was lost, so nothing is reported.
			return
		}
		// AN EMPTY PATH IS NOT THE SAME STATEMENT AS "NO CATEGORY". archiveCategoryPath stops at the
		// first level it cannot NAME, so a card that HAD a category can still arrive with nothing to
		// walk — and reading that as "the card had none" would drop a filing decision in silence,
		// which is the exact failure mode the report exists to prevent. The source id is the only
		// handle either side has on it, and it names nothing here, which is why the line says so.
		r.hole(techcardarchive.EntityCard, fmt.Sprintf("category_id=%d", source),
			techcardarchive.StatusDegraded, techcardarchive.ReasonCategoryUnknown,
			fmt.Sprintf("the export could not NAME the source's category (id=%d), so the archive carries no "+
				"category path to walk; the card landed without a category", source))
		return
	}

	byParent := make(map[int][]entity.Category, len(categories))
	for _, c := range categories {
		parent := 0
		if c.ParentID != nil {
			parent = *c.ParentID
		}
		byParent[parent] = append(byParent[parent], c)
	}

	current := 0
	for _, want := range path {
		next := 0
		for _, c := range byParent[current] {
			if tcimpKey(c.Name) == tcimpKey(want) {
				next = c.ID
				break
			}
		}
		if next == 0 {
			r.hole(techcardarchive.EntityCard, fmt.Sprintf("category_path=%s", strings.Join(path, "/")),
				techcardarchive.StatusDegraded, techcardarchive.ReasonCategoryUnknown,
				fmt.Sprintf("no category named %q under this base's tree at that level; the card landed without a category "+
					"(the archive's own category_id was %d, which means nothing here)", want, source))
			return
		}
		current = next
	}
	ins.CategoryId = int32(current)
}

// ────────────────────────────── 3. media ──────────────────────────────

// resolveMedia decides what happens to every picture the card references, then rewrites the card's
// media FKs accordingly.
//
// Two questions, in this order:
//
//  1. Does the archive carry the bytes at all? media/index.json lists one entry per media id, and an
//     entry whose file is absent (or a slot whose id is in no entry — an export hole
//     media_object_missing, re-reported from the manifest) has nothing to import: the FK is cleared
//     and the slot reports media_missing.
//  2. Does this base already hold those bytes? FindMediaByContentHash (0336) answers by CONTENT, so
//     a photo shared by two cards is stored once however many archives it arrives in. No match means
//     an upload, which Ф3.1 performs — until then the insert carries a negative placeholder.
//
// After the remap, a MediaItem or an operation photo whose id was cleared is REMOVED: the converter
// rejects media_id <= 0 outright ("a step picture with no media means nothing"), so leaving the row
// would trade one missing picture for a failed import. A callout is the deliberate exception — its
// media_id = 0 is documented as "not anchored to a picture" and the pin keeps its geometry.
func (r *tcimpResolver) resolveMedia(ctx context.Context) error {
	var index []techcardarchive.MediaIndexEntry
	if _, err := r.readSidecar(techcardarchive.FileMediaIndex, &index); err != nil {
		return err
	}

	mapping := make(map[int64]int64, len(index))
	placeholderBySHA := make(map[string]int32, len(index))
	for _, e := range index {
		if e.Ref <= 0 {
			continue
		}
		if !r.a.Has(e.File) {
			// The index names a file the ZIP does not carry. Nothing is planned for it; the card's
			// slots will miss the mapping below and report media_missing where they are USED, which
			// is where an operator can do something about it.
			slog.Default().WarnContext(ctx, "tech card import: media index names a file the archive does not carry",
				slog.String("file", e.File), slog.Int("media_id", int(e.Ref)))
			continue
		}

		plan := tcimpMediaPlan{
			SourceID: e.Ref, File: e.File, SHA256: e.SHA256,
			Kind: e.Kind, Caption: e.Caption, Width: e.Width, Height: e.Height,
		}
		existing, err := r.s.repo.Media().FindMediaByContentHash(ctx, e.SHA256)
		if err != nil {
			return fmt.Errorf("tech card import: look media up by content hash: %w", err)
		}
		switch {
		case existing != nil && existing.Id > 0:
			plan.Action = tcimpMediaReuse
			plan.TargetID = int32(existing.Id)
			mapping[int64(e.Ref)] = int64(existing.Id)
		default:
			plan.Action = tcimpMediaUpload
			ph, ok := placeholderBySHA[e.SHA256]
			if !ok || e.SHA256 == "" {
				// One placeholder per DISTINCT content; an entry with no digest gets its own,
				// because "" is not a content identity and folding two of them together would
				// merge two different pictures into one row.
				ph = int32(-(len(placeholderBySHA) + 1))
				if e.SHA256 != "" {
					placeholderBySHA[e.SHA256] = ph
				} else {
					// Keep the counter moving without keying on the empty digest.
					placeholderBySHA[fmt.Sprintf("no-digest:%d", e.Ref)] = ph
				}
			}
			plan.Placeholder = ph
			mapping[int64(e.Ref)] = int64(ph)
		}
		r.out.MediaPlan = append(r.out.MediaPlan, plan)
	}

	missing := map[int64]bool{}
	techcardarchive.RemapIntFieldsDeep(r.out.Insert.ProtoReflect(), techcardarchive.MediaFieldNames, mapping,
		func(_ string, old int64) {
			missing[old] = true
			r.hole(techcardarchive.EntityMedia, fmt.Sprintf("media_id=%d", old), techcardarchive.StatusSkipped,
				techcardarchive.ReasonMediaMissing,
				"the archive carries no file for this picture; the slot was left empty and the rest of the card imported")
		})
	r.dropEmptyMediaRows()

	r.out.Counters.AddImported(techcardarchive.EntityMedia, len(r.out.MediaPlan))
	r.out.Counters.AddSkipped(techcardarchive.EntityMedia, len(missing))
	return nil
}

// dropEmptyMediaRows removes the rows a cleared media FK would have made unwritable. A pre-existing
// zero (a malformed card.json) is dropped by the same pass and needs no hole of its own: nothing was
// referenced, so nothing was lost.
//
// ZERO, NOT «not positive». A negative id here is a PLACEHOLDER this resolver wrote for a file Ф3.1
// still has to upload, and dropping those would delete every picture the target base does not
// already hold — the commonest case of all. After the remap above, the only negative values in the
// tree are ours: a negative that arrived in card.json is not in the mapping, so it was cleared to 0
// and reported like any other miss.
func (r *tcimpResolver) dropEmptyMediaRows() {
	ins := r.out.Insert
	ins.MoodboardMedia = tcimpKeepMediaItems(ins.MoodboardMedia)
	ins.TechnicalMedia = tcimpKeepMediaItems(ins.TechnicalMedia)
	for _, op := range ins.GetOperations() {
		if op == nil {
			continue
		}
		kept := op.Media[:0]
		for _, m := range op.Media {
			if m.GetMediaId() != 0 {
				kept = append(kept, m)
			}
		}
		op.Media = kept
	}
}

func tcimpKeepMediaItems(items []*pb_common.TechCardMediaItem) []*pb_common.TechCardMediaItem {
	kept := items[:0]
	for _, m := range items {
		if m.GetMediaId() != 0 {
			kept = append(kept, m)
		}
	}
	return kept
}

// ────────────────────────────── 4. materials ──────────────────────────────

// resolveMaterials matches every passport in the archive against this catalogue once, then links —
// or unlinks — every BOM line accordingly.
//
// A LINE ALWAYS IMPORTS. Since 0068 a BOM line carries its own name, supplier, supplier_ref,
// composition, spec and unit, so an unmatched article costs the CATALOGUE LINK and nothing else:
// the constructor still reads what the cloth is. That is the whole reason the owner's "a gap is a
// skip with a report" is affordable here.
//
// The catalogue is read ONCE, with archived rows included, because MatchMaterial needs to see them
// to ignore them deliberately (the code's uniqueness is a promise about live rows only).
func (r *tcimpResolver) resolveMaterials(ctx context.Context) error {
	var passports []techcardarchive.MaterialPassport
	if _, err := r.readSidecar(techcardarchive.FileMaterialsIndex, &passports); err != nil {
		return err
	}

	lines := r.out.Insert.GetBomItems()
	needsCatalogue := len(passports) > 0
	if !needsCatalogue {
		for _, b := range lines {
			if b.GetMaterialId() > 0 {
				needsCatalogue = true
				break
			}
		}
	}

	byRef := make(map[int64]techcardarchive.MaterialPassport, len(passports))
	for _, p := range passports {
		byRef[p.Ref] = p
	}
	if needsCatalogue {
		mats, err := r.s.repo.TechCards().ListMaterials(ctx, "", true)
		if err != nil {
			return fmt.Errorf("tech card import: load material catalogue: %w", err)
		}
		catalog := make([]entity.Material, 0, len(mats))
		for i := range mats {
			// entity.Material, not MaterialWithPrice: the matcher must not be able to reach a price.
			catalog = append(catalog, mats[i].Material)
		}
		for _, p := range passports {
			id, verdict := techcardarchive.MatchMaterial(p, catalog)
			r.out.MaterialPlan[p.Ref] = tcimpMaterialMatch{TargetID: id, Verdict: verdict}
		}
	}

	var imported, degraded int
	for _, b := range lines {
		if b == nil {
			continue
		}
		source := b.GetMaterialId()
		if source <= 0 {
			// A line that never named an article is complete as it stands.
			imported++
			continue
		}
		ref := fmt.Sprintf("bom_line_key=%s", b.GetLineKey())
		b.MaterialId = 0

		p, ok := byRef[source]
		if !ok {
			// The export could not put a passport in the archive (manifest.export_holes already
			// says so with material_not_found); this is the second half of the same news, on the
			// side where the line actually lands unlinked.
			degraded++
			r.hole(techcardarchive.EntityMaterial, ref, techcardarchive.StatusDegraded,
				techcardarchive.ReasonMaterialNotFound,
				fmt.Sprintf("the archive carries no passport for material_id=%d, so there was nothing to match; "+
					"the line imported with its own name, supplier and unit", source))
			continue
		}
		match := r.out.MaterialPlan[p.Ref]
		if match.Verdict == techcardarchive.MaterialMatched && match.TargetID > 0 {
			b.MaterialId = match.TargetID
			imported++
			continue
		}
		degraded++
		r.hole(techcardarchive.EntityMaterial, ref, techcardarchive.StatusDegraded,
			techcardarchive.ReasonForMaterialVerdict(match.Verdict),
			fmt.Sprintf("passport %q (%s / %s) did not resolve to one live article here; the line imported unlinked",
				p.Code, p.Supplier, p.SupplierRef))
	}

	r.out.Counters.AddImported(techcardarchive.EntityBOMLine, imported)
	r.out.Counters.AddDegraded(techcardarchive.EntityBOMLine, degraded)

	r.resolveOutputMaterial(byRef)
	return nil
}

// resolveOutputMaterial links the auxiliary card's output article — the warehouse bucket its
// production run receipts into — through the SAME passports and the same three verdicts as a pin.
//
// It lives here and not in resolveForeignScalars because it is not a foreign scalar any more: the
// export puts a passport for it in materials/index.json under `ref = output_material_id`
// (FORMAT.md §5.4), so there is something to match. That is the whole fix — no new reason code was
// invented at this call site (reasons.go forbids it), the existing material codes carry it.
//
// Reported rather than logged, unlike base_model_id: on an auxiliary card this article is a
// property OF THE CARD, required before its first production run, and a server log is not a report.
// Counted against no entity: it is not a BOM line, and adding it to that tally would make the
// line count disagree with the number of lines.
func (r *tcimpResolver) resolveOutputMaterial(byRef map[int64]techcardarchive.MaterialPassport) {
	source := int64(r.out.Insert.GetOutputMaterialId())
	if source <= 0 {
		return
	}
	// Cleared first, in every branch: the source's row id must not survive even for the instant it
	// takes to decide, and a match writes the target's id over it.
	r.out.Insert.OutputMaterialId = 0

	p, ok := byRef[source]
	if !ok {
		r.hole(techcardarchive.EntityMaterial, archiveRefOutputMaterial, techcardarchive.StatusDegraded,
			techcardarchive.ReasonMaterialNotFound,
			fmt.Sprintf("the archive carries no passport for the output article (material_id=%d), so there was "+
				"nothing to match; set the card's output material by hand before its first production run", source))
		return
	}
	if match := r.out.MaterialPlan[source]; match.Verdict == techcardarchive.MaterialMatched && match.TargetID > 0 {
		r.out.Insert.OutputMaterialId = int32(match.TargetID)
		return
	}
	r.hole(techcardarchive.EntityMaterial, archiveRefOutputMaterial, techcardarchive.StatusDegraded,
		techcardarchive.ReasonForMaterialVerdict(r.out.MaterialPlan[source].Verdict),
		fmt.Sprintf("the output article's passport %q (%s / %s) did not resolve to one live article here; set the "+
			"card's output material by hand before its first production run", p.Code, p.Supplier, p.SupplierRef))
}

// ────────────────────────────── 5. work tokens ──────────────────────────────

// resolveWorkTokens clears every operation.work this base's catalogue does not know.
//
// WITHOUT THIS THE WHOLE IMPORT DIES: the column carries a foreign key onto operation_work (0330),
// and a token this base never seeded fails the write with a bare 1452 — or, on the path that checks
// first, refuses the payload by name. An unknown work costs the operation its THIRD axis and
// nothing else: the verb, the machine and every property block stay exactly as authored, because
// those are the step's own facts and not a dictionary reference.
//
// SCOPE, DELIBERATELY NARROW: only "is this token in the catalogue". The write path also refuses a
// token whose VERB disagrees with the step's and a token on a machine outside its list, and neither
// is re-checked here — both need the canonicalised verb of the step, which is derived deep inside
// the converter, and a second, approximate copy of that derivation would clear valid tokens while
// claiming they are unknown. Both catalogues are seeded by the same migrations, so a token that
// exists on both sides carries the same verb on both sides; if that ever stops being true the write
// refuses by field name, loudly, which is the honest failure.
func (r *tcimpResolver) resolveWorkTokens(ctx context.Context) error {
	ops := r.out.Insert.GetOperations()
	var wanted bool
	for _, op := range ops {
		if strings.TrimSpace(op.GetWork()) != "" {
			wanted = true
			break
		}
	}

	known := map[string]bool{}
	if wanted {
		// Archived — sorry, RETIRED — works are included: a retired row is still in the catalogue
		// and still satisfies the foreign key, and hiding it is the picker's job, not the import's.
		works, err := r.s.repo.TechCards().GetOperationWorkCatalog(ctx)
		if err != nil {
			return fmt.Errorf("tech card import: load work catalogue: %w", err)
		}
		for _, w := range works {
			known[w.Token] = true
		}
	}

	var imported, degraded int
	for i, op := range ops {
		if op == nil {
			continue
		}
		token := strings.TrimSpace(op.GetWork())
		if token == "" || known[token] {
			imported++
			continue
		}
		op.Work = ""
		degraded++
		r.hole(techcardarchive.EntityOperation, tcimpOperationRef(op, i), techcardarchive.StatusDegraded,
			techcardarchive.ReasonWorkTokenUnknown,
			fmt.Sprintf("the work %q is not in this base's work catalogue; the step imported without its work — "+
				"its verb, zone and machine are untouched", token))
	}

	r.out.Counters.AddImported(techcardarchive.EntityOperation, imported)
	r.out.Counters.AddDegraded(techcardarchive.EntityOperation, degraded)
	return nil
}

// tcimpOperationRef names a step the way the printed sheet does — by its human number — falling
// back to the position when the card leaves numbering to the server.
func tcimpOperationRef(op *pb_common.TechCardOperation, index int) string {
	if n := op.GetOperationNumber(); n > 0 {
		return fmt.Sprintf("operation_number=%d", n)
	}
	return fmt.Sprintf("operation_index=%d", index)
}

// ────────────────────────────── 6. patterns ──────────────────────────────

// resolvePatterns keeps only the sheets the archive actually carries a file for, and drops the rest
// BEFORE anything tries to convert them.
//
// The order is the point, and it comes from R1-1. The export blanks pattern.url (it is the source
// instance's object key worn as a URL), and ConvertPbTechCardInsertToEntity REQUIRES a non-empty
// url on a managed host. A sheet row with no file behind it would therefore reach the converter with
// an empty url and fail the ENTIRE import — one lost sheet taking the whole card with it — instead
// of producing the one hole it deserves. So the row leaves the payload here, while the payload is
// still ours to edit.
//
// A surviving row keeps its blank url on purpose: Ф3.1 substitutes the re-uploaded one, and anything
// Ф3.1 did not reach must still fail loudly rather than convert.
func (r *tcimpResolver) resolvePatterns() error {
	var index []techcardarchive.PatternIndexEntry
	if _, err := r.readSidecar(techcardarchive.FilePatternsIndex, &index); err != nil {
		return err
	}
	byKey := make(map[string]techcardarchive.PatternIndexEntry, len(index))
	for _, e := range index {
		if key := strings.TrimSpace(e.LineKey); key != "" {
			byKey[key] = e
		}
	}

	ins := r.out.Insert
	kept := ins.Patterns[:0]
	var skipped int
	for _, p := range ins.Patterns {
		if p == nil {
			continue
		}
		key := strings.TrimSpace(p.GetLineKey())
		ref := fmt.Sprintf("line_key=%s", key)
		if key == "" {
			ref = fmt.Sprintf("pattern_name=%s", p.GetName())
		}

		e, ok := byKey[key]
		if key == "" || !ok || !r.a.Has(e.File) {
			skipped++
			r.hole(techcardarchive.EntityPattern, ref, techcardarchive.StatusSkipped,
				techcardarchive.ReasonPatternInvalid,
				"the archive carries no file for this sheet, so the row was dropped rather than imported "+
					"pointing at nothing; upload the sheet by hand on the patterns tab")
			continue
		}
		r.out.PatternPlan = append(r.out.PatternPlan, tcimpPatternPlan{
			LineKey: key, File: e.File, SHA256: e.SHA256, Filename: e.Filename,
		})
		kept = append(kept, p)
	}
	ins.Patterns = kept

	r.out.Counters.AddImported(techcardarchive.EntityPattern, len(r.out.PatternPlan))
	r.out.Counters.AddSkipped(techcardarchive.EntityPattern, skipped)
	return nil
}

// ────────────────────────────── 7. the rest of the foreign scalars ──────────────────────────────

// resolveForeignScalars deals with the ids that belong to no map at all.
//
// FORMAT.md §6.2 says every id in the archive is either remapped or dropped. These three have no
// dictionary travelling beside them, so they are dropped — but they are dropped in THREE different
// ways, because they mean three different things:
//
//   - pieces[].materials is the per-colourway "this piece is cut from that article" mapping, and its
//     colorway_id is a product id of the source base. Colourways do not travel (§5.3), so the whole
//     list goes, with a colorways_not_applied line naming the piece;
//   - labels[].bom_item_id IS recoverable: card.json carries every BOM line's source id next to its
//     stable line_key, so the link is translated into a key for Ф3.2 to re-sew after the insert;
//   - base_model_id names a fit model in the source's model table, and no model dictionary travels.
//     Cleared, and only LOGGED: the closed reason dictionary has no code for it, and inventing one at
//     a call site is forbidden (reasons.go). This is the same choice the export makes for a marker
//     that vanishes mid-read — a loud log beats a made-up code. Our own exports no longer put one in
//     card.json at all (sanitizeCardForArchive); this is what stands under a hand-made archive.
//
// output_material_id USED TO BE the fourth. It is not one any more: the export ships a passport for
// it in materials/index.json, so it resolves through resolveMaterials like every other pin — and
// because it does, it must not be cleared here, which runs AFTER that.
//
// The output-only ids (bom_items[].id, operations[].piece_ids / bom_item_ids) are cleared too. The
// converter ignores them today, which is exactly why they are worth clearing: a payload carrying
// another base's row ids is one refactor away from somebody deciding to trust them.
func (r *tcimpResolver) resolveForeignScalars() {
	ins := r.out.Insert

	// Labels first — the translation reads bom_items[].id, which the loop below then clears.
	lineKeyByBomID := make(map[int32]string, len(ins.GetBomItems()))
	for _, b := range ins.GetBomItems() {
		if b.GetId() > 0 && b.GetLineKey() != "" {
			lineKeyByBomID[int32(b.GetId())] = b.GetLineKey()
		}
	}
	for i, l := range ins.GetLabels() {
		if l == nil || l.GetBomItemId() <= 0 {
			continue
		}
		if key := lineKeyByBomID[l.GetBomItemId()]; key != "" {
			r.out.LabelPlan = append(r.out.LabelPlan, tcimpLabelLink{LabelIndex: i, BomLineKey: key})
		}
		l.BomItemId = 0
	}

	for _, b := range ins.GetBomItems() {
		if b != nil {
			b.Id = 0
		}
	}
	for _, op := range ins.GetOperations() {
		if op != nil {
			op.PieceIds = nil
			op.BomItemIds = nil
		}
	}

	var pieceHoles int
	for _, p := range ins.GetPieces() {
		if p == nil || len(p.GetMaterials()) == 0 {
			continue
		}
		p.Materials = nil
		pieceHoles++
		r.hole(techcardarchive.EntityColorway, fmt.Sprintf("piece_line_key=%s", p.GetLineKey()),
			techcardarchive.StatusSkipped, techcardarchive.ReasonColorwaysNotApplied,
			"this piece named the cloth it is cut from PER COLOURWAY, and colourways are products that an "+
				"import does not create; the piece imported without that mapping")
	}

	if id := ins.GetBaseModelId(); id > 0 {
		ins.BaseModelId = 0
		slog.Default().Warn("tech card import: dropped the source base's fit model reference",
			slog.Int("base_model_id", int(id)))
	}
}

// ────────────────────────────── 8. size chart (and its SECOND axis) ──────────────────────────────

// resolveSizeChart rebuilds the style's measurement grid against BOTH of this base's dictionaries.
//
// sizechart.json carries names, not ids, on both axes — and they are two axes, not one spelled
// twice. A size name that is missing sends the operator to the size dictionary; a MEASUREMENT name
// that is missing sends them to the measurement dictionary, and telling them "size unknown" about a
// measurement would send them to the wrong one. Hence measurement_unknown as its own code, its own
// entity word, and its own line.
//
// Neither dictionary is written to. They are shared vocabularies of the whole instance — sizes,
// measurements and categories alike — and an import that quietly added "чест" next to "chest"
// because one archive spelled it that way would corrupt every other style's chart with it.
func (r *tcimpResolver) resolveSizeChart() error {
	var chart techcardarchive.SizeChart
	ok, err := r.readSidecar(techcardarchive.FileSizeChart, &chart)
	if err != nil || !ok {
		return err
	}

	out := entity.StyleSizeChart{}
	for _, c := range chart.Cells {
		sizeID, ok := r.lookupChartSize(c.SizeName, "cell")
		if !ok {
			continue
		}
		measurementID, ok := r.lookupMeasurement(c.Measurement, "cell")
		if !ok {
			continue
		}
		value, err := decimal.NewFromString(strings.TrimSpace(c.Value))
		if err != nil {
			// A decimal that is not a decimal is a malformed sidecar, not a missing reference, and
			// the closed dictionary has no code for it. Dropping the one cell keeps the chart; the
			// log is what stops it being silent.
			slog.Default().Warn("tech card import: size chart cell carries an unreadable value",
				slog.String("size", c.SizeName), slog.String("measurement", c.Measurement),
				slog.String("value", c.Value))
			continue
		}
		out.Cells = append(out.Cells, entity.StyleSizeChartCell{
			SizeID: sizeID, MeasurementNameID: measurementID, Value: value,
		})
	}

	if name := strings.TrimSpace(chart.GradeBaseSizeName); name != "" {
		if id, ok := r.lookupChartSize(name, "grade base"); ok {
			out.GradeBaseSizeID = id
		}
	}
	for _, st := range chart.GradeSteps {
		measurementID, ok := r.lookupMeasurement(st.Measurement, "grade step")
		if !ok {
			continue
		}
		step, err := decimal.NewFromString(strings.TrimSpace(st.Step))
		if err != nil {
			slog.Default().Warn("tech card import: grade step carries an unreadable value",
				slog.String("measurement", st.Measurement), slog.String("step", st.Step))
			continue
		}
		out.GradeSteps = append(out.GradeSteps, entity.StyleSizeChartGradeStep{
			MeasurementNameID: measurementID, Step: step,
		})
	}

	// A grade rule with a base and no steps (or steps and no base) is half a rule and would read as
	// an authored one. Both halves or neither — the cells stand on their own either way, which is
	// what every pre-existing chart looks like.
	if out.GradeBaseSizeID == 0 || len(out.GradeSteps) == 0 {
		out.GradeBaseSizeID, out.GradeSteps = 0, nil
	}
	r.out.SizeChartPlan = out
	return nil
}

// lookupChartSize resolves a size NAME off the chart and records the hole itself, so the two call
// sites cannot report the same miss two different ways.
func (r *tcimpResolver) lookupChartSize(name, where string) (int, bool) {
	id, ok := r.sizeIDByName[tcimpKey(name)]
	if ok {
		return id, true
	}
	r.sizeMissed[r.sourceSizeIDForName(name)] = true
	r.hole(techcardarchive.EntitySize, fmt.Sprintf("size_name=%s", name), techcardarchive.StatusSkipped,
		techcardarchive.ReasonSizeUnknown,
		fmt.Sprintf("the size chart's %s names a size this base's dictionary does not have; the row was dropped "+
			"and the rest of the chart imported", where))
	return 0, false
}

// sourceSizeIDForName finds the source id the manifest gave a name, so a chart miss and a card miss
// count as ONE unresolved size rather than two.
//
// A name the manifest never mapped gets a synthetic NEGATIVE key, memoised by name: it still has to
// count (the size axis's skipped column is the only place an operator sees how many sizes went
// missing), it can collide with no real source id, and memoising is what stops the same unknown name
// appearing in three chart rows from counting as three different sizes.
func (r *tcimpResolver) sourceSizeIDForName(name string) int64 {
	key := tcimpKey(name)
	for id, n := range r.sizeNameBySourceID {
		if tcimpKey(n) == key {
			return id
		}
	}
	if id, ok := r.syntheticSizeID[key]; ok {
		return id
	}
	id := -int64(len(r.syntheticSizeID) + 1)
	r.syntheticSizeID[key] = id
	return id
}

func (r *tcimpResolver) lookupMeasurement(name, where string) (int, bool) {
	id, ok := r.measurementIDByName[tcimpKey(name)]
	if ok {
		return id, true
	}
	r.hole(techcardarchive.EntityMeasurement, fmt.Sprintf("measurement=%s", name), techcardarchive.StatusSkipped,
		techcardarchive.ReasonMeasurementUnknown,
		fmt.Sprintf("the size chart's %s names a measurement this base's dictionary does not have; the row was "+
			"dropped and the rest of the chart imported", where))
	return 0, false
}

// ────────────────────────────── 9. assembly ──────────────────────────────

// resolveAssembly re-points the style's assembly bill at THIS base's auxiliary cards.
//
// The component travels by STYLE NUMBER and never by id (§5.2), so the resolution is a lookup in a
// map of this base's auxiliary styles. A component that does not exist here is a hole with the same
// code the export uses when the component was already gone on that side — assembly_component_not_found
// — and the line is not written: an assembly line pointing at nothing is a label nobody sews on.
//
// Only AUXILIARY cards are candidates, which is not a shortcut but the store's own rule
// (UpsertStyleAssembly refuses a component whose purpose is not auxiliary), so narrowing the read to
// them cannot hide a legal match.
func (r *tcimpResolver) resolveAssembly(ctx context.Context) error {
	var links []techcardarchive.AssemblyLink
	ok, err := r.readSidecar(techcardarchive.FileAssembly, &links)
	if err != nil || !ok || len(links) == 0 {
		return err
	}

	byNumber, err := r.s.auxiliaryStyleNumbers(ctx)
	if err != nil {
		return err
	}

	var imported, skipped int
	for _, l := range links {
		number := strings.TrimSpace(l.ComponentStyleNumber)
		ref := fmt.Sprintf("component_style_number=%s", number)
		id, found := byNumber[tcimpKey(number)]
		if number == "" || !found {
			skipped++
			r.hole(techcardarchive.EntityAssembly, ref, techcardarchive.StatusSkipped,
				techcardarchive.ReasonAssemblyComponentNotFound,
				"no auxiliary style with that number in this base; the assembly line was not imported")
			continue
		}

		qty, err := decimal.NewFromString(strings.TrimSpace(l.Qty))
		if err != nil || !qty.IsPositive() {
			// The store refuses a non-positive qty with a field violation, which would take the
			// whole import down over one label line. No reason code covers "the sidecar's number is
			// not a number", so the line is dropped, counted and logged.
			skipped++
			slog.Default().WarnContext(ctx, "tech card import: assembly line carries an unusable quantity",
				slog.String("component", number), slog.String("qty", l.Qty))
			continue
		}

		item := entity.StyleAssemblyInsert{
			ComponentTechCardId: id,
			Qty:                 qty,
			PrintNote:           tcimpNullString(l.PrintNote),
			PositionNote:        tcimpNullString(l.PositionNote),
			Active:              l.Active,
		}
		if l.SizeName != nil {
			sizeID, ok := r.sizeIDByName[tcimpKey(*l.SizeName)]
			if !ok {
				// NULL here would mean «this line applies to EVERY size», i.e. a different number
				// of labels per run. Widening it silently is worse than dropping it, which is the
				// same judgement the export makes on the same row.
				skipped++
				r.sizeMissed[r.sourceSizeIDForName(*l.SizeName)] = true
				r.hole(techcardarchive.EntitySize, fmt.Sprintf("size_name=%s", *l.SizeName),
					techcardarchive.StatusSkipped, techcardarchive.ReasonSizeUnknown,
					"an assembly line is filed under this size, which this base's dictionary does not have; the "+
						"line was dropped rather than widened to every size")
				continue
			}
			item.SizeId = tcimpNullInt32(int32(sizeID))
		}
		r.out.AssemblyPlan = append(r.out.AssemblyPlan, item)
		imported++
	}

	r.out.Counters.AddImported(techcardarchive.EntityAssembly, imported)
	r.out.Counters.AddSkipped(techcardarchive.EntityAssembly, skipped)
	return nil
}

// auxiliaryStyleNumbers indexes this base's auxiliary styles by style number, folded for comparison.
//
// Paged to the end rather than one page deep: a partial index would report a component that exists
// as missing, which is precisely the kind of confident wrong answer a hole must never be. Auxiliary
// cards are labels, tags and packaging — tens of rows, not the whole catalogue — so the walk is
// short in practice and bounded by the count the store itself reports.
func (s *Server) auxiliaryStyleNumbers(ctx context.Context) (map[string]int, error) {
	const page = 100
	out := map[string]int{}
	for offset := 0; ; offset += page {
		cards, total, err := s.repo.TechCards().ListTechCards(ctx, page, offset, entity.Ascending,
			entity.TechCardListFilter{Purpose: string(entity.TechCardPurposeAuxiliary)})
		if err != nil {
			return nil, fmt.Errorf("tech card import: list auxiliary styles: %w", err)
		}
		for i := range cards {
			number := strings.TrimSpace(cards[i].StyleNumber.String)
			if number == "" {
				continue
			}
			if _, dup := out[tcimpKey(number)]; !dup {
				out[tcimpKey(number)] = cards[i].Id
			}
		}
		if len(cards) == 0 || offset+len(cards) >= total {
			return out, nil
		}
	}
}

// ────────────────────────────── 10. colourways ──────────────────────────────

// resolveColorways carries colorways.json through untouched and says so, once per colourway.
//
// Colourways are PRODUCTS (§5.3) and an import creates no products, so there is nothing to resolve
// and nothing to write. What there is, is a promise to keep: the bytes travel to Ф6.2's explicit
// "create colourways from archive" action, and until somebody runs it the report says plainly that
// the colours the source card was made in did not arrive.
func (r *tcimpResolver) resolveColorways() error {
	if !r.a.Has(techcardarchive.FileColorways) {
		return nil
	}
	// Read ONCE and keep the bytes: they are what travels to Ф6.2, and re-marshalling the parsed
	// form would drop whatever a newer MINOR added — under the label "what the archive said".
	raw, err := r.a.ReadFile(techcardarchive.FileColorways)
	if err != nil {
		return err
	}
	var payloads []techcardarchive.ColorwayPayload
	if err := json.Unmarshal(raw, &payloads); err != nil {
		return fmt.Errorf("%w: %s does not parse: %v", techcardarchive.ErrCorrupt, techcardarchive.FileColorways, err)
	}
	r.out.ColorwaysRaw = json.RawMessage(raw)

	for _, c := range payloads {
		r.hole(techcardarchive.EntityColorway, fmt.Sprintf("color_code=%s", c.ColorCode),
			techcardarchive.StatusSkipped, techcardarchive.ReasonColorwaysNotApplied,
			fmt.Sprintf("the source card's colourway %q (%d recipe rows) travelled as reference only; "+
				"create it here and apply the archive's recipe when you need it", c.ColorCode, len(c.Recipe)))
	}
	r.out.Counters.AddSkipped(techcardarchive.EntityColorway, len(payloads))
	return nil
}

// ────────────────────────────── 11. markers ──────────────────────────────

// resolveMarkers re-identifies every раскладка of the card. It is the one entry that travels as RAW
// protojson, so FORMAT.md §5.7's list is not a description of what the file contains — it is the
// whole contract for what the IMPORT must do to it, and it is executed here literally:
//
//   - summary.id / summary.tech_card_id are the source ROW's own identity. Zeroed: the marker is
//     inserted on the imported card and takes that card's numbers.
//   - EVERY size_id in the blob (the legacy summary one, both composition lists, every layout piece)
//     goes through the same id_maps.sizes table as the rest of the archive. A size this base does not
//     have DROPS THE WHOLE MARKER rather than leaving a gap in its состав: a раскладка missing one
//     size no longer describes the lay that was measured, and the piece-instance formula would hand
//     the orphaned contour zero instances without saying anything.
//   - summary.colorway_id is ZEROED with a report line. Colourways are products and none is created,
//     so there is nothing to remap onto; the marker lands as общекарточная geometry, and the hole is
//     what records that the length was measured on ONE colourway's article — at its roll width and
//     its кромка.
//   - summary.production_run_id is 0 by construction (only card markers travel). A blob carrying one
//     belongs to a run and is not imported.
//   - layout.pieces[].source_url points at the exporting instance's CDN and is blanked; the contours
//     are inside the blob.
//   - piece_id on pieces and placements is LAYOUT-LOCAL — stable inside this one blob, referenced
//     nowhere else — and is deliberately left alone.
func (r *tcimpResolver) resolveMarkers() error {
	var index []techcardarchive.MarkerIndexEntry
	if _, err := r.readSidecar(techcardarchive.FileMarkersIndex, &index); err != nil {
		return err
	}

	var imported, skipped, degraded int
	for _, e := range index {
		ref := fmt.Sprintf("marker_name=%s", e.MarkerName)
		if !r.a.Has(e.File) {
			skipped++
			slog.Default().Warn("tech card import: marker index names a file the archive does not carry",
				slog.String("file", e.File), slog.String("marker", e.MarkerName))
			continue
		}
		raw, err := r.a.ReadFile(e.File)
		if err != nil {
			return err
		}
		var marker pb_common.TechCardMarker
		if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, &marker); err != nil {
			return fmt.Errorf("%w: marker %s does not parse: %v", techcardarchive.ErrCorrupt, e.File, err)
		}

		summary := marker.GetSummary()
		if summary.GetProductionRunId() > 0 {
			// A production run's marker belongs to the run, not to the style. The card read that
			// built the archive already filtered them out; this is the second half of the same
			// statement, so a change of that read cannot smuggle one in.
			skipped++
			slog.Default().Warn("tech card import: skipped a production run's marker found in a style archive",
				slog.String("file", e.File), slog.Int("production_run_id", int(summary.GetProductionRunId())))
			continue
		}

		var missed []int64
		techcardarchive.RemapIntFieldsDeep(marker.ProtoReflect(), techcardarchive.SizeFieldNames, r.sizeMapping,
			func(_ string, old int64) { missed = append(missed, old) })
		if len(missed) > 0 {
			skipped++
			for _, old := range missed {
				r.sizeMissed[old] = true
			}
			r.hole(techcardarchive.EntityMarker, ref, techcardarchive.StatusSkipped,
				techcardarchive.ReasonSizeUnknown,
				fmt.Sprintf("the раскладка is laid out in %s, which this base's dictionary does not have; the whole "+
					"marker was skipped, because one measured on a different set of sizes is not the lay that was measured",
					r.sizeRef(missed[0])))
			continue
		}

		if summary != nil {
			summary.Id = 0
			summary.TechCardId = 0
			if summary.GetColorwayId() != 0 {
				summary.ColorwayId = 0
				degraded++
				r.hole(techcardarchive.EntityMarker, ref, techcardarchive.StatusDegraded,
					techcardarchive.ReasonColorwaysNotApplied,
					"this раскладка was measured on ONE colourway's article — at its roll width and its кромка — and "+
						"colourways do not travel; it imported as geometry of the whole card")
			} else {
				imported++
			}
		} else {
			imported++
		}
		for _, p := range marker.GetLayout().GetPieces() {
			p.SourceUrl = ""
		}

		var sizeName string
		if e.SizeName != nil {
			sizeName = *e.SizeName
		}
		r.out.MarkerPlan = append(r.out.MarkerPlan, tcimpMarkerPlan{
			Name: e.MarkerName, SizeName: sizeName, BomLineKey: e.BomLineKey, Marker: &marker,
		})
	}

	r.out.Counters.AddImported(techcardarchive.EntityMarker, imported)
	r.out.Counters.AddSkipped(techcardarchive.EntityMarker, skipped)
	r.out.Counters.AddDegraded(techcardarchive.EntityMarker, degraded)
	return nil
}

// ────────────────────────────── 12. entries this server does not know ──────────────────────────────

// reportUnknownEntries turns the reader's list of unrecognised entries into report lines.
//
// This is the MINOR-compatibility rule of FORMAT.md §3 being executed rather than described: a newer
// exporter may add files and an older reader may not choke on them — and may not swallow them
// either. The operator is told the archive holds something this server cannot read, which is how a
// missing piece stops looking like an absent one.
//
// A FILE A PLAN ALREADY CLAIMED IS NOT UNKNOWN. The reader classifies an entry by its NAME, and the
// name carries the extension — so `media/<sha>.avif`, whose extension is outside §1.1, lands in
// UnknownEntries even though media/index.json points a slot at it and the plan above will move its
// bytes. Both lines would then be true of the same file and the second one false in spirit: "this
// server does not know this file" beside a plan to upload it. The index is what makes a file known;
// the plans are the executed proof that it was read.
func (r *tcimpResolver) reportUnknownEntries() {
	claimed := make(map[string]bool, len(r.out.MediaPlan)+len(r.out.PatternPlan))
	for _, m := range r.out.MediaPlan {
		claimed[m.File] = true
	}
	for _, p := range r.out.PatternPlan {
		claimed[p.File] = true
	}
	for _, name := range r.a.UnknownEntries {
		if claimed[name] {
			continue
		}
		r.hole(techcardarchive.EntityArchive, fmt.Sprintf("entry=%s", name), techcardarchive.StatusSkipped,
			techcardarchive.ReasonUnknownEntry,
			"this server does not know this file — the archive was written by a newer version of the format")
	}
}

// ────────── 13. the OUTER card message: catalogue facts and measured piece areas ──────────
//
// EVERYTHING ABOVE THIS LINE WORKS ON `Insert`, AND THAT WAS THE HOLE. TechCardInsert is what the
// write path receives, so it is what the generic walk (Ф0.4) was ever run over — and two branches of
// card.json do not live there:
//
//   - model_wears_size_id (field 21 of TechCard) — a size FK, LISTED in techcardarchive.SizeFieldNames
//     since the list was written, so the walk would have translated it the moment it reached the
//     message. It never reached it;
//   - piece_area_scopes (field 27) — whose areas carry a size FK each.
//
// Both are read-only projections written by other RPCs (UpdateStyle and SaveTechCardPieceAreas), which
// is why they are on the outer message at all, and both are carried by the export verbatim. Left
// unremapped they land in the target base wearing the SOURCE's numbers, and nothing downstream can
// tell: the store's own guard (writeImportedStyleFacts / insertImportedPieceAreas) only asks whether
// the id falls inside the IMPORTED CARD'S size range — which, after the remap above, is made of
// target ids, while both dictionaries are small integers. A foreign id therefore lands INSIDE the
// range far more often than not, and the row imports silently under the wrong size. For a piece area
// that is not cosmetic: the per-size areas feed the base-size costing, so an "S" contour filed under
// the target's "M" quietly moves the cloth norm.
//
// FORMAT.md §6.2 — «no foreign id is ever written» — is therefore only kept if the outer message is
// resolved here, through the SAME r.sizeMapping. A second mapping built at the handler is not an
// option and not a style preference: Archive.CardJSON() re-parses the entry on every call, so the
// handler would be holding a second, unsanitised message, and two copies of one translation drift.

// resolveStyleFacts lifts the style's catalogue half off the outer message, translating the one id
// among the five.
//
// fit / composition / care_instructions / model_wears_* are UpdateStyle's columns — the tech-card
// create pipeline writes none of them — so without this plan an imported card lands with its fit,
// composition and care silently blank. The three strings and the height are facts, not references,
// and travel verbatim; the empty-to-NULL rule is the one ConvertPbStylePatchToEntity applies on the
// live path, so an imported style and an edited one store the same thing for "not stated".
//
// model_wears_size_id goes through r.sizeMapping BY HAND, under the walk's own three rules: 0 is
// «unset» across the whole contract and is never remapped, a value the manifest's table cannot place
// is a size_unknown hole and lands NULL, and the source's number is never written through. It counts
// into sizeMissed like every other miss — one size missing here and in the card's body is ONE
// unresolved size on the report's size axis, not two.
func (r *tcimpResolver) resolveStyleFacts() {
	c := r.card
	height := c.GetModelWearsHeightCm()
	facts := entity.TechCardArchiveStyleFacts{
		Fit:              tcimpNullString(c.GetFit()),
		Composition:      tcimpNullString(c.GetComposition()),
		CareInstructions: tcimpNullString(c.GetCareInstructions()),
		// 0 is «unknown» on the wire (techcard.proto, field 20) and NULL in the column.
		ModelWearsHeightCm: sql.NullInt32{Int32: height, Valid: height != 0},
	}

	if src := int64(c.GetModelWearsSizeId()); src != 0 {
		if local, ok := r.sizeMapping[src]; ok {
			facts.ModelWearsSizeId = sql.NullInt32{Int32: int32(local), Valid: true}
		} else {
			r.sizeMissed[src] = true
			r.hole(techcardarchive.EntitySize, r.sizeRef(src), techcardarchive.StatusSkipped,
				techcardarchive.ReasonSizeUnknown,
				"the size the lookbook model wears is not in this base's dictionary; the card imported "+
					"without that reference rather than pointing at whichever local size shares the number")
		}
	}

	r.reportCompositionEntries()
	r.out.StylePlan = facts
}

// reportCompositionEntries names the ONE thing the outer message carries that this import writes
// nowhere — the structured fibre breakdown (field 14).
//
// IT IS NOT WRITTEN, AND THAT IS THE DECISION, NOT AN OVERSIGHT. composition_entries projects
// `style_composition`, and that table has exactly one writer in this service —
// product.ReconcileStyleCompositionTx — which throws the whole set away and RE-DERIVES it from the
// card's own shell-fabric BOM lines, resolved against THIS catalogue's articles, on every save of
// the card (UpdateTechCard, UpdateStyle, UpdateColorwayRecipe all end in it). Two consequences
// decide the matter together:
//
//   - what the archive carries is a derivation over the SOURCE's catalogue. Written here it would
//     state, as a fact about this base's BOM, a breakdown this base's BOM does not produce;
//   - and it would not survive being read twice: the imported card's FIRST save replaces it in
//     silence — with an empty set wherever the linked articles carry no fibre composition, which is
//     the ordinary state of this catalogue. Writing therefore trades a loss the report names for one
//     that nothing anywhere names, which is the exact trade the owner's rule forbids.
//
// So the loss is REPORTED instead, and reported HERE rather than in the write: the dry run is where
// an operator sees it before committing, and the condition — «the archive carried entries» — is a
// property of card.json that the transaction has no reason to re-read.
//
// The numbers go into the detail because the report is the only place on this side they exist at
// all, and the list is CAPPED: an archive is somebody else's file and `composition_entries` is
// repeated, so the detail is bounded by construction rather than by the reader's good manners.
func (r *tcimpResolver) reportCompositionEntries() {
	entries := r.card.GetCompositionEntries()
	if len(entries) == 0 {
		return
	}

	const spelled = 12 // more fibres than any garment declares; a hostile archive may carry thousands
	parts := make([]string, 0, spelled)
	for _, e := range entries {
		if len(parts) == spelled {
			parts = append(parts, fmt.Sprintf("… and %d more", len(entries)-spelled))
			break
		}
		code := strings.TrimSpace(e.GetFiberCode())
		if code == "" {
			code = "?"
		}
		if pct := e.GetPercent(); pct != nil && strings.TrimSpace(pct.GetValue()) != "" {
			parts = append(parts, fmt.Sprintf("%s %s%%", code, strings.TrimSpace(pct.GetValue())))
			continue
		}
		parts = append(parts, code)
	}

	r.hole(techcardarchive.EntityCard, "composition_entries", techcardarchive.StatusDegraded,
		techcardarchive.ReasonCompositionNotDerived,
		fmt.Sprintf("the archive states a structured fibre breakdown (%s), which this base derives "+
			"from the card's own fabric lines rather than importing; the card landed with the "+
			"free-text composition and no breakdown until it is saved here",
			strings.Join(parts, ", ")))
}

// resolvePieceAreas carries the measured contour areas across, translating the one id among them.
//
// WHAT TRAVELS VERBATIM AND WHY. scope_key and piece_line_key are STABLE KEYS, not ids — the fabric
// scope (COALESCE(назначение, bom line_key)) and the cut piece's own key — and they are valid on the
// imported card as they stand; the store re-sews them against the just-inserted rows. The measurement's
// conditions (contour_layer, seam_allowance_mm) and its provenance (parsed_by, parsed_at) travel
// unchanged for the reason the entity states: who measured this geometry and when is a fact about the
// MEASUREMENT, and re-stamping it with today's date and this operator's name would claim a measurement
// nobody took. `stale` is not carried at all — it is a verdict the reader recomputes, and an imported
// scope reads stale anyway (the store mints a domain-separated fingerprint for exactly that).
//
// size_id IS an id and is the whole point of this function:
//
//   - 0 / unset means «the piece does not grade and enters every size's set whole» — a documented
//     state, carried through as NULL and never reported;
//   - a value r.sizeMapping cannot place DROPS THAT ONE ROW with a size_unknown line. Dropped, not
//     NULLed: NULL is a different statement («ungraded»), and an "S" contour filed as ungraded would
//     be counted into every size of the run. The rest of the scope's rows import — a missing size
//     costs the sizes it measured and nothing else.
func (r *tcimpResolver) resolvePieceAreas() {
	for _, sc := range r.card.GetPieceAreaScopes() {
		if sc == nil {
			continue
		}

		// parsed_at rides the SCOPE (one transaction wrote it) and lands in a TIMESTAMP NOT NULL
		// column whose range starts one second after the Unix epoch. An archive that carries none
		// falls back to when the archive was written — an upper bound on when the measurement was
		// recorded, which is a fact rather than an invention — and if even that is missing the
		// scope is dropped with a log rather than stamped with a date nobody measured on. Same
		// answer the size chart gives an unreadable cell: the reason dictionary is closed
		// (reasons.go) and a malformed sidecar is not a missing reference, so it is a log, not a code.
		parsedAt := sc.GetParsedAt().AsTime()
		if sc.GetParsedAt() == nil {
			parsedAt = r.a.Manifest.ExportedAt
		}
		if parsedAt.Unix() <= 0 {
			slog.Default().Warn("tech card import: dropped a piece-area scope with no measurement date",
				slog.String("scope_key", sc.GetScopeKey()), slog.Int("areas", len(sc.GetAreas())))
			continue
		}

		seam, _ := tcimpWireDecimal(sc.GetSeamAllowanceMm(), "seam_allowance_mm", sc.GetScopeKey())

		for _, ar := range sc.GetAreas() {
			if ar == nil {
				continue
			}
			ref := sc.GetScopeKey() + "/" + ar.GetPieceLineKey()

			area, ok := tcimpWireDecimal(ar.GetAreaCm2(), "area_cm2", ref)
			if !ok {
				// chk_tcpa_area_positive refuses it at the schema level anyway; caught here so a
				// corrupt row costs one line of the log instead of the transaction.
				slog.Default().Warn("tech card import: dropped a piece area with no readable area",
					slog.String("scope_key", sc.GetScopeKey()), slog.String("piece_line_key", ar.GetPieceLineKey()))
				continue
			}

			row := entity.TechCardArchivePieceArea{
				ScopeKey:        sc.GetScopeKey(),
				PieceLineKey:    ar.GetPieceLineKey(),
				AreaCm2:         area,
				ContourLayer:    sc.GetContourLayer(),
				SeamAllowanceMm: seam,
				Hulled:          ar.GetHulled(),
				AmbiguousPick:   ar.GetAmbiguousPick(),
				ParsedBy:        sc.GetParsedBy(),
				ParsedAt:        parsedAt,
			}
			// An absent perimeter is a LEGAL and permanent state (every measurement before 0305
			// carries none), so it lands unset and says so — the edge-fusing estimate refuses on it
			// rather than deriving a strip width from the area.
			if p, ok := tcimpWireDecimal(ar.GetPerimeterCm(), "perimeter_cm", ref); ok {
				row.PerimeterCm = decimal.NullDecimal{Decimal: p, Valid: true}
			}

			if src := int64(ar.GetSizeId()); src != 0 {
				local, ok := r.sizeMapping[src]
				if !ok {
					r.sizeMissed[src] = true
					r.hole(techcardarchive.EntitySize, r.sizeRef(src), techcardarchive.StatusSkipped,
						techcardarchive.ReasonSizeUnknown,
						"a measured cut-piece area is filed under a size this base's dictionary does not "+
							"have; that area was dropped — an area of an unknown size cannot be read as "+
							"«the piece does not grade» without inflating every other size's cloth norm")
					continue
				}
				row.SizeId = sql.NullInt64{Int64: local, Valid: true}
			}

			r.out.PieceAreaPlan = append(r.out.PieceAreaPlan, row)
		}
	}
}

// tcimpWireDecimal reads one google.type.Decimal off the archive. ok=false means "absent or not a
// number", and the caller decides what that costs — an absent perimeter is legal, an absent area is
// not a row.
//
// Unreadable is LOGGED and then treated as absent rather than given a code of its own: the reason
// dictionary is closed (reasons.go), a malformed number is not a missing reference, and the size
// chart's cells already answer the same question the same way.
func tcimpWireDecimal(d *pb_decimal.Decimal, field, ref string) (decimal.Decimal, bool) {
	raw := strings.TrimSpace(d.GetValue())
	if raw == "" {
		return decimal.Decimal{}, false
	}
	v, err := decimal.NewFromString(raw)
	if err != nil {
		slog.Default().Warn("tech card import: the archive carries an unreadable decimal",
			slog.String("field", field), slog.String("ref", ref), slog.String("value", raw))
		return decimal.Decimal{}, false
	}
	return v, true
}

// ────────────────────────────── shared plumbing ──────────────────────────────

// readSidecar parses one optional JSON file of the archive.
//
// ok=false means the archive does not carry it, which is legal for every entry except manifest.json
// and card.json (§1) and means exactly "the card had none of this". An error means the bytes are
// there and do not hold what they claim — corruption, which fails the whole import and never
// degrades into a hole (§1.2): a sidecar that half-parses would import half a card while reporting
// nothing.
func (r *tcimpResolver) readSidecar(name string, v any) (bool, error) {
	if !r.a.Has(name) {
		return false, nil
	}
	raw, err := r.a.ReadFile(name)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return false, fmt.Errorf("%w: %s does not parse: %v", techcardarchive.ErrCorrupt, name, err)
	}
	return true, nil
}

func tcimpNullString(s string) sql.NullString {
	s = strings.TrimSpace(s)
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func tcimpNullInt32(v int32) sql.NullInt32 {
	return sql.NullInt32{Int32: v, Valid: true}
}
