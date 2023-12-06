package mail

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

const (
	NewSubscriber  = "new_subscriber.gohtml"
	OrderCancelled = "order_cancelled.gohtml"
	OrderPlaced    = "order_placed.gohtml"
	OrderShipped   = "order_shipped.gohtml"
	PromoCode      = "promo_code.gohtml"
)

// Define a map for template names to subjects
var templateSubjects = map[string]string{
	NewSubscriber:  "Welcome to GRBPWR",
	OrderCancelled: "Your order has been cancelled",
	OrderPlaced:    "Your order has been placed",
	OrderShipped:   "Your order has been shipped",
	PromoCode:      "Your promo code",
}

// SendNewSubscriber sends a welcome email to a new subscriber.
func (m *Mailer) SendNewSubscriber(to string) error {
	return m.send(to, NewSubscriber, struct{}{})
}

// SendOrderConfirmation sends an order confirmation email.
func (m *Mailer) SendOrderConfirmation(to string, orderDetails *dto.OrderConfirmed) error {
	if orderDetails.OrderID == "" || orderDetails.Name == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	return m.send(to, OrderPlaced, orderDetails) // Added validation for OrderDetails fields.
}

// SendOrderConfirmation sends an order cancellation email.
func (m *Mailer) SendOrderCancellation(to string, orderDetails *dto.OrderCancelled) error {
	if orderDetails.OrderID == "" || orderDetails.Name == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	return m.send(to, OrderCancelled, orderDetails)
}

// SendOrderShipped sends an order shipped email.
func (m *Mailer) SendOrderShipped(to string, shipmentDetails *dto.OrderShipment) error {
	if shipmentDetails.OrderID == "" || shipmentDetails.TrackingNumber == "" {
		return fmt.Errorf("incomplete shipment details: %+v", shipmentDetails)
	}
	return m.send(to, OrderShipped, shipmentDetails)
}

// SendPromoCode sends a promo code email.
func (m *Mailer) SendPromoCode(to string, promoDetails *dto.PromoCodeDetails) error {
	if promoDetails.PromoCode == "" {
		return fmt.Errorf("incomplete promo code details: %+v", promoDetails)
	}
	return m.send(to, PromoCode, promoDetails)
}
