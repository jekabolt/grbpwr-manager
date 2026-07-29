package membership

import (
	"strings"
	"testing"
)

func TestComputeQualifyingSpendEURUsesSharedNetExpression(t *testing.T) {
	const want = "co.total_price_eur * (1 - (co.refunded_amount / NULLIF(co.total_price, 0)))"
	if NetEURSpendExpr != want {
		t.Fatalf("NetEURSpendExpr = %q, want %q", NetEURSpendExpr, want)
	}
	if count := strings.Count(computeQualifyingSpendEURQuery, NetEURSpendExpr); count != 1 {
		t.Fatalf("ComputeQualifyingSpendEUR query contains NetEURSpendExpr %d times, want 1", count)
	}
}
