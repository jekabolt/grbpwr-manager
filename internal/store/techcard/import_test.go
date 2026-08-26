package techcard

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	"github.com/shopspring/decimal"
)

// THE IMPORT PATH CANNOT BE TESTED AGAINST A DATABASE HERE — the store's own suite talks to a real
// MySQL, so these are the checks that stand without one: every named query binds, the statements
// say the things the format depends on them saying, and the one piece of behaviour that runs
// OUTSIDE the transaction (the draft coercion) is exercised directly.

// TestArchiveImportQueriesBind reproduces, without a database, the failure that would otherwise
// happen at request time: sqlx reads EVERY ':' as a named parameter, so one inside a comment or one
// parameter missing from the map takes the whole import down at the statement that carries it —
// after the card has already been inserted.
func TestArchiveImportQueriesBind(t *testing.T) {
	cases := []struct {
		name  string
		query string
		args  map[string]any
		want  int
	}{
		{"claim", archiveImportClaimQuery, map[string]any{
			"import_id": "01J8", "committed": entity.TechCardImportStatusCommitted,
			"uploaded": entity.TechCardImportStatusUploaded,
		}, 3},
		{"status", archiveImportStatusQuery, map[string]any{"import_id": "01J8"}, 1},
		{"stamp result", archiveImportStampResultQuery, map[string]any{
			"import_id": "01J8", "tech_card_id": 7, "report": `{"lines":[]}`,
		}, 3},
		{"row by import id", archiveImportRowByIDQuery, map[string]any{"import_id": "01J8"}, 1},
		{"latest by card", archiveImportLatestByCardQuery, map[string]any{"tech_card_id": 7}, 1},
		{"acknowledge", archiveImportAckQuery, map[string]any{"tech_card_id": 7}, 1},
		{"style facts", archiveImportStyleFactsQuery, map[string]any{
			"id": 7, "fit": sql.NullString{}, "composition": sql.NullString{},
			"care_instructions": sql.NullString{}, "model_wears_height_cm": sql.NullInt32{},
			"model_wears_size_id": sql.NullInt32{},
		}, 6},
		{"grade base", archiveImportGradeBaseQuery, map[string]any{"id": 7, "base": sql.NullInt32{}}, 2},
		{"bom lines", archiveImportBomLinesQuery, map[string]any{"card": 7}, 1},
		{"labels", archiveImportLabelsQuery, map[string]any{"card": 7}, 1},
		{"label relink", archiveImportLabelRelinkQuery, map[string]any{"id": 3, "card": 7, "bom": int64(11)}, 3},
		{"marker insert", archiveImportMarkerInsertQuery, map[string]any{
			"tech_card_id": 7, "size_id": sql.NullInt64{}, "bom_item_id": sql.NullInt64{},
			"name": "shell 150", "source": "engine", "fabric_width_cm": decimal.Zero,
			"gap_cm": decimal.Zero, "edge_margin_cm": decimal.Zero, "selvedge_cm": decimal.Zero,
			"allow_cross_grain": false, "sets": sql.NullInt64{}, "total_units": 4,
			"used_length_cm": decimal.Zero, "efficiency_pct": decimal.NullDecimal{},
			"placed_count": 12, "total_count": 12, "is_draft": false, "layout": "{}",
			"schema_version":    entity.MarkerLayoutSchemaWithFlip,
			"seam_allowance_mm": decimal.NullDecimal{}, "contour_allowance_mm": decimal.NullDecimal{},
			"contour_layer": sql.NullString{}, "grain_layer": sql.NullString{},
			"allow_flip": sql.NullBool{}, "piece_set_fp": sql.NullString{}, "username": "im",
		}, 27},
		{"piece area insert", archiveImportPieceAreaInsertQuery, map[string]any{
			"card": 7, "scope": "MAIN", "piece": "01J8ZC5R7NQ1PP0A31", "size": sql.NullInt64{},
			"area": decimal.NewFromInt(120), "perimeter": decimal.NullDecimal{}, "layer": "1",
			"seam": decimal.Zero, "hulled": false, "ambiguous": false,
			"fingerprint": strings.Repeat("a", 64), "parsed_by": "im", "parsed_at": time.Now(),
		}, 13},
		{"component facts", archiveImportComponentFactsQuery, map[string]any{"ids": []int{7, 9}}, 2},
		{"assembly insert", archiveImportAssemblyInsertQuery, map[string]any{
			"style_id": 7, "component_tech_card_id": 9, "size_id": sql.NullInt32{},
			"qty": decimal.NewFromInt(1), "print_note": sql.NullString{},
			"position_note": sql.NullString{}, "active": true, "actor": "im",
		}, 9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, args, err := storeutil.MakeQuery(c.query, c.args)
			if err != nil {
				t.Fatalf("%s query does not bind: %v", c.name, err)
			}
			if len(args) != c.want {
				t.Fatalf("bound args = %d, want %d", len(args), c.want)
			}
		})
	}
}

// EVERY GUARD OF THIS FILE IS EXACTLY ITS CONDITIONS — no fewer, and above all no more.
//
// The claim is the whole defence against a double commit: two clicks on «import» are two calls
// naming one import_id, and without `status = :uploaded` in the WHERE the second one would sail
// through and create a SECOND card from the same archive, silently, since nothing downstream looks
// for a twin.
//
// It is checked as a SET OF CONDITIONS rather than by looking for the substring the guard is spelled
// with, because a substring check passes a statement that still contains it: append ` OR 1=1` to
// this WHERE and `status = :uploaded` is right there, unchanged, while the guard is gone. The same
// widening is what would turn the label re-sew — the one statement that binds a label to a BOM line
// — into an UPDATE across other people's cards, so the whole family is held to the same rule here.
func TestArchiveImportGuardsAreExactlyTheseConditions(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		{"claim", archiveImportClaimQuery, []string{"import_id = :import_id", "status = :uploaded"}},
		{"status", archiveImportStatusQuery, []string{"import_id = :import_id"}},
		{"stamp result", archiveImportStampResultQuery, []string{"import_id = :import_id"}},
		{"row by import id", archiveImportRowByIDQuery, []string{"import_id = :import_id"}},
		{"acknowledge", archiveImportAckQuery, []string{"tech_card_id = :tech_card_id", "acknowledged_at IS NULL"}},
		{"style facts", archiveImportStyleFactsQuery, []string{"id = :id"}},
		{"grade base", archiveImportGradeBaseQuery, []string{"id = :id"}},
		{"bom lines", archiveImportBomLinesQuery, []string{"tech_card_id = :card"}},
		{"labels", archiveImportLabelsQuery, []string{"tech_card_id = :card"}},
		{"label relink", archiveImportLabelRelinkQuery, []string{"id = :id", "tech_card_id = :card"}},
		{"component facts", archiveImportComponentFactsQuery, []string{"id IN (:ids)"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := whereConditions(t, c.query)
			if len(got) != len(c.want) {
				t.Fatalf("WHERE has %d conditions %q, want %d %q", len(got), got, len(c.want), c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("condition %d = %q, want %q (whole WHERE: %q)", i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// whereConditions returns the ANDed conditions of a single-statement query, and fails the test on
// anything that would make «ANDed conditions» the wrong description of its WHERE: a second
// statement, an OR, or a bracket that could group one.
func whereConditions(t *testing.T, query string) []string {
	t.Helper()
	flat := strings.Join(strings.Fields(query), " ")
	if strings.Contains(flat, ";") {
		t.Fatalf("the statement carries a second statement: %q", flat)
	}
	if strings.Contains(strings.ToUpper(flat), " OR ") {
		t.Fatalf("the statement's guard is widened by an OR: %q", flat)
	}
	parts := strings.SplitN(flat, " WHERE ", 2)
	if len(parts) != 2 {
		t.Fatalf("the statement has no WHERE at all: %q", flat)
	}
	if strings.Contains(parts[1], " WHERE ") {
		t.Fatalf("the statement has two WHERE clauses: %q", flat)
	}
	tail := parts[1]
	for _, clause := range []string{" ORDER BY ", " LIMIT ", " GROUP BY "} {
		if i := strings.Index(tail, clause); i >= 0 {
			tail = tail[:i]
		}
	}
	// `IN (:ids)` is the one legal bracket in this family; anything else could group an OR.
	if strings.Count(tail, "(") != strings.Count(tail, "IN (") {
		t.Fatalf("the WHERE carries a bracket that is not an IN list: %q", tail)
	}
	out := strings.Split(tail, " AND ")
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	return out
}

// An import creates no colourway and adopts no production run, and it designates no норма. All
// three are enforced by the STATEMENT rather than by a value, so no payload can express them:
// literal NULLs where a parameter would be, and no is_norm column at all.
func TestArchiveImportMarkerInsertWritesNoForeignOwnership(t *testing.T) {
	if strings.Contains(archiveImportMarkerInsertQuery, ":colorway_id") ||
		strings.Contains(archiveImportMarkerInsertQuery, ":run_id") {
		t.Fatal("an imported marker takes no colourway and no production run: both are literal NULL")
	}
	if strings.Contains(archiveImportMarkerInsertQuery, "is_norm") {
		t.Fatal("designating a норма is SetMarkerNorm's alone; the import must not write is_norm")
	}
	if !strings.Contains(archiveImportMarkerInsertQuery, "colorway_id, run_id") ||
		!strings.Contains(archiveImportMarkerInsertQuery, "NULL, NULL") {
		t.Fatal("colorway_id and run_id must still be written, as literal NULLs")
	}
}

// The label re-sew addresses one row by id AND by card. The id alone would be enough today; the
// card is there because this statement is the one that binds a label to a BOM line, and an
// unscoped UPDATE on that pair is exactly the mistake the whole re-sew exists to prevent.
func TestArchiveImportLabelRelinkIsScopedToTheCard(t *testing.T) {
	if !strings.Contains(archiveImportLabelRelinkQuery, "tech_card_id = :card") {
		t.Fatal("the label re-sew must be scoped to the imported card")
	}
}

// The measurement's provenance travels; it is not re-stamped. Without parsed_by / parsed_at in the
// statement the column defaults would quietly claim the importing operator measured these contours
// today — a false statement that reads exactly like a true one.
func TestArchiveImportPieceAreaCarriesItsProvenance(t *testing.T) {
	for _, col := range []string{"sheet_fingerprint", "parsed_by", "parsed_at"} {
		if !strings.Contains(archiveImportPieceAreaInsertQuery, col) {
			t.Errorf("the piece-area insert must write %s", col)
		}
	}
}

// AN IMPORTED CARD MAY NEVER LOOK SIGNED — the DB-free half of the adversarial gate (Ф3.6). The
// input here is the worst case the format is defended against: a RELEASED card carrying sign-offs,
// both approval stamps and the source's fit model, exactly what a hand-made archive would put in
// card.json.
func TestPreparedImportedCardCannotLookSigned(t *testing.T) {
	s := &Store{Base: storeutil.Base{Now: time.Now}}
	signedAt := sql.NullTime{Time: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Valid: true}
	card := &entity.TechCardInsert{
		Name:          "released style",
		ApprovalState: entity.TechCardApprovalReleased,
		ApprovedAt:    signedAt,
		ReleasedAt:    signedAt,
		BaseModelId:   sql.NullInt32{Int32: 42, Valid: true},
		Signoffs: []entity.TechCardSignoff{
			{Section: entity.SignoffConstruction, SignedBy: sql.NullString{String: "someone else", Valid: true}},
		},
	}

	s.prepareImportedCard(card)

	if card.ApprovalState != entity.TechCardApprovalDraft {
		t.Errorf("approval state = %q, want draft", card.ApprovalState)
	}
	if len(card.Signoffs) != 0 {
		t.Errorf("sign-offs survived the import: %d left", len(card.Signoffs))
	}
	if card.ApprovedAt.Valid || card.ReleasedAt.Valid {
		t.Errorf("approval stamps survived: approved=%v released=%v", card.ApprovedAt, card.ReleasedAt)
	}
	if card.BaseModelId.Valid {
		t.Errorf("the source's fit model survived: %v", card.BaseModelId)
	}
}

// The provenance token an imported scope carries must be UNABLE to equal a fingerprint computed
// from a real sheet set — that inequality IS the «measured elsewhere, recount me» verdict the
// reader shows. The empty sheet set is the case worth naming: a card whose scope has no sheets and
// no block links produces a fixed value, and a fingerprint derived from an empty string would have
// collided with it.
func TestImportedPieceAreaFingerprintCannotReadAsCurrent(t *testing.T) {
	fp := importedPieceAreaFingerprint("01J8ZC4Q0FQ8M6R0K2", "MAIN")
	if len(fp) != 64 {
		t.Fatalf("fingerprint length = %d, want 64 hex characters", len(fp))
	}
	if strings.Trim(fp, "0123456789abcdef") != "" {
		t.Fatalf("fingerprint is not lower-case hex: %q", fp)
	}
	if fp != importedPieceAreaFingerprint("01J8ZC4Q0FQ8M6R0K2", "MAIN") {
		t.Fatal("the token must be stable across a scope's rows")
	}
	if fp == importedPieceAreaFingerprint("01J8ZC4Q0FQ8M6R0K2", "LINING") {
		t.Fatal("two scopes of one import must not share a token")
	}
	if fp == importedPieceAreaFingerprint("01J8ZC4Q0FQ8M6R0K3", "MAIN") {
		t.Fatal("two imports of one scope must not share a token")
	}
	if fp == entity.PieceAreaSourceFingerprint(nil, nil) {
		t.Fatal("the token must not equal the fingerprint of a card with no sheets and no block links")
	}
}

// The journal entry is the only thing that survives the tech_card_import row being pruned, so it
// has to name the source in words rather than degrade into an empty sentence.
func TestImportedFromArchiveSummaryNamesTheSource(t *testing.T) {
	got := importedFromArchiveSummary(entity.TechCardArchiveImport{
		SourceStyleNumber: "GRB-SS26-014", SourceHost: "backend.grbpwr.com",
	})
	if got != "imported from archive GRB-SS26-014 of backend.grbpwr.com" {
		t.Fatalf("summary = %q", got)
	}
	blank := importedFromArchiveSummary(entity.TechCardArchiveImport{})
	if !strings.HasPrefix(blank, "imported from archive ") || strings.Contains(blank, "  ") {
		t.Fatalf("a nameless source must still read as a sentence, got %q", blank)
	}
}

// line_key comparisons are the ONLY way a label, a marker or a piece area finds its row on the
// imported card. The column's collation is case-insensitive, so the lookup key has to be too — a
// case-sensitive map would fail to find a line that is right there, and the label would then be
// refused (loudly, which is the good half) on a perfectly good archive.
func TestImportedLineKeyFoldsCaseAndSpace(t *testing.T) {
	want := "01J8ZC4Q0FQ8M6R0K2"
	for _, in := range []string{"01j8zc4q0fq8m6r0k2", "  01J8ZC4Q0FQ8M6R0K2 ", "01J8ZC4Q0FQ8M6R0K2"} {
		if got := importedLineKey(in); got != want {
			t.Errorf("importedLineKey(%q) = %q, want %q", in, got, want)
		}
	}
	if importedLineKey("   ") != "" {
		t.Error("a blank key must normalise to empty so it is skipped rather than looked up")
	}
}

// ────────────────────────────── what the WRITE dropped ──────────────────────────────
//
// The transaction cannot be run here (it needs MySQL), so what is exercised below is every DECISION
// it makes about dropping a row — which is exactly the half that used to reach slog and never reach
// the operator. Each case asks the same two questions: was the row dropped, and does the report say
// so in a way that leaves the counters honest.

// A loss that never had a counter must not invent one, and a loss that did must move exactly one
// row per loss — including when two rows are lost for the same reason and share a line.
func TestImportLossesSeparateLinesFromCounters(t *testing.T) {
	l := newImportLosses()
	l.drop(techcardarchive.EntityCard, "size_name=XXL", techcardarchive.StatusDegraded,
		techcardarchive.ReasonSizeNotInCardRange, "the model wears a size this card does not make")
	l.dropCounted(techcardarchive.EntityAssembly, "component_style_number=GRB-LBL-1",
		techcardarchive.StatusSkipped, techcardarchive.ReasonAssemblyComponentNotFound, "not auxiliary here")
	l.dropCounted(techcardarchive.EntityAssembly, "component_style_number=GRB-LBL-1",
		techcardarchive.StatusSkipped, techcardarchive.ReasonAssemblyComponentNotFound, "not auxiliary here")

	if len(l.holes) != 2 {
		t.Fatalf("lines = %d, want 2: the two assembly losses share a (entity, ref, reason) and one line", len(l.holes))
	}
	if got := l.lost[techcardarchive.EntityAssembly].Skipped; got != 2 {
		t.Fatalf("assembly rows moved = %d, want 2: a deduplicated LINE must not deduplicate the rows", got)
	}
	if got := l.lost[techcardarchive.EntityCard].Skipped + l.lost[techcardarchive.EntityCard].Degraded; got != 0 {
		t.Fatalf("card rows moved = %d, want 0: the card carries no counter to move", got)
	}
}

// The card's «model wears» reference names a size the card does not make: cleared, and SAID. The
// card itself landed, so the line is a degradation of the card rather than a skipped size — the
// same shape the resolver gives a card that lost its category.
func TestImportedModelWearsSizeOutOfRangeIsReported(t *testing.T) {
	rng := storeutil.NewTechCardSizeRange(7, 11, 12)
	l := newImportLosses()

	if got := importedModelWearsSize(sql.NullInt32{Int32: 11, Valid: true}, rng, l); !got.Valid || got.Int32 != 11 {
		t.Fatalf("a size the card makes must survive, got %+v", got)
	}
	if len(l.holes) != 0 {
		t.Fatalf("a size the card makes must leave no line, got %+v", l.holes)
	}

	got := importedModelWearsSize(sql.NullInt32{Int32: 99, Valid: true}, rng, l)
	if got.Valid {
		t.Fatalf("a size outside the card's range must be cleared, got %+v", got)
	}
	if len(l.holes) != 1 {
		t.Fatalf("lines = %d, want 1", len(l.holes))
	}
	h := l.holes[0]
	if h.Entity != techcardarchive.EntityCard || h.Status != techcardarchive.StatusDegraded ||
		h.Reason != techcardarchive.ReasonSizeNotInCardRange || h.Ref != "size_id=99" {
		t.Fatalf("line = %+v, want a degraded card line naming size 99", h)
	}
	// The reason is the instruction. This check ran against `size_unknown` for a whole review cycle
	// and passed, while the sentence the operator was shown said «add the size to the dictionary» —
	// about a size the dictionary already had, refused by the CARD's range. Naming the code is not
	// enough to catch that; the action text has to be read.
	if act := techcardarchive.ActionFor(h.Reason); strings.Contains(act, "to the dictionary") {
		t.Errorf("the action for %q sends the operator to the size dictionary (%q), which placed this "+
			"size perfectly well — it is the imported card's range that refused it", h.Reason, act)
	}
	if l.lost[techcardarchive.EntitySize].Skipped != 0 {
		t.Fatal("the size itself imported; moving the size counter would say it did not")
	}
}

// A chart is sizes × measurements, so one size the card does not make costs a row per measurement.
// The operator gets ONE line per size with the count in it, and every other cell still lands.
func TestImportedChartCellsReportOneLinePerSize(t *testing.T) {
	rng := storeutil.NewTechCardSizeRange(7, 11)
	chart := entity.StyleSizeChart{Cells: []entity.StyleSizeChartCell{
		{SizeID: 11, MeasurementNameID: 3, Value: decimal.NewFromInt(50)},
		{SizeID: 11, MeasurementNameID: 4, Value: decimal.NewFromInt(60)},
		{SizeID: 99, MeasurementNameID: 3, Value: decimal.NewFromInt(52)},
		{SizeID: 99, MeasurementNameID: 4, Value: decimal.NewFromInt(62)},
		{SizeID: 0, MeasurementNameID: 4, Value: decimal.NewFromInt(62)},
		{SizeID: 11, MeasurementNameID: 0, Value: decimal.NewFromInt(62)},
	}}
	l := newImportLosses()

	rows := importedChartCellRows(7, chart, rng, l)
	if len(rows) != 2 {
		t.Fatalf("kept cells = %d, want 2 (both cells of the size the card makes)", len(rows))
	}
	if len(l.holes) != 3 {
		t.Fatalf("lines = %d, want 3 (one per out-of-range size, one per unaddressed axis): %+v", len(l.holes), l.holes)
	}
	if !strings.Contains(l.holes[0].Detail, "2 size chart rows") {
		t.Fatalf("the out-of-range line must count the rows it dropped, got %q", l.holes[0].Detail)
	}
	if l.holes[0].Ref != "size_id=99" || l.holes[0].Reason != techcardarchive.ReasonSizeNotInCardRange {
		t.Fatalf("line = %+v, want size 99 reported as size_not_in_card_range — the dictionary has it, "+
			"this card does not make it", l.holes[0])
	}
	// The two «addresses nothing» lines must stay distinguishable BY ENTITY: one cell named no
	// measurement, one named no size. But neither is a missing reference — there is no name to add to
	// any dictionary — so both carry archive_row_invalid, and an operator is no longer sent to a
	// dictionary to look for something the row never named.
	if l.holes[1].Entity != techcardarchive.EntityMeasurement ||
		l.holes[1].Reason != techcardarchive.ReasonArchiveRowInvalid {
		t.Fatalf("line = %+v, want the measurement-less cell reported as a measurement whose ROW is "+
			"unusable, not as a dictionary miss", l.holes[1])
	}
	if l.holes[2].Entity != techcardarchive.EntitySize ||
		l.holes[2].Reason != techcardarchive.ReasonArchiveRowInvalid {
		t.Fatalf("line = %+v, want the size-less cell reported as a size whose ROW is unusable", l.holes[2])
	}
	for _, h := range l.holes[1:] {
		if act := techcardarchive.ActionFor(h.Reason); strings.Contains(act, "Add the ") {
			t.Errorf("line %+v is told %q — there is nothing to add: the cell named no dictionary "+
				"entry at all", h, act)
		}
	}
}

// THE GRADE RULE IS BOTH HALVES OR NEITHER. Steps used to be written before the base was checked, so
// a base outside the card's range left the steps standing alone — «grows by 2 cm per size away from
// the base», with no base — and that reads on the card exactly like a rule somebody authored.
func TestImportedGradeRuleIsBothHalvesOrNeither(t *testing.T) {
	steps := []entity.StyleSizeChartGradeStep{
		{MeasurementNameID: 3, Step: decimal.NewFromInt(2)},
		{MeasurementNameID: 4, Step: decimal.NewFromInt(1)},
	}
	rng := storeutil.NewTechCardSizeRange(7, 11, 12)

	l := newImportLosses()
	base, stepRows := importedGradeRule(7, entity.StyleSizeChart{GradeBaseSizeID: 11, GradeSteps: steps}, rng, l)
	if base != 11 || len(stepRows) != 2 {
		t.Fatalf("a base the card makes must keep the whole rule, got base=%d steps=%d", base, len(stepRows))
	}
	if len(l.holes) != 0 {
		t.Fatalf("a whole rule must leave no line, got %+v", l.holes)
	}

	l = newImportLosses()
	base, stepRows = importedGradeRule(7, entity.StyleSizeChart{GradeBaseSizeID: 99, GradeSteps: steps}, rng, l)
	if base != 0 {
		t.Fatalf("base = %d, want it cleared: the card does not make size 99", base)
	}
	if len(stepRows) != 0 {
		t.Fatalf("steps = %d, want none: a step without its base is half a rule that reads as a whole one", len(stepRows))
	}
	if len(l.holes) != 1 {
		t.Fatalf("lines = %d, want 1: dropping a rule silently is what this test exists to prevent", len(l.holes))
	}
	if !strings.Contains(l.holes[0].Detail, "2 steps") {
		t.Fatalf("the line must say the steps went with the base, got %q", l.holes[0].Detail)
	}
}

// MySQL's TIMESTAMP starts one second AFTER the Unix epoch, and an unset protobuf Timestamp IS the
// epoch — so an archive that simply carries no parsed_at would otherwise reach the driver and take
// the whole transaction down with a bare 1292, at a statement in the middle of the import.
func TestFitsMySQLTimestampRefusesWhatTheColumnRefuses(t *testing.T) {
	cases := []struct {
		name string
		in   time.Time
		want bool
	}{
		{"the zero time.Time", time.Time{}, false},
		{"the unix epoch, which is an unset protobuf timestamp", time.Unix(0, 0).UTC(), false},
		{"one second after the epoch, the column's floor", time.Unix(1, 0).UTC(), true},
		{"an ordinary measurement", time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC), true},
		{"the column's ceiling", time.Date(2038, 1, 19, 3, 14, 7, 0, time.UTC), true},
		{"one second past the ceiling", time.Date(2038, 1, 19, 3, 14, 8, 0, time.UTC), false},
		// A non-UTC zone must be judged on the instant, not on the wall clock: 00:30 on 1 Jan 1970
		// in Riga is half an hour BEFORE the epoch.
		{"before the epoch in another zone", time.Date(1970, 1, 1, 0, 30, 0, 0, time.FixedZone("EET", 2*60*60)), false},
	}
	for _, c := range cases {
		if got := fitsMySQLTimestamp(c.in); got != c.want {
			t.Errorf("%s: fitsMySQLTimestamp(%s) = %v, want %v", c.name, c.in, got, c.want)
		}
	}
}

// A measured area the target cannot hold costs the ROW and a report line — never the import. The
// date is the case worth naming: re-dating the measurement would claim one nobody took, which is
// the one thing this whole path refuses to do.
func TestImportedPieceAreaRowsDropWhatTheyCannotStore(t *testing.T) {
	rng := storeutil.NewTechCardSizeRange(7, 11)
	measured := time.Date(2026, 2, 3, 10, 0, 0, 0, time.UTC)
	areas := []entity.TechCardArchivePieceArea{
		{ScopeKey: "MAIN", PieceLineKey: "AAA", AreaCm2: decimal.NewFromInt(120), ParsedAt: measured},
		{ScopeKey: "MAIN", PieceLineKey: "BBB", AreaCm2: decimal.NewFromInt(90), ParsedAt: time.Unix(0, 0).UTC()},
		{ScopeKey: "MAIN", PieceLineKey: "CCC", AreaCm2: decimal.Zero, ParsedAt: measured},
		{ScopeKey: "", PieceLineKey: "DDD", AreaCm2: decimal.NewFromInt(10), ParsedAt: measured},
		{ScopeKey: "MAIN", PieceLineKey: "EEE", AreaCm2: decimal.NewFromInt(10), ParsedAt: measured,
			SizeId: sql.NullInt64{Int64: 99, Valid: true}},
	}
	l := newImportLosses()

	rows, err := importedPieceAreaRows(7, "01J8", areas, rng, l)
	if err != nil {
		t.Fatalf("a droppable row must not fail the import: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("kept rows = %d, want 1 (only the measurement this base can hold)", len(rows))
	}
	if len(l.holes) != 4 {
		t.Fatalf("lines = %d, want 4 — every dropped row on the report: %+v", len(l.holes), l.holes)
	}
	for _, h := range l.holes {
		if h.Entity != techcardarchive.EntityPieceArea {
			t.Errorf("line %+v: a measured contour is its own entity — reported under `pattern` it "+
				"reads as a sheet that did not import, and the sheet imported", h)
		}
	}
	// THE THREE MALFORMED ROWS AND THE ONE OUT-OF-RANGE SIZE ARE DIFFERENT LOSSES. The first three
	// were unusable in the archive itself — no scope, no area, no storable date — and nothing added
	// on this side closes them; the fourth names a size this base has and this card does not make.
	// Reported under one code (they were both, at different times: pattern_invalid and size_unknown)
	// the operator is sent to re-upload a readable file, or to add a size that is already there.
	byReason := map[techcardarchive.Reason]int{}
	for _, h := range l.holes {
		byReason[h.Reason]++
	}
	if byReason[techcardarchive.ReasonArchiveRowInvalid] != 3 ||
		byReason[techcardarchive.ReasonSizeNotInCardRange] != 1 {
		t.Fatalf("reasons = %+v, want 3 archive_row_invalid + 1 size_not_in_card_range: %+v", byReason, l.holes)
	}
	for _, h := range l.holes {
		if act := techcardarchive.ActionFor(h.Reason); act == "" || strings.Contains(act, "DXF") {
			t.Errorf("line %+v is told %q — the pattern sheet read perfectly well; it is the measured "+
				"row that did not", h, act)
		}
	}
	if !strings.Contains(l.holes[0].Detail, "1970-01-01 00:00:00") {
		t.Errorf("the date line must quote the date it refused, got %q", l.holes[0].Detail)
	}

	// A DUPLICATE is the archive contradicting itself — the row it collides with is in the same file
	// — so that one is still loud.
	dup := []entity.TechCardArchivePieceArea{
		{ScopeKey: "MAIN", PieceLineKey: "AAA", AreaCm2: decimal.NewFromInt(120), ParsedAt: measured},
		{ScopeKey: "main", PieceLineKey: "aaa", AreaCm2: decimal.NewFromInt(121), ParsedAt: measured},
	}
	if _, err := importedPieceAreaRows(7, "01J8", dup, rng, newImportLosses()); err == nil {
		t.Fatal("the same piece measured twice in one scope and size must still refuse the import")
	}
}

// The assembly bill is the one place the dry run counted the very rows this transaction drops, so it
// is the one place a counter has to move with the line.
func TestImportedAssemblyLinesMoveTheCounterTheyDrop(t *testing.T) {
	rng := storeutil.NewTechCardSizeRange(7, 11)
	aux := string(entity.TechCardPurposeAuxiliary)
	facts := map[int]importedComponentFacts{
		9:  {Id: 9, Purpose: aux, StyleNumber: "GRB-LBL-1"},
		10: {Id: 10, Purpose: "garment", StyleNumber: "GRB-SS26-014"},
	}
	one := decimal.NewFromInt(1)
	items := []entity.StyleAssemblyInsert{
		{ComponentTechCardId: 9, Qty: one},                                                // kept
		{ComponentTechCardId: 9, Qty: one, SizeId: sql.NullInt32{Int32: 11, Valid: true}}, // kept
		{ComponentTechCardId: 9, Qty: one},                                                // duplicate
		{ComponentTechCardId: 10, Qty: one},                                               // not auxiliary
		{ComponentTechCardId: 9, Qty: decimal.Zero},                                       // no quantity
		{ComponentTechCardId: 7, Qty: one},                                                // itself
		{ComponentTechCardId: 0, Qty: one},                                                // nothing
		{ComponentTechCardId: 9, Qty: one, SizeId: sql.NullInt32{Int32: 99, Valid: true}}, // size it does not make
		{ComponentTechCardId: 12, Qty: one},                                               // gone since the dry run
	}
	l := newImportLosses()

	kept := importedAssemblyLines(7, items, facts, rng, l)
	if len(kept) != 2 {
		t.Fatalf("kept lines = %d, want 2", len(kept))
	}
	if got := l.lost[techcardarchive.EntityAssembly].Skipped; got != 7 {
		t.Fatalf("assembly rows moved out of imported = %d, want 7 — one per dropped line", got)
	}
	// One component can be lost for two DIFFERENT reasons and then carries two lines: the ref names
	// the component, the reason says what went wrong with it, and folding them together would tell
	// the operator only one of the two things they have to fix.
	seen := map[string]bool{}
	for _, h := range l.holes {
		seen[h.Ref+"|"+string(h.Reason)] = true
	}
	for _, want := range []string{
		"component_style_number=GRB-SS26-014|" + string(techcardarchive.ReasonAssemblyComponentNotFound),
		"component_style_number=GRB-LBL-1|" + string(techcardarchive.ReasonAssemblyComponentNotFound),
		"component_style_number=GRB-LBL-1|" + string(techcardarchive.ReasonSizeNotInCardRange),
		"component_tech_card_id=0|" + string(techcardarchive.ReasonAssemblyComponentNotFound),
		"component_tech_card_id=7|" + string(techcardarchive.ReasonAssemblyComponentNotFound),
		"component_tech_card_id=12|" + string(techcardarchive.ReasonAssemblyComponentNotFound),
	} {
		if !seen[want] {
			t.Errorf("no report line for %q; the report has %+v", want, l.holes)
		}
	}
	// The duplicate and the zero quantity name the same component for the same reason code, so they
	// SHARE a line — while both still left the imported column.
	if len(l.holes) != 6 {
		t.Fatalf("lines = %d, want 6: seven losses, six distinct (component, reason) pairs", len(l.holes))
	}
	// The size line is the one this bill can get WRONG in a way the operator acts on. Its size is in
	// this base's dictionary — the resolver put it there — and it is the imported card that does not
	// make it. Reported as size_unknown, the sentence read «add the size to the dictionary and import
	// again», which fixes nothing and reruns the whole import to prove it.
	for _, h := range l.holes {
		if h.Reason != techcardarchive.ReasonSizeNotInCardRange {
			continue
		}
		act := techcardarchive.ActionFor(h.Reason)
		if strings.Contains(act, "to the dictionary") || !strings.Contains(act, "size range") {
			t.Errorf("the assembly line dropped over the card's own size range is told %q; it has to "+
				"name the RANGE, and must not send anybody to the dictionary", act)
		}
	}
}

// THE WHOLE POINT, END TO END: a report built by the dry run, amended with what the write dropped,
// and read back as the client would read it. The counters must MOVE — what the write dropped cannot
// still be counted as imported — and the sum must not, or the positive control that compares the
// tally with the archive's own contents claim would start refusing healthy imports.
func TestAmendedReportCannotStillCallADroppedRowImported(t *testing.T) {
	counters := techcardarchive.NewCounters()
	counters.AddImported(techcardarchive.EntityAssembly, 3)
	counters.AddImported(techcardarchive.EntitySize, 4)
	built := techcardarchive.BuildReport(techcardarchive.ReportInput{
		ImportID: "01J8", StyleNumber: "GRB-SS26-014", Stage: "proto", Counters: counters,
	})
	raw, err := techcardarchive.MarshalReport(built)
	if err != nil {
		t.Fatalf("marshal the dry run's report: %v", err)
	}
	base, err := techcardarchive.ParseReport(raw)
	if err != nil {
		t.Fatalf("parse the dry run's report: %v", err)
	}

	l := newImportLosses()
	l.dropCounted(techcardarchive.EntityAssembly, "component_style_number=GRB-LBL-1",
		techcardarchive.StatusSkipped, techcardarchive.ReasonAssemblyComponentNotFound,
		"a card with this number exists here but is not an AUXILIARY one")
	l.drop(techcardarchive.EntitySize, "size_id=99", techcardarchive.StatusSkipped,
		techcardarchive.ReasonSizeUnknown, "2 size chart rows filed under a size the card does not make")

	stamped, err := l.stamp(base)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}

	var got struct {
		Lines []struct {
			Entity, Ref, Status, Reason, Detail, Action string
		} `json:"lines"`
		Counters []struct {
			Entity                      string
			Imported, Skipped, Degraded int
		} `json:"counters"`
	}
	if err := json.Unmarshal(stamped, &got); err != nil {
		t.Fatalf("the stamped report is not readable as the client reads it: %v", err)
	}
	if len(got.Lines) != 2 {
		t.Fatalf("stamped lines = %d, want 2", len(got.Lines))
	}
	for _, ln := range got.Lines {
		if ln.Action == "" {
			t.Errorf("line %+v carries no action text: a hole an operator cannot close reads as a "+
				"complaint. Lines assembled outside the report package are how that happens", ln)
		}
	}
	tally := map[string][3]int{}
	for _, c := range got.Counters {
		tally[c.Entity] = [3]int{c.Imported, c.Skipped, c.Degraded}
	}
	if got := tally[techcardarchive.EntityAssembly]; got != [3]int{2, 1, 0} {
		t.Fatalf("assembly counter = %v, want [2 1 0]: the dropped line must leave the imported column", got)
	}
	if got := tally[techcardarchive.EntitySize]; got != [3]int{4, 0, 0} {
		t.Fatalf("size counter = %v, want [4 0 0]: the sizes themselves imported; only chart rows went", got)
	}
	if _, ok := tally[techcardarchive.EntityMedia]; !ok {
		t.Fatal("the amended report must still carry every counted entity, even at zero")
	}
}

// The journal sentence is the last door through which a stranger's free text reaches a permanent
// record of ours, and change_note is a TEXT column: 65 535 bytes, at the LAST statement of the
// import. A manifest that is perfectly valid to the reader can carry a 70 KiB host.
func TestImportedFromArchiveSummaryClampsWhatTheArchiveSupplied(t *testing.T) {
	got := importedFromArchiveSummary(entity.TechCardArchiveImport{
		SourceStyleNumber: "GRB-SS26-014", SourceHost: "backend.grbpwr.com",
	})
	if got != "imported from archive GRB-SS26-014 of backend.grbpwr.com" {
		t.Fatalf("an ordinary source must read exactly as before, got %q", got)
	}
	blank := importedFromArchiveSummary(entity.TechCardArchiveImport{})
	if !strings.HasPrefix(blank, "imported from archive ") || strings.Contains(blank, "  ") {
		t.Fatalf("a nameless source must still read as a sentence, got %q", blank)
	}

	huge := importedFromArchiveSummary(entity.TechCardArchiveImport{
		SourceStyleNumber: strings.Repeat("Ы", 70*1024),
		SourceHost:        strings.Repeat("h", 70*1024),
	})
	if len(huge) > 4096 {
		t.Fatalf("the sentence is %d bytes; change_note holds 65 535 and this is the last statement "+
			"of the import", len(huge))
	}
	if !utf8.ValidString(huge) {
		t.Fatal("the cut must be at a rune boundary: MySQL refuses a broken sequence with 1366")
	}
	if !strings.Contains(huge, "…") {
		t.Fatal("a truncation nobody can see reads exactly like a short name")
	}
}

// clampProvenance is what stands between a file somebody sent us and a line a human reads.
func TestClampProvenanceMakesArchiveTextFitToRead(t *testing.T) {
	cases := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"an ordinary value is untouched", "GRB-SS26-014", 255, "GRB-SS26-014"},
		{"surrounding space goes", "  backend.grbpwr.com \t", 253, "backend.grbpwr.com"},
		{"a newline cannot rearrange the журнал", "host\nStyle: forged", 253, "host Style: forged"},
		{"control characters become spaces", "host\x00\x1b[31m", 253, "host [31m"},
		{"invalid UTF-8 is dropped, not stored", "host\xff\xfe", 253, "host"},
		{"a rune outside the BMP cannot reach a utf8mb3 column", "host\U0001F600", 253, "host�"},
		{"the cut is at a rune boundary and is said", strings.Repeat("Ы", 10), 4, "ЫЫЫЫ…"},
		{"nothing but noise reads as nothing", "\x00\x01\x02", 253, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := clampProvenance(c.in, c.max); got != c.want {
				t.Fatalf("clampProvenance(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// A раскладка is dropped WHOLE over a size the imported card does not make, and the two ways it can
// name one — the состав and the legacy summary size — are one decision with one line.
//
// The decision used to be a REFUSAL of the entire import, and the archive that reached it is one our
// own export writes: narrowing a card's size range while its markers are alive is legal (the prune
// clears measurements and the grade base, and a состав's foreign key points at the size DICTIONARY),
// the export carries every marker of the card, and the export-side reimport probe cannot see markers
// at all — TechCardInsert has none. So this is also the test that says the backup restores.
func TestImportedMarkerOutsideTheCardsRangeIsNamedWhole(t *testing.T) {
	rng := storeutil.NewTechCardSizeRange(7, 11, 12)
	mk := func(size int64, composition ...int) entity.TechCardMarkerInsert {
		out := entity.TechCardMarkerInsert{Name: "lay"}
		if size > 0 {
			out.SizeId = sql.NullInt64{Int64: size, Valid: true}
		}
		for _, s := range composition {
			out.Composition = append(out.Composition, entity.MarkerCompositionEntry{SizeId: s, Quantity: 1})
		}
		return out
	}
	cases := []struct {
		name string
		in   entity.TechCardMarkerInsert
		want []int
	}{
		{"a lay of sizes the card makes stays", mk(11, 11, 12), nil},
		{"a состав naming one size outside the range", mk(0, 11, 99), []int{99}},
		{"the legacy summary size alone", mk(99), []int{99}},
		{"both halves name the same size once", mk(99, 99), []int{99}},
		{"a mixed lay names every offending size, sorted", mk(0, 98, 12, 99), []int{98, 99}},
		{"«no size» is not size zero", mk(0, 11), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := importedMarkerSizesOutsideRange(c.in, rng)
			if len(got) != len(c.want) {
				t.Fatalf("sizes outside the range = %v, want %v", got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Fatalf("sizes outside the range = %v, want %v", got, c.want)
				}
			}
		})
	}

	// THE SENTENCE THE OPERATOR READS. A mixed lay is lost over a SET of sizes, so naming only the
	// first would have them widen the range, import again, and be told about the next one.
	if got := importedSizeRefs([]int{98, 99}); got != "size_id=98, size_id=99" {
		t.Fatalf("detail names %q, want both sizes", got)
	}
	// The ref is the resolver's own, so the dry run's marker lines and the write's read as one
	// document: dropping the marker under a size ref would file it beside the size chart's losses.
	if got := importedMarkerRef("  RT · основная 150 "); got != "marker_name=RT · основная 150" {
		t.Fatalf("marker ref = %q", got)
	}

	// AND THE LOSS IS COUNTED, not merely mentioned: the resolver tallied this раскладка as imported,
	// and a report that still says so about a marker the write threw away is the exact lie the
	// amendment mechanism exists to prevent.
	l := newImportLosses()
	l.dropCounted(techcardarchive.EntityMarker, importedMarkerRef("lay"),
		techcardarchive.StatusSkipped, techcardarchive.ReasonSizeNotInCardRange, "detail")
	if l.lost[techcardarchive.EntityMarker].Skipped != 1 || len(l.holes) != 1 {
		t.Fatalf("counters = %+v, lines = %+v; the marker must leave the imported column", l.lost, l.holes)
	}
	if act := techcardarchive.ActionFor(techcardarchive.ReasonSizeNotInCardRange); strings.Contains(act, "to the dictionary") {
		t.Errorf("the action for a size the CARD refused sends the operator to the dictionary: %q", act)
	}
}
