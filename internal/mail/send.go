package mail

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
)

type templateName string

const (
	AccountLogin         templateName = "account_login.gohtml"
	NewSubscriber        templateName = "new_subscriber.gohtml"
	OrderCancelled       templateName = "order_cancelled.gohtml"
	OrderConfirmed       templateName = "order_confirmed.gohtml"
	OrderShipped         templateName = "order_shipped.gohtml"
	OrderDelivered       templateName = "order_delivered.gohtml"
	OrderRefundInitiated templateName = "refund_initiated.gohtml"
	OrderPendingReturn   templateName = "pending_return.gohtml"
	PromoCode            templateName = "promo_code.gohtml"
	BackInStock          templateName = "back_in_stock.gohtml"

	TierUpgrade             templateName = "tier_upgrade.gohtml"
	TierDowngrade           templateName = "tier_downgrade.gohtml"
	DowngradeReminder       templateName = "downgrade_reminder.gohtml"
	TierRollbackAfterRefund templateName = "tier_rollback_after_refund.gohtml"
	FirstPurchaseThanks     templateName = "first_purchase_thanks.gohtml"
	UnsubscribeConfirmation templateName = "unsubscribe_confirmation.gohtml"
	BirthdayGift            templateName = "birthday_gift.gohtml"
	EventInvite             templateName = "event_invite.gohtml"
	HackerInvite            templateName = "hacker_invite.gohtml"
)

// Define a map for template names to subjects
var templateSubjects = map[templateName]string{
	AccountLogin:         "Your sign-in code",
	NewSubscriber:        "Welcome to GRBPWR",
	OrderCancelled:       "Your order has been cancelled",
	OrderConfirmed:       "Your order has been confirmed",
	OrderShipped:         "Your order has been shipped",
	OrderDelivered:       "Your order has been delivered",
	OrderRefundInitiated: "Your refund has been initiated",
	OrderPendingReturn:   "Your return has been requested",
	PromoCode:            "Your promo code",
	BackInStock:          "Your waitlist item is back in stock",

	TierUpgrade:             "Your GRBPWR tier",
	TierDowngrade:           "Your GRBPWR tier",
	DowngradeReminder:       "Keep your GRBPWR tier",
	TierRollbackAfterRefund: "Your GRBPWR tier was adjusted",
	FirstPurchaseThanks:     "Thank you from GRBPWR",
	UnsubscribeConfirmation: "You've been unsubscribed",
	BirthdayGift:            "A gift from GRBPWR",
	EventInvite:             "You're invited",
	HackerInvite:            "Your GRBPWR HACKER invite",
}

// alwaysSendSubjects lists emails that dispatch even when the mailer is Disabled
// for bulk suppression (beta seeding). Account sign-in (OTP + magic link) is
// required to actually log into beta, so it is never suppressed — everything
// else (order/tier/promo/support/etc.) is.
var alwaysSendSubjects = map[string]bool{
	templateSubjects[AccountLogin]: true,
}

// suppressed reports whether an email with the given subject must be dropped
// because the mailer is Disabled and the subject is not on the always-send list.
func (m *Mailer) suppressed(subject string) bool {
	return m.c.Disabled && !alwaysSendSubjects[subject]
}

// SendNewSubscriber sends a welcome email to a new subscriber.
func (m *Mailer) SendNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	data := &struct {
		Preheader string
		EmailB64  string
	}{
		Preheader: "WELCOME TO GRBPWR",
		EmailB64:  " ", // Non-empty marker to prevent injection - template will check and not show unsubscribe
	}
	ser, err := m.buildSendMailRequest(to, NewSubscriber, data)
	if err != nil {
		return fmt.Errorf("can't build send mail request for new subscriber: %w", err)
	}
	return m.sendWithInsert(ctx, rep, ser)
}

// QueueNewSubscriber queues a welcome email to a new subscriber for asynchronous sending.
func (m *Mailer) QueueNewSubscriber(ctx context.Context, rep dependency.Repository, to string) error {
	data := &struct {
		Preheader string
		EmailB64  string
	}{
		Preheader: "WELCOME TO GRBPWR",
		EmailB64:  " ", // Non-empty marker to prevent injection - template will check and not show unsubscribe
	}
	ser, err := m.buildSendMailRequest(to, NewSubscriber, data)
	if err != nil {
		return fmt.Errorf("can't build send mail request for new subscriber: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
}

// QueueAccountLogin queues a combined OTP + magic link sign-in email.
func (m *Mailer) QueueAccountLogin(ctx context.Context, rep dependency.Repository, to string, otpCode string, magicLinkURL string) error {
	data := &struct {
		Preheader    string
		EmailB64     string
		OTPCode      string
		MagicLinkURL string
	}{
		Preheader:    "Your GRBPWR sign-in code",
		EmailB64:     " ",
		OTPCode:      otpCode,
		MagicLinkURL: magicLinkURL,
	}
	ser, err := m.buildSendMailRequest(to, AccountLogin, data)
	if err != nil {
		return fmt.Errorf("can't build account login email: %w", err)
	}
	return m.queueEmail(ctx, rep, ser)
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

// QueueOrderConfirmation queues an order confirmation email for asynchronous sending.
// The background worker will pick it up and send it. Use this when email must not block
// critical operations (e.g. payment confirmation).
func (m *Mailer) QueueOrderConfirmation(ctx context.Context, rep dependency.Repository, to string, orderDetails *dto.OrderConfirmed) error {
	if orderDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete order details: %+v", orderDetails)
	}

	ser, err := m.buildSendMailRequest(to, OrderConfirmed, orderDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order confirmation: %w", err)
	}

	return m.queueEmail(ctx, rep, ser)
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

// SendOrderDelivered sends an order delivered email (with a link to leave a review). Sent only on
// a real delivery — the manual admin path, the AfterShip signal (webhook/reconcile) — never by the
// timer safety net.
func (m *Mailer) SendOrderDelivered(ctx context.Context, rep dependency.Repository, to string, deliveryDetails *dto.OrderDelivered) error {
	if deliveryDetails.OrderUUID == "" {
		return fmt.Errorf("incomplete delivery details: %+v", deliveryDetails)
	}

	ser, err := m.buildSendMailRequest(to, OrderDelivered, deliveryDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for order delivered: %w", err)
	}

	return m.sendWithInsert(ctx, rep, ser)
}

// SendOrderDeliveredForUUID loads the order and sends the delivered email. Shared by the
// delivery-sync worker (real AfterShip signal) and the AfterShip webhook so the DTO build + review
// link live in one place. The caller is responsible for only invoking it when the order actually
// transitioned to delivered (so it fires at most once per order).
func SendOrderDeliveredForUUID(ctx context.Context, rep dependency.Repository, mailer dependency.Mailer, orderUUID string) error {
	of, err := rep.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("get order for delivered email: %w", err)
	}
	if of.Buyer.Email == "" {
		return fmt.Errorf("order %s has no buyer email", orderUUID)
	}
	return mailer.SendOrderDelivered(ctx, rep, of.Buyer.Email, dto.OrderFullToOrderDelivered(of))
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

// SendPendingReturn sends a pending return email when order is set to PendingReturn (waiting for parcel back).
func (m *Mailer) SendPendingReturn(ctx context.Context, rep dependency.Repository, to string, details *dto.OrderPendingReturn) error {
	if details.OrderUUID == "" {
		return fmt.Errorf("incomplete pending return details: %+v", details)
	}

	ser, err := m.buildSendMailRequest(to, OrderPendingReturn, details)
	if err != nil {
		return fmt.Errorf("can't build send mail request for pending return: %w", err)
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

// SendBackInStock sends a back-in-stock notification email.
// It queues the email for asynchronous sending to avoid blocking operations.
func (m *Mailer) SendBackInStock(ctx context.Context, rep dependency.Repository, to string, productDetails *dto.BackInStock) error {
	if productDetails.ProductURL == "" {
		return fmt.Errorf("incomplete product details: %+v", productDetails)
	}

	ser, err := m.buildSendMailRequest(to, BackInStock, productDetails)
	if err != nil {
		return fmt.Errorf("can't build send mail request for back in stock: %w", err)
	}

	// Queue email for async sending (better for batch operations)
	return m.queueEmail(ctx, rep, ser)
}
