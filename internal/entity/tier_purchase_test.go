package entity

import (
	"fmt"
	"testing"
)

// TestTierCanPurchase is the truth table for the single tier-gating predicate used by BOTH the
// server-side purchase block and the storefront `locked` projection. The hacker track (99) is
// invite-only: min_tier=99 is satisfied ONLY by tier 99; every other min_tier is satisfied by
// buyerTier >= minTier (so hacker, the highest code, also qualifies for all numeric tiers).
func TestTierCanPurchase(t *testing.T) {
	cases := []struct {
		buyer, min int16
		want       bool
	}{
		// guest / member (0)
		{0, 0, true}, {0, 1, false}, {0, 2, false}, {0, 99, false},
		// plus (1)
		{1, 0, true}, {1, 1, true}, {1, 2, false}, {1, 99, false},
		// plus_plus (2)
		{2, 0, true}, {2, 1, true}, {2, 2, true}, {2, 99, false},
		// hacker (99) — qualifies for everything, numeric tiers included
		{99, 0, true}, {99, 1, true}, {99, 2, true}, {99, 99, true},
	}
	for _, c := range cases {
		t.Run(fmt.Sprintf("buyer=%d_min=%d", c.buyer, c.min), func(t *testing.T) {
			if got := TierCanPurchase(c.buyer, c.min); got != c.want {
				t.Fatalf("TierCanPurchase(%d, %d) = %v, want %v", c.buyer, c.min, got, c.want)
			}
		})
	}
}
