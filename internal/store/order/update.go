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

// redactNullString returns a non-sensitive placeholder for a secret value so it
// can be logged without leaking the secret itself. It reveals only whether the
// value is present/valid and its length, never the contents.
func redactNullString(v sql.NullString) string {
	if !v.Valid {
		return "<null>"
	}
	if v.String == "" {
		return "<empty>"
	}
	return fmt.Sprintf("[REDACTED len=%d]", len(v.String))
}

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

	// Build a redacted view of params for logging: client_secret and
	// transaction_id authorize confirming/cancelling a PaymentIntent and must
	// not be written to application logs. Keep non-sensitive context (order id,
	// amounts, payment method, status) to remain useful for debugging.
	logParams := make(map[string]any, len(params))
	for k, v := range params {
		switch k {
		case "clientSecret":
			logParams[k] = redactNullString(payment.ClientSecret)
		case "transactionId":
			logParams[k] = redactNullString(payment.TransactionID)
		default:
			logParams[k] = v
		}
	}

	slog.Default().InfoContext(ctx, "update order payment", "params", logParams)

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
		"promoId":    promoIdNull,
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

// disableVoucherInTx disables a single-use voucher in the DB on the caller's
// transaction connection and returns the promo code that was disabled (empty if
// the promo is not a voucher or is unknown). It intentionally does NOT mutate the
// in-memory promo cache: the caller must apply cache.DisablePromo only after the
// transaction commits, otherwise a rollback or serialization retry would leave
// the cache marking the voucher disabled while the DB still allows it.
func disableVoucherInTx(ctx context.Context, db dependency.DB, promoID sql.NullInt32) (string, error) {
	if !promoID.Valid || promoID.Int32 == 0 {
		return "", nil
	}
	promo, ok := cache.GetPromoById(int(promoID.Int32))
	if !ok || !promo.Voucher {
		return "", nil
	}
	if err := storeutil.ExecNamed(ctx, db, `UPDATE promo_code SET allowed = false WHERE code = :code`, map[string]any{
		"code": promo.Code,
	}); err != nil {
		return "", fmt.Errorf("can't disable voucher: %w", err)
	}
	return promo.Code, nil
}
