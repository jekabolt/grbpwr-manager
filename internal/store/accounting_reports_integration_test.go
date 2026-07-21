package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestAccountingReportsEndToEnd reproduces the docs/plan-accounting/04-posting-rules.md "сквозной
// пример" straight through the step-7 reports (06-reports.md). It posts the example's ledger via the
// pure internal/accounting builders + CreateJournalEntry (no worker, no operational-source rows) and
// asserts the control numbers: Trial Balance balances; the monthly P&L shows revenue 203.25, COGS
// 84.50, fee 7.55; the Balance Sheet nets 1030 to 0, holds 1130 at 338.00, CHK==0 and carries the
// virtual "Current Period Net Profit" row (111.20); the 1030 drill-down runs 250 → 242.45 → 0.
//
// Reconciliation is exercised for SHAPE and the ledger side only: the fixtures post journal entries
// directly, so there are no customer_order / material_stock_movement rows to reconcile against, and
// NewForTest does not load the order-status cache — so every operational figure is 0 and each delta
// equals the ledger figure. A delta==0 reconciliation would need a seeded operational fixture plus a
// loaded cache (store.New), which is out of scope for this ledger-focused test (see block comment).
//
// Integration test: runs only against a real MySQL (TestMain connects + migrates). Cleans up its own
// journal entries (acct_journal_line cascades) and the period row it lazily opened.
func TestAccountingReportsEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)

	dec := decimal.RequireFromString

	mar := func(day int) time.Time { return time.Date(2026, 3, day, 0, 0, 0, 0, time.UTC) }
	startDate := mar(1)
	movDate := mar(5)
	receiveDate := mar(10)
	saleDate := mar(15)
	payoutDate := mar(20)
	from := mar(1)
	to := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	asOf := mar(31)

	const orderUUID = "acct-report-test-order-0001"

	var entryIDs []int
	// post persists a built entry through CreateJournalEntry inside a Tx (the store never opens its
	// own), collecting the id for cleanup.
	post := func(entry entity.AcctJournalEntryInsert) {
		var id int
		err := s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e error
			id, _, e = rep.Accounting().CreateJournalEntry(ctx, entry)
			return e
		})
		require.NoError(t, err, "post %s/%s", entry.SourceType, entry.SourceKey)
		entryIDs = append(entryIDs, id)
	}
	defer func() {
		for _, id := range entryIDs {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_entry WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_period WHERE period = ?", "2026-03-01")
	}()

	// M1: purchase fabric 180 → Dr 1110 / Cr 2010 180.
	m1, err := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id: 900001, MaterialId: 900100, MovementType: entity.MaterialMovementReceipt,
			Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("180"), Valid: true},
			OccurredAt: sql.NullTime{Time: movDate, Valid: true}, CreatedAt: movDate,
		},
		MaterialName: "Test Fabric",
	}, startDate)
	require.NoError(t, err)
	post(m1)

	// M3 ×2: issue fabric 180 + trims 12.50 into the run → Dr 1120 / Cr 1110 192.50.
	m3a, err := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id: 900002, MaterialId: 900100, MovementType: entity.MaterialMovementIssueProduction,
			Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("180"), Valid: true},
			OccurredAt: sql.NullTime{Time: movDate, Valid: true}, CreatedAt: movDate,
		},
		MaterialName: "Test Fabric",
	}, startDate)
	require.NoError(t, err)
	post(m3a)
	m3b, err := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id: 900003, MaterialId: 900101, MovementType: entity.MaterialMovementIssueProduction,
			Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("12.50"), Valid: true},
			OccurredAt: sql.NullTime{Time: movDate, Valid: true}, CreatedAt: movDate,
		},
		MaterialName: "Test Trims",
	}, startDate)
	require.NoError(t, err)
	post(m3b)

	// P1: receive run — manual CMT 200 + overhead 30 = 230 → Dr 1120 / Cr 2010 230; WIP→FG
	// (230 + ledger WIP 192.50) = 422.50 → Dr 1130 / Cr 1120 422.50.
	p1, err := acctrules.BuildProductionReceiveEntry(entity.AcctRunFacts{
		RunID: 900010, ReceivedAt: receiveDate, TechCardName: "Test Jacket",
		Costs: []entity.ProductionRunCost{
			{AmountBase: decimal.NullDecimal{Decimal: dec("200"), Valid: true}},
			{AmountBase: decimal.NullDecimal{Decimal: dec("30"), Valid: true}},
		},
		Issues: []entity.AcctRunIssueFact{
			{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("180"), Valid: true}, CreatedAt: movDate},
			{MovementType: entity.MaterialMovementIssueProduction, Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("12.50"), Valid: true}, CreatedAt: movDate},
		},
	}, startDate)
	require.NoError(t, err)
	post(p1)

	// S1: sale of 1 unit for 250 (EUR, card/Stripe, VAT 23% incl 46.75, shipping 10, fee 7.55, COGS
	// 84.50). k=1 → NET 193.25, SHIP 10, VAT 46.75.
	s1, err := acctrules.BuildOrderSaleEntry(entity.AcctOrderFacts{
		Id: 900200, UUID: orderUUID, Placed: saleDate,
		TotalPrice:        dec("250"),
		Currency:          "EUR",
		TotalSettledBase:  decimal.NullDecimal{Decimal: dec("250"), Valid: true},
		PaymentFee:        decimal.NullDecimal{Decimal: dec("7.55"), Valid: true},
		VatAmount:         decimal.NullDecimal{Decimal: dec("46.75"), Valid: true},
		PaymentMethodId:   1,
		PaymentMethodName: entity.CARD,
		ShipmentCost:      decimal.NullDecimal{Decimal: dec("10"), Valid: true},
		FreeShipping:      sql.NullBool{Bool: false, Valid: true},
		Items: []entity.AcctOrderItemFact{
			{Id: 900300, ProductId: 900400, Quantity: dec("1"), UnitCost: decimal.NullDecimal{Decimal: dec("84.50"), Valid: true}},
		},
		// Phase 2: VAT now comes from the resolved regime. OSS 23% inclusive on G=250 → 46.75, matching
		// the 46.75 snapshot (no mismatch caveat).
	}, acctrules.VatDecision{Regime: entity.VatRegimeOSS, RatePct: dec("23")}, saleDate)
	require.NoError(t, err)
	require.False(t, s1.HasCaveat, "the fully-costed sale should carry no caveat")
	post(s1)

	// MN: Stripe payout to bank — Dr 1010 / Cr 1030 242.45. Zeroes the processor balance.
	post(entity.AcctJournalEntryInsert{
		OccurredAt:  payoutDate,
		Description: "stripe payout to bank (test)",
		SourceType:  entity.AcctSourceManual,
		SourceKey:   "manual:acct-report-test-payout",
		CreatedBy:   "test",
		Lines: []entity.AcctJournalLineInsert{
			{AccountCode: "1010", Side: entity.AcctSideDebit, Amount: dec("242.45")},
			{AccountCode: "1030", Side: entity.AcctSideCredit, Amount: dec("242.45")},
		},
	})

	eq := func(want string, got decimal.Decimal, msg string) {
		require.Equal(t, want, got.StringFixed(2), msg)
	}

	// --- Trial Balance ---
	tb, err := s.Accounting().GetTrialBalance(ctx, from, to)
	require.NoError(t, err)
	require.True(t, tb.Balanced, "trial balance must balance")
	require.True(t, tb.TotalDebit.Equal(tb.TotalCredit), "ΣDr == ΣCr")
	eq("1609.50", tb.TotalDebit, "total debit turnover")

	// --- P&L (single month column: March 2026) ---
	pl, err := s.Accounting().GetProfitLoss(ctx, from, to)
	require.NoError(t, err)
	require.Len(t, pl.Months, 1, "one month column")
	eq("203.25", pl.TotalRevenue[0], "revenue = 4020 193.25 + 4110 10")
	eq("84.50", pl.NetCogs[0], "net cogs = 5010")
	eq("7.55", pl.TotalOpex[0], "opex = 6050 fee")
	eq("118.75", pl.GrossProfit[0], "gross profit = revenue - cogs")
	eq("111.20", pl.OperatingProfit[0], "operating profit = gross - opex")
	require.Equal(t, "7.55", plRowTotal(t, pl, "6050").StringFixed(2), "6050 fee row total")

	// --- Balance Sheet (as of 2026-03-31) ---
	bs, err := s.Accounting().GetBalanceSheet(ctx, asOf)
	require.NoError(t, err)
	eq("0.00", assetBalanceOrZero(bs, "1030"), "1030 processor nets to zero after payout")
	eq("338.00", assetBalanceOrZero(bs, "1130"), "1130 finished goods = 422.50 - 84.50")
	eq("0.00", bs.BalanceCheck, "CHK = assets - (liab + equity) == 0")
	require.True(t, bs.Balanced, "balance sheet must balance")
	np := findEquityRow(bs, "Current Period Net Profit")
	require.NotNil(t, np, "virtual Current Period Net Profit row must be present in equity")
	eq("111.20", np.Balance, "net profit == P&L operating profit for the span")

	// --- Drill-down: 1030 Payment Processor ---
	ledger, err := s.Accounting().GetAccountLedger(ctx, "1030", entity.AcctLedgerFilter{From: from, To: to})
	require.NoError(t, err)
	require.Equal(t, 3, ledger.Total, "three lines touch 1030")
	require.Len(t, ledger.Rows, 3)
	eq("0.00", ledger.OpeningBalance, "no 1030 activity before the period")
	eq("250.00", ledger.Rows[0].RunningBalance, "after Dr 250")
	eq("242.45", ledger.Rows[1].RunningBalance, "after Cr 7.55 fee")
	eq("0.00", ledger.Rows[2].RunningBalance, "after Cr 242.45 payout")
	eq("0.00", ledger.ClosingBalance, "1030 closes at zero")

	// --- Reconciliation (shape + ledger side; operational is 0, see block comment) ---
	rec, err := s.Accounting().GetReconciliation(ctx, from, to)
	require.NoError(t, err)
	require.Equal(t, "revenue", rec.Revenue.Name)
	eq("203.25", rec.Revenue.Ledger, "recon revenue ledger = NET+SHIP on order_sale entries")
	eq("0.00", rec.Revenue.Operational, "no operational orders + empty status cache in NewForTest")
	eq("203.25", rec.Revenue.Delta, "delta = ledger - operational")
	eq("7.55", rec.Fees.Ledger, "recon fees ledger = 6050")
	eq("84.50", rec.COGS.Ledger, "recon cogs ledger = 5010")
	eq("338.00", rec.FinishedGoods.Ledger, "recon FG ledger = 1130 balance")
	eq("-12.50", rec.Materials.Ledger, "recon materials ledger = 1110 balance (example issues trims never purchased)")
}

// plRowTotal returns the row total for a P&L account code across all sections (fails if absent).
func plRowTotal(t *testing.T, pl *entity.AcctProfitLoss, code string) decimal.Decimal {
	t.Helper()
	for _, sec := range pl.Sections {
		for _, r := range sec.Rows {
			if r.Code == code {
				return r.Total
			}
		}
	}
	t.Fatalf("P&L row %s not found", code)
	return decimal.Zero
}

// assetBalanceOrZero returns a BS asset row's balance, or zero when the account is absent (a
// zero-balance account is omitted from the report — netted-out 1030 is checked this way).
func assetBalanceOrZero(bs *entity.AcctBalanceSheet, code string) decimal.Decimal {
	for _, r := range bs.Assets.Rows {
		if r.Code == code {
			return r.Balance
		}
	}
	return decimal.Zero
}

// findEquityRow returns the equity row with the given name, or nil.
func findEquityRow(bs *entity.AcctBalanceSheet, name string) *entity.AcctBalanceSheetRow {
	for i := range bs.Equity.Rows {
		if bs.Equity.Rows[i].Name == name {
			return &bs.Equity.Rows[i]
		}
	}
	return nil
}

// TestAccountingReconciliationRevenueDeltaZero is the wave-0 techdebt item
// (docs/plan-accounting-phase2/06-wave0-techdebt.md #4): TestAccountingReportsEndToEnd's own block
// comment says its reconciliation assertions exercise "SHAPE and the ledger side only" because that
// test posts journal entries directly with no customer_order fixture behind them, and NewForTest does
// not load the order-status cache by itself — so rec.Revenue.Operational reads 0 there and Delta
// always just echoes the ledger figure. The genuine delta==0 path (ledger and operational actually
// agreeing) had no coverage. This test closes that gap without touching
// TestAccountingReportsEndToEnd: it warms the order-status cache the same way
// TestAcctEventProducers/TestAcctPostingWorker do (NewForTest alone does not populate it — see its own
// doc comment), seeds a real customer_order/payment/order_item fixture, reads it back with
// GetOrderFactsForPosting (the exact fact source the acctposting worker itself uses) and posts it
// through BuildOrderSaleEntry — so the ledger and operational sides are DERIVED FROM THE SAME ROW
// rather than two independently hand-typed numbers that merely happen to match. It runs in an
// isolated period (2033-02, grepped clean of every other suite in this package) so it cannot collide
// with any other test's revenue figures for that month.
//
// Per 06-wave0-techdebt.md #4 ("если кэш недоступен в харнессе, сделай сценарий best-effort"): if the
// order-status cache fails to warm (e.g. a minimal harness with no dictionary/hero seed data), the
// scenario is skipped rather than failed — GetReconciliation degrades to ledger-only in that case
// (reconcile.go's own empty-cache guard), which cannot exercise a genuine delta==0.
func TestAccountingReconciliationRevenueDeltaZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	if len(cache.OrderStatusIDsForNetRevenue()) == 0 {
		t.Skip("order-status cache did not populate a net-revenue status set in this harness; " +
			"the delta==0 scenario needs it (best-effort per 06-wave0-techdebt.md #4)")
	}

	const uuid = "acct-report-delta-zero-0001"
	month := time.Date(2033, 2, 15, 12, 0, 0, 0, time.UTC)
	from := time.Date(2033, 2, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2033, 3, 1, 0, 0, 0, 0, time.UTC)

	var sizeA int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE sku_ord != 0 ORDER BY id LIMIT 1`).Scan(&sizeA))
	var langID, pmID, confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM language").Scan(&langID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM payment_method WHERE name = ?", string(entity.CARD)).Scan(&pmID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	// A real, throwaway product (order_item.variant_id is a NOT NULL FK RESTRICT to product_size —
	// see migration 0153 — so the operational fixture needs a genuine variant, not a bare id).
	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	prices := make([]entity.ColorwayPriceInsert, 0)
	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}
	prodID, err := s.Products().AddProduct(ctx, &entity.ColorwayNew{
		Product: &entity.ColorwayInsert{
			ProductBodyInsert: entity.ColorwayBodyInsert{
				Brand: "ACME", Color: "black", ColorCode: "BLK", ColorHexOverride: sql.NullString{String: "#000000", Valid: true}, CountryOfOrigin: "IT",
				TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
			},
			ThumbnailMediaID: mediaID,
			Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: "ACCT-DELTA-ZERO", Description: "d"}},
			Prices:           prices,
		},
		SizeMeasurements: []entity.SizeWithMeasurementInsert{
			{ProductSize: entity.VariantInsert{SizeId: sizeA, Quantity: decimal.NewFromInt(5)}},
		},
		MediaIds: []int{mediaID}, Tags: []entity.ColorwayTagInsert{}, Prices: prices,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", prodID) })
	var variantID int64
	var variantSKU string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id, sku FROM product_size WHERE product_id = ? AND size_id = ?`, prodID, sizeA).
		Scan(&variantID, &variantSKU))

	// Operational fixture: total_price 100, total_settled_base 100 (k=1, no FX), vat_amount 20, no
	// shipment row (ship=0) — so both the ledger builder and the recon SQL reduce to the SAME formula
	// G - VAT = 80.00, by construction rather than coincidence (see block comment above).
	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, total_settled_base, vat_amount, placed)
		VALUES (?, ?, 'EUR', 100, 100, 20, ?)`, uuid, confirmedID, month)
	require.NoError(t, err)
	orderID, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM payment WHERE order_id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_type = 'order_sale' AND source_key = ?", uuid)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = '2033-02-01'")
	})
	_, err = testDB.ExecContext(ctx,
		`INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done)
		 VALUES (?, ?, 100, 100, 1)`, orderID, pmID)
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO order_item
		(order_id, product_id, variant_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id, variant_sku_snapshot)
		VALUES (?, ?, ?, 100, 100, 40, 0, 1, ?, ?)`, orderID, prodID, variantID, sizeA, variantSKU)
	require.NoError(t, err)

	// Ledger fixture: post the SAME row's facts (via the worker's own fact-reader) through the sale
	// builder, so the journal entry is not an independent guess at what the order "should" have been.
	facts, err := s.Accounting().GetOrderFactsForPosting(ctx, uuid)
	require.NoError(t, err)
	// Phase 2: post via an explicit OSS 25% decision so the regime VAT (100*25/125 = 20.00) reproduces
	// the order's 20.00 snapshot — keeping G − VAT = 80.00 for the revenue reconciliation below. (The
	// order fixture has no address, so the resolver alone would classify it export; the explicit
	// decision isolates the reconciliation from destination resolution.)
	entry, err := acctrules.BuildOrderSaleEntry(*facts, acctrules.VatDecision{Regime: entity.VatRegimeOSS, RatePct: decimal.RequireFromString("25")}, month)
	require.NoError(t, err)
	require.False(t, entry.HasCaveat, "settled base present, VAT set, one costed item: no caveat expected")
	require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, _, e := rep.Accounting().CreateJournalEntry(ctx, entry)
		return e
	}))

	rec, err := s.Accounting().GetReconciliation(ctx, from, to)
	require.NoError(t, err)
	require.Equal(t, "80.00", rec.Revenue.Ledger.StringFixed(2), "ledger = NET credited by the order_sale entry (G 100 - VAT 20)")
	require.Equal(t, "80.00", rec.Revenue.Operational.StringFixed(2), "operational = settled (100) - VAT share (20) on the Confirmed order")
	require.True(t, rec.Revenue.Delta.IsZero(), "ledger and operational are derived from the same row: delta must be exactly 0, got %s", rec.Revenue.Delta)
}
