package techcardarchive

import (
	"strings"
	"testing"
)

// The amend exists because the DRY RUN cannot know everything the write will drop, and a report
// stamped unchanged would count the dropped rows as imported. These are the properties that make
// the amended report readable as the truth: the losses arrive as lines with action text, the
// counters MOVE rather than grow, and the report the amend was built from is left alone so a
// deadlock retry cannot count one dropped row twice.

func baseReportForTest(t *testing.T) *ImportReport {
	t.Helper()
	c := NewCounters()
	c.AddImported(EntityAssembly, 3)
	c.AddImported(EntityMedia, 2)
	c.AddSkipped(EntityMedia, 1)
	raw, err := MarshalReport(BuildReport(ReportInput{
		ImportID: "01J8", StyleNumber: "GRB-SS26-014", Stage: "proto", Counters: c,
	}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	rep, err := ParseReport(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return rep
}

// A payload that is not a report has to be refused where the refusal is still cheap. The write
// parses BEFORE it opens its transaction precisely so this cannot happen at the last statement,
// after the card and every child are written.
func TestParseReportRefusesWhatIsNotAReport(t *testing.T) {
	for _, in := range []string{"", "   ", "not json", `{"lines": 3}`, `["a"]`} {
		if _, err := ParseReport([]byte(in)); err == nil {
			t.Errorf("ParseReport(%q) must refuse", in)
		}
	}
	if _, err := ParseReport([]byte(`{"lines":[],"counters":[],"importId":"01J8"}`)); err != nil {
		t.Errorf("a report this package wrote must read back: %v", err)
	}
}

// WHAT THE WRITE DROPPED CANNOT STILL BE COUNTED AS IMPORTED, and the sum must survive the move —
// ValidateReportAgainstManifest compares the archive's own contents claim against the TOTAL, so an
// amend that removed rows instead of moving them would start refusing healthy imports.
func TestAmendMovesRowsOutOfImportedWithoutChangingTheSum(t *testing.T) {
	rep := baseReportForTest(t)
	lost := NewCounters()
	lost.AddSkipped(EntityAssembly, 2)

	out, err := rep.Amend([]ImportHole{{
		Entity: EntityAssembly, Ref: "component_style_number=GRB-LBL-1",
		Status: StatusSkipped, Reason: ReasonAssemblyComponentNotFound,
		Detail: "not an auxiliary card here",
	}}, lost)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	amended, err := ParseReport(out)
	if err != nil {
		t.Fatalf("the amended report must read back: %v", err)
	}

	got := tallyOf(t, amended)
	if got[EntityAssembly] != (EntityTally{Imported: 1, Skipped: 2}) {
		t.Fatalf("assembly = %+v, want imported=1 skipped=2", got[EntityAssembly])
	}
	if got[EntityAssembly].Sum() != 3 {
		t.Fatalf("assembly sum = %d, want 3: a move must not change how many rows there were",
			got[EntityAssembly].Sum())
	}
	if got[EntityMedia] != (EntityTally{Imported: 2, Skipped: 1}) {
		t.Fatalf("media = %+v, want the untouched entity left alone", got[EntityMedia])
	}
	if n := len(amended.msg.GetLines()); n != 1 {
		t.Fatalf("lines = %d, want 1", n)
	}
	if a := amended.msg.GetLines()[0].GetAction(); a == "" || a != ActionFor(ReasonAssemblyComponentNotFound) {
		t.Fatalf("the added line must carry the dictionary's own action text, got %q", a)
	}
}

// The closure that calls Amend is re-entered on a deadlock retry, so the report it amends must come
// out of the retry exactly as it went in.
func TestAmendLeavesTheReportItAmendedAlone(t *testing.T) {
	rep := baseReportForTest(t)
	lost := NewCounters()
	lost.AddSkipped(EntityAssembly, 1)
	hole := []ImportHole{{Entity: EntityAssembly, Ref: "component_style_number=GRB-LBL-1",
		Reason: ReasonAssemblyComponentNotFound, Detail: "not an auxiliary card here"}}

	first, err := rep.Amend(hole, lost)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	second, err := rep.Amend(hole, lost)
	if err != nil {
		t.Fatalf("amend again: %v", err)
	}
	a, b := tallyOf(t, mustParse(t, first)), tallyOf(t, mustParse(t, second))
	if a[EntityAssembly] != b[EntityAssembly] {
		t.Fatalf("a second attempt amended a report that already carried the first: %+v then %+v",
			a[EntityAssembly], b[EntityAssembly])
	}
	if n := len(mustParse(t, second).msg.GetLines()); n != 1 {
		t.Fatalf("lines after the second attempt = %d, want 1", n)
	}
}

// The two halves disagreeing about how many rows there were is a bug in the counting, not a reason
// to fail an import — and a NEGATIVE count would be, since the positive control refuses one.
func TestAmendCapsAMoveAtWhatTheImportedColumnHolds(t *testing.T) {
	rep := baseReportForTest(t)
	lost := NewCounters()
	lost.AddSkipped(EntityAssembly, 99)

	out, err := rep.Amend(nil, lost)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	got := tallyOf(t, mustParse(t, out))[EntityAssembly]
	if got != (EntityTally{Imported: 0, Skipped: 3}) {
		t.Fatalf("assembly = %+v, want imported=0 skipped=3 — capped, never negative", got)
	}
}

// A loss counted as IMPORTED is the one thing the amend cannot mean. Ignoring it would leave a
// number nobody reads, which is how a counter starts lying.
func TestAmendRefusesALossCountedAsImported(t *testing.T) {
	lost := NewCounters()
	lost.AddImported(EntityAssembly, 1)
	if _, err := baseReportForTest(t).Amend(nil, lost); err == nil ||
		!strings.Contains(err.Error(), "assembly") {
		t.Fatalf("err = %v, want a refusal naming the entity", err)
	}
}

// An entity the report does not count has no imported column for a row to leave. The LINE is the
// whole record of the loss, and a counter invented here would claim rows nobody ever counted.
func TestAmendInventsNoCounterForAnUncountedEntity(t *testing.T) {
	rep := baseReportForTest(t)
	lost := NewCounters()
	lost.AddSkipped(EntityCard, 1)

	// size_not_in_card_range and NOT size_unknown, and the detail beside it says why: the size is in
	// this base's dictionary and the imported CARD does not make it. The two were one code once, and
	// the sentence the operator was shown told them to add a size that was already there.
	out, err := rep.Amend([]ImportHole{{Entity: EntityCard, Ref: "size_id=99",
		Reason: ReasonSizeNotInCardRange, Detail: "the model wears a size this card does not make"}}, lost)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	amended := mustParse(t, out)
	if _, ok := tallyOf(t, amended)[EntityCard]; ok {
		t.Fatal("the card is not a counted entity; the amend must not add a counter row for it")
	}
	if n := len(amended.msg.GetLines()); n != 1 {
		t.Fatalf("lines = %d, want 1: the line is the whole record of an uncounted loss", n)
	}
	// The status the reporter did not state comes from the closed dictionary, not from a default
	// invented at the call site.
	if got := amended.msg.GetLines()[0].GetStatus(); got != DefaultStatusFor(ReasonSizeNotInCardRange) {
		t.Fatalf("status = %q, want the dictionary's %q", got, DefaultStatusFor(ReasonSizeNotInCardRange))
	}
}

// The entities the dictionary gained are uncounted ones, and an uncounted entity is the case where a
// wrong assumption is cheapest to make and most expensive to find: `piece_area` has no counter, so a
// line about a dropped contour must add a LINE and nothing else. Adding a counter row for it would
// put a number in front of the operator that no manifest claim can be reconciled against, and
// ValidateReportAgainstManifest would then be comparing against an entity nobody counts.
func TestAmendCarriesTheNewUncountedEntities(t *testing.T) {
	rep := baseReportForTest(t)
	before := tallyOf(t, rep) // Amend does not modify its receiver — TestAmendLeavesTheReportItAmendedAlone

	out, err := rep.Amend([]ImportHole{
		{Entity: EntityPieceArea, Ref: "piece_line_key=AAA scope_key=MAIN",
			Reason: ReasonArchiveRowInvalid, Detail: "the archive measures an area that names no cut piece"},
		{Entity: EntityCard, Ref: "composition_entries",
			Reason: ReasonCompositionNotDerived, Detail: "the breakdown is derived here, not imported"},
	}, NewCounters())
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	amended := mustParse(t, out)

	if _, ok := tallyOf(t, amended)[EntityPieceArea]; ok {
		t.Fatal("piece_area is not a counted entity; the amend must not add a counter row for it")
	}
	if got := tallyOf(t, amended); len(got) != len(before) {
		t.Fatalf("counter rows = %d, want the %d the report already had", len(got), len(before))
	}
	lines := amended.msg.GetLines()
	if len(lines) != 2 {
		t.Fatalf("lines = %d, want 2", len(lines))
	}
	// Each new code has to arrive with the status AND the sentence the dictionary holds for it —
	// which is the whole reason a code invented at a call site is refused.
	for i, want := range []Reason{ReasonArchiveRowInvalid, ReasonCompositionNotDerived} {
		if got := lines[i].GetStatus(); got != DefaultStatusFor(want) {
			t.Errorf("line %d status = %q, want the dictionary's %q", i, got, DefaultStatusFor(want))
		}
		if got := lines[i].GetAction(); got == "" || got != ActionFor(want) {
			t.Errorf("line %d action = %q, want the dictionary's %q", i, got, ActionFor(want))
		}
	}
	if lines[1].GetStatus() != StatusDegraded {
		t.Errorf("the card landed, one projection thinner: composition_not_derived is a degradation, "+
			"not a skip — got %q", lines[1].GetStatus())
	}
}

// The positive control runs against the stamped report too, and it must still hold: the archive
// claims two media, the write moved rows around, and the claim is still accounted for.
func TestAmendedReportStillPassesThePositiveControl(t *testing.T) {
	rep := baseReportForTest(t)
	lost := NewCounters()
	lost.AddSkipped(EntityAssembly, 3)

	out, err := rep.Amend(nil, lost)
	if err != nil {
		t.Fatalf("amend: %v", err)
	}
	manifest := &Manifest{Contents: Contents{Media: 2}}
	if err := ValidateReportAgainstManifest(mustParse(t, out).msg, manifest); err != nil {
		t.Fatalf("the amended report must still satisfy its own positive control: %v", err)
	}
}

func mustParse(t *testing.T, b []byte) *ImportReport {
	t.Helper()
	rep, err := ParseReport(b)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return rep
}

func tallyOf(t *testing.T, rep *ImportReport) map[string]EntityTally {
	t.Helper()
	out := make(map[string]EntityTally)
	for _, c := range rep.msg.GetCounters() {
		out[c.GetEntity()] = EntityTally{
			Imported: int(c.GetImported()), Skipped: int(c.GetSkipped()), Degraded: int(c.GetDegraded()),
		}
	}
	return out
}
