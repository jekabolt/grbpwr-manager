package rates

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
)

var (
	exchangeRatesBaseURL = "http://api.exchangeratesapi.io/v1/"
	cryptoCompareBaseURL = "https://min-api.cryptocompare.com/data/"

	baseCurrency = "EUR"
)

var currencyMap = map[string]dto.CurrencyRate{
	"BTC": {
		Description: "Bitcoin",
	},
	"CHF": {
		Description: "Swiss Franc",
	},
	"CNY": {
		Description: "Chinese Yuan",
	},
	"CZK": {
		Description: "Czech Republic Koruna",
	},
	"DKK": {
		Description: "Danish Krone",
	},
	"EUR": {
		Description: "Euro",
	},
	"ETH": {
		Description: "Ethereum",
	},
	"GBP": {
		Description: "British Pound Sterling",
	},
	"GEL": {
		Description: "Georgian Lari",
	},
	"HKD": {
		Description: "Hong Kong Dollar",
	},
	"HUF": {
		Description: "Hungarian Forint",
	},
	"ILS": {
		Description: "Israeli New Sheqel",
	},
	"JPY": {
		Description: "Japanese Yen",
	},
	"NOK": {
		Description: "Norwegian Krone",
	},
	"PLN": {
		Description: "Polish Zloty",
	},
	"RUB": {
		Description: "Russian Ruble",
	},
	"SEK": {
		Description: "Swedish Krona",
	},
	"SGD": {
		Description: "Singapore Dollar",
	},
	"TRY": {
		Description: "Turkish Lira",
	},
	"UAH": {
		Description: "Ukrainian Hryvnia",
	},
	"USD": {
		Description: "United States Dollar",
	},
}

type Config struct {
	ExchangeAPIKey    string        `mapstructure:"api_key"`
	RatesUpdatePeriod time.Duration `mapstructure:"rates_update_period"`
}

type Client struct {
	c      *Config
	cli    *resty.Client
	crypto *resty.Client
	rates  map[string]dto.CurrencyRate
	mu     sync.RWMutex
	stopCh chan struct{}
	doneCh chan struct{}
	ctx    context.Context
	cancel context.CancelFunc
}

func New(c *Config) *Client {
	cli := resty.New()
	cli.SetQueryParam("access_key", c.ExchangeAPIKey)
	cli.SetBaseURL(exchangeRatesBaseURL)
	cli.SetTimeout(10 * time.Second)

	cryptoCli := resty.New()
	cryptoCli.SetBaseURL(cryptoCompareBaseURL)
	cryptoCli.SetTimeout(10 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())

	return &Client{
		cli:    cli,
		crypto: cryptoCli,
		c:      c,
		rates:  make(map[string]dto.CurrencyRate),
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
}

func (cli *Client) Start() error {
	if err := cli.updateRates(); err != nil {
		return err
	}
	go func() {
		ticker := time.NewTicker(cli.c.RatesUpdatePeriod)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := cli.updateRates(); err != nil {
					fmt.Printf("could not update rates: %v", err)
				}
			case <-cli.ctx.Done():
				close(cli.doneCh)
				return
			}
		}
	}()
	return nil
}

func (cli *Client) Stop() {
	cli.cancel()
	<-cli.doneCh // wait for the goroutine to stop
}

func (cli *Client) GetRates() map[string]dto.CurrencyRate {
	cli.mu.RLock() // Read lock
	defer cli.mu.RUnlock()
	return cli.rates
}
func (cli *Client) updateRates() error {
	frm, err := cli.getFiatRates(baseCurrency)
	if err != nil {
		return fmt.Errorf("could not get fiat rates: %w", err)
	}
	crm, err := cli.getCryptoRates(baseCurrency, []string{"BTC", "ETH"})
	if err != nil {
		return fmt.Errorf("could not get crypto rates: %w", err)
	}

	cli.mu.Lock()
	defer cli.mu.Unlock()
	cli.rates = mergeMaps(frm, crm)
	return nil
}

type GetLatestRatesResponse struct {
	Success   bool               `json:"success"`
	Timestamp int64              `json:"timestamp"`
	Base      string             `json:"base"`
	Date      string             `json:"date"`
	Rates     map[string]float64 `json:"rates"`
}

func (client *Client) getFiatRates(baseCurrency string) (map[string]decimal.Decimal, error) {
	var currencies []string
	for currency := range currencyMap {
		currencies = append(currencies, currency)
	}
	symbols := strings.Join(currencies, ",")

	url := fmt.Sprintf("latest?base=%s&symbols=%s", baseCurrency, symbols)
	resp, err := client.cli.R().Get(url)
	if err != nil {
		return nil, err
	}

	var res GetLatestRatesResponse
	if err := json.Unmarshal(resp.Body(), &res); err != nil {
		return nil, fmt.Errorf("could not unmarshal response fiat: %w : body: %v", err, resp.String())
	}

	if !res.Success {
		return nil, fmt.Errorf("fiat api request failed: %v", resp.String())
	}

	return floatMapToDecimal(res.Rates), nil
}

func (client *Client) getCryptoRates(fsym string, tsyms []string) (map[string]decimal.Decimal, error) {
	url := fmt.Sprintf("price?fsym=%s&tsyms=%s", fsym, strings.Join(tsyms, ","))
	resp, err := client.crypto.R().Get(url)
	if err != nil {
		return nil, err
	}
	var rates map[string]float64
	if err := json.Unmarshal(resp.Body(), &rates); err != nil {
		return nil, fmt.Errorf("could not unmarshal response crypto: %w : body: %v", err, resp.String())
	}

	return floatMapToDecimal(rates), nil
}

func floatMapToDecimal(m map[string]float64) map[string]decimal.Decimal {
	res := make(map[string]decimal.Decimal)
	for k, v := range m {
		res[k] = decimal.NewFromFloat(v)
	}
	return res
}

// update currencyMap with rates from the API
func mergeMaps(maps ...map[string]decimal.Decimal) map[string]dto.CurrencyRate {
	res := make(map[string]dto.CurrencyRate)
	for _, m := range maps {
		for k, v := range m {
			if cr, ok := currencyMap[k]; ok {
				cr.Rate = v
				res[k] = cr
			}
		}
	}
	return res
}
