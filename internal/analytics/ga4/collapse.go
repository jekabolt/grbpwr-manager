package ga4

import (
	"time"

	"github.com/shopspring/decimal"
)

// prodKey groups a raw GA4 item-conversion row by (date, base SKU) so item-level rows that only
// differ by size variant collapse into one product-family row (problem 020/R3) instead of GA4's
// mismatched raw itemId inflating the funnel into multiple "products".
type prodKey struct {
	date time.Time
	base string
}

// prodAggregator collapses per-itemId GA4 rows into one row per (date, base SKU). GA4's Data API has
// no item_group_id report dimension (only the event-scoped item_id/item_variant/... item parameters),
// and historically item_id disagreed across event types — view/list/cart sent the base SKU while
// checkout/purchase sent the variant SKU (problem 020) — so grouping has to happen on the read side
// here rather than relying on GA4's own dimension grouping. baseSKUFromItemID handles both shapes
// (bare base SKU or variant SKU) uniformly, so this also collapses correctly across the historical
// cutover boundary once every event source agrees on item_id=variant SKU (decision R3).
type prodAggregator struct {
	byKey map[prodKey]*ProductConversionMetrics
	order []prodKey // first-seen key order, so result() is deterministic across a paginated fetch
}

func newProdAggregator() *prodAggregator {
	return &prodAggregator{byKey: make(map[prodKey]*ProductConversionMetrics)}
}

// add folds one raw item-conversion row in, keyed by (date, base SKU extracted from rawItemID). It
// reports ok=false and drops the row when rawItemID does not match the canonical base/variant SKU
// shape at all (blank/garbage dimension value) — the caller should log that case rather than group it
// under a wrong/empty key.
func (a *prodAggregator) add(date time.Time, rawItemID, itemName string, itemsViewed, addToCarts, purchases int, revenue decimal.Decimal) (ok bool) {
	base, ok := baseSKUFromItemID(rawItemID)
	if !ok {
		return false
	}
	key := prodKey{date: date, base: base}
	agg, exists := a.byKey[key]
	if !exists {
		agg = &ProductConversionMetrics{Date: date, ProductID: base}
		a.byKey[key] = agg
		a.order = append(a.order, key)
	}
	// itemName can legitimately vary per size-variant row (translation/casing edge cases); keep the
	// first non-empty one seen for the collapsed family.
	if agg.ProductName == "" {
		agg.ProductName = itemName
	}
	agg.ItemsViewed += itemsViewed
	agg.AddToCarts += addToCarts
	agg.Purchases += purchases
	agg.Revenue = agg.Revenue.Add(revenue)
	return true
}

// result returns one ProductConversionMetrics row per (date, base SKU), in first-seen order.
func (a *prodAggregator) result() []ProductConversionMetrics {
	out := make([]ProductConversionMetrics, 0, len(a.order))
	for _, key := range a.order {
		out = append(out, *a.byKey[key])
	}
	return out
}
