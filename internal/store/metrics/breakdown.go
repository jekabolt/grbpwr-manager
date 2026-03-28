package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// formatCategoryDisplayName converts slug-style category names to human-readable labels.
// Examples: "hoodies_sweatshirts" -> "Hoodies Sweatshirts", "jackets" -> "Jackets"
func formatCategoryDisplayName(name string) string {
	if name == "" {
		return ""
	}
	// Replace underscores with spaces
	formatted := strings.ReplaceAll(name, "_", " ")
	// Title case each word
	words := strings.Fields(formatted)
	for i, word := range words {
		if len(word) > 0 {
			words[i] = strings.ToUpper(word[:1]) + word[1:]
		}
	}
	return strings.Join(words, " ")
}

func (s *Store) getRevenueByPaymentMethod(ctx context.Context, from, to time.Time) ([]entity.PaymentMethodMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id,
				(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.total_price, co.refunded_amount
			) ob
		)
		SELECT COALESCE(p.payment_method_type, pm.name) AS payment_method,
			COALESCE(SUM(ob.revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base ob
		JOIN payment p ON p.order_id = ob.id
		JOIN payment_method pm ON p.payment_method_id = pm.id
		GROUP BY COALESCE(p.payment_method_type, pm.name)
		ORDER BY value DESC
	`
	rows, err := storeutil.QueryListNamed[struct {
		PaymentMethod string          `db:"payment_method"`
		Value         decimal.Decimal `db:"value"`
		Count         int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.PaymentMethodMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.PaymentMethodMetric{PaymentMethod: r.PaymentMethod, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getRevenueByCurrency(ctx context.Context, from, to time.Time) ([]entity.CurrencyMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_base AS (
			SELECT ob.id, ob.currency,
				(ob.items_base * (100 - ob.discount) / 100.0 + CASE WHEN ob.free_shipping THEN 0 ELSE ob.shipment_base END) * (ob.total_price - ob.refunded_amount) / NULLIF(ob.total_price, 0) AS revenue_base
			FROM (
				SELECT co.id, co.currency,
					COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base,
					COALESCE(MAX(scp.price), 0) AS shipment_base,
					COALESCE(MAX(pc.discount), 0) AS discount,
					COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
					co.total_price,
					COALESCE(co.refunded_amount, 0) AS refunded_amount
				FROM customer_order co
				LEFT JOIN order_item oi ON co.id = oi.order_id
				LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
				LEFT JOIN shipment s ON co.id = s.order_id
				LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
				LEFT JOIN promo_code pc ON co.promo_id = pc.id
				WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
				GROUP BY co.id, co.currency, co.total_price, co.refunded_amount
			) ob
		)
		SELECT currency,
			COALESCE(SUM(revenue_base), 0) AS value,
			COUNT(*) AS cnt
		FROM order_base
		GROUP BY currency
		ORDER BY value DESC
	`
	rows, err := storeutil.QueryListNamed[struct {
		Currency string          `db:"currency"`
		Value    decimal.Decimal `db:"value"`
		Count    int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CurrencyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.CurrencyMetric{Currency: r.Currency, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getTopProductsByRevenue(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT oi.product_id, p.brand,
			(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1) AS product_name,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi.product_id, p.brand
		ORDER BY value DESC
		LIMIT :limit
	`
	rows, err := storeutil.QueryListNamed[struct {
		ProductId   int             `db:"product_id"`
		Brand       string          `db:"brand"`
		ProductName string          `db:"product_name"`
		Value       decimal.Decimal `db:"value"`
		Count       int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		productName := r.ProductName
		if productName == "" {
			productName = r.Brand
		}
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: productName, Brand: r.Brand, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getTopProductsByQuantity(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT oi.product_id, p.brand,
			(SELECT pt.name FROM product_translation pt WHERE pt.product_id = p.id ORDER BY pt.language_id LIMIT 1) AS product_name,
			SUM(oi.quantity) AS cnt,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS value
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi.product_id, p.brand
		ORDER BY cnt DESC
		LIMIT :limit
	`
	rows, err := storeutil.QueryListNamed[struct {
		ProductId   int             `db:"product_id"`
		Brand       string          `db:"brand"`
		ProductName string          `db:"product_name"`
		Count       int             `db:"cnt"`
		Value       decimal.Decimal `db:"value"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "limit": limit, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.ProductMetric, len(rows))
	for i, r := range rows {
		productName := r.ProductName
		if productName == "" {
			productName = r.Brand
		}
		result[i] = entity.ProductMetric{ProductId: r.ProductId, ProductName: productName, Brand: r.Brand, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getRevenueByCategory(ctx context.Context, from, to time.Time) ([]entity.CategoryMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		SELECT p.top_category_id AS category_id, c.name AS category_name,
			c.name AS category_display_name,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS value,
			SUM(oi.quantity) AS cnt
		FROM order_item oi
		JOIN product p ON oi.product_id = p.id
		JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		JOIN category c ON p.top_category_id = c.id
		JOIN customer_order co ON oi.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY p.top_category_id, c.name
		ORDER BY value DESC
		LIMIT 30
	`
	rows, err := storeutil.QueryListNamed[struct {
		CategoryId          int             `db:"category_id"`
		CategoryName        string          `db:"category_name"`
		CategoryDisplayName string          `db:"category_display_name"`
		Value               decimal.Decimal `db:"value"`
		Count               int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CategoryMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.CategoryMetric{
			CategoryId:          r.CategoryId,
			CategoryName:        r.CategoryName,
			CategoryDisplayName: formatCategoryDisplayName(r.CategoryDisplayName),
			Value:               r.Value,
			Count:               r.Count,
		}
	}
	return result, nil
}

func (s *Store) getCrossSellPairs(ctx context.Context, from, to time.Time, limit int) ([]entity.CrossSellPair, error) {
	query := `
		SELECT oi1.product_id AS product_a_id, oi2.product_id AS product_b_id,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p1.id ORDER BY pt.language_id LIMIT 1), p1.brand) AS product_a_name,
			COALESCE((SELECT pt.name FROM product_translation pt WHERE pt.product_id = p2.id ORDER BY pt.language_id LIMIT 1), p2.brand) AS product_b_name,
			COUNT(*) AS cnt
		FROM order_item oi1
		JOIN order_item oi2 ON oi1.order_id = oi2.order_id AND oi1.product_id < oi2.product_id
		JOIN product p1 ON oi1.product_id = p1.id
		JOIN product p2 ON oi2.product_id = p2.id
		JOIN customer_order co ON oi1.order_id = co.id
		WHERE co.placed >= :from AND co.placed < :to
		AND co.order_status_id IN (:statusIds)
		GROUP BY oi1.product_id, oi2.product_id, p1.brand, p2.brand
		ORDER BY cnt DESC
		LIMIT :limit
	`
	rows, err := storeutil.QueryListNamed[struct {
		ProductAId   int    `db:"product_a_id"`
		ProductBId   int    `db:"product_b_id"`
		ProductAName string `db:"product_a_name"`
		ProductBName string `db:"product_b_name"`
		Count        int    `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "limit": limit, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.CrossSellPair, len(rows))
	for i, r := range rows {
		result[i] = entity.CrossSellPair{
			ProductAId:   r.ProductAId,
			ProductBId:   r.ProductBId,
			ProductAName: r.ProductAName,
			ProductBName: r.ProductBName,
			Count:        r.Count,
		}
	}
	return result, nil
}
