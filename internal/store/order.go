package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
)

type orderStore struct {
	*MYSQLStore
}

// Order returns an object implementing order interface
func (ms *MYSQLStore) Order() dependency.Order {
	return &orderStore{
		MYSQLStore: ms,
	}
}

func (ms *MYSQLStore) CreateOrder(ctx context.Context, order *dto.Order) (*dto.Order, error) {
	if !ms.InTx() {
		return nil, fmt.Errorf("CreateOrder must be called from within transaction")
	}

	baid, err := addAddress(ctx, ms, order.Buyer.BillingAddress)
	if err != nil {
		return nil, fmt.Errorf("addAddress billing  [%v]", err.Error())
	}
	order.Buyer.BillingAddress.ID = baid

	said, err := addAddress(ctx, ms, order.Buyer.ShippingAddress)
	if err != nil {
		return nil, fmt.Errorf("addAddress shipping [%v]", err.Error())
	}
	order.Buyer.ShippingAddress.ID = said

	pid, err := addPayment(ctx, ms, order.Payment)
	if err != nil {
		return nil, fmt.Errorf("addPayment [%v]", err.Error())
	}
	order.Payment.ID = pid

	bid, err := addBuyer(ctx, ms, order.Buyer)
	if err != nil {
		return nil, fmt.Errorf("addBuyer [%v]", err.Error())
	}
	order.Buyer.ID = bid

	sid, err := addShipment(ctx, ms, order.Shipment.Carrier)
	if err != nil {
		return nil, fmt.Errorf("addShipment [%v]", err.Error())
	}
	order.Shipment.ID = sid

	err = addOrder(ctx, ms, order, bid, pid, sid)
	if err != nil {
		return nil, fmt.Errorf("addOrder [%v]", err.Error())
	}

	return order, nil
}

func addPayment(ctx context.Context, rep dependency.Repository, payment *dto.Payment) (int64, error) {
	if !rep.InTx() {
		return 0, fmt.Errorf("addPayment must be called from within transaction")
	}

	// empty tx related fields
	txid := ""
	payer := ""
	payee := ""
	isTransactionDone := false

	// Insert payment
	res, err := rep.DB().ExecContext(ctx, `
	INSERT INTO payment 
	(
		method_id,
		currency_id,
		currency_transaction_id,
		transaction_amount,
		payer,
		payee,
		is_transaction_done
	) 
    VALUES (
		(SELECT id FROM payment_method WHERE method = ?), 
		(SELECT id FROM payment_currency WHERE currency = ?),
	 	?, ?, ?, ?, ?)`,
		payment.Method, payment.Currency, txid, payment.TransactionAmount, payer, payee, isTransactionDone)
	if err != nil {
		return 0, err
	}
	paymentID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return paymentID, nil
}

func addAddress(ctx context.Context, rep dependency.Repository, address *dto.Address) (int64, error) {
	if !rep.InTx() {
		return 0, fmt.Errorf("insertAddress must be called from within transaction")
	}
	res, err := rep.DB().ExecContext(ctx, `INSERT INTO address (street, house_number, apartment_number, city, state, country, postal_code) 
	VALUES (?, ?, ?, ?, ?, ?, ?)`, address.Street, address.HouseNumber, address.ApartmentNumber, address.City, address.State, address.Country, address.PostalCode)
	if err != nil {
		return 0, err
	}
	addressID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return addressID, nil
}

func addBuyer(ctx context.Context, rep dependency.Repository, buyer *dto.Buyer) (int64, error) {
	if !rep.InTx() {
		return 0, fmt.Errorf("addBuyer must be called from within transaction")
	}
	res, err := rep.DB().ExecContext(ctx, `INSERT INTO buyer (first_name, last_name, email, phone, billing_address_id, shipping_address_id, receive_promo_emails) 
	VALUES (?, ?, ?, ?, ?, ?, ?)`, buyer.FirstName, buyer.LastName, buyer.Email, buyer.Phone, buyer.BillingAddress.ID, buyer.ShippingAddress.ID, buyer.ReceivePromoEmails)
	if err != nil {
		return 0, err
	}
	buyerID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return buyerID, nil
}

func addShipment(ctx context.Context, rep dependency.Repository, carrier string) (int64, error) {
	if !rep.InTx() {
		return 0, fmt.Errorf("addShipment must be called from within transaction")
	}
	res, err := rep.DB().ExecContext(ctx, `INSERT INTO shipment (carrier_id) 
		VALUES ((SELECT id FROM shipment_carriers WHERE carrier = ?))`, carrier)
	if err != nil {
		return 0, err
	}

	shipmentID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return shipmentID, nil
}

func (ms *MYSQLStore) ApplyPromoCode(ctx context.Context, orderId int32, promoCode string) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		currency, err := rep.Order().GetOrderCurrency(ctx, orderId)
		if err != nil {

			return err
		}
		promo, err := rep.Promo().GetPromoByCode(ctx, promoCode)
		if err != nil {
			// remove promo code from order if new promo code is invalid
			_, err = rep.Order().UpdateOrderTotalByCurrency(ctx, orderId, currency, nil)
			if err != nil {
				return err
			}
			return fmt.Errorf("failed to get promo code: %w", err)
		}

		if !promo.Allowed || promo.Expiration.Before(time.Now()) {
			// remove promo code from order if new promo code is invalid
			_, err = rep.Order().UpdateOrderTotalByCurrency(ctx, orderId, currency, nil)
			if err != nil {
				return err
			}
			return fmt.Errorf("promo code is not allowed or expired")
		}
		res, err := rep.DB().ExecContext(ctx, `
		UPDATE orders 
		SET promo_id = ? 
		WHERE id = ?`, promo.ID, orderId)
		if err != nil {
			return err
		}
		rowsAffected, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return fmt.Errorf("no order found with id %d", orderId)
		}

		_, err = rep.Order().UpdateOrderTotalByCurrency(ctx, orderId, currency, promo)
		if err != nil {
			return err
		}

		return nil
	})
}

func (ms *MYSQLStore) GetOrderCurrency(ctx context.Context, orderId int32) (dto.PaymentCurrency, error) {
	var currency string
	err := ms.DB().QueryRowContext(ctx,
		`SELECT pc.currency 
			FROM orders o 
			JOIN payment p 
			ON o.payment_id = p.id 
			JOIN payment_currency pc 
			ON p.currency_id = pc.id 
			WHERE o.id = ?`, orderId).Scan(&currency)
	if err != nil {
		return "", err
	}
	return dto.PaymentCurrency(currency), nil
}

func (ms *MYSQLStore) UpdateOrderTotalByCurrency(ctx context.Context,
	orderId int32, pc dto.PaymentCurrency, promo *dto.PromoCode) (decimal.Decimal, error) {
	if !ms.InTx() {
		return decimal.Zero, fmt.Errorf("UpdateOrderTotalByCurrency must be called from within transaction")
	}
	if promo == nil {
		promo = &dto.PromoCode{
			Sale: decimal.Zero,
		}
	}

	var itemsPrice decimal.Decimal
	err := ms.DB().QueryRowContext(ctx, fmt.Sprintf(`
	SELECT SUM(pp.%s * (100 - pp.sale) / 100 * oi.quantity)
	FROM product_prices pp
	JOIN order_item oi ON oi.product_id = pp.product_id
	WHERE oi.order_id = ? AND pp.product_id IN (
		SELECT product_id
		FROM order_item
		WHERE order_id = ?
	)`, pc), orderId, orderId).Scan(&itemsPrice)
	if err != nil {
		return decimal.Zero, err
	}

	if !promo.Sale.IsZero() {
		itemsPrice = itemsPrice.Mul(decimal.NewFromFloat(1).Sub(promo.Sale.Div(decimal.NewFromFloat(100))))
	}

	var shippingPrice decimal.Decimal
	if !promo.FreeShipping {
		err = ms.DB().QueryRowContext(ctx, `
		SELECT 
		CASE
			WHEN pc.currency = 'USD' THEN sc.USD
			WHEN pc.currency = 'EUR' THEN sc.EUR
			WHEN pc.currency = 'USDC' THEN sc.USDC
			WHEN pc.currency = 'ETH' THEN sc.ETH
		END AS shipment_price
		FROM 
			orders o
		JOIN 
			payment p ON o.payment_id = p.id
		JOIN 
			payment_currency pc ON p.currency_id = pc.id
		JOIN 
			shipment s ON o.shipment_id = s.id
		JOIN 
			shipment_carriers sc ON s.carrier_id = sc.id
		WHERE 
			o.id = ?;`, orderId).Scan(&shippingPrice)

		if err != nil {
			return decimal.Zero, err
		}
	}

	total := itemsPrice.Add(shippingPrice)

	_, err = ms.DB().ExecContext(ctx, `
			UPDATE orders
			SET total_price = ?
			WHERE id = ?`,
		total, orderId)
	if err != nil {
		return decimal.Zero, err
	}

	return total, nil

}

func addOrder(ctx context.Context, rep dependency.Repository, order *dto.Order, bid, pid, sid int64) error {
	if !rep.InTx() {
		return fmt.Errorf("addOrder must be called from within transaction")
	}

	// Insert the new order into the `order` table
	res, err := rep.DB().ExecContext(ctx, `
	INSERT INTO orders (buyer_id, placed, payment_id, shipment_id, total_price, status_id) 
	VALUES (?, ?, ?, ?, ?, 
		(SELECT id FROM order_status WHERE status = ?))`, bid, order.Placed, pid, sid, nil, string(dto.OrderPlaced))
	if err != nil {
		return err
	}
	orderId, err := res.LastInsertId()
	if err != nil {
		return err
	}
	order.ID = int32(orderId)

	// Prepare a batch insert for order_item
	valueStrings := make([]string, 0, len(order.Items))
	valueArgs := make([]interface{}, 0, len(order.Items)*4)
	for _, item := range order.Items {
		valueStrings = append(valueStrings, "(?, ?, ?, ?)")
		valueArgs = append(valueArgs, orderId, item.ID, item.Quantity, item.Size)
	}

	stmt := fmt.Sprintf("INSERT INTO order_item (order_id, product_id, quantity, size) VALUES %s",
		strings.Join(valueStrings, ","))
	_, err = rep.DB().ExecContext(ctx, stmt, valueArgs...)
	if err != nil {
		return err
	}

	// Update total_price in the order table by calculating it from the product_prices table
	// and shipping price from the shipment_carriers table.
	total, err := rep.Order().UpdateOrderTotalByCurrency(ctx, order.ID, order.Payment.Currency, nil)
	if err != nil {
		return err
	}
	order.TotalPrice = total

	return nil
}

// UpdateOrderItems update order items
func (ms *MYSQLStore) UpdateOrderItems(ctx context.Context, orderId int32, items []dto.Item) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, err := rep.DB().ExecContext(ctx, `DELETE FROM order_item WHERE order_id = ?`, orderId)
		if err != nil {
			return err
		}

		// Prepare a batch insert for order_item
		valueStrings := make([]string, 0, len(items))
		valueArgs := make([]interface{}, 0, len(items)*4)
		for _, item := range items {
			valueStrings = append(valueStrings, "(?, ?, ?, ?)")
			valueArgs = append(valueArgs, orderId, item.ID, item.Quantity, item.Size)
		}

		stmt := fmt.Sprintf("INSERT INTO order_item (order_id, product_id, quantity, size) VALUES %s",
			strings.Join(valueStrings, ","))
		_, err = rep.DB().ExecContext(ctx, stmt, valueArgs...)
		if err != nil {
			return err
		}

		cur, err := rep.Order().GetOrderCurrency(ctx, orderId)
		if err != nil {
			return err
		}

		_, err = rep.Order().UpdateOrderTotalByCurrency(ctx, orderId, cur, nil)
		if err != nil {
			return err
		}
		return nil
	})
}

// UpdateOrderStatus is used to update the status of an order.
func (ms *MYSQLStore) UpdateOrderStatus(ctx context.Context, orderId int32, status dto.OrderStatus) error {
	if !ms.InTx() {
		return fmt.Errorf("UpdateOrderStatus must be called from within transaction")
	}
	_, err := ms.DB().ExecContext(ctx, `UPDATE orders 
					SET status_id = (SELECT id FROM order_status WHERE status = ?) 
					WHERE id = ?`, status, orderId)
	if err != nil {
		return err
	}

	return nil
}

func (ms *MYSQLStore) updatePayment(ctx context.Context, orderId int32, payment *dto.Payment) error {
	if !ms.InTx() {
		return fmt.Errorf("updatePayment must be called from within transaction")
	}

	// Fetch order's total price
	orderTotal, err := ms.UpdateOrderTotalByCurrency(ctx, orderId, payment.Currency, nil)
	if err != nil {
		return err
	}

	// Check if payment.TransactionAmount is more or equal to order's total_price
	if !payment.TransactionAmount.GreaterThanOrEqual(orderTotal) {
		return fmt.Errorf("transaction amount is less than order total price")
	}

	// Update payment
	res, err := ms.DB().ExecContext(ctx, `
	UPDATE payment SET
		method_id = (SELECT id FROM payment_method WHERE method = ?), 
		currency_id = (SELECT id FROM payment_currency WHERE currency = ?),
		currency_transaction_id = ?,
		transaction_amount = ?,
		payer = ?,
		payee = ?,
		is_transaction_done = ?
	WHERE id = (SELECT payment_id FROM orders WHERE id = ?)`,
		payment.Method,
		payment.Currency,
		payment.TransactionID,
		payment.TransactionAmount,
		payment.Payer,
		payment.Payee,
		payment.IsTransactionDone,
		orderId)

	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return fmt.Errorf("no payment found with order id %d", orderId)
	}
	return nil
}

// OrderPaymentDone updates the payment status of an order and adds payment info to order.
func (ms *MYSQLStore) OrderPaymentDone(ctx context.Context, orderId int32, payment *dto.Payment) error {
	if !ms.InTx() {
		return fmt.Errorf("OrderPaymentDone must be called from within transaction")
	}
	// Change order status to 'Confirmed'.
	err := ms.UpdateOrderStatus(ctx, orderId, dto.OrderConfirmed)
	if err != nil {
		return err
	}
	// Add payment info to the order.
	err = ms.updatePayment(ctx, orderId, payment)
	if err != nil {
		return err
	}

	return nil
}

// UpdateShippingStatus updates the shipping status of an order.
func (ms *MYSQLStore) UpdateShippingInfo(ctx context.Context,
	orderId int32, carrier string,
	trackingCode string, shippingTime time.Time) error {
	// Execute the query.
	_, err := ms.DB().ExecContext(ctx, `
		UPDATE shipment 
		SET carrier = (SELECT id FROM shipment_carriers WHERE carrier = ?), 
		tracking_code = ?, 
		shipping_date = ?
		WHERE id = (SELECT shipment_id FROM orders WHERE id = ?)
	`, carrier, trackingCode, shippingTime, orderId)
	if err != nil {
		return err
	}
	return nil
}

// GetOrderItems
func (ms *MYSQLStore) GetOrderItems(ctx context.Context, orderId int32) ([]dto.Item, error) {
	rows, err := ms.DB().QueryContext(ctx, `
		SELECT product_id, quantity, size from order_item where order_id = 141;
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []dto.Item
	for rows.Next() {
		var item dto.Item
		err = rows.Scan(&item.ID, &item.Quantity, &item.Size)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// RefundOrder refunds an existing order TODO: can be refunded only if payed.
func (ms *MYSQLStore) RefundOrder(ctx context.Context, orderId int32) error {
	ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Change order status to 'Refunded'.
		err := rep.Order().UpdateOrderStatus(ctx, orderId, dto.OrderRefunded)
		if err != nil {
			return err
		}
		_, err = rep.DB().ExecContext(ctx, `
		UPDATE payment 
		SET is_transaction_done = false 
		WHERE id = (SELECT payment_id FROM orders WHERE id = ?)
		`, orderId)
		if err != nil {
			return err
		}
		return nil

	})
	return nil
}

// OrdersByEmail retrieves all orders for a given email address.
func (ms *MYSQLStore) OrdersByEmail(ctx context.Context, email string) ([]dto.Order, error) {
	ordersQuery := `
	SELECT 
		o.id, 
		o.placed, 
		o.total_price, 
		os.status, 
		b.first_name, b.last_name, b.email, b.phone, b.receive_promo_emails,
		pa.method, 
		pc.currency,
		p.id, p.currency_transaction_id, p.transaction_amount, p.payer, p.payee, p.is_transaction_done,
		sc.carrier, sc.USD,	sc.EUR, sc.USDC, sc.ETH,
		s.tracking_code, s.shipping_date, s.estimated_arrival_date
	FROM 
		orders o 
	INNER JOIN 
		order_status os ON o.status_id = os.id
	INNER JOIN 
		buyer b ON o.buyer_id = b.id
	INNER JOIN 
		payment p ON o.payment_id = p.id
	INNER JOIN 
		payment_method pa ON p.method_id = pa.id
	INNER JOIN 
		payment_currency pc ON p.currency_id = pc.id
	INNER JOIN 
		shipment s ON o.shipment_id = s.id
	INNER JOIN 
		shipment_carriers sc ON s.carrier_id = sc.id
	WHERE 
		b.email = ?
	AND 
		o.status_id != (SELECT id FROM order_status WHERE status = ?)
	`

	rows, err := ms.DB().QueryContext(ctx, ordersQuery, email, dto.OrderCancelled)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var orders []dto.Order

	for rows.Next() {
		var order dto.Order
		order.Buyer = &dto.Buyer{}
		order.Payment = &dto.Payment{}
		order.Shipment = &dto.Shipment{}
		var trackingCode sql.NullString
		var shippingDate sql.NullTime
		var estimatedArrivalDate sql.NullTime
		var USD, EUR, USDC, ETH decimal.Decimal

		err = rows.Scan(
			&order.ID,
			&order.Placed,
			&order.TotalPrice,
			&order.Status,
			&order.Buyer.FirstName,
			&order.Buyer.LastName,
			&order.Buyer.Email,
			&order.Buyer.Phone,
			&order.Buyer.ReceivePromoEmails,
			&order.Payment.Method,
			&order.Payment.Currency,
			&order.Payment.ID,
			&order.Payment.TransactionID,
			&order.Payment.TransactionAmount,
			&order.Payment.Payer,
			&order.Payment.Payee,
			&order.Payment.IsTransactionDone,
			&order.Shipment.Carrier,
			&USD,
			&EUR,
			&USDC,
			&ETH,
			&trackingCode,
			&shippingDate,
			&estimatedArrivalDate,
		)

		if err != nil {
			return nil, err
		}

		if trackingCode.Valid {
			order.Shipment.TrackingCode = trackingCode.String
		}
		if shippingDate.Valid {
			order.Shipment.ShippingDate = shippingDate.Time
		}
		if estimatedArrivalDate.Valid {
			order.Shipment.EstimatedArrivalDate = estimatedArrivalDate.Time
		}
		switch order.Payment.Currency {
		case dto.USD:
			order.Shipment.Cost = USD
		case dto.EUR:
			order.Shipment.Cost = EUR
		case dto.USDCrypto:
			order.Shipment.Cost = USDC
		case dto.ETH:
			order.Shipment.Cost = ETH
		}

		// Then fetch the items for this order
		itemsQuery := `
        SELECT 
            oi.product_id, oi.quantity, oi.size
        FROM 
            order_item oi 
        WHERE 
            oi.order_id = ?
        `

		itemRows, err := ms.DB().QueryContext(ctx, itemsQuery, order.ID)
		if err != nil {
			return nil, err
		}

		for itemRows.Next() {
			var item dto.Item
			err = itemRows.Scan(&item.ID, &item.Quantity, &item.Size)
			if err != nil {
				return nil, err
			}
			order.Items = append(order.Items, item)
		}

		if err = itemRows.Err(); err != nil {
			return nil, err
		}

		orders = append(orders, order)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return orders, nil
}

// GetOrder retrieves an existing order by its ID.
func (ms *MYSQLStore) GetOrder(ctx context.Context, orderId int32) (*dto.Order, error) {
	orderQuery := `
	SELECT 
		o.id, 
		o.placed, 
		o.total_price, 
		os.status, 
		b.first_name, b.last_name, b.email, b.phone, b.receive_promo_emails,
		pa.method, 
		pc.currency,
		p.id, p.currency_transaction_id, p.transaction_amount, p.payer, p.payee, p.is_transaction_done,
		sc.carrier, sc.USD,	sc.EUR, sc.USDC, sc.ETH,
		s.tracking_code, s.shipping_date, s.estimated_arrival_date
	FROM 
		orders o 
	INNER JOIN 
		order_status os ON o.status_id = os.id
	INNER JOIN 
		buyer b ON o.buyer_id = b.id
	INNER JOIN 
		payment p ON o.payment_id = p.id
	INNER JOIN 
		payment_method pa ON p.method_id = pa.id
	INNER JOIN 
		payment_currency pc ON p.currency_id = pc.id
	INNER JOIN 
		shipment s ON o.shipment_id = s.id
	INNER JOIN 
		shipment_carriers sc ON s.carrier_id = sc.id
	WHERE 
		o.id = ?
	`

	row := ms.DB().QueryRowContext(ctx, orderQuery, orderId)

	var order dto.Order
	order.Buyer = &dto.Buyer{}
	order.Payment = &dto.Payment{}
	order.Shipment = &dto.Shipment{}
	var trackingCode sql.NullString
	var shippingDate sql.NullTime
	var estimatedArrivalDate sql.NullTime
	var USD, EUR, USDC, ETH decimal.Decimal

	err := row.Scan(
		&order.ID,
		&order.Placed,
		&order.TotalPrice,
		&order.Status,
		&order.Buyer.FirstName,
		&order.Buyer.LastName,
		&order.Buyer.Email,
		&order.Buyer.Phone,
		&order.Buyer.ReceivePromoEmails,
		&order.Payment.Method,
		&order.Payment.Currency,
		&order.Payment.ID,
		&order.Payment.TransactionID,
		&order.Payment.TransactionAmount,
		&order.Payment.Payer,
		&order.Payment.Payee,
		&order.Payment.IsTransactionDone,
		&order.Shipment.Carrier,
		&USD,
		&EUR,
		&USDC,
		&ETH,
		&trackingCode,
		&shippingDate,
		&estimatedArrivalDate,
	)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil // Return nil, nil if no row found
		}
		return nil, err
	}

	if trackingCode.Valid {
		order.Shipment.TrackingCode = trackingCode.String
	}
	if shippingDate.Valid {
		order.Shipment.ShippingDate = shippingDate.Time
	}
	if estimatedArrivalDate.Valid {
		order.Shipment.EstimatedArrivalDate = estimatedArrivalDate.Time
	}

	switch order.Payment.Currency {
	case dto.USD:
		order.Shipment.Cost = USD
	case dto.EUR:
		order.Shipment.Cost = EUR
	case dto.USDCrypto:
		order.Shipment.Cost = USDC
	case dto.ETH:
		order.Shipment.Cost = ETH
	}

	// Then fetch the items for this order
	itemsQuery := `
	SELECT 
		oi.product_id, oi.quantity, oi.size
	FROM 
		order_item oi 
	WHERE 
		oi.order_id = ?
	`

	itemRows, err := ms.DB().QueryContext(ctx, itemsQuery, order.ID)
	if err != nil {
		return nil, err
	}
	defer itemRows.Close()

	for itemRows.Next() {
		var item dto.Item
		err = itemRows.Scan(&item.ID, &item.Quantity, &item.Size)
		if err != nil {
			return nil, err
		}
		order.Items = append(order.Items, item)
	}

	if err = itemRows.Err(); err != nil {
		return nil, err
	}

	return &order, nil
}

// TODO: implement
func (ms *MYSQLStore) GetOrderByStatus(ctx context.Context, status dto.OrderStatus) ([]dto.Order, error) {
	return nil, nil
}
