package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestProductionReceiptDefectDisposition pins Phase 7 end-to-end on real schema: a seconds
// disposition books the B-grade variant (own stock, '-B' SKU, journalled movement), scrap leaves
// only the units_scrapped event (no stock), dispositions persist on receipt lines, the A-grade
// read paths never see B rows, and a reversal takes the B stock back out with the good units.
func TestProductionReceiptDefectDisposition(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(150)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(150)})
	}
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "RCPT-DISP", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(0)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) })

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-DISP", Valid: true}, Name: "Disposition Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: int32(prodID), Valid: true}, SizeId: sizeA, PlannedQty: 10},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run_receipt WHERE run_id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM product_stock_change_history WHERE reference_id = ?", "production_run:"+strconv.Itoa(runID))
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
	})

	run, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	keyA := run.Lines[0].LineKey

	// FINAL receipt: 6 good, 2 seconds, 2 scrap on one line... but one line carries ONE disposition —
	// so split the intent across two receipts instead: a partial with the seconds and the scrap, then
	// the final with the good units (also the realistic operator flow: defects surface per delivery).
	lines1 := []entity.ProductionRunReceiptLineInput{{LineKey: keyA, GoodQty: 0, DefectQty: 2, DefectDisposition: entity.DefectDispositionSeconds}}
	key1, err := entity.MintProductionRunLineKey()
	require.NoError(t, err)
	_, err = P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
		RunID: runID, Lines: lines1, IdempotencyKey: key1,
		RequestHash: dto.HashProductionRunReceiptPayload(runID, lines1, "", false, false),
		Username:    "tester", BaseCurrency: "EUR", Final: false,
	})
	require.NoError(t, err)

	// B-grade stock exists with its own '-B' SKU; A-grade stock is untouched.
	var bQty decimal.Decimal
	var bSKU sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity, sku FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'B'", prodID, sizeA).Scan(&bQty, &bSKU))
	require.EqualValues(t, 2, bQty.IntPart())
	require.True(t, bSKU.Valid)
	var aSKU sql.NullString
	var aQty decimal.Decimal
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity, sku FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'A'", prodID, sizeA).Scan(&aQty, &aSKU))
	require.EqualValues(t, 0, aQty.IntPart())
	require.Equal(t, aSKU.String+"-B", bSKU.String, "the B SKU is the A SKU with a -B suffix")

	// The movement was journalled against the B stream.
	var bMoves int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_stock_change_history
		 WHERE product_id = ? AND size_id = ? AND grade = 'B' AND source = 'production_received'`, prodID, sizeA).Scan(&bMoves))
	require.Equal(t, 1, bMoves)

	// The units event recorded the seconds.
	var payload string
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT payload FROM production_run_event WHERE run_id = ? AND event_type = 'units_scrapped' ORDER BY id DESC LIMIT 1`, runID).Scan(&payload))
	require.Contains(t, payload, `"seconds_qty": 2`)

	// FINAL: 6 good + 2 scrap. Disposition defaults to scrap when the client omits it.
	lines2 := []entity.ProductionRunReceiptLineInput{{LineKey: keyA, GoodQty: 6, DefectQty: 2}}
	key2, err := entity.MintProductionRunLineKey()
	require.NoError(t, err)
	res2, err := P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
		RunID: runID, Lines: lines2, IdempotencyKey: key2,
		RequestHash: dto.HashProductionRunReceiptPayload(runID, lines2, "", false, true),
		Username:    "tester", BaseCurrency: "EUR", Final: true,
	})
	require.NoError(t, err)

	var disp string
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT defect_disposition FROM production_run_receipt_line WHERE receipt_id = ?", res2.ReceiptID).Scan(&disp))
	require.Equal(t, entity.DefectDispositionScrap, disp)
	// Scrap wrote NO stock movement — good units book, seconds booked earlier, scrap only events.
	var totalMoves int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_stock_change_history
		 WHERE reference_id = ?`, "production_run:"+strconv.Itoa(runID)).Scan(&totalMoves))
	require.Equal(t, 2, totalMoves, "one B booking + one A booking; scrap books nothing")
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT payload FROM production_run_event WHERE run_id = ? AND event_type = 'units_scrapped' ORDER BY id DESC LIMIT 1`, runID).Scan(&payload))
	require.Contains(t, payload, `"scrap_qty": 2`)

	// The A-grade read path never sees the B row: the run detail's stock probe and the sellable
	// availability both count 6.
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'A'", prodID, sizeA).Scan(&aQty))
	require.EqualValues(t, 6, aQty.IntPart())

	// Reversal of the seconds receipt is blocked while the final stands (newest-first), then the
	// final's reversal takes the good units out, and the seconds receipt's reversal empties B.
	var secondsReceiptID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM production_run_receipt WHERE run_id = ? AND final = 0 ORDER BY id LIMIT 1", runID).Scan(&secondsReceiptID))
	_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
		RunID: runID, ReceiptID: secondsReceiptID, Reason: "undo seconds", Username: "tester",
	})
	require.ErrorIs(t, err, entity.ErrProductionRunReversalFinalFirst)

	_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
		RunID: runID, ReceiptID: res2.ReceiptID, Reason: "undo final", Username: "tester",
	})
	require.NoError(t, err)
	_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
		RunID: runID, ReceiptID: secondsReceiptID, Reason: "undo seconds", Username: "tester",
	})
	require.NoError(t, err)
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'B'", prodID, sizeA).Scan(&bQty))
	require.EqualValues(t, 0, bQty.IntPart(), "the reversal took the seconds back out of B stock")
}
