package stripe

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/preorderpayment"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/shopspring/decimal"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
)

type Config struct {
	SecretKey         string        `mapstructure:"secret_key"`
	PubKey            string        `mapstructure:"pub_key"`
	InvoiceExpiration time.Duration `mapstructure:"invoice_expiration"`
}

// ErrPaymentAlreadyCompleted is returned when the PaymentIntent was already used for a completed payment.
var ErrPaymentAlreadyCompleted = errors.New("payment already completed for this session")

type Processor struct {
	c                *Config
	mailer           dependency.Mailer
	rep              dependency.Repository
	stripeClient     *client.API
	pm               entity.PaymentMethod
	reservationMgr   dependency.StockReservationManager
	preOrderStore    *preorderpayment.Store

	monCtxt map[string]context.CancelFunc // tracks monitoring contexts by order uuid
	ctxMu   sync.Mutex
}

func New(ctx context.Context, c *Config, rep dependency.Repository, m dependency.Mailer, pmn entity.PaymentMethodName) (dependency.Invoicer, error) {
	if cache.GetBaseCurrency() == "" {
		return nil, fmt.Errorf("base currency not configured")
	}

	pm, ok := cache.GetPaymentMethodByName(pmn)
	if !ok {
		return nil, fmt.Errorf("payment method not found")
	}
	if !isPaymentMethodCard(pm.Method) {
		return nil, fmt.Errorf("payment method is not valid for card")
	}

	stripe.DefaultLeveledLogger = log.NewSlogLeveledLogger()

	ttl := c.InvoiceExpiration
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	p := Processor{
		c:              c,
		mailer:         m,
		stripeClient:   client.New(c.SecretKey, nil),
		rep:            rep,
		pm:             pm.Method,
		monCtxt:        make(map[string]context.CancelFunc),
		reservationMgr: nil, // Will be set via SetReservationManager if needed
		preOrderStore:  preorderpayment.NewStore(ttl),
	}

	err := p.initAddressesFromUnpaidOrders(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't init addresses from unpaid orders: %w", err)
	}

	return &p, nil

}

func isPaymentMethodCard(pm entity.PaymentMethod) bool {
	return pm.Name == entity.CARD || pm.Name == entity.CARD_TEST
}

func (p *Processor) initAddressesFromUnpaidOrders(ctx context.Context) error {
	poids, err := p.rep.Order().GetAwaitingPaymentsByPaymentType(ctx, p.pm.Name)
	if err != nil {
		return fmt.Errorf("can't get unpaid orders: %w", err)
	}

	for _, poid := range poids {
		p.ctxMu.Lock()
		if _, exists := p.monCtxt[poid.OrderUUID]; exists {
			p.ctxMu.Unlock()
			continue
		}
		p.ctxMu.Unlock()

		slog.Default().Info("monitorPayment", slog.Any("poid", poid))
		go p.monitorPayment(ctx, poid.OrderUUID, &poid.Payment)
	}

	return nil
}

// address is our address on which the payment should be made
func (p *Processor) expireOrderPayment(ctx context.Context, orderUUID string) error {

	payment, err := p.rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get payment by order id: %w", err)
	}

	if payment.IsTransactionDone {
		return nil
	}

	pi, err := p.getPaymentIntent(payment.ClientSecret.String)
	if err != nil {
		return fmt.Errorf("can't get payment intent: %w", err)
	}

	switch pi.Status {
	case stripe.PaymentIntentStatusSucceeded:
		// Fetch PaymentIntent with expanded payment_method to get sub-type (apple_pay, klarna, etc.)
		piExpanded, expandErr := p.getPaymentIntentWithExpand(payment.ClientSecret.String, []string{"payment_method"})
		if expandErr == nil && piExpanded.PaymentMethod != nil {
			payment.PaymentInsert.PaymentMethodType = sql.NullString{
				String: string(piExpanded.PaymentMethod.Type),
				Valid:  true,
			}
		}
		err = p.updateOrderAsPaid(ctx, p.rep, orderUUID, *payment)
		if err != nil {
			return fmt.Errorf("can't update order as paid: %w", err)
		}
		return nil
	default:
		_, err = p.rep.Order().ExpireOrderPayment(ctx, orderUUID)
		if err != nil {
			return fmt.Errorf("can't expire order payment: %w", err)
		}
		p.CancelMonitorPayment(orderUUID)
		p.cancelPaymentIntent(payment.ClientSecret.String)
	}

	return nil
}

func (p *Processor) updateOrderAsPaid(ctx context.Context, rep dependency.Repository, orderUUID string, payment entity.Payment) error {
	var err error

	payment.IsTransactionDone = true
	wasUpdated, err := rep.Order().OrderPaymentDone(ctx, orderUUID, &payment)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok && mysqlErr.Number == 1062 {
			slog.Default().InfoContext(ctx, "Order already marked as paid (idempotent)", slog.String("orderUUID", orderUUID))
			return nil
		}
		return fmt.Errorf("can't update order payment done: %w", err)
	}

	// Only send confirmation email if we actually updated the order status
	if !wasUpdated {
		slog.Default().InfoContext(ctx, "Order already confirmed, skipping duplicate email", slog.String("orderUUID", orderUUID))
		return nil
	}

	slog.Default().InfoContext(ctx, "Order marked as paid", slog.String("orderUUID", orderUUID))

	// RELEASE RESERVATION: Free stock when payment is completed
	if p.reservationMgr != nil {
		p.reservationMgr.Release(ctx, orderUUID)
	}

	of, err := rep.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by id: %w", err)
	}

	orderDetails := dto.OrderFullToOrderConfirmed(of)
	if err := p.mailer.QueueOrderConfirmation(ctx, rep, of.Buyer.Email, orderDetails); err != nil {
		// Log but never fail payment update due to email - worker will retry queued emails
		slog.Default().ErrorContext(ctx, "can't queue order confirmation email",
			slog.String("orderUUID", orderUUID),
			slog.String("err", err.Error()),
		)
	}

	return nil

}

// GetOrderInvoice returns the payment details for the given order and expiration date.
func (p *Processor) GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, error) {

	payment := &entity.Payment{}
	var err error

	payment, err = p.rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get payment by order id: %w", err)
	}

	// If the payment is already done, return it immediately.
	if payment.IsTransactionDone {
		return &payment.PaymentInsert, nil
	}

	// Order has unexpired invoice, return it.
	if payment.ClientSecret.Valid {
		if !payment.PaymentInsert.ExpiredAt.Valid || payment.PaymentInsert.ExpiredAt.Time.After(time.Now()) {
			return &payment.PaymentInsert, nil
		}
	}

	// CRITICAL: Create PaymentIntent inside transaction to prevent race condition.
	// Without this, concurrent requests could both create PaymentIntents on Stripe,
	// then serialize at DB level. The "loser" would error out but leave an orphaned
	// PaymentIntent on Stripe (money leak). By creating PI inside the TX after
	// acquiring the order lock, we ensure only one request creates the PI.
	var pi *stripe.PaymentIntent
	var paymentCurrencyAmount decimal.Decimal

	err = p.rep.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// Re-check payment status inside transaction after acquiring lock (via InsertFiatInvoice)
		// Another request may have created the invoice while we were waiting
		payment, err = rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get payment by order id: %w", err)
		}

		if payment.ClientSecret.Valid {
			if !payment.PaymentInsert.ExpiredAt.Valid || payment.PaymentInsert.ExpiredAt.Time.After(time.Now()) {
				return nil // Invoice already exists and is valid, skip PI creation
			}
		}

		// Get order details for PaymentIntent creation
		of, err := rep.Order().GetOrderFullByUUID(ctx, orderUUID)
		if err != nil {
			return fmt.Errorf("can't get order by id: %w", err)
		}

		// Idempotency key: use rotation when payment was expired so Stripe creates a fresh PI instead of returning cached expired one
		idempotencyKey := orderUUID
		if payment.ClientSecret.Valid {
			// Had ClientSecret but expired - rotate key to get new PaymentIntent
			idempotencyKey = orderUUID + "_" + strconv.FormatInt(time.Now().Unix(), 10)
		}

		// Create PaymentIntent on Stripe (external API call inside TX - acceptable for idempotency)
		pi, err = p.createPaymentIntent(*of, idempotencyKey)
		if err != nil {
			return fmt.Errorf("can't create payment intent: %w", err)
		}

		// Get the actual amount charged from PaymentIntent (in payment currency)
		// PaymentIntent.Amount is in smallest currency unit (cents for most currencies, but not for zero-decimal like JPY, KRW)
		paymentCurrencyAmount = AmountFromSmallestUnit(pi.Amount, string(pi.Currency))

		of, err = rep.Order().InsertFiatInvoice(ctx, orderUUID, pi.ClientSecret, p.pm, time.Now().Add(p.expirationDuration()))
		if err != nil {
			return fmt.Errorf("can't insert fiat invoice: %w", err)
		}

		payment.PaymentInsert.ClientSecret = sql.NullString{
			String: pi.ClientSecret,
			Valid:  true,
		}

		// Set transaction amounts: order currency amount and payment currency amount (what was actually charged)
		payment.TransactionAmount = of.Order.TotalPriceDecimal()
		payment.TransactionAmountPaymentCurrency = paymentCurrencyAmount

		err = rep.Order().UpdateTotalPaymentCurrency(ctx, orderUUID, paymentCurrencyAmount)
		if err != nil {
			return fmt.Errorf("can't update total payment currency: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("can't insert fiat invoice: %w", err)
	}

	// If pi is nil, it means another request created the invoice while we were waiting
	if pi == nil {
		// Return the existing payment info
		return &payment.PaymentInsert, nil
	}

	payment.PaymentInsert.ExpiredAt = sql.NullTime{
		Time:  time.Now().Add(p.expirationDuration()),
		Valid: true,
	}

	go p.monitorPayment(context.TODO(), orderUUID, payment)

	return &payment.PaymentInsert, nil
}

func (p *Processor) monitorPayment(ctx context.Context, orderUUID string, payment *entity.Payment) {
	ctx, cancel := context.WithCancel(ctx)
	p.ctxMu.Lock()
	p.monCtxt[orderUUID] = cancel
	p.ctxMu.Unlock()

	defer cancel() // Ensure the context is cancelled when the monitoring stops.
	defer func() {
		p.ctxMu.Lock()
		delete(p.monCtxt, orderUUID) // Clean up the map when monitoring ends.
		p.ctxMu.Unlock()
	}()

	if payment.IsTransactionDone {
		return // Exit the loop once the payment is done.
	}

	// Calculate the expiration time based on the payment.ModifiedAt and p.c.InvoiceExpiration.
	expirationDuration := time.Until(time.Now().Add(p.c.InvoiceExpiration))
	if payment.PaymentInsert.ExpiredAt.Valid {
		expirationDuration = time.Until(payment.PaymentInsert.ExpiredAt.Time)
	}

	expirationTimer := time.NewTimer(expirationDuration)
	defer expirationTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Default().DebugContext(ctx, "context cancelled, stopping payment monitoring")
			return
		case <-expirationTimer.C:
			slog.Default().InfoContext(ctx, "order payment expired",
				slog.String("orderUUID", orderUUID))
			if err := p.expireOrderPayment(ctx, orderUUID); err != nil {
				slog.Default().ErrorContext(ctx, "can't expire order payment",
					slog.String("err", err.Error()),
				)
			}
			return // exit the loop once the payment has expired.
		}
	}

}

func (p *Processor) CancelMonitorPayment(orderUUID string) error {
	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()

	if cancel, exists := p.monCtxt[orderUUID]; exists {
		cancel()                     // Cancel the monitoring context.
		delete(p.monCtxt, orderUUID) // Clean up the map.
		return nil
	}
	return fmt.Errorf("no monitoring process found for order ID: %s", orderUUID)
}

func (p *Processor) ExpirationDuration() time.Duration {
	return p.expirationDuration()
}

func (p *Processor) expirationDuration() time.Duration {
	if sec := cache.GetOrderExpirationSeconds(); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return p.c.InvoiceExpiration
}

func (p *Processor) CheckForTransactions(ctx context.Context, orderUUID string, payment entity.Payment) (*entity.Payment, error) {

	if payment.IsTransactionDone {
		return &payment, nil
	}

	o, err := p.rep.Order().GetOrderByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}

	if o.OrderStatusId != cache.OrderStatusAwaitingPayment.Status.Id {
		return &payment, nil
	}

	pi, err := p.getPaymentIntent(payment.ClientSecret.String)
	if err != nil {
		slog.Default().Info("can't get payment intent", "err", err)
		return &payment, fmt.Errorf("can't get payment intent: %w", err)
	}

	if pi != nil {
		switch pi.Status {
		case stripe.PaymentIntentStatusSucceeded:
			slog.Default().Info("payment intent succeeded", "pi", pi)
			err := p.updateOrderAsPaid(ctx, p.rep, orderUUID, payment)
			if err != nil {
				return nil, fmt.Errorf("can't update order as paid: %w", err)
			}
			return &payment, nil
		default:
			slog.Default().Info("payment intent not succeeded", "pi", pi)
			return &payment, nil
		}
	}

	return &payment, nil

}

// CreatePreOrderPaymentIntent creates a PaymentIntent before order submission
func (p *Processor) CreatePreOrderPaymentIntent(ctx context.Context, amount decimal.Decimal, currency string, country string, idempotencyKey string) (*stripe.PaymentIntent, error) {
	// Validate amount meets Stripe minimum (e.g. KRW >= 100)
	if err := dto.ValidatePriceMeetsMinimum(amount, currency); err != nil {
		return nil, fmt.Errorf("amount below currency minimum: %w", err)
	}
	// Stripe requires lowercase ISO currency codes (e.g. "eur" not "EUR")
	currency = strings.ToLower(currency)
	// Convert amount to smallest currency unit (cents for most currencies, but not for zero-decimal like JPY, KRW)
	amountCents := AmountToSmallestUnit(amount, currency)

	params := &stripe.PaymentIntentParams{
		Amount:   stripe.Int64(amountCents),
		Currency: stripe.String(currency),
		AutomaticPaymentMethods: &stripe.PaymentIntentAutomaticPaymentMethodsParams{
			Enabled: stripe.Bool(true),
		},
		Metadata: map[string]string{
			"pre_order": "true",
			"country":   country,
		},
	}
	params.SetIdempotencyKey(idempotencyKey)

	// Create the PaymentIntent
	pi, err := p.stripeClient.PaymentIntents.New(params)
	if err != nil {
		return nil, fmt.Errorf("failed to create pre-order PaymentIntent: %w", err)
	}

	slog.Default().InfoContext(ctx, "created pre-order PaymentIntent",
		slog.String("payment_intent_id", pi.ID),
		slog.Int64("amount_cents", amountCents),
		slog.String("currency", currency),
		slog.String("country", country),
	)

	return pi, nil
}

// GetOrCreatePreOrderPaymentIntent gets or creates a PaymentIntent for pre-order with idempotency and rotation.
func (p *Processor) GetOrCreatePreOrderPaymentIntent(ctx context.Context, idempotencyKey string, amount decimal.Decimal, currency, country string, cartFingerprint string) (*stripe.PaymentIntent, string, error) {
	// New session: no key or key not found
	if idempotencyKey == "" {
		return p.createNewPreOrderSession(ctx, "", amount, currency, country, cartFingerprint)
	}

	sess, ok := p.preOrderStore.Get(idempotencyKey)
	if !ok {
		return p.createNewPreOrderSession(ctx, "", amount, currency, country, cartFingerprint)
	}

	// Cart changed - old key stale
	if sess.CartFingerprint != cartFingerprint {
		p.preOrderStore.Delete(idempotencyKey)
		return p.createNewPreOrderSession(ctx, "", amount, currency, country, cartFingerprint)
	}

	// Check if PI already succeeded (payment completed)
	pi, err := p.GetPaymentIntentByID(ctx, sess.PaymentIntentID)
	if err != nil {
		slog.Default().WarnContext(ctx, "failed to get payment intent for pre-order session, creating new",
			slog.String("payment_intent_id", sess.PaymentIntentID),
			slog.String("err", err.Error()),
		)
		p.preOrderStore.Delete(idempotencyKey)
		return p.createNewPreOrderSession(ctx, "", amount, currency, country, cartFingerprint)
	}
	if pi.Status == stripe.PaymentIntentStatusSucceeded {
		p.preOrderStore.Delete(idempotencyKey)
		return nil, "", ErrPaymentAlreadyCompleted
	}

	// Session expired - rotate: create new PI, return new key
	if time.Now().After(sess.ExpiresAt) {
		stripeKey := idempotencyKey + "_rotated_" + strconv.FormatInt(time.Now().Unix(), 10)
		newPi, err := p.CreatePreOrderPaymentIntent(ctx, amount, currency, country, stripeKey)
		if err != nil {
			return nil, "", err
		}
		newKey := p.preOrderStore.Put("", &preorderpayment.Session{
			PaymentIntentID:      newPi.ID,
			ClientSecret:         newPi.ClientSecret,
			StripeIdempotencyKey: stripeKey,
			CartFingerprint:      cartFingerprint,
		})
		p.preOrderStore.Delete(idempotencyKey)
		slog.Default().InfoContext(ctx, "rotated pre-order payment session",
			slog.String("old_key", idempotencyKey),
			slog.String("new_key", newKey),
			slog.String("payment_intent_id", newPi.ID),
		)
		return newPi, newKey, nil
	}

	// Valid session - return existing PI (reconstruct for response; ClientSecret is in sess)
	return &stripe.PaymentIntent{
		ID:          sess.PaymentIntentID,
		ClientSecret: sess.ClientSecret,
	}, "", nil
}

func (p *Processor) createNewPreOrderSession(ctx context.Context, idempotencyKey string, amount decimal.Decimal, currency, country string, cartFingerprint string) (*stripe.PaymentIntent, string, error) {
	ourKey := idempotencyKey
	if ourKey == "" {
		ourKey = uuid.New().String()
	}
	stripeKey := "preorder_" + ourKey
	pi, err := p.CreatePreOrderPaymentIntent(ctx, amount, currency, country, stripeKey)
	if err != nil {
		return nil, "", err
	}
	key := p.preOrderStore.Put(ourKey, &preorderpayment.Session{
		PaymentIntentID:      pi.ID,
		ClientSecret:         pi.ClientSecret,
		StripeIdempotencyKey: stripeKey,
		CartFingerprint:      cartFingerprint,
	})
	return pi, key, nil
}

// UpdatePaymentIntentWithOrder updates an existing PaymentIntent with order details
func (p *Processor) UpdatePaymentIntentWithOrder(ctx context.Context, paymentIntentID string, order entity.OrderFull) error {
	params := &stripe.PaymentIntentParams{
		Description:  stripe.String(fmt.Sprintf("order #%s", order.Order.UUID)),
		ReceiptEmail: stripe.String(order.Buyer.Email),
		Metadata: map[string]string{
			"order_id": order.Order.UUID,
		},
		Shipping: &stripe.ShippingDetailsParams{
			Address: &stripe.AddressParams{
				City:       &order.Shipping.City,
				Country:    &order.Shipping.Country,
				Line1:      &order.Shipping.AddressLineOne,
				Line2:      &order.Shipping.AddressLineTwo.String,
				PostalCode: &order.Shipping.PostalCode,
				State:      &order.Shipping.State.String,
			},
			Name: stripe.String(fmt.Sprintf("%s %s", order.Buyer.FirstName, order.Buyer.LastName)),
		},
	}

	_, err := p.stripeClient.PaymentIntents.Update(paymentIntentID, params)
	if err != nil {
		return fmt.Errorf("failed to update PaymentIntent with order details: %w", err)
	}

	slog.Default().InfoContext(ctx, "updated PaymentIntent with order details",
		slog.String("payment_intent_id", paymentIntentID),
		slog.String("order_uuid", order.Order.UUID),
	)

	return nil
}

// UpdatePaymentIntentWithOrderNew updates a PaymentIntent using OrderNew data (optimized, no DB query needed)
func (p *Processor) UpdatePaymentIntentWithOrderNew(ctx context.Context, paymentIntentID string, orderUUID string, orderNew *entity.OrderNew) error {
	params := &stripe.PaymentIntentParams{
		Description:  stripe.String(fmt.Sprintf("order #%s", orderUUID)),
		ReceiptEmail: stripe.String(orderNew.Buyer.Email),
		Metadata: map[string]string{
			"order_id": orderUUID,
		},
		Shipping: &stripe.ShippingDetailsParams{
			Address: &stripe.AddressParams{
				City:       &orderNew.ShippingAddress.City,
				Country:    &orderNew.ShippingAddress.Country,
				Line1:      &orderNew.ShippingAddress.AddressLineOne,
				Line2:      &orderNew.ShippingAddress.AddressLineTwo.String,
				PostalCode: &orderNew.ShippingAddress.PostalCode,
				State:      &orderNew.ShippingAddress.State.String,
			},
			Name: stripe.String(fmt.Sprintf("%s %s", orderNew.Buyer.FirstName, orderNew.Buyer.LastName)),
		},
	}

	_, err := p.stripeClient.PaymentIntents.Update(paymentIntentID, params)
	if err != nil {
		return fmt.Errorf("failed to update PaymentIntent with order details: %w", err)
	}

	slog.Default().InfoContext(ctx, "updated PaymentIntent with order details",
		slog.String("payment_intent_id", paymentIntentID),
		slog.String("order_uuid", orderUUID),
	)

	return nil
}

// StartMonitoringPayment starts monitoring an existing payment
func (p *Processor) StartMonitoringPayment(ctx context.Context, orderUUID string, payment entity.Payment) {
	go p.monitorPayment(ctx, orderUUID, &payment)
}

// CleanupOrphanedPreOrderPaymentIntents searches Stripe for PaymentIntents with metadata pre_order=true
// older than olderThan and cancels them. Only cancellable statuses are cancelled (requires_payment_method,
// requires_confirmation, requires_action, processing, requires_capture).
func (p *Processor) CleanupOrphanedPreOrderPaymentIntents(ctx context.Context, olderThan time.Time) error {
	cutoffUnix := olderThan.Unix()
	query := fmt.Sprintf("metadata['pre_order']:'true' AND created<%d", cutoffUnix)
	limit := int64(100)
	params := &stripe.PaymentIntentSearchParams{}
	params.Query = query
	params.Limit = &limit
	params.Context = ctx

	iter := p.stripeClient.PaymentIntents.Search(params)
	cancelled := 0
	for iter.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		pi := iter.PaymentIntent()
		if !isCancellable(pi.Status) {
			continue
		}
		reason := stripe.String(string(stripe.PaymentIntentCancellationReasonAbandoned))
		_, err := p.stripeClient.PaymentIntents.Cancel(pi.ID, &stripe.PaymentIntentCancelParams{
			CancellationReason: reason,
		})
		if err != nil {
			slog.Default().ErrorContext(ctx, "stripe reconcile: failed to cancel orphaned pre-order PI",
				slog.String("payment_intent_id", pi.ID),
				slog.String("err", err.Error()),
			)
			continue
		}
		cancelled++
		slog.Default().InfoContext(ctx, "stripe reconcile: cancelled orphaned pre-order PI",
			slog.String("payment_intent_id", pi.ID),
		)
	}
	if err := iter.Err(); err != nil {
		return fmt.Errorf("stripe search: %w", err)
	}
	if cancelled > 0 {
		slog.Default().InfoContext(ctx, "stripe reconcile: cancelled orphaned pre-order PIs",
			slog.Int("count", cancelled),
		)
	}
	return nil
}

func isCancellable(status stripe.PaymentIntentStatus) bool {
	switch status {
	case stripe.PaymentIntentStatusRequiresPaymentMethod,
		stripe.PaymentIntentStatusRequiresConfirmation,
		stripe.PaymentIntentStatusRequiresAction,
		stripe.PaymentIntentStatusProcessing,
		stripe.PaymentIntentStatusRequiresCapture:
		return true
	default:
		return false
	}
}
