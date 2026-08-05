package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// ReduceStockForProductSizes reduces stock for product sizes atomically. Each line decrements
// its OWN grade's row (item.Grade, empty = 'A'): a sold '-B' line must never drain A stock (0251).
func (s *Store) ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error {
	var historyEntries []entity.StockChangeInsert
	for _, item := range items {
		grade := entity.NormalizeVariantGrade(item.Grade)
		quantityBefore, exists, err := getProductSizeStockGrade(ctx, s.DB, item.ProductId, item.SizeId, grade)
		if err != nil {
			return fmt.Errorf("error checking current quantity: %w", err)
		}
		if !exists {
			return fmt.Errorf("product size not found: product ID: %d, size ID: %d, grade %s", item.ProductId, item.SizeId, grade)
		}

		query := `UPDATE product_size
			SET quantity = quantity - :quantity
			WHERE product_id = :productId
			AND size_id = :sizeId
			AND grade = :grade
			AND quantity >= :quantity
			AND status = 1`

		result, err := s.DB.NamedExecContext(ctx, query, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
			"grade":     grade,
		})
		if err != nil {
			return fmt.Errorf("can't decrease available sizes: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("can't get rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("cannot decrease available sizes: insufficient quantity for product ID: %d, size ID: %d, grade %s", item.ProductId, item.SizeId, grade)
		}

		quantityAfter := quantityBefore.Sub(item.QuantityDecimal())

		if history != nil {
			entry := entity.StockChangeInsert{
				ProductId:      sql.NullInt32{Int32: int32(item.ProductId), Valid: true},
				SizeId:         sql.NullInt32{Int32: int32(item.SizeId), Valid: true},
				Grade:          grade,
				QuantityDelta:  item.QuantityDecimal().Neg(),
				QuantityBefore: quantityBefore,
				QuantityAfter:  quantityAfter,
				Source:         string(history.Source),
				OrderId:        sql.NullInt32{Int32: int32(history.OrderId), Valid: history.OrderId != 0},
				OrderUUID:      sql.NullString{String: history.OrderUUID, Valid: history.OrderUUID != ""},
			}
			if history.OrderComment != "" {
				entry.OrderComment = sql.NullString{String: history.OrderComment, Valid: true}
			}
			if history.OrderCurrency != "" {
				itemPrice := item.ProductPriceDecimal()
				itemPriceWithSale := item.ProductPriceWithSaleDecimal()

				saleDiscountPerItem := itemPrice.Sub(itemPriceWithSale)
				promoDiscountPerItem := decimal.Zero
				if history.PromoDiscount.IsPositive() {
					promoDiscountPerItem = itemPriceWithSale.Mul(history.PromoDiscount).Div(decimal.NewFromInt(100))
				}
				totalDiscountPerItem := saleDiscountPerItem.Add(promoDiscountPerItem)
				paidPerItem := itemPrice.Sub(totalDiscountPerItem)

				qty := item.QuantityDecimal()
				entry.PriceBeforeDiscount = decimal.NullDecimal{Decimal: itemPrice.Mul(qty), Valid: true}
				entry.DiscountAmount = decimal.NullDecimal{Decimal: totalDiscountPerItem.Mul(qty), Valid: true}
				entry.PaidCurrency = sql.NullString{String: history.OrderCurrency, Valid: true}
				entry.PaidAmount = decimal.NullDecimal{Decimal: paidPerItem.Mul(qty), Valid: true}
				if history.PayoutBaseAmount.IsPositive() {
					entry.PayoutBaseAmount = decimal.NullDecimal{Decimal: history.PayoutBaseAmount, Valid: true}
					entry.PayoutBaseCurrency = sql.NullString{String: "EUR", Valid: true}
				}
			}
			switch entity.StockChangeSource(history.Source) {
			case entity.StockChangeSourceOrderPaid:
				entry.Reason = sql.NullString{String: string(entity.StockChangeReasonOrder), Valid: true}
			case entity.StockChangeSourceOrderCustom:
				entry.Reason = sql.NullString{String: string(entity.StockChangeReasonCustomOrder), Valid: true}
			}
			historyEntries = append(historyEntries, entry)
		}
	}
	if len(historyEntries) > 0 {
		return s.RecordStockChange(ctx, historyEntries)
	}
	return nil
}

// RestoreStockForProductSizes restores stock for product sizes. Each line restores into its OWN
// grade's row (item.Grade, empty = 'A'): a refunded/cancelled '-B' line goes back to B stock, not
// A (0251). The B row is guaranteed to exist — the unit was decremented from it at sale.
func (s *Store) RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error {
	var historyEntries []entity.StockChangeInsert
	for _, item := range items {
		grade := entity.NormalizeVariantGrade(item.Grade)
		// Locking read (FOR UPDATE, callers run inside a tx): without it the before/after pair
		// journalled below can lie under a concurrent writer even though the additive UPDATE
		// itself stays correct — and the B row is now shared with the locked seconds path, so
		// both writers of the same row must carry the same guarantee.
		quantityBefore, err := lockProductSizeQuantity(ctx, s.DB, item.ProductId, item.SizeId, grade)
		if err != nil {
			return fmt.Errorf("can't get product size stock: %w", err)
		}
		quantityAfter := quantityBefore.Add(item.QuantityDecimal())

		updateQuery := `UPDATE product_size SET quantity = quantity + :quantity WHERE product_id = :productId AND size_id = :sizeId AND grade = :grade`
		err = storeutil.ExecNamed(ctx, s.DB, updateQuery, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
			"grade":     grade,
		})
		if err != nil {
			return fmt.Errorf("can't restore product quantity for sizes: %w", err)
		}

		if history != nil {
			entry := entity.StockChangeInsert{
				ProductId:      sql.NullInt32{Int32: int32(item.ProductId), Valid: true},
				SizeId:         sql.NullInt32{Int32: int32(item.SizeId), Valid: true},
				Grade:          grade,
				QuantityDelta:  item.QuantityDecimal(),
				QuantityBefore: quantityBefore,
				QuantityAfter:  quantityAfter,
				Source:         string(history.Source),
				OrderId:        sql.NullInt32{Int32: int32(history.OrderId), Valid: history.OrderId != 0},
				OrderUUID:      sql.NullString{String: history.OrderUUID, Valid: history.OrderUUID != ""},
			}
			switch entity.StockChangeSource(history.Source) {
			case entity.StockChangeSourceOrderReturned:
				entry.Reason = sql.NullString{String: string(entity.StockChangeReasonReturnToStock), Valid: true}
			case entity.StockChangeSourceOrderCancelled:
				entry.Reason = sql.NullString{String: string(entity.StockChangeReasonOrderCancelled), Valid: true}
			}
			historyEntries = append(historyEntries, entry)
		}
	}
	if len(historyEntries) > 0 {
		return s.RecordStockChange(ctx, historyEntries)
	}
	return nil
}

// RestoreStockForProductSizesSeconds is the seconds-disposition counterpart of
// RestoreStockForProductSizes (Phase 8, plan 13 §5): a worn-but-sellable return goes back to the
// product's B-GRADE variant (Phase 7 mechanism — the row and its '-B' SKU are created on first
// touch), never to sellable A stock. Journalled per unit with the same order_returned source, the
// grade marker separating the streams. B stock carries zero cost, so the refund's ledger entry
// books NO inventory return for these units — see BuildOrderRefundEntry.
func (s *Store) RestoreStockForProductSizesSeconds(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error {
	var historyEntries []entity.StockChangeInsert
	for _, item := range items {
		before, err := lockProductSizeQuantity(ctx, s.DB, item.ProductId, item.SizeId, entity.VariantGradeB)
		if err != nil {
			return fmt.Errorf("can't lock B stock for product %d size %d: %w", item.ProductId, item.SizeId, err)
		}
		after := before.Add(item.QuantityDecimal())
		if err := s.updateSecondsStock(ctx, item.ProductId, item.SizeId, int(after.IntPart())); err != nil {
			return fmt.Errorf("can't restore seconds stock for product %d size %d: %w", item.ProductId, item.SizeId, err)
		}
		if history != nil {
			historyEntries = append(historyEntries, entity.StockChangeInsert{
				ProductId:      sql.NullInt32{Int32: int32(item.ProductId), Valid: true},
				SizeId:         sql.NullInt32{Int32: int32(item.SizeId), Valid: true},
				Grade:          entity.VariantGradeB,
				QuantityDelta:  item.QuantityDecimal(),
				QuantityBefore: before,
				QuantityAfter:  after,
				Source:         string(history.Source),
				OrderId:        sql.NullInt32{Int32: int32(history.OrderId), Valid: history.OrderId != 0},
				OrderUUID:      sql.NullString{String: history.OrderUUID, Valid: history.OrderUUID != ""},
				Reason:         sql.NullString{String: string(entity.StockChangeReasonReturnToStock), Valid: true},
			})
		}
	}
	if len(historyEntries) > 0 {
		return s.RecordStockChange(ctx, historyEntries)
	}
	return nil
}

// GetProductSizeStock gets the current stock quantity for a specific product/size combination
// (sellable A grade — the public/default stream).
func (s *Store) GetProductSizeStock(ctx context.Context, productId int, sizeId int) (decimal.Decimal, bool, error) {
	return getProductSizeStockGrade(ctx, s.DB, productId, sizeId, entity.VariantGradeA)
}

// getProductSizeStockGrade reads one grade's stock quantity for a (product, size) pair. The order
// stock paths use it with the sold line's own grade (0251) so A and B streams never cross.
func getProductSizeStockGrade(ctx context.Context, db dependency.DB, productId, sizeId int, grade string) (decimal.Decimal, bool, error) {
	query := `SELECT quantity FROM product_size WHERE product_id = :productId AND size_id = :sizeId AND grade = :grade`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
		"grade":     entity.NormalizeVariantGrade(grade),
	}

	type ProductSizeQuantity struct {
		Quantity decimal.Decimal `db:"quantity"`
	}

	productSize, err := storeutil.QueryNamedOne[ProductSizeQuantity](ctx, db, query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decimal.Zero, false, nil
		}
		return decimal.Zero, false, fmt.Errorf("can't get product size stock: %w", err)
	}

	return productSize.Quantity, true, nil
}

// GetVariantByID returns a single variant (product_size) by its stable id. It returns sql.ErrNoRows when
// no such variant exists so callers can map that to NOT_FOUND — variant addressing (stock, archive)
// never implicitly creates a variant (R2/p012).
func (s *Store) GetVariantByID(ctx context.Context, variantID int) (entity.Variant, error) {
	return storeutil.QueryNamedOne[entity.Variant](ctx, s.DB,
		`SELECT id, quantity, product_id, size_id, sku, status FROM product_size WHERE id = :id AND grade = 'A'`,
		map[string]any{"id": variantID})
}

// GetVariantBySKU returns a single variant (product_size) by its public variant SKU. Returns
// sql.ErrNoRows when no such variant exists so storefront callers (NotifyMe) can map that to NOT_FOUND.
// variant_sku is UNIQUE, so this resolves to exactly one variant.
func (s *Store) GetVariantBySKU(ctx context.Context, variantSKU string) (entity.Variant, error) {
	return storeutil.QueryNamedOne[entity.Variant](ctx, s.DB,
		`SELECT id, quantity, product_id, size_id, sku, status FROM product_size WHERE sku = :sku AND grade = 'A'`,
		map[string]any{"sku": variantSKU})
}

// UpdateProductSizeStock updates the stock quantity for a product size.
func (s *Store) UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error {
	sz, ok := cache.GetSizeById(sizeId)
	if !ok {
		return fmt.Errorf("can't get size by id: %d", sizeId)
	}

	query := `
		INSERT INTO product_size 
			(product_id, size_id, quantity, grade) 
		VALUES 
			(:productId, :sizeId, :quantity, 'A') 
		ON DUPLICATE KEY UPDATE quantity = :quantity
	`
	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"productId": productId,
		"sizeId":    sz.Id,
		"quantity":  quantity,
	})
	if err != nil {
		return fmt.Errorf("can't insert product size: %w", err)
	}
	// The upsert above can MATERIALISE a new variant row (a size the colourway did not have) with a
	// NULL SKU. Mint it from the product's base so no stock path leaves a variant without a stable
	// identity; an existing variant's SKU is left untouched (problem 002).
	if err := ensureVariantSKU(ctx, s.DB, productId, sz.Id); err != nil {
		return fmt.Errorf("can't ensure variant sku: %w", err)
	}
	return nil
}

// updateSecondsStock is the B-grade counterpart of UpdateProductSizeStock (Phase 7): it upserts the
// (product, size, 'B') row and, on first materialisation, mints its SKU as the A variant's SKU with
// a '-B' suffix — the A variant's identity is guaranteed first, so a seconds booking on an
// unpublished colourway fails with the same actionable publish-first error as any stock write.
func (s *Store) updateSecondsStock(ctx context.Context, productId, sizeId, quantity int) error {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO product_size 
			(product_id, size_id, quantity, grade) 
		VALUES 
			(:productId, :sizeId, :quantity, 'B') 
		ON DUPLICATE KEY UPDATE quantity = :quantity`,
		map[string]any{"productId": productId, "sizeId": sizeId, "quantity": quantity}); err != nil {
		return fmt.Errorf("can't upsert seconds variant: %w", err)
	}
	if err := ensureVariantSKU(ctx, s.DB, productId, sizeId); err != nil {
		return fmt.Errorf("can't ensure A variant sku for seconds: %w", err)
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		UPDATE product_size b
		JOIN product_size a ON a.product_id = b.product_id AND a.size_id = b.size_id AND a.grade = 'A'
		SET b.sku = CONCAT(a.sku, '-B')
		WHERE b.product_id = :productId AND b.size_id = :sizeId AND b.grade = 'B'
		  AND (b.sku IS NULL OR b.sku = '') AND a.sku IS NOT NULL AND a.sku <> ''`,
		map[string]any{"productId": productId, "sizeId": sizeId}); err != nil {
		return fmt.Errorf("can't mint seconds variant sku: %w", err)
	}
	return nil
}

// ReceiveProductionStock increments a product's per-size stock by the received quantities of a
// production run and records each increment in product_stock_change_history with the
// `production_received` source (the run id in reference_id). It operates on the store's current
// connection so it participates in the caller's transaction (ReceiveProductionRun) — do not open a
// new transaction here. Sizes with a non-positive quantity are skipped.
//
// Each variant's quantity is read FOR UPDATE and the incremented value written under that lock. The
// run lock ReceiveProductionRun holds guards production_run, not product_size; under a weaker
// isolation level an unlocked read followed by this absolute write would resurrect a unit a sale
// removed in between (10 → receive reads 10 → sale writes 9 → receive writes 60 instead of 59).
// Today's transactions run SERIALIZABLE (internal/store/db.go), where a plain SELECT already takes a
// shared lock — the explicit X lock makes correctness independent of that configuration and avoids
// the S→X upgrade deadlock shape. The journal's before/after are the locked values, so the history
// sums to the real stock. Sizes are visited in ascending order so two concurrent receives over
// overlapping variants take their row locks in the same order.
func (s *Store) ReceiveProductionStock(ctx context.Context, productID int, perSize map[int]int, runID int, username, grade string) ([]entity.StockTransition, error) {
	ref := sql.NullString{String: fmt.Sprintf("production_run:%d", runID), Valid: true}
	var adminUser sql.NullString
	if username != "" {
		adminUser = sql.NullString{String: username, Valid: true}
	}
	sizeIDs := make([]int, 0, len(perSize))
	for sizeID := range perSize {
		sizeIDs = append(sizeIDs, sizeID)
	}
	sort.Ints(sizeIDs)
	transitions := make([]entity.StockTransition, 0, len(sizeIDs))
	for _, sizeID := range sizeIDs {
		qty := perSize[sizeID]
		if qty <= 0 {
			continue
		}
		before, err := lockProductSizeQuantity(ctx, s.DB, productID, sizeID, grade)
		if err != nil {
			return nil, fmt.Errorf("can't lock stock for product %d size %d: %w", productID, sizeID, err)
		}
		after := before.Add(decimal.NewFromInt(int64(qty)))
		if grade == entity.VariantGradeB {
			err = s.updateSecondsStock(ctx, productID, sizeID, int(after.IntPart()))
		} else {
			err = s.UpdateProductSizeStock(ctx, productID, sizeID, int(after.IntPart()))
		}
		if err != nil {
			return nil, fmt.Errorf("can't increment stock for product %d size %d: %w", productID, sizeID, err)
		}
		if err := s.RecordStockChange(ctx, []entity.StockChangeInsert{{
			ProductId:      sql.NullInt32{Int32: int32(productID), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sizeID), Valid: true},
			Grade:          grade,
			QuantityDelta:  decimal.NewFromInt(int64(qty)),
			QuantityBefore: before,
			QuantityAfter:  after,
			Source:         string(entity.StockChangeSourceProductionReceived),
			ReferenceId:    ref,
			AdminUsername:  adminUser,
		}}); err != nil {
			return nil, fmt.Errorf("can't record production-received stock change: %w", err)
		}
		transitions = append(transitions, entity.StockTransition{
			ProductID: productID, SizeID: sizeID, Grade: grade, Before: before, After: after,
		})
	}
	return transitions, nil
}

// ReverseProductionStock is the mirror of ReceiveProductionStock (Phase 6): it takes a reversed
// receipt's good units back OUT of the product's per-size stock, journalling each decrement with
// the `production_reversed` source (reversed receipt in reference_id, operator's reason in
// comment). It runs on the caller's transaction and NEVER writes a negative quantity: every size
// whose locked on-hand is short is collected into the returned shortfall list and nothing further
// is decided here — with ANY shortfall the caller aborts the whole transaction, so partial writes
// roll back. Sold-but-unshipped units already left `quantity` at payment, so they produce the same
// shortfall — a reversal can never steal a unit a sale claimed. Sizes are visited ascending,
// matching the receive path's deterministic lock order.
func (s *Store) ReverseProductionStock(ctx context.Context, productID int, perSize map[int]int, receiptID int, username, reason, grade string) ([]entity.ProductionRunReversalShortfallItem, error) {
	ref := sql.NullString{String: fmt.Sprintf("receipt:%d", receiptID), Valid: true}
	var adminUser sql.NullString
	if username != "" {
		adminUser = sql.NullString{String: username, Valid: true}
	}
	var comment sql.NullString
	if reason != "" {
		comment = sql.NullString{String: reason, Valid: true}
	}
	sizeIDs := make([]int, 0, len(perSize))
	for sizeID := range perSize {
		sizeIDs = append(sizeIDs, sizeID)
	}
	sort.Ints(sizeIDs)
	var short []entity.ProductionRunReversalShortfallItem
	for _, sizeID := range sizeIDs {
		qty := perSize[sizeID]
		if qty <= 0 {
			continue
		}
		before, err := lockProductSizeQuantity(ctx, s.DB, productID, sizeID, grade)
		if err != nil {
			return nil, fmt.Errorf("can't lock stock for product %d size %d: %w", productID, sizeID, err)
		}
		after := before.Sub(decimal.NewFromInt(int64(qty)))
		if after.IsNegative() {
			short = append(short, entity.ProductionRunReversalShortfallItem{
				ProductID: productID, SizeID: sizeID, Grade: grade, Requested: qty, OnHand: int(before.IntPart()),
			})
			continue
		}
		if grade == entity.VariantGradeB {
			err = s.updateSecondsStock(ctx, productID, sizeID, int(after.IntPart()))
		} else {
			err = s.UpdateProductSizeStock(ctx, productID, sizeID, int(after.IntPart()))
		}
		if err != nil {
			return nil, fmt.Errorf("can't decrement stock for product %d size %d: %w", productID, sizeID, err)
		}
		if err := s.RecordStockChange(ctx, []entity.StockChangeInsert{{
			ProductId:      sql.NullInt32{Int32: int32(productID), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sizeID), Valid: true},
			Grade:          grade,
			QuantityDelta:  decimal.NewFromInt(int64(qty)).Neg(),
			QuantityBefore: before,
			QuantityAfter:  after,
			Source:         string(entity.StockChangeSourceProductionReversed),
			Reason:         sql.NullString{String: string(entity.StockChangeReasonReceiptReversed), Valid: true},
			Comment:        comment,
			ReferenceId:    ref,
			AdminUsername:  adminUser,
		}}); err != nil {
			return nil, fmt.Errorf("can't record production-reversed stock change: %w", err)
		}
	}
	return short, nil
}

// ClearProductCostPriceClaimOfRun is the reversal's cost_price rollback (Phase 6, plan 05 item 5).
// Guarded on the exact claim — only a cost THIS run seeded is touched; a manual figure or a later
// run/card source stays (the caller reports it as skipped). With a computable card estimate the
// cost transfers back to tech_card provenance; without one it clears to honestly-unknown NULL.
// cost_breakdown rides the same statement so price and decomposition never come from different
// facts. Returns whether the product row was actually written.
func (s *Store) ClearProductCostPriceClaimOfRun(ctx context.Context, productID, runID, techCardID int, est entity.ProductCostReseed) (bool, error) {
	if est.Cost.Valid {
		if err := storeutil.ExecNamed(ctx, s.DB, `
			INSERT INTO product_cost_event (product_id, cost_before, cost_after, source, source_ref)
			SELECT p.id, p.cost_price, :cost, 'production_run_reversal_reseed',
			       CONCAT('production_run', CHAR(58), CAST(:run AS CHAR CHARACTER SET utf8mb4))
			FROM product p
			WHERE p.id = :id AND p.cost_price_source = 'production_run'
			  AND p.cost_price_production_run_id = :run AND NOT (p.cost_price <=> :cost)`,
			map[string]any{"id": productID, "run": runID, "cost": est.Cost.Decimal}); err != nil {
			return false, fmt.Errorf("can't record cost event (reversal reseed): %w", err)
		}
		n, err := storeutil.ExecNamedRows(ctx, s.DB, `
			UPDATE product
			SET cost_price = :cost,
				cost_price_source = 'tech_card',
				cost_price_tech_card_id = :tc,
				cost_price_production_run_id = NULL,
				cost_price_updated_at = NOW(),
				cost_breakdown = :breakdown
			WHERE id = :id
				AND cost_price_source = 'production_run'
				AND cost_price_production_run_id = :run`,
			map[string]any{"id": productID, "run": runID, "tc": techCardID,
				"cost": est.Cost.Decimal, "breakdown": est.Breakdown})
		return n > 0, err
	}
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO product_cost_event (product_id, cost_before, cost_after, source, source_ref)
		SELECT p.id, p.cost_price, NULL, 'production_run_reversal_clear',
		       CONCAT('production_run', CHAR(58), CAST(:run AS CHAR CHARACTER SET utf8mb4))
		FROM product p
		WHERE p.id = :id AND p.cost_price_source = 'production_run'
		  AND p.cost_price_production_run_id = :run AND p.cost_price IS NOT NULL`,
		map[string]any{"id": productID, "run": runID}); err != nil {
		return false, fmt.Errorf("can't record cost event (reversal clear): %w", err)
	}
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE product
		SET cost_price = NULL,
			cost_price_source = NULL,
			cost_price_tech_card_id = NULL,
			cost_price_production_run_id = NULL,
			cost_price_updated_at = NOW(),
			cost_breakdown = NULL
		WHERE id = :id
			AND cost_price_source = 'production_run'
			AND cost_price_production_run_id = :run`,
		map[string]any{"id": productID, "run": runID})
	return n > 0, err
}

// SetProductCostPriceFromProductionRun writes cost (base currency) as the production-run-sourced
// cost_price of a product, recording the provenance (source + run id + timestamp). It returns
// whether the product was actually written.
//
// The write is gated on provenance, exactly like the tech-card seeds (SeedProductCostFromTechCard):
// a MANUALLY set cost is the owner's deliberate figure and a receipt must never overwrite it, so
// only an unset, tech-card- or run-sourced cost is claimed. cost_breakdown is cleared in the same
// statement: it decomposes the tech-card ESTIMATE, and leaving it beside a run actual makes the
// COGS-structure report split the actual by the old plan's proportions.
func (s *Store) SetProductCostPriceFromProductionRun(ctx context.Context, productID, runID int, cost decimal.Decimal) (bool, error) {
	if err := storeutil.ExecNamed(ctx, s.DB, `
		INSERT INTO product_cost_event (product_id, cost_before, cost_after, source, source_ref)
		SELECT p.id, p.cost_price, :cost, 'production_run_receive',
		       CONCAT('production_run', CHAR(58), CAST(:run AS CHAR CHARACTER SET utf8mb4))
		FROM product p
		WHERE p.id = :id
		  AND (p.cost_price_source IS NULL OR p.cost_price_source IN ('tech_card', 'production_run'))
		  AND NOT (p.cost_price <=> :cost)`,
		map[string]any{"id": productID, "run": runID, "cost": cost}); err != nil {
		return false, fmt.Errorf("can't record cost event (run receive): %w", err)
	}
	n, err := storeutil.ExecNamedRows(ctx, s.DB, `
		UPDATE product
		SET cost_price = :cost,
			cost_price_source = 'production_run',
			cost_price_production_run_id = :run,
			cost_price_tech_card_id = NULL,
			cost_price_updated_at = NOW(),
			cost_breakdown = NULL
		WHERE id = :id
			AND (cost_price_source IS NULL OR cost_price_source IN ('tech_card', 'production_run'))`,
		map[string]any{"id": productID, "run": runID, "cost": cost})
	return n > 0, err
}

// UpdateProductSizeStockWithHistory applies a stock change and records it to
// product_stock_change_history atomically. It reads the current quantity FOR UPDATE, computes the new
// value from mode+amount (Set = absolute, Adjust = signed delta), writes it and records the history —
// all under the same row lock — so concurrent adjustments compose instead of clobbering (problem 025).
// It returns the committed before/after quantities so the caller derives the real 0->positive
// transition (e.g. waitlist notification) from what actually happened, not a pre-read stale value.
// A resulting negative quantity is a *entity.ValidationError (the caller maps it to InvalidArgument).
func (s *Store) UpdateProductSizeStockWithHistory(ctx context.Context, productId int, sizeId int, mode entity.StockUpdateMode, amount int, reason string, comment string) (decimal.Decimal, decimal.Decimal, error) {
	var before, after decimal.Decimal
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		var err error
		before, err = lockProductSizeQuantity(ctx, rep.DB(), productId, sizeId, entity.VariantGradeA)
		if err != nil {
			return err
		}
		if mode == entity.StockUpdateModeAdjust {
			after = before.Add(decimal.NewFromInt(int64(amount)))
		} else {
			after = decimal.NewFromInt(int64(amount))
		}
		if after.IsNegative() {
			return &entity.ValidationError{Message: fmt.Sprintf("stock adjustment would result in negative stock (%s -> %s)", before.String(), after.String())}
		}
		if err := rep.Products().UpdateProductSizeStock(ctx, productId, sizeId, int(after.IntPart())); err != nil {
			return err
		}
		e := entity.StockChangeInsert{
			ProductId:      sql.NullInt32{Int32: int32(productId), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sizeId), Valid: true},
			QuantityDelta:  after.Sub(before),
			QuantityBefore: before,
			QuantityAfter:  after,
			Source:         string(entity.StockChangeSourceManualAdjustment),
		}
		if adminUsername := auth.GetAdminUsername(ctx); adminUsername != "" {
			e.AdminUsername = sql.NullString{String: adminUsername, Valid: true}
			e.ReferenceId = sql.NullString{String: "admin:" + adminUsername, Valid: true}
		}
		if reason != "" {
			e.Reason = sql.NullString{String: reason, Valid: true}
		}
		if comment != "" {
			e.Comment = sql.NullString{String: comment, Valid: true}
		}
		return rep.Products().RecordStockChange(ctx, []entity.StockChangeInsert{e})
	})
	return before, after, err
}

// lockProductSizeQuantity reads a variant's current quantity with FOR UPDATE (row lock), returning 0
// when the variant row does not exist yet. Must run inside a transaction; the lock serialises
// concurrent adjustments on the same variant. grade selects the A or B row of the (product, size)
// pair (Phase 7) — every path outside production receive/reverse pins VariantGradeA.
func lockProductSizeQuantity(ctx context.Context, db dependency.DB, productId, sizeId int, grade string) (decimal.Decimal, error) {
	type qty struct {
		Quantity decimal.Decimal `db:"quantity"`
	}
	row, err := storeutil.QueryNamedOne[qty](ctx, db,
		`SELECT quantity FROM product_size WHERE product_id = :p AND size_id = :s AND grade = :g FOR UPDATE`,
		map[string]any{"p": productId, "s": sizeId, "g": grade})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decimal.Zero, nil
		}
		return decimal.Zero, fmt.Errorf("lock product size stock: %w", err)
	}
	return row.Quantity, nil
}
