package fxsync

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// A trimmed but structurally faithful copy of the ECB eurofxref-daily.xml feed (single-quoted attrs
// and the gesmes/eurofxref namespaces, exactly as ECB serves it).
const sampleECBXML = `<?xml version="1.0" encoding="UTF-8"?>
<gesmes:Envelope xmlns:gesmes="http://www.gesmes.org/xml/2002-08-01" xmlns="http://www.ecb.int/vocabulary/2002-08-01/eurofxref">
	<gesmes:subject>Reference rates</gesmes:subject>
	<gesmes:Sender><gesmes:name>European Central Bank</gesmes:name></gesmes:Sender>
	<Cube>
		<Cube time='2026-07-17'>
			<Cube currency='USD' rate='1.1435'/>
			<Cube currency='JPY' rate='185.65'/>
			<Cube currency='GBP' rate='0.85098'/>
			<Cube currency='CNY' rate='7.7501'/>
			<Cube currency='KRW' rate='1698.46'/>
			<Cube currency='PLN' rate='4.3473'/>
		</Cube>
	</Cube>
</gesmes:Envelope>`

func rateFor(rows []entity.CostingFxRate, cur string) (decimal.Decimal, bool) {
	for _, r := range rows {
		if r.Currency == cur {
			return r.RateToBase, true
		}
	}
	return decimal.Zero, false
}

func approx(t *testing.T, name string, got, want, tol decimal.Decimal) {
	t.Helper()
	if got.Sub(want).Abs().GreaterThan(tol) {
		t.Errorf("%s: got %s, want ~%s (±%s)", name, got.String(), want.String(), tol.String())
	}
}

func TestParseECB(t *testing.T) {
	snap, err := parseECB([]byte(sampleECBXML))
	if err != nil {
		t.Fatalf("parseECB: %v", err)
	}
	if got := snap.date.Format(ecbDateLayout); got != "2026-07-17" {
		t.Errorf("date: got %q, want 2026-07-17", got)
	}
	// EUR is injected as the base unit; the six quoted currencies are present.
	if len(snap.perEUR) != 7 {
		t.Errorf("perEUR size: got %d, want 7", len(snap.perEUR))
	}
	if !snap.perEUR["EUR"].Equal(decimal.NewFromInt(1)) {
		t.Errorf("EUR should be 1, got %s", snap.perEUR["EUR"])
	}
	if !snap.perEUR["USD"].Equal(decimal.RequireFromString("1.1435")) {
		t.Errorf("USD perEUR: got %s, want 1.1435", snap.perEUR["USD"])
	}
}

func TestToBaseRates_EURBase(t *testing.T) {
	snap, err := parseECB([]byte(sampleECBXML))
	if err != nil {
		t.Fatalf("parseECB: %v", err)
	}
	rows, err := snap.toBaseRates("EUR")
	if err != nil {
		t.Fatalf("toBaseRates: %v", err)
	}

	// The base currency itself is never stored.
	if _, ok := rateFor(rows, "EUR"); ok {
		t.Errorf("EUR must be omitted from base-relative rows")
	}

	// For an EUR base, rate_to_base(X) is EUR per 1 X, i.e. 1 / ecbRate(X). Invariant check that is
	// independent of the conversion code: rate_to_base(X) * ecbRate(X) ≈ 1. The slack scales with
	// ecbRate(X) because rate_to_base is stored at 8 dp (KRW ~1698 amplifies the rounding to ~1e-5).
	one := decimal.NewFromInt(1)
	invTol := decimal.RequireFromString("0.0001")
	for cur, ecb := range map[string]string{
		"USD": "1.1435", "JPY": "185.65", "GBP": "0.85098",
		"CNY": "7.7501", "KRW": "1698.46", "PLN": "4.3473",
	} {
		r, ok := rateFor(rows, cur)
		if !ok {
			t.Fatalf("missing rate for %s", cur)
		}
		approx(t, cur+"*ecb", r.Mul(decimal.RequireFromString(ecb)), one, invTol)
	}

	// USDT is pegged 1:1 to USD (ECB carries no crypto).
	usd, _ := rateFor(rows, "USD")
	usdt, ok := rateFor(rows, "USDT")
	if !ok {
		t.Fatalf("USDT peg missing")
	}
	if !usdt.Equal(usd) {
		t.Errorf("USDT peg: got %s, want == USD %s", usdt, usd)
	}

	// EUR omitted, six fiat + USDT peg.
	if len(rows) != 7 {
		t.Errorf("row count: got %d, want 7", len(rows))
	}
	// All rows carry the ECB reference date.
	for _, r := range rows {
		if r.ValidFrom.Format(ecbDateLayout) != "2026-07-17" {
			t.Errorf("%s valid_from: got %s", r.Currency, r.ValidFrom)
		}
	}
}

func TestToBaseRates_NonEURBase(t *testing.T) {
	snap, err := parseECB([]byte(sampleECBXML))
	if err != nil {
		t.Fatalf("parseECB: %v", err)
	}
	rows, err := snap.toBaseRates("USD")
	if err != nil {
		t.Fatalf("toBaseRates(USD): %v", err)
	}
	if _, ok := rateFor(rows, "USD"); ok {
		t.Errorf("USD (base) must be omitted")
	}
	// rate_to_base(EUR) in a USD base is USD-per-EUR = ecbRate(USD) = 1.1435.
	eur, ok := rateFor(rows, "EUR")
	if !ok {
		t.Fatalf("missing EUR rate under USD base")
	}
	approx(t, "EUR under USD base", eur, decimal.RequireFromString("1.1435"), decimal.RequireFromString("0.00000001"))
}

func TestToBaseRates_MissingBase(t *testing.T) {
	snap, err := parseECB([]byte(sampleECBXML))
	if err != nil {
		t.Fatalf("parseECB: %v", err)
	}
	if _, err := snap.toBaseRates("ZZZ"); err == nil {
		t.Errorf("expected error for base currency absent from feed")
	}
}

func TestRatesToBase_HTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(sampleECBXML))
	}))
	defer srv.Close()

	c := newECBClient(srv.URL, 0)
	rows, err := c.RatesToBase(context.Background(), "EUR")
	if err != nil {
		t.Fatalf("RatesToBase: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("row count: got %d, want 7", len(rows))
	}
	if _, ok := rateFor(rows, "USDT"); !ok {
		t.Errorf("expected USDT peg from live fetch")
	}
}

func TestRatesToBase_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := newECBClient(srv.URL, 0)
	if _, err := c.RatesToBase(context.Background(), "EUR"); err == nil {
		t.Errorf("expected error on http 500")
	}
}
