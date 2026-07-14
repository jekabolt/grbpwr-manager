package metrics

import "fmt"

// Shared SQL for apportioning the actual Stripe-settled base amount across line items.
//
// Order-level revenue metrics anchor to customer_order.total_settled_base (the amount
// Stripe actually settled, in base currency) when present, falling back to the
// product_price reconstruction. Item/category metrics cannot use that order total
// directly, so each line item's base list value (COALESCE(oi.product_price_base, pp_base.price) * (1-sale) * qty) is
// scaled by a per-order factor that reproduces the same total:
//
//	adj = (100 - discount)/100               -- promo discount applied to items
//	      * COALESCE(settled, recon)/recon   -- scale reconstruction to the actual settled amount (FX)
//	      * refundRatio                       -- refund proration (per-line when line detail exists)
//
// where recon = items_base_total*(100-discount)/100 + shipping. For EUR orders with no
// FX gap and no refund the factor reduces to (100-discount)/100, so item revenues sum to
// the discounted items subtotal (shipping is intentionally excluded from product revenue).
// For orders with no captured settled amount (pre-feature / non-Stripe) the COALESCE keeps
// the pure reconstruction.
//
// Refund proration (refundRatio) is per-LINE when the order has any refunded_order_item
// rows (has_line_refunds = 1): each line keeps 1 - quantity_refunded/quantity of its value,
// so returning one item of a two-item order shrinks only that item's revenue/cost and leaves
// the kept item whole — instead of the old order-level (total_price-refunded)/total ratio
// that bled the refund across every line. Orders with a refund but no line detail
// (has_line_refunds = 0) fall back to that order-level ratio. Non-line refunds (shipping,
// goodwill) on a line-detailed order intentionally do not touch product lines.

// orderFactorsCTENamed returns an order_factors CTE body (no leading WITH) named `name`,
// covering orders in [:fromParam, :toParam) with status IN (:statusIds) priced in
// :baseCurrency. Join it as `JOIN <name> <alias> ON <alias>.order_id = oi.order_id` and
// reference the per-item multiplier via itemAdj(alias) / itemAdjGross(alias).
func orderFactorsCTENamed(name, fromParam, toParam string) string {
	return fmt.Sprintf(`%s AS (
		SELECT co.id AS order_id, co.placed, co.total_price,
			COALESCE(co.refunded_amount, 0) AS refunded_amount,
			co.total_settled_base,
			COALESCE(co.vat_rate_pct, 0) AS vat_rate_pct,
			COALESCE(MAX(co.promo_discount_pct), MAX(pc.discount), 0) AS discount,
			COALESCE(MAX(co.promo_free_shipping), MAX(pc.free_shipping), 0) AS free_shipping,
			COALESCE(MAX(scp.price), 0) AS shipment_base,
			COALESCE(MAX(rl.has), 0) AS has_line_refunds,
			COALESCE(SUM(COALESCE(oi.product_price_base, pp_base.price) * (1 - COALESCE(oi.product_sale_percentage, 0) / 100.0) * oi.quantity), 0) AS items_base_total
		FROM customer_order co
		LEFT JOIN order_item oi ON co.id = oi.order_id
		LEFT JOIN product_price pp_base ON oi.product_id = pp_base.product_id AND UPPER(pp_base.currency) = UPPER(:baseCurrency)
		LEFT JOIN shipment s ON co.id = s.order_id
		LEFT JOIN shipment_carrier_price scp ON s.carrier_id = scp.shipment_carrier_id AND UPPER(scp.currency) = UPPER(:baseCurrency)
		LEFT JOIN promo_code pc ON co.promo_id = pc.id
		LEFT JOIN (SELECT order_id, 1 AS has FROM refunded_order_item GROUP BY order_id) rl ON rl.order_id = co.id
		WHERE co.placed >= :%s AND co.placed < :%s
		AND co.order_status_id IN (:statusIds)
		GROUP BY co.id, co.placed, co.total_price, co.refunded_amount, co.total_settled_base, co.vat_rate_pct
	)`, name, fromParam, toParam)
}

// netOfVat is the per-order factor that converts a VAT-inclusive gross amount to net-of-VAT
// revenue, using the rate snapshotted on the order (0 → factor 1, no change). Prices are
// VAT-inclusive, so net = gross × 100/(100+rate). Applied to revenue only, never to cost
// (cost_price carries no VAT). `alias` is the order_factors row alias.
func netOfVat(alias string) string {
	return fmt.Sprintf(`(100.0 / (100 + COALESCE(%[1]s.vat_rate_pct, 0)))`, alias)
}

// refundRatio is the fraction of a line item's value that survives refunds, for the
// order_factors row aliased `alias`. When the order has line-level refund detail
// (has_line_refunds = 1) it is per-LINE — 1 - quantity_refunded/quantity for THIS
// order_item (oi.id / oi.quantity, which every caller has in scope) — so returning one of
// several items shrinks only that line. Otherwise it falls back to the order-level
// (total_price - refunded)/total ratio. Both itemAdj (revenue) and costAdj (COGS) use this
// same ratio, so a refunded unit drops out of both and margin stays on net units. The
// correlated subquery returns at most one row (refunded_order_item is UNIQUE per
// (order_id, order_item_id) and order_item_id is a global PK); MySQL `/` is decimal
// division, so partial fractions are not truncated.
func refundRatio(alias string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.has_line_refunds = 1
		THEN 1 - COALESCE((SELECT r.quantity_refunded FROM refunded_order_item r WHERE r.order_item_id = oi.id), 0) / NULLIF(oi.quantity, 0)
		ELSE (%[1]s.total_price - %[1]s.refunded_amount) / NULLIF(%[1]s.total_price, 0) END)`, alias)
}

// itemAdj is the per-order revenue multiplier (incl. refund ratio and net-of-VAT) for the
// order_factors row joined as `alias`. Note: `of` is a reserved word in MySQL 8, so callers
// alias e.g. `ofac`.
func itemAdj(alias string) string {
	return fmt.Sprintf(`((100 - %[1]s.discount) / 100.0
		* COALESCE(%[1]s.total_settled_base, %[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END)
			/ NULLIF(%[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END, 0)
		* %[3]s
		* %[2]s)`, alias, netOfVat(alias), refundRatio(alias))
}

// itemAdjGross is itemAdj WITHOUT the refund ratio — promo discount × fx/actual scaling ×
// net-of-VAT only. Use it where refunds are already accounted for at the line-item level (e.g.
// net units via refunded_order_item), so refunds are not double-counted. "Gross" here refers
// to gross-of-refunds, not gross-of-VAT: VAT is still removed.
func itemAdjGross(alias string) string {
	return fmt.Sprintf(`((100 - %[1]s.discount) / 100.0
		* COALESCE(%[1]s.total_settled_base, %[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END)
			/ NULLIF(%[1]s.items_base_total * (100 - %[1]s.discount) / 100.0 + CASE WHEN %[1]s.free_shipping THEN 0 ELSE %[1]s.shipment_base END, 0)
		* %[2]s)`, alias, netOfVat(alias))
}

// costAdj is the per-order multiplier for COGS on the order_factors row aliased `alias`.
// Unlike itemAdj it applies ONLY the refund proration — a product's cost is not reduced
// by a promo/sale discount, nor scaled by the settled-vs-list (FX) gap, since cost_price
// is already stored in base currency. Using the same refund ratio as itemAdj keeps a
// refunded unit removed from both revenue and cost, so margin stays on net units.
func costAdj(alias string) string {
	return refundRatio(alias)
}

// Convenience values for the common single-period case: a CTE named order_factors over
// [:from, :to), joined as alias `ofac`.
var (
	orderFactorsCTE  = orderFactorsCTENamed("order_factors", "from", "to")
	itemAdjExpr      = itemAdj("ofac")
	itemAdjGrossExpr = itemAdjGross("ofac")
	costAdjExpr      = costAdj("ofac")
)
