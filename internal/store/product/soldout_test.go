package product

import (
	"strings"
	"testing"
)

// TestSoldOutSelectUsesLessThanOrEqual pins the SQL sold_out projection to <=0 (50-B): the Go
// definition (entity.SoldOutFromSizes) already treats a negative total as sold out, matching an
// oversell-bug/race scenario where stock can technically go negative. Before this fix the SQL
// projection used a strict "= 0", so anomalous negative-stock data read as sold_out=false in SQL
// while entity.SoldOutFromSizes said true for the exact same numbers -- this is a static, DB-free
// regression guard against that drift reappearing (see internal/entity/soldout_test.go for the Go
// side).
func TestSoldOutSelectUsesLessThanOrEqual(t *testing.T) {
	const want = "<= 0 AS sold_out"
	if !strings.Contains(soldOutSelect, want) {
		t.Errorf("soldOutSelect = %q; want it to contain %q so negative stock (anomalous data) "+
			"still reads sold_out=true, matching entity.SoldOutFromSizes", soldOutSelect, want)
	}
}
