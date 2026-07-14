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
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/preorderpayment"
	"github.com/jekabolt/grbpwr-manager/internal/tiermanagement"
	"github.com/jekabolt/grbpwr-manager/log"
	"github.com/shopspring/decimal"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
)

type Config struct {
	SecretKey         string        `mapstructure:"secret_key"`
	PubKey            string        `mapstructure:"pub_key"`
	InvoiceExpiration time.Duration `mapstructure:"invoice_expiration"`
	// WebhookSecret is the Stripe webhook signing secret (whsec_...) used to
	// verify inbound events for this processor. Empty disables this endpoint.
	WebhookSecret string `mapstructure:"webhook_secret"`
}

// ErrPaymentAlreadyCompleted is returned when the PaymentIntent was already used for a completed payment.
var ErrPaymentAlreadyCompleted = errors.New("payment already completed for this session")

// monEntry is a single tracked payment monitor: its cancel func plus a unique
// token used to identify map ownership (CancelFunc values are not comparable).
type monEntry struct {
	cancel context.CancelFunc
	token  *int
}

// ErrUnderpaid is returned by updateOrderAsPaid when a PaymentIntent succeeded
// for less than the order total (amount tampering / early confirmation). The
// order is intentionally left AwaitingPayment for manual review rather than
// fulfilled; callers treat it as "not paid", not as a hard failure.
var ErrUnderpaid = errors.New("payment intent succeeded for less than the order total")

type Processor struct {
	c              *Config
	mailer         dependency.Mailer
	rep            dependency.Repository
	stripeClient   *client.API
	pm             entity.PaymentMethod
	reservationMgr dependency.StockReservationManager
	preOrderStore  *preorderpayment.Store
	ga4mp          *ga4mp.Client

	monCtxt map[string]monEntry // tracks monitoring contexts by order uuid
	ctxMu   sync.Mutex

	// monParentCtx is the parent context for ALL in-process payment monitors.
	// Every monitor derives its context from this one, so cancelling monParent
	// (via StopAllMonitors) stops every monitor at shutdown — before the DB is
	// closed — instead of leaving detached goroutines racing against teardown.
	monParentCtx    context.Context
	monParentCancel context.CancelFunc
	// monWg tracks in-flight monitor goroutines so StopAllMonitors can wait for
	// them to finish before the app closes the DB.
	monWg sync.WaitGroup
}

func New(ctx context.Context, c *Config, rep dependency.Repository, m dependency.Mailer, pmn entity.PaymentMethodName) (dependency.Invoicer, error) {
	if c.SecretKey == "" {
		// Without this guard an empty key only surfaces at the first live charge
		// (i.e. in production checkout). Fail closed at startup instead.
		return nil, fmt.Errorf("stripe secret_key is required for payment method %s", pmn)
	}
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
	// Parent context for all in-process payment monitors. Derived from
	// context.Background() (NOT the boot ctx) so monitors live for the lifetime
	// of the process and are stopped explicitly via StopAllMonitors at shutdown.
	monParentCtx, monParentCancel := context.WithCancel(context.Background())

	p := Processor{
		c:               c,
		mailer:          m,
		stripeClient:    client.New(c.SecretKey, nil),
		rep:             rep,
		pm:              pm.Method,
		monCtxt:         make(map[string]monEntry),
		reservationMgr:  nil, // Will be set via SetReservationManager if needed
		preOrderStore:   preorderpayment.NewStore(ttl),
		monParentCtx:    monParentCtx,
		monParentCancel: monParentCancel,
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
		p.monWg.Add(1)
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
		// The payment method type, card, receipt and settled amount are all captured from the
		// charge in capturePaymentDetails (via updateOrderAsPaid), so no separate expand here.
		received := AmountFromSmallestUnit(pi.AmountReceived, string(pi.Currency))
		err = p.updateOrderAsPaid(ctx, p.rep, orderUUID, *payment, received)
		if err != nil {
			// Underpaid: leave the order AwaitingPayment (flagged for review) and
			// do NOT fall through to the cancel/restore-stock branch below.
			if errors.Is(err, ErrUnderpaid) {
				return nil
			}
			return fmt.Errorf("can't update order as paid: %w", err)
		}
		return nil
	default:
		_, err = p.rep.Order().ExpireOrderPayment(ctx, orderUUID)
		if err != nil {
			return fmt.Errorf("can't expire order payment: %w", err)
		}
		if p.reservationMgr != nil {
			p.reservationMgr.Release(ctx, orderUUID)
		}
		p.CancelMonitorPayment(orderUUID)
		p.cancelPaymentIntent(payment.ClientSecret.String)
	}

	return nil
}

// ExpireOrderPayment verifies the order's PaymentIntent with Stripe and either
// marks the order paid (when the PaymentIntent already succeeded) or expires it
// (cancel + restore stock). Unlike the store-level ExpireOrderPayment, this is
// safe to call from the cleanup safety-net: it never cancels a payment that
// actually succeeded on Stripe.
func (p *Processor) ExpireOrderPayment(ctx context.Context, orderUUID string) error {
	return p.expireOrderPayment(ctx, orderUUID)
}

// updateOrderAsPaid marks an order paid after its PaymentIntent succeeded.
// receivedAmount is the amount Stripe actually collected, in the payment
// currency, and is validated against the order total before fulfillment: the
// client holds the client_secret from pre-checkout and can confirm the
// PaymentIntent before the server finalizes the amount, so a succeeded PI is not
// by itself proof of full payment. This is the single choke point for all
// confirmation paths (expiry monitor, lazy check, and the Stripe webhook).
func (p *Processor) updateOrderAsPaid(ctx context.Context, rep dependency.Repository, orderUUID string, payment entity.Payment, receivedAmount decimal.Decimal) error {
	var err error

	// Amount-tamper / underpayment guard. expected is the order total in payment
	// currency, persisted at invoice time (see SubmitOrder/GetOrderInvoice). When
	// Stripe received less, leave the order AwaitingPayment and flag for review
	// instead of fulfilling it for less than it owes.
	expected := payment.TransactionAmountPaymentCurrency
	if !expected.IsPositive() {
		// The expected amount is not finalized yet. The payment row is created with
		// expected=0 and InsertFiatInvoice commits the real total only after the PI's
		// order_id metadata is published, so a payment_intent.succeeded webhook can
		// arrive in that window. Treating 0 as "no check" would skip the underpayment
		// guard below and fulfill for whatever Stripe collected (possibly the lower
		// pre-checkout amount). Return a transient error so the webhook (mapped to HTTP
		// 500) is retried by Stripe once the invoice commits; the monitor and
		// lazy-check paths simply retry on their next pass.
		return fmt.Errorf("order %s expected amount not finalized yet, retry", orderUUID)
	}
	if receivedAmount.LessThan(expected) {
		slog.Default().ErrorContext(ctx, "UNDERPAID order: payment intent succeeded for less than the order total; leaving AwaitingPayment for manual review",
			slog.String("orderUUID", orderUUID),
			slog.String("received", receivedAmount.String()),
			slog.String("expected", expected.String()),
		)
		p.flagUnderpaidForReview(ctx, rep, orderUUID, receivedAmount, expected)
		return ErrUnderpaid
	}

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

	// Capture the Stripe payment detail (settled EUR + fee, PaymentIntent id, method
	// type, card, receipt, FX rate, risk) for accurate revenue analytics and admin
	// visibility. Best effort — never blocks fulfillment.
	p.capturePaymentDetails(ctx, rep, orderUUID, payment)

	// RELEASE RESERVATION: Free stock when payment is completed
	if p.reservationMgr != nil {
		p.reservationMgr.Release(ctx, orderUUID)
	}

	of, err := rep.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by id: %w", err)
	}

	if p.ga4mp != nil {
		p.ga4mp.TrackPurchase(ctx, *of)
	}

	orderDetails := dto.OrderFullToOrderConfirmed(of)
	if err := p.mailer.QueueOrderConfirmation(ctx, rep, of.Buyer.Email, orderDetails); err != nil {
		// Log but never fail payment update due to email - worker will retry queued emails
		slog.Default().ErrorContext(ctx, "can't queue order confirmation email",
			slog.String("orderUUID", orderUUID),
			slog.String("err", err.Error()),
		)
	}

	// Loyalty: recompute spend and upgrade tier if newly qualified (best effort).
	if err := tiermanagement.NewEngine(rep, p.mailer).EvaluateAfterOrderPaid(ctx, of.Buyer.Email); err != nil {
		slog.Default().ErrorContext(ctx, "can't evaluate tier after order paid",
			slog.String("orderUUID", orderUUID),
			slog.String("err", err.Error()),
		)
	}

	return nil

}

// capturePaymentDetails records, best-effort, the facts of a succeeded Stripe charge:
//   - the actual amount Stripe settled in the base currency + the processing fee, from the
//     charge's balance transaction (may lag for async methods; guarded);
//   - the PaymentIntent id, payment method / wallet, card brand+last4, hosted receipt URL,
//     FX rate and Radar risk level, all read from the charge (present as soon as the PI
//     succeeds), so the admin can see and deep-link the payment without re-hitting Stripe.
//
// It never blocks fulfilment: when total_settled_base stays NULL (no balance transaction yet,
// or a non-base settlement), revenue metrics fall back to the product_price reconstruction.
// This is the single capture point for every confirmation path (webhook, expiry monitor, lazy
// CheckForTransactions), so the payment method type is populated on all of them.
func (p *Processor) capturePaymentDetails(ctx context.Context, rep dependency.Repository, orderUUID string, payment entity.Payment) {
	if !payment.ClientSecret.Valid || payment.ClientSecret.String == "" {
		return
	}
	pi, err := p.getPaymentIntentWithExpand(payment.ClientSecret.String, []string{"latest_charge.balance_transaction"})
	if err != nil {
		slog.Default().ErrorContext(ctx, "capture-payment: can't fetch payment intent",
			slog.String("orderUUID", orderUUID), slog.String("err", err.Error()))
		return
	}
	ch := pi.LatestCharge
	if ch == nil {
		slog.Default().WarnContext(ctx, "capture-payment: no charge on succeeded payment intent yet",
			slog.String("orderUUID", orderUUID))
		return
	}

	// Charge-level detail (available as soon as the PI succeeds). Persisted to the payment row.
	details := entity.StripePaymentDetails{
		TransactionID:     pi.ID,
		PaymentMethodType: paymentMethodLabel(ch),
		ReceiptURL:        ch.ReceiptURL,
	}
	if pmd := ch.PaymentMethodDetails; pmd != nil && pmd.Card != nil {
		details.CardBrand = string(pmd.Card.Brand)
		details.CardLast4 = pmd.Card.Last4
	}
	if ch.Outcome != nil {
		details.StripeRiskLevel = ch.Outcome.RiskLevel
	}

	// Settlement figures (base currency) from the balance transaction. It may not exist yet
	// for async settlement — capture the charge detail regardless and only write settled/fee
	// (and the FX rate) when the balance transaction is present and settles in the base ccy.
	baseCurrency := cache.GetBaseCurrency()
	var settledCaptured bool
	var settledBase, paymentFee decimal.Decimal
	if bt := ch.BalanceTransaction; bt != nil {
		if strings.EqualFold(string(bt.Currency), baseCurrency) {
			settledBase = AmountFromSmallestUnit(bt.Amount, baseCurrency)
			// Stripe keeps the fee even on refunds, so this is the full fee actually paid.
			paymentFee = AmountFromSmallestUnit(bt.Fee, baseCurrency)
			settledCaptured = true
			if bt.ExchangeRate != 0 {
				details.StripeExchangeRate = decimal.NullDecimal{
					Decimal: decimal.NewFromFloat(bt.ExchangeRate),
					Valid:   true,
				}
			}
		} else {
			slog.Default().ErrorContext(ctx, "capture-payment: Stripe settlement currency != base currency; skipping settled capture",
				slog.String("orderUUID", orderUUID),
				slog.String("settlement_currency", string(bt.Currency)),
				slog.String("base_currency", baseCurrency))
		}
	} else {
		slog.Default().WarnContext(ctx, "capture-payment: no balance transaction on charge yet; settled base left NULL",
			slog.String("orderUUID", orderUUID))
	}

	if err := rep.Order().UpdatePaymentStripeDetails(ctx, orderUUID, details); err != nil {
		slog.Default().ErrorContext(ctx, "capture-payment: can't persist payment detail",
			slog.String("orderUUID", orderUUID), slog.String("err", err.Error()))
	}
	if settledCaptured {
		if err := rep.Order().UpdateSettledBaseAndFee(ctx, orderUUID, settledBase, paymentFee); err != nil {
			slog.Default().ErrorContext(ctx, "capture-payment: can't persist settled base and fee",
				slog.String("orderUUID", orderUUID), slog.String("err", err.Error()))
		}
	}
	slog.Default().InfoContext(ctx, "payment detail captured",
		slog.String("orderUUID", orderUUID),
		slog.String("payment_intent_id", pi.ID),
		slog.String("method_type", details.PaymentMethodType),
		slog.Bool("settled_captured", settledCaptured),
		slog.String("amount_base", settledBase.String()),
		slog.String("payment_fee_base", paymentFee.String()))
}

// paymentMethodLabel returns the most specific label for how a charge was paid: the card
// wallet (apple_pay, google_pay, link, ...) when the card was tokenised through a wallet,
// otherwise the payment-method type on the charge (card, klarna, sepa_debit, ...).
func paymentMethodLabel(ch *stripe.Charge) string {
	pmd := ch.PaymentMethodDetails
	if pmd == nil {
		return ""
	}
	if pmd.Card != nil && pmd.Card.Wallet != nil && pmd.Card.Wallet.Type != "" {
		return string(pmd.Card.Wallet.Type)
	}
	return string(pmd.Type)
}

// flagUnderpaidForReview records a persistent, admin-visible marker on the order
// so an underpaid-but-charged order surfaces in the admin panel for manual
// reconciliation. Best effort: it never blocks confirmation handling.
func (p *Processor) flagUnderpaidForReview(ctx context.Context, rep dependency.Repository, orderUUID string, received, expected decimal.Decimal) {
	comment := fmt.Sprintf("UNDERPAID: Stripe received %s but the order total is %s — manual review required; order kept in AwaitingPayment.",
		received.String(), expected.String())
	if err := rep.Order().AddOrderComment(ctx, orderUUID, comment); err != nil {
		slog.Default().ErrorContext(ctx, "can't flag underpaid order for review",
			slog.String("orderUUID", orderUUID),
			slog.String("err", err.Error()),
		)
	}
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
		if !payment.PaymentInsert.ExpiredAt.Valid || payment.PaymentInsert.ExpiredAt.Time.After(time.Now().UTC()) {
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
			if !payment.PaymentInsert.ExpiredAt.Valid || payment.PaymentInsert.ExpiredAt.Time.After(time.Now().UTC()) {
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
			idempotencyKey = orderUUID + "_" + strconv.FormatInt(time.Now().UTC().Unix(), 10)
		}

		// Create PaymentIntent on Stripe (external API call inside TX - acceptable for idempotency)
		pi, err = p.createPaymentIntent(*of, idempotencyKey)
		if err != nil {
			return fmt.Errorf("can't create payment intent: %w", err)
		}

		// Get the actual amount charged from PaymentIntent (in payment currency)
		// PaymentIntent.Amount is in smallest currency unit (cents for most currencies, but not for zero-decimal like JPY, KRW)
		paymentCurrencyAmount = AmountFromSmallestUnit(pi.Amount, string(pi.Currency))

		of, err = rep.Order().InsertFiatInvoice(ctx, orderUUID, pi.ClientSecret, p.pm, time.Now().UTC().Add(p.expirationDuration()))
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
		Time:  time.Now().UTC().Add(p.expirationDuration()),
		Valid: true,
	}

	// The monitor's lifecycle is governed by the processor's parent context
	// (monParentCtx), not the passed ctx — see monitorPayment.
	p.monWg.Add(1)
	go p.monitorPayment(context.Background(), orderUUID, payment)

	return &payment.PaymentInsert, nil
}

// monitorPayment watches a single order's PaymentIntent until it expires (or is
// cancelled). It derives its context from the processor-wide parent (monParentCtx)
// so StopAllMonitors can stop every monitor at shutdown; the caller-supplied ctx
// is intentionally ignored for cancellation (e.g. a per-request ctx would die the
// moment the RPC returns). Monitors are tracked in monWg so shutdown can wait for
// them to finish before the DB is closed.
func (p *Processor) monitorPayment(_ context.Context, orderUUID string, payment *entity.Payment) {
	// The caller does monWg.Add(1) immediately before `go`, so the monitor is
	// registered before StopAllMonitors' Wait can observe the counter. Adding inside
	// the goroutine raced Wait, which could return early and let the DB close under a
	// live monitor (violating the WaitGroup Add-before-Wait contract).
	defer p.monWg.Done()

	ctx, cancel := context.WithCancel(p.monParentCtx)
	// token identifies this monitor's entry in monCtxt so a later monitor that
	// replaces us can be distinguished on cleanup (CancelFunc values are not
	// comparable, so we key map ownership by this unique pointer instead).
	token := new(int)

	p.ctxMu.Lock()
	// Cancel any monitor already registered for this order before overwriting it,
	// otherwise the previous goroutine leaks and CancelMonitorPayment /
	// confirmPaymentFromWebhook would only ever cancel the latest one.
	if prev, exists := p.monCtxt[orderUUID]; exists {
		prev.cancel()
	}
	p.monCtxt[orderUUID] = monEntry{cancel: cancel, token: token}
	p.ctxMu.Unlock()

	defer cancel() // Ensure the context is cancelled when the monitoring stops.
	defer func() {
		p.ctxMu.Lock()
		// Only delete our own entry: a newer monitor may have replaced us in the
		// map (it cancelled us above), and removing it would orphan that monitor.
		if cur, ok := p.monCtxt[orderUUID]; ok && cur.token == token {
			delete(p.monCtxt, orderUUID) // Clean up the map when monitoring ends.
		}
		p.ctxMu.Unlock()
	}()

	if payment.IsTransactionDone {
		return // Exit the loop once the payment is done.
	}

	// Calculate the expiration time based on the payment.ModifiedAt and p.c.InvoiceExpiration.
	expirationDuration := time.Until(time.Now().UTC().Add(p.c.InvoiceExpiration))
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

	if entry, exists := p.monCtxt[orderUUID]; exists {
		entry.cancel()               // Cancel the monitoring context.
		delete(p.monCtxt, orderUUID) // Clean up the map.
		return nil
	}
	return fmt.Errorf("no monitoring process found for order ID: %s", orderUUID)
}

// StopAllMonitors cancels the processor-wide monitor parent context (stopping
// every in-process payment monitor) and waits for the in-flight monitor
// goroutines to return, or until ctx is done. It is called from App.Stop after
// the workers are stopped but BEFORE the DB is closed, so monitors never write to
// a closed connection pool. Safe to call more than once.
func (p *Processor) StopAllMonitors(ctx context.Context) {
	if p.monParentCancel != nil {
		p.monParentCancel()
	}

	// Also cancel any per-order entries explicitly (belt-and-suspenders: they all
	// derive from monParentCtx, but this releases their cancel funcs promptly).
	p.ctxMu.Lock()
	for uuid, entry := range p.monCtxt {
		entry.cancel()
		delete(p.monCtxt, uuid)
	}
	p.ctxMu.Unlock()

	done := make(chan struct{})
	go func() {
		p.monWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.Default().InfoContext(ctx, "all stripe payment monitors stopped")
	case <-ctx.Done():
		slog.Default().WarnContext(ctx, "timed out waiting for stripe payment monitors to stop",
			slog.String("err", ctx.Err().Error()))
	}

	// Stop the pre-order session cleanup goroutine, which this processor owns and
	// which otherwise leaks one ticker per shutdown. Stop is stopOnce-guarded, so
	// this stays safe alongside StopAllMonitors being called more than once.
	if p.preOrderStore != nil {
		p.preOrderStore.Stop()
	}
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
			received := AmountFromSmallestUnit(pi.AmountReceived, string(pi.Currency))
			err := p.updateOrderAsPaid(ctx, p.rep, orderUUID, payment, received)
			if err != nil {
				// Underpaid: order stays AwaitingPayment (flagged for review);
				// surface the current payment state without a hard error.
				if errors.Is(err, ErrUnderpaid) {
					return &payment, nil
				}
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
	if time.Now().UTC().After(sess.ExpiresAt) {
		stripeKey := idempotencyKey + "_rotated_" + strconv.FormatInt(time.Now().UTC().Unix(), 10)
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
		ID:           sess.PaymentIntentID,
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

// StartMonitoringPayment starts monitoring an existing payment in a background
// goroutine. The monitor's cancellation is tied to the processor's parent
// context (monParentCtx), not the supplied ctx, so it survives the caller's
// request returning and is stopped centrally via StopAllMonitors at shutdown.
func (p *Processor) StartMonitoringPayment(ctx context.Context, orderUUID string, payment entity.Payment) {
	p.monWg.Add(1)
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
