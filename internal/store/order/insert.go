package order

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func insertAddresses(ctx context.Context, db dependency.DB, shippingAddress, billingAddress *entity.AddressInsert) (int, int, error) {
	query := `
		INSERT INTO address (country, state, city, address_line_one, address_line_two, company, postal_code)
		VALUES (:country, :state, :city, :address_line_one, :address_line_two, :company, :postal_code);
	`

	insertAddress := func(address *entity.AddressInsert) (int64, error) {
		result, err := db.NamedExecContext(ctx, query, address)
		if err != nil {
			return 0, err
		}
		return result.LastInsertId()
	}

	if *shippingAddress == *billingAddress {
		id, err := insertAddress(shippingAddress)
		if err != nil {
			return 0, 0, err
		}
		return int(id), int(id), nil
	}

	shippingID, err := insertAddress(shippingAddress)
	if err != nil {
		return 0, 0, err
	}

	billingID, err := insertAddress(billingAddress)
	if err != nil {
		return 0, 0, err
	}

	return int(shippingID), int(billingID), nil
}

func insertBuyer(ctx context.Context, db dependency.DB, b *entity.BuyerInsert, sAdr, bAdr int) error {
	phone, err := sanitizePhone(b.Phone)
	if err != nil {
		return fmt.Errorf("invalid phone number: %w", err)
	}

	query := `
	INSERT INTO buyer 
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
	VALUES 
		(:orderId, :firstName, :lastName, :email, :phone, :billingAddressId, :shippingAddressId)
	`

	email := strings.ToLower(strings.TrimSpace(b.Email))
	err = storeutil.ExecNamed(ctx, db, query, map[string]interface{}{
		"orderId":           b.OrderId,
		"firstName":         b.FirstName,
		"lastName":          b.LastName,
		"email":             email,
		"phone":             phone,
		"billingAddressId":  bAdr,
		"shippingAddressId": sAdr,
	})
	if err != nil {
		return fmt.Errorf("can't insert buyer: %w", err)
	}

	return nil
}

func insertPaymentRecord(ctx context.Context, db dependency.DB, paymentMethodId, orderId int, expiredAt time.Time) error {
	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done, expired_at)
		VALUES (:orderId, :paymentMethodId, 0, 0, false, :expiredAt);
	`

	err := storeutil.ExecNamed(ctx, db, insertQuery, map[string]interface{}{
		"orderId":         orderId,
		"paymentMethodId": paymentMethodId,
		"expiredAt":       sql.NullTime{Time: expiredAt, Valid: true},
	})
	if err != nil {
		return fmt.Errorf("can't insert payment record: %w", err)
	}

	return nil
}

func insertPaymentRecordForCustomOrder(ctx context.Context, db dependency.DB, paymentMethodId, orderId int, totalAmount decimal.Decimal) error {
	insertQuery := `
		INSERT INTO payment (order_id, payment_method_id, transaction_amount, transaction_amount_payment_currency, is_transaction_done, expired_at)
		VALUES (:orderId, :paymentMethodId, :transactionAmount, :transactionAmountPaymentCurrency, true, NULL);
	`
	err := storeutil.ExecNamed(ctx, db, insertQuery, map[string]interface{}{
		"orderId":                          orderId,
		"paymentMethodId":                  paymentMethodId,
		"transactionAmount":                totalAmount,
		"transactionAmountPaymentCurrency": totalAmount,
	})
	if err != nil {
		return fmt.Errorf("can't insert payment record for custom order: %w", err)
	}
	return nil
}

func insertOrderItems(ctx context.Context, db dependency.DB, items []entity.OrderItemInsert, orderID int) error {
	if len(items) == 0 {
		return fmt.Errorf("no order items to insert")
	}
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		row := map[string]any{
			"order_id":                orderID,
			"product_id":              item.ProductId,
			"product_price":           item.ProductPriceDecimal(),
			"product_sale_percentage": item.ProductSalePercentageDecimal(),
			"quantity":                item.QuantityDecimal(),
			"size_id":                 item.SizeId,
		}
		rows = append(rows, row)
	}

	return storeutil.BulkInsert(ctx, db, "order_item", rows)
}

func deleteOrderItems(ctx context.Context, db dependency.DB, orderId int) error {
	query := `DELETE FROM order_item WHERE order_id = :orderId`
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't delete order items: %w", err)
	}
	return nil
}

func insertShipment(ctx context.Context, db dependency.DB, sc *entity.ShipmentCarrier, orderId int, cost decimal.Decimal, freeShipping bool) error {
	query := `
	INSERT INTO shipment (carrier_id, order_id, cost, free_shipping)
	VALUES (:carrierId, :orderId, :cost, :freeShipping)
	`
	err := storeutil.ExecNamed(ctx, db, query, map[string]interface{}{
		"carrierId":    sc.Id,
		"orderId":      orderId,
		"cost":         cost,
		"freeShipping": freeShipping,
	})
	if err != nil {
		return fmt.Errorf("can't insert shipment: %w", err)
	}
	return nil
}

func insertOrder(ctx context.Context, db dependency.DB, order *entity.Order) (int, string, error) {
	var err error
	query := `
	INSERT INTO customer_order
	 (uuid, total_price, currency, order_status_id, promo_id, ga_client_id)
	 VALUES (:uuid, :totalPrice, :currency, :orderStatusId, :promoId, :gaClientId)
	`

	orderRef := generateOrderReference()
	order.Id, err = storeutil.ExecNamedLastId(ctx, db, query, map[string]interface{}{
		"uuid":          orderRef,
		"totalPrice":    order.TotalPriceDecimal(),
		"currency":      order.Currency,
		"orderStatusId": order.OrderStatusId,
		"promoId":       order.PromoId,
		"gaClientId":    order.GAClientID,
	})
	if err != nil {
		return 0, "", fmt.Errorf("can't insert order: %w", err)
	}
	return order.Id, orderRef, nil
}

func (s *Store) insertOrderDetails(ctx context.Context, db dependency.DB, order *entity.Order, validItemsInsert []entity.OrderItemInsert, carrier *entity.ShipmentCarrier, shipmentCost decimal.Decimal, freeShipping bool, orderNew *entity.OrderNew) error {
	var err error
	order.Id, order.UUID, err = insertOrder(ctx, db, order)
	if err != nil {
		return fmt.Errorf("error while inserting final order: %w", err)
	}

	slog.Info("inserting order items", "order_id", order.Id, "items", validItemsInsert)
	if err = insertOrderItems(ctx, db, validItemsInsert, order.Id); err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	if err = insertShipment(ctx, db, carrier, order.Id, shipmentCost, freeShipping); err != nil {
		return fmt.Errorf("error while inserting shipment: %w", err)
	}
	shippingAddressId, billingAddressId, err := insertAddresses(ctx, db, orderNew.ShippingAddress, orderNew.BillingAddress)
	if err != nil {
		return fmt.Errorf("error while inserting addresses: %w", err)
	}
	orderNew.Buyer.OrderId = order.Id
	if err = insertBuyer(ctx, db, orderNew.Buyer, shippingAddressId, billingAddressId); err != nil {
		return fmt.Errorf("error while inserting buyer: %w", err)
	}
	return nil
}

func insertRefundedOrderItems(ctx context.Context, db dependency.DB, orderId int, refundedByItem map[int]int64) error {
	for orderItemId, qty := range refundedByItem {
		query := `INSERT INTO refunded_order_item (order_id, order_item_id, quantity_refunded) VALUES (:orderId, :orderItemId, :quantityRefunded)`
		if err := storeutil.ExecNamed(ctx, db, query, map[string]any{
			"orderId":          orderId,
			"orderItemId":      orderItemId,
			"quantityRefunded": qty,
		}); err != nil {
			return fmt.Errorf("insert refunded_order_item: %w", err)
		}
	}
	return nil
}
