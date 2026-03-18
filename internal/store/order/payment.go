package order

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// InsertFiatInvoice handles fiat-specific invoice insertion.
func (s *Store) InsertFiatInvoice(ctx context.Context, orderUUID string, clientSecret string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error) {
	return s.insertOrderInvoice(ctx, orderUUID, clientSecret, pm, expiredAt)
}

func (s *Store) insertOrderInvoice(ctx context.Context, orderUUID string, addrOrSecret string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error) {
	if err := validatePaymentMethodAllowed(&pm); err != nil {
		return nil, err
	}

	var itemsChanged bool
	var orderFull *entity.OrderFull

	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txDB := rep.DB()
		txStore := &Store{Base: storeutil.Base{DB: txDB, Now: s.Now}, txFunc: s.txFunc, repFunc: func() dependency.Repository { return rep }}

		order, err := getOrderByUUIDForUpdate(ctx, txDB, orderUUID)
		if err != nil {
			return fmt.Errorf("cannot get order by UUID %s: %w", orderUUID, err)
		}

		_, err = validateOrderStatus(order, entity.Placed)
		if err != nil {
			return err
		}

		orderItems, err := getOrderItemsInsert(ctx, txDB, order.Id)
		if err != nil {
			return fmt.Errorf("cannot get order items: %w", err)
		}

		ofs, err := fetchOrderInfo(ctx, rep, []entity.Order{*order})
		if err != nil {
			return fmt.Errorf("cannot fetch order info: %w", err)
		}
		if len(ofs) == 0 {
			return fmt.Errorf("order is not found")
		}
		orderFull = &ofs[0]

		if orderFull.Order.TotalPrice.IsZero() {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order total is zero, cancelling",
				slog.String("order_uuid", orderUUID),
				slog.Int("order_id", orderFull.Order.Id),
				slog.String("currency", orderFull.Order.Currency),
			)
			if err := cancelOrder(ctx, rep, &orderFull.Order, orderItems, entity.StockChangeSourceOrderCancelled, ""); err != nil {
				return fmt.Errorf("cannot cancel order: %w", err)
			}
			return fmt.Errorf("total price is zero")
		}
		if err := dto.ValidatePriceMeetsMinimum(orderFull.Order.TotalPrice, orderFull.Order.Currency); err != nil {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order total below currency minimum",
				slog.String("order_uuid", orderUUID),
				slog.String("total", orderFull.Order.TotalPrice.String()),
				slog.String("currency", orderFull.Order.Currency),
				slog.String("err", err.Error()),
			)
			return fmt.Errorf("order total below currency minimum: %w", err)
		}

		itemsChanged, err = validateAndUpdateOrderIfNeededForUpdate(ctx, rep, txStore, orderFull, true)
		if err != nil {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice: order validation failed, cancelling",
				slog.String("order_uuid", orderUUID),
				slog.String("err", err.Error()),
			)
			return err
		}

		if itemsChanged {
			return nil
		}

		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(orderFull.OrderItems)
		history := &entity.StockHistoryParams{
			Source:        entity.StockChangeSourceOrderPaid,
			OrderId:       orderFull.Order.Id,
			OrderUUID:     orderFull.Order.UUID,
			OrderCurrency: orderFull.Order.Currency,
			OrderComment:  orderFull.Order.OrderComment.String,
			PromoDiscount: orderFull.PromoCode.Discount,
		}
		if err := rep.Products().ReduceStockForProductSizes(ctx, validItemsInsert, history); err != nil {
			return fmt.Errorf("error reducing stock for product sizes: %w", err)
		}

		return s.processPayment(ctx, txDB, orderFull, addrOrSecret, pm, expiredAt)
	})
	if err != nil {
		return nil, err
	}
	if itemsChanged {
		return nil, ErrOrderItemsUpdated
	}

	orderFull, err = s.GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("cannot refresh order details: %w", err)
	}

	return orderFull, nil
}

func (s *Store) processPayment(ctx context.Context, db dependency.DB, orderFull *entity.OrderFull, addrOrSecret string, pm entity.PaymentMethod, expiredAt time.Time) error {
	orderFull.Payment.PaymentMethodID = pm.Id
	orderFull.Payment.IsTransactionDone = false
	orderFull.Payment.TransactionAmount = orderFull.Order.TotalPriceDecimal()
	orderFull.Payment.TransactionAmountPaymentCurrency = orderFull.Order.TotalPriceDecimal()
	orderFull.Payment.PaymentInsert.ExpiredAt = sql.NullTime{Time: expiredAt, Valid: true}

	switch pm.Name {
	case entity.CARD, entity.CARD_TEST:
		orderFull.Payment.ClientSecret = sql.NullString{String: addrOrSecret, Valid: true}
	default:
		return fmt.Errorf("unsupported payment method: %s", pm.Name)
	}

	if err := updateOrderPayment(ctx, db, orderFull.Order.Id, orderFull.Payment.PaymentInsert); err != nil {
		return fmt.Errorf("cannot update order payment: %w", err)
	}

	if err := updateOrderStatus(ctx, db, orderFull.Order.Id, cache.OrderStatusAwaitingPayment.Status.Id); err != nil {
		return fmt.Errorf("cannot update order status: %w", err)
	}

	return nil
}

// AssociatePaymentIntentWithOrder stores the PaymentIntent ID in the payment record.
func (s *Store) AssociatePaymentIntentWithOrder(ctx context.Context, orderUUID string, paymentIntentId string) error {
	query := `
	UPDATE payment 
	SET client_secret = :paymentIntentId 
	WHERE order_id = (
		SELECT id FROM customer_order 
		WHERE uuid = :orderUUID
	)`

	rows, err := s.DB.NamedExecContext(ctx, query, map[string]any{
		"paymentIntentId": paymentIntentId,
		"orderUUID":       orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't associate payment intent with order: %w", err)
	}

	if rowsAffected, err := rows.RowsAffected(); err != nil || rowsAffected == 0 {
		return fmt.Errorf("can't associate payment intent with order: %w", errPaymentRecordNotFound)
	}
	return nil
}

// UpdateTotalPaymentCurrency updates the transaction_amount_payment_currency for an order.
func (s *Store) UpdateTotalPaymentCurrency(ctx context.Context, orderUUID string, tapc decimal.Decimal) error {
	query := `        
	UPDATE payment 
	SET transaction_amount_payment_currency = :tapc 
	WHERE order_id = (
		SELECT id FROM customer_order 
		WHERE uuid = :orderUUID
	)`

	err := storeutil.ExecNamed(ctx, s.DB, query, map[string]any{
		"tapc":      tapc,
		"orderUUID": orderUUID,
	})
	if err != nil {
		return fmt.Errorf("can't update total payment currency: %w", err)
	}
	return nil
}

// GetPaymentByOrderUUID retrieves a payment by the order UUID.
func (s *Store) GetPaymentByOrderUUID(ctx context.Context, orderUUID string) (*entity.Payment, error) {
	query := `
    SELECT p.*
    FROM payment p
    JOIN customer_order co ON p.order_id = co.id
    WHERE co.uuid = :orderUUID;`

	payment, err := storeutil.QueryNamedOne[entity.Payment](ctx, s.DB, query, map[string]interface{}{
		"orderUUID": orderUUID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get payment by order UUID: %w", err)
	}

	return &payment, nil
}

// ExpireOrderPayment expires an order's payment and restores stock.
func (s *Store) ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error) {
	var payment *entity.Payment

	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txDB := rep.DB()

		order, err := getOrderByUUIDForUpdate(ctx, txDB, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		orderStatus, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		if orderStatus.Status.Name != entity.AwaitingPayment {
			slog.DebugContext(ctx, "order status is not awaiting payment, no expiration needed",
				slog.String("order_status", orderStatus.PB.String()),
			)
			return nil
		}

		payment, err = s.GetPaymentByOrderUUID(ctx, order.UUID)
		if err != nil {
			return fmt.Errorf("can't get payment by order id: %w", err)
		}

		orderItems, err := getOrderItemsInsert(ctx, txDB, order.Id)
		if err != nil {
			return fmt.Errorf("can't get order items: %w", err)
		}

		paymentUpdate := entity.PaymentInsert{
			PaymentMethodID:                  payment.PaymentMethodID,
			TransactionID:                    sql.NullString{Valid: false},
			TransactionAmount:                decimal.Zero,
			TransactionAmountPaymentCurrency: decimal.Zero,
			IsTransactionDone:                false,
		}

		if payment.IsTransactionDone {
			return nil
		}

		if err := updateOrderPayment(ctx, txDB, order.Id, paymentUpdate); err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		if err := rep.Products().RestoreStockSilently(ctx, orderItems); err != nil {
			return fmt.Errorf("can't restore stock: %w", err)
		}

		statusCancelled, ok := cache.GetOrderStatusByName(entity.Cancelled)
		if !ok {
			return fmt.Errorf("can't get order status by name %s", entity.Cancelled)
		}

		if err := updateOrderStatus(ctx, txDB, order.Id, statusCancelled.Status.Id); err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		if order.PromoId.Int32 != 0 {
			if err := removePromo(ctx, txDB, order.Id); err != nil {
				return fmt.Errorf("can't remove promo: %w", err)
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return payment, nil
}

// OrderPaymentDone marks an order payment as done and transitions to Confirmed.
func (s *Store) OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (bool, error) {
	wasUpdated := false

	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txDB := rep.DB()

		order, err := getOrderByUUIDForUpdate(ctx, txDB, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		os, err := getOrderStatus(order.OrderStatusId)
		if err != nil {
			return err
		}

		if os.Status.Name != entity.AwaitingPayment {
			return nil
		}

		if order.PromoId.Int32 != 0 {
			err := rep.Promo().DisableVoucher(ctx, order.PromoId)
			if err != nil {
				return fmt.Errorf("can't disable voucher: %w", err)
			}
		}

		err = updateOrderStatus(ctx, txDB, order.Id, cache.OrderStatusConfirmed.Status.Id)
		if err != nil {
			return fmt.Errorf("can't update order status: %w", err)
		}

		p.PaymentInsert.IsTransactionDone = true

		err = updateOrderPayment(ctx, txDB, order.Id, p.PaymentInsert)
		if err != nil {
			return fmt.Errorf("can't update order payment: %w", err)
		}

		wasUpdated = true
		return nil
	})
	if err != nil {
		return false, err
	}

	return wasUpdated, nil
}

// GetAwaitingPaymentsByPaymentType retrieves all orders with "awaiting payment" status.
func (s *Store) GetAwaitingPaymentsByPaymentType(ctx context.Context, pmn ...entity.PaymentMethodName) ([]entity.PaymentOrderUUID, error) {
	pmIds := []int{}
	for _, pm := range pmn {
		method, ok := cache.GetPaymentMethodByName(pm)
		if ok {
			pmIds = append(pmIds, method.Method.Id)
		}
	}

	orders, err := getOrdersByStatusAndPayment(ctx, s.DB, cache.OrderStatusAwaitingPayment.Status.Id, pmIds...)
	if err != nil {
		return nil, err
	}

	oids := []int{}
	for _, o := range orders {
		oids = append(oids, o.Id)
	}

	mpo, err := paymentsByOrderIds(ctx, s.DB, oids)
	if err != nil {
		return nil, fmt.Errorf("can't get payments by order ids: %w", err)
	}

	poids := []entity.PaymentOrderUUID{}
	for oUUID, payment := range mpo {
		poids = append(poids, entity.PaymentOrderUUID{
			OrderUUID: oUUID,
			Payment:   payment,
		})
	}

	return poids, nil
}
