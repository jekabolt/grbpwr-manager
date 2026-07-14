package order

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// SetShipmentLabel persists the carrier-generated shipping-label fields on an order's shipment,
// keyed by order UUID. It writes only the label columns; the tracking code and the Shipped status
// transition are written separately by SetTrackingNumber (the shared shipOrder path), so a
// manually-entered tracking number is unaffected. label_created_at is stamped now. Errors if no
// shipment matches the UUID.
func (s *Store) SetShipmentLabel(ctx context.Context, orderUUID string, label entity.ShipmentLabel) error {
	query := `
	UPDATE shipment sh
	JOIN customer_order co ON co.id = sh.order_id
	SET sh.label_url = :labelUrl,
		sh.carrier_shipment_id = :carrierShipmentId,
		sh.label_service_type = :serviceType,
		sh.parcel_weight_grams = :parcelWeightGrams,
		sh.parcel_dimensions = :parcelDimensions,
		sh.label_created_at = :labelCreatedAt
	WHERE co.uuid = :uuid`
	rows, err := storeutil.ExecNamedRows(ctx, s.DB, query, map[string]any{
		"uuid":              orderUUID,
		"labelUrl":          label.LabelURL,
		"carrierShipmentId": label.CarrierShipmentID,
		"serviceType":       sql.NullString{String: label.ServiceType, Valid: label.ServiceType != ""},
		"parcelWeightGrams": label.ParcelWeightGrams,
		"parcelDimensions":  sql.NullString{String: label.ParcelDimensions, Valid: label.ParcelDimensions != ""},
		"labelCreatedAt":    time.Now().UTC(),
	})
	if err != nil {
		return fmt.Errorf("can't set shipment label: %w", err)
	}
	if rows == 0 {
		return fmt.Errorf("no shipment found for order uuid %s", orderUUID)
	}
	return nil
}

// VoidShipmentLabel reverses a label-generated shipment: it clears the label + tracking + shipping
// date and reverts the order Shipped -> Confirmed so the operator can regenerate. Only a Shipped
// order that actually has a generated label (carrier_shipment_id) can be voided; a manually-shipped
// order (no label) or a delivered order is rejected. The carrier-side cancellation is done by the
// caller before this; here we only undo the local state, in one transaction.
func (s *Store) VoidShipmentLabel(ctx context.Context, orderUUID string) error {
	return s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		order, err := getOrderByUUIDForUpdate(ctx, rep.DB(), orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by uuid: %w", err)
		}
		if _, err := validateOrderStatus(order, entity.Shipped); err != nil {
			return fmt.Errorf("order must be shipped to void its label: %w", err)
		}
		shipment, err := getOrderShipment(ctx, rep.DB(), order.Id)
		if err != nil {
			return fmt.Errorf("can't get order shipment: %w", err)
		}
		if !shipment.HasLabel() {
			return fmt.Errorf("order has no generated label to void")
		}
		if err := storeutil.ExecNamed(ctx, rep.DB(), `
			UPDATE shipment
			SET label_url = NULL, carrier_shipment_id = NULL, label_service_type = NULL,
			    label_created_at = NULL, parcel_weight_grams = NULL, parcel_dimensions = NULL,
			    tracking_code = NULL, shipping_date = NULL
			WHERE order_id = :orderId`,
			map[string]any{"orderId": order.Id}); err != nil {
			return fmt.Errorf("can't clear shipment label: %w", err)
		}
		if err := updateOrderStatus(ctx, rep.DB(), order.Id, cache.OrderStatusConfirmed.Status.Id); err != nil {
			return fmt.Errorf("can't revert order status to confirmed: %w", err)
		}
		return nil
	})
}

// GetOrderParcelItems returns each line's packaging weight/box for an order, joined from the
// product's primary tech card (order_item -> product.primary_tech_card_id -> tech_card_packaging).
// weight_gross / box_dimensions are NULL for a product with no primary tech card or no packaging
// spec; the caller sums the weights, picks a box, and flags any NULL line so an operator supplies a
// manual override. Used to pre-fill the label form (PrepareShippingLabel).
func (s *Store) GetOrderParcelItems(ctx context.Context, orderID int) ([]entity.OrderItemParcel, error) {
	query := `
	SELECT
		oi.product_id,
		oi.quantity,
		oi.product_price * (1 - COALESCE(oi.product_sale_percentage, 0) / 100) AS product_price_with_sale,
		p.sku,
		p.hs_code,
		p.country_of_origin,
		p.customs_description,
		tcp.weight_gross_grams,
		tcp.box_dimensions
	FROM order_item oi
	JOIN product p ON oi.product_id = p.id
	LEFT JOIN tech_card_packaging tcp ON tcp.tech_card_id = p.primary_tech_card_id
	WHERE oi.order_id = :orderId`
	items, err := storeutil.QueryListNamed[entity.OrderItemParcel](ctx, s.DB, query, map[string]any{
		"orderId": orderID,
	})
	if err != nil {
		return nil, fmt.Errorf("can't get order parcel items: %w", err)
	}
	return items, nil
}
