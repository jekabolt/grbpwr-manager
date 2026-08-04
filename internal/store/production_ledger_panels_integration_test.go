package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestProductionRunLedgerPanels pins the Phase 8 audit surface on real schema: lifecycle events at
// every transition, the run-scoped stock-journal filter, the server-side reconciliation checks
// (including a hand-constructed discrepancy), and the product_cost_event stream the reversal path
// writes.
func TestProductionRunLedgerPanels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	P := s.ProductionRuns()

	eventTypes := func(runID int) []string {
		rows, err := testDB.QueryContext(ctx,
			"SELECT event_type FROM production_run_event WHERE run_id = ? ORDER BY id", runID)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var et string
			require.NoError(t, rows.Scan(&et))
			out = append(out, et)
		}
		return out
	}

	t.Run("lifecycle events at every transition", func(t *testing.T) {
		prodID, _, _, runID := seedReversalFixture(ctx, t, s, "L", 0)
		_ = prodID
		require.Equal(t, []string{"created"}, eventTypes(runID), "creation leaves its event")

		receiptID := postReceiptForReversal(ctx, t, s, runID, 2, 1, false)
		types := eventTypes(runID)
		require.Contains(t, types, "receipt_posted")
		require.Contains(t, types, "units_scrapped")

		_ = postReceiptForReversal(ctx, t, s, runID, 1, 0, true)
		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, entity.ProductionRunReceived, run.Status)
		require.NotEmpty(t, run.Events, "events ride the single-run read")

		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "x", Username: "tester",
		})
		require.ErrorIs(t, err, entity.ErrProductionRunReversalFinalFirst,
			"sanity: partial under live final still refuses (Phase 6 discipline)")
	})

	t.Run("reconciliation is green on a clean run and catches an injected journal row", func(t *testing.T) {
		_, _, _, runID := seedReversalFixture(ctx, t, s, "M", 40)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 3, 0, true)
		postLiveReceiveEntry(ctx, t, s, receiptID, decimal.NewFromInt(40), decimal.RequireFromString("25.00"))

		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Len(t, run.Recon, 3)
		byKey := map[string]entity.ProductionRunReconCheck{}
		for _, c := range run.Recon {
			byKey[c.Key] = c
		}
		require.True(t, byKey["units_receipts_vs_stock_journal"].Ok, "clean run: units tie out")
		require.True(t, byKey["money_posted_vs_entries"].Ok)
		require.True(t, byKey["costs_capitalised"].Ok, "manual claim stamped by postLiveReceiveEntry")

		// Inject a journal row that references the run but has no receipt behind it — the exact
		// class of drift the check exists to catch (manual edits, journal bypasses).
		_, err = testDB.ExecContext(ctx, `
			INSERT INTO product_stock_change_history
				(product_id, size_id, grade, quantity_delta, quantity_before, quantity_after, source, reference_id)
			SELECT rl.product_id, rl.size_id, 'A', 1, 0, 1, 'production_received', CONCAT('production_run:', ?)
			FROM production_run_receipt_line rl JOIN production_run_receipt pr ON pr.id = rl.receipt_id
			WHERE pr.run_id = ? LIMIT 1`, runID, runID)
		require.NoError(t, err)

		run, err = P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		for _, c := range run.Recon {
			if c.Key == "units_receipts_vs_stock_journal" {
				require.False(t, c.Ok, "the injected row must surface")
				require.NotEmpty(t, c.Detail)
			}
		}
	})

	t.Run("stock journal filters by the run's whole reference family", func(t *testing.T) {
		_, _, _, runID := seedReversalFixture(ctx, t, s, "N", 0)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 2, 0, true)
		_, err := P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "undo", Username: "tester",
		})
		require.NoError(t, err)

		from := time.Now().UTC().Add(-time.Hour)
		to := time.Now().UTC().Add(time.Hour)
		rows, total, err := s.Products().GetStockChanges(ctx, from, to, nil, nil, "", &runID, 100, 0, "", entity.Descending)
		require.NoError(t, err)
		require.Equal(t, 2, total, "receive (run ref) + reversal (receipt ref) — both families")
		require.Len(t, rows, 2)
	})

	t.Run("cost events record the reversal's clear", func(t *testing.T) {
		prodID, _, _, runID := seedReversalFixture(ctx, t, s, "O", 30)
		receiptID := postReceiptForReversal(ctx, t, s, runID, 2, 0, true)
		// The fixture's create-time cost is manual provenance, which the run seed rightly refuses —
		// neutralise it so this test exercises the seed + clear audit pair.
		_, err := testDB.ExecContext(ctx,
			"UPDATE product SET cost_price = NULL, cost_price_source = NULL WHERE id = ?", prodID)
		require.NoError(t, err)
		// Claim the cost like the receive path does, so the reversal has something to clear.
		written, err := s.Products().SetProductCostPriceFromProductionRun(ctx, prodID, runID, decimal.RequireFromString("15.00"))
		require.NoError(t, err)
		require.True(t, written)
		_, err = P.ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
			RunID: runID, ReceiptID: receiptID, Reason: "undo", Username: "tester",
		})
		require.NoError(t, err)

		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM product_cost_event WHERE product_id = ?", prodID).Scan(&n))
		require.GreaterOrEqual(t, n, 2, "receive seed + reversal clear both audit")
		var lastSource string
		var lastAfter interface{}
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT source, cost_after FROM product_cost_event WHERE product_id = ? ORDER BY id DESC LIMIT 1", prodID).
			Scan(&lastSource, &lastAfter))
		require.Equal(t, "production_run_reversal_clear", lastSource)
		require.Nil(t, lastAfter, "clear records an honestly-unknown NULL")
	})
}

// TestRefundDispositions pins the Phase 8 refund branches on real schema: writeoff restocks
// nothing; seconds restocks into the B-grade variant; the outbox payload carries the disposition.
func TestRefundDispositions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The order lifecycle resolves statuses via the process-wide cache — prime it like the app does.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// A delivered order over the reversal fixture's product (published, sized, stocked via receipt).
	seedOrder := func(tag string, prodID, sizeID int) (string, int64, int) {
		uuid := "P8-REFUND-" + tag
		var deliveredID int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM order_status WHERE name = 'delivered'").Scan(&deliveredID))
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price) VALUES (?, ?, 'EUR', 100)`,
			uuid, deliveredID)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO order_item (order_id, product_id, variant_id, product_price, product_price_base, product_sale_percentage, quantity, size_id, variant_sku_snapshot)
			 VALUES (?, ?, (SELECT id FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'A'), 100, 100, 0, 1, ?, 'P8-SKU')`,
			oid, prodID, prodID, sizeID, sizeID)
		require.NoError(t, err)
		var iid int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT id FROM order_item WHERE order_id = ?", oid).Scan(&iid))
		return uuid, oid, iid
	}
	aQty := func(prodID, sizeID int) string {
		var q string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'A'", prodID, sizeID).Scan(&q))
		return q
	}

	t.Run("writeoff restocks nothing and journals nothing", func(t *testing.T) {
		prodID, sizeA, _, runID := seedReversalFixture(ctx, t, s, "R1", 0)
		_ = postReceiptForReversal(ctx, t, s, runID, 3, 0, true)
		uuid, _, itemID := seedOrder("W", prodID, sizeA)
		before := aQty(prodID, sizeA)

		require.NoError(t, s.Order().RefundOrder(ctx, uuid, []int32{int32(itemID)}, "damaged", "", false, entity.RefundDispositionWriteoff))
		require.Equal(t, before, aQty(prodID, sizeA), "A stock untouched")
		var journalRows int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM product_stock_change_history WHERE order_uuid = ? AND source = 'order_returned'", uuid).Scan(&journalRows))
		require.Zero(t, journalRows, "no phantom journal rows for consumed goods")

		var payload string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT payload FROM acct_event WHERE source_key = ?", uuid+":1").Scan(&payload))
		require.Contains(t, payload, `"disposition"`)
		require.Contains(t, payload, `"writeoff"`)
	})

	t.Run("seconds restocks into the B variant at zero cost", func(t *testing.T) {
		prodID, sizeA, _, runID := seedReversalFixture(ctx, t, s, "R2", 0)
		_ = postReceiptForReversal(ctx, t, s, runID, 3, 0, true)
		uuid, _, itemID := seedOrder("S", prodID, sizeA)
		before := aQty(prodID, sizeA)

		require.NoError(t, s.Order().RefundOrder(ctx, uuid, []int32{int32(itemID)}, "worn but fine", "", false, entity.RefundDispositionSeconds))
		require.Equal(t, before, aQty(prodID, sizeA), "A stock untouched")
		var bQty string
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT quantity FROM product_size WHERE product_id = ? AND size_id = ? AND grade = 'B'", prodID, sizeA).Scan(&bQty))
		require.True(t, decimal.RequireFromString(bQty).Equal(decimal.NewFromInt(1)), "the unit lives as a B second, got %s", bQty)
		var graded int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM product_stock_change_history WHERE order_uuid = ? AND source = 'order_returned' AND grade = 'B'", uuid).Scan(&graded))
		require.Equal(t, 1, graded, "the B restock is journalled with its grade")
	})

	t.Run("unknown disposition refuses", func(t *testing.T) {
		err := s.Order().RefundOrder(ctx, "no-such-order", nil, "r", "", false, "quarantine")
		require.Error(t, err)
		require.Contains(t, err.Error(), "disposition")
	})
}

// TestAdminWaitlistList pins the Phase 9 admin read over product_waitlist.
func TestAdminWaitlistList(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	prodID, sizeA, _, _ := seedReversalFixture(ctx, t, s, "WL", 0)
	require.NoError(t, s.Products().AddToWaitlist(ctx, prodID, sizeA, "p8-one@example.com"))
	require.NoError(t, s.Products().AddToWaitlist(ctx, prodID, sizeA, "p8-two@example.com"))

	entries, total, err := s.Products().ListWaitlist(ctx, &prodID, 50, 0)
	require.NoError(t, err)
	require.Equal(t, 2, total)
	require.Len(t, entries, 2)

	n, err := s.Products().CountWaitlistForProduct(ctx, prodID)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	all, allTotal, err := s.Products().ListWaitlist(ctx, nil, 1, 0)
	require.NoError(t, err)
	require.GreaterOrEqual(t, allTotal, 2)
	require.Len(t, all, 1, "pagination caps the page")
}
