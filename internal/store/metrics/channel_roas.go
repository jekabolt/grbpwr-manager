package metrics

import (
	"context"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// GetChannelRoasSettled attributes SETTLED order revenue to marketing channels (task 20 step 2). Each
// order's GA client_id (customer_order.ga_client_id) is joined to bq_order_channel — the server-side
// "last non-direct click" map from BigQuery — so revenue is attributed from the authoritative
// settled amount (total_settled_base, reconstruction fallback) rather than GA4's consent/ad-block-
// lossy purchase_revenue. Orders whose client_id has no non-direct touch (or no ga_client_id) fall to
// '(direct)'. Also counts distinct FIRST-TIME customers per channel (first order by buyer email lands
// in the period), which the caller turns into per-channel CAC. Only net-revenue statuses count.
//
// Revenue basis is gross-incl-VAT settled NET OF REFUNDS (what the customer actually paid, minus any
// refunded portion) — the natural ROAS numerator, NOT the net-of-VAT headline; ROAS conventionally
// sits on the amount that came in. The refund proration `(total_price − refunded_amount)/total_price`
// mirrors getCoreSalesMetrics so a partially-refunded order does not inflate a channel's revenue, and
// the report total reconciles with the dashboard settled figure.
func (s *Store) GetChannelRoasSettled(ctx context.Context, from, to time.Time) ([]entity.ChannelSettledRow, error) {
	baseCurrency := strings.ToUpper(cache.GetBaseCurrency())
	query := `
		WITH order_rev AS (
			SELECT co.id, co.ga_client_id, b.email,
				COALESCE(
					co.total_settled_base,
					COALESCE(SUM(COALESCE(oi.product_price_base, pp.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0)
						* (100 - COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0)) / 100.0
						+ CASE WHEN COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) THEN 0 ELSE COALESCE(MAX(scp.price), 0) END
				) * (co.total_price - COALESCE(co.refunded_amount, 0)) / NULLIF(co.total_price, 0) AS settled_base
			FROM customer_order co
			JOIN buyer b ON b.order_id = co.id
			LEFT JOIN order_item oi ON oi.order_id = co.id
			LEFT JOIN product_price pp ON oi.product_id = pp.product_id AND UPPER(pp.currency) = UPPER(:baseCurrency)
			LEFT JOIN shipment s ON s.order_id = co.id
			LEFT JOIN shipment_carrier_price scp ON scp.shipment_carrier_id = s.carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
			LEFT JOIN promo_code pc ON pc.id = co.promo_id
			WHERE co.placed >= :from AND co.placed < :to
				AND co.order_status_id IN (:statusIds)
			GROUP BY co.id, co.ga_client_id, b.email, co.total_settled_base, co.total_price, co.refunded_amount
		),
		first_order AS (
			SELECT b.email, MIN(co.placed) AS first_placed
			FROM customer_order co
			JOIN buyer b ON b.order_id = co.id
			GROUP BY b.email
		),
		attributed AS (
			SELECT
				COALESCE(NULLIF(oc.utm_source, ''), '(direct)') AS utm_source,
				COALESCE(NULLIF(oc.utm_medium, ''), '(none)') AS utm_medium,
				COALESCE(NULLIF(oc.utm_campaign, ''), '(not set)') AS utm_campaign,
				orv.settled_base,
				orv.email,
				CASE WHEN fo.first_placed >= :from THEN 1 ELSE 0 END AS is_new
			FROM order_rev orv
			LEFT JOIN bq_order_channel oc ON oc.client_id = orv.ga_client_id
			JOIN first_order fo ON fo.email = orv.email
		)
		SELECT utm_source, utm_medium, utm_campaign,
			COALESCE(SUM(settled_base), 0) AS settled_revenue,
			COUNT(*) AS orders,
			COUNT(DISTINCT CASE WHEN is_new = 1 THEN email END) AS new_customers
		FROM attributed
		GROUP BY utm_source, utm_medium, utm_campaign
		ORDER BY settled_revenue DESC
	`
	rows, err := storeutil.QueryListNamed[entity.ChannelSettledRow](ctx, s.DB, query, map[string]any{
		"baseCurrency": baseCurrency,
		"statusIds":    cache.OrderStatusIDsForNetRevenue(),
		"from":         from,
		"to":           to,
	})
	if err != nil {
		return nil, err
	}
	return rows, nil
}
