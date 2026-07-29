package order

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

type orderCommentOrder struct {
	Id int `db:"id"`
}

// AddOrderThreadComment appends an authenticated admin comment and updates the
// legacy customer_order.order_comment projection in the same transaction.
func (s *Store) AddOrderThreadComment(ctx context.Context, orderUUID, author, body string) (*entity.OrderComment, error) {
	var comment entity.OrderComment
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := storeutil.QueryNamedOne[orderCommentOrder](ctx, rep.DB(),
			`SELECT id FROM customer_order WHERE uuid = :orderUuid`,
			map[string]any{"orderUuid": orderUUID})
		if err != nil {
			return fmt.Errorf("failed to resolve order UUID: %w", err)
		}

		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE customer_order
			SET order_comment = :body
			WHERE id = :orderId`,
			map[string]any{"body": body, "orderId": order.Id}); err != nil {
			return fmt.Errorf("failed to update legacy order comment: %w", err)
		}

		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(), `
			INSERT INTO order_comment (order_id, author, body)
			VALUES (:orderId, :author, :body)`,
			map[string]any{"orderId": order.Id, "author": author, "body": body})
		if err != nil {
			return fmt.Errorf("failed to insert order comment: %w", err)
		}

		comment, err = storeutil.QueryNamedOne[entity.OrderComment](ctx, rep.DB(), `
			SELECT oc.id, co.uuid AS order_uuid, oc.author, oc.body, oc.created_at
			FROM order_comment oc
			JOIN customer_order co ON co.id = oc.order_id
			WHERE oc.id = :id`,
			map[string]any{"id": id})
		if err != nil {
			return fmt.Errorf("failed to read inserted order comment: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("can't add order thread comment: %w", err)
	}
	return &comment, nil
}

// ListOrderComments returns an order's append-only comment thread, oldest first.
func (s *Store) ListOrderComments(ctx context.Context, orderUUID string) ([]entity.OrderComment, error) {
	comments, err := storeutil.QueryListNamed[entity.OrderComment](ctx, s.DB, `
		SELECT oc.id, co.uuid AS order_uuid, oc.author, oc.body, oc.created_at
		FROM order_comment oc
		JOIN customer_order co ON co.id = oc.order_id
		WHERE co.uuid = :orderUuid
		ORDER BY oc.created_at, oc.id`,
		map[string]any{"orderUuid": orderUUID})
	if err != nil {
		return nil, fmt.Errorf("can't list order comments: %w", err)
	}
	return comments, nil
}
