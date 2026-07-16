package bigquery

import "fmt"

// Product identification in GA4/BigQuery after the SKU redesign.
//
// Before, products were keyed by a 10-digit numeric id embedded in the item_id SKU and in the
// URL path (REGEXP_EXTRACT ... r'(\d{10})'). That id no longer appears anywhere public:
//   - item_id (GA4 ecommerce items[]) now carries the *variant* SKU, e.g. "SS26-00021-BLK-04";
//   - page_path is "/p/{pretty}-{sku}" where {sku} is the lower-case 14-char *base* SKU.
// Both collapse to the base SKU for product-level aggregation. GA4/BigQuery history on the old
// numeric ids does not carry over — these queries identify products by base SKU going forward.
//
// The base SKU shape is {SEASON}{YY}-{MODEL:5}-{COLOR:3} → two letters, two digits, '-', five
// digits, '-', three alphanumerics (see internal/store/product/sku.go). The variant SKU appends
// "-{SIZE_ORD:2}"; extracting the base pattern strips that size tail, so a variant item_id and a
// base-SKU URL resolve to the same product key.

// baseSKUFromItemID returns a BigQuery expression extracting the upper-case base SKU from an
// item_id column/expression (item_id is upper-case, matching order_item.sku).
func baseSKUFromItemID(itemIDExpr string) string {
	return fmt.Sprintf(`REGEXP_EXTRACT(%s, r'^((?:SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3})(?:-[0-9]{2})?$')`, itemIDExpr)
}

// baseSKUFromPath returns a BigQuery expression extracting the base SKU from a page_path
// ("/p/{pretty}-{sku}") and upper-casing it so it aligns with baseSKUFromItemID for joins. The URL
// sku is lower-case; the pattern is specific enough (a 5-digit run) not to match the pretty slug.
func baseSKUFromPath(pathExpr string) string {
	return fmt.Sprintf(`UPPER(REGEXP_EXTRACT(%s, r'/((?:ss|fw|pf|rc)[0-9]{2}-[0-9]{5}-[a-z0-9]{3})$'))`, pathExpr)
}
