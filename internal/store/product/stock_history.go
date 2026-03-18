package product

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// RecordStockChange inserts stock change history entries.
func (s *Store) RecordStockChange(ctx context.Context, entries []entity.StockChangeInsert) error {
	if len(entries) == 0 {
		return nil
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		row := map[string]any{
			"quantity_delta":  e.QuantityDelta,
			"quantity_before": e.QuantityBefore,
			"quantity_after":  e.QuantityAfter,
			"source":          e.Source,
		}
		setNullableInt32(row, "product_id", e.ProductId)
		setNullableInt32(row, "size_id", e.SizeId)
		setNullableInt32(row, "order_id", e.OrderId)
		setNullableString(row, "order_uuid", e.OrderUUID)
		setNullableString(row, "admin_username", e.AdminUsername)
		setNullableString(row, "reference_id", e.ReferenceId)
		setNullableString(row, "reason", e.Reason)
		setNullableString(row, "comment", e.Comment)
		setNullableString(row, "order_comment", e.OrderComment)
		setNullableDecimal(row, "price_before_discount", e.PriceBeforeDiscount)
		setNullableDecimal(row, "discount_amount", e.DiscountAmount)
		setNullableString(row, "paid_currency", e.PaidCurrency)
		setNullableDecimal(row, "paid_amount", e.PaidAmount)
		setNullableDecimal(row, "payout_base_amount", e.PayoutBaseAmount)
		setNullableString(row, "payout_base_currency", e.PayoutBaseCurrency)
		rows = append(rows, row)
	}
	return storeutil.BulkInsert(ctx, s.DB, "product_stock_change_history", rows)
}

// RecordShippingStockChange creates a SHIPPING entry in stock change history.
func (s *Store) RecordShippingStockChange(ctx context.Context, history *entity.StockHistoryParams, shippingCost decimal.Decimal) error {
	if history == nil {
		return nil
	}
	entry := entity.StockChangeInsert{
		QuantityDelta:  decimal.Zero,
		QuantityBefore: decimal.Zero,
		QuantityAfter:  decimal.Zero,
		Source:         string(history.Source),
		OrderId:        sql.NullInt32{Int32: int32(history.OrderId), Valid: history.OrderId != 0},
		OrderUUID:      sql.NullString{String: history.OrderUUID, Valid: history.OrderUUID != ""},
		Reason:         sql.NullString{String: string(entity.StockChangeReasonOrder), Valid: true},
	}
	if history.OrderCurrency != "" {
		entry.PriceBeforeDiscount = decimal.NullDecimal{Decimal: shippingCost, Valid: true}
		entry.DiscountAmount = decimal.NullDecimal{Decimal: decimal.Zero, Valid: true}
		entry.PaidCurrency = sql.NullString{String: history.OrderCurrency, Valid: true}
		entry.PaidAmount = decimal.NullDecimal{Decimal: shippingCost, Valid: true}
		if history.PayoutBaseAmount.IsPositive() {
			entry.PayoutBaseAmount = decimal.NullDecimal{Decimal: history.PayoutBaseAmount, Valid: true}
			entry.PayoutBaseCurrency = sql.NullString{String: "EUR", Valid: true}
		}
	}
	return s.RecordStockChange(ctx, []entity.StockChangeInsert{entry})
}

// GetStockChangeHistory returns paginated stock change history with optional filters.
func (s *Store) GetStockChangeHistory(ctx context.Context, productId, sizeId *int, dateFrom, dateTo *time.Time, source string, limit, offset int, orderFactor entity.OrderFactor) ([]entity.StockChange, int, error) {
	baseQuery := `FROM product_stock_change_history WHERE 1=1`
	params := map[string]any{"limit": limit, "offset": offset}
	if productId != nil {
		baseQuery += ` AND product_id = :productId`
		params["productId"] = *productId
	}
	if sizeId != nil {
		baseQuery += ` AND size_id = :sizeId`
		params["sizeId"] = *sizeId
	}
	if dateFrom != nil {
		baseQuery += ` AND created_at >= :dateFrom`
		params["dateFrom"] = *dateFrom
	}
	if dateTo != nil {
		baseQuery += ` AND created_at <= :dateTo`
		params["dateTo"] = *dateTo
	}
	if source != "" {
		baseQuery += ` AND source = :source`
		params["source"] = source
	}

	orderBy := "ORDER BY created_at DESC"
	if orderFactor == entity.Ascending {
		orderBy = "ORDER BY created_at ASC"
	}

	countQuery := `SELECT COUNT(*) ` + baseQuery
	total, err := storeutil.QueryCountNamed(ctx, s.DB, countQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count stock change history: %w", err)
	}

	dataQuery := `SELECT id, COALESCE(product_id, 0) AS product_id, COALESCE(size_id, 0) AS size_id,
		quantity_delta, quantity_before, quantity_after, source,
		COALESCE(order_id, 0) AS order_id, COALESCE(order_uuid, '') AS order_uuid,
		COALESCE(admin_username, '') AS admin_username,
		COALESCE(reference_id, '') AS reference_id,
		COALESCE(reason, '') AS reason,
		COALESCE(comment, '') AS comment,
		COALESCE(price_before_discount, '') AS price_before_discount,
		COALESCE(discount_amount, '') AS discount_amount,
		COALESCE(paid_currency, '') AS paid_currency,
		COALESCE(paid_amount, '') AS paid_amount,
		COALESCE(payout_base_amount, '') AS payout_base_amount,
		COALESCE(payout_base_currency, '') AS payout_base_currency,
		created_at ` + baseQuery + ` ` + orderBy
	if limit > 0 {
		dataQuery += ` LIMIT :limit OFFSET :offset`
	} else {
		delete(params, "limit")
		delete(params, "offset")
	}
	changes, err := storeutil.QueryListNamed[entity.StockChange](ctx, s.DB, dataQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get stock change history: %w", err)
	}
	return changes, total, nil
}

// GetStockChanges returns simplified stock changes for reporting API.
func (s *Store) GetStockChanges(ctx context.Context, dateFrom, dateTo time.Time, productId *int, sizeId *int, source string, limit, offset int, sortByDirection entity.StockAdjustmentDirection, orderFactor entity.OrderFactor) ([]entity.StockChangeRow, int, error) {
	baseQuery := `
		FROM product_stock_change_history psch
		LEFT JOIN product p ON p.id = psch.product_id
		LEFT JOIN size s ON s.id = psch.size_id
		WHERE psch.created_at >= :dateFrom
		AND psch.created_at <= :dateTo
	`

	params := map[string]any{
		"dateFrom": dateFrom,
		"dateTo":   dateTo,
		"limit":    limit,
		"offset":   offset,
	}

	if productId != nil {
		baseQuery += ` AND psch.product_id = :productId`
		params["productId"] = *productId
	}
	if sizeId != nil {
		baseQuery += ` AND psch.size_id = :sizeId`
		params["sizeId"] = *sizeId
	}
	if source != "" {
		baseQuery += ` AND psch.source = :source`
		params["source"] = source
	}
	if sortByDirection == entity.StockAdjustmentDirectionIncrease {
		baseQuery += ` AND psch.quantity_delta > 0`
	} else if sortByDirection == entity.StockAdjustmentDirectionDecrease {
		baseQuery += ` AND psch.quantity_delta < 0`
	}

	countQuery := `SELECT COUNT(*) ` + baseQuery
	total, err := storeutil.QueryCountNamed(ctx, s.DB, countQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't count stock changes: %w", err)
	}

	dataQuery := `
		SELECT
			psch.created_at,
			COALESCE(p.sku, 'SHIPPING') as sku,
			COALESCE(s.name, '') as size_name,
			psch.quantity_delta,
			psch.quantity_after,
			psch.source,
			COALESCE(psch.reference_id, '') as reference_id,
			COALESCE(psch.order_uuid, '') as order_uuid,
			COALESCE(psch.admin_username, '') as admin_username,
			COALESCE(psch.reason, '') as reason,
			COALESCE(psch.comment, '') as comment,
			COALESCE(psch.order_comment, '') as order_comment,
			COALESCE(psch.price_before_discount, '') as price_before_discount,
			COALESCE(psch.discount_amount, '') as discount_amount,
			COALESCE(psch.paid_currency, '') as paid_currency,
			COALESCE(psch.paid_amount, '') as paid_amount,
			COALESCE(psch.payout_base_amount, '') as payout_base_amount,
			CASE WHEN psch.payout_base_amount IS NOT NULL THEN COALESCE(psch.payout_base_currency, '') ELSE '' END as payout_base_currency
	` + baseQuery + `
		ORDER BY psch.created_at ` + orderFactor.String() + `
	`

	if limit > 0 {
		dataQuery += ` LIMIT :limit OFFSET :offset`
	} else {
		delete(params, "limit")
		delete(params, "offset")
	}

	changes, err := storeutil.QueryListNamed[entity.StockChangeRow](ctx, s.DB, dataQuery, params)
	if err != nil {
		return nil, 0, fmt.Errorf("can't get stock changes: %w", err)
	}

	return changes, total, nil
}

func setNullableInt32(row map[string]any, key string, v sql.NullInt32) {
	if v.Valid {
		row[key] = v.Int32
	} else {
		row[key] = nil
	}
}

func setNullableString(row map[string]any, key string, v sql.NullString) {
	if v.Valid {
		row[key] = v.String
	} else {
		row[key] = nil
	}
}

func setNullableDecimal(row map[string]any, key string, v decimal.NullDecimal) {
	if v.Valid {
		row[key] = v.Decimal
	} else {
		row[key] = nil
	}
}
