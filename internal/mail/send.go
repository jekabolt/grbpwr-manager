package mail

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type templateName string

const (
	NewSubscriber        templateName = "new_subscriber.gohtml"
	OrderCancelled       templateName = "order_cancelled.gohtml"
	OrderConfirmed       templateName = "order_confirmed.gohtml"
	OrderShipped         templateName = "order_shipped.gohtml"
	OrderRefundInitiated templateName = "refund_initiated.gohtml"
	PromoCode            templateName = "promo_code.gohtml"
)

// Define a map for template names to subjects
var templateSubjects = map[templateName]string{
	NewSubscriber:        "Welcome to GRBPWR",
	OrderCancelled:       "Your order has been cancelled",
	OrderConfirmed:       "Your order has been confirmed",
	OrderShipped:         "Your order has been shipped",
	OrderRefundInitiated: "Your refund has been initiated",
	PromoCode:            "Your promo code",
}

// SendNewSubscriber sends a welcome email to a new subscriber.
func (m *Mailer) SendNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	data := struct {
		Preheader string
	}{
		Preheader: "Welcome to GRBPWR",
	}
	ser, err := m.buildSendMailRequest(to, NewSubscriber, data)
	if err != nil {
		return fmt.Errorf("can't build send mail request for new subscriber: %w", err)
	}
	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderConfirmation sends an order confirmation email.
func (m *Mailer) SendOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, orderDetails *dto.OrderConfirmed) error {
	if orderDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}

	ser, err := m.buildSendMailRequest(to, OrderConfirmed, orderDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order confirmation : %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderCancellation sends an order cancellation email.
func (m *Mailer) SendOrderCancellation(ctx context.Context, rep dependency.Repository, to string, orderDetails *dto.OrderCancelled) error {
	if orderDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}
	ser, err := m.buildSendMailRequest(to, OrderCancelled, orderDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order cancellation: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderShipped sends an order shipped email.
func (m *Mailer) SendOrderShipped(ctx context.Context, rep dependency.Repository, to string, shipmentDetails *dto.OrderShipment) error {
	if shipmentDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete shipment details: %+v", shipmentDetails)
	}

	ser, err := m.buildSendMailRequest(to, OrderShipped, shipmentDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order shipped: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendRefundInitiated sends a refund initiated email.
func (m *Mailer) SendRefundInitiated(ctx context.Context, rep dependency.Repository, to string, refundDetails *dto.OrderRefundInitiated) error {
	if refundDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete refund details: %+v", refundDetails)
	}

	ser, err := m.buildSendMailRequest(to, OrderRefundInitiated, refundDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for refund initiated: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendPromoCode sends a promo code email.
func (m *Mailer) SendPromoCode(ctx context.Context, rep dependency.Repository, to string, promoDetails *dto.PromoCodeDetails) error {
	if promoDetails.PromoCode == "" {
		return fmt.Errorf("incomplete promo code details: %+v", promoDetails)
	}

	ser, err := m.buildSendMailRequest(to, PromoCode, promoDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for promo: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}
