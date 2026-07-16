package product

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// ReduceStockForProductSizes reduces stock for product sizes atomically.
func (s *Store) ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error {
	var historyEntries []entity.StockChangeInsert
	for _, item := range items {
		quantityBefore, exists, err := s.GetProductSizeStock(ctx, item.ProductId, item.SizeId)
		if err != nil {
			return fmt.Errorf("error checking current quantity: %w", err)
		}
		if !exists {
			return fmt.Errorf("product size not found: product ID: %d, size ID: %d", item.ProductId, item.SizeId)
		}

		query := `UPDATE product_size 
			SET quantity = quantity - :quantity 
			WHERE product_id = :productId 
			AND size_id = :sizeId 
			AND quantity >= :quantity`

		result, err := s.DB.NamedExecContext(ctx, query, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("can't decrease available sizes: %w", err)
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("can't get rows affected: %w", err)
		}
		if rowsAffected == 0 {
			return fmt.Errorf("cannot decrease available sizes: insufficient quantity for product ID: %d, size ID: %d", item.ProductId, item.SizeId)
		}

		quantityAfter := quantityBefore.Sub(item.QuantityDecimal())

		if history != nil {
			entry := entity.StockChangeInsert{
				ProductId:      sql.NullInt32{Int32: int32(item.ProductId), Valid: true},
				SizeId:         sql.NullInt32{Int32: int32(item.SizeId), Valid: true},
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

// RestoreStockForProductSizes restores stock for product sizes.
func (s *Store) RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error {
	var historyEntries []entity.StockChangeInsert
	for _, item := range items {
		quantityBefore, _, err := s.GetProductSizeStock(ctx, item.ProductId, item.SizeId)
		if err != nil {
			return fmt.Errorf("can't get product size stock: %w", err)
		}
		quantityAfter := quantityBefore.Add(item.QuantityDecimal())

		updateQuery := `UPDATE product_size SET quantity = quantity + :quantity WHERE product_id = :productId AND size_id = :sizeId`
		err = storeutil.ExecNamed(ctx, s.DB, updateQuery, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("can't restore product quantity for sizes: %w", err)
		}

		if history != nil {
			entry := entity.StockChangeInsert{
				ProductId:      sql.NullInt32{Int32: int32(item.ProductId), Valid: true},
				SizeId:         sql.NullInt32{Int32: int32(item.SizeId), Valid: true},
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

// RestoreStockSilently restores stock without recording history.
func (s *Store) RestoreStockSilently(ctx context.Context, items []entity.OrderItemInsert) error {
	for _, item := range items {
		updateQuery := `UPDATE product_size SET quantity = quantity + :quantity WHERE product_id = :productId AND size_id = :sizeId`
		err := storeutil.ExecNamed(ctx, s.DB, updateQuery, map[string]any{
			"quantity":  item.QuantityDecimal(),
			"productId": item.ProductId,
			"sizeId":    item.SizeId,
		})
		if err != nil {
			return fmt.Errorf("can't restore product quantity for sizes: %w", err)
		}
	}
	return nil
}

// GetProductSizeStock gets the current stock quantity for a specific product/size combination.
func (s *Store) GetProductSizeStock(ctx context.Context, productId int, sizeId int) (decimal.Decimal, bool, error) {
	query := `SELECT quantity FROM product_size WHERE product_id = :productId AND size_id = :sizeId`
	params := map[string]any{
		"productId": productId,
		"sizeId":    sizeId,
	}

	type ProductSizeQuantity struct {
		Quantity decimal.Decimal `db:"quantity"`
	}

	productSize, err := storeutil.QueryNamedOne[ProductSizeQuantity](ctx, s.DB, query, params)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return decimal.Zero, false, nil
		}
		return decimal.Zero, false, fmt.Errorf("can't get product size stock: %w", err)
	}

	return productSize.Quantity, true, nil
}

// UpdateProductSizeStock updates the stock quantity for a product size.
func (s *Store) UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error {
	sz, ok := cache.GetSizeById(sizeId)
	if !ok {
		return fmt.Errorf("can't get size by id: %d", sizeId)
	}

	query := `
		INSERT INTO product_size 
			(product_id, size_id, quantity) 
		VALUES 
			(:productId, :sizeId, :quantity) 
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

// ReceiveProductionStock increments a product's per-size stock by the received quantities of a
// production run and records each increment in product_stock_change_history with the
// `production_received` source (the run id in reference_id). It operates on the store's current
// connection so it participates in the caller's transaction (ReceiveProductionRun) — do not open a
// new transaction here. Sizes with a non-positive quantity are skipped.
func (s *Store) ReceiveProductionStock(ctx context.Context, productID int, perSize map[int]int, runID int, username string) error {
	ref := sql.NullString{String: fmt.Sprintf("production_run:%d", runID), Valid: true}
	var adminUser sql.NullString
	if username != "" {
		adminUser = sql.NullString{String: username, Valid: true}
	}
	for sizeID, qty := range perSize {
		if qty <= 0 {
			continue
		}
		before, _, err := s.GetProductSizeStock(ctx, productID, sizeID)
		if err != nil {
			return fmt.Errorf("can't read stock for product %d size %d: %w", productID, sizeID, err)
		}
		after := before.Add(decimal.NewFromInt(int64(qty)))
		if err := s.UpdateProductSizeStock(ctx, productID, sizeID, int(after.IntPart())); err != nil {
			return fmt.Errorf("can't increment stock for product %d size %d: %w", productID, sizeID, err)
		}
		if err := s.RecordStockChange(ctx, []entity.StockChangeInsert{{
			ProductId:      sql.NullInt32{Int32: int32(productID), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sizeID), Valid: true},
			QuantityDelta:  decimal.NewFromInt(int64(qty)),
			QuantityBefore: before,
			QuantityAfter:  after,
			Source:         string(entity.StockChangeSourceProductionReceived),
			ReferenceId:    ref,
			AdminUsername:  adminUser,
		}}); err != nil {
			return fmt.Errorf("can't record production-received stock change: %w", err)
		}
	}
	return nil
}

// SetProductCostPriceFromProductionRun writes cost (base currency) as the production-run-sourced
// cost_price of a product, recording the provenance (source + run id + timestamp).
func (s *Store) SetProductCostPriceFromProductionRun(ctx context.Context, productID, runID int, cost decimal.Decimal) error {
	return storeutil.ExecNamed(ctx, s.DB, `
		UPDATE product
		SET cost_price = :cost,
			cost_price_source = 'production_run',
			cost_price_production_run_id = :run,
			cost_price_tech_card_id = NULL,
			cost_price_updated_at = NOW()
		WHERE id = :id`,
		map[string]any{"id": productID, "run": runID, "cost": cost})
}

// UpdateProductSizeStockWithHistory updates stock and records to product_stock_change_history.
func (s *Store) UpdateProductSizeStockWithHistory(ctx context.Context, productId int, sizeId int, newQuantity int, reason string, comment string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		prevQty, _, err := rep.Products().GetProductSizeStock(ctx, productId, sizeId)
		if err != nil {
			return err
		}
		if err := rep.Products().UpdateProductSizeStock(ctx, productId, sizeId, newQuantity); err != nil {
			return err
		}
		newQty := decimal.NewFromInt(int64(newQuantity))
		delta := newQty.Sub(prevQty)
		e := entity.StockChangeInsert{
			ProductId:      sql.NullInt32{Int32: int32(productId), Valid: true},
			SizeId:         sql.NullInt32{Int32: int32(sizeId), Valid: true},
			QuantityDelta:  delta,
			QuantityBefore: prevQty,
			QuantityAfter:  newQty,
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
}
