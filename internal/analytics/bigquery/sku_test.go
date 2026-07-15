package bigquery

import "testing"

// TestBaseSKUExpressions pins the BigQuery base-SKU extraction fragments so an accidental
// change to the pattern (which must match the base SKU shape SS26-00021-BLK) is caught.
func TestBaseSKUExpressions(t *testing.T) {
	if got, want := baseSKUFromItemID("item.item_id"),
		`REGEXP_EXTRACT(item.item_id, r'([A-Z]{2}[0-9]{2}-[0-9]{5}-[A-Z0-9]{3})')`; got != want {
		t.Errorf("baseSKUFromItemID = %q, want %q", got, want)
	}
	if got, want := baseSKUFromPath("pp"),
		`UPPER(REGEXP_EXTRACT(pp, r'([a-z]{2}[0-9]{2}-[0-9]{5}-[a-z0-9]{3})'))`; got != want {
		t.Errorf("baseSKUFromPath = %q, want %q", got, want)
	}
}
