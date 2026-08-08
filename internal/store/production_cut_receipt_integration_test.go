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

// ПРИЁМКА КРОЯ end to end (Ф5б.5, migration 0287). These are the acceptance probes of §6 п.8 and
// п.10 of the phase spec, and every one of them exists because getting it wrong would be invisible:
//
//	(a) §6.8 — заказано 10, выкроено 12, принято 10 exist side by side, and received_qty IS NOT
//	    TOUCHED. Proved by re-reading production_run_line straight from the database rather than by
//	    reasoning about the code: the whole point of the separate table is that the finished-goods
//	    number keeps its meaning, and only the row itself can testify to that;
//	(b) overcutting is ACCEPTED, not refused. «Раскроено 12 при заказанных 10» is the cutting room
//	    working with a margin; a refusal would leave the operator unable to state what happened;
//	(c) the pair (настил, размер) is the identity: re-reporting it UPDATES the row and never opens a
//	    second one — the same id, and one row in the table;
//	(d) §6.10 — deleting the настил takes its receipts, and deleting the RUN takes everything and
//	    does not fail. Every FK on this path is CASCADE precisely because DeleteProductionRun is not
//	    transactional and relies on them;
//	(e) THE DECISION OF THE PHASE: a terminal run still accepts a receipt. A настил is refused there
//	    because it is a plan; a receipt is a report of what happened at the table, and cutting
//	    precedes sewing, which precedes receiving. The cancelled run is the sharpest case — its cut
//	    cloth is exactly the loss somebody has to account for;
//	(f) a size the RUN never planned is accepted (a fact about the table), while a size the STYLE
//	    does not grade is refused (no pattern, no раскладка — nothing that could have been cut);
//	(g) negative counts are refused by field and reason, mirroring chk_prcr_cut_qty /
//	    chk_prcr_accepted_qty so the operator is told which number is wrong instead of meeting
//	    MySQL error 3819.
func TestProductionRunCutReceipts(t *testing.T) {
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

	// Three sizes: two the style grades (one planned by the run, one not) and one it does not.
	var sizeIDs []int
	sizeRows, err := testDB.QueryContext(ctx, "SELECT id FROM size ORDER BY id LIMIT 3")
	require.NoError(t, err)
	for sizeRows.Next() {
		var id int
		require.NoError(t, sizeRows.Scan(&id))
		sizeIDs = append(sizeIDs, id)
	}
	require.NoError(t, sizeRows.Err())
	sizeRows.Close()
	require.Len(t, sizeIDs, 3, "the size dictionary must carry at least three sizes")
	szA, szB, szUngraded := sizeIDs[0], sizeIDs[1], sizeIDs[2]

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	var colorCode string
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT code FROM color ORDER BY code LIMIT 1").Scan(&colorCode))

	fabric := entity.TechCardBomItem{
		LineKey: "01CUTFABRIC00000000000MAIN", Section: entity.BomSectionFabric, Name: "Основная",
		FabricDirection: ns("any"),
	}
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Cut Receipt Style", Stage: entity.TechCardStageProto, StyleNumber: ns("CUT-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		SizeIds:  []int{szA, szB},
		BomItems: []entity.TechCardBomItem{fabric},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES (?, ?, ?, '#000000', 'US', ?, ?, 1)`, "CUT-CW-A", colorCode, colorCode, mediaID, tcID)
	require.NoError(t, err)
	cwRaw, err := res.LastInsertId()
	require.NoError(t, err)
	cwA := int(cwRaw)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwA)
	})

	var fabricSlot int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM tech_card_bom_item WHERE tech_card_id = ? AND line_key = ?", tcID, fabric.LineKey).Scan(&fabricSlot))

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

	// The раскладка is inserted directly: these probes are about the RECEIPT, and routing the fixture
	// through the marker path would make one failure look like two (TestMarkerRunOwnership owns that).
	newMarker := func(runID int) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card_marker
			(tech_card_id, run_id, bom_item_id, colorway_id, size_id, name, source, fabric_width_cm,
			 used_length_cm, total_units, placed_count, total_count, layout)
			VALUES (?, ?, ?, ?, NULL, 'приёмка кроя', 'auto', 140.00, 900.00, 2, 4, 4, '{}')`,
			tcID, runID, fabricSlot, cwA)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}

	// The run plans TEN of size A for colourway A. Size B is graded by the style but absent from the
	// run's grid on purpose — probe (f) rests on that.
	runID := newRun([]entity.ProductionRunLine{
		{LineKey: "01CUTRUNLINE0000000000CWA1", ProductId: ni(cwA), SizeId: szA, PlannedQty: 10},
	})
	marker := newMarker(runID)

	const (
		layKey = "01CUTLAYAAAAAAAAAAAAAAAAAA"
		secKey = "01CUTSECAAAAAAAAAAAAAAAAAA"
	)
	lay, err := PR.SaveLay(ctx, runID, entity.ProductionRunLayInsert{
		LayKey: layKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
		Mode: entity.ProductionLayModeFaceUp, EndLossCm: d("2"), Name: "настил 1",
		Sections: []entity.ProductionRunLaySectionInsert{
			{SectionKey: secKey, MarkerId: marker, Plies: 12, Position: 0},
		},
	}, entity.NoLockVersion(), false, "tester")
	require.NoError(t, err)

	// СДАНО ГОТОВЫМ И БРАК СТАВЯТСЯ ДО ПРИЁМКИ КРОЯ, прямым запросом: the point of probe (a) is that
	// the receipt path leaves them exactly where it found them, so they have to be non-zero and
	// non-NULL BEFORE it runs. Written straight to the row rather than through UpdateProductionRun so
	// nothing but the receipt write can be blamed for a change.
	_, err = testDB.ExecContext(ctx,
		`UPDATE production_run_line SET received_qty = 7, defect_qty = 1
		 WHERE run_id = ? AND product_id = ? AND size_id = ?`, runID, cwA, szA)
	require.NoError(t, err)

	runLineNumbers := func(t *testing.T) (planned int, received, defect sql.NullInt64) {
		t.Helper()
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT planned_qty, received_qty, defect_qty FROM production_run_line
			 WHERE run_id = ? AND product_id = ? AND size_id = ?`, runID, cwA, szA).
			Scan(&planned, &received, &defect))
		return planned, received, defect
	}
	receiptRows := func(t *testing.T, layID int) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_cut_receipt WHERE lay_id = ?", layID).Scan(&n))
		return n
	}

	// ---- (a)+(b) §6.8 the numbers between planned and received ------------------------------------
	saved, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
		SizeId: szA, CutQty: 12, AcceptedQty: 10, Note: ns("перекрой на 2 — узкая кромка"),
	}, "cutter")
	require.NoError(t, err, "заказано 10, выкроено 12 — это законный перекрой, а не ошибка ввода")
	require.Equal(t, 12, saved.CutQty)
	require.Equal(t, 10, saved.AcceptedQty)
	require.Equal(t, layKey, saved.LayKey, "строка адресуется стабильным ключом настила, а не его id")
	require.Equal(t, szA, saved.SizeId)
	require.NotEmpty(t, saved.SizeName, "половина пары не должна приезжать голым id")
	require.Equal(t, "cutter", saved.CreatedBy)
	require.Equal(t, "перекрой на 2 — узкая кромка", saved.Note.String)
	firstID := saved.Id

	// THE PROOF, and it is a query rather than an argument: the run's own row is exactly what it was
	// before the receipt was written. (The receipt path does not even READ production_run_line — see
	// cutReceiptColumns — so «выкроено» and «принято в пошив» cannot disturb «сдано готовым».)
	planned, received, defect := runLineNumbers(t)
	require.Equal(t, 10, planned, "заказано не трогается приёмкой кроя")
	require.Equal(t, int64(7), received.Int64, "received_qty — это СДАНО ГОТОВЫМ; приёмка кроя его не переозначает")
	require.True(t, received.Valid)
	require.Equal(t, int64(1), defect.Int64)
	require.Equal(t, 1, receiptRows(t, lay.Id))

	// ---- (c) the pair is the identity -------------------------------------------------------------
	t.Run("re-reporting the same pair updates the row instead of opening a second", func(t *testing.T) {
		again, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 12, AcceptedQty: 11,
		}, "second-cutter")
		require.NoError(t, err)
		require.Equal(t, firstID, again.Id, "(настил, размер) — естественный ключ, а не ULID")
		require.Equal(t, 11, again.AcceptedQty)
		require.False(t, again.Note.Valid, "перезапись без заметки очищает заметку, а не хранит вчерашнюю")
		require.Equal(t, "cutter", again.CreatedBy, "created_by — это тот, кто завёл строку")
		require.Equal(t, "second-cutter", again.UpdatedBy, "updated_by — тот, кто пересчитал последним")
		require.Equal(t, 1, receiptRows(t, lay.Id))

		// A byte-identical re-save is a success, not a phantom 404: the driver counts rows CHANGED.
		identical, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 12, AcceptedQty: 11,
		}, "second-cutter")
		require.NoError(t, err)
		require.Equal(t, firstID, identical.Id)
		require.Equal(t, 1, receiptRows(t, lay.Id))
	})

	// ---- (f) a size the RUN never planned is a fact, not an error --------------------------------
	t.Run("a size the run does not plan is accepted", func(t *testing.T) {
		got, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szB, CutQty: 3, AcceptedQty: 3,
		}, "cutter")
		require.NoError(t, err, "цех выкроил размер, которого нет в плане — это расхождение, а не запрет")
		require.Equal(t, 3, got.CutQty)

		// The pair already stored is untouched by a save that addressed another one.
		list, err := PR.ListCutReceipts(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list, 2, "апсерт по паре не трогает пары, которых нет в пейлоаде")
		require.Equal(t, szA, list[0].SizeId, "строки идут в порядке размеров, а не в порядке вставки")
		require.Equal(t, firstID, list[0].Id)
		require.Equal(t, 12, list[0].CutQty)
	})

	t.Run("a size the style does not grade is refused", func(t *testing.T) {
		_, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szUngraded, CutQty: 1, AcceptedQty: 1,
		}, "cutter")
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve)
		require.Equal(t, "receipt.size_id", ve.Field)
		require.Equal(t, "size_not_in_style_range", ve.Reason,
			"у размера, который стиль не градирует, нет ни лекал, ни раскладки — выкроить его было нечем")
	})

	// ---- (g) the two counts are checked on their own terms, and against nothing else -------------
	t.Run("negative counts are refused by field and reason", func(t *testing.T) {
		refusal := func(t *testing.T, ins entity.ProductionRunCutReceiptInsert, field, reason string) {
			t.Helper()
			_, err := PR.SaveCutReceipt(ctx, runID, layKey, ins, "cutter")
			var ve *entity.ValidationError
			require.ErrorAs(t, err, &ve)
			require.Equal(t, field, ve.Field)
			require.Equal(t, reason, ve.Reason)
		}
		refusal(t, entity.ProductionRunCutReceiptInsert{SizeId: szA, CutQty: -1},
			"receipt.cut_qty", "out_of_range")
		refusal(t, entity.ProductionRunCutReceiptInsert{SizeId: szA, AcceptedQty: -5},
			"receipt.accepted_qty", "out_of_range")
		refusal(t, entity.ProductionRunCutReceiptInsert{SizeId: 0, CutQty: 1},
			"receipt.size_id", "required")

		// Nothing above moved a stored number.
		list, err := PR.ListCutReceipts(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list, 2)
		require.Equal(t, 12, list[0].CutQty)
		require.Equal(t, 11, list[0].AcceptedQty)
	})

	t.Run("accepted may exceed cut and cut may fall short of the order", func(t *testing.T) {
		// Neither is validated: the first is somebody counting pieces against the wrong настил, the
		// second is a lay that has not covered the order yet. Both are discrepancies to SHOW.
		got, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 4, AcceptedQty: 9,
		}, "cutter")
		require.NoError(t, err)
		require.Equal(t, 4, got.CutQty)
		require.Equal(t, 9, got.AcceptedQty)

		// Put the sane numbers back for the probes that follow.
		_, err = PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 12, AcceptedQty: 10,
		}, "cutter")
		require.NoError(t, err)
	})

	// THE PROOF AGAIN, AFTER EVERY WRITE PATH HAS RUN. The first check above only witnessed an
	// INSERT; by now the same pair has been updated four times, reported at a size the run does not
	// plan, and refused half a dozen times. The run's own three numbers are still the ones the test
	// put there by hand — asked of the database, not inferred from the code.
	planned, received, defect = runLineNumbers(t)
	require.Equal(t, 10, planned)
	require.Equal(t, int64(7), received.Int64)
	require.True(t, received.Valid)
	require.Equal(t, int64(1), defect.Int64)

	// ---- delete one pair --------------------------------------------------------------------------
	t.Run("deleting one pair leaves the other and refuses the second time", func(t *testing.T) {
		require.NoError(t, PR.DeleteCutReceipt(ctx, runID, layKey, szB))
		require.ErrorIs(t, PR.DeleteCutReceipt(ctx, runID, layKey, szB),
			entity.ErrProductionRunCutReceiptNotFound,
			"строки нет — это NotFound, установленный SELECT'ом, а не выведенный из RowsAffected")

		list, err := PR.ListCutReceipts(ctx, runID)
		require.NoError(t, err)
		require.Len(t, list, 1)
		require.Equal(t, szA, list[0].SizeId)
	})

	// ---- addressing ------------------------------------------------------------------------------
	t.Run("an unknown настил and an unknown run are named separately", func(t *testing.T) {
		_, err := PR.SaveCutReceipt(ctx, runID, "01CUTLAYNOSUCHKEY000000000",
			entity.ProductionRunCutReceiptInsert{SizeId: szA, CutQty: 1}, "cutter")
		require.ErrorIs(t, err, entity.ErrProductionRunLayNotFound)

		_, err = PR.SaveCutReceipt(ctx, 0, layKey,
			entity.ProductionRunCutReceiptInsert{SizeId: szA, CutQty: 1}, "cutter")
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, PR.DeleteCutReceipt(ctx, 0, layKey, szA), sql.ErrNoRows)
		_, err = PR.ListCutReceipts(ctx, 0)
		require.ErrorIs(t, err, sql.ErrNoRows,
			"«прогона нет» и «приёмки нет» — разные предложения, и только первое это 404")
	})

	t.Run("another run cannot reach this run's настил", func(t *testing.T) {
		other := newRun([]entity.ProductionRunLine{
			{LineKey: "01CUTRUNLINE0000000000OTHR", ProductId: ni(cwA), SizeId: szA, PlannedQty: 2},
		})
		_, err := PR.SaveCutReceipt(ctx, other, layKey,
			entity.ProductionRunCutReceiptInsert{SizeId: szA, CutQty: 1}, "cutter")
		require.ErrorIs(t, err, entity.ErrProductionRunLayNotFound,
			"настил адресуется парой (прогон, ключ); чужой ключ не резолвится")

		list, err := PR.ListCutReceipts(ctx, other)
		require.NoError(t, err)
		require.Empty(t, list, "список ограничен настилами СВОЕГО прогона")
	})

	// ---- (e) THE DECISION: a terminal run still accepts a receipt --------------------------------
	t.Run("a cancelled run accepts a cut receipt while refusing a настил", func(t *testing.T) {
		require.NoError(t, PR.UpdateProductionRunPreservingCosts(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunCancelled,
			Lines: []entity.ProductionRunLine{
				{LineKey: "01CUTRUNLINE0000000000CWA1", ProductId: ni(cwA), SizeId: szA, PlannedQty: 10},
			},
		}, entity.NoLockVersion()))

		// The настил is a PLAN, and a cancelled run's plan is history.
		_, err := PR.SaveLay(ctx, runID, entity.ProductionRunLayInsert{
			LayKey: layKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, Name: "настил 1",
		}, entity.LockVersion(lay.LockVersion), false, "tester")
		require.ErrorIs(t, err, entity.ErrProductionRunLocked)

		// The receipt is a FACT about the table, and the cloth was cut BEFORE the run was cancelled.
		// Refusing here would mean only successful runs can have their cutting losses recorded.
		got, err := PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 12, AcceptedQty: 0, Note: ns("прогон отменён, крой списывается"),
		}, "cutter")
		require.NoError(t, err, "приёмка кроя — отчёт о прошлом; закрытый прогон её не запрещает")
		require.Equal(t, 0, got.AcceptedQty)
		require.NoError(t, PR.DeleteCutReceipt(ctx, runID, layKey, szA),
			"и удалить ошибочно введённый факт на закрытом прогоне тоже можно")

		// Put it back — probe (d) needs a row to carry away.
		_, err = PR.SaveCutReceipt(ctx, runID, layKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 12, AcceptedQty: 10,
		}, "cutter")
		require.NoError(t, err)
	})

	// ---- (d) §6.10 the cascades ------------------------------------------------------------------
	t.Run("deleting the настил takes its receipts", func(t *testing.T) {
		second := newRun([]entity.ProductionRunLine{
			{LineKey: "01CUTRUNLINE0000000000CASC", ProductId: ni(cwA), SizeId: szA, PlannedQty: 4},
		})
		const secondLayKey = "01CUTLAYBBBBBBBBBBBBBBBBBB"
		secondLay, err := PR.SaveLay(ctx, second, entity.ProductionRunLayInsert{
			LayKey: secondLayKey, ColorwayId: cwA, BomLineKey: fabric.LineKey,
			Mode: entity.ProductionLayModeFaceUp, Name: "настил под удаление",
			Sections: []entity.ProductionRunLaySectionInsert{
				{SectionKey: "01CUTSECBBBBBBBBBBBBBBBBBB", MarkerId: newMarker(second), Plies: 4, Position: 0},
			},
		}, entity.NoLockVersion(), false, "tester")
		require.NoError(t, err)

		_, err = PR.SaveCutReceipt(ctx, second, secondLayKey, entity.ProductionRunCutReceiptInsert{
			SizeId: szA, CutQty: 4, AcceptedQty: 4,
		}, "cutter")
		require.NoError(t, err)
		require.Equal(t, 1, receiptRows(t, secondLay.Id))

		require.NoError(t, PR.DeleteLay(ctx, second, secondLayKey))
		require.Equal(t, 0, receiptRows(t, secondLay.Id),
			"fk_prcr_lay is CASCADE: удаление настила уносит его строки приёмки")
	})

	t.Run("deleting the run takes everything and does not fail", func(t *testing.T) {
		var layID int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM production_run_lay WHERE run_id = ?", runID).Scan(&layID))
		require.Equal(t, 1, receiptRows(t, layID), "прогон уходит с непустой приёмкой — иначе зонд ничего не проверяет")

		require.NoError(t, PR.DeleteProductionRun(ctx, runID),
			"DeleteProductionRun не транзакционен и живёт каскадами; любой RESTRICT сделал бы удаление зависимым от порядка обхода InnoDB")

		require.Equal(t, 0, receiptRows(t, layID))
		var lays, lines int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_lay WHERE id = ?", layID).Scan(&lays))
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_line WHERE run_id = ?", runID).Scan(&lines))
		require.Equal(t, 0, lays)
		require.Equal(t, 0, lines)
	})

	// ---- the aux path -----------------------------------------------------------------------------
	t.Run("an auxiliary card has no pair to report a cut on", func(t *testing.T) {
		auxID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: "Cut Aux", Stage: entity.TechCardStageProto, StyleNumber: ns("CUT-AUX-1"),
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
			Lines: []entity.ProductionRunLine{{LineKey: "01CUTRUNLINE00000000000AUX", SizeId: szA, PlannedQty: 3}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", auxRun)
		})

		// The WRITE says so in words, because «настил not found» would send the operator looking for a
		// lay that could never exist.
		_, err = PR.SaveCutReceipt(ctx, auxRun, layKey,
			entity.ProductionRunCutReceiptInsert{SizeId: szA, CutQty: 1}, "cutter")
		require.ErrorIs(t, err, entity.ErrProductionRunLayNotApplicable)
		require.ErrorIs(t, PR.DeleteCutReceipt(ctx, auxRun, layKey, szA), entity.ErrProductionRunLayNotApplicable)

		// The READ is an empty list: this screen is reached THROUGH the lay plan, which has already
		// said «not applicable» in words, and there is no wire field here to repeat it in.
		list, err := PR.ListCutReceipts(ctx, auxRun)
		require.NoError(t, err)
		require.Empty(t, list)
	})
}
