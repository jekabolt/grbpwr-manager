package metrics

import "fmt"

// Shared SQL for apportioning the actual Stripe-settled base amount across line items.
//
// Order-level revenue metrics anchor to customer_order.total_settled_base (the amount
// Stripe actually settled, in base currency) when present, falling back to the
// product_price reconstruction. Item/category metrics cannot use that order total
// directly, so each line item's base list value (pp_base.price * (1-sale) * qty) is
// scaled by a per-order factor that reproduces the same total:
//
//	adj = (100 - discount)/100               -- promo discount applied to items
//	      * COALESCE(settled, recon)/recon   -- scale reconstruction to the actual settled amount (FX)
//	      * (total_price - refunded)/total    -- refund proration
//
// where recon = items_base_total*(100-discount)/100 + shipping. For EUR orders with no
// FX gap and no refund the factor reduces to (100-discount)/100, so item revenues sum to
// the discounted items subtotal (shipping is intentionally excluded from product revenue).
// For orders with no captured settled amount (pre-feature / non-Stripe) the COALESCE keeps
// the pure reconstruction.

// orderFactorsCTENamed returns an order_factors CTE body (no leading WITH) named `name`,
// covering orders in [:fromParam, :toParam) with status IN (:statusIds) priced in
// :baseCurrency. Join it as `JOIN <name> <alias> ON <alias>.order_id = oi.order_id` and
// reference the per-item multiplier via itemAdj(alias) / itemAdjGross(alias).
func orderFactorsCTENamed(name, fromParam, toParam string) string {
	return fmt.Sprintf(`%s AS (
		SELECT co.id AS order_id, co.placed, co.total_price,
			COALESCE(co.refunded_amount, 0) AS refunded_amount,
			co.total_settled_base,
			COALESCE(MAX(pc.discount), 0) AS discount,
			COALESCE(MAX(pc.free_shipping), 0) AS free_shipping,
			COALESCE(MAX(scp.price), 0) AS shipment_base,
			COALESCE(SUM(pp_base.price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base_total
		FROM customer_order co
		LEFT JOIN order_item oi ON co.id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		LEFT JOIN shipment s ON co.id = s.order_id
		LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		LEFT JOIN promo_code pc ON co.promo_id = pc.id
		WHERE co.placed >= :%s AND co.placed < :%s
		AND co.order_status_id IN (:statusIds)
		GROUP BY co.id, co.placed, co.total_price, co.refunded_amount, co.total_settled_base
	)`, name, fromParam, toParam)
}

// itemAdj is the per-order multiplier (incl. refund ratio) for the order_factors row
// joined as `alias`. Note: `of` is a reserved word in MySQL 8, so callers alias e.g. `ofac`.
func itemAdj(alias string) string {
	return fmt.Sprintf(`((100 - %[1]s.discount) / 100.0
		* COALESCE(%[1]s.total_settled_base, %[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END)
			/ NULLIF(%[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END, 0)
		* (%[1]s.total_price - %[1]s.refunded_amount) / NULLIF(%[1]s.total_price, 0))`, alias)
}

// itemAdjGross is itemAdj WITHOUT the refund ratio — promo discount × fx/actual scaling
// only. Use it where refunds are already accounted for at the line-item level (e.g. net
// units via refunded_order_item), so refunds are not double-counted.
func itemAdjGross(alias string) string {
	return fmt.Sprintf(`((100 - %[1]s.discount) / 100.0
		* COALESCE(%[1]s.total_settled_base, %[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END)
			/ NULLIF(%[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END, 0))`, alias)
}

// costAdj is the per-order multiplier for COGS on the order_factors row aliased `alias`.
// Unlike itemAdj it applies ONLY the refund proration — a product's cost is not reduced
// by a promo/sale discount, nor scaled by the settled-vs-list (FX) gap, since cost_price
// is already stored in base currency. Using the same refund ratio as itemAdj keeps a
// refunded unit removed from both revenue and cost, so margin stays on net units.
func costAdj(alias string) string {
	return fmt.Sprintf(`((%[1]s.total_price - %[1]s.refunded_amount) / NULLIF(%[1]s.total_price, 0))`, alias)
}

// Convenience values for the common single-period case: a CTE named order_factors over
// [:from, :to), joined as alias `ofac`.
var (
	orderFactorsCTE  = orderFactorsCTENamed("order_factors", "from", "to")
	itemAdjExpr      = itemAdj("ofac")
	itemAdjGrossExpr = itemAdjGross("ofac")
	costAdjExpr      = costAdj("ofac")
)
