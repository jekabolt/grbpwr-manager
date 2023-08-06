package store

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type purchaseStore struct {
	*MYSQLStore
}

// PurchaseStore returns an object implementing purchase interface
func (ms *MYSQLStore) Purchase() dependency.Purchase {
	return &purchaseStore{
		MYSQLStore: ms,
	}
}

// Acquire acquires an order if order is valid and all items are available
func (ms *MYSQLStore) Acquire(ctx context.Context, oid int64, payment *dto.Payment) error {

	return ms.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		// Validate order
		valid, err := store.Purchase().ValidateOrder(ctx, oid)
		if err != nil {
			return fmt.Errorf("error validating order: %v", err)
		}
		if !valid {
			return fmt.Errorf("order is not valid")
		}

		items, err := store.Order().GetOrderItems(ctx, oid)
		if err != nil {
			return fmt.Errorf("error getting order items: %v", err)
		}

		err = store.Order().OrderPaymentDone(ctx, oid, payment)
		if err != nil {
			return fmt.Errorf("error updating order payment: %v", err)
		}

		err = store.Products().DecreaseAvailableSizes(ctx, items)
		if err != nil {
			return fmt.Errorf("error decreasing available sizes: %v", err)
		}

		return nil

	})

}

func (ms *MYSQLStore) ValidateOrder(ctx context.Context, oid int64) (bool, error) {
	// Query to get all order items for the given order id`
	rows, err := ms.db.QueryContext(ctx, `
		SELECT oi.product_id, oi.quantity, oi.size, ps.XXS, ps.XS, ps.S, ps.M, ps.L, ps.XL, ps.XXL, ps.OS
        FROM order_item oi 
        INNER JOIN product_sizes ps ON oi.product_id = ps.product_id
        WHERE oi.order_id = ?`, oid)
	if err != nil {
		return false, err
	}
	defer rows.Close()

	// Process each row (each order item)
	for rows.Next() {
		var productId int
		var quantity int
		var size string
		var XXS, XS, S, M, L, XL, XXL, OS int

		// Scan the result into our variables
		if err := rows.Scan(&productId, &quantity, &size, &XXS, &XS, &S, &M, &L, &XL, &XXL, &OS); err != nil {
			return false, err
		}

		// Switch-case to match the product size and compare it with order quantity
		switch size {
		case "XXS":
			if XXS < quantity {
				return false, nil
			}
		case "XS":
			if XS < quantity {
				return false, nil
			}
		case "S":
			if S < quantity {
				return false, nil
			}
		case "M":
			if M < quantity {
				return false, nil
			}
		case "L":
			if L < quantity {
				return false, nil
			}
		case "XL":
			if XL < quantity {
				return false, nil
			}
		case "XXL":
			if XXL < quantity {
				return false, nil
			}
		case "OS":
			if OS < quantity {
				return false, nil
			}
		default:
			return false, fmt.Errorf("invalid size: %s", size)
		}
	}

	// If we got through all order items without returning false, all items are in stock and we return true
	return true, nil
}
