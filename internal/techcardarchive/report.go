package techcardarchive

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// The import report, assembled here and nowhere else.
//
// A report is TWO things that must not be derived from each other:
//
//   - LINES — what went wrong. One per hole, each carrying a closed reason code and the sentence
//     telling the operator what to do about it.
//   - COUNTERS — how much went through. imported / skipped / degraded per entity, and the imported
//     half CANNOT come from the lines: a success writes no line (only a degradation does), so a
//     report built from lines alone would say "nothing was imported" about a clean card. The other
//     direction is just as broken: one media slot legitimately produces TWO lines — the export's
//     media_object_missing re-reported plus the import's own media_missing (FORMAT.md §5.5) — so
//     counting lines would tally two skipped media where one slot exists. The resolver counts what
//     it touched; BuildReport carries that tally through untouched.
//
// And the reason all of it exists: an EMPTY LIST OF HOLES IS NOT PROOF OF A CLEAN IMPORT. A parser
// that fell over halfway finds no holes either, and its silence reads exactly like success.
// ValidateReportAgainstManifest is the positive control that separates the two — the archive's own
// contents claim against the tally — and it is an error, not a warning, because the failure mode it
// catches is a card that quietly landed missing half of itself.

// Line statuses. Strings, not an enum, for the reason stated over the proto block: an unknown enum
// member is dropped SILENTLY by protojson, and a report that loses rows on the way to the operator
// is worse than one carrying a word an old client cannot translate.
const (
	StatusImported = "imported"
	StatusSkipped  = "skipped"
	StatusDegraded = "degraded"
)

// Entity names. The vocabulary of FORMAT.md §7: the human word for what a line happened to. The
// first eight are also the entities that get a counter — see CountedEntities.
const (
	EntityBOMLine   = "bom_line"
	EntityMedia     = "media"
	EntityPattern   = "pattern"
	EntityMarker    = "marker"
	EntitySize      = "size"
	EntityOperation = "operation"
	EntityAssembly  = "assembly"
	EntityColorway  = "colorway"

	// Entities that appear on LINES but are never tallied: a material is not imported (it is
	// matched against the target catalogue — the BOM line is what lands, and that is what
	// bom_line counts), a measurement is a size-chart AXIS rather than a row of the card, and
	// card / archive are the whole thing, not a row of it.
	//
	// EntityMeasurement is the second named axis of sizechart.json and exists precisely so a
	// measurement problem is NOT reported as "size": an operator told "the size is unknown" about
	// a measurement goes and reads the wrong dictionary. It pairs with ReasonMeasurementUnknown.
	//
	// EntityPieceArea is the MEASURED CONTOUR of one cut piece — a row of tech_card_piece_area, the
	// geometry a cloth norm is derived from. It is not `pattern`, which it was reported as while
	// this word was missing: a pattern is a SHEET, it has its own counter, and a sheet whose
	// measured areas were dropped imported perfectly well. An operator reading «pattern skipped»
	// goes to re-upload a file that is already there.
	EntityMaterial    = "material"
	EntityMeasurement = "measurement"
	EntityPieceArea   = "piece_area"
	EntityCard        = "card"
	EntityArchive     = "archive"
)

// CountedEntities is the fixed set of entities that carry a counter, in the order they appear in
// the report. Every one of them is present in every report even at zero — a missing row and a zero
// row would otherwise be the same thing on the wire, and "we counted none" has to be visibly
// different from "we never looked".
var CountedEntities = []string{
	EntityBOMLine,
	EntityMedia,
	EntityPattern,
	EntityMarker,
	EntitySize,
	EntityOperation,
	EntityAssembly,
	EntityColorway,
}

// ErrParseControl marks every refusal that comes out of the positive control, so the import route
// can answer with "this archive contradicts itself" instead of a generic failure — the file DID
// parse, and its own contents claim is what disagrees with the parse. errors.Is against it; the
// wrapped text names every violation found, not just the first.
var ErrParseControl = errors.New("import report failed its positive control")

// EntityTally is one entity's three numbers as the RESOLVER counted them while it worked.
type EntityTally struct {
	Imported int
	Skipped  int
	Degraded int
}

// Sum is what the positive control looks at: how many rows of this entity the import saw at all,
// whatever became of them. Zero means the parser never reached this entity.
func (t EntityTally) Sum() int { return t.Imported + t.Skipped + t.Degraded }

// Counters is the resolver's tally, keyed by entity. Build it with NewCounters — the Add* methods
// write back into the map and a nil map panics on write, and more importantly a hand-made literal
// would be missing the zero rows that make an untouched entity visible.
type Counters map[string]EntityTally

// NewCounters returns a tally with every counted entity present at zero.
func NewCounters() Counters {
	c := make(Counters, len(CountedEntities))
	for _, e := range CountedEntities {
		c[e] = EntityTally{}
	}
	return c
}

// AddImported / AddSkipped / AddDegraded count n rows of entity. An entity outside CountedEntities
// is accepted and reported — dropping a number the resolver bothered to produce would be the same
// silence this file exists to prevent.
func (c Counters) AddImported(entity string, n int) { c.add(entity, n, 0, 0) }

// AddSkipped counts n rows of entity that did not land at all.
func (c Counters) AddSkipped(entity string, n int) { c.add(entity, 0, n, 0) }

// AddDegraded counts n rows of entity that landed with something missing.
func (c Counters) AddDegraded(entity string, n int) { c.add(entity, 0, 0, n) }

func (c Counters) add(entity string, imported, skipped, degraded int) {
	t := c[entity]
	t.Imported += imported
	t.Skipped += skipped
	t.Degraded += degraded
	c[entity] = t
}

// ImportHole is one thing the IMPORT could not do cleanly — the mirror of ExportHole, with the one
// field the export side has no use for: what became of the row.
//
// Status is the import's own verdict, because only the import knows it: material_not_found leaves
// a BOM line standing without its article (degraded) on one card and drops a recipe pin entirely
// (skipped) on another. Left empty, it falls back to DefaultStatusFor(Reason).
type ImportHole struct {
	Entity string
	Ref    string
	Status string
	Reason Reason
	Detail string
}

// ReportInput is everything BuildReport needs, as a struct rather than a parameter list, because
// the two data sources have to stay visibly separate: Holes are what the resolver could not do,
// Counters are what it did. A future field is added here without re-typing every call site.
type ReportInput struct {
	// ImportID is the ULID of the uploaded archive; StyleNumber and Stage are FINAL — as the card
	// actually landed in THIS base, which is not necessarily what the archive asked for.
	ImportID    string
	StyleNumber string
	Stage       string

	// Counters is the resolver's tally. Nil is accepted and produces the all-zero set, which the
	// positive control then refuses against any non-empty archive.
	Counters Counters

	// Holes are the import's own. ExportHoles come from manifest.export_holes and are re-reported
	// verbatim in the SAME list on purpose (FORMAT.md §2): the operator sees where the data was
	// already thin before it travelled, in the same place they read the rest.
	Holes       []ImportHole
	ExportHoles []ExportHole
}

// BuildReport turns holes and a tally into the report that stays on the card forever.
//
// It formats nothing for a screen: no grouping, no headings, no counts folded into sentences. The
// client renders. What it does guarantee is order (export holes first, then the import's own, each
// in the order it was given) and completeness (every counted entity has a row).
func BuildReport(in ReportInput) *pb_admin.TechCardImportReport {
	rep := &pb_admin.TechCardImportReport{
		StyleNumber: in.StyleNumber,
		Stage:       in.Stage,
		ImportId:    in.ImportID,
	}

	lines := make([]*pb_admin.TechCardImportReportLine, 0, len(in.ExportHoles)+len(in.Holes))
	for _, h := range in.ExportHoles {
		// An export hole has no status of its own — the export writes what it could not carry, and
		// what that costs on arrival follows from the code alone.
		lines = append(lines, reportLine(h.Entity, h.Ref, DefaultStatusFor(h.Reason), h.Reason, h.Detail))
	}
	for _, h := range in.Holes {
		status := h.Status
		if status == "" {
			status = DefaultStatusFor(h.Reason)
		}
		lines = append(lines, reportLine(h.Entity, h.Ref, status, h.Reason, h.Detail))
	}
	if len(lines) > 0 {
		rep.Lines = lines
	}

	rep.Counters = buildCounters(in.Counters)
	return rep
}

func reportLine(entity, ref, status string, reason Reason, detail string) *pb_admin.TechCardImportReportLine {
	return &pb_admin.TechCardImportReportLine{
		Entity: entity,
		Ref:    ref,
		Status: status,
		Reason: string(reason),
		// CLIPPED HERE because this is the ONE funnel both kinds of hole pass through, and one of
		// the two kinds is foreign prose: ExportHoles come out of manifest.json, which some other
		// instance wrote, and their detail is copied into the stored report and into the RPC answer
		// verbatim. A producer that bounds its own text is doing it twice; a producer that forgets
		// is bounded anyway, which is the property worth having.
		Detail: ClipDetail(detail, DetailLimit),
		Action: ActionFor(reason),
	}
}

// DetailLimit is the ceiling on ONE hole's free-text detail, in bytes.
//
// A couple of kilobytes: enough for the longest sentence any producer here writes plus the row it
// names, and small enough that a report full of holes stays a report. The number that makes it
// necessary is the other one — card.json may be 16 MiB, every repeated field in it is somebody
// else's file, and a detail built out of one of those travels into the `report` JSON column and
// into every read of the card afterwards. Counting ENTRIES bounds nothing when one entry can be
// megabytes.
const DetailLimit = 2048

// detailClipMark is what a clipped detail ends with. It has to be visible: a sentence cut without
// saying so reads as a complete one, and the operator acts on a list they think they have all of.
const detailClipMark = "… [clipped]"

// ClipDetail bounds s to limit BYTES, cutting on a rune boundary and saying that it cut.
//
// Bytes, not runes, because bytes are what the column and the wire hold — a rune limit over
// Cyrillic prose (which every detail in this package may be) bounds the string at twice the number
// it looks like. The cut walks back to a rune start, so the result never ends in half a character:
// half a rune is invalid UTF-8, which protojson refuses to marshal — the guard would abort the very
// answer it exists to keep small.
//
// A limit too small to hold the mark is a programming error and is answered with the mark alone,
// because the alternative — a silent cut — is the thing this function exists to prevent.
func ClipDetail(s string, limit int) string {
	if limit <= 0 || len(s) <= limit {
		return s
	}
	keep := limit - len(detailClipMark)
	if keep <= 0 {
		return detailClipMark
	}
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + detailClipMark
}

// buildCounters emits the eight counted entities in their fixed order, then anything else the
// resolver counted, sorted — an entity nobody planned for still reaches the operator.
func buildCounters(c Counters) []*pb_admin.TechCardImportCounter {
	out := make([]*pb_admin.TechCardImportCounter, 0, len(CountedEntities)+len(c))
	seen := make(map[string]bool, len(CountedEntities))
	for _, e := range CountedEntities {
		seen[e] = true
		out = append(out, counterOf(e, c[e]))
	}

	extra := make([]string, 0, len(c))
	for e := range c {
		if !seen[e] {
			extra = append(extra, e)
		}
	}
	sort.Strings(extra)
	for _, e := range extra {
		out = append(out, counterOf(e, c[e]))
	}
	return out
}

func counterOf(entity string, t EntityTally) *pb_admin.TechCardImportCounter {
	return &pb_admin.TechCardImportCounter{
		Entity:   entity,
		Imported: clampCount(t.Imported),
		Skipped:  clampCount(t.Skipped),
		Degraded: clampCount(t.Degraded),
	}
}

// clampCount saturates instead of wrapping. A wrapped count could turn a real number NEGATIVE and
// a negative one is caught by the control below; a saturated one stays absurd but positive, which
// is the honest shape of "more than we can say".
func clampCount(n int) int32 {
	switch {
	case n < math.MinInt32:
		return math.MinInt32
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

// ValidateReportAgainstManifest is the guard against the FALSE GREEN: a report with no holes in it
// is indistinguishable from a parser that produced nothing, and the second one has to fail.
//
// The archive states what it contains (manifest.contents, FORMAT.md §2). If it claims fourteen
// media and the report tallies zero media in every column — not imported, not skipped, not
// degraded — then nothing ever read media/index.json, and the card on screen is missing pictures
// nobody was told about. Same for patterns, markers and the material passports, which exist only
// because BOM lines reference them (§5.4), so a passport count with no bom_line row counted means
// the BOM was never parsed.
//
// It deliberately does NOT check the opposite direction (tally larger than the claim): contents
// counts FILES in the archive while a counter counts ROWS the import placed, and the two are not
// the same population — a media file used by three slots is one file. Refusing on that would fail
// healthy imports.
//
// Every violation found is named in one error, so a broken parse is diagnosed once rather than one
// entity per re-run.
func ValidateReportAgainstManifest(report *pb_admin.TechCardImportReport, manifest *Manifest) error {
	if report == nil {
		return fmt.Errorf("%w: no report was built at all", ErrParseControl)
	}
	if manifest == nil {
		return fmt.Errorf("%w: no manifest to check the report against — the archive's own claim "+
			"about its contents is the only thing that can tell a clean import from a dead parser",
			ErrParseControl)
	}

	var problems []string

	tally := make(map[string]EntityTally, len(report.GetCounters()))
	for _, c := range report.GetCounters() {
		if c == nil {
			problems = append(problems, "the report carries a nil counter row")
			continue
		}
		if _, dup := tally[c.GetEntity()]; dup {
			problems = append(problems, fmt.Sprintf("entity %q is counted twice — two answers to "+
				"how much of it arrived", c.GetEntity()))
			continue
		}
		if c.GetImported() < 0 || c.GetSkipped() < 0 || c.GetDegraded() < 0 {
			problems = append(problems, fmt.Sprintf("entity %q has a negative count "+
				"(imported=%d skipped=%d degraded=%d)", c.GetEntity(), c.GetImported(), c.GetSkipped(), c.GetDegraded()))
		}
		tally[c.GetEntity()] = EntityTally{
			Imported: int(c.GetImported()),
			Skipped:  int(c.GetSkipped()),
			Degraded: int(c.GetDegraded()),
		}
	}

	for _, e := range CountedEntities {
		if _, ok := tally[e]; !ok {
			problems = append(problems, fmt.Sprintf("no counter for %q at all — a report that "+
				"BuildReport assembled always carries one, so this one was assembled elsewhere", e))
		}
	}

	// The claims, in the order the operator would read them.
	claims := []struct {
		count  int
		entity string
		what   string
	}{
		{manifest.Contents.Media, EntityMedia, "media files"},
		{manifest.Contents.Patterns, EntityPattern, "pattern sheets"},
		{manifest.Contents.Markers, EntityMarker, "markers"},
		{manifest.Contents.Materials, EntityBOMLine, "material passports"},
	}
	for _, c := range claims {
		if c.count <= 0 {
			continue
		}
		if tally[c.entity].Sum() > 0 {
			continue
		}
		problems = append(problems, fmt.Sprintf("the archive says it carries %d %s, and the report "+
			"counted no %s rows at all (imported=0 skipped=0 degraded=0)", c.count, c.what, c.entity))
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s. An empty list of holes is not proof of a clean import — this looks "+
		"like a parse that stopped halfway, so the import is refused instead of reported as clean",
		ErrParseControl, strings.Join(problems, "; "))
}

// reasonGuidance is what the report says about one reason code: the status a hole lands in when
// its reporter did not say, and the sentence the operator is given.
//
// The action text is the fourth thing a new reason code needs, next to the constant, its line of
// explanation in reasons.go and its row in FORMAT.md §7 — TestReportActionTextCoversEveryReason
// reads reasons.go and fails if the four ever drift apart.
type reasonGuidance struct {
	status string
	action string
}

// reasonGuide covers reasons.go exactly — no more (a code that no longer exists), no less (a code
// nobody told the operator how to close).
//
// The two media codes are the reason the dictionary keeps them apart. media_object_missing means
// the bytes never left the SOURCE: nothing done on this instance closes it, and telling the
// operator to "retry the import" would send them in circles forever. media_upload_failed means the
// bytes DID travel and this instance's storage refused them, which a second import may well fix.
// The report line has no field saying which side a hole came from, so the code is the only thing
// carrying that difference — and it carries it here.
var reasonGuide = map[Reason]reasonGuidance{
	ReasonMaterialNotFound: {StatusDegraded,
		"Create the article in the material catalogue and link the BOM line by hand. The line itself " +
			"imported and kept its name, supplier and unit — only the link to the catalogue is missing."},
	ReasonMaterialAmbiguous: {StatusDegraded,
		"Several live articles carry this code, so none was picked. Archive the duplicates or link " +
			"the BOM line to the right article by hand."},
	ReasonMaterialUnitMismatch: {StatusDegraded,
		"An article with this code exists here but is kept in a different unit, so it is not the same " +
			"article. Fix the unit on one side, then link the BOM line by hand."},

	ReasonMediaMissing: {StatusSkipped,
		"The archive carries no file for this slot. Attach the picture by hand, or export the source " +
			"card again and import the new archive."},
	ReasonMediaObjectMissing: {StatusSkipped,
		"The picture never left the source — its bytes were not in the archive, so there is nothing " +
			"here to retry. Fix the media on the SOURCE card, export again, and import that archive."},
	ReasonMediaUploadFailed: {StatusSkipped,
		"The picture travelled but this instance's storage refused it, so the slot was left empty. " +
			"Import the same archive again; if it keeps failing, the bucket needs looking at."},
	ReasonMediaVanished: {StatusSkipped,
		"This picture was already stored here, so nothing was uploaded — and that stored copy was " +
			"deleted while the import was running, usually by another import being rolled back. " +
			"Nothing is wrong with the archive or with the storage: import the same archive again " +
			"and the file will be uploaded afresh."},

	ReasonPatternInvalid: {StatusSkipped,
		"The pattern file could not be read as a DXF or a PDF. Upload the sheet by hand on the " +
			"patterns tab."},

	ReasonSizeUnknown: {StatusSkipped,
		"This size is not in the size dictionary here, so rows filed under it were dropped. Add the " +
			"size to the dictionary and import the archive again."},
	ReasonSizeNotInCardRange: {StatusSkipped,
		"The size dictionary is fine — the imported card's own size range does not include this size, " +
			"so rows filed under it were dropped. Widen the card's size range and re-enter those rows; " +
			"a size_unknown line naming the same size means the range lost it on the way in."},
	ReasonMeasurementUnknown: {StatusSkipped,
		"This measurement is not in the measurement dictionary here, so its row was dropped from the " +
			"size chart. Add the measurement, then re-enter the row by hand or import again."},
	ReasonWorkTokenUnknown: {StatusDegraded,
		"The operation's work is not in the work catalogue here. Pick the work on the operation by " +
			"hand, or add the missing work to the catalogue and import again."},
	ReasonCategoryUnknown: {StatusDegraded,
		"The archive's category path has no match here, so the card landed without a category. Set " +
			"the category on the card by hand."},
	ReasonAssemblyComponentNotFound: {StatusSkipped,
		"The component style is not in this base. Import or create that style, then link it on the " +
			"assembly tab."},

	ReasonColorwaysNotApplied: {StatusSkipped,
		"Colourways are products and an import creates none. Press «create colourways from archive» " +
			"on this report to build them as drafts with their recipes, or create them by hand — the " +
			"archive's colour list is in the report for reference."},
	ReasonColorwayExists: {StatusDegraded,
		"This card already has a colourway of that colour, so nothing was created and its recipe was " +
			"left alone. Nothing to do unless you WANT the archive's recipe on it — in that case " +
			"compare the two by hand; a button that overwrote a colourway somebody is working on " +
			"would be the worse of the two mistakes."},
	ReasonColorwayNotCreated: {StatusSkipped,
		"The colourway could not be created here, usually because its colour code is not in this " +
			"base's colour dictionary. The detail names the refusal: add the colour (or fix what it " +
			"names) and press «create colourways from archive» again — colours already created are " +
			"skipped, so it is safe to press twice."},
	ReasonColorwayPinLost: {StatusDegraded,
		"The recipe row landed with its consumption and its placement, and with no article pinned — " +
			"so it takes the BOM line's own article. A pin is described by the archive's material " +
			"passports, which do not outlive the uploaded file; pin the article by hand on the " +
			"colourway's recipe if this colour takes a different one."},
	ReasonCompositionNotDerived: {StatusDegraded,
		"The fibre breakdown is derived here from the articles the card's fabric lines link to, and is " +
			"re-derived every time the card is saved — so the archive's own rows were not written. The " +
			"free-text composition did travel and is on the card. Save the card to derive the breakdown; " +
			"if it comes out empty, the linked articles carry no fibre composition in this catalogue."},
	ReasonWastageClaimDegraded: {StatusDegraded,
		"The figure stands, but this base's own measured lays do not confirm it as their median, so " +
			"it now reads as entered by hand. Open the fabric tab and apply this base's suggestion " +
			"if you want the badge back — the number itself is unchanged either way."},
	ReasonNormMarkerLost: {StatusDegraded,
		"The norm stands, the marker stamp behind it does not. Re-run the marker on this card if you " +
			"need the geometry the norm came from."},

	ReasonStyleNumberTaken: {StatusDegraded,
		"The style number from the archive is already in use here, so the card landed under a " +
			"different one. Rename the card if that is not what you want."},
	ReasonUnknownEntry: {StatusSkipped,
		"This server does not know this file — the archive was written by a newer version. Update the " +
			"server and import again if the missing piece matters."},
	ReasonArchiveRowInvalid: {StatusSkipped,
		"The row was already unusable in the archive — it names nothing, or carries a value that is " +
			"not one — so it was dropped and the rest imported. Nothing here can repair the row itself: " +
			"re-enter it on this card (measured areas are recounted on the patterns tab), or fix it on " +
			"the source card and import that archive again."},
	ReasonCardNotImportable: {StatusSkipped,
		"An import will REFUSE this archive whole: the card breaks a rule the write path enforces, so " +
			"it can be read and inspected but not restored anywhere. The detail names the field. Open " +
			"the source card, fix that field, save it — the save runs the same check — and export again."},
}

// ActionFor is the sentence the operator is shown for a reason code, and it is empty for a code
// this server does not know. Empty rather than invented: the client shows an untranslated code as
// it is and must not be handed a made-up instruction next to it.
func ActionFor(r Reason) string { return reasonGuide[r].action }

// DefaultStatusFor is the status a hole with this reason lands in when its reporter did not say —
// which is every re-reported export hole, since the export side has no status field at all.
// Unknown codes default to skipped: of the two possible lies, "the row is not there" sends the
// operator to look, and "it is there but thinner" does not.
func DefaultStatusFor(r Reason) string {
	if g, ok := reasonGuide[r]; ok {
		return g.status
	}
	return StatusSkipped
}
