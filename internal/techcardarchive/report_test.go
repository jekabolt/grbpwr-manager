package techcardarchive

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// ─────────────────────────────────────────────────────────────────────────────
// TestBuildReport                     — lines, order, action text, two sources.
// TestReportPositiveControl           — the guard against "empty report = clean import".
// TestReportActionTextCoversEveryReason — every code in reasons.go has a way out.
// ─────────────────────────────────────────────────────────────────────────────

const reasonsSourcePath = "reasons.go"
const formatDocPath = "FORMAT.md"

// reportCounter pulls one entity's counter row out of a built report, and fails loudly when the
// row is absent — every counted entity is supposed to be there at zero, so "absent" is a defect
// and must not read as an empty tally.
func reportCounter(t *testing.T, rep *pb_admin.TechCardImportReport, entity string) *pb_admin.TechCardImportCounter {
	t.Helper()
	for _, c := range rep.GetCounters() {
		if c.GetEntity() == entity {
			return c
		}
	}
	t.Fatalf("report carries no counter for %q; it has %v", entity, reportCounterEntities(rep))
	return nil
}

func reportCounterEntities(rep *pb_admin.TechCardImportReport) []string {
	out := make([]string, 0, len(rep.GetCounters()))
	for _, c := range rep.GetCounters() {
		out = append(out, c.GetEntity())
	}
	return out
}

// reportManifestClaiming is a manifest that says the archive holds this much and nothing else
// about it — contents is the only half the positive control reads.
func reportManifestClaiming(media, patterns, markers, materials int) *Manifest {
	return &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		MoneyPolicy:   MoneyPolicyStrippedV1,
		Contents: Contents{
			Media:     media,
			Patterns:  patterns,
			Markers:   markers,
			Materials: materials,
		},
	}
}

func TestBuildReport(t *testing.T) {
	t.Run("lines keep their order and carry the action text of their code", func(t *testing.T) {
		rep := BuildReport(ReportInput{
			ImportID:    "01J8ZC6T4KQ2RS0B77",
			StyleNumber: "GRB-SS26-014-1",
			Stage:       "development",
			Counters:    NewCounters(),
			ExportHoles: []ExportHole{{
				Entity: EntityMedia,
				Ref:    "media_id=4021",
				Reason: ReasonMediaObjectMissing,
				Detail: "full_size object 2026/08/1a2b.jpg: 404 from bucket",
			}},
			Holes: []ImportHole{
				{
					Entity: EntityMedia,
					Ref:    "media_id=4021",
					Status: StatusSkipped,
					Reason: ReasonMediaMissing,
					Detail: "no file in the archive for this slot",
				},
				{
					Entity: EntityMaterial,
					Ref:    "bom_line_key=01J8ZC4Q0FQ8M6R0K2",
					Status: StatusDegraded,
					Reason: ReasonMaterialNotFound,
					Detail: "code F-WOOL-320 matches nothing live",
				},
			},
		})

		if rep.GetStyleNumber() != "GRB-SS26-014-1" || rep.GetStage() != "development" ||
			rep.GetImportId() != "01J8ZC6T4KQ2RS0B77" {
			t.Fatalf("the card's own identity did not survive the build: style=%q stage=%q import=%q",
				rep.GetStyleNumber(), rep.GetStage(), rep.GetImportId())
		}

		if len(rep.GetLines()) != 3 {
			t.Fatalf("want 3 lines (one export hole re-reported + two of the import's own), got %d",
				len(rep.GetLines()))
		}

		// The export hole comes first: it happened first, and the operator reads the history in
		// order.
		first := rep.GetLines()[0]
		if first.GetReason() != string(ReasonMediaObjectMissing) {
			t.Fatalf("line 0 should be the re-reported export hole, got reason %q", first.GetReason())
		}
		if first.GetStatus() != StatusSkipped {
			t.Fatalf("an export hole with no status of its own should take the code's default %q, got %q",
				StatusSkipped, first.GetStatus())
		}
		if first.GetDetail() != "full_size object 2026/08/1a2b.jpg: 404 from bucket" {
			t.Fatalf("the export hole's free text was rewritten: %q", first.GetDetail())
		}

		// The two media codes must not give the same instruction: one is fixed on the source, the
		// other by importing again. That difference is the only thing the codes carry, since a
		// line has no field saying which side the hole came from.
		objectMissing := first.GetAction()
		uploadFailed := ActionFor(ReasonMediaUploadFailed)
		if objectMissing == "" || uploadFailed == "" {
			t.Fatalf("both media codes need an action text: object_missing=%q upload_failed=%q",
				objectMissing, uploadFailed)
		}
		if objectMissing == uploadFailed {
			t.Fatalf("media_object_missing and media_upload_failed carry the SAME instruction %q — "+
				"they are two codes precisely because the operator has to do two different things",
				objectMissing)
		}
		if !strings.Contains(strings.ToLower(objectMissing), "source") {
			t.Fatalf("media_object_missing must send the operator to the SOURCE card (the bytes never "+
				"travelled, nothing here can be retried); got %q", objectMissing)
		}

		for i, l := range rep.GetLines() {
			if l.GetAction() == "" {
				t.Fatalf("line %d (reason %q) carries no action — a hole the operator is not told "+
					"how to close is a hole nobody closes", i, l.GetReason())
			}
		}

		last := rep.GetLines()[2]
		if last.GetEntity() != EntityMaterial || last.GetStatus() != StatusDegraded ||
			last.GetRef() != "bom_line_key=01J8ZC4Q0FQ8M6R0K2" {
			t.Fatalf("the import's own hole came out wrong: %+v", last)
		}
	})

	t.Run("every counted entity is present even when nothing happened to it", func(t *testing.T) {
		rep := BuildReport(ReportInput{Counters: NewCounters()})

		if len(rep.GetLines()) != 0 {
			t.Fatalf("no holes were given, so no lines should be built; got %d", len(rep.GetLines()))
		}
		got := reportCounterEntities(rep)
		if len(got) != len(CountedEntities) {
			t.Fatalf("want a counter per counted entity %v, got %v", CountedEntities, got)
		}
		for i, want := range CountedEntities {
			if got[i] != want {
				t.Fatalf("counter order is not the fixed one: want %v, got %v", CountedEntities, got)
			}
		}
	})

	t.Run("a nil tally still produces the full zero set", func(t *testing.T) {
		rep := BuildReport(ReportInput{})
		for _, e := range CountedEntities {
			c := reportCounter(t, rep, e)
			if c.GetImported() != 0 || c.GetSkipped() != 0 || c.GetDegraded() != 0 {
				t.Fatalf("entity %q counted something out of a nil tally: %+v", e, c)
			}
		}
	})

	t.Run("an entity nobody planned for is still reported", func(t *testing.T) {
		c := NewCounters()
		c.AddImported("piece", 4)
		rep := BuildReport(ReportInput{Counters: c})

		extra := reportCounter(t, rep, "piece")
		if extra.GetImported() != 4 {
			t.Fatalf("an unplanned entity lost its count: %+v", extra)
		}
		if got := reportCounterEntities(rep); len(got) != len(CountedEntities)+1 ||
			got[len(got)-1] != "piece" {
			t.Fatalf("the unplanned entity should come after the fixed set, got %v", got)
		}
	})

	t.Run("the tally comes from the resolver, not from the lines", func(t *testing.T) {
		// Two things are proven here at once, and they are the two halves of the trap:
		//
		//  1. A clean import writes NO lines — a success is not a hole — so a tally derived from
		//     lines would report "nothing was imported" about a perfect card.
		//  2. ONE media slot legitimately produces TWO lines (the export's media_object_missing
		//     re-reported, plus the import's own media_missing), so a tally counted off the lines
		//     would say two media were skipped where one slot exists.
		c := NewCounters()
		c.AddImported(EntityBOMLine, 11)
		c.AddImported(EntityOperation, 26)
		c.AddSkipped(EntityMedia, 1)

		rep := BuildReport(ReportInput{
			Counters: c,
			ExportHoles: []ExportHole{{
				Entity: EntityMedia, Ref: "media_id=4021", Reason: ReasonMediaObjectMissing,
			}},
			Holes: []ImportHole{{
				Entity: EntityMedia, Ref: "media_id=4021", Status: StatusSkipped, Reason: ReasonMediaMissing,
			}},
		})

		if got := reportCounter(t, rep, EntityBOMLine).GetImported(); got != 11 {
			t.Fatalf("11 BOM lines imported without writing a single line of report; counter says %d — "+
				"the imported half MUST come from the resolver, successes are never lines", got)
		}
		if got := reportCounter(t, rep, EntityOperation).GetImported(); got != 26 {
			t.Fatalf("operations imported=%d, want 26", got)
		}
		if len(rep.GetLines()) != 2 {
			t.Fatalf("want the slot reported twice (once per side), got %d lines", len(rep.GetLines()))
		}
		if got := reportCounter(t, rep, EntityMedia).GetSkipped(); got != 1 {
			t.Fatalf("two lines about ONE slot inflated the tally to skipped=%d; the resolver counted "+
				"1 and the report must carry that number, not the line count", got)
		}
	})

	t.Run("the import's own status wins over the code's default", func(t *testing.T) {
		// work_token_unknown defaults to degraded — the operation lands without its work. A
		// resolver that dropped the whole operation says so, and the report must not overrule it.
		if def := DefaultStatusFor(ReasonWorkTokenUnknown); def != StatusDegraded {
			t.Fatalf("precondition: work_token_unknown should default to %q, got %q", StatusDegraded, def)
		}
		rep := BuildReport(ReportInput{
			Counters: NewCounters(),
			Holes: []ImportHole{{
				Entity: EntityOperation, Ref: "operation_line_key=01J8", Status: StatusSkipped,
				Reason: ReasonWorkTokenUnknown,
			}},
		})
		if got := rep.GetLines()[0].GetStatus(); got != StatusSkipped {
			t.Fatalf("the resolver said %q and the report says %q", StatusSkipped, got)
		}
	})

	t.Run("a code this server does not know is carried, not dressed up", func(t *testing.T) {
		rep := BuildReport(ReportInput{
			Counters: NewCounters(),
			Holes:    []ImportHole{{Entity: EntityCard, Ref: "card", Reason: Reason("invented_here")}},
		})
		line := rep.GetLines()[0]
		if line.GetReason() != "invented_here" {
			t.Fatalf("the unknown code was rewritten to %q", line.GetReason())
		}
		if line.GetAction() != "" {
			t.Fatalf("an unknown code must get NO action text — a made-up instruction next to a code "+
				"nobody can translate is worse than silence; got %q", line.GetAction())
		}
		if line.GetStatus() != StatusSkipped {
			t.Fatalf("an unknown code should fall back to %q (send the operator to look), got %q",
				StatusSkipped, line.GetStatus())
		}
	})
}

func TestReportPositiveControl(t *testing.T) {
	t.Run("a non-empty archive with an all-zero tally is REFUSED", func(t *testing.T) {
		// This is the whole point of the file. The parse died after manifest.json: no holes were
		// found, so the report is spotless — fourteen media, six sheets, three markers and eleven
		// passports simply never happened. Spotless is exactly what a dead parser looks like.
		rep := BuildReport(ReportInput{Counters: NewCounters()})
		if len(rep.GetLines()) != 0 {
			t.Fatalf("precondition: this report is supposed to look perfectly clean, it has %d lines",
				len(rep.GetLines()))
		}

		err := ValidateReportAgainstManifest(rep, reportManifestClaiming(14, 6, 3, 11))
		if err == nil {
			t.Fatal("an archive claiming 14 media / 6 patterns / 3 markers / 11 passports was parsed " +
				"into a report that counted NOTHING, and the control let it through. An empty list " +
				"of holes is not proof of a clean import.")
		}
		if !errors.Is(err, ErrParseControl) {
			t.Fatalf("the refusal must be recognisable as the parse control (errors.Is), got %v", err)
		}
		for _, want := range []string{EntityMedia, EntityPattern, EntityMarker, EntityBOMLine} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("every violated claim has to be named at once so the parse is diagnosed in "+
					"one run; %q is missing from: %v", want, err)
			}
		}
		for _, want := range []string{"14", "6", "3", "11"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("the claim %s is missing from the refusal: %v", want, err)
			}
		}
	})

	t.Run("one entity dead is enough to refuse", func(t *testing.T) {
		// Media, patterns and passports all landed; markers alone produced nothing. Three quarters
		// of a card is not a card.
		c := NewCounters()
		c.AddImported(EntityMedia, 14)
		c.AddImported(EntityPattern, 6)
		c.AddImported(EntityBOMLine, 11)

		err := ValidateReportAgainstManifest(
			BuildReport(ReportInput{Counters: c}), reportManifestClaiming(14, 6, 3, 11))
		if err == nil {
			t.Fatal("3 markers were claimed and none was counted; that has to be a refusal")
		}
		if !strings.Contains(err.Error(), EntityMarker) {
			t.Fatalf("the refusal does not name the dead entity: %v", err)
		}
		if strings.Contains(err.Error(), EntityPattern) {
			t.Fatalf("patterns were fine and must not be blamed: %v", err)
		}
	})

	t.Run("a claim answered by skipped rows alone passes", func(t *testing.T) {
		// Nothing landed — every media file was refused by the bucket — but the import SAW them
		// all and said so. That is a bad import, not a broken parser, and the report is the truth
		// about it: the control must not turn honest failure into a refusal to import at all.
		c := NewCounters()
		c.AddSkipped(EntityMedia, 14)
		c.AddDegraded(EntityBOMLine, 11)
		c.AddImported(EntityPattern, 6)
		c.AddImported(EntityMarker, 3)

		if err := ValidateReportAgainstManifest(
			BuildReport(ReportInput{Counters: c}), reportManifestClaiming(14, 6, 3, 11)); err != nil {
			t.Fatalf("rows that were seen and skipped are still rows that were seen: %v", err)
		}
	})

	t.Run("an archive that claims nothing passes with an empty tally", func(t *testing.T) {
		// An idea-stage card with no media, no sheets, no markers and no BOM is a legal archive.
		// The control has nothing to compare against and must stay silent.
		if err := ValidateReportAgainstManifest(
			BuildReport(ReportInput{Counters: NewCounters()}), reportManifestClaiming(0, 0, 0, 0)); err != nil {
			t.Fatalf("an empty archive parsed into an empty report is not a defect: %v", err)
		}
	})

	t.Run("a tally larger than the claim is NOT refused", func(t *testing.T) {
		// contents counts FILES, a counter counts ROWS the import placed, and one file can serve
		// three slots. Refusing on that would fail healthy imports.
		c := NewCounters()
		c.AddImported(EntityMedia, 31)
		if err := ValidateReportAgainstManifest(
			BuildReport(ReportInput{Counters: c}), reportManifestClaiming(14, 0, 0, 0)); err != nil {
			t.Fatalf("more rows than files is normal (one file, many slots): %v", err)
		}
	})

	t.Run("a report assembled somewhere else is refused", func(t *testing.T) {
		hand := &pb_admin.TechCardImportReport{
			Counters: []*pb_admin.TechCardImportCounter{{Entity: EntityMedia, Imported: 14}},
		}
		err := ValidateReportAgainstManifest(hand, reportManifestClaiming(14, 0, 0, 0))
		if err == nil {
			t.Fatal("a report missing seven of the eight counters cannot say anything about them, " +
				"and silence about an entity is the failure this control exists for")
		}
		for _, want := range []string{EntityPattern, EntityMarker, EntityBOMLine, EntitySize,
			EntityOperation, EntityAssembly, EntityColorway} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("missing counter %q is not named in: %v", want, err)
			}
		}
	})

	t.Run("two answers about one entity are refused", func(t *testing.T) {
		rep := BuildReport(ReportInput{Counters: NewCounters()})
		rep.Counters = append(rep.Counters, &pb_admin.TechCardImportCounter{
			Entity: EntityMedia, Imported: 14,
		})
		err := ValidateReportAgainstManifest(rep, reportManifestClaiming(14, 0, 0, 0))
		if err == nil {
			t.Fatal("media is counted twice (0 and 14) and the control accepted both")
		}
		if !strings.Contains(err.Error(), "twice") {
			t.Fatalf("the refusal should say what is wrong: %v", err)
		}
	})

	t.Run("a negative count is refused", func(t *testing.T) {
		rep := BuildReport(ReportInput{Counters: NewCounters()})
		for _, c := range rep.GetCounters() {
			if c.GetEntity() == EntityMedia {
				c.Skipped = -1
			}
		}
		err := ValidateReportAgainstManifest(rep, reportManifestClaiming(0, 0, 0, 0))
		if err == nil {
			t.Fatal("a negative tally is arithmetic that did not happen; it must not pass")
		}
		if !strings.Contains(err.Error(), "negative") {
			t.Fatalf("the refusal should say what is wrong: %v", err)
		}
	})

	t.Run("nothing to check is itself a refusal", func(t *testing.T) {
		if err := ValidateReportAgainstManifest(nil, reportManifestClaiming(14, 0, 0, 0)); err == nil {
			t.Fatal("no report at all must not read as a clean one")
		}
		rep := BuildReport(ReportInput{Counters: NewCounters()})
		if err := ValidateReportAgainstManifest(rep, nil); err == nil {
			t.Fatal("without the manifest there is no claim to check against, so there is no proof " +
				"the parse ran; that has to fail rather than pass by default")
		}
	})

	t.Run("a real import passes cleanly", func(t *testing.T) {
		// The positive control needs its own positive control: if this subtest cannot go green,
		// every refusal above is satisfied by a Validate that always returns an error.
		c := NewCounters()
		c.AddImported(EntityBOMLine, 10)
		c.AddDegraded(EntityBOMLine, 1)
		c.AddImported(EntityMedia, 13)
		c.AddSkipped(EntityMedia, 1)
		c.AddImported(EntityPattern, 6)
		c.AddImported(EntityMarker, 3)
		c.AddImported(EntitySize, 5)
		c.AddImported(EntityOperation, 26)
		c.AddSkipped(EntityColorway, 2)

		rep := BuildReport(ReportInput{
			ImportID: "01J8ZC6T4KQ2RS0B77",
			Counters: c,
			Holes: []ImportHole{
				{Entity: EntityMedia, Ref: "media_id=4021", Status: StatusSkipped, Reason: ReasonMediaMissing},
				{Entity: EntityMaterial, Ref: "bom_line_key=01J8", Status: StatusDegraded, Reason: ReasonMaterialNotFound},
				{Entity: EntityColorway, Ref: "color_code=BLK", Status: StatusSkipped, Reason: ReasonColorwaysNotApplied},
			},
		})
		if err := ValidateReportAgainstManifest(rep, reportManifestClaiming(14, 6, 3, 11)); err != nil {
			t.Fatalf("a normal import with three holes was refused: %v", err)
		}
	})
}

// TestReportActionTextCoversEveryReason keeps four things in step: the constant in reasons.go, its
// row in FORMAT.md §7, its action text here, and the status a re-reported hole lands in. The
// dictionary is CLOSED, so "every code" is a set that can be read off the source — and it is read
// off the source rather than from a copied list, because a copied list is the thing that rots.
func TestReportActionTextCoversEveryReason(t *testing.T) {
	source := reportParseReasonCodes(t, reasonsSourcePath)

	guided := make(map[string]bool, len(reasonGuide))
	for r := range reasonGuide {
		guided[string(r)] = true
	}

	for code := range source {
		if !guided[code] {
			t.Errorf("reasons.go declares %q and the report has no action text for it. A hole the "+
				"operator is not told how to close is a hole nobody closes: add the row to "+
				"reasonGuide in report.go (the code, its line in reasons.go, its row in FORMAT.md §7 "+
				"and its action text belong in one commit)", code)
		}
	}
	for code := range guided {
		if !source[code] {
			t.Errorf("report.go carries an action text for %q, which is not a code in reasons.go. "+
				"Either the code was removed (drop the text) or it was invented here (the dictionary "+
				"is closed — codes live in reasons.go and nowhere else)", code)
		}
	}

	for r, g := range reasonGuide {
		if strings.TrimSpace(g.action) == "" {
			t.Errorf("%q has an EMPTY action text — an empty entry passes the coverage check above "+
				"while telling the operator nothing", r)
		}
		if g.status != StatusSkipped && g.status != StatusDegraded {
			t.Errorf("%q defaults to status %q; a hole is either %q (the row is not there) or %q "+
				"(it is there, thinner) — %q is neither, and a re-reported export hole would carry it",
				r, g.status, StatusSkipped, StatusDegraded, g.status)
		}
	}

	t.Run("FORMAT.md §7 lists the same codes", func(t *testing.T) {
		documented := reportParseFormatReasonRows(t, formatDocPath)
		for code := range source {
			if !documented[code] {
				t.Errorf("%q is in reasons.go and missing from the FORMAT.md §7 table — the format "+
					"document is the contract both sides are written against", code)
			}
		}
		for code := range documented {
			if !source[code] {
				t.Errorf("FORMAT.md §7 documents %q, which no longer exists in reasons.go", code)
			}
		}
	})
}

// TestEntityVocabularyIsDocumented is the reason guard's other half, and it exists because the half
// that was missing is what produced the defect it now watches.
//
// §7 documents TWO closed lists: the reason codes (guarded above, off the source of reasons.go) and
// the ENTITY words — the human noun for what a line happened to. Only the first was ever checked, so
// the entity list was prose: `piece_area` was missing from it for as long as the measured contours
// travelled, and the store dutifully reported every dropped contour as `pattern` — «the nearest word
// of the twelve» — sending an operator to re-upload a sheet that had imported perfectly well.
//
// A word that is in report.go and not in the document is exactly that failure starting again from
// the other end, so both directions fail.
func TestEntityVocabularyIsDocumented(t *testing.T) {
	source := reportParseEntityWords(t, "report.go")
	documented := reportParseFormatEntityWords(t, formatDocPath)

	for word := range source {
		if !documented[word] {
			t.Errorf("report.go declares the entity %q and FORMAT.md §7 does not list it. The §7 "+
				"vocabulary is what both sides are written against — a word only one side knows is "+
				"how a line ends up filed under somebody else's noun", word)
		}
	}
	for word := range documented {
		if !source[word] {
			t.Errorf("FORMAT.md §7 lists the entity %q, which is no longer an Entity* constant in "+
				"report.go", word)
		}
	}
	for _, e := range CountedEntities {
		if !source[e] {
			t.Errorf("%q is counted and is not an Entity* constant — the two lists are one vocabulary", e)
		}
	}
}

// reportParseEntityWords reads the string values of the Entity* constants out of the SOURCE of
// report.go. They are untyped string constants (unlike Reason), so they are found by NAME —
// which also means a constant renamed out of the Entity* family silently leaves this guard, and
// that is why the count below is asserted.
func reportParseEntityWords(t *testing.T, path string) map[string]bool {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — the entity vocabulary lives there; if it moved, re-point this test "+
			"(do not delete the check)", path, err)
	}

	out := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, "Entity") || i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Fatalf("%s: const %s is not a plain string literal (%T); decide by hand whether "+
						"§7 still documents it", path, name.Name, vs.Values[i])
				}
				word, err := strconv.Unquote(bl.Value)
				if err != nil || word == "" {
					t.Fatalf("%s: const %s has an unreadable or empty value %s", path, name.Name, bl.Value)
				}
				out[word] = true
			}
		}
	}

	if len(out) < len(CountedEntities) {
		t.Fatalf("%s parsed to %d entity words (%s) — fewer than the %d counted ones, so the file was "+
			"read and the vocabulary was not found; the comparison would pass against anything",
			path, len(out), strings.Join(reportSortedKeys(out), ", "), len(CountedEntities))
	}
	return out
}

// reportParseFormatEntityWords reads the entity vocabulary out of the §7 prose — the backticked
// words of the sentence that enumerates them, which is where the document states the list.
func reportParseFormatEntityWords(t *testing.T, path string) map[string]bool {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	section := string(body)
	i := strings.Index(section, "`entity` on a hole is the human word for what it happened to")
	if i < 0 {
		t.Fatalf("%s no longer states the entity vocabulary in §7; find where it went and re-point "+
			"this test", path)
	}
	section = section[i:]
	if j := strings.Index(section, "— and `ref` is"); j > 0 {
		section = section[:j]
	} else {
		t.Fatalf("%s: the entity sentence no longer ends where this test cuts it; re-point the cut "+
			"rather than widening it, or every backticked word in §7 would count as an entity", path)
	}

	out := map[string]bool{}
	for _, m := range reportFormatWordRe.FindAllStringSubmatch(section, -1) {
		if m[1] == "entity" { // the sentence names the FIELD before it lists its values
			continue
		}
		out[m[1]] = true
	}
	return out
}

var reportFormatWordRe = regexp.MustCompile("`([a-z0-9_]+)`")

// reportParseReasonCodes reads the string values of the `Reason` constants out of the SOURCE of
// reasons.go. Source rather than reflection because Go has no way to enumerate a const set at
// runtime, and a hand-kept list here would be exactly the second copy this test exists to prevent.
//
// Every way the read can go wrong is a failure, never an empty set: an empty set would turn both
// comparisons above into "nothing is missing" and the guard into decoration.
func reportParseReasonCodes(t *testing.T, path string) map[string]bool {
	t.Helper()

	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v — this test reads the closed dictionary out of that file; if it "+
			"moved, re-point the test (do not delete the check)", path, err)
	}

	out := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != "Reason" {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					t.Fatalf("%s: const %s has no value — the dictionary can only be read as "+
						"`Name Reason = \"code\"` lines", path, name.Name)
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					t.Fatalf("%s: const %s is not a plain string literal (%T); this test cannot "+
						"evaluate it, so decide by hand whether report.go still covers it",
						path, name.Name, vs.Values[i])
				}
				code, err := strconv.Unquote(bl.Value)
				if err != nil {
					t.Fatalf("%s: const %s has an unreadable value %s: %v", path, name.Name, bl.Value, err)
				}
				if code == "" {
					t.Fatalf("%s: const %s is the empty code", path, name.Name)
				}
				out[code] = true
			}
		}
	}

	if len(out) < 10 {
		t.Fatalf("%s parsed to %d reason codes — the file was read but the dictionary was not found "+
			"in it (it holds well over ten), so the comparison above would pass against anything",
			path, len(out))
	}
	return out
}

var reportFormatRowRe = regexp.MustCompile("(?m)^\\| `([a-z0-9_]+)` \\|")

// reportParseFormatReasonRows reads the code column of the FORMAT.md §7 table.
func reportParseFormatReasonRows(t *testing.T, path string) map[string]bool {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v — the reason table lives there and this test compares against it", path, err)
	}
	section := string(body)
	if i := strings.Index(section, "## 7. Reason codes"); i >= 0 {
		section = section[i:]
	} else {
		t.Fatalf("%s no longer has a '## 7. Reason codes' heading; find where the table went and "+
			"re-point this test", path)
	}

	out := map[string]bool{}
	for _, m := range reportFormatRowRe.FindAllStringSubmatch(section, -1) {
		out[m[1]] = true
	}
	if len(out) < 10 {
		t.Fatalf("%s §7 parsed to %d rows (%s) — the table was not read, so the comparison would "+
			"pass against anything", path, len(out), strings.Join(reportSortedKeys(out), ", "))
	}
	return out
}

func reportSortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ────────────────────────────── the detail is bounded in BYTES ──────────────────────────────

// A hole's detail quotes the archive — a fibre code, a size name, a scope key — and the archive is
// somebody else's 16 MiB file. Counting the ENTRIES a producer spells out bounds nothing when one
// entry can be megabytes: the string lands in the card's stored report and in every read of it
// afterwards. So the assertion here is in BYTES, which is what the column and the wire hold, and
// never in runes: a rune limit over Cyrillic prose bounds the string at twice the number it looks
// like, and every detail in this package may be Cyrillic.
func TestClipDetailBoundsBytesAndSaysItCut(t *testing.T) {
	t.Run("short strings are untouched", func(t *testing.T) {
		const s = "the row named nothing"
		if got := ClipDetail(s, DetailLimit); got != s {
			t.Errorf("ClipDetail rewrote a string that fits: %q", got)
		}
	})

	t.Run("a long string is clipped to the limit and says so", func(t *testing.T) {
		got := ClipDetail(strings.Repeat("x", 100_000), DetailLimit)
		if len(got) > DetailLimit {
			t.Errorf("ClipDetail returned %d bytes, over the %d-byte limit", len(got), DetailLimit)
		}
		if !strings.HasSuffix(got, detailClipMark) {
			t.Errorf("a clipped detail that does not SAY it was clipped reads as a complete one: %q",
				got[max(0, len(got)-40):])
		}
	})

	t.Run("cyrillic is bounded in bytes and stays valid UTF-8", func(t *testing.T) {
		// Two bytes per rune, so a rune-counting guard would let through twice the ceiling — and a
		// cut landing mid-rune produces invalid UTF-8, which protojson refuses to marshal: the
		// guard would then abort the very answer it exists to keep small.
		got := ClipDetail(strings.Repeat("я", 50_000), DetailLimit)
		if len(got) > DetailLimit {
			t.Errorf("ClipDetail returned %d bytes, over the %d-byte limit", len(got), DetailLimit)
		}
		if !utf8.ValidString(got) {
			t.Errorf("the cut landed inside a rune — protojson would refuse the report: %q",
				got[max(0, len(got)-16):])
		}
		if !strings.HasSuffix(got, detailClipMark) {
			t.Errorf("no clip mark on a clipped cyrillic detail: %q", got[max(0, len(got)-40):])
		}
	})

	t.Run("a limit too small to hold the mark still says it cut", func(t *testing.T) {
		got := ClipDetail(strings.Repeat("x", 500), 3)
		if got != detailClipMark {
			t.Errorf("a nonsensical limit must answer with the mark and never with a silent cut, got %q", got)
		}
	})
}

// The clip lives in reportLine, which is the ONE funnel both kinds of hole pass through — and one
// of the two kinds is foreign prose: an export hole's detail is copied out of somebody else's
// manifest.json verbatim. A producer that bounds its own text is doing it twice; a producer that
// forgets is bounded anyway, which is the property worth having.
func TestReportLineClipsAnExportHoleFromAForeignManifest(t *testing.T) {
	rep := BuildReport(ReportInput{
		ExportHoles: []ExportHole{{
			Entity: EntityMedia,
			Ref:    "media_id=7",
			Reason: ReasonMediaObjectMissing,
			Detail: strings.Repeat("д", 200_000),
		}},
	})

	if len(rep.GetLines()) != 1 {
		t.Fatalf("expected the re-reported export hole, got %d lines", len(rep.GetLines()))
	}
	got := rep.GetLines()[0].GetDetail()
	if len(got) > DetailLimit {
		t.Errorf("a foreign manifest's detail reached the report at %d bytes — it is stored on the "+
			"card and returned by every read of it", len(got))
	}
	if !utf8.ValidString(got) {
		t.Error("the clip cut inside a rune: protojson would refuse to marshal the report")
	}
}

// The defect this code exists to close: the handler used to report a media row that VANISHED under
// a running import with media_upload_failed. The action («import the archive again») was right and
// the PROSE was false — nothing about this instance's storage refused anything — and its tail sent
// the operator to inspect a bucket that is working perfectly. A wrong dictionary entry does not read
// oddly; it decides which of three places a person spends an afternoon in.
func TestMediaVanishedDoesNotBorrowTheUploadFailureSentence(t *testing.T) {
	vanished := ActionFor(ReasonMediaVanished)
	if vanished == "" {
		t.Fatal("media_vanished has no action text; the closed dictionary keeps the code, its §7 row and its sentence together")
	}
	if vanished == ActionFor(ReasonMediaUploadFailed) {
		t.Error("media_vanished repeats media_upload_failed's sentence — then the two codes are one code with two names")
	}
	if strings.Contains(strings.ToLower(vanished), "bucket") {
		t.Errorf("the sentence sends the operator to the bucket, which is the false half it was "+
			"split off to stop saying: %q", vanished)
	}
	if !strings.Contains(strings.ToLower(vanished), "again") {
		t.Errorf("the remedy IS to import again, and the line has to say so: %q", vanished)
	}
	if got := DefaultStatusFor(ReasonMediaVanished); got != StatusSkipped {
		t.Errorf("a slot left empty is skipped, not %q", got)
	}
}
