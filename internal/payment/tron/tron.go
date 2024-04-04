package tron

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slog"
)

type Config struct {
	Addresses               []string      `mapstructure:"addresses"`
	Node                    string        `mapstructure:"node"`
	InvoiceExpiration       time.Duration `mapstructure:"invoice_expiration"`
	CheckIncomingTxInterval time.Duration `mapstructure:"check_incoming_tx_interval"`
	ContractAddress         string        `mapstructure:"contract_address"`
}

type Processor struct {
	c      *Config
	pm     *entity.PaymentMethod
	addrs  map[string]*entity.OrderFull
	mu     sync.Mutex
	rep    dependency.Repository
	tg     dependency.Trongrid
	mailer dependency.Mailer
	rates  dependency.RatesService
}

func New(ctx context.Context, c *Config, rep dependency.Repository, m dependency.Mailer, tg dependency.Trongrid, r dependency.RatesService, pmn entity.PaymentMethodName) (dependency.CryptoInvoice, error) {
	pm, ok := rep.Cache().GetPaymentMethodsByName(pmn)
	if !ok {
		return nil, fmt.Errorf("payment method not found")
	}

	addrs := make(map[string]*entity.OrderFull, len(c.Addresses))
	for _, addr := range c.Addresses {
		addrs[addr] = &entity.OrderFull{}
	}

	p := &Processor{
		c:      c,
		pm:     &pm,
		rep:    rep,
		addrs:  addrs,
		mailer: m,
		tg:     tg,
		rates:  r,
	}

	err := p.initAddressesFromUnpaidOrders(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't init addresses from unpaid orders: %w", err)
	}

	return p, nil

}

func (p *Processor) initAddressesFromUnpaidOrders(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	ofs, err := p.rep.Order().GetAwaitingOrdersByPaymentType(ctx, p.pm.Name)
	if err != nil {
		return fmt.Errorf("can't get unpaid orders: %w", err)
	}

	for _, of := range ofs {
		ofC := of
		p.addrs[of.Payment.Payee.String] = &ofC
		go p.monitorPayment(ctx, of.Order.ID, of.Payment)
	}

	return nil
}

// address is our address on which the payment should be made
func (p *Processor) expireOrderPayment(ctx context.Context, orderId, paymentId int, address string) error {
	err := p.rep.Order().ExpireOrderPayment(ctx, orderId, paymentId)
	if err != nil {
		return fmt.Errorf("can't update orders status: %w", err)
	}

	_, err = p.freeAddress(address)
	if err != nil {
		return fmt.Errorf("can't free address: %w", err)
	}

	return nil
}

func (p *Processor) getFreeAddress() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, order := range p.addrs {
		if order == nil || order.Order.ID == 0 {
			return addr, nil
		}
	}
	return "", fmt.Errorf("no free address")
}

func (p *Processor) setAddressOrder(addr string, of *entity.OrderFull) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.addrs[addr]; !ok {
		return fmt.Errorf("address not found")
	}
	p.addrs[addr] = of
	return nil
}

func (p *Processor) freeAddress(addr string) (*entity.OrderFull, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	of, ok := p.addrs[addr]
	if !ok {
		return nil, fmt.Errorf("address not found")
	}
	p.addrs[addr] = nil
	return of, nil
}

// GetOrderInvoice returns the payment details for the given order and expiration date.
func (p *Processor) GetOrderInvoice(ctx context.Context, orderId int) (*entity.PaymentInsert, time.Time, error) {

	var payment *entity.Payment
	expiration := time.Now()
	var err error
	p.rep.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		payment, err = rep.Order().GetPaymentByOrderId(ctx, orderId)
		if err != nil {
			return fmt.Errorf("can't get payment by order id: %w", err)
		}

		// If the payment is already done, return it immediately.
		if payment.IsTransactionDone {
			expiration = payment.ModifiedAt
			return nil
		}

		// Order has unexpired invoice, return it.
		if payment.Payee.Valid && payment.Payee.String != "" {
			expiration = payment.ModifiedAt.Add(p.c.InvoiceExpiration)
			return nil
		}

		// If the payment is not done and the address is not set, generate a new invoice.
		pAddr, err := p.getFreeAddress()
		if err != nil {
			return fmt.Errorf("can't get free address: %w", err)
		}

		orderFull, err := p.rep.Order().InsertOrderInvoice(ctx, orderId, pAddr, p.pm)
		if err != nil {
			return fmt.Errorf("can't insert order invoice: %w", err)
		}

		// convert base currency to payment currency in this case to USD
		totalUSD, err := p.rates.ConvertFromBaseCurrency(dto.USD, payment.TransactionAmount)
		if err != nil {
			return fmt.Errorf("can't convert to base currency: %w", err)
		}

		err = p.rep.Order().UpdateTotalPaymentCurrency(ctx, orderId, totalUSD)
		if err != nil {
			return fmt.Errorf("can't update total payment currency: %w", err)
		}

		err = p.setAddressOrder(pAddr, orderFull)
		if err != nil {
			return fmt.Errorf("can't set address amount: %w", err)
		}

		go p.monitorPayment(ctx, orderId, payment)

		return nil
	})

	return &payment.PaymentInsert, expiration, err
}

func (p *Processor) monitorPayment(ctx context.Context, orderId int, payment *entity.Payment) {
	// Immediately check for transactions at least once before entering the loop.
	payment, err := p.CheckForTransactions(ctx, orderId, payment)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "Error during initial transaction check",
			slog.String("err", err.Error()),
			slog.Int("orderId", orderId),
			slog.String("address", payment.Payee.String),
		)
	}

	if payment.IsTransactionDone {
		return // Exit the loop once the payment is done.
	}

	// Calculate the expiration time based on the payment.ModifiedAt and p.c.InvoiceExpiration.
	expirationDuration := time.Until(payment.ModifiedAt.Add(p.c.InvoiceExpiration))

	ticker := time.NewTicker(p.c.CheckIncomingTxInterval)
	defer ticker.Stop()

	expirationTimer := time.NewTimer(expirationDuration)
	defer expirationTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Default().DebugCtx(ctx, "Context cancelled, stopping payment monitoring")
			return
		case <-ticker.C:
			payment, err := p.CheckForTransactions(ctx, orderId, payment)
			if err != nil {
				slog.Default().ErrorCtx(ctx, "Error during transaction check", slog.String("err", err.Error()))
			}
			if payment.IsTransactionDone {
				return // Exit the loop once the payment is done.
			}
		case <-expirationTimer.C:
			// Attempt to expire the order payment only if it's not already done.
			if !payment.IsTransactionDone {
				if err := p.expireOrderPayment(ctx, orderId, payment.ID, payment.Payee.String); err != nil {
					slog.Default().ErrorCtx(ctx, "can't expire order payment", slog.String("err", err.Error()))
				}
			}
			return // Exit the loop once the payment has expired.
		}
	}
}

func (p *Processor) CheckForTransactions(ctx context.Context, orderId int, payment *entity.Payment) (*entity.Payment, error) {
	transactions, err := p.tg.GetAddressTransactions(payment.Payee.String)
	if err != nil {
		return nil, fmt.Errorf("can't get address transactions: %w", err)
	}

	for _, tx := range transactions.Data {

		blockTimestamp := time.Unix(0, tx.BlockTimestamp*int64(time.Millisecond)).UTC()

		if blockTimestamp.After(payment.ModifiedAt) {

			if tx.TokenInfo.Address != p.c.ContractAddress {
				continue // Skip this transaction if it's not a selected coin transaction.
			}

			amount, err := decimal.NewFromString(tx.Value)
			if err != nil {
				continue // Skip this transaction if the amount cannot be parsed.
			}

			// Convert payment.TransactionAmount to the same scale as blockchain amount
			// Assuming payment.TransactionAmount is in USD and needs to be converted to the format with 6 decimals
			paymentAmountInBlockchainFormat := convertToBlockchainFormat(payment.TransactionAmount, tx.TokenInfo.Decimals)

			if amount.Equal(paymentAmountInBlockchainFormat) {
				// TODO: in transaction OrderPaymentDone + freeAddress
				payment.TransactionID = sql.NullString{
					String: tx.TransactionID,
					Valid:  true,
				}
				payment.Payee = sql.NullString{
					String: tx.To,
					Valid:  true,
				}
				payment.Payer = sql.NullString{
					String: tx.From,
					Valid:  true,
				}

				payment.IsTransactionDone = true
				payment, err = p.rep.Order().OrderPaymentDone(ctx, orderId, payment)
				if err != nil {
					return nil, fmt.Errorf("can't update order payment done: %w", err)
				} else {
					slog.Default().InfoCtx(ctx, "Order marked as paid", slog.Int("orderId", orderId))
				}
				orderFull, err := p.freeAddress(payment.Payee.String)
				if err != nil {
					return nil, fmt.Errorf("can't free address: %w", err)
				}

				orderDetails := dto.OrderFullToOrderConfirmed(orderFull, p.rep.Cache().GetAllSizes(), p.rep.Cache().GetAllShipmentCarriers())
				err = p.mailer.SendOrderConfirmation(ctx, p.rep, orderFull.Buyer.Email, orderDetails)
				if err != nil {
					return nil, fmt.Errorf("can't send order confirmation: %w", err)
				}

				return payment, nil // Exit as the payment is successfully processed.
			}
		}
	}

	return payment, nil // Return nil if no suitable transaction was found.
}

func convertToBlockchainFormat(amount decimal.Decimal, decimals int) decimal.Decimal {
	// Create a new Decimal representing the scale factor (10^decimals).
	scaleFactor := decimal.NewFromInt(10).Pow(decimal.NewFromInt(int64(decimals)))

	// Multiply the transaction amount by the scale factor to get the amount in blockchain format.
	return amount.Mul(scaleFactor)
}
