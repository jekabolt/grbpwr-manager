package store

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// seedReversalFixture creates a product (with one variant), a tech card and an in-progress run with
// one product-linked plan line (planned 10), returning (productID, sizeID, techCardID, runID).
func seedReversalFixture(ctx context.Context, t *testing.T, s *MYSQLStore, tag string, manualCost int64) (int, int, int, int) {
	t.Helper()
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
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(200)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(200)})
	}
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "REV-" + tag, Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(2)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) })

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: sql.NullString{String: "PRUN-REV-" + tag, Valid: true}, Name: "Rev Coat " + tag, Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{
			ProductId: sql.NullInt32{Int32: int32(prodID), Valid: true}, SizeId: sizeA, PlannedQty: 10,
		}},
		Costs: []entity.ProductionRunCost{{
			Kind: entity.ProductionRunCostCMT, Amount: decimal.NewFromInt(manualCost), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(manualCost), Valid: true},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run_receipt WHERE run_id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
	})
	return prodID, sizeA, tcID, runID
}

// postReceiptForReversal posts one receipt (good/defect on the run's single line) and returns its id.
func postReceiptForReversal(ctx context.Context, t *testing.T, s *MYSQLStore, runID, good, defect int, final bool) int {
	t.Helper()
	run, err := s.ProductionRuns().GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.NotEmpty(t, run.Lines)
	key, err := entity.MintProductionRunLineKey()
	require.NoError(t, err)
	lines := []entity.ProductionRunReceiptLineInput{{LineKey: run.Lines[0].LineKey, GoodQty: good, DefectQty: defect}}
	res, err := s.ProductionRuns().PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
		RunID: runID, Lines: lines, IdempotencyKey: key,
		RequestHash:     "hash-" + key,
		UpdateCostPrice: true,
		Username:        "tester",
		BaseCurrency:    "EUR",
		Final:           final,
	})
	require.NoError(t, err)
	return res.ReceiptID
}

// postLiveReceiveEntry simulates the posting worker: a live production_receive entry for the
// receipt (Dr 1120/Cr 2010 manual + Dr 1130/Cr 1120 fg) and the same-tx posted-amount stamps.
func postLiveReceiveEntry(ctx context.Context, t *testing.T, s *MYSQLStore, receiptID int, manual, fg decimal.Decimal) {
	t.Helper()
	lines := []entity.AcctJournalLineInsert{}
	if manual.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: "1120", Side: entity.AcctSideDebit, Amount: manual},
			entity.AcctJournalLineInsert{AccountCode: "2010", Side: entity.AcctSideCredit, Amount: manual},
		)
	}
	if fg.IsPositive() {
		lines = append(lines,
			entity.AcctJournalLineInsert{AccountCode: "1130", Side: entity.AcctSideDebit, Amount: fg},
			entity.AcctJournalLineInsert{AccountCode: "1120", Side: entity.AcctSideCredit, Amount: fg},
		)
	}
	require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, existed, err := rep.Accounting().CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
			OccurredAt:  time.Now().UTC(),
			Description: "test receive",
			SourceType:  entity.AcctSourceProductionReceive,
			SourceKey:   "receipt:" + strconv.Itoa(receiptID),
			CreatedBy:   "system",
			Lines:       lines,
		})
		if err != nil {
			return err
		}
		require.False(t, existed)
		return rep.Accounting().MarkReceiptPosted(ctx, receiptID, manual, fg)
	}))
}

// TestProductionReceiptReversal pins the Phase 6 command end-to-end on real schema: stock back out
// (journaled), rollups subtracted, scoped compensation (manual/AP stays), linkage + history,
// cost_price rollback, run status recompute, the audit event, and every refusal path.
func TestProductionReceiptReversal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	P := s.ProductionRuns()

	t.Run("final receipt reversal: stock, money, claim, status, event", func(t *testing.T) {
		prodID, sizeA, _, runID := seedReversalFixture(ctx, t, s, "A", 90)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 6, 3, true)
		postLiveReceiveEntry(ctx, t, s, receiptID, decimal.NewFromInt(90), decimal.RequireFromString("60.00"))

		var qtyBefore decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyBefore))

		res, err := P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "wrong batch counted", Username: "tester",
		})
		require.NoError(t, err)
		require.Positive(t, res.ReversalReceiptID)
		require.True(t, res.CompensatedFGBase.Valid)
		require.True(t, res.CompensatedFGBase.Decimal.Equal(decimal.RequireFromString("60.00")))
		require.Equal(t, []int{prodID}, res.CostPriceCleared, "run-claimed cost_price cleared (no card estimate)")

		// Stock: the 6 good units left again, journaled as production_reversed.
		var qtyAfter decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyAfter))
		require.True(t, qtyBefore.Sub(qtyAfter).Equal(decimal.NewFromInt(6)), "before %s after %s", qtyBefore, qtyAfter)
		var journaled int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM product_stock_change_history
			WHERE product_id = ? AND source = 'production_reversed' AND reason = 'receipt_reversed' AND reference_id = ?`,
			prodID, "receipt:"+strconv.Itoa(receiptID)).Scan(&journaled))
		require.Equal(t, 1, journaled)

		// Rollups subtracted back to zero.
		var recQty, defQty int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COALESCE(received_qty, -1), COALESCE(defect_qty, -1) FROM production_run_line WHERE run_id = ?", runID).
			Scan(&recQty, &defQty))
		require.Zero(t, recQty)
		require.Zero(t, defQty)

		// Linkage pair + history.
		var reversedBy sql.NullInt32
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT reversed_by FROM production_run_receipt WHERE id = ?", receiptID).Scan(&reversedBy))
		require.True(t, reversedBy.Valid)
		require.EqualValues(t, res.ReversalReceiptID, reversedBy.Int32)
		var reversalOf sql.NullInt32
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT reversal_of FROM production_run_receipt WHERE id = ?", res.ReversalReceiptID).Scan(&reversalOf))
		require.True(t, reversalOf.Valid)
		require.EqualValues(t, receiptID, reversalOf.Int32)

		// Scoped compensation: Dr 1120 / Cr 1130 of the FG figure; the original entry stays LIVE
		// (the manual/AP capitalisation remains payable); posted_manual_base kept, posted_fg_base gone.
		var compensations int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM acct_journal_entry
			WHERE source_type = 'production_receive_reversal' AND reversed_by IS NULL
			  AND source_key = ?`, "receipt:"+strconv.Itoa(receiptID)).Scan(&compensations))
		require.Equal(t, 1, compensations)
		var origLive int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM acct_journal_entry
			WHERE source_type = 'production_receive' AND reversed_by IS NULL AND source_key = ?`,
			"receipt:"+strconv.Itoa(receiptID)).Scan(&origLive))
		require.Equal(t, 1, origLive, "original receive entry must stay live")
		var manualClaim decimal.NullDecimal
		var fgClaim decimal.NullDecimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT posted_manual_base, posted_fg_base FROM production_run_receipt WHERE id = ?", receiptID).
			Scan(&manualClaim, &fgClaim))
		require.True(t, manualClaim.Valid, "manual claim survives a scoped reversal")
		require.True(t, manualClaim.Decimal.Equal(decimal.NewFromInt(90)))
		require.False(t, fgClaim.Valid, "fg claim dies with the compensation")

		// cost_price rollback: the run's claim cleared to NULL (no card estimate available).
		var costPrice decimal.NullDecimal
		var src sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT cost_price, cost_price_source FROM product WHERE id = ?", prodID).Scan(&costPrice, &src))
		require.False(t, costPrice.Valid)
		require.False(t, src.Valid)

		// Run back to in_progress (its only receipt is reversed), received_at cleared.
		var status string
		var receivedAt sql.NullTime
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT status, received_at FROM production_run WHERE id = ?", runID).Scan(&status, &receivedAt))
		require.Equal(t, string(entity.ProductionRunInProgress), status)
		require.False(t, receivedAt.Valid)

		// The audit event.
		var events int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_event WHERE run_id = ? AND event_type = 'receipt_reversed'", runID).
			Scan(&events))
		require.Equal(t, 1, events)

		// Idempotency guard: the second reversal refuses; reversing the reversal row refuses.
		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "again", Username: "tester",
		})
		require.ErrorIs(t, err, entity.ErrProductionRunReceiptAlreadyReversed)
		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: res.ReversalReceiptID, Reason: "meta", Username: "tester",
		})
		require.ErrorIs(t, err, entity.ErrProductionRunReversalOfReversal)
	})

	t.Run("sold units block with the exact shortfall and nothing changes", func(t *testing.T) {
		prodID, sizeA, _, runID := seedReversalFixture(ctx, t, s, "B", 50)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 5, 0, false)

		// A sale takes 4 of the received units (plus the 2 seeded, 3 remain of the receipt's 5).
		_, _, err := s.Products().UpdateProductSizeStockWithHistory(ctx, prodID, sizeA, entity.StockUpdateModeAdjust, -4, "correction", "sold")
		require.NoError(t, err)

		var qtyBefore decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyBefore))

		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "try", Username: "tester",
		})
		var shortErr *entity.ProductionRunReversalShortfallError
		require.True(t, errors.As(err, &shortErr), "got %v", err)
		require.Len(t, shortErr.Items, 1)
		require.Equal(t, prodID, shortErr.Items[0].ProductID)
		require.Equal(t, sizeA, shortErr.Items[0].SizeID)
		require.Equal(t, 5, shortErr.Items[0].Requested)
		require.Equal(t, 3, shortErr.Items[0].OnHand)

		// Whole transaction rolled back: stock, receipt and run untouched.
		var qtyAfter decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyAfter))
		require.True(t, qtyAfter.Equal(qtyBefore))
		var reversedBy sql.NullInt32
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT reversed_by FROM production_run_receipt WHERE id = ?", receiptID).Scan(&reversedBy))
		require.False(t, reversedBy.Valid)
		var status string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT status FROM production_run WHERE id = ?", runID).Scan(&status))
		require.Equal(t, string(entity.ProductionRunPartiallyReceived), status)
	})

	t.Run("scoped reversal keeps the manual claim visible to the next receipt", func(t *testing.T) {
		prodID, _, _, runID := seedReversalFixture(ctx, t, s, "C", 100)
		_ = prodID
		r1 := postReceiptForReversal(ctx, t, s, runID, 2, 0, false)
		postLiveReceiveEntry(ctx, t, s, r1, decimal.NewFromInt(100), decimal.RequireFromString("20.00"))

		_, err := P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: r1, Reason: "recount", Username: "tester",
		})
		require.NoError(t, err)

		// A new receipt's facts must still see the 100 the reversed receipt capitalised — the
		// invoice remains payable; re-capitalising it would double AP (the whole point of the
		// live-claim aggregate semantics).
		r2 := postReceiptForReversal(ctx, t, s, runID, 3, 0, false)
		facts, err := s.Accounting().GetReceiptFactsForPosting(ctx, r2)
		require.NoError(t, err)
		require.True(t, facts.OtherPostedManualBase.Equal(decimal.NewFromInt(100)),
			"other_manual must keep the reversed receipt's live manual claim, got %s", facts.OtherPostedManualBase)
		require.True(t, facts.OtherPostedFGBase.Equal(decimal.Zero),
			"other_fg must NOT see the compensated fg claim, got %s", facts.OtherPostedFGBase)
		// Unit aggregates see only the live receipt.
		require.Equal(t, 3, facts.AllGoodQty)
		require.Equal(t, 3, facts.AllReceivedQty)
	})

	t.Run("closed period refuses and rolls back", func(t *testing.T) {
		prodID, sizeA, _, runID := seedReversalFixture(ctx, t, s, "D", 40)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 4, 0, true)
		postLiveReceiveEntry(ctx, t, s, receiptID, decimal.NewFromInt(40), decimal.RequireFromString("40.00"))

		month := time.Now().UTC().Format("2006-01") + "-01"
		_, err := testDB.ExecContext(ctx, `
			INSERT INTO acct_period (period, status, closed_at, closed_by) VALUES (?, 'closed', NOW(), 'tester')
			ON DUPLICATE KEY UPDATE status = 'closed', closed_at = NOW(), closed_by = 'tester'`, month)
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(),
				"UPDATE acct_period SET status = 'open', closed_at = NULL, closed_by = NULL WHERE period = ?", month)
		})

		var qtyBefore decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyBefore))

		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "late", Username: "tester",
		})
		require.ErrorIs(t, err, entity.ErrProductionRunReversalPeriodClosed)

		var qtyAfter decimal.Decimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ?", prodID, sizeA).Scan(&qtyAfter))
		require.True(t, qtyAfter.Equal(qtyBefore), "stock rollback on refusal")
		var status string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT status FROM production_run WHERE id = ?", runID).Scan(&status))
		require.Equal(t, string(entity.ProductionRunReceived), status)
	})

	t.Run("reversing a partial keeps a live final untouched and the run received", func(t *testing.T) {
		_, _, _, runID := seedReversalFixture(ctx, t, s, "E", 0)
		r1 := postReceiptForReversal(ctx, t, s, runID, 2, 0, false)
		_ = postReceiptForReversal(ctx, t, s, runID, 3, 0, true)

		_, err := P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: r1, Reason: "partial was wrong", Username: "tester",
		})
		require.NoError(t, err)
		var status string
		var receivedAt sql.NullTime
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT status, received_at FROM production_run WHERE id = ?", runID).Scan(&status, &receivedAt))
		require.Equal(t, string(entity.ProductionRunReceived), status, "a live final keeps the run received")
		require.True(t, receivedAt.Valid)
	})
}
