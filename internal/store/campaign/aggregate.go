package campaign

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/membership"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// RefreshMarketingAggregate fully rebuilds the per-account marketing aggregate
// inside one transaction so readers never observe the intermediate empty state.
func (s *Store) RefreshMarketingAggregate(ctx context.Context) (int64, error) {
	statusIDs := membership.QualifyingStatusIDs()
	if len(statusIDs) == 0 {
		return 0, fmt.Errorf("no qualifying order statuses resolved from cache")
	}

	var rowsAffected int64
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		if _, err := rep.DB().ExecContext(ctx, `DELETE FROM marketing_account_aggregate`); err != nil {
			return fmt.Errorf("clear marketing account aggregate: %w", err)
		}

		query := fmt.Sprintf(`
			INSERT INTO marketing_account_aggregate (
				account_id,
				total_spend_eur,
				order_count,
				first_order_at,
				last_order_at
			)
			SELECT
				sa.id,
				COALESCE(SUM(%s), 0),
				COUNT(co.id),
				MIN(co.placed),
				MAX(co.placed)
			FROM storefront_account sa
			JOIN buyer b ON b.email = sa.email
			JOIN customer_order co ON co.id = b.order_id
			WHERE co.order_status_id IN (:statusIDs)
			  AND co.total_price_eur IS NOT NULL
			GROUP BY sa.id`, membership.NetEURSpendExpr)
		query, args, err := storeutil.MakeQuery(query, map[string]any{
			"statusIDs": statusIDs,
		})
		if err != nil {
			return fmt.Errorf("build marketing aggregate refresh query: %w", err)
		}
		result, err := rep.DB().ExecContext(ctx, query, args...)
		if err != nil {
			return fmt.Errorf("populate marketing account aggregate: %w", err)
		}
		rowsAffected, err = result.RowsAffected()
		if err != nil {
			return fmt.Errorf("marketing account aggregate rows affected: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("refresh marketing account aggregate: %w", err)
	}
	return rowsAffected, nil
}
