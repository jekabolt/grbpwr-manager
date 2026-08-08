package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЛОТ И ФАКТ РАСХОДА НА НАСТИЛЕ end to end (Ф5б.1/Ф5б.2, migration 0285). The acceptance probes of
// the phase's §6, and each one is a sentence the phase refused to leave to convention:
//
//	(1) §6.6 — a fact without a unit is REFUSED, and refused with the field named. The CHECK
//	    (chk_prlay_actual_complete) is the floor; what a cutting room reads is a sentence;
//	(2) §6.9 — deleting a lot leaves the настил alive and still able to NAME the roll it lost. That
//	    is what ON DELETE SET NULL is paid for with (Р6), and it is the same bargain bom_item_id
//	    struck in 0281;
//	(3) the badge: recording the fact must NOT recompute the quantity snapshot. The snapshot moves
//	    only on a section edit or an explicit reaffirmation — a настил whose run has been re-planned
//	    under it must keep saying so WHILE the cutting room types what really went;
//	(4) §4.2 — the plan/fact drift is COMPUTED (факт/план − 1) and is ABSENT on a настил without a
//	    fact, rather than zero. Zero would read as «план сошёлся»;
//	(5) silence protects the fact: a save that does not speak about the lot or the fact leaves both
//	    alone, and echoing an unchanged fact does not rewrite who measured it. Every client written
//	    before 0285 sends exactly such a payload, with a perfectly valid expected version.
func TestProductionRunLayLotAndFact(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

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
	MS := s.MaterialStock()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }
	nl := func(v int) sql.NullInt64 { return sql.NullInt64{Int64: int64(v), Valid: true} }
	dec := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	ndec := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

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

	// Two articles, so the настил can be offered a roll of the WRONG cloth on purpose.
	newMaterial := func(name string) int {
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{Name: name, Section: "fabric", Unit: ns("m")})
		require.NoError(t, err)
		t.Cleanup(func() {
			c := context.Background()
			_, _ = testDB.ExecContext(c, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(c, "DELETE FROM material_lot WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(c, "DELETE FROM material WHERE id = ?", id)
		})
		return id
	}
	matMain := newMaterial("F5B Lay Fabric")
	matOther := newMaterial("F5B Other Fabric")

	fabric := entity.TechCardBomItem{
		LineKey: "01F5BFABRIC0000000000MAIN0", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"), MaterialId: nl(matMain),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "F5B Lay Style", Stage: entity.TechCardStageProto, StyleNumber: ns("F5B-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES (?, ?, ?, '#000000', 'US', ?, ?, 1)`, "F5B-CW-A", colorCode, colorCode, mediaID, tcID)
	require.NoError(t, err)
	cwRaw, err := res.LastInsertId()
	require.NoError(t, err)
	cwA := int(cwRaw)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwA)
	})

	var fabricSlot int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?",
		tcID, fabric.LineKey).Scan(&fabricSlot))
	var slotMaterial sql.NullInt64
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT material_id FROM tech_card_bom_item WHERE id = ?", fabricSlot).Scan(&slotMaterial))
	require.True(t, slotMaterial.Valid,
		"this probe needs the slot linked to an article — otherwise the lot check has nothing to disprove and allows everything")

	runID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{LineKey: "01F5BRUNLINE00000000000CWA", ProductId: ni(cwA), SizeId: szA, PlannedQty: 20},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

	res, err = testDB.ExecContext(ctx, `INSERT INTO tech_card_marker
		(tech_card_id, run_id, bom_item_id, colorway_id, size_id, name, source, fabric_width_cm,
		 used_length_cm, total_units, placed_count, total_count, layout)
		VALUES (?, ?, ?, ?, NULL, 'основная 40-42', 'auto', 140.00, 900.00, 2, 4, 4, '{}')`,
		tcID, runID, fabricSlot, cwA)
	require.NoError(t, err)
	markerRaw, err := res.LastInsertId()
	require.NoError(t, err)
	markerID := int(markerRaw)

	// Рулоны. ROLL-A1 замерен (148 при номинале 150) и имеет оттенок — эти факты обязаны доехать до
	// настила джойном, потому что их спрашивает проверка ширины и заметка об оттенке.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matMain, Quantity: decimal.NewFromInt(300), Lot: ns("ROLL-A1"),
		MeasuredWidthCm: ndec("148"), ShadeCode: ns("SH-7"),
	})
	require.NoError(t, err)
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matOther, Quantity: decimal.NewFromInt(50), Lot: ns("ROLL-B1"),
	})
	require.NoError(t, err)
	lotOf := func(materialID int, code string) entity.MaterialLot {
		t.Helper()
		lots, err := MS.ListMaterialLots(ctx, materialID, false)
		require.NoError(t, err)
		for _, l := range lots {
			if l.LotCode == code {
				return l
			}
		}
		t.Fatalf("lot %q of material %d not found", code, materialID)
		return entity.MaterialLot{}
	}
	lotA := lotOf(matMain, "ROLL-A1")
	lotB := lotOf(matOther, "ROLL-B1")

	const (
		layKey = "01F5BLAYAAAAAAAAAAAAAAAAAA"
		secKey = "01F5BSECAAAAAAAAAAAAAAAAAA"
	)
	// План настила: 900 см × 10 слоёв + 2 × 2 см × 10 слоёв = 9040 см (Ф4, чистая геометрия — без
	// коэффициента раскроя, иначе калибровка стала бы круговой).
	plannedCm := dec("9040")
	baseLay := func() entity.ProductionRunLayInsert {
		return entity.ProductionRunLayInsert{
			LayKey: layKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, EndLossCm: dec("2"), Name: "настил 1",
			Sections: []entity.ProductionRunLaySectionInsert{
				{SectionKey: secKey, MarkerId: markerID, Plies: 10, Position: 0},
			},
		}
	}

	// ---- нет лота, нет факта -------------------------------------------------------------------
	lay, err := PR.SaveLay(ctx, runID, baseLay(), entity.NoLockVersion(), false, "planner")
	require.NoError(t, err)
	require.False(t, lay.LotId.Valid, "a lay is born without a roll")
	require.Empty(t, lay.LotCode, "and without a remembered code — «never named» and «lost» are different sentences")
	require.False(t, lay.HasActual(), "and without a fact")
	require.False(t, lay.LotDetached())

	drift := entity.ProductionRunLayDrift(plannedCm, lay)
	require.False(t, drift.Known, "a настил nobody measured has no drift")
	require.Equal(t, entity.LayDriftReasonNoActual, drift.Reason)
	require.True(t, drift.Drift.IsZero(),
		"the unread zero must stay unread: «0 %» would read as «план сошёлся», which nobody has earned")

	// ---- §6.6 факт без единицы отвергается ВНЯТНО ------------------------------------------------
	bad := baseLay()
	bad.Actual = &entity.ProductionRunLayActualInput{
		Qty: ndec("94.92"), Method: entity.ProductionLayActualMethodRollBeforeAfter,
	}
	_, err = PR.SaveLay(ctx, runID, bad, entity.LockVersion(lay.LockVersion), false, "cutter")
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve, "the refusal must be a typed field violation, not MySQL error 3819")
	require.Equal(t, "lay.actual_uom", ve.Field)
	require.Equal(t, "required", ve.Reason)
	require.Contains(t, ve.Message, "unit",
		"the sentence must say what is missing; a constraint name is not an answer a cutting room can act on")

	badMethod := baseLay()
	badMethod.Actual = &entity.ProductionRunLayActualInput{Qty: ndec("94.92"), Uom: entity.MaterialUnitM}
	_, err = PR.SaveLay(ctx, runID, badMethod, entity.LockVersion(lay.LockVersion), false, "cutter")
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "lay.actual_method", ve.Field)

	reread, err := PR.ListLays(ctx, runID)
	require.NoError(t, err)
	require.Len(t, reread.Lays, 1)
	require.False(t, reread.Lays[0].HasActual(), "a refused fact leaves nothing behind")
	require.Equal(t, lay.LockVersion, reread.Lays[0].LockVersion, "and does not bump the version")

	// ---- лот чужого артикула отвергается ---------------------------------------------------------
	wrongLot := baseLay()
	otherID := lotB.Id
	wrongLot.LotId = &otherID
	_, err = PR.SaveLay(ctx, runID, wrongLot, entity.LockVersion(lay.LockVersion), false, "cutter")
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "lay.lot_id", ve.Field)
	require.Equal(t, "lot_of_another_article", ve.Reason)

	// ---- бейдж: снимок количеств устарел ДО ввода факта ------------------------------------------
	// Прогон перепланировали под настилом: 20 → 33. Снимок настила остался на 20, и это ровно тот
	// сигнал, который факт не имеет права отмыть.
	_, err = testDB.ExecContext(ctx,
		"UPDATE production_run_line SET planned_qty = 33 WHERE run_id = ? AND product_id = ?", runID, cwA)
	require.NoError(t, err)
	stale, err := PR.ListLays(ctx, runID)
	require.NoError(t, err)
	require.True(t, stale.Lays[0].QuantitiesStale, "the badge must be up before the fact is typed")
	require.Len(t, stale.Lays[0].QtySnapshot, 1)
	require.Equal(t, 20, stale.Lays[0].QtySnapshot[0].Qty)

	// ---- лот и факт записываются ------------------------------------------------------------------
	withFact := baseLay()
	lotID := lotA.Id
	withFact.LotId = &lotID
	withFact.Actual = &entity.ProductionRunLayActualInput{
		Qty: ndec("94.920"), Uom: entity.MaterialUnitM,
		Method: entity.ProductionLayActualMethodRollBeforeAfter,
	}
	saved, err := PR.SaveLay(ctx, runID, withFact, entity.LockVersion(lay.LockVersion), false, "cutter")
	require.NoError(t, err)

	require.True(t, saved.LotId.Valid)
	require.Equal(t, int64(lotA.Id), saved.LotId.Int64)
	require.Equal(t, "ROLL-A1", saved.LotCode, "the code is snapshotted at bind time")
	require.True(t, saved.LotMeasuredWidthCm.Valid && saved.LotMeasuredWidthCm.Decimal.Equal(dec("148")),
		"the roll's MEASURED width rides along for the width check, got %v", saved.LotMeasuredWidthCm)
	require.Equal(t, "SH-7", saved.LotShadeCode.String)
	require.Equal(t, int64(matMain), saved.LotMaterialId.Int64)

	require.True(t, saved.HasActual())
	require.True(t, saved.ActualQty.Decimal.Equal(dec("94.92")), "got %v", saved.ActualQty)
	require.Equal(t, "m", saved.ActualUom.String)
	require.Equal(t, "roll_before_after", saved.ActualMethod.String)
	require.Equal(t, "cutter", saved.ActualBy, "the fact is signed by whoever measured it")
	require.True(t, saved.ActualAt.Valid, "and dated")
	measuredAt := saved.ActualAt.Time

	// §4.2 — дрейф считается, а не хранится: 9492 см факта против 9040 см плана.
	drift = entity.ProductionRunLayDrift(plannedCm, saved)
	require.True(t, drift.Known, "reason: %s", drift.Reason)
	require.True(t, drift.Drift.Equal(dec("0.05")), "drift = %s, want 0.05", drift.Drift)
	require.True(t, drift.PlannedInFactUnit.Equal(dec("90.4")),
		"the plan restated in the fact's unit = %s, want 90.4 m", drift.PlannedInFactUnit)

	// ---- БЕЙДЖ НЕ ОТМЫТ ---------------------------------------------------------------------------
	require.True(t, saved.QuantitiesStale,
		"typing what really went says NOTHING about whether the run still plans the quantities this "+
			"настил was built for — laundering the badge here would erase it at the exact moment it is being read")
	require.Len(t, saved.QtySnapshot, 1)
	require.Equal(t, 20, saved.QtySnapshot[0].Qty, "the snapshot is untouched by the fact")
	require.Len(t, saved.QtyCurrent, 1)
	require.Equal(t, 33, saved.QtyCurrent[0].Qty)

	// ---- молчание защищает факт --------------------------------------------------------------------
	// Ровно та полезная нагрузка, которую шлёт клиент, написанный до 0285: корректная версия, ни слова
	// о лоте и о факте.
	silent := baseLay()
	silent.Note = ns("клиент, который не знает про 0285")
	quiet, err := PR.SaveLay(ctx, runID, silent, entity.LockVersion(saved.LockVersion), false, "planner")
	require.NoError(t, err)
	require.Equal(t, saved.LockVersion+1, quiet.LockVersion, "the save did land")
	require.True(t, quiet.HasActual(), "a payload that does not mention the fact must not erase it")
	require.True(t, quiet.ActualQty.Decimal.Equal(dec("94.92")))
	require.Equal(t, "cutter", quiet.ActualBy)
	require.True(t, quiet.LotId.Valid, "nor erase the roll")
	require.Equal(t, "ROLL-A1", quiet.LotCode)
	require.True(t, quiet.QuantitiesStale, "and still not launder the badge")

	// ---- эхо неизменённого факта не переподписывает его ---------------------------------------------
	echo := baseLay()
	echo.LotId = &lotID
	echo.Actual = &entity.ProductionRunLayActualInput{
		Qty: ndec("94.9200"), Uom: entity.MaterialUnit("м"),
		Method: entity.ProductionLayActualMethodRollBeforeAfter,
	}
	echoed, err := PR.SaveLay(ctx, runID, echo, entity.LockVersion(quiet.LockVersion), false, "planner")
	require.NoError(t, err)
	require.Equal(t, "cutter", echoed.ActualBy,
		"«94.9200» is «94.92» and «м» is «m»: an unchanged measurement must keep the signature of the "+
			"person who took it, not gain that of the last person to save the настил")
	require.WithinDuration(t, measuredAt, echoed.ActualAt.Time, time.Second)

	// ---- единица, выбранная раньше количества, — половина формы, а не ложь --------------------------
	half := baseLay()
	half.Actual = &entity.ProductionRunLayActualInput{Uom: entity.MaterialUnitKg}
	halfSaved, err := PR.SaveLay(ctx, runID, half, entity.LockVersion(echoed.LockVersion), false, "cutter")
	require.NoError(t, err, "the implication is ONE-WAY (chk_prlay_actual_complete): a unit without a "+
		"quantity is a half-filled form")
	require.False(t, halfSaved.HasActual(), "and the fact is withdrawn with the quantity")
	require.Equal(t, "kg", halfSaved.ActualUom.String)
	require.Empty(t, halfSaved.ActualBy, "a signature must not outlive the number it was put under")
	require.False(t, halfSaved.ActualAt.Valid)

	drift = entity.ProductionRunLayDrift(plannedCm, halfSaved)
	require.False(t, drift.Known, "a withdrawn fact leaves no drift behind")
	require.Equal(t, entity.LayDriftReasonNoActual, drift.Reason)

	// Вернём факт, теперь взвешиванием: килограммы против плана в сантиметрах — величина, которую
	// нечем сравнить, и она ОТСУТСТВУЕТ, а не равна нулю.
	weighed := baseLay()
	weighed.LotId = &lotID
	weighed.Actual = &entity.ProductionRunLayActualInput{
		Qty: ndec("18.400"), Uom: entity.MaterialUnitKg, Method: entity.ProductionLayActualMethodWeighed,
	}
	weighedSaved, err := PR.SaveLay(ctx, runID, weighed, entity.LockVersion(halfSaved.LockVersion), false, "cutter")
	require.NoError(t, err)
	require.True(t, weighedSaved.HasActual())
	drift = entity.ProductionRunLayDrift(plannedCm, weighedSaved)
	require.False(t, drift.Known)
	require.Equal(t, entity.LayDriftReasonUnitNotLength, drift.Reason,
		"converting kilograms into the lay's centimetres needs the article's grammage and width — a "+
			"guess wearing a number's clothes")

	// ---- отвязка: настил больше не претендует на рулон ------------------------------------------------
	unbind := baseLay()
	zero := 0
	unbind.LotId = &zero
	unbound, err := PR.SaveLay(ctx, runID, unbind, entity.LockVersion(weighedSaved.LockVersion), false, "cutter")
	require.NoError(t, err)
	require.False(t, unbound.LotId.Valid)
	require.Empty(t, unbound.LotCode, "an unbound настил must stop naming a roll it no longer claims")
	require.False(t, unbound.LotDetached(), "which is NOT the same as having lost one")
	require.True(t, unbound.HasActual(), "unbinding the roll does not unmeasure the cloth")

	rebind := baseLay()
	rebind.LotId = &lotID
	rebound, err := PR.SaveLay(ctx, runID, rebind, entity.LockVersion(unbound.LockVersion), false, "cutter")
	require.NoError(t, err)
	require.Equal(t, "ROLL-A1", rebound.LotCode)

	// ---- §6.9 УДАЛЕНИЕ ЛОТА -----------------------------------------------------------------------
	_, err = testDB.ExecContext(ctx, "DELETE FROM material_stock_movement WHERE lot_id = ?", lotA.Id)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "DELETE FROM material_lot WHERE id = ?", lotA.Id)
	require.NoError(t, err, "RESTRICT here would mean a roll can NEVER be deleted once anything was laid off it")

	after, err := PR.ListLays(ctx, runID)
	require.NoError(t, err)
	require.Len(t, after.Lays, 1, "the настил survives its roll")
	orphan := after.Lays[0]
	require.False(t, orphan.LotId.Valid, "ON DELETE SET NULL fired")
	require.Equal(t, "ROLL-A1", orphan.LotCode,
		"and the snapshot is what SET NULL is paid for: the настил NAMES the roll that vanished instead of going quiet")
	require.True(t, orphan.LotDetached())
	require.False(t, orphan.LotMeasuredWidthCm.Valid,
		"the lot's own facts are UNKNOWN now, not zero — a width check must answer «нечем проверить», never «влезает»")
	require.False(t, orphan.LotShadeCode.Valid)
	require.True(t, orphan.HasActual(), "the measurement is the настил's own fact and outlives the roll")
	require.True(t, orphan.ActualQty.Decimal.Equal(dec("18.4")))
	require.Equal(t, "cutter", orphan.ActualBy)
	require.True(t, orphan.QuantitiesStale, "and the badge is still up, all the way through")
}
