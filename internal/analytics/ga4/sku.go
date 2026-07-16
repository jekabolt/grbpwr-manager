package ga4

import "regexp"

// itemIDPattern matches GA4's item_id value once every event source (browser view/cart,
// checkout/purchase, backend GA4MP purchase) sends the R3 identity contract (problem 020): item_id is
// the variant SKU ({SEASON}{YY}-{MODEL:5}-{COLOR:3}-{SIZE_ORD:2}). It also matches a bare base SKU (no
// size tail) so pre-cutover historical rows — the browser used to send the base SKU for view/list/cart
// while checkout/purchase already sent the variant SKU — still collapse to the same product family:
// either shape's first capture group is the base SKU. Mirrors
// internal/analytics/bigquery/sku.go's baseSKUFromItemID BigQuery expression (identical grammar,
// restated as a Go regex rather than shared via import — ga4 and bigquery are independent leaf
// analytics clients, following the pattern already established between those two packages).
var itemIDPattern = regexp.MustCompile(`^((?:SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3})(?:-[0-9]{2})?$`)

// baseSKUFromItemID extracts the base SKU from a GA4 itemId dimension value, collapsing a variant SKU
// down to its base and passing a bare base SKU through unchanged. ok is false when itemId does not
// match the canonical shape at all (blank/garbage dimension value) — the caller must then skip the row
// rather than group it under a wrong/empty key.
func baseSKUFromItemID(itemID string) (string, bool) {
	m := itemIDPattern.FindStringSubmatch(itemID)
	if m == nil {
		return "", false
	}
	return m[1], true
}
