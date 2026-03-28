package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func (s *Store) getRevenueByGeography(ctx context.Context, from, to time.Time, groupBy string, country, city *string) ([]entity.GeographyMetric, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	var groupCol, selectCol string
	if groupBy == "city" {
		groupCol = "a.country, a.state, a.city"
		selectCol = "a.country, a.state, a.city"
	} else {
		groupCol = "a.country"
		selectCol = "a.country, NULL AS state, NULL AS city"
	}
	query := fmt.Sprintf(`
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
		),
		total_revenue AS (
			SELECT COALESCE(SUM(revenue_base), 0) AS total FROM order_base
		)
		SELECT %s,
			COALESCE(SUM(ob.revenue_base), 0) AS value,
			COUNT(DISTINCT ob.id) AS cnt,
			COALESCE(SUM(ob.revenue_base) / NULLIF((SELECT total FROM total_revenue), 0) * 100, 0) AS share_pct,
			COALESCE(AVG(ob.revenue_base), 0) AS aov
		FROM order_base ob
		JOIN buyer b ON ob.id = b.order_id
		JOIN address a ON b.shipping_address_id = a.id
		WHERE (:country IS NULL OR a.country = :country)
		AND (:city IS NULL OR a.city = :city)
		GROUP BY %s
		ORDER BY value DESC
		LIMIT 50
	`, selectCol, groupCol)

	params := map[string]any{"from": from, "to": to, "country": country, "city": city, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()}
	rows, err := storeutil.QueryListNamed[struct {
		Country  string          `db:"country"`
		State    *string         `db:"state"`
		City     *string         `db:"city"`
		Value    decimal.Decimal `db:"value"`
		Count    int             `db:"cnt"`
		SharePct float64         `db:"share_pct"`
		AOV      decimal.Decimal `db:"aov"`
	}](ctx, s.DB, query, params)
	if err != nil {
		return nil, err
	}

	result := make([]entity.GeographyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.GeographyMetric{
			Country:       r.Country,
			State:         r.State,
			City:          r.City,
			Value:         r.Value,
			Count:         r.Count,
			SharePct:      &r.SharePct,
			AvgOrderValue: &r.AOV,
		}
	}
	return result, nil
}

func (s *Store) getAvgOrderByGeography(ctx context.Context, from, to time.Time) ([]entity.GeographyMetric, error) {
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
		SELECT a.country,
			COALESCE(AVG(ob.revenue_base), 0) AS value,
			COUNT(DISTINCT ob.id) AS cnt
		FROM order_base ob
		JOIN buyer b ON ob.id = b.order_id
		JOIN address a ON b.shipping_address_id = a.id
		GROUP BY a.country
		ORDER BY value DESC
		LIMIT 30
	`
	rows, err := storeutil.QueryListNamed[struct {
		Country string          `db:"country"`
		Value   decimal.Decimal `db:"value"`
		Count   int             `db:"cnt"`
	}](ctx, s.DB, query, map[string]any{"from": from, "to": to, "baseCurrency": baseCurrency, "statusIds": cache.OrderStatusIDsForNetRevenue()})
	if err != nil {
		return nil, err
	}
	result := make([]entity.GeographyMetric, len(rows))
	for i, r := range rows {
		result[i] = entity.GeographyMetric{Country: r.Country, Value: r.Value, Count: r.Count}
	}
	return result, nil
}

func (s *Store) getRevenueByRegion(byCountry []entity.GeographyMetric) ([]entity.RegionMetric, error) {
	regionAgg := make(map[string]struct {
		value decimal.Decimal
		count int
	})
	for _, g := range byCountry {
		cc := strings.ToUpper(strings.TrimSpace(g.Country))
		region, ok := entity.CountryToRegion(cc)
		regionKey := "OTHER"
		if ok {
			regionKey = string(region)
		}
		agg := regionAgg[regionKey]
		agg.value = agg.value.Add(g.Value)
		agg.count += g.Count
		regionAgg[regionKey] = agg
	}
	result := make([]entity.RegionMetric, 0, len(regionAgg))
	for region, agg := range regionAgg {
		result = append(result, entity.RegionMetric{Region: region, Value: agg.value, Count: agg.count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Value.GreaterThan(result[j].Value) })
	return result, nil
}

// GetRevenueByCountry returns revenue breakdown by country for the given time period.
// Public wrapper for geography breakdown used in dedicated geography section.
func (s *Store) GetRevenueByCountry(ctx context.Context, from, to time.Time) ([]entity.GeographyMetric, error) {
	return s.getRevenueByGeography(ctx, from, to, "country", nil, nil)
}
