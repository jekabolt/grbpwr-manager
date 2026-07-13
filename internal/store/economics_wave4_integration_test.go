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

// TestRefundLineLevelApportionment exercises the task-12 change: when an order carries
// line-level refund detail (refunded_order_item rows), revenue/COGS apportionment shrinks
// ONLY the refunded line. A two-item order (coat + t-shirt, €100 list / €40 cost each)
// where the coat is fully returned must report the coat at zero revenue/cost and the kept
// t-shirt at its FULL €100 revenue / €40 cost — not the old order-level ratio
// (total_price-refunded)/total = 0.5 that bled the refund across both lines and would have
// left each at half. Throwaway harness; cleans up in reverse-dependency order.
func TestRefundLineLevelApportionment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The apportion SQL reads the global cache (net-revenue status ids, base currency).
	// NewForTest does not populate it (only New() does), so initialize it here.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	var mediaID, coatID, tshirtID, sizeID, statusID int
	var orderID int64
	defer func() {
		if orderID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM refunded_order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		}
		for _, pid := range []int{coatID, tshirtID} {
			if pid != 0 {
				_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", pid)
			}
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.PartiallyRefunded)).Scan(&statusID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	// media (product thumbnail FK target)
	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	mkProduct := func(sku string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
			VALUES (?, 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, sku, mediaID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	coatID = mkProduct("ECO-W4-COAT")
	tshirtID = mkProduct("ECO-W4-TSHIRT")

	// A partially_refunded order (a net-revenue status) placed inside the window.
	// total_settled_base == the reconstructed base (200) so the FX factor is exactly 1;
	// VAT 0; no promo, no shipment. refunded_amount 100 mirrors the returned coat but is
	// only consulted by the ORDER-level fallback — the line detail below overrides it.
	placed := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, refunded_amount, total_settled_base, placed)
		VALUES ('ECO-W4-REFUND-0001', ?, 'EUR', 200, 100, 200, ?)`, statusID, placed)
	require.NoError(t, err)
	orderID, err = res.LastInsertId()
	require.NoError(t, err)

	// Two lines, €100 base list / €40 base cost each, both snapshots set directly so no
	// product_price / product.cost_price lookup is needed.
	mkItem := func(prodID int) int64 {
		res, err := testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id)
			VALUES (?, ?, 100, 100, 40, 0, 1, ?)`, orderID, prodID, sizeID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	coatItemID := mkItem(coatID)
	_ = mkItem(tshirtID)

	// The coat is fully returned (1 of 1 unit); the t-shirt is kept.
	_, err = testDB.ExecContext(ctx, `INSERT INTO refunded_order_item
		(order_id, order_item_id, quantity_refunded) VALUES (?, ?, 1)`, orderID, coatItemID)
	require.NoError(t, err)

	rows, err := s.Metrics().GetSlowMovers(ctx, placed.Add(-24*time.Hour), placed.Add(24*time.Hour), 10000)
	require.NoError(t, err)

	byID := map[int]entity.SlowMoverRow{}
	for _, r := range rows {
		byID[r.ProductID] = r
	}
	coat, ok := byID[coatID]
	require.True(t, ok, "coat present in slow movers")
	tshirt, ok := byID[tshirtID]
	require.True(t, ok, "t-shirt present in slow movers")

	// Coat fully refunded → its line contributes zero net revenue and zero net cost.
	require.True(t, coat.Revenue.IsZero(), "refunded coat revenue should be 0, got %s", coat.Revenue)
	require.True(t, coat.RevenueCost.IsZero(), "refunded coat cost should be 0, got %s", coat.RevenueCost)

	// T-shirt kept → full €100 revenue and €40 cost, NOT halved by order-level proration.
	require.True(t, tshirt.Revenue.Equal(decimal.NewFromInt(100)),
		"kept t-shirt revenue should be 100, got %s", tshirt.Revenue)
	require.True(t, tshirt.RevenueCost.Equal(decimal.NewFromInt(40)),
		"kept t-shirt cost should be 40, got %s", tshirt.RevenueCost)

	// Smoke the other apportion SQL shape — GetProductTrend builds TWO order_factors CTEs
	// (current + previous) and references itemAdj on both aliases, so the per-line refund
	// subquery / has_line_refunds column must resolve there too. Just assert it executes.
	_, err = s.Metrics().GetProductTrend(ctx, placed.Add(-24*time.Hour), placed.Add(24*time.Hour), 100)
	require.NoError(t, err, "product trend (dual order_factors CTE) must run with per-line refund ratio")
}

// TestMarginByStyleAndCogsStructure exercises task-15 Part A (GetMarginByStyle) and Part B
// (GetCogsStructure). One style (tech card) has two colourway SKUs that both sold; a third
// product has no style. GetMarginByStyle must collapse the two colourways into one style row
// (colorway_count=2, summed revenue/cost) and put the third in the tech_card_id=0 "no style"
// row. GetCogsStructure must split each coat line's COGS by its cost_breakdown proportions
// (materials 6 / cmt 4 of a 10 unit ⇒ 60/40) and bucket the breakdown-less t-shirt as
// "unattributed", with the components summing to the total COGS.
func TestMarginByStyleAndCogsStructure(t *testing.T) {
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

	var mediaID, coatBlackID, coatWhiteID, tshirtID, sizeID, statusID, techCardID int
	var orderID int64
	defer func() {
		if orderID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
		for _, pid := range []int{coatBlackID, coatWhiteID, tshirtID} {
			if pid != 0 {
				_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", pid)
			}
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&statusID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	mkProduct := func(sku string) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
			VALUES (?, 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, sku, mediaID)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return int(id)
	}
	coatBlackID = mkProduct("ECO-W4-COAT-BLK")
	coatWhiteID = mkProduct("ECO-W4-COAT-WHT")
	tshirtID = mkProduct("ECO-W4-STYLE-TSHIRT")

	// A style (tech card) linking the two coat colourways; set as their primary card.
	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     "ECO-W4-STYLE-1",
		Name:            "The Coat",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{sizeID},
		ProductIds:      []int{coatBlackID, coatWhiteID},
	})
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx,
		"UPDATE product SET primary_tech_card_id = ? WHERE id IN (?, ?)", techCardID, coatBlackID, coatWhiteID)
	require.NoError(t, err)

	// cost_breakdown snapshot on the coats: materials 6 + cmt 4 = unit 10. The t-shirt has none.
	cb := `{"materials":6,"cmt":4,"hardware":0,"packaging":0,"logistics":0,"overhead":0,"defect_pct":0}`
	_, err = testDB.ExecContext(ctx,
		"UPDATE product SET cost_breakdown = ? WHERE id IN (?, ?)", cb, coatBlackID, coatWhiteID)
	require.NoError(t, err)

	// A confirmed order placed in a tight, unique window. total_settled_base == items_base_total
	// (250) so the apportion factor is 1; VAT 0; no refund/promo/shipment.
	placed := time.Date(2026, 2, 11, 12, 0, 0, 0, time.UTC)
	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, refunded_amount, total_settled_base, placed)
		VALUES ('ECO-W4-STYLE-0001', ?, 'EUR', 250, 0, 250, ?)`, statusID, placed)
	require.NoError(t, err)
	orderID, err = res.LastInsertId()
	require.NoError(t, err)

	// coat lines €100 list / €10 cost; t-shirt €50 list / €20 cost.
	mkItem := func(prodID int, price, cost int) {
		_, err := testDB.ExecContext(ctx, `INSERT INTO order_item
			(order_id, product_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id)
			VALUES (?, ?, ?, ?, ?, 0, 1, ?)`, orderID, prodID, price, price, cost, sizeID)
		require.NoError(t, err)
	}
	mkItem(coatBlackID, 100, 10)
	mkItem(coatWhiteID, 100, 10)
	mkItem(tshirtID, 50, 20)

	from, to := placed.Add(-24*time.Hour), placed.Add(24*time.Hour)

	// --- Part A: GetMarginByStyle ---
	styles, err := s.Metrics().GetMarginByStyle(ctx, from, to, 100)
	require.NoError(t, err)
	byStyle := map[int]entity.MarginByStyleRow{}
	for _, r := range styles {
		byStyle[r.TechCardID] = r
	}
	style, ok := byStyle[techCardID]
	require.True(t, ok, "the coat style row must be present")
	require.Equal(t, "ECO-W4-STYLE-1", style.StyleNumber)
	require.Equal(t, "The Coat", style.Name)
	require.Equal(t, int64(2), style.UnitsSold, "two coat units")
	require.Equal(t, 2, style.ColorwayCount, "two distinct colourway SKUs")
	require.True(t, style.Revenue.Equal(decimal.NewFromInt(200)), "style revenue 100+100, got %s", style.Revenue)
	require.True(t, style.RevenueCost.Equal(decimal.NewFromInt(20)), "style COGS 10+10, got %s", style.RevenueCost)
	require.True(t, style.HasCost)

	noStyle, ok := byStyle[0]
	require.True(t, ok, "the no-style (t-shirt) row must be present")
	require.Equal(t, int64(1), noStyle.UnitsSold)
	require.True(t, noStyle.Revenue.Equal(decimal.NewFromInt(50)), "no-style revenue 50, got %s", noStyle.Revenue)

	// --- Part C: GetStyleMargin (lifetime single-style; anchor of GetStyleEconomics) ---
	one, err := s.Metrics().GetStyleMargin(ctx, techCardID)
	require.NoError(t, err)
	require.NotNil(t, one, "the coat style has sales")
	require.Equal(t, techCardID, one.TechCardID)
	require.Equal(t, "ECO-W4-STYLE-1", one.StyleNumber)
	require.Equal(t, int64(2), one.UnitsSold, "two coat units, lifetime")
	require.Equal(t, 2, one.ColorwayCount)
	require.True(t, one.Revenue.Equal(decimal.NewFromInt(200)), "style revenue 200, got %s", one.Revenue)
	require.True(t, one.RevenueCost.Equal(decimal.NewFromInt(20)), "style COGS 20, got %s", one.RevenueCost)
	require.True(t, one.GrossMargin.Equal(decimal.NewFromInt(180)), "gross margin 180, got %s", one.GrossMargin)
	require.True(t, one.HasCost)
	// A tech card with no sales returns nil (the handler fills identity from the card).
	none, err := s.Metrics().GetStyleMargin(ctx, 999999)
	require.NoError(t, err)
	require.Nil(t, none)

	// --- Part B: GetCogsStructure ---
	comps, err := s.Metrics().GetCogsStructure(ctx, from, to)
	require.NoError(t, err)
	byComp := map[string]decimal.Decimal{}
	total := decimal.Zero
	for _, c := range comps {
		byComp[c.Component] = c.Amount
		total = total.Add(c.Amount)
	}
	// Two coat lines: each €10 COGS split 6/10 materials, 4/10 cmt ⇒ materials 12, cmt 8.
	require.True(t, byComp["materials"].Equal(decimal.NewFromInt(12)), "materials 12, got %s", byComp["materials"])
	require.True(t, byComp["cmt"].Equal(decimal.NewFromInt(8)), "cmt 8, got %s", byComp["cmt"])
	// The breakdown-less t-shirt line (€20 COGS) is unattributed.
	require.True(t, byComp["unattributed"].Equal(decimal.NewFromInt(20)), "unattributed 20, got %s", byComp["unattributed"])
	// Components sum to the total COGS (12 + 8 + 20).
	require.True(t, total.Equal(decimal.NewFromInt(40)), "total COGS 40, got %s", total)
}

// TestInventoryValuation exercises task-16 GetInventoryValuation: stock valued at cost_price,
// dead stock (in stock but unsold in the window), uncosted-stock honesty, and damage/loss
// write-offs. Product A (cost 10, 5 on hand) sells in the window; B (cost 20, 3 on hand) does
// not → dead stock; C (no cost, 4 on hand) is uncosted. A damage write-off of 2 units of A is
// booked in the window. Assertions target the specific products so leftover DB stock can't
// break them; the windowed write-off total is asserted exactly.
func TestInventoryValuation(t *testing.T) {
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

	var mediaID, prodA, prodB, prodC, sizeID, statusID int
	var orderID int64
	defer func() {
		if orderID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM order_item WHERE order_id = ?", orderID)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM customer_order WHERE id = ?", orderID)
		}
		for _, pid := range []int{prodA, prodB, prodC} {
			if pid != 0 {
				// product_size + product_stock_change_history cascade on product delete.
				_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", pid)
			}
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&statusID))
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT MIN(id) FROM size").Scan(&sizeID))

	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)

	mkProduct := func(sku string, cost string, onHand int) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
			VALUES (?, 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, sku, mediaID)
		require.NoError(t, err)
		id64, err := res.LastInsertId()
		require.NoError(t, err)
		id := int(id64)
		if cost != "" {
			_, err = testDB.ExecContext(ctx, "UPDATE product SET cost_price = ? WHERE id = ?", cost, id)
			require.NoError(t, err)
		}
		_, err = testDB.ExecContext(ctx,
			"INSERT INTO product_size (product_id, size_id, quantity) VALUES (?, ?, ?)", id, sizeID, onHand)
		require.NoError(t, err)
		return id
	}
	prodA = mkProduct("ECO-W4-INV-A", "10", 5) // costed, sells → not dead
	prodB = mkProduct("ECO-W4-INV-B", "20", 3) // costed, no sale → dead stock
	prodC = mkProduct("ECO-W4-INV-C", "", 4)   // uncosted

	// Window: sale of A + the write-off both fall inside it; B has no sale.
	winStart := time.Date(2026, 2, 12, 0, 0, 0, 0, time.UTC)
	from, to := winStart, winStart.Add(24*time.Hour)
	placed := winStart.Add(12 * time.Hour)

	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, refunded_amount, total_settled_base, placed)
		VALUES ('ECO-W4-INV-0001', ?, 'EUR', 30, 0, 30, ?)`, statusID, placed)
	require.NoError(t, err)
	orderID, err = res.LastInsertId()
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO order_item
		(order_id, product_id, product_price, product_price_base, cost_price_at_sale, product_sale_percentage, quantity, size_id)
		VALUES (?, ?, 30, 30, 10, 0, 1, ?)`, orderID, prodA, sizeID)
	require.NoError(t, err)

	// Damage write-off: 2 units of A removed in the window.
	_, err = testDB.ExecContext(ctx, `INSERT INTO product_stock_change_history
		(product_id, size_id, quantity_delta, quantity_after, source, reason, created_at)
		VALUES (?, ?, -2, 3, 'manual_adjustment', 'damage', ?)`, prodA, sizeID, placed)
	require.NoError(t, err)

	v, err := s.Metrics().GetInventoryValuation(ctx, from, to, 10000)
	require.NoError(t, err)

	byID := map[int]entity.InventoryValuationRow{}
	for _, r := range v.TopByValue {
		byID[r.ProductID] = r
	}
	// A and B appear in top-by-value with their frozen value; C (uncosted) does not.
	rowA, okA := byID[prodA]
	require.True(t, okA, "A in top-by-value")
	require.True(t, rowA.Value.Equal(decimal.NewFromInt(50)), "A value 10×5=50, got %s", rowA.Value)
	require.Equal(t, int64(1), rowA.SoldUnits, "A sold 1 in window")
	rowB, okB := byID[prodB]
	require.True(t, okB, "B in top-by-value")
	require.True(t, rowB.Value.Equal(decimal.NewFromInt(60)), "B value 20×3=60, got %s", rowB.Value)
	_, okC := byID[prodC]
	require.False(t, okC, "uncosted C is not valued")

	// Dead stock contains B (unsold) but not A (sold in window).
	deadIDs := map[int]bool{}
	for _, r := range v.DeadStock {
		deadIDs[r.ProductID] = true
	}
	require.True(t, deadIDs[prodB], "B is dead stock (unsold, in stock)")
	require.False(t, deadIDs[prodA], "A sold in window → not dead")

	// Totals include our contributions (>= because the shared test DB may hold other stock).
	require.True(t, v.TotalStockValue.GreaterThanOrEqual(decimal.NewFromInt(110)),
		"total value ≥ 50+60, got %s", v.TotalStockValue)
	require.GreaterOrEqual(t, v.UncostedStockUnits, int64(4), "C's 4 uncosted units counted")
	require.GreaterOrEqual(t, v.UncostedStockProducts, 1)

	// Write-offs are windowed → exactly our one damage row: 2 units × cost 10 = 20.
	require.True(t, v.WriteOffsValue.Equal(decimal.NewFromInt(20)), "write-off value 20, got %s", v.WriteOffsValue)
	require.Equal(t, int64(2), v.WriteOffsUnits)
}

// TestSeedProductsCostBreakdownFromTechCard covers the task-15 store write: the breakdown JSON
// is written onto a product whose primary card is this one and whose cost is not manual (same
// predicate as the cost_price seed), a manual cost is never touched, and a NULL clears a stale
// breakdown.
func TestSeedProductsCostBreakdownFromTechCard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var mediaID, prodID, techCardID int
	defer func() {
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
		if prodID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", prodID)
		}
		if mediaID != 0 {
			_ = s.Media().DeleteMediaById(ctx, mediaID)
		}
	}()

	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
	})
	require.NoError(t, err)
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, brand, color, color_hex, country_of_origin, thumbnail_id, top_category_id, target_gender, version)
		VALUES ('ECO-W4-SEED-BD', 'b', 'c', '#000000', 'US', ?, 1, 'unisex', 'v1')`, mediaID)
	require.NoError(t, err)
	pid64, err := res.LastInsertId()
	require.NoError(t, err)
	prodID = int(pid64)

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     "ECO-W4-SEED-1",
		Name:            "n",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
		ProductIds:      []int{prodID},
	})
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, "UPDATE product SET primary_tech_card_id = ? WHERE id = ?", techCardID, prodID)
	require.NoError(t, err)

	P := s.Products()
	cb := sql.NullString{String: `{"materials":6,"cmt":4,"hardware":0,"packaging":0,"logistics":0,"overhead":0,"defect_pct":0}`, Valid: true}

	// non-manual primary product gets the breakdown.
	n, err := P.SeedProductsCostBreakdownFromTechCard(ctx, techCardID, cb)
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	var got sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT cost_breakdown FROM product WHERE id = ?", prodID).Scan(&got))
	require.True(t, got.Valid && len(got.String) > 0, "breakdown written: %+v", got)

	// a manual cost is never touched.
	_, err = testDB.ExecContext(ctx, "UPDATE product SET cost_price_source='manual' WHERE id = ?", prodID)
	require.NoError(t, err)
	n, err = P.SeedProductsCostBreakdownFromTechCard(ctx, techCardID, sql.NullString{})
	require.NoError(t, err)
	require.Equal(t, int64(0), n, "manual cost breakdown not cleared by seed")

	// back to tech_card source: a NULL clears the stale breakdown.
	_, err = testDB.ExecContext(ctx, "UPDATE product SET cost_price_source='tech_card' WHERE id = ?", prodID)
	require.NoError(t, err)
	n, err = P.SeedProductsCostBreakdownFromTechCard(ctx, techCardID, sql.NullString{})
	require.NoError(t, err)
	require.Equal(t, int64(1), n)
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT cost_breakdown FROM product WHERE id = ?", prodID).Scan(&got))
	require.False(t, got.Valid, "breakdown cleared to NULL, got %+v", got)
}

// TestTechCardDevExpenseCRUD covers the task-14 development-cost journal store methods: append
// (returns the stored row with server-stamped created_at and folded amount_base), list
// (newest-first), and delete a single row.
func TestTechCardDevExpenseCRUD(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var techCardID int
	defer func() {
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID) // dev expenses cascade
		}
	}()

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     "ECO-W4-DEV-1",
		Name:            "n",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	T := s.TechCards()
	mk := func(kind, amount, base string) entity.TechCardDevExpense {
		e := entity.TechCardDevExpense{
			TechCardId: techCardID,
			Kind:       kind,
			Amount:     decimal.RequireFromString(amount),
			Currency:   "EUR",
		}
		if base != "" {
			e.AmountBase = decimal.NullDecimal{Decimal: decimal.RequireFromString(base), Valid: true}
		}
		return e
	}

	sample, err := T.AddTechCardDevExpense(ctx, mk("sample", "120.50", "120.50"))
	require.NoError(t, err)
	require.NotZero(t, sample.Id, "returned row has an id")
	require.False(t, sample.CreatedAt.IsZero(), "created_at is server-stamped")
	require.True(t, sample.AmountBase.Valid && sample.AmountBase.Decimal.Equal(decimal.RequireFromString("120.5")),
		"amount_base persisted: %+v", sample.AmountBase)

	labour, err := T.AddTechCardDevExpense(ctx, mk("labour", "60", "60"))
	require.NoError(t, err)

	list, err := T.ListTechCardDevExpenses(ctx, techCardID)
	require.NoError(t, err)
	require.Len(t, list, 2)

	// delete one → list shrinks to the other.
	require.NoError(t, T.DeleteTechCardDevExpense(ctx, sample.Id))
	list, err = T.ListTechCardDevExpenses(ctx, techCardID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, labour.Id, list[0].Id)
}

// TestFittingRoundAndChangeRequests exercises task-13: round_number auto-assignment per tech
// card, the structured outcome, and the full-replace change-request list (incl. resolving one).
func TestFittingRoundAndChangeRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	var techCardID int
	var fittingIDs []int
	defer func() {
		for _, id := range fittingIDs {
			_ = s.Fittings().DeleteFitting(ctx, id)
		}
		if techCardID != 0 {
			_ = s.TechCards().DeleteTechCard(ctx, techCardID)
		}
	}()

	techCardID, err = s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     "ECO-W4-FIT-1",
		Name:            "n",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{4},
	})
	require.NoError(t, err)

	tcRef := sql.NullInt32{Int32: int32(techCardID), Valid: true}
	date := time.Date(2026, 2, 20, 0, 0, 0, 0, time.UTC)
	mkFitting := func() *entity.FittingInsert {
		return &entity.FittingInsert{
			TechCardId:  tcRef,
			FittingDate: date,
			Status:      entity.FittingPlanned,
			Verdict:     entity.FittingPending,
		}
	}

	// Round numbers auto-assign 1, 2 per tech card.
	id1, err := s.Fittings().AddFitting(ctx, mkFitting())
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, id1)
	id2, err := s.Fittings().AddFitting(ctx, mkFitting())
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, id2)

	f1, err := s.Fittings().GetFittingById(ctx, id1)
	require.NoError(t, err)
	require.True(t, f1.RoundNumber.Valid && f1.RoundNumber.Int32 == 1, "first round = 1, got %+v", f1.RoundNumber)
	f2, err := s.Fittings().GetFittingById(ctx, id2)
	require.NoError(t, err)
	require.True(t, f2.RoundNumber.Valid && f2.RoundNumber.Int32 == 2, "second round = 2, got %+v", f2.RoundNumber)

	// A fitting with an outcome and a change request.
	f3ins := mkFitting()
	f3ins.Outcome = sql.NullString{String: "new_round", Valid: true}
	f3ins.ChangeRequests = []entity.FittingChangeRequest{
		{Target: "pattern", Note: "raise the hem 2cm", Resolved: false},
		{Target: "construction", Note: "reinforce the shoulder seam", Resolved: false},
	}
	id3, err := s.Fittings().AddFitting(ctx, f3ins)
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, id3)

	f3, err := s.Fittings().GetFittingById(ctx, id3)
	require.NoError(t, err)
	require.True(t, f3.RoundNumber.Valid && f3.RoundNumber.Int32 == 3, "third round = 3, got %+v", f3.RoundNumber)
	require.Equal(t, "new_round", f3.Outcome.String)
	require.Len(t, f3.ChangeRequests, 2)
	require.Equal(t, "pattern", f3.ChangeRequests[0].Target)
	require.False(t, f3.ChangeRequests[0].Resolved)

	// Resolve the first change request via a full-replace update (echo the fetched insert,
	// toggling resolved). round_number is preserved because the update echoes it.
	upd := f3.FittingInsert
	upd.ChangeRequests[0].Resolved = true
	require.NoError(t, s.Fittings().UpdateFitting(ctx, id3, &upd))

	f3b, err := s.Fittings().GetFittingById(ctx, id3)
	require.NoError(t, err)
	require.True(t, f3b.RoundNumber.Valid && f3b.RoundNumber.Int32 == 3, "round preserved on update, got %+v", f3b.RoundNumber)
	require.Len(t, f3b.ChangeRequests, 2)
	require.True(t, f3b.ChangeRequests[0].Resolved, "first change request resolved")
	require.False(t, f3b.ChangeRequests[1].Resolved, "second still open")

	// A manual round_number is honoured (not overwritten by auto-assign).
	manual := mkFitting()
	manual.RoundNumber = sql.NullInt32{Int32: 99, Valid: true}
	idM, err := s.Fittings().AddFitting(ctx, manual)
	require.NoError(t, err)
	fittingIDs = append(fittingIDs, idM)
	fM, err := s.Fittings().GetFittingById(ctx, idM)
	require.NoError(t, err)
	require.True(t, fM.RoundNumber.Valid && fM.RoundNumber.Int32 == 99, "manual round honoured, got %+v", fM.RoundNumber)
}
