package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/acctposting"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAcctPostingWorker is the PR5 (docs/plan-accounting/07, 09 §9.5) acceptance for the posting
// worker. Driving Worker.RunOnce directly against a real DB, it proves the read-on-pool → build →
// write-in-Tx pipeline for the phases that matter to the ledger's correctness:
//
//	(a) order_paid (cash/EUR)     -> an order_sale entry (S1), balanced, on the right accounts;
//	(b) material receipt          -> a material_receipt entry (M1), Dr 1110 / Cr 2010;
//	(c) a second RunOnce          -> no duplicate entries (idempotency on source_type,source_key);
//	(d) Stripe settled=NULL       -> the event stays pending ("settled pending"), then posts once
//	                                 total_settled_base is filled (maturation);
//	(e) refund before its sale    -> "awaiting sale posting", then posts after the sale exists (S2).
//
// It isolates itself from other suites' data by pinning both pull-source checkpoints ahead of any
// pre-existing rows (so only this suite's movement is scanned and opex is suppressed) and by using a
// distinctive 'ACCTW-' key prefix, a dedicated movement admin_username, and a far-future posting
// month (2038-09) that it cleans up.
func TestAcctPostingWorker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	acc := s.Accounting()

	// --- reference ids --------------------------------------------------------------------------
	var cashPM, cardPM, statusID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM payment_method WHERE name = 'cash'").Scan(&cashPM))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM payment_method WHERE name = 'card'").Scan(&cardPM))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM order_status").Scan(&statusID))

	// A movement id captured by the movement subtest so cleanup can drop its journal entry (source_key
	// is the numeric movement id, which the 'ACCTW-' prefix does not cover).
	var movID int64

	t.Cleanup(func() {
		cctx := context.Background()
		if movID != 0 {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_line WHERE entry_id IN (SELECT id FROM acct_journal_entry WHERE source_type='material_receipt' AND source_key = ?)", fmt.Sprint(movID))
			_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_type='material_receipt' AND source_key = ?", fmt.Sprint(movID))
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_line WHERE entry_id IN (SELECT id FROM acct_journal_entry WHERE source_key LIKE 'ACCTW-%')")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_key LIKE 'ACCTW-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_event WHERE source_key LIKE 'ACCTW-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = '2038-09-01'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_checkpoint WHERE source IN ('material_movement','opex_line')")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM payment WHERE order_id IN (SELECT id FROM customer_order WHERE uuid LIKE 'ACCTW-%')")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE uuid LIKE 'ACCTW-%'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE admin_username = 'ACCTW-tester'")
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE name LIKE 'ACCTW-%'")
	})

	// Pin both pull checkpoints ahead of any pre-existing data so RunOnce only ever posts THIS suite's
	// movement (id > current max) and never sweeps foreign opex months.
	var maxMov int64
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT COALESCE(MAX(id),0) FROM material_stock_movement").Scan(&maxMov))
	require.NoError(t, acc.SetCheckpoint(ctx, "material_movement", sql.NullInt64{Int64: maxMov, Valid: true}, sql.NullTime{}))
	require.NoError(t, acc.SetCheckpoint(ctx, "opex_line", sql.NullInt64{}, sql.NullTime{Time: time.Now().UTC(), Valid: true}))

	// A cutover well before every fixture's created_at (movements scan by created_at = NOW), so the
	// suite's movement/orders are all in scope.
	w := acctposting.New(&acctposting.Config{
		Enabled:        true,
		WorkerInterval: time.Minute,
		BatchSize:      200,
		StartDate:      "2020-01-01",
		SettledWaitMax: 48 * time.Hour,
	}, s)

	occurredPaid := time.Date(2038, 9, 15, 12, 0, 0, 0, time.UTC)
	occurredRefund := time.Date(2038, 9, 16, 12, 0, 0, 0, time.UTC)

	// --- helpers --------------------------------------------------------------------------------
	mkOrder := func(uuid string, pmID int, totalPrice string, settled sql.NullString) {
		res, err := testDB.ExecContext(ctx,
			`INSERT INTO customer_order (uuid, order_status_id, currency, total_price, total_settled_base) VALUES (?, ?, 'EUR', ?, ?)`,
			uuid, statusID, totalPrice, settled)
		require.NoError(t, err)
		oid, err := res.LastInsertId()
		require.NoError(t, err)
		_, err = testDB.ExecContext(ctx,
			`INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done) VALUES (?, ?, ?, ?, 1)`,
			oid, pmID, totalPrice, totalPrice)
		require.NoError(t, err)
	}

	getEntry := func(sourceType entity.AcctSourceType, sourceKey string) (*entity.AcctJournalEntryFull, bool) {
		var id int
		err := testDB.QueryRowContext(ctx,
			"SELECT id FROM acct_journal_entry WHERE source_type = ? AND source_key = ?",
			string(sourceType), sourceKey).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false
		}
		require.NoError(t, err)
		full, err := s.Accounting().GetJournalEntry(ctx, id)
		require.NoError(t, err)
		return full, true
	}

	lineAmount := func(full *entity.AcctJournalEntryFull, code string, side entity.AcctSide) (decimal.Decimal, bool) {
		for _, l := range full.Lines {
			if l.AccountCode == code && l.Side == side {
				return l.Amount, true
			}
		}
		return decimal.Zero, false
	}

	requireBalanced := func(full *entity.AcctJournalEntryFull) {
		dr, cr := decimal.Zero, decimal.Zero
		for _, l := range full.Lines {
			switch l.Side {
			case entity.AcctSideDebit:
				dr = dr.Add(l.Amount)
			case entity.AcctSideCredit:
				cr = cr.Add(l.Amount)
			}
		}
		require.True(t, dr.Equal(cr), "entry must balance: debit %s != credit %s", dr, cr)
	}

	requireLine := func(full *entity.AcctJournalEntryFull, code string, side entity.AcctSide, want string) {
		amt, ok := lineAmount(full, code, side)
		require.True(t, ok, "expected a %s line on %s", side, code)
		require.True(t, amt.Equal(decimal.RequireFromString(want)), "%s %s: want %s got %s", code, side, want, amt)
	}

	eventState := func(sourceKey string) (processed bool, lastErr string) {
		var processedAt sql.NullTime
		var le sql.NullString
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT processed_at, last_error FROM acct_event WHERE source_key = ?", sourceKey).Scan(&processedAt, &le))
		return processedAt.Valid, le.String
	}

	dueNow := func(sourceKey string) {
		_, err := testDB.ExecContext(ctx, "UPDATE acct_event SET next_retry_at = NULL WHERE source_key = ?", sourceKey)
		require.NoError(t, err)
	}

	// (a) order_paid (cash, EUR) -> order_sale S1 posted against 1010 Cash / 4010 Retail. Phase 2 wave 1:
	// a cash order resolves to the uk_stock_domestic regime (vatregime.go — cash-first rule), so the
	// gross splits into net revenue + 20% inclusive UK VAT (2070): 100 -> 83.33 net + 16.67 VAT.
	// (Requires the GB rate seeded by migration 0193.)
	t.Run("order_paid_posts_sale", func(t *testing.T) {
		const uuid = "ACCTW-SALE-1"
		mkOrder(uuid, cashPM, "100", sql.NullString{})
		require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  uuid,
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
			OccurredAt: occurredPaid,
		}))

		require.NoError(t, w.RunOnce(ctx))

		full, ok := getEntry(entity.AcctSourceOrderSale, uuid)
		require.True(t, ok, "order_sale entry must exist")
		requireBalanced(full)
		requireLine(full, "1010", entity.AcctSideDebit, "100")    // cash money account (gross)
		requireLine(full, "4010", entity.AcctSideCredit, "83.33") // cash revenue (net of 20% UK VAT)
		requireLine(full, "2070", entity.AcctSideCredit, "16.67") // output VAT (uk_stock_domestic 20%, inclusive)

		processed, _ := eventState(uuid)
		require.True(t, processed, "the sale event must be marked processed")
	})

	// (b) material receipt -> material_receipt M1 posted Dr 1110 Materials / Cr 2010 AP.
	t.Run("movement_posts_receipt", func(t *testing.T) {
		var matID int64
		res, err := testDB.ExecContext(ctx, "INSERT INTO material (name, section) VALUES ('ACCTW-fabric', 'fabric')")
		require.NoError(t, err)
		matID, err = res.LastInsertId()
		require.NoError(t, err)

		res, err = testDB.ExecContext(ctx,
			`INSERT INTO material_stock_movement
			   (material_id, movement_type, quantity, on_hand_before, on_hand_after, unit_cost_base, occurred_at, admin_username)
			 VALUES (?, 'receipt', 10, 0, 10, 5.0000, '2038-09-15', 'ACCTW-tester')`, matID)
		require.NoError(t, err)
		movID, err = res.LastInsertId()
		require.NoError(t, err)

		require.NoError(t, w.RunOnce(ctx))

		full, ok := getEntry(entity.AcctSourceMaterialReceipt, fmt.Sprint(movID))
		require.True(t, ok, "material_receipt entry must exist")
		requireBalanced(full)
		requireLine(full, "1110", entity.AcctSideDebit, "50")  // 10 * 5.00
		requireLine(full, "2010", entity.AcctSideCredit, "50") // accounts payable

		// the checkpoint advanced past this movement
		cp, err := acc.GetCheckpoint(ctx, "material_movement")
		require.NoError(t, err)
		require.True(t, cp.LastId.Valid && cp.LastId.Int64 >= movID, "movement checkpoint advanced")
	})

	// (c) idempotency: a second full pass creates no duplicates for the already-posted sale/movement.
	t.Run("rerun_no_duplicates", func(t *testing.T) {
		count := func(sourceType, sourceKey string) int {
			var n int
			require.NoError(t, testDB.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM acct_journal_entry WHERE source_type = ? AND source_key = ?",
				sourceType, sourceKey).Scan(&n))
			return n
		}
		require.NoError(t, w.RunOnce(ctx))
		require.Equal(t, 1, count("order_sale", "ACCTW-SALE-1"), "sale not duplicated")
		require.Equal(t, 1, count("material_receipt", fmt.Sprint(movID)), "movement not duplicated")
	})

	// (d) maturation: a Stripe order with total_settled_base=NULL stays pending, then posts once filled.
	t.Run("settled_maturation", func(t *testing.T) {
		const uuid = "ACCTW-CARD-1"
		mkOrder(uuid, cardPM, "100", sql.NullString{}) // settled NULL
		require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  uuid,
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
			OccurredAt: occurredPaid,
		}))

		require.NoError(t, w.RunOnce(ctx))

		_, ok := getEntry(entity.AcctSourceOrderSale, uuid)
		require.False(t, ok, "no sale entry while settled base is pending")
		processed, lastErr := eventState(uuid)
		require.False(t, processed, "event stays pending")
		require.Equal(t, "settled pending", lastErr)

		// Capture the authoritative settlement, then let the deferred event become due again.
		_, err := testDB.ExecContext(ctx, "UPDATE customer_order SET total_settled_base = 95 WHERE uuid = ?", uuid)
		require.NoError(t, err)
		dueNow(uuid)

		require.NoError(t, w.RunOnce(ctx))

		full, ok := getEntry(entity.AcctSourceOrderSale, uuid)
		require.True(t, ok, "sale posts once settled base is present")
		requireBalanced(full)
		requireLine(full, "1030", entity.AcctSideDebit, "95")  // Stripe processor, gross = settled
		requireLine(full, "4020", entity.AcctSideCredit, "95") // DTC revenue (net)
		processed, _ = eventState(uuid)
		require.True(t, processed)
	})

	// (e) refund ordering: a refund seen before its sale defers, then posts once the sale exists.
	t.Run("refund_awaits_then_posts", func(t *testing.T) {
		const uuid = "ACCTW-REF-1"
		const refundKey = uuid + ":1"
		mkOrder(uuid, cardPM, "100", sql.NullString{String: "100", Valid: true}) // settled present

		// Refund event arrives first (sale not posted yet).
		require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType: entity.AcctEventOrderRefund,
			SourceKey: refundKey,
			Payload: entity.AcctOrderRefundPayload{
				OrderUUID:      uuid,
				RefundAmount:   decimal.NewFromInt(30),
				OrderCurrency:  "EUR",
				RefundedByItem: map[int]int64{},
			},
			OccurredAt: occurredRefund,
		}))

		require.NoError(t, w.RunOnce(ctx))
		_, ok := getEntry(entity.AcctSourceOrderRefund, refundKey)
		require.False(t, ok, "refund does not post before its sale")
		processed, lastErr := eventState(refundKey)
		require.False(t, processed, "refund event deferred")
		require.Equal(t, "awaiting sale posting", lastErr)

		// The sale event arrives and posts.
		require.NoError(t, acc.EnqueueEvent(ctx, entity.AcctEventInsert{
			EventType:  entity.AcctEventOrderPaid,
			SourceKey:  uuid,
			Payload:    entity.AcctOrderPaidPayload{OrderUUID: uuid},
			OccurredAt: occurredPaid,
		}))
		require.NoError(t, w.RunOnce(ctx))
		_, ok = getEntry(entity.AcctSourceOrderSale, uuid)
		require.True(t, ok, "sale posts")

		// Re-arm the deferred refund; now it posts (S2) against 4040 Returns / 1030 processor.
		dueNow(refundKey)
		require.NoError(t, w.RunOnce(ctx))

		full, ok := getEntry(entity.AcctSourceOrderRefund, refundKey)
		require.True(t, ok, "refund posts once the sale exists")
		requireBalanced(full)
		requireLine(full, "4040", entity.AcctSideDebit, "30")  // contra-revenue
		requireLine(full, "1030", entity.AcctSideCredit, "30") // money back to the processor
		processed, _ = eventState(refundKey)
		require.True(t, processed)
	})
}
