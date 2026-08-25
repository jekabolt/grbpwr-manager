package techcard

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
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
		{"component purposes", archiveImportComponentPurposeQuery, map[string]any{"ids": []int{7, 9}}, 2},
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

// The claim is the whole defence against a double commit: two clicks on «import» are two calls
// naming one import_id, and without `status = :uploaded` in the WHERE the second one would sail
// through and create a SECOND card from the same archive — silently, since nothing downstream
// looks for a twin.
func TestArchiveImportClaimIsGuardedByStatus(t *testing.T) {
	if !strings.Contains(archiveImportClaimQuery, "status = :uploaded") {
		t.Fatal("the claim must only ever move a row that is still `uploaded`")
	}
	if !strings.Contains(archiveImportClaimQuery, "import_id = :import_id") {
		t.Fatal("the claim must address exactly one import")
	}
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
