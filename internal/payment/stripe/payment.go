package stripe

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/log"

	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
)

type Config struct {
	SecretKey         string        `mapstructure:"secret_key"`
	PubKey            string        `mapstructure:"pub_key"`
	InvoiceExpiration time.Duration `mapstructure:"invoice_expiration"`
}

type Processor struct {
	c            *Config
	baseCurrency dto.CurrencyTicker
	mailer       dependency.Mailer
	rates        dependency.RatesService
	rep          dependency.Repository
	stripeClient *client.API
	pm           entity.PaymentMethod

	// secrets map[string]string //k:clientSecret v: order uuid
	// mu      sync.Mutex

	monCtxt map[string]context.CancelFunc // tracks monitoring contexts by order uuid
	ctxMu   sync.Mutex
}

func New(ctx context.Context, c *Config, rep dependency.Repository, rs dependency.RatesService, m dependency.Mailer, pmn entity.PaymentMethodName) (dependency.Invoicer, error) {
	ticker, ok := dto.VerifyCurrencyTicker(cache.GetBaseCurrency())
	if !ok {
		return nil, fmt.Errorf("invalid default currency: %s", cache.GetBaseCurrency())
	}

	pm, ok := cache.GetPaymentMethodByName(pmn)
	if !ok {
		return nil, fmt.Errorf("payment method not found")
	}
	if !isPaymentMethodCard(pm.Method) {
		return nil, fmt.Errorf("payment method is not valid for card")
	}

	stripe.DefaultLeveledLogger = log.NewSlogLeveledLogger()

	p := Processor{
		c:            c,
		baseCurrency: ticker,
		rates:        rs,
		mailer:       m,
		stripeClient: client.New(c.SecretKey, nil),
		rep:          rep,
		pm:           pm.Method,
		monCtxt:      make(map[string]context.CancelFunc),
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
		err := p.updateOrderAsPaid(ctx, p.rep, orderUUID, payment)
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

func (p *Processor) updateOrderAsPaid(ctx context.Context, rep dependency.Repository, orderUUID string, payment *entity.Payment) error {
	var err error

	payment.IsTransactionDone = true
	_, err = rep.Order().OrderPaymentDone(ctx, orderUUID, payment)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == 1062 {
				slog.Default().InfoContext(ctx, "Order already marked as paid", slog.String("orderUUID", orderUUID))
			} else {
				return fmt.Errorf("can't update order payment done: %w", err)
			}
		}
		return fmt.Errorf("can't update order payment done: %w", err)
	} else {
		slog.Default().InfoContext(ctx, "Order marked as paid", slog.String("orderUUID", orderUUID))
	}

	of, err := rep.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't get order by id: %w", err)
	}

	orderDetails := dto.OrderFullToOrderConfirmed(of)
	err = p.mailer.SendOrderConfirmation(ctx, rep, of.Buyer.Email, orderDetails)
	if err != nil {
		return fmt.Errorf("can't send order confirmation: %w", err)
	}

	return nil

}

// GetOrderInvoice returns the payment details for the given order and expiration date.
func (p *Processor) GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, time.Time, error) {

	payment := &entity.Payment{}
	expiration := time.Now()
	var err error

	payment, err = p.rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't get payment by order id: %w", err)
	}

	of, err := p.rep.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't get order by id: %w", err)
	}

	// If the payment is already done, return it immediately.
	if payment.IsTransactionDone {
		expiration = payment.ModifiedAt
		return &payment.PaymentInsert, expiration, nil
	}

	// Order has unexpired invoice, return it.
	if payment.ClientSecret.Valid && payment.Payee.String != "" {
		expiration = payment.ModifiedAt.Add(p.c.InvoiceExpiration)
		return &payment.PaymentInsert, expiration, nil
	}

	pi, err := p.createPaymentIntent(*of)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't create payment intent: %w", err)
	}

	err = p.rep.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		of, err = p.rep.Order().InsertFiatInvoice(ctx, orderUUID, pi.ClientSecret, p.pm)
		if err != nil {
			return fmt.Errorf("can't insert fiat invoice: %w", err)
		}
		payment.PaymentInsert.ClientSecret = sql.NullString{
			String: pi.ClientSecret,
			Valid:  true,
		}

		payment.TransactionAmountPaymentCurrency = of.Order.TotalPriceDecimal()
		payment.TransactionAmount = of.Order.TotalPriceDecimal()

		err = p.rep.Order().UpdateTotalPaymentCurrency(ctx, orderUUID, payment.TransactionAmount)
		if err != nil {
			return fmt.Errorf("can't update total payment currency: %w", err)
		}

		return nil
	})

	if err != nil {
		return nil, expiration, fmt.Errorf("can't insert fiat invoice: %w", err)
	}

	go p.monitorPayment(context.TODO(), orderUUID, payment)

	return &payment.PaymentInsert, expiration, err
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
	expirationDuration := time.Until(payment.ModifiedAt.Add(p.c.InvoiceExpiration))

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

func (p *Processor) CheckForTransactions(ctx context.Context, orderUUID string, payment *entity.Payment) (*entity.Payment, error) {

	if payment.IsTransactionDone {
		return payment, nil
	}

	o, err := p.rep.Order().GetOrderByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("can't get order by id: %w", err)
	}

	if o.OrderStatusId != cache.OrderStatusAwaitingPayment.Status.Id {
		return payment, nil
	}

	pi, err := p.getPaymentIntent(payment.ClientSecret.String)
	if err != nil {
		return nil, fmt.Errorf("can't get payment intent: %w", err)
	}

	switch pi.Status {
	case stripe.PaymentIntentStatusSucceeded:
		err := p.updateOrderAsPaid(ctx, p.rep, orderUUID, payment)
		if err != nil {
			return nil, fmt.Errorf("can't update order as paid: %w", err)
		}
		return payment, nil
	default:
		return nil, nil
	}
}
