package mail

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type templateName string

const (
	NewSubscriber  templateName = "new_subscriber.gohtml"
	OrderCancelled templateName = "order_cancelled.gohtml"
	OrderConfirmed templateName = "order_confirmed.gohtml"
	OrderShipped   templateName = "order_shipped.gohtml"
	PromoCode      templateName = "promo_code.gohtml"
)

// Define a map for template names to subjects
var templateSubjects = map[templateName]string{
	NewSubscriber:  "Welcome to GRBPWR",
	OrderCancelled: "Your order has been cancelled",
	OrderConfirmed: "Your order has been confirmed",
	OrderShipped:   "Your order has been shipped",
	PromoCode:      "Your promo code",
}

// SendNewSubscriber sends a welcome email to a new subscriber.
func (m *Mailer) SendNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	ser, err := m.buildSendMailRequest(to, NewSubscriber, struct{}{})
	if err != nil {
		return fmt.Errorf("can't build send mail request for new subscriber: %w", err)
	}
	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderConfirmation sends an order confirmation email.
func (m *Mailer) SendOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, orderDetails *dto.OrderConfirmed) error {
	if orderDetails.OrderUUID == "" || orderDetails.FullName == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}

	ser, err := m.buildSendMailRequest(to, NewSubscriber, orderDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order confirmation : %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderConfirmation sends an order cancellation email.
func (m *Mailer) SendOrderCancellation(ctx context.Context, rep dependency.Repository, to string, orderDetails *dto.OrderCancelled) error {
	if orderDetails.OrderID == "" || orderDetails.Name == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	ser, err := m.buildSendMailRequest(to, NewSubscriber, orderDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order cancellation: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderShipped sends an order shipped email.
func (m *Mailer) SendOrderShipped(ctx context.Context, rep dependency.Repository, to string, shipmentDetails *dto.OrderShipment) error {
	ser, err := m.buildSendMailRequest(to, NewSubscriber, shipmentDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order shipped: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendPromoCode sends a promo code email.
func (m *Mailer) SendPromoCode(ctx context.Context, rep dependency.Repository, to string, promoDetails *dto.PromoCodeDetails) error {
	if promoDetails.PromoCode == "" {
		return fmt.Errorf("incomplete promo code details: %+v", promoDetails)
	}

	ser, err := m.buildSendMailRequest(to, NewSubscriber, promoDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for promo: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}
