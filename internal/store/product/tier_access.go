package product

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SetProductTierAccess updates the per-product tier gating fields. When the tier
// is hacker (99) the product is always hidden from non-qualified customers.
func (s *Store) SetProductTierAccess(ctx context.Context, productID int, minTier int16, hiddenForNonQualified bool) error {
	if minTier == 99 {
		hiddenForNonQualified = true
	}
	q := `UPDATE product SET min_tier = :minTier, hidden_for_non_qualified = :hidden, updated_at = CURRENT_TIMESTAMP WHERE id = :id AND deleted_at IS NULL`
	if err := storeutil.ExecNamed(ctx, s.DB, q, map[string]any{
		"minTier": minTier,
		"hidden":  hiddenForNonQualified,
		"id":      productID,
	}); err != nil {
		return fmt.Errorf("set product tier access: %w", err)
	}
	return nil
}
