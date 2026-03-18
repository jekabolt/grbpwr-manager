package order

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// AddOrderReview adds an order-level review (delivery & packaging) and item-level reviews
// for a delivered order. The buyer is identified by order UUID and email.
func (s *Store) AddOrderReview(ctx context.Context, orderUUID string, email string, orderReview *entity.OrderReviewInsert, itemReviews []entity.OrderItemReviewInsert) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		db := rep.DB()

		// 1. Get order and verify it exists
		order, err := getOrderByUUID(ctx, db, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by UUID: %w", err)
		}

		// 2. Verify order status is "delivered"
		eos, ok := cache.GetOrderStatusById(order.OrderStatusId)
		if !ok {
			return &entity.ValidationError{Message: "order status not found"}
		}
		if eos.Status.Name != entity.Delivered {
			return &entity.ValidationError{Message: "reviews can only be submitted for delivered orders"}
		}

		// 3. Verify buyer email matches
		buyer, err := getBuyerById(ctx, db, order.Id)
		if err != nil {
			return fmt.Errorf("can't get buyer: %w", err)
		}
		if !strings.EqualFold(buyer.Email, email) {
			return &entity.ValidationError{Message: "email does not match the order buyer"}
		}

		// 4. Validate order review
		if err := entity.ValidateOrderReviewInsert(orderReview); err != nil {
			return err
		}

		// 5. Get order items to validate item reviews
		orderItemsMap, err := getOrdersItems(ctx, db, order.Id)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}
		orderItems := orderItemsMap[order.Id]

		validItemIds := make(map[int]bool, len(orderItems))
		for _, item := range orderItems {
			validItemIds[item.Id] = true
		}

		// 6. Validate each item review
		for i := range itemReviews {
			if err := entity.ValidateOrderItemReviewInsert(&itemReviews[i]); err != nil {
				return fmt.Errorf("item review validation failed for order_item_id %d: %w", itemReviews[i].OrderItemId, err)
			}
			if !validItemIds[itemReviews[i].OrderItemId] {
				return &entity.ValidationError{Message: fmt.Sprintf("order_item_id %d does not belong to this order", itemReviews[i].OrderItemId)}
			}
		}

		// 7. Insert order-level review
		_, err = storeutil.ExecNamedLastId(ctx, db, `
			INSERT INTO order_review (order_id, delivery_rating, packaging_rating)
			VALUES (:orderId, :deliveryRating, :packagingRating)`,
			map[string]any{
				"orderId":         order.Id,
				"deliveryRating":  string(orderReview.DeliveryRating),
				"packagingRating": string(orderReview.PackagingRating),
			})
		if err != nil {
			return fmt.Errorf("can't insert order review: %w", err)
		}

		// 8. Insert item-level reviews
		for _, ir := range itemReviews {
			_, err = storeutil.ExecNamedLastId(ctx, db, `
				INSERT INTO order_item_review (order_item_id, rating, fit_rating, recommend, text)
				VALUES (:orderItemId, :rating, :fitRating, :recommend, :text)`,
				map[string]any{
					"orderItemId": ir.OrderItemId,
					"rating":      string(ir.Rating),
					"fitRating":   string(ir.FitRating),
					"recommend":   ir.Recommend,
					"text":        ir.Text,
				})
			if err != nil {
				return fmt.Errorf("can't insert item review for order_item_id %d: %w", ir.OrderItemId, err)
			}
		}

		return nil
	})
}

// GetOrderReviewsPaged returns paginated order reviews with their item reviews.
func (s *Store) GetOrderReviewsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.OrderReviewFull, int, error) {
	orderDirection := "DESC"
	if orderFactor == entity.Ascending {
		orderDirection = "ASC"
	}

	// Count total
	count, err := storeutil.QueryCountNamed(ctx, s.DB,
		`SELECT COUNT(*) FROM order_review`, map[string]any{})
	if err != nil {
		return nil, 0, fmt.Errorf("can't count order reviews: %w", err)
	}

	if count == 0 {
		return []entity.OrderReviewFull{}, 0, nil
	}

	// Fetch order reviews
	query := fmt.Sprintf(`
		SELECT * FROM order_review
		ORDER BY created_at %s
		LIMIT :limit OFFSET :offset`, orderDirection)

	orderReviews, err := storeutil.QueryListNamed[entity.OrderReview](ctx, s.DB, query, map[string]any{
		"limit":  limit,
		"offset": offset,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get order reviews: %w", err)
	}

	if len(orderReviews) == 0 {
		return []entity.OrderReviewFull{}, count, nil
	}

	// Collect order IDs to fetch item reviews
	orderIds := make([]int, 0, len(orderReviews))
	for _, or := range orderReviews {
		orderIds = append(orderIds, or.OrderId)
	}

	// Fetch all item reviews for these orders
	itemReviews, err := storeutil.QueryListNamed[entity.OrderItemReview](ctx, s.DB, `
		SELECT oir.*
		FROM order_item_review oir
		INNER JOIN order_item oi ON oi.id = oir.order_item_id
		WHERE oi.order_id IN (:orderIds)`,
		map[string]any{
			"orderIds": orderIds,
		})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get item reviews: %w", err)
	}

	// Map item reviews by order_id (need to join through order_item)
	// First, get order_item_id -> order_id mapping
	type orderItemMapping struct {
		Id      int `db:"id"`
		OrderId int `db:"order_id"`
	}
	itemMappings, err := storeutil.QueryListNamed[orderItemMapping](ctx, s.DB, `
		SELECT id, order_id FROM order_item
		WHERE order_id IN (:orderIds)`,
		map[string]any{
			"orderIds": orderIds,
		})
	if err != nil {
		return nil, 0, fmt.Errorf("can't get order item mappings: %w", err)
	}

	itemIdToOrderId := make(map[int]int, len(itemMappings))
	for _, m := range itemMappings {
		itemIdToOrderId[m.Id] = m.OrderId
	}

	// Group item reviews by order_id
	itemReviewsByOrderId := make(map[int][]entity.OrderItemReview)
	for _, ir := range itemReviews {
		orderId := itemIdToOrderId[ir.OrderItemId]
		itemReviewsByOrderId[orderId] = append(itemReviewsByOrderId[orderId], ir)
	}

	// Build result
	result := make([]entity.OrderReviewFull, 0, len(orderReviews))
	for _, or := range orderReviews {
		irs := itemReviewsByOrderId[or.OrderId]
		if irs == nil {
			irs = []entity.OrderItemReview{}
		}
		result = append(result, entity.OrderReviewFull{
			OrderReview: or,
			ItemReviews: irs,
		})
	}

	return result, count, nil
}

// DeleteOrderReview deletes an order review and its associated item reviews (via CASCADE).
func (s *Store) DeleteOrderReview(ctx context.Context, orderId int) error {
	err := storeutil.ExecNamed(ctx, s.DB, `
		DELETE FROM order_review WHERE order_id = :orderId`,
		map[string]any{
			"orderId": orderId,
		})
	if err != nil {
		return fmt.Errorf("can't delete order review: %w", err)
	}

	// Also delete item reviews (they don't cascade from order_review, they cascade from order_item)
	err = storeutil.ExecNamed(ctx, s.DB, `
		DELETE oir FROM order_item_review oir
		INNER JOIN order_item oi ON oi.id = oir.order_item_id
		WHERE oi.order_id = :orderId`,
		map[string]any{
			"orderId": orderId,
		})
	if err != nil {
		return fmt.Errorf("can't delete item reviews: %w", err)
	}

	return nil
}
