package order

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

func updateOrderPayment(ctx context.Context, db dependency.DB, orderId int, payment entity.PaymentInsert) error {
	query := `
	UPDATE payment 
	SET transaction_amount = :transactionAmount,
		transaction_amount_payment_currency = :transactionAmountPaymentCurrency,
		transaction_id = :transactionId,
		is_transaction_done = :isTransactionDone,
		payment_method_id = :paymentMethodId,
		client_secret = :clientSecret,
		expired_at = :expiredAt,
		payment_method_type = :paymentMethodType
	WHERE order_id = :orderId`

	params := map[string]any{
		"transactionAmount":                payment.TransactionAmount,
		"transactionAmountPaymentCurrency": payment.TransactionAmountPaymentCurrency,
		"transactionId":                    payment.TransactionID,
		"isTransactionDone":                payment.IsTransactionDone,
		"paymentMethodId":                  payment.PaymentMethodID,
		"clientSecret":                     payment.ClientSecret,
		"orderId":                          orderId,
		"expiredAt":                        payment.ExpiredAt,
		"paymentMethodType":                payment.PaymentMethodType,
	}

	slog.Default().InfoContext(ctx, "update order payment", "params", params, "query", query)

	_, err := db.NamedExecContext(ctx, query, params)
	if err != nil {
		return fmt.Errorf("failed to update payment: %w", err)
	}

	return nil
}

func updateOrderItems(ctx context.Context, db dependency.DB, validItems []entity.OrderItemInsert, orderId int) error {
	err := deleteOrderItems(ctx, db, orderId)
	if err != nil {
		return fmt.Errorf("error while deleting order items: %w", err)
	}
	err = insertOrderItems(ctx, db, validItems, orderId)
	if err != nil {
		return fmt.Errorf("error while inserting order items: %w", err)
	}
	return nil
}

func updateShipmentCostAndFreeShipping(ctx context.Context, db dependency.DB, shipmentId int, cost decimal.Decimal, freeShipping bool) error {
	query := `UPDATE shipment SET cost = :cost, free_shipping = :freeShipping WHERE id = :id`
	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"id":           shipmentId,
		"cost":         cost,
		"freeShipping": freeShipping,
	})
}

func updateOrderTotalPromo(ctx context.Context, db dependency.DB, orderId int, promoId int, totalPrice decimal.Decimal) error {
	query := `
	UPDATE customer_order
	SET promo_id = :promoId,
		total_price = :totalPrice
	WHERE id = :orderId`

	promoIdNull := sql.NullInt32{
		Int32: int32(promoId),
		Valid: promoId != 0,
	}

	return storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId":    orderId,
		"promoId":   promoIdNull,
		"totalPrice": totalPrice,
	})
}

func updateTotalAmount(ctx context.Context, db dependency.DB, orderId int, subtotal decimal.Decimal, promo entity.PromoCode, shipment entity.Shipment, currency string) (decimal.Decimal, error) {
	if !promo.IsAllowed() {
		promo = entity.PromoCode{}
	}

	shipmentCost := shipment.CostDecimal(currency)
	freeShipping := false
	carrier, carrierOk := cache.GetShipmentCarrierById(shipment.CarrierId)
	if carrierOk {
		carrierPrice, err := carrier.PriceDecimal(currency)
		if err == nil {
			shipmentCost = carrierPrice
			complimentaryPrices := cache.GetComplimentaryShippingPrices()
			if threshold, ok := complimentaryPrices[strings.ToUpper(currency)]; ok && threshold.GreaterThan(decimal.Zero) {
				if subtotal.GreaterThanOrEqual(threshold) {
					shipmentCost = decimal.Zero
					freeShipping = true
				}
			}
		}
	}
	if promo.FreeShipping {
		shipmentCost = decimal.Zero
		freeShipping = true
	}

	if err := updateShipmentCostAndFreeShipping(ctx, db, shipment.Id, shipmentCost, freeShipping); err != nil {
		return decimal.Zero, fmt.Errorf("can't update shipment cost: %w", err)
	}

	subtotal = promo.SubtotalWithPromo(subtotal, shipmentCost, dto.DecimalPlacesForCurrency(currency))

	err := updateOrderTotalPromo(ctx, db, orderId, promo.Id, subtotal)
	if err != nil {
		return decimal.Zero, fmt.Errorf("can't update order total promo: %w", err)
	}

	return subtotal, nil
}

func updateOrderShipment(ctx context.Context, db dependency.DB, shipment *entity.Shipment) error {
	query := `
    UPDATE shipment
    SET 
        tracking_code = :trackingCode,
        carrier_id = :carrierId,
        shipping_date = :shippingDate,
        estimated_arrival_date = :estimatedArrivalDate
    WHERE order_id = :orderId`

	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId":              shipment.OrderId,
		"carrierId":            shipment.CarrierId,
		"trackingCode":         shipment.TrackingCode,
		"shippingDate":         shipment.ShippingDate,
		"estimatedArrivalDate": shipment.EstimatedArrivalDate,
	})
	if err != nil {
		return fmt.Errorf("can't update order shipment: %w", err)
	}
	return nil
}

func removePromo(ctx context.Context, db dependency.DB, orderId int) error {
	query := `UPDATE customer_order SET promo_id = NULL WHERE id = :orderId`
	err := storeutil.ExecNamed(ctx, db, query, map[string]any{
		"orderId": orderId,
	})
	if err != nil {
		return fmt.Errorf("can't remove promo: %w", err)
	}
	return nil
}
