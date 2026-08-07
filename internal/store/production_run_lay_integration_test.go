package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// НАСТИЛЫ end to end (Ф4, migrations 0281/0282). These are the acceptance probes of the phase's
// spec §13, and each one exists because the repository has already paid for the lesson:
//
//	(a) §13.6 — the section diff: a section whose ply count changed keeps its id, one swapped with
//	    its neighbour keeps its id, one the payload omits disappears. Ф5б will hang the consumption
//	    fact and the cutting receipt off those ids, and a full replace would re-mint them on every
//	    save, including a save that only touched the note (the 0230 lesson, verbatim);
//	(b) §13.5 — THE 0119 REGRESSION: a lay written by a direct API call survives a full save of the
//	    run from the UI, untouched and at the same version. production_run_marker died because the
//	    run's save full-replaced its children (0243:3-9); this is the structural proof it cannot
//	    happen again — UpdateProductionRun does not know lays exist;
//	(c) a byte-identical re-save must NOT 404. The driver counts rows CHANGED (no clientFoundRows in
//	    the DSN), so an existence answer derived from RowsAffected would report a phantom 404 —
//	    the trap named in techcard/markers.go;
//	(d) §4.4 — two tabs: a stale expected version conflicts, and so does NO version at all. Presence,
//	    not magnitude: the run's own 0-means-skip contract (0120) left the first edit of every run
//	    unguarded, and a new object does not inherit that;
//	(e) §13.8 — deleting a run takes its lays with it and does not fail. Every FK in the lay tree is
//	    CASCADE precisely because DeleteProductionRun is not transactional and relies on them;
//	(f) §13.2 — the stale badge: a note-only save must NOT launder it, an explicit reaffirmation must
//	    clear it;
//	(g) §13.4/§13.11 — an auxiliary card has no lay plan at all, and a face-to-face lay refuses an
//	    odd ply count at save time.
func TestProductionRunLays(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	PR := s.ProductionRuns()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }

	var szA int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&szA))
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	var codes []string
	rows, err := testDB.QueryContext(ctx, "SELECT code FROM color ORDER BY code LIMIT 2")
	require.NoError(t, err)
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		codes = append(codes, c)
	}
	require.NoError(t, rows.Err())
	rows.Close()
	require.Len(t, codes, 2, "the colour dictionary must carry at least two codes")

	fabric := entity.TechCardBomItem{
		LineKey: "01LAYFABRIC00000000000MAIN", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	lining := entity.TechCardBomItem{
		LineKey: "01LAYLINING00000000000LINE", Section: entity.BomSectionLining, Name: "Подкладка",
		FabricDirection: ns("any"),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Lay Style", Stage: entity.TechCardStageProto, StyleNumber: ns("LAY-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA},
		BomItems: []entity.TechCardBomItem{fabric, lining},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	newColorway := func(cardID int, code, sku string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
			VALUES (?, ?, ?, '#000000', 'US', ?, ?, 1)`, sku, code, code, mediaID, cardID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id)
		})
		return int(id)
	}
	cwA := newColorway(tcID, codes[0], "LAY-CW-A")
	cwB := newColorway(tcID, codes[1], "LAY-CW-B")

	bomItemID := func(lineKey string) int {
		var id int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?", tcID, lineKey).Scan(&id))
		return id
	}
	fabricSlot := bomItemID(fabric.LineKey)
	liningSlot := bomItemID(lining.LineKey)

	// РАСКРОЙНЫЕ МАРКЕРЫ are inserted directly because these probes are about the LAY, not about how
	// a раскладка got its owner — that path has its own acceptance test (TestMarkerRunOwnership), and
	// routing the fixtures through it would make one failure look like two. run_id NULL is the card
	// marker — the one a section may never point at.
	newMarker := func(name string, runID, slot int, colorway int) int {
		var runVal any
		if runID > 0 {
			runVal = runID
		}
		res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_marker
			(tech_card_id, run_id, bom_item_id, colorway_id, size_id, name, source, fabric_width_cm,
			 used_length_cm, total_units, placed_count, total_count, layout)
			VALUES (?, ?, ?, ?, NULL, ?, 'auto', 140.00, 900.00, 2, 4, 4, '{}')`,
			tcID, runVal, slot, colorway, name)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}

	newRun := func(lines []entity.ProductionRunLine) int {
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress, Lines: lines,
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id)
		})
		return id
	}

	runID := newRun([]entity.ProductionRunLine{
		{LineKey: "01LAYRUNLINE0000000000CWA1", ProductId: ni(cwA), SizeId: szA, PlannedQty: 20},
	})
	mainMarker := newMarker("основная 40-42", runID, fabricSlot, cwA)
	altMarker := newMarker("основная добор", runID, fabricSlot, cwA)
	liningMarker := newMarker("подкладка 40-42", runID, liningSlot, cwA)
	cardMarker := newMarker("норма основной", 0, fabricSlot, cwA)

	const (
		layKey  = "01LAYAAAAAAAAAAAAAAAAAAAAA"
		secKeyA = "01LAYSECAAAAAAAAAAAAAAAAAA"
		secKeyB = "01LAYSECBBBBBBBBBBBBBBBBBB"
	)
	baseLay := func(sections ...entity.ProductionRunLaySectionInsert) entity.ProductionRunLayInsert {
		return entity.ProductionRunLayInsert{
			LayKey: layKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, EndLossCm: d("2"), Name: "настил 1",
			Sections: sections,
		}
	}
	sectionIDs := func(lay entity.ProductionRunLay) map[string]int {
		out := make(map[string]int, len(lay.Sections))
		for _, sec := range lay.Sections {
			out[sec.SectionKey] = sec.Id
		}
		return out
	}

	// ---- create -------------------------------------------------------------------------------
	created, err := PR.SaveLay(ctx, runID, baseLay(
		entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 24, Position: 0},
		entity.ProductionRunLaySectionInsert{SectionKey: secKeyB, MarkerId: altMarker, Plies: 12, Position: 1},
	), entity.NoLockVersion(), false, "tester")
	require.NoError(t, err, "creating a lay does not need an expected version — there is nothing to overwrite")
	require.Equal(t, 0, created.LockVersion, "a fresh lay is born at version 0")
	require.Len(t, created.Sections, 2)
	require.Equal(t, fabricSlot, int(created.BomItemId.Int64))
	require.False(t, created.Broken())
	require.Equal(t, []entity.ProductionRunLayQtyEntry{{SizeId: szA, Qty: 20}}, created.QtySnapshot,
		"the server snapshots ITS OWN run lines; the payload has no say in it")
	require.False(t, created.QuantitiesStale)
	firstIDs := sectionIDs(created)

	runBefore, err := PR.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	runVersionBeforeLays := runBefore.LockVersion

	// ---- (c) a byte-identical re-save is NOT a 404 --------------------------------------------
	same, err := PR.SaveLay(ctx, runID, baseLay(
		entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 24, Position: 0},
		entity.ProductionRunLaySectionInsert{SectionKey: secKeyB, MarkerId: altMarker, Plies: 12, Position: 1},
	), entity.LockVersion(0), false, "tester")
	require.NoError(t, err, "a byte-identical re-save must not 404: the driver counts rows CHANGED, not matched")
	require.Equal(t, 1, same.LockVersion, "every save bumps the version, changed columns or not")
	require.Equal(t, firstIDs, sectionIDs(same), "an identical payload re-mints nothing")

	// ---- (a) §13.6 the section diff -------------------------------------------------------------
	t.Run("plies edit keeps the section id", func(t *testing.T) {
		edited, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 0},
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyB, MarkerId: altMarker, Plies: 12, Position: 1},
		), entity.LockVersion(1), false, "tester")
		require.NoError(t, err)
		require.Equal(t, firstIDs, sectionIDs(edited),
			"changing the ply count is the commonest edit there is; it must not re-mint the row Ф5б points at")
		require.Equal(t, 30, edited.Sections[0].Plies)
	})

	t.Run("reordering keeps both section ids", func(t *testing.T) {
		swapped, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyB, MarkerId: altMarker, Plies: 12, Position: 0},
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 1},
		), entity.LockVersion(2), false, "tester")
		require.NoError(t, err)
		require.Equal(t, firstIDs, sectionIDs(swapped), "position is an attribute, not an identity")
		require.Equal(t, secKeyB, swapped.Sections[0].SectionKey, "the list comes back in the stored order")
		require.Equal(t, 42, swapped.TotalPlies())
	})

	t.Run("an omitted section disappears", func(t *testing.T) {
		trimmed, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 0},
		), entity.LockVersion(3), false, "tester")
		require.NoError(t, err)
		require.Len(t, trimmed.Sections, 1, "inside one lay the submitted list IS the lay")
		require.Equal(t, firstIDs[secKeyA], trimmed.Sections[0].Id, "the survivor keeps its id")

		var gone int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay_section WHERE id = ?", firstIDs[secKeyB]).Scan(&gone))
		require.Equal(t, 0, gone)
	})

	// ---- (d) §4.4 concurrent editing -----------------------------------------------------------
	t.Run("a stale expected version conflicts", func(t *testing.T) {
		_, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 0},
		), entity.LockVersion(1), false, "tester")
		require.ErrorIs(t, err, entity.ErrProductionRunLayConflict)
	})

	t.Run("no expected version at all conflicts too", func(t *testing.T) {
		// This is the difference from the run, and it is deliberate: the run treats an absent token
		// as the legacy last-write-wins opt-out (0120), which left the FIRST edit of every run
		// unguarded. A lay has no legacy clients, so it starts closed.
		_, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 0},
		), entity.NoLockVersion(), false, "tester")
		require.ErrorIs(t, err, entity.ErrProductionRunLayConflict)

		lays, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, lays.Lays, 1)
		require.Equal(t, 4, lays.Lays[0].LockVersion, "a refused save changes nothing, version included")
	})

	// ---- section scope: only THIS run's markers, on THIS slot ----------------------------------
	//
	// Every refusal below is checked by FIELD AND REASON, not merely by "an error came back". These
	// saves all carry the CORRECT expected version, so a bare require.Error would also be satisfied
	// by a lock conflict — that is, by the test passing for the opposite reason.
	refusal := func(t *testing.T, err error, field, reason string) {
		t.Helper()
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, field, ve.Field)
		require.Equal(t, reason, ve.Reason)
	}

	t.Run("a card marker can never be a section", func(t *testing.T) {
		_, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: cardMarker, Plies: 2, Position: 0},
		), entity.LockVersion(4), false, "tester")
		refusal(t, err, "lay.sections[0].marker_id", "not_a_run_marker")
	})

	t.Run("a marker of another slot is refused", func(t *testing.T) {
		_, err := PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: liningMarker, Plies: 2, Position: 0},
		), entity.LockVersion(4), false, "tester")
		refusal(t, err, "lay.sections[0].marker_id", "not_a_run_marker")
	})

	t.Run("an odd ply count is refused in face to face", func(t *testing.T) {
		lay := baseLay(entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 7, Position: 0})
		lay.Mode = entity.ProductionLayModeFaceToFace
		_, err := PR.SaveLay(ctx, runID, lay, entity.LockVersion(4), false, "tester")
		refusal(t, err, "lay.sections[0].plies", "odd_plies_in_face_to_face")
	})

	t.Run("a colourway the run does not plan is refused", func(t *testing.T) {
		lay := baseLay(entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 2, Position: 0})
		lay.ColorwayId = cwB
		_, err := PR.SaveLay(ctx, runID, lay, entity.LockVersion(4), false, "tester")
		refusal(t, err, "lay.colorway_id", "not_planned_by_run")
	})

	t.Run("a refused save leaves the stored lay exactly as it was", func(t *testing.T) {
		list, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list.Lays, 1)
		require.Equal(t, 4, list.Lays[0].LockVersion, "four refusals in a row moved nothing")
		require.Len(t, list.Lays[0].Sections, 1)
		require.Equal(t, mainMarker, list.Lays[0].Sections[0].MarkerId)
		require.Equal(t, 30, list.Lays[0].Sections[0].Plies)
		require.Equal(t, entity.ProductionLayModeFaceUp, list.Lays[0].Mode,
			"the face-to-face attempt was refused as a whole; the stored mode is untouched")
	})

	// ---- (f) §13.2 the stale badge --------------------------------------------------------------
	t.Run("quantities go stale and only a reaffirmation clears them", func(t *testing.T) {
		require.NoError(t, PR.UpdateProductionRunPreservingCosts(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{
				{LineKey: "01LAYRUNLINE0000000000CWA1", ProductId: ni(cwA), SizeId: szA, PlannedQty: 26},
			},
		}, entity.NoLockVersion()))

		lays, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.True(t, lays.Lays[0].QuantitiesStale, "the grid moved under the lay and the badge must say so")
		require.Equal(t, []entity.ProductionRunLayQtyEntry{{SizeId: szA, Qty: 26}}, lays.Lays[0].QtyCurrent)

		// A NOTE-ONLY save must not launder the badge.
		noteOnly := baseLay(entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 30, Position: 0})
		noteOnly.Note = ns("подложить кромку")
		afterNote, err := PR.SaveLay(ctx, runID, noteOnly, entity.LockVersion(4), false, "tester")
		require.NoError(t, err)
		require.True(t, afterNote.QuantitiesStale,
			"editing the note must not clear the badge — otherwise the signal washes itself clean")
		require.Equal(t, []entity.ProductionRunLayQtyEntry{{SizeId: szA, Qty: 20}}, afterNote.QtySnapshot)

		// The explicit reaffirmation is the only way out that does not touch a section.
		afterReaffirm, err := PR.SaveLay(ctx, runID, noteOnly, entity.LockVersion(5), true, "tester")
		require.NoError(t, err)
		require.False(t, afterReaffirm.QuantitiesStale)
		require.Equal(t, []entity.ProductionRunLayQtyEntry{{SizeId: szA, Qty: 26}}, afterReaffirm.QtySnapshot)
	})

	// ---- (b) §13.5 THE 0119 REGRESSION ----------------------------------------------------------
	t.Run("a lay survives a full save of the run", func(t *testing.T) {
		before, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, before.Lays, 1)
		layVersionBefore := before.Lays[0].LockVersion
		layIDBefore := before.Lays[0].Id
		sectionIDBefore := before.Lays[0].Sections[0].Id

		// The UI's own save: the whole grid, every line present, exactly as production-run-modal
		// sends it. This is the shape that erased production_run_marker on every save (0243).
		require.NoError(t, PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{
				{LineKey: "01LAYRUNLINE0000000000CWA1", ProductId: ni(cwA), SizeId: szA, PlannedQty: 26},
			},
		}, entity.NoLockVersion(), dto.CostingFx{}))

		after, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, after.Lays, 1, "saving the run must not touch a lay")
		require.Equal(t, layIDBefore, after.Lays[0].Id)
		require.Equal(t, layVersionBefore, after.Lays[0].LockVersion,
			"the run's save did not edit the lay, so the lay's version must not move")
		require.Equal(t, sectionIDBefore, after.Lays[0].Sections[0].Id)

		// And the mirror image: saving a LAY must not bump the RUN's version, or the operator's open
		// run form would 409 on a lay somebody else edited.
		run, err := PR.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		versionAfterRunSaves := run.LockVersion
		_, err = PR.SaveLay(ctx, runID, baseLay(
			entity.ProductionRunLaySectionInsert{SectionKey: secKeyA, MarkerId: mainMarker, Plies: 28, Position: 0},
		), entity.LockVersion(after.Lays[0].LockVersion), false, "tester")
		require.NoError(t, err)
		run, err = PR.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, versionAfterRunSaves, run.LockVersion, "a lay save is not a run save")
		require.GreaterOrEqual(t, run.LockVersion, runVersionBeforeLays)
	})

	// ---- DeleteLay ------------------------------------------------------------------------------
	t.Run("delete removes one lay and refuses the second time", func(t *testing.T) {
		const secondKey = "01LAYBBBBBBBBBBBBBBBBBBBBB"
		second := entity.ProductionRunLayInsert{
			LayKey: secondKey, ColorwayId: cwA, BomLineKey: lining.LineKey,
			Mode: entity.ProductionLayModeFaceUp, EndLossCm: d("1.5"), Name: "подкладка",
			Sections: []entity.ProductionRunLaySectionInsert{
				{SectionKey: "01LAYSECCCCCCCCCCCCCCCCCCC", MarkerId: liningMarker, Plies: 10, Position: 0},
			},
		}
		saved, err := PR.SaveLay(ctx, runID, second, entity.NoLockVersion(), false, "tester")
		require.NoError(t, err)

		list, err := PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list.Lays, 2, "two lays, one per slot")

		require.NoError(t, PR.DeleteLay(ctx, runID, secondKey))
		require.ErrorIs(t, PR.DeleteLay(ctx, runID, secondKey), entity.ErrProductionRunLayNotFound)

		var sections int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay_section WHERE lay_id = ?", saved.Id).Scan(&sections))
		require.Equal(t, 0, sections, "the sections cascade with their lay")

		list, err = PR.ListLays(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list.Lays, 1, "deleting one lay leaves the other alone")
	})

	// ---- terminal run ---------------------------------------------------------------------------
	t.Run("a cancelled run has no editable lay plan", func(t *testing.T) {
		cancelledRun := newRun([]entity.ProductionRunLine{
			{LineKey: "01LAYRUNLINE0000000000CANC", ProductId: ni(cwA), SizeId: szA, PlannedQty: 5},
		})
		marker := newMarker("отменённый прогон", cancelledRun, fabricSlot, cwA)
		lay := entity.ProductionRunLayInsert{
			LayKey: "01LAYCANCELLED000000000000", ColorwayId: cwA, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, Name: "до отмены",
			Sections: []entity.ProductionRunLaySectionInsert{
				{SectionKey: "01LAYSECCANCELLED000000000", MarkerId: marker, Plies: 4, Position: 0},
			},
		}
		_, err := PR.SaveLay(ctx, cancelledRun, lay, entity.NoLockVersion(), false, "tester")
		require.NoError(t, err)

		require.NoError(t, PR.UpdateProductionRunPreservingCosts(ctx, cancelledRun, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunCancelled,
			Lines: []entity.ProductionRunLine{
				{LineKey: "01LAYRUNLINE0000000000CANC", ProductId: ni(cwA), SizeId: szA, PlannedQty: 5},
			},
		}, entity.NoLockVersion()))

		_, err = PR.SaveLay(ctx, cancelledRun, lay, entity.LockVersion(0), false, "tester")
		require.ErrorIs(t, err, entity.ErrProductionRunLocked, "a lay is a plan, not history")
		require.ErrorIs(t, PR.DeleteLay(ctx, cancelledRun, lay.LayKey), entity.ErrProductionRunLocked)

		// ---- (e) §13.8 the run's delete carries its lays away ---------------------------------
		var layID int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM production_run_lay WHERE run_id = ?", cancelledRun).Scan(&layID))
		require.NoError(t, PR.DeleteProductionRun(ctx, cancelledRun),
			"every FK in the lay tree is CASCADE because DeleteProductionRun is not transactional")

		var lays, sections, markers int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay WHERE id = ?", layID).Scan(&lays))
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay_section WHERE lay_id = ?", layID).Scan(&sections))
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_marker WHERE id = ?", marker).Scan(&markers))
		require.Equal(t, 0, lays)
		require.Equal(t, 0, sections)
		require.Equal(t, 0, markers, "a run's раскладки die with the run")

		var cardMarkerAlive int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_marker WHERE id = ?", cardMarker).Scan(&cardMarkerAlive))
		require.Equal(t, 1, cardMarkerAlive, "the card's own раскладка is untouched by a run's death")
	})

	// ---- (g) §13.4 the aux path -----------------------------------------------------------------
	t.Run("an auxiliary card has no lay plan", func(t *testing.T) {
		auxID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Lay Aux", Stage: entity.TechCardStageProto, StyleNumber: ns("LAY-AUX-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose:  entity.TechCardPurposeAuxiliary,
			SizeIds:  []int{szA},
			BomItems: []entity.TechCardBomItem{fabric},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", auxID)
		})
		auxRun, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: auxID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{LineKey: "01LAYRUNLINE0000000000AUX1", SizeId: szA, PlannedQty: 3}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", auxRun)
		})

		list, err := PR.ListLays(ctx, auxRun)
		require.NoError(t, err)
		require.False(t, list.Applicable, "stated explicitly: an empty list would read as an invitation")
		require.Equal(t, entity.ProductionRunLayNotApplicableKey, list.NotApplicableReason)
		require.Empty(t, list.Lays)

		_, err = PR.SaveLay(ctx, auxRun, entity.ProductionRunLayInsert{
			ColorwayId: cwA, BomLineKey: fabric.LineKey, Mode: entity.ProductionLayModeFaceUp,
		}, entity.NoLockVersion(), false, "tester")
		require.ErrorIs(t, err, entity.ErrProductionRunLayNotApplicable)
	})

	// ---- a run that does not exist ---------------------------------------------------------------
	t.Run("an unknown run is sql.ErrNoRows on every path", func(t *testing.T) {
		_, err := PR.ListLays(ctx, 0)
		require.ErrorIs(t, err, sql.ErrNoRows)
		_, err = PR.SaveLay(ctx, 0, baseLay(), entity.NoLockVersion(), false, "tester")
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, PR.DeleteLay(ctx, 0, layKey), sql.ErrNoRows)
	})
}
