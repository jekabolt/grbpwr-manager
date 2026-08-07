package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ВЛАДЕНИЕ МАРКЕРОМ (Ф4, migration 0282) — the acceptance probes of spec §13 that concern the WRITE
// path, the one the phase's other tests had to fake by INSERTing run_id by hand.
//
// Each of these can only be proved against a real schema, and each is a way the feature dies quietly:
//
//	(1) §13.7 — two runs of one card, each with a раскладка named «основная 40-42», both save. This
//	    is the failure named as «самая вероятная причина упасть на бете на второй день»: every
//	    раскладка with a состав has size_id NULL ⇒ size_key 0, so before run_key they all shared one
//	    uniqueness bucket, and the цех names markers by meaning, not by run;
//	(2) ownership is actually WRITTEN, and written only at create. A save that would move a раскладка
//	    between the card and a прогон is refused (решение Р2) — in BOTH directions, because both
//	    re-home who owns the row's life;
//	(3) §5.2 — a run's раскладка appears in none of the three CARD reads: the card's marker list, the
//	    list badge count, and the Ф1.8 direction report (решение Р6);
//	(4) §5.4 — deleting a раскладка a секция настила stands on is refused BY THE NAME OF THE НАСТИЛ;
//	(5) Ф3 — SetMarkerNorm on a run marker answers a sentence, not MySQL's ERROR 3819;
//	(6) §13.8 — the run's death takes its раскладки and настилы and leaves the card's НОРМА standing.
func TestMarkerRunOwnership(t *testing.T) {
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

	T := s.TechCards()
	PR := s.ProductionRuns()
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
	var colorCode string
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT code FROM color ORDER BY code LIMIT 1").Scan(&colorCode))

	// The cloth line carries NO направление on purpose: that is what puts this card in the Ф1.8
	// worklist, which is where probe (3) reads the two marker counts. A layout with neither a 180°
	// nor a mirror is saveable against unset cloth (ValidateMarkerFabricDirection answers before it
	// asks about direction), so the fixture stays about ownership.
	fabric := entity.TechCardBomItem{
		LineKey: "01OWNFABRIC00000000000MAIN", Section: entity.BomSectionFabric, Name: "Основная",
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Ownership Style", Stage: entity.TechCardStageProto, StyleNumber: ns("OWN-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	var cwA int
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES ('OWN-CW-A', ?, ?, '#000000', 'US', ?, ?, 1)`, colorCode, colorCode, mediaID, tcID)
	require.NoError(t, err)
	cwID, err := res.LastInsertId()
	require.NoError(t, err)
	cwA = int(cwID)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwA)
	})

	newRun := func(cardID int, lineKey string) int {
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{
				{LineKey: lineKey, ProductId: ni(cwA), SizeId: szA, PlannedQty: 20},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id)
		})
		return id
	}
	runA := newRun(tcID, "01OWNRUNLINE0000000000RUNA")
	runB := newRun(tcID, "01OWNRUNLINE0000000000RUNB")

	ins := func(name string, runID int) entity.TechCardMarkerInsert {
		m := entity.TechCardMarkerInsert{
			Name: name, Source: entity.MarkerSourceAuto,
			BomLineKey:      fabric.LineKey,
			ColorwayId:      cwA,
			ProductionRunId: runID,
			FabricWidthCm:   d("140"), GapCm: d("0.5"), EdgeMarginCm: d("1"),
			UsedLengthCm: d("900"),
			PlacedCount:  4, TotalCount: 4,
			Layout: markerLayoutV1, LayoutFacts: markerLayoutFacts(t, markerLayoutV1),
		}
		markerSizing(&m, szA, 2)
		return m
	}
	violation := func(t *testing.T, err error, field, reason string) {
		t.Helper()
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve, "the refusal must be a FIELD violation, not a raw driver error")
		require.Equal(t, field, ve.Field)
		require.Equal(t, reason, ve.Reason)
	}

	// ---- (1) §13.7 one name, two прогона, plus the card's own -----------------------------------
	const sameName = "основная 40-42"
	idA, err := T.SaveMarker(ctx, tcID, 0, ins(sameName, runA), "tester")
	require.NoError(t, err)
	idB, err := T.SaveMarker(ctx, tcID, 0, ins(sameName, runB), "tester")
	require.NoError(t, err,
		"the second прогон of the same model is the normal case, not a name collision (uniq_tcm_card_run_sizekey_name)")
	idCard, err := T.SaveMarker(ctx, tcID, 0, ins(sameName, 0), "tester")
	require.NoError(t, err, "a карточная раскладка lives in its own bucket (run_key 0)")
	require.NotEqual(t, idA, idB)
	require.NotEqual(t, idA, idCard)

	_, err = T.SaveMarker(ctx, tcID, 0, ins(sameName, runA), "tester")
	require.Error(t, err, "inside ONE прогон the name is still unique — run_key widened the bucket, it did not remove it")

	// ---- (2) ownership is written, and only at create -------------------------------------------
	markerA, err := T.GetMarker(ctx, idA)
	require.NoError(t, err)
	require.True(t, markerA.RunId.Valid, "SaveMarker must persist run_id — without it no marker can become раскройный")
	require.Equal(t, int64(runA), markerA.RunId.Int64)

	markerCard, err := T.GetMarker(ctx, idCard)
	require.NoError(t, err)
	require.False(t, markerCard.RunId.Valid, "no production_run_id means КАРТОЧНЫЙ, exactly as before Ф4")

	t.Run("a card marker cannot be adopted by a run", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, idCard, ins(sameName, runA), "tester")
		violation(t, err, "production_run_id", "immutable")

		still, err := T.GetMarker(ctx, idCard)
		require.NoError(t, err)
		require.False(t, still.RunId.Valid, "a refused save moves nothing")
	})

	t.Run("a run marker cannot be released to the card", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, idA, ins(sameName, 0), "tester")
		violation(t, err, "production_run_id", "immutable")

		still, err := T.GetMarker(ctx, idA)
		require.NoError(t, err)
		require.Equal(t, int64(runA), still.RunId.Int64)
	})

	t.Run("a run marker cannot be handed to another run", func(t *testing.T) {
		_, err := T.SaveMarker(ctx, tcID, idA, ins(sameName, runB), "tester")
		violation(t, err, "production_run_id", "immutable")
	})

	t.Run("re-saving with the SAME owner is an ordinary edit", func(t *testing.T) {
		saved, err := T.SaveMarker(ctx, tcID, idA, ins("основная 40-42 v2", runA), "editor")
		require.NoError(t, err, "ownership unchanged is not re-validated — that is what keeps a stored marker editable")
		require.Equal(t, idA, saved)
		got, err := T.GetMarker(ctx, idA)
		require.NoError(t, err)
		require.Equal(t, "основная 40-42 v2", got.Name)
		require.Equal(t, int64(runA), got.RunId.Int64)
	})

	t.Run("a run of another card is refused by name", func(t *testing.T) {
		otherCard, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Other Style", Stage: entity.TechCardStageProto, StyleNumber: ns("OWN-2"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			SizeIds:  []int{szA},
			BomItems: []entity.TechCardBomItem{fabric},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", otherCard)
		})
		otherRun, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: otherCard, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{LineKey: "01OWNRUNLINE0000000000OTHR", SizeId: szA, PlannedQty: 1}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", otherRun)
		})

		// fk_tcm_run alone would have accepted this: the run exists. It would have produced a row no
		// screen can reach — hidden from this card's list because it has an owner, never offered to
		// that run's настилы because they also match on tech_card_id.
		_, err = T.SaveMarker(ctx, tcID, 0, ins("чужой прогон", otherRun), "tester")
		violation(t, err, "production_run_id", "not_a_run_of_this_card")
	})

	// ---- (5) Ф3: норма and раскройный маркер never meet ------------------------------------------
	t.Run("SetMarkerNorm on a run marker refuses in words", func(t *testing.T) {
		_, err := T.SetMarkerNorm(ctx, idA, true, "tester")
		violation(t, err, "id", "run_marker_cannot_be_norm")
		require.NotContains(t, err.Error(), "3819",
			"chk_tcm_run_not_norm is the net; the sentence is the answer")

		// Clearing is refused too: reporting success would imply it could have been the norm.
		_, err = T.SetMarkerNorm(ctx, idA, false, "tester")
		violation(t, err, "id", "run_marker_cannot_be_norm")
	})

	previous, err := T.SetMarkerNorm(ctx, idCard, true, "tester")
	require.NoError(t, err, "the card's own раскладка is exactly what a норма is made of")
	require.Equal(t, 0, previous)

	// ---- (3) §5.2 the three card reads ------------------------------------------------------------
	t.Run("a run marker is in none of the card's reads", func(t *testing.T) {
		card, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		ids := make([]int, 0, len(card.Markers))
		for _, m := range card.Markers {
			ids = append(ids, m.Id)
		}
		require.Equal(t, []int{idCard}, ids,
			"GetTechCard.markers feeds the раскладки table, the costing band and the recipe suggestion")
		require.True(t, card.Markers[0].IsNorm)

		list, _, err := T.ListTechCards(ctx, 50, 0, entity.Descending, entity.TechCardListFilter{Name: "OWN-1"})
		require.NoError(t, err)
		var badge int
		for _, c := range list {
			if c.Id == tcID {
				badge = c.MarkerCount
			}
		}
		require.Equal(t, 1, badge,
			"the badge and the list must agree: a count that climbs with every прогон accuses the list of hiding rows")

		gaps, err := T.ListFabricDirectionGaps(ctx, tcID)
		require.NoError(t, err)
		require.Len(t, gaps, 1)
		require.Equal(t, 1, gaps[0].LinkedMarkerCount,
			"решение Р6: the кампания Д1 worklist counts КАРТОЧНЫЕ раскладки only")
		require.Len(t, gaps[0].Lines, 1)
		require.Equal(t, 1, gaps[0].Lines[0].BlockedMarkerCount)

		// The complement holds: what the card hides, the run's own read returns.
		runMarkers, err := T.ListRunMarkers(ctx, runA)
		require.NoError(t, err)
		require.Len(t, runMarkers, 1)
		require.Equal(t, idA, runMarkers[0].Id)
	})

	// ---- (4) §5.4 a раскладка a настил stands on --------------------------------------------------
	const (
		layKey  = "01OWNLAYAAAAAAAAAAAAAAAAAA"
		secKey  = "01OWNSECAAAAAAAAAAAAAAAAAA"
		layName = "BLACK · основная"
	)
	lay := entity.ProductionRunLayInsert{
		LayKey: layKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
		Mode: entity.ProductionLayModeFaceUp, EndLossCm: d("2"), Name: layName,
		Sections: []entity.ProductionRunLaySectionInsert{
			{SectionKey: secKey, MarkerId: idA, Plies: 20, Position: 0},
		},
	}
	savedLay, err := PR.SaveLay(ctx, runA, lay, entity.NoLockVersion(), false, "tester")
	require.NoError(t, err, "the marker SaveMarker wrote must be acceptable to a section — that is the whole point of run_id")
	require.Len(t, savedLay.Sections, 1)

	t.Run("deleting a busy marker refuses and NAMES the настил", func(t *testing.T) {
		err := T.DeleteMarker(ctx, idA)
		require.ErrorIs(t, err, entity.ErrMarkerUsedByLay)
		require.Contains(t, err.Error(), layName,
			"«нельзя» sends the operator hunting; the name sends them to the screen that can free it")

		var alive int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_marker WHERE id = ?", idA).Scan(&alive))
		require.Equal(t, 1, alive)
	})

	t.Run("a free marker still deletes", func(t *testing.T) {
		require.NoError(t, T.DeleteMarker(ctx, idB), "the guard must refuse the busy one, not markers in general")
	})

	// ---- (5b) ЗАМОРОЖЕННАЯ КАРТОЧКА И ГРАНИЦА СОБСТВЕННОСТИ -------------------------------------
	//
	// Прогоны запускают с РЕЛИЗНУТЫХ карточек — это нормальный, а не краевой случай. Гвард
	// «released ⇒ отказ» защищает СОДЕРЖИМОЕ карточки, на которое сослался релиз; раскройная
	// раскладка карточке не принадлежит вовсе (умирает с прогоном, нормой быть не может, из всех
	// карточных перечислений скрыта), и релиз на неё сослаться не мог. Без этого изъятия фаза
	// отказывала бы ровно на тех карточках, ради которых написана.
	t.Run("released card still takes and frees a run marker", func(t *testing.T) {
		_, err := testDB.ExecContext(ctx,
			"UPDATE tech_card SET approval_state = ? WHERE id = ?", string(entity.TechCardApprovalReleased), tcID)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(ctx,
				"UPDATE tech_card SET approval_state = ? WHERE id = ?", string(entity.TechCardApprovalDraft), tcID)
		})

		// Контроль: карточная раскладка на релизнутой карточке по-прежнему отказывается — изъятие
		// не должно было открыть дверь содержимому карточки.
		_, err = T.SaveMarker(ctx, tcID, 0, ins("карточная на релизе", 0), "tester")
		require.ErrorIs(t, err, entity.ErrTechCardReleased,
			"the exemption is for RUN markers only — card content on a released card must still refuse")

		idFrozen, err := T.SaveMarker(ctx, tcID, 0, ins("раскройная на релизе", runA), "tester")
		require.NoError(t, err,
			"прогоны запускают с релизнутых карточек: отказ здесь убил бы живой путь всей фазы")

		require.NoError(t, T.DeleteMarker(ctx, idFrozen),
			"иначе убрать лишнюю раскладку из плана раскроя можно было бы только удалив весь прогон")
	})

	// ---- (6) §13.8 the run's death ---------------------------------------------------------------
	t.Run("deleting the run takes its markers and lays and leaves the norm", func(t *testing.T) {
		require.NoError(t, PR.DeleteProductionRun(ctx, runA),
			"no RESTRICT may exist on this path: DeleteProductionRun is not transactional and lives on cascades")

		var markers, lays, sections int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM tech_card_marker WHERE id = ?", idA).Scan(&markers))
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay WHERE id = ?", savedLay.Id).Scan(&lays))
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay_section WHERE lay_id = ?", savedLay.Id).Scan(&sections))
		require.Equal(t, 0, markers, "«умирает с прогоном» is a property of the schema, not a convention")
		require.Equal(t, 0, lays)
		require.Equal(t, 0, sections)

		norm, err := T.GetMarker(ctx, idCard)
		require.NoError(t, err, "the НОРМА is a card asset; a прогон has no claim on it")
		require.True(t, norm.IsNorm)
		require.False(t, norm.RunId.Valid)

		card, err := T.GetTechCardById(ctx, tcID)
		require.NoError(t, err)
		require.Len(t, card.Markers, 1)
		require.Equal(t, idCard, card.Markers[0].Id)
	})
}
