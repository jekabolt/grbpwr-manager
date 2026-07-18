package fxsync

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

const (
	// defaultSourceURL is the ECB euro foreign-exchange reference rates (daily) feed. It is EUR-based,
	// free, keyless, and published every TARGET working day (~16:00 CET); on non-working days it
	// carries the last working day's rates dated to that day.
	defaultSourceURL   = "https://www.ecb.europa.eu/stats/eurofxref/eurofxref-daily.xml"
	defaultHTTPTimeout = 15 * time.Second
	maxRespBody        = 1 << 20 // 1 MiB — the daily feed is ~2 KiB; this only bounds a pathological body.
	ecbDateLayout      = "2006-01-02"
	// rateScale mirrors costing_fx_rate.rate_to_base DECIMAL(18,8); rates are rounded to it so the
	// stored value is exactly what we computed (MySQL would otherwise round on insert).
	rateScale = 8
)

// ecbClient fetches the ECB euro reference rates. baseURL is injectable so tests can point it at a
// fake server.
type ecbClient struct {
	url  string
	http *http.Client
}

func newECBClient(url string, timeout time.Duration) *ecbClient {
	if strings.TrimSpace(url) == "" {
		url = defaultSourceURL
	}
	if timeout <= 0 {
		timeout = defaultHTTPTimeout
	}
	return &ecbClient{url: url, http: &http.Client{Timeout: timeout}}
}

// ecbEnvelope maps the ECB XML: Envelope > Cube > Cube[time] > Cube[currency,rate]. Element names
// match on local name, so the gesmes/eurofxref namespaces are irrelevant.
type ecbEnvelope struct {
	Days []struct {
		Time  string `xml:"time,attr"`
		Rates []struct {
			Currency string `xml:"currency,attr"`
			Rate     string `xml:"rate,attr"`
		} `xml:"Cube"`
	} `xml:"Cube>Cube"`
}

// ecbSnapshot is one day's EUR-based rates: perEUR[X] = units of X per 1 EUR (EUR itself = 1).
type ecbSnapshot struct {
	date   time.Time
	perEUR map[string]decimal.Decimal
}

// RatesToBase fetches the ECB feed and expresses each currency as base-currency-per-unit, ready to
// upsert into costing_fx_rate.
func (c *ecbClient) RatesToBase(ctx context.Context, base string) ([]entity.CostingFxRate, error) {
	snap, err := c.fetch(ctx)
	if err != nil {
		return nil, err
	}
	return snap.toBaseRates(base)
}

func (c *ecbClient) fetch(ctx context.Context) (ecbSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return ecbSnapshot{}, fmt.Errorf("fxsync: build request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return ecbSnapshot{}, fmt.Errorf("fxsync: request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return ecbSnapshot{}, fmt.Errorf("fxsync: ecb http status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return ecbSnapshot{}, fmt.Errorf("fxsync: read response: %w", err)
	}
	return parseECB(raw)
}

func parseECB(raw []byte) (ecbSnapshot, error) {
	var env ecbEnvelope
	if err := xml.Unmarshal(raw, &env); err != nil {
		return ecbSnapshot{}, fmt.Errorf("fxsync: decode ecb xml: %w", err)
	}
	if len(env.Days) == 0 {
		return ecbSnapshot{}, fmt.Errorf("fxsync: ecb feed had no daily cube")
	}
	day := env.Days[0] // the daily feed carries exactly one day; take the first (latest).
	date, err := time.Parse(ecbDateLayout, strings.TrimSpace(day.Time))
	if err != nil {
		return ecbSnapshot{}, fmt.Errorf("fxsync: parse ecb date %q: %w", day.Time, err)
	}
	perEUR := make(map[string]decimal.Decimal, len(day.Rates)+1)
	perEUR["EUR"] = decimal.NewFromInt(1)
	for _, r := range day.Rates {
		cur := strings.ToUpper(strings.TrimSpace(r.Currency))
		if cur == "" {
			continue
		}
		rate, err := decimal.NewFromString(strings.TrimSpace(r.Rate))
		if err != nil || !rate.IsPositive() {
			continue
		}
		perEUR[cur] = rate
	}
	if len(perEUR) <= 1 {
		return ecbSnapshot{}, fmt.Errorf("fxsync: ecb feed had no usable rates")
	}
	return ecbSnapshot{date: date, perEUR: perEUR}, nil
}

// toBaseRates converts the EUR-based snapshot into base-currency-per-unit rows. The base currency
// itself is omitted (the cost fold treats base→base as 1). USDT is pegged 1:1 to USD (a USD
// stablecoin; ECB carries no crypto) so multi-currency costing booked in USDT still folds.
func (s ecbSnapshot) toBaseRates(base string) ([]entity.CostingFxRate, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	if base == "" {
		base = "EUR"
	}
	perBase, ok := s.perEUR[base]
	if !ok || !perBase.IsPositive() {
		return nil, fmt.Errorf("fxsync: base currency %q not present in ecb feed", base)
	}
	out := make([]entity.CostingFxRate, 0, len(s.perEUR))
	for cur, perEUR := range s.perEUR {
		if cur == base || !perEUR.IsPositive() {
			continue
		}
		// Both are quoted per 1 EUR, so 1 cur = (perBase / perEUR) base. Skip any rate that rounds
		// away to zero at the stored scale — the column's rate_to_base > 0 CHECK would reject it and
		// fail the whole upsert.
		if rate := perBase.DivRound(perEUR, rateScale); rate.IsPositive() {
			out = append(out, entity.CostingFxRate{Currency: cur, RateToBase: rate, ValidFrom: s.date})
		}
	}
	// USDT is pegged 1:1 to USD (ECB carries no crypto) so costing booked in USDT still folds.
	if _, quoted := s.perEUR["USDT"]; !quoted && base != "USDT" {
		if usd, ok := s.perEUR["USD"]; ok && usd.IsPositive() {
			if rate := perBase.DivRound(usd, rateScale); rate.IsPositive() {
				out = append(out, entity.CostingFxRate{Currency: "USDT", RateToBase: rate, ValidFrom: s.date})
			}
		}
	}
	return out, nil
}
