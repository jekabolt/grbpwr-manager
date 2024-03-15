package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"golang.org/x/exp/slog"
)

var (
	exchangeRatesBaseURL = "http://api.exchangeratesapi.io/v1/"
	cryptoCompareBaseURL = "https://min-api.cryptocompare.com/data/"
	defaultTimeout       = 10 * time.Second
)

type Config struct {
	ExchangeAPIKey    string        `mapstructure:"api_key"`
	RatesUpdatePeriod time.Duration `mapstructure:"rates_update_period"`
	BaseCurrency      string        `mapstructure:"base_currency"`
}

var currencyDescriptions = map[dto.CurrencyTicker]string{
	dto.BTC: "Bitcoin",
	dto.ETH: "Ethereum",
	dto.CHF: "Swiss Franc",
	dto.CNY: "Chinese Yuan",
	dto.CZK: "Czech Republic Koruna",
	dto.DKK: "Danish Krone",
	dto.EUR: "Euro",
	dto.GBP: "British Pound Sterling",
	dto.GEL: "Georgian Lari",
	dto.HKD: "Hong Kong Dollar",
	dto.HUF: "Hungarian Forint",
	dto.ILS: "Israeli New Sheqel",
	dto.JPY: "Japanese Yen",
	dto.NOK: "Norwegian Krone",
	dto.PLN: "Polish Zloty",
	dto.RUB: "Russian Ruble",
	dto.SEK: "Swedish Krona",
	dto.SGD: "Singapore Dollar",
	dto.TRY: "Turkish Lira",
	dto.UAH: "Ukrainian Hryvnia",
	dto.USD: "United States Dollar",
}

type Client struct {
	config       *Config
	fiatClient   *resty.Client
	cryptoClient *resty.Client
	rates        sync.Map // thread-safe storage for currency rates
	ratesStore   dependency.Rates
	ctx          context.Context
	cancel       context.CancelFunc
	baseCurrency dto.CurrencyTicker
}

func New(config *Config, ratesStore dependency.Rates) (dependency.RatesService, error) {
	if _, exists := currencyDescriptions[dto.CurrencyTicker(strings.ToUpper(config.BaseCurrency))]; !exists {
		return nil, fmt.Errorf("unsupported base currency: %s", config.BaseCurrency)
	}

	fiatClient := resty.New().SetTimeout(defaultTimeout).SetBaseURL(exchangeRatesBaseURL).SetQueryParam("access_key", config.ExchangeAPIKey)
	cryptoClient := resty.New().SetTimeout(defaultTimeout).SetBaseURL(cryptoCompareBaseURL)

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		config:       config,
		fiatClient:   fiatClient,
		cryptoClient: cryptoClient,
		ratesStore:   ratesStore,
		ctx:          ctx,
		cancel:       cancel,
		baseCurrency: dto.CurrencyTicker(strings.ToUpper(config.BaseCurrency)),
	}, nil
}

// Start initializes and starts the rates update process.
func (c *Client) Start() {
	if err := c.loadLatestRates(); err != nil {
		slog.Error("Failed to load latest rates", slog.String("error", err.Error()))
	}

	go c.scheduleRateUpdates()
}

// Stop signals the update process to stop.
func (c *Client) Stop() {
	c.cancel()
}

func (c *Client) GetBaseCurrency() dto.CurrencyTicker {
	return c.baseCurrency
}

// ConvertToBaseCurrency converts the given amount from the given currency to the base currency.
func (c *Client) ConvertToBaseCurrency(currencyFrom dto.CurrencyTicker, amount decimal.Decimal) (decimal.Decimal, error) {
	if currencyFrom == c.baseCurrency {
		return amount, nil
	}

	rate, ok := c.GetRates()[currencyFrom]
	if !ok {
		return decimal.Zero, fmt.Errorf("unsupported currency: %s", currencyFrom)
	}
	return amount.Div(rate.Rate), nil
}

// ConvertFromBaseCurrency converts the given amount from the base currency to the given currency.
func (c *Client) ConvertFromBaseCurrency(currencyTo dto.CurrencyTicker, amount decimal.Decimal) (decimal.Decimal, error) {
	if currencyTo == c.baseCurrency {
		return amount, nil
	}

	rate, ok := c.GetRates()[currencyTo]
	if !ok {
		return decimal.Zero, fmt.Errorf("unsupported currency: %s", currencyTo)
	}
	return amount.Mul(rate.Rate), nil
}

// GetRates returns the current rates.
func (c *Client) GetRates() map[dto.CurrencyTicker]dto.CurrencyRate {
	rates := make(map[dto.CurrencyTicker]dto.CurrencyRate)
	c.rates.Range(func(key, value interface{}) bool {
		ticker, ok := key.(string)
		if !ok {
			slog.Error("Invalid currency ticker type", slog.String("type", fmt.Sprintf("%T", key)))
			return true
		}
		rates[dto.CurrencyTicker(ticker)] = value.(dto.CurrencyRate)
		return true
	})
	return rates
}

func (c *Client) loadLatestRates() error {
	latestRates, err := c.ratesStore.GetLatestRates(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to get latest rates: %w", err)
	}

	for _, rate := range latestRates {
		c.rates.Store(dto.CurrencyTicker(rate.CurrencyCode), dto.CurrencyRate{
			Description: currencyDescriptions[dto.CurrencyTicker(rate.CurrencyCode)],
			Rate:        rate.Rate,
		})
	}

	return nil
}

func (c *Client) scheduleRateUpdates() {
	ticker := time.NewTicker(c.config.RatesUpdatePeriod)
	defer ticker.Stop()

	err := c.updateRates()
	if err != nil {
		slog.Default().Error("Failed to update rates", slog.String("error", err.Error()))
	}

	for {
		select {
		case <-ticker.C:
			err := c.updateRates()
			if err != nil {
				slog.Default().Error("Failed to update rates", slog.String("error", err.Error()))
			}
		case <-c.ctx.Done():
			return
		}
	}
}

func (c *Client) updateRates() error {
	fiatRates, err := c.fetchFiatRates()
	if err != nil {
		return err
	}

	cryptoRates, err := c.fetchCryptoRates()
	if err != nil {
		return err
	}

	// Update fiat rates in the map.
	for k, v := range fiatRates {
		// Ensure keys are of type CurrencyTicker.
		ticker, ok := currencyDescriptions[dto.CurrencyTicker(k)]
		if ok {
			c.rates.Store(ticker, v)
		} else {
			slog.Error("Unsupported currency ticker for fiat rate",
				slog.String("ticker", k))
		}
	}

	// Update crypto rates in the map.
	for k, v := range cryptoRates {
		// Ensure keys are of type CurrencyTicker.
		ticker, ok := currencyDescriptions[dto.CurrencyTicker(k)]
		if ok {
			c.rates.Store(ticker, v)
		} else {
			slog.Error("Unsupported currency ticker for crypto rate",
				slog.String("ticker", k))
		}
	}

	crs := []entity.CurrencyRate{}
	rates := c.GetRates()
	for k, v := range rates {
		crs = append(crs, entity.CurrencyRate{
			CurrencyCode: k.String(),
			Rate:         v.Rate,
		})
	}

	err = c.ratesStore.BulkUpdateRates(c.ctx, crs)
	if err != nil {
		return fmt.Errorf("failed to bulk update rates: %w", err)
	}

	return nil
}

// fetchFiatRates fetches the latest fiat currency rates from the exchange rates API.
func (c *Client) fetchFiatRates() (map[string]dto.CurrencyRate, error) {
	type response struct {
		Success bool               `json:"success"`
		Rates   map[string]float64 `json:"rates"`
	}

	// Construct the URL with the base currency and symbols.
	currencies := make([]string, 0, len(currencyDescriptions))
	for k := range currencyDescriptions {
		currencies = append(currencies, k.String())
	}
	symbols := strings.Join(currencies, ",")
	url := fmt.Sprintf("latest?base=%s&symbols=%s", c.baseCurrency, symbols)

	// Make the request.
	resp, err := c.fiatClient.R().Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch fiat rates: %w", err)
	}
	if resp.StatusCode() != 200 {
		return nil, fmt.Errorf("failed to fetch fiat rates: status %d body %v", resp.StatusCode(), resp.String())
	}
	r := response{}
	json.Unmarshal(resp.Body(), &r)

	// Convert fetched rates into dto.CurrencyRate map.
	rates := make(map[string]dto.CurrencyRate)
	for k, v := range r.Rates {
		rates[k] = dto.CurrencyRate{
			Description: currencyDescriptions[dto.CurrencyTicker(k)],
			Rate:        decimal.NewFromFloat(v),
		}
	}

	return rates, nil
}

// fetchCryptoRates fetches the latest cryptocurrency rates from the crypto compare API.
func (c *Client) fetchCryptoRates() (map[string]dto.CurrencyRate, error) {
	rates := make(map[string]float64)
	// Specify the cryptocurrencies you are interested in.
	cryptoCurrencies := []string{"BTC", "ETH"} // Example cryptocurrencies
	url := fmt.Sprintf("price?fsym=%s&tsyms=%s", c.baseCurrency, strings.Join(cryptoCurrencies, ","))

	// Make the request and directly check the error without storing the response object.
	resp, err := c.cryptoClient.R().SetResult(&rates).Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch crypto rates: %w", err)
	}
	if resp.IsError() {
		return nil, fmt.Errorf("error fetching crypto rates: status %s, body %s", resp.Status(), resp.String())
	}

	// Convert fetched rates into dto.CurrencyRate map.
	cryptoRates := make(map[string]dto.CurrencyRate)
	for k, v := range rates {
		cryptoRates[k] = dto.CurrencyRate{
			Description: currencyDescriptions[dto.CurrencyTicker(k)],
			Rate:        decimal.NewFromFloat(v),
		}
	}

	return cryptoRates, nil
}
