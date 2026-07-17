package ga4mp

import "regexp"

// GA4 item identity (problem 020, decision R3): item_id is the variant SKU, item_group_id is the base
// SKU, item_variant is the public size ordinal. GA4MP reads ONLY the frozen order_item snapshot
// (order_item.variant_sku_snapshot as of this contract version — entity.OrderItem.SKU) and derives both
// item_group_id and item_variant strictly from that snapshot string, never from a live product/size
// lookup, so a re-minted/changed live SKU can never disagree with what was actually sold.
//
// The shape mirrors internal/store/product/sku.go's fixed-width contract
// ({SEASON}{YY}-{MODEL:5}-{COLOR:3}-{SIZE_ORD:2}, canon v1 — internal/sku/sku-contract-v1.json) without
// importing that package: ga4mp is a leaf analytics client and the two consumers (BigQuery SQL text in
// internal/analytics/bigquery/sku.go, this Go regex) each restate the same grammar locally rather than
// share an import, following the pattern already used by bigquery/sku.go.
var variantSKUPattern = regexp.MustCompile(`^(?:SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3}-[0-9]{2}$`)

// baseSKULen is the fixed length of the base-SKU prefix of a canonical variant SKU (see
// internal/store/product/sku.go BaseSKULen).
const baseSKULen = 14

// splitVariantSKU derives (base SKU, size ordinal) from a frozen variant SKU snapshot. ok is false
// when sku is not a canonical 17-char variant SKU (e.g. a pre-contract legacy snapshot) — the caller
// must then omit item_group_id/item_variant rather than emit a wrong grouping key.
func splitVariantSKU(sku string) (base, sizeCode string, ok bool) {
	if !variantSKUPattern.MatchString(sku) {
		return "", "", false
	}
	return sku[:baseSKULen], sku[baseSKULen+1:], true
}
