package store

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// TestTechCardArchiveCarriesSeams is the 1.3 round trip of the seam decisions (FORMAT.md §5.3b):
// a card with two seams (one confirmed, one rejected) and three step↔seam links is exported and
// imported, and the new card must hold the same seams — every field the reviewer decided — and
// the same links, by operation order. Then the two ways an archive may lack a seam: a seam whose
// piece is not on the card (dropped with a seam_piece_missing line, the import still succeeds),
// and a 1.2 archive with no seams.json (imports, no seams, no step links).
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardArchiveCarriesSeams(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	seams := seamArchSeams()
	_, written, err := T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: rtActor, Seams: seams,
	})
	require.NoError(t, err)
	require.Equal(t, 2, written)

	// Step links: the first step sews both seams (cuff first), the second sews the shoulder.
	var opIDs []int
	rows, err := testDB.QueryContext(ctx,
		"SELECT id FROM tech_card_operation WHERE tech_card_id = ? ORDER BY operation_number IS NULL, operation_number, display_order", fx.cardID)
	require.NoError(t, err)
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		opIDs = append(opIDs, id)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.GreaterOrEqual(t, len(opIDs), 2, "the fixture carries several steps")
	for _, l := range []struct {
		op    int
		key   string
		order int
	}{{opIDs[0], keyCuff, 0}, {opIDs[0], keyShoulder, 1}, {opIDs[1], keyShoulder, 0}} {
		_, err := testDB.ExecContext(ctx,
			"INSERT INTO tech_card_operation_seam (operation_id, seam_key, display_order) VALUES (?, ?, ?)",
			l.op, l.key, l.order)
		require.NoError(t, err)
	}

	src, err := T.GetTechCardById(ctx, fx.cardID)
	require.NoError(t, err)
	require.Len(t, src.Seams, 2)
	srcLinks := seamArchLinks(src)
	require.Equal(t, 3, seamArchLinkCount(srcLinks), "the source card reads its three links")

	// ── EXPORT 1.3 ──
	zip13, _ := rig.export(t, fx.cardID)
	arch, err := techcardarchive.OpenArchive(bytes.NewReader(zip13), int64(len(zip13)))
	require.NoError(t, err)
	require.Equal(t, techcardarchive.FormatVersion, arch.Manifest.FormatVersion)
	require.Equal(t, 2, arch.Manifest.Contents.Seams)
	require.True(t, arch.Has(techcardarchive.FileSeams))
	cardJSON, err := arch.CardJSON()
	require.NoError(t, err)
	require.Empty(t, cardJSON.GetSeams(), "the seams travel in seams.json, never twice")
	jsonLinks := 0
	for _, op := range cardJSON.GetTechCard().GetOperations() {
		jsonLinks += len(op.GetSeamKeys())
	}
	require.Equal(t, 3, jsonLinks, "the steps keep their seam_keys in card.json")

	// ── IMPORT: identical seams and links ──
	newID, rep := rig.importArchive(t, zip13)
	got, err := T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Len(t, got.Seams, 2)
	byKey := map[string]entity.TechCardSeam{}
	for _, s := range got.Seams {
		byKey[s.SeamKey] = s
	}
	for _, want := range src.Seams {
		g, ok := byKey[want.SeamKey]
		require.True(t, ok, "seam %s did not arrive", want.SeamKey)
		require.Equal(t, want.Status, g.Status)
		require.Equal(t, want.Kind, g.Kind)
		require.Equal(t, want.Direction, g.Direction)
		require.Equal(t, want.Source, g.Source)
		require.Equal(t, want.AnchoredSize, g.AnchoredSize)
		require.Equal(t, want.Note, g.Note)
		require.Equal(t, want.Stale, g.Stale)
		require.Equal(t, newID, g.TechCardId)
		require.Equal(t, rtImporter, g.CreatedBy, "the importer writes the seams")
		seamRTSameSide(t, want.SideA, g.SideA)
		seamRTSameSide(t, want.SideB, g.SideB)
		for i := range want.SideB {
			require.Equal(t, want.SideB[i].Placement, g.SideB[i].Placement, "hand placement travels")
		}
	}
	require.Equal(t, srcLinks, seamArchLinks(got), "step↔seam links by operation order")
	seamArchRequireCounter(t, rep, 2, 0)

	// ── ORPHAN: a seam on a piece the card does not have is dropped and reported ──
	const keyOrphan = "01SEAMARCH0000000000000003"
	orphan := seamsToArchiveForTest(seams[0])
	orphan.SeamKey = keyOrphan
	orphan.SideB[0].PieceLineKey = "01RTPIECEGONE00000000000P9"
	zipOrphan := seamArchRewrite(t, zip13, func(name string, raw []byte) ([]byte, bool) {
		if name != techcardarchive.FileSeams {
			return raw, true
		}
		var rows []techcardarchive.SeamPayload
		require.NoError(t, json.Unmarshal(raw, &rows))
		rows = append(rows, orphan)
		out, err := json.Marshal(rows)
		require.NoError(t, err)
		return out, true
	}, func(m *techcardarchive.Manifest) { m.Contents.Seams = 3 })
	orphanID, repOrphan := rig.importArchive(t, zipOrphan)
	require.Positive(t, orphanID, "an orphan seam never fails the import")
	rtRequireReportReason(t, repOrphan, techcardarchive.EntitySeam, "seam_key="+keyOrphan,
		string(techcardarchive.ReasonSeamPieceMissing))
	seamArchRequireCounter(t, repOrphan, 2, 1)
	gotOrphan, err := T.GetTechCardById(ctx, orphanID)
	require.NoError(t, err)
	require.Len(t, gotOrphan.Seams, 2)
	require.Equal(t, srcLinks, seamArchLinks(gotOrphan))

	// ── 1.2: no seams.json → the card imports with no seams and no step links ──
	zip12 := seamArchRewrite(t, zip13, func(name string, raw []byte) ([]byte, bool) {
		return raw, name != techcardarchive.FileSeams
	}, func(m *techcardarchive.Manifest) {
		m.FormatVersion = "1.2"
		m.Contents.Seams = 0
	})
	oldID, _ := rig.importArchive(t, zip12)
	gotOld, err := T.GetTechCardById(ctx, oldID)
	require.NoError(t, err)
	require.Empty(t, gotOld.Seams)
	require.Zero(t, seamArchLinkCount(seamArchLinks(gotOld)),
		"card.json still names the seams on its steps; with no seams.json they must not land")
}

// seamArchLinks is every step's seam keys, by operation order.
func seamArchLinks(card *entity.TechCard) [][]string {
	out := make([][]string, 0, len(card.Operations))
	for _, op := range card.Operations {
		out = append(out, append([]string{}, op.SeamKeys...))
	}
	return out
}

func seamArchLinkCount(links [][]string) int {
	n := 0
	for _, l := range links {
		n += len(l)
	}
	return n
}

func seamArchRequireCounter(t *testing.T, rep *pb_admin.TechCardImportReport, imported, skipped int) {
	t.Helper()
	for _, c := range rep.GetCounters() {
		if c.GetEntity() == techcardarchive.EntitySeam {
			require.EqualValues(t, imported, c.GetImported(), "seams imported")
			require.EqualValues(t, skipped, c.GetSkipped(), "seams skipped")
			return
		}
	}
	t.Fatalf("the report has no seam counter")
}

func seamsToArchiveForTest(s entity.TechCardSeamInput) techcardarchive.SeamPayload {
	side := func(in []entity.TechCardSeamAnchor) []techcardarchive.SeamAnchor {
		var out []techcardarchive.SeamAnchor
		for _, a := range in {
			var samples []techcardarchive.SeamSample
			for _, p := range a.Samples {
				samples = append(samples, techcardarchive.SeamSample{U: p.U, V: p.V})
			}
			out = append(out, techcardarchive.SeamAnchor{
				PieceLineKey: a.PieceLineKey, Samples: samples, PerimShare: a.PerimShare, LenMm: a.LenMm,
				Notches: a.Notches, TurnDeg: a.TurnDeg, EdgeHint: a.EdgeHint, ContourSig: a.ContourSig,
			})
		}
		return out
	}
	return techcardarchive.SeamPayload{
		SeamKey: s.SeamKey, Status: string(s.Status), Kind: string(s.Kind), Direction: string(s.Direction),
		Source: string(s.Source), SideA: side(s.SideA), SideB: side(s.SideB), Note: s.Note,
	}
}

// seamArchRewrite rebuilds the ZIP (a ZIP directory carries each entry's length and CRC), letting
// edit change or drop an entry and fix patch the manifest.
func seamArchRewrite(t *testing.T, archive []byte, edit func(name string, raw []byte) ([]byte, bool),
	fix func(*techcardarchive.Manifest)) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	require.NoError(t, err)
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	for _, f := range zr.File {
		rc, err := f.Open()
		require.NoError(t, err)
		raw, err := io.ReadAll(rc)
		require.NoError(t, rc.Close())
		require.NoError(t, err)
		if f.Name == techcardarchive.FileManifest {
			var m techcardarchive.Manifest
			require.NoError(t, json.Unmarshal(raw, &m))
			fix(&m)
			raw, err = json.Marshal(m)
			require.NoError(t, err)
		} else {
			var keep bool
			raw, keep = edit(f.Name, raw)
			if !keep {
				continue
			}
		}
		w, err := zw.Create(f.Name)
		require.NoError(t, err)
		_, err = w.Write(raw)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return out.Bytes()
}

const (
	keyShoulder = "01SEAMARCH0000000000000001"
	keyCuff     = "01SEAMARCH0000000000000002"
)

// seamArchSeams is the two seams both archive tests write: a confirmed shoulder FRONT↔BACK and a
// rejected BACK↔CUFF whose cuff side carries a hand placement.
func seamArchSeams() []entity.TechCardSeamInput {
	anchor := func(piece, hint string, share float64) entity.TechCardSeamAnchor {
		return entity.TechCardSeamAnchor{
			PieceLineKey: piece,
			Samples:      []entity.TechCardSeamSample{{U: 0, V: 0}, {U: 0.25, V: 0.1}, {U: 0.5, V: 0.2}, {U: 0.75, V: 0.3}, {U: 1, V: 0.4}},
			PerimShare:   share, LenMm: 142.5, Notches: 2, TurnDeg: -3.5,
			EdgeHint: hint, ContourSig: "abcdef0123456789",
		}
	}
	cuffSide := anchor(rtPieceCuff, "CUFF#1", 0.5)
	cuffSide.Placement = &entity.TechCardSeamPlacement{XMm: 12.5, YMm: -40, RotDeg: 90, Flip: true}
	return []entity.TechCardSeamInput{
		{
			SeamKey: keyShoulder, Status: entity.TechCardSeamConfirmed, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamReversed, Source: entity.TechCardSeamSourceGraph,
			SideA:        []entity.TechCardSeamAnchor{anchor(rtPieceFront, "FRONT#2", 0.125)},
			SideB:        []entity.TechCardSeamAnchor{anchor(rtPieceBack, "BACK#8", 0.125)},
			AnchoredSize: "M", Note: "shoulder",
		},
		{
			SeamKey: keyCuff, Status: entity.TechCardSeamRejected, Kind: entity.TechCardSeamKindEdge,
			Direction: entity.TechCardSeamSame, Source: entity.TechCardSeamSourceManual,
			SideA: []entity.TechCardSeamAnchor{anchor(rtPieceBack, "BACK#3", 0.25)},
			SideB: []entity.TechCardSeamAnchor{cuffSide},
			Note:  "not a seam",
		},
	}
}

// TestTechCardArchiveSeamLinksFollowTheirStep proves a step↔seam link follows its OWN step through
// the FUSING reconciliation the import runs after the steps land — never the step that now sits
// at its old position.
//
// The source is shaped the way the reconciliation reshapes an archive:
//   - the FRONT automatic fusing step is gone (a raw delete, so the source keeps no step for a fused
//     piece): the import inserts a fresh automatic step FIRST and every archive step shifts by one;
//   - the BACK automatic step is linked to the shell BOM line, so in the archive (where the private
//     auto identity does not travel) it is a manual fusing step the reconciliation drops and
//     replaces with a fresh automatic one.
//
// Links: BACK fusing → cuff; machine → cuff, shoulder; press → shoulder. Expected on the import:
// machine and press keep theirs on their own rows, the BACK link is dropped with a
// seam_step_missing line, the fresh automatic steps carry nothing.
//
// SAFE ONLY against a local container DSN or CI — see mysql_test.go.
func TestTechCardArchiveSeamLinksFollowTheirStep(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()
	rig := newRTRig(t, ctx)
	fx := rtBuildMaximalCard(t, rig)
	T := rig.store.TechCards()

	_, _, err := T.UpsertTechCardSeams(ctx, entity.TechCardSeamsWrite{
		TechCardId: fx.cardID, By: rtActor, Seams: seamArchSeams(),
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
	machine := opID("note = ?", "стачать боковые")
	press := opID("note = ?", "разутюжить шов")

	_, err = testDB.ExecContext(ctx, "DELETE FROM tech_card_operation WHERE id = ?", frontFusing)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `
		INSERT INTO tech_card_operation_bom (operation_id, bom_item_id, display_order)
		SELECT ?, b.id, 9 FROM tech_card_bom_item b WHERE b.tech_card_id = ? AND b.line_key = ?`,
		backFusing, fx.cardID, rtLineShell)
	require.NoError(t, err)
	for _, l := range []struct {
		op    int
		key   string
		order int
	}{{backFusing, keyCuff, 0}, {machine, keyCuff, 0}, {machine, keyShoulder, 1}, {press, keyShoulder, 0}} {
		_, err := testDB.ExecContext(ctx,
			"INSERT INTO tech_card_operation_seam (operation_id, seam_key, display_order) VALUES (?, ?, ?)",
			l.op, l.key, l.order)
		require.NoError(t, err)
	}

	zip, _ := rig.export(t, fx.cardID)
	newID, rep := rig.importArchive(t, zip)

	got, err := T.GetTechCardById(ctx, newID)
	require.NoError(t, err)
	require.Len(t, got.Seams, 2, "both seams import; only a link is lost")
	byNote := map[string][]string{}
	var fusing [][]string
	for _, op := range got.Operations {
		if op.OperationType == entity.OpTypeFusing {
			fusing = append(fusing, op.SeamKeys)
			continue
		}
		byNote[op.Note.String] = op.SeamKeys
	}
	require.Len(t, fusing, 2, "one automatic fusing step per fused piece after the reconciliation")
	require.Equal(t, entity.OpTypeFusing, got.Operations[0].OperationType,
		"the reconciliation put a fresh fusing step first — the archive's steps shifted")
	for i, keys := range fusing {
		require.Empty(t, keys, "fusing step %d is the reconciliation's own; no archive link may land on it", i)
	}
	require.Equal(t, []string{keyCuff, keyShoulder}, byNote["стачать боковые"], "machine keeps its links")
	require.Equal(t, []string{keyShoulder}, byNote["разутюжить шов"], "press keeps its link")
	for note, keys := range byNote {
		if note != "стачать боковые" && note != "разутюжить шов" {
			require.Empty(t, keys, "step %q sewed nothing on the source", note)
		}
	}
	rtRequireReportReason(t, rep, techcardarchive.EntitySeam, "seam_key="+keyCuff+" step=1",
		string(techcardarchive.ReasonSeamStepMissing))
}
