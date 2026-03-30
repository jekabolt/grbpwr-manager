package order

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// CreateOrder creates a new order with the provided details.
func (s *Store) CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool, expiredAt time.Time) (*entity.Order, bool, error) {
	if err := validateOrderInput(orderNew); err != nil {
		return nil, false, err
	}

	paymentMethod, err := validatePaymentMethod(orderNew.PaymentMethod)
	if err != nil {
		return nil, false, err
	}

	shippingCountry := ""
	if orderNew.ShippingAddress != nil {
		shippingCountry = orderNew.ShippingAddress.Country
	}
	shipmentCarrier, err := validateShipmentCarrier(orderNew.ShipmentCarrierId, shippingCountry)
	if err != nil {
		return nil, false, err
	}

	order := &entity.Order{}
	sendEmail := false

	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txDB := rep.DB()
		txStore := &Store{Base: storeutil.Base{DB: txDB, Now: s.Now}, txFunc: s.txFunc, repFunc: func() dependency.Repository { return rep }}

		orderNew.Items = mergeOrderItems(orderNew.Items)

		promo, ok := cache.GetPromoByCode(orderNew.PromoCode)
		if !ok || !promo.IsAllowed() {
			promo = entity.PromoCode{}
		}
		prId := sql.NullInt32{
			Int32: int32(promo.Id),
			Valid: promo.Id > 0,
		}

		validItems, _, err := txStore.validateOrderItemsInsert(ctx, orderNew.Items, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error while validating order items: %w", err)
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)

		providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
		subtotal, err := calculateTotalAmount(providers, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error while calculating total amount: %w", err)
		}

		shipmentPrice, err := shipmentCarrier.PriceDecimal(orderNew.Currency)
		if err != nil {
			return fmt.Errorf("can't get shipment carrier price for currency %s: %w", orderNew.Currency, err)
		}

		freeShipping := false
		complimentaryPrices := cache.GetComplimentaryShippingPrices()
		if threshold, ok := complimentaryPrices[strings.ToUpper(orderNew.Currency)]; ok && threshold.GreaterThan(decimal.Zero) {
			if subtotal.GreaterThanOrEqual(threshold) {
				shipmentPrice = decimal.Zero
				freeShipping = true
			}
		}
		if promo.FreeShipping {
			shipmentPrice = decimal.Zero
			freeShipping = true
		}

		totalPrice := promo.SubtotalWithPromo(subtotal, shipmentPrice, dto.DecimalPlacesForCurrency(orderNew.Currency))

		order = &entity.Order{
			TotalPrice:    totalPrice,
			Currency:      orderNew.Currency,
			PromoId:       prId,
			OrderStatusId: cache.OrderStatusPlaced.Status.Id,
			GAClientID:    sql.NullString{String: orderNew.GAClientID, Valid: orderNew.GAClientID != ""},
		}

		err = txStore.insertOrderDetails(ctx, txDB, order, validItemsInsert, shipmentCarrier, shipmentPrice, freeShipping, orderNew)
		if err != nil {
			return fmt.Errorf("error while inserting order details: %w", err)
		}

		if receivePromo {
			if err := s.handlePromoSubscription(ctx, rep, orderNew.Buyer.Email, &sendEmail); err != nil {
				return fmt.Errorf("error while handling promotional subscription: %w", err)
			}
		}

		err = insertPaymentRecord(ctx, txDB, paymentMethod.Method.Id, order.Id, expiredAt)
		if err != nil {
			return fmt.Errorf("error while inserting payment record: %w", err)
		}

		return nil
	})

	return order, sendEmail, err
}

// CreateCustomOrder creates an order with bank_invoice or cash payment, custom item prices, and confirmed status.
func (s *Store) CreateCustomOrder(ctx context.Context, orderNew *entity.OrderNew) (*entity.Order, error) {
	if err := validateOrderInput(orderNew); err != nil {
		return nil, err
	}
	if orderNew.PaymentMethod != entity.BANK_INVOICE && orderNew.PaymentMethod != entity.CASH {
		return nil, &entity.ValidationError{Message: "payment method must be bank_invoice or cash for custom orders"}
	}
	paymentMethod, err := validatePaymentMethod(orderNew.PaymentMethod)
	if err != nil {
		return nil, err
	}
	shippingCountry := ""
	if orderNew.ShippingAddress != nil {
		shippingCountry = orderNew.ShippingAddress.Country
	}
	shipmentCarrier, err := validateShipmentCarrier(orderNew.ShipmentCarrierId, shippingCountry)
	if err != nil {
		return nil, err
	}

	var order *entity.Order
	err = s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		txDB := rep.DB()

		orderNew.Items = mergeOrderItems(orderNew.Items)
		validItems, adjustments, err := validateOrderItemsStockForCustomOrder(ctx, rep, orderNew.Items)
		if err != nil {
			return err
		}
		if len(adjustments) > 0 {
			var invalidItems []string
			for _, adj := range adjustments {
				invalidItems = append(invalidItems, fmt.Sprintf("product_id=%d size_id=%d (reason: %s)", adj.ProductId, adj.SizeId, adj.Reason))
			}
			return &entity.ValidationError{Message: fmt.Sprintf("cannot create custom order: some items are invalid or out of stock: %s", strings.Join(invalidItems, "; "))}
		}
		if len(validItems) == 0 {
			return &entity.ValidationError{Message: "no valid order items: products or sizes not found, or out of stock"}
		}
		validItemsInsert := entity.ConvertOrderItemToOrderItemInsert(validItems)
		providers := entity.ConvertOrderItemInsertsToProductInfoProviders(validItemsInsert)
		subtotal, err := calculateTotalAmount(providers, orderNew.Currency)
		if err != nil {
			return fmt.Errorf("error calculating total: %w", err)
		}
		var shipmentPrice decimal.Decimal
		if orderNew.CustomShipmentCost != nil {
			shipmentPrice = orderNew.CustomShipmentCost.Round(2)
		} else {
			shipmentPrice, err = shipmentCarrier.PriceDecimal(orderNew.Currency)
			if err != nil {
				return fmt.Errorf("can't get shipment carrier price: %w", err)
			}
		}
		totalPrice := dto.RoundForCurrency(subtotal.Add(shipmentPrice), orderNew.Currency)
		order = &entity.Order{
			TotalPrice:    totalPrice,
			Currency:      orderNew.Currency,
			OrderStatusId: cache.OrderStatusConfirmed.Status.Id,
		}
		txStore := &Store{Base: storeutil.Base{DB: txDB, Now: s.Now}, txFunc: s.txFunc, repFunc: func() dependency.Repository { return rep }}
		if err = txStore.insertOrderDetails(ctx, txDB, order, validItemsInsert, shipmentCarrier, shipmentPrice, false, orderNew); err != nil {
			return err
		}
		if err = insertPaymentRecordForCustomOrder(ctx, txDB, paymentMethod.Method.Id, order.Id, totalPrice); err != nil {
			return err
		}
		history := &entity.StockHistoryParams{
			Source:        entity.StockChangeSourceOrderCustom,
			OrderId:       order.Id,
			OrderUUID:     order.UUID,
			OrderCurrency: orderNew.Currency,
		}
		if err = rep.Products().ReduceStockForProductSizes(ctx, validItemsInsert, history); err != nil {
			return fmt.Errorf("error reducing stock: %w", err)
		}
		return insertOrderStatusHistoryEntry(ctx, txDB, order.Id, cache.OrderStatusConfirmed.Status.Id, "admin", "custom order")
	})
	if err != nil {
		return nil, err
	}
	fullOrder, err := s.GetOrderById(ctx, order.Id)
	if err != nil {
		return nil, fmt.Errorf("order created but failed to fetch: %w", err)
	}
	return &fullOrder.Order, nil
}

func (s *Store) handlePromoSubscription(ctx context.Context, rep dependency.Repository, email string, sendEmail *bool) error {
	*sendEmail = true

	isSubscribed, err := rep.Subscribers().UpsertSubscription(ctx, email, true)
	if err != nil {
		return fmt.Errorf("error while upserting subscription: %w", err)
	}

	if !isSubscribed {
		*sendEmail = false
	}

	return nil
}
