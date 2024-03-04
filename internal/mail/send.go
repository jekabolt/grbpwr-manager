package mail

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

const (
	NewSubscriber  = "new_subscriber.gohtml"
	OrderCancelled = "order_cancelled.gohtml"
	OrderConfirmed = "order_confirmed.gohtml"
	OrderShipped   = "order_shipped.gohtml"
	PromoCode      = "promo_code.gohtml"
)

// Define a map for template names to subjects
var templateSubjects = map[string]string{
	NewSubscriber:  "Welcome to GRBPWR",
	OrderCancelled: "Your order has been cancelled",
	OrderConfirmed: "Your order has been confirmed",
	OrderShipped:   "Your order has been shipped",
	PromoCode:      "Your promo code",
}

// SendNewSubscriber sends a welcome email to a new subscriber.
func (m *Mailer) SendNewSubscriber(ctx context.Context, to string) (*entity.SendEmailRequest, error) {
	return m.send(ctx, to, NewSubscriber, struct{}{})
}

// SendOrderConfirmation sends an order confirmation email.
func (m *Mailer) SendOrderConfirmation(ctx context.Context, to string, orderDetails *dto.OrderConfirmed) (*entity.SendEmailRequest, error) {
	if orderDetails.OrderUUID == "" || orderDetails.FullName == "" {
		return nil, fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	return m.send(ctx, to, OrderConfirmed, orderDetails)
}

// SendOrderConfirmation sends an order cancellation email.
func (m *Mailer) SendOrderCancellation(ctx context.Context, to string, orderDetails *dto.OrderCancelled) (*entity.SendEmailRequest, error) {
	if orderDetails.OrderID == "" || orderDetails.Name == "" {
		return nil, fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	return m.send(ctx, to, OrderCancelled, orderDetails)
}

// SendOrderShipped sends an order shipped email.
func (m *Mailer) SendOrderShipped(ctx context.Context, to string, shipmentDetails *dto.OrderShipment) (*entity.SendEmailRequest, error) {
	return m.send(ctx, to, OrderShipped, shipmentDetails)
}

// SendPromoCode sends a promo code email.
func (m *Mailer) SendPromoCode(ctx context.Context, to string, promoDetails *dto.PromoCodeDetails) (*entity.SendEmailRequest, error) {
	if promoDetails.PromoCode == "" {
		return nil, fmt.Errorf("incomplete promo code details: %+v", promoDetails)
	}
	return m.send(ctx, to, PromoCode, promoDetails)
}
