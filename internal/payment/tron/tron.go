package tron

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"log/slog"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

type Config struct {
	Addresses               []string      `mapstructure:"addresses"`
	Node                    string        `mapstructure:"node"`
	InvoiceExpiration       time.Duration `mapstructure:"invoice_expiration"`
	CheckIncomingTxInterval time.Duration `mapstructure:"check_incoming_tx_interval"`
	ContractAddress         string        `mapstructure:"contract_address"`
}

type Processor struct {
	c       *Config
	pm      *entity.PaymentMethod
	addrs   map[string]string //k:address v: order uuid
	mu      sync.Mutex
	rep     dependency.Repository
	tg      dependency.Trongrid
	mailer  dependency.Mailer
	rates   dependency.RatesService
	monCtxt map[string]context.CancelFunc // Tracks monitoring contexts by order uuid
	ctxMu   sync.Mutex
}

func New(ctx context.Context, c *Config, rep dependency.Repository, m dependency.Mailer, tg dependency.Trongrid, r dependency.RatesService, pmn entity.PaymentMethodName) (dependency.CryptoInvoice, error) {
	pm, ok := rep.Cache().GetPaymentMethodByName(pmn)
	if !ok {
		return nil, fmt.Errorf("payment method not found")
	}
	if !isPaymentMethodTron(pm) {
		return nil, fmt.Errorf("payment method is not valid for tron")
	}

	addrs := make(map[string]string, len(c.Addresses))
	for _, addr := range c.Addresses {
		addrs[addr] = ""
	}

	p := &Processor{
		c:       c,
		pm:      &pm,
		rep:     rep,
		addrs:   addrs,
		mailer:  m,
		tg:      tg,
		rates:   r,
		monCtxt: make(map[string]context.CancelFunc),
	}

	err := p.initAddressesFromUnpaidOrders(ctx)
	if err != nil {
		return nil, fmt.Errorf("can't init addresses from unpaid orders: %w", err)
	}

	return p, nil

}
func isPaymentMethodTron(pm entity.PaymentMethod) bool {
	return pm.Name == entity.USDT_TRON || pm.Name == entity.USDT_TRON_TEST
}

func (p *Processor) initAddressesFromUnpaidOrders(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	poids, err := p.rep.Order().GetAwaitingPaymentsByPaymentType(ctx, p.pm.Name)
	if err != nil {
		return fmt.Errorf("can't get unpaid orders: %w", err)
	}

	for _, poid := range poids {
		poidC := poid
		p.addrs[poidC.Payment.Payee.String] = poid.OrderUUID
		slog.Default().Info("monitorPayment", slog.Any("poid", poid))
		go p.monitorPayment(ctx, poidC.OrderUUID, &poidC.Payment)
	}

	return nil
}

// address is our address on which the payment should be made
func (p *Processor) expireOrderPayment(ctx context.Context, orderUUID string) error {
	_, err := p.rep.Order().ExpireOrderPayment(ctx, orderUUID)
	if err != nil {
		return fmt.Errorf("can't expire order payment: %w", err)
	}

	err = p.freeAddress(orderUUID)
	if err != nil {
		return fmt.Errorf("can't free address: %w", err)
	}

	return nil
}

func (p *Processor) getFreeAddress() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for addr, orderId := range p.addrs {
		if orderId == "" {
			return addr, nil
		}
	}

	slog.Default().Debug("addrs", slog.Any("addrs", p.addrs))
	return "", fmt.Errorf("no free address")
}

// TODO: rename to setOrderAddress
func (p *Processor) occupyPaymentAddress(addr string, orderUUID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, ok := p.addrs[addr]; !ok {
		return fmt.Errorf("address not found")
	}
	p.addrs[addr] = orderUUID
	return nil
}

func (p *Processor) freeAddress(orderUUID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	for address, oid := range p.addrs {
		if oid == orderUUID {
			p.addrs[address] = ""
			return nil
		}
	}
	slog.Default().Error("can't free address", slog.String("order_uuid", orderUUID))
	return nil
}

// GetOrderInvoice returns the payment details for the given order and expiration date.
func (p *Processor) GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, time.Time, error) {

	var payment *entity.Payment
	expiration := time.Now()
	var err error

	payment, err = p.rep.Order().GetPaymentByOrderUUID(ctx, orderUUID)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't get payment by order id: %w", err)
	}

	// If the payment is already done, return it immediately.
	if payment.IsTransactionDone {
		expiration = payment.ModifiedAt
		return &payment.PaymentInsert, expiration, nil
	}

	// Order has unexpired invoice, return it.
	if payment.Payee.Valid && payment.Payee.String != "" {
		expiration = payment.ModifiedAt.Add(p.c.InvoiceExpiration)
		return &payment.PaymentInsert, expiration, nil
	}

	// If the payment is not done and the address is not set, generate a new invoice.
	pAddr, err := p.getFreeAddress()
	if err != nil {
		return nil, expiration, fmt.Errorf("can't get free address: %w", err)
	}

	of, err := p.rep.Order().InsertOrderInvoice(ctx, orderUUID, pAddr, p.pm)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't insert order invoice: %w", err)
	}
	payment.PaymentInsert.Payee = sql.NullString{
		String: pAddr,
		Valid:  true,
	}
	// convert base currency to payment currency in this case to USD
	totalUSD, err := p.rates.ConvertFromBaseCurrency(dto.USD, of.Order.TotalPrice)
	if err != nil {
		return nil, expiration, fmt.Errorf("can't convert from base currency: %w", err)
	}

	slog.Default().InfoContext(ctx, "Total USD", slog.String("totalUSD", totalUSD.String()))
	// TODO: token decimals to config

	totalBlockchainValue := convertToBlockchainFormat(totalUSD, 6)
	slog.Default().InfoContext(ctx, "Total USD",
		slog.String("totalUSD", totalUSD.String()),
		slog.String("totalUSDBlockchain", totalBlockchainValue.String()),
	)

	payment.TransactionAmountPaymentCurrency = totalBlockchainValue
	payment.TransactionAmount = of.Order.TotalPrice

	p.rep.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {

		err = p.rep.Order().UpdateTotalPaymentCurrency(ctx, orderUUID, totalBlockchainValue)
		if err != nil {
			return fmt.Errorf("can't update total payment currency: %w", err)
		}

		err = p.occupyPaymentAddress(pAddr, orderUUID)
		if err != nil {
			return fmt.Errorf("can't set address amount: %w", err)
		}
		return nil
	})

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

	// // Immediately check for transactions at least once before entering the loop.
	// payment, err := p.CheckForTransactions(ctx, orderId, payment)
	// if err != nil {
	// 	slog.Default().ErrorContext(ctx, "Error during initial transaction check",
	// 		slog.String("err", err.Error()),
	// 		slog.Int("orderId", orderId),
	// 		slog.Any("payment", payment),
	// 	)
	// }
	var err error

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
			slog.Default().DebugContext(ctx, "context cancelled, stopping payment monitoring")
			return
		case <-ticker.C:
			slog.Default().DebugContext(ctx, "checking for transactions",
				slog.String("orderUUID", orderUUID),
				slog.Any("payment", payment),
			)
			payment, err = p.CheckForTransactions(ctx, orderUUID, payment)
			if err != nil {
				slog.Default().ErrorContext(ctx, "error during transaction check",
					slog.String("err", err.Error()),
					slog.String("orderUUID", orderUUID),
					slog.Any("address", payment),
				)
			}
			if payment.IsTransactionDone {
				return // Exit the loop once the payment is done.
			}
		case <-expirationTimer.C:
			slog.Default().InfoContext(ctx, "order payment expired",
				slog.String("orderUUID", orderUUID))
			// Attempt to expire the order payment only if it's not already done.
			if !payment.IsTransactionDone {
				if err := p.expireOrderPayment(ctx, orderUUID); err != nil {
					slog.Default().ErrorContext(ctx, "can't expire order payment",
						slog.String("err", err.Error()),
					)
				}
			}
			return // Exit the loop once the payment has expired.
		}
	}

}

func (p *Processor) CancelMonitorPayment(orderUUID string) error {
	p.ctxMu.Lock()
	defer p.ctxMu.Unlock()

	err := p.freeAddress(orderUUID)
	if err != nil {
		return fmt.Errorf("can't free address: %w", err)
	}

	if cancel, exists := p.monCtxt[orderUUID]; exists {
		cancel()                     // Cancel the monitoring context.
		delete(p.monCtxt, orderUUID) // Clean up the map.
		return nil
	}
	return fmt.Errorf("no monitoring process found for order ID: %s", orderUUID)
}

func (p *Processor) CheckForTransactions(ctx context.Context, orderUUID string, payment *entity.Payment) (*entity.Payment, error) {
	transactions, err := p.tg.GetAddressTransactions(payment.Payee.String)
	if err != nil {
		return nil, fmt.Errorf("can't get address transactions: %w", err)
	}

	slog.Default().Debug("Checking for transactions",
		slog.String("orderUUID", orderUUID),
		slog.String("address", payment.Payee.String),
		slog.Any("txs", transactions),
	)

	for _, tx := range transactions.Data {

		blockTimestamp := time.Unix(0, tx.BlockTimestamp*int64(time.Millisecond)).UTC()

		slog.Default().Debug("Checking transaction",
			slog.Any("blockTimestamp", blockTimestamp.Format(time.UnixDate)),
			slog.Any("now", time.Now().UTC().Format(time.UnixDate)),
			slog.Any("payment.ModifiedAt", payment.ModifiedAt.UTC().Format(time.UnixDate)),
		)

		//TODO: check if the transaction is recent
		if blockTimestamp.After(payment.ModifiedAt.UTC().Add(-1 * time.Minute)) {

			if tx.TokenInfo.Address != p.c.ContractAddress {
				slog.Default().Debug("Skipping transaction",
					slog.String("tx.TokenInfo.Address", tx.TokenInfo.Address),
					slog.String("p.c.ContractAddress", p.c.ContractAddress),
				)
				continue // Skip this transaction if it's not a selected coin transaction.
			}

			amount, err := decimal.NewFromString(tx.Value)
			if err != nil {
				slog.Default().Error("Error parsing transaction amount",
					slog.String("tx.Value", tx.Value),
					slog.String("err", err.Error()),
				)
				continue // Skip this transaction if the amount cannot be parsed.
			}

			// Convert payment.TransactionAmount to the same scale as blockchain amount
			// Assuming payment.TransactionAmount is in USD and needs to be converted to the format with 6 decimals

			slog.Default().Debug("Checking transaction amount",
				slog.String("payment.TransactionAmountPaymentCurrency", payment.TransactionAmountPaymentCurrency.String()),
				slog.String("amount", amount.String()),
				slog.Any("equal", amount.Equal(payment.TransactionAmountPaymentCurrency)),
			)

			if amount.Equal(payment.TransactionAmountPaymentCurrency) {

				slog.Default().Info("Transaction found",
					slog.String("tx.TransactionID", tx.TransactionID),
					slog.String("tx.From", tx.From),
					slog.String("tx.To", tx.To),
					slog.String("tx.Value", tx.Value),
					slog.String("tx.TokenInfo.Address", tx.TokenInfo.Address),
					slog.String("tx.TokenInfo.Decimals", fmt.Sprintf("%d", tx.TokenInfo.Decimals)),
				)
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
				payment, err = p.rep.Order().OrderPaymentDone(ctx, orderUUID, payment)
				if err != nil {
					if mysqlErr, ok := err.(*mysql.MySQLError); ok {
						if mysqlErr.Number == 1062 {
							slog.Default().InfoContext(ctx, "Order already marked as paid", slog.String("orderUUID", orderUUID))
						} else {
							return nil, fmt.Errorf("can't update order payment done: %w", err)
						}
					}
					return nil, fmt.Errorf("can't update order payment done: %w", err)
				} else {
					slog.Default().InfoContext(ctx, "Order marked as paid", slog.String("orderUUID", orderUUID))
				}
				err := p.freeAddress(orderUUID)
				if err != nil {
					return nil, fmt.Errorf("can't free address: %w", err)
				}

				of, err := p.rep.Order().GetOrderFullByUUID(ctx, orderUUID)
				if err != nil {
					return nil, fmt.Errorf("can't get order by id: %w", err)
				}

				orderDetails := dto.OrderFullToOrderConfirmed(of, p.rep.Cache().GetAllSizes(), p.rep.Cache().GetAllShipmentCarriers())
				err = p.mailer.SendOrderConfirmation(ctx, p.rep, of.Buyer.Email, orderDetails)
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
