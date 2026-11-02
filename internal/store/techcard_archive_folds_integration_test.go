package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

const (
	foldArchFirst  = "01FDARCH000000000000000001" // front, seq 1
	foldArchSecond = "01FDARCH000000000000000002" // front, seq 2
	foldArchBack   = "01FDARCH000000000000000003" // back, seq 1
	foldArchNoteA  = "стачать боковые"
	foldArchNoteB  = "разутюжить шов"
)

func foldArchRows() []entity.TechCardPieceFoldInput {
	row := func(key, piece string, seq int) entity.TechCardPieceFoldInput {
		return entity.TechCardPieceFoldInput{
			FoldKey: key, PieceLineKey: piece, Seq: seq,
			Kind: entity.TechCardPieceFoldInward, Source: entity.TechCardPieceFoldSourceOwner,
			Line:   []entity.TechCardSeamSample{{U: 0, V: 0.1}, {U: 1, V: 0.1}},
			FlipAt: entity.TechCardSeamSample{U: 0.5, V: 0.05},
			Note:   "fold " + key[len(key)-1:],
		}
	}
	return []entity.TechCardPieceFoldInput{
		row(foldArchFirst, rtPieceFront, 1),
		row(foldArchSecond, rtPieceFront, 2),
		row(foldArchBack, rtPieceBack, 1),
	}
}

// foldArchRig is the round-trip rig, cleaned up after. An import stamps content_hash on the media rows
// it mints, and their bytes live only in this rig's in-memory bucket. Left behind, a later rig in the
// same database recognises such a row by its hash, reuses it and finds no object:
// TestTechCardArchiveRoundtrip, which runs after this file, would read that as a hole in its export.
func foldArchRig(t *testing.T, ctx context.Context) *rtRig {
	t.Helper()
	rig := newRTRig(t, ctx)
	var newestBefore int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id), 0) FROM media").Scan(&newestBefore))
	t.Cleanup(func() {
		_, err := testDB.ExecContext(context.Background(),
			"UPDATE media SET content_hash = NULL WHERE id > ?", newestBefore)
		require.NoError(t, err)
	})
	return rig
}

// foldArchLink writes one step→fold link the way the seam archive tests do: the save path has its own test.
func foldArchLink(t *testing.T, ctx context.Context, opID int, key string, order int) {
	t.Helper()
	_, err := testDB.ExecContext(ctx,
		"INSERT INTO tech_card_operation_fold (operation_id, fold_key, display_order) VALUES (?, ?, ?)",
		opID, key, order)
	require.NoError(t, err)
}

// foldArchLinksByNote is every step's fold keys by its note; a step with no note goes under "".
func foldArchLinksByNote(card *entity.TechCard) map[string][]string {
	out := map[string][]string{}
	for _, op := range card.Operations {
		if len(op.FoldKeys) > 0 {
			out[op.Note.String] = append(out[op.Note.String], op.FoldKeys...)
		}
	}
	return out
}

func foldArchCounter(rep *pb_admin.TechCardImportReport) (imported, skipped int, ok bool) {
	for _, c := range rep.GetCounters() {
		if c.GetEntity() == techcardarchive.EntityFold {
			return int(c.GetImported()), int(c.GetSkipped()), true
		}
	}
	return 0, 0, false
}

// foldArchEditCard rewrites card.json as plain JSON, the way an old server or a hand edit would see it.
func foldArchEditCard(t *testing.T, archive []byte, edit func(card map[string]any)) []byte {
	t.Helper()
	return seamArchRewrite(t, archive, func(name string, raw []byte) ([]byte, bool) {
		if name != techcardarchive.FileCard {
			return raw, true
		}
		var card map[string]any
		require.NoError(t, json.Unmarshal(raw, &card))
		edit(card)
		out, err := json.Marshal(card)
		require.NoError(t, err)
		return out, true
	}, func(*techcardarchive.Manifest) {})
}

// foldArchOps is card.json's steps.
func foldArchOps(t *testing.T, card map[string]any) []map[string]any {
	t.Helper()
	insert, ok := card["techCard"].(map[string]any)
	require.True(t, ok, "card.json carries techCard")
	raw, ok := insert["operations"].([]any)
	require.True(t, ok, "card.json carries operations")
	out := make([]map[string]any, 0, len(raw))
	for _, o := range raw {
		out = append(out, o.(map[string]any))
	}
	return out
}

// TestTechCardArchiveCarriesFolds is the round trip of FORMAT.md §5.3c: the folds ride card.json, the
// format stays 1.3 and no file is added. The import writes the rows afresh (importer as the author, its
// own fingerprint, a stale row lands stale) and links the steps. Then the ways an archive may lack
// something, none of which fail the import: an archive with no fold_keys (folds with no step, flat), a
// fold on a piece the card does not have, an invalid row, and links that read in reverse.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardArchiveCarriesFolds(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := foldArchRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	_, written, err := T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: rtActor, Folds: foldArchRows(),
	})
	require.NoError(t, err)
	require.Equal(t, 3, written)
	// The piece's sources moved under the back fold: its stored fingerprint no longer matches.
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card_piece_fold SET source_fingerprint = 'moved' WHERE tech_card_id = ? AND fold_key = ?",
		fx.cardID, foldArchBack)
	require.NoError(t, err)

	opID := func(where string, args ...any) int {
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM tech_card_operation WHERE tech_card_id = ? AND "+where,
			append([]any{fx.cardID}, args...)...).Scan(&id), where)
		return id
	}
	foldArchLink(t, ctx, opID("note = ?", foldArchNoteA), foldArchFirst, 0)
	foldArchLink(t, ctx, opID("note = ?", foldArchNoteB), foldArchSecond, 0)
	foldArchLink(t, ctx, opID("note = ?", foldArchNoteB), foldArchBack, 1)

	src, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, src.PieceFolds, 3)
	wantLinks := map[string][]string{
		foldArchNoteA: {foldArchFirst},
		foldArchNoteB: {foldArchSecond, foldArchBack},
	}
	require.Equal(t, wantLinks, foldArchLinksByNote(src))

	// ── EXPORT: 1.3, no new file, the folds and the links in card.json ──
	archive, _ := rig.export(t, fx.cardID)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	require.Equal(t, "1.3", arch.Manifest.FormatVersion, "the folds do not bump the format")
	require.False(t, arch.Has("folds.json"), "the folds travel in card.json")
	cardJSON, err := arch.CardJSON()
	require.NoError(t, err)
	require.Len(t, cardJSON.GetPieceFolds(), 3)
	jsonLinks := 0
	for _, op := range cardJSON.GetTechCard().GetOperations() {
		jsonLinks += len(op.GetFoldKeys())
	}
	require.Equal(t, 3, jsonLinks, "the steps keep their fold_keys in card.json")

	// ── IMPORT: the same folds on the same pieces, the same links by the steps' own rows ──
	newID, rep := rig.importArchive(t, archive)
	got, err := T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Len(t, got.PieceFolds, 3)
	byKey := map[string]entity.TechCardPieceFold{}
	for _, f := range got.PieceFolds {
		byKey[f.FoldKey] = f
	}
	for _, want := range src.PieceFolds {
		g, ok := byKey[want.FoldKey]
		require.True(t, ok, "fold %s did not arrive", want.FoldKey)
		require.Equal(t, newID, g.TechCardId)
		require.Equal(t, want.PieceLineKey, g.PieceLineKey)
		require.Equal(t, want.Seq, g.Seq)
		require.Equal(t, want.Kind, g.Kind)
		require.Equal(t, want.Source, g.Source)
		require.Equal(t, want.Note, g.Note)
		foldRTSamePoints(t, want.Line, g.Line)
		require.Equal(t, rtImporter, g.CreatedBy, "the importer writes the folds")
		if want.FoldKey != foldArchBack {
			require.Len(t, g.SourceFingerprint, 64, "the import computes its own fingerprint")
		}
	}
	require.False(t, byKey[foldArchFirst].Stale)
	require.False(t, byKey[foldArchSecond].Stale)
	require.True(t, byKey[foldArchBack].Stale, "a fold the source read as stale lands stale")
	require.Equal(t, wantLinks, foldArchLinksByNote(got))
	imported, skipped, ok := foldArchCounter(rep)
	require.True(t, ok, "a card that carries folds has a fold counter")
	require.Equal(t, 3, imported)
	require.Zero(t, skipped)

	// ── AN ARCHIVE WITH NO fold_keys brings the folds with no step: flat ──
	zipFlat := foldArchEditCard(t, archive, func(card map[string]any) {
		for _, op := range foldArchOps(t, card) {
			delete(op, "foldKeys")
		}
	})
	flatID, _ := rig.importArchive(t, zipFlat)
	flat, err := T.GetTechCardById(ctx, flatID)
	require.NoError(t, err)
	require.Len(t, flat.PieceFolds, 3, "the folds arrive")
	require.Empty(t, foldArchLinksByNote(flat), "no step closes them")

	// ── A FOLD ON A PIECE THE CARD DOES NOT HAVE, AND AN INVALID ROW: dropped, reported, the rest land ──
	const keyOrphan = "01FDARCH000000000000000004"
	const keyBroken = "01FDARCH000000000000000005"
	zipHoles := foldArchEditCard(t, archive, func(card map[string]any) {
		rows, ok := card["pieceFolds"].([]any)
		require.True(t, ok)
		orphan := map[string]any{}
		for k, v := range rows[0].(map[string]any) {
			orphan[k] = v
		}
		orphan["foldKey"] = keyOrphan
		orphan["pieceLineKey"] = "01RTPIECEGONE00000000000P9"
		broken := map[string]any{}
		for k, v := range rows[0].(map[string]any) {
			broken[k] = v
		}
		broken["foldKey"] = keyBroken
		broken["seq"] = 7
		delete(broken, "kind")
		card["pieceFolds"] = append(rows, orphan, broken)
		ops := foldArchOps(t, card)
		ops[0]["foldKeys"] = append(asAnyList(ops[0]["foldKeys"]), keyOrphan)
	})
	holesID, repHoles := rig.importArchive(t, zipHoles)
	require.Positive(t, holesID, "a bad fold never fails the import")
	rtRequireReportReason(t, repHoles, techcardarchive.EntityFold, "fold_key="+keyOrphan,
		string(techcardarchive.ReasonFoldPieceMissing))
	rtRequireReportReason(t, repHoles, techcardarchive.EntityFold, "fold_key="+keyBroken,
		string(techcardarchive.ReasonArchiveRowInvalid))
	imported, skipped, ok = foldArchCounter(repHoles)
	require.True(t, ok)
	require.Equal(t, 3, imported)
	require.Equal(t, 2, skipped)
	holes, err := T.GetTechCardById(ctx, holesID)
	require.NoError(t, err)
	require.Len(t, holes.PieceFolds, 3)
	require.Equal(t, wantLinks, foldArchLinksByNote(holes), "the dropped fold's link went with it")

	// ── LINKS THAT READ IN REVERSE: only the offending link goes, with a line ──
	stepOf := map[string]int{}
	zipReverse := foldArchEditCard(t, archive, func(card map[string]any) {
		for i, op := range foldArchOps(t, card) {
			note, _ := op["note"].(string)
			stepOf[note] = i
			switch note {
			case foldArchNoteA:
				op["foldKeys"] = []any{foldArchSecond}
			case foldArchNoteB:
				op["foldKeys"] = []any{foldArchFirst, foldArchBack}
			}
		}
	})
	require.Less(t, stepOf[foldArchNoteA], stepOf[foldArchNoteB], "the machine step sews first")
	reverseID, repReverse := rig.importArchive(t, zipReverse)
	rtRequireReportReason(t, repReverse, techcardarchive.EntityFold,
		"fold_key="+foldArchFirst+" step="+itoaOne(stepOf[foldArchNoteB]),
		string(techcardarchive.ReasonFoldStepConflict))
	reverse, err := T.GetTechCardById(ctx, reverseID)
	require.NoError(t, err)
	require.Len(t, reverse.PieceFolds, 3, "only a link is lost")
	require.Equal(t, map[string][]string{foldArchNoteA: {foldArchSecond}, foldArchNoteB: {foldArchBack}},
		foldArchLinksByNote(reverse))
}

// TestTechCardArchiveFoldLinksFollowTheirStep mirrors TestTechCardArchiveSeamLinksFollowTheirStep: a link
// follows its OWN step through the FUSING reconciliation the import runs after the steps land, never
// the step that now sits at its old position, and a link whose step did not survive goes with a line.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardArchiveFoldLinksFollowTheirStep(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := foldArchRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	_, _, err := T.UpsertTechCardPieceFolds(ctx, entity.TechCardPieceFoldsWrite{
		TechCardId: fx.cardID, By: rtActor, Folds: foldArchRows(),
	})
	require.NoError(t, err)

	opID := func(where string, args ...any) int {
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM tech_card_operation WHERE tech_card_id = ? AND "+where,
			append([]any{fx.cardID}, args...)...).Scan(&id), where)
		return id
	}
	frontFusing := opID("auto_fusing_piece_key = ?", rtPieceFront)
	backFusing := opID("auto_fusing_piece_key = ?", rtPieceBack)
	machine := opID("note = ?", foldArchNoteA)
	press := opID("note = ?", foldArchNoteB)

	// The shape of TestTechCardArchiveSeamLinksFollowTheirStep: no front fusing step (the import inserts
	// a fresh one first and every step shifts), a back fusing step the reconciliation drops.
	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_operation WHERE id = ?", frontFusing)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_operation_bom (operation_id, bom_item_id, display_order)
		SELECT ?, b.id, 9 FROM tech_card_bom_item b WHERE b.tech_card_id = ? AND b.line_key = ?`,
		backFusing, fx.cardID, rtLineShell)
	require.NoError(t, err)
	foldArchLink(t, ctx, backFusing, foldArchBack, 0)
	foldArchLink(t, ctx, machine, foldArchFirst, 0)
	foldArchLink(t, ctx, press, foldArchSecond, 0)

	archive, _ := rig.export(t, fx.cardID)
	newID, rep := rig.importArchive(t, archive)

	got, err := T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Len(t, got.PieceFolds, 3, "every fold imports; only a link is lost")
	require.Equal(t, entity.OpTypeFusing, got.Operations[0].OperationType,
		"the reconciliation put a fresh fusing step first: the archive's steps shifted")
	require.Equal(t, map[string][]string{foldArchNoteA: {foldArchFirst}, foldArchNoteB: {foldArchSecond}},
		foldArchLinksByNote(got), "each link on its own step, none on a fresh fusing step")
	rtRequireReportReason(t, rep, techcardarchive.EntityFold, "fold_key="+foldArchBack+" step=1",
		string(techcardarchive.ReasonFoldStepMissing))
}

// TestTechCardArchiveFoldlessCardIsUnchanged: a card with no folds says nothing new, neither on the way
// out nor in the import's report.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardArchiveFoldlessCardIsUnchanged(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := foldArchRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)

	archive, _ := rig.export(t, fx.cardID)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	raw, err := arch.ReadFile(techcardarchive.FileCard)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "pieceFolds")
	require.NotContains(t, string(raw), "foldKeys", "an empty list is not serialized")

	newID, rep := rig.importArchive(t, archive)
	_, _, ok := foldArchCounter(rep)
	require.False(t, ok, "no folds, no counter")
	got, err := rig.store.TechCards().GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Empty(t, got.PieceFolds)
	require.Empty(t, foldArchLinksByNote(got))
	var rows int
	require.NoError(t, testDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tech_card_operation_fold l
		JOIN tech_card_operation o ON o.id = l.operation_id WHERE o.tech_card_id = ?`, newID).Scan(&rows))
	require.Zero(t, rows)
}

func asAnyList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return nil
}

// itoaOne renders a zero-based step index the way a report line names it.
func itoaOne(i int) string { return strconv.Itoa(i + 1) }
