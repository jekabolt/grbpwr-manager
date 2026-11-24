package stripe

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	stripego "github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
)

type seenCall struct {
	method, path, account, auth string
	form                        url.Values
}

// fakeStripe answers every call with a minimal payment intent and records what it got.
func fakeStripe(t *testing.T) (*stripego.Backends, func() []seenCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []seenCall
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f, _ := url.ParseQuery(string(body))
		mu.Lock()
		calls = append(calls, seenCall{method: r.Method, path: r.URL.Path, account: r.Header.Get("Stripe-Account"),
			auth: r.Header.Get("Authorization"), form: f})
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "pi_1", "object": "payment_intent", "amount": 1000, "currency": "eur",
			"client_secret": "pi_1_secret_x", "status": "requires_payment_method"})
	}))
	t.Cleanup(srv.Close)
	b := stripego.GetBackendWithConfig(stripego.APIBackend, &stripego.BackendConfig{
		URL: stripego.String(srv.URL), HTTPClient: srv.Client(), MaxNetworkRetries: stripego.Int64(0),
		LeveledLogger: &stripego.LeveledLogger{Level: stripego.LevelNull},
	})
	return &stripego.Backends{API: b, Connect: b, Uploads: b}, func() []seenCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]seenCall(nil), calls...)
	}
}

// TestConnectCallsCarryTheAccountAndFee: a Connect processor's calls all carry the connected
// account, a payment intent create/update with an amount carries the fee, a call without params
// still carries the account; own keys send no Stripe-Account at all.
func TestConnectCallsCarryTheAccountAndFee(t *testing.T) {
	backends, seen := fakeStripe(t)
	sc := client.New("sk_test_platform", teamBackends(TeamOptions{Account: "acct_A", FeeBps: 250, Backends: backends}))

	if _, err := sc.PaymentIntents.New(&stripego.PaymentIntentParams{Amount: stripego.Int64(1000), Currency: stripego.String("eur")}); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.PaymentIntents.Update("pi_1", &stripego.PaymentIntentParams{Amount: stripego.Int64(2001)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.PaymentIntents.Get("pi_1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.PaymentIntents.Cancel("pi_1", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := sc.Refunds.New(&stripego.RefundParams{PaymentIntent: stripego.String("pi_1")}); err != nil {
		t.Fatal(err)
	}
	calls := seen()
	if len(calls) != 5 {
		t.Fatalf("calls = %d", len(calls))
	}
	for _, c := range calls {
		if c.account != "acct_A" {
			t.Fatalf("%s %s without the connected account: %q", c.method, c.path, c.account)
		}
	}
	if got := calls[0].form.Get("application_fee_amount"); got != "25" {
		t.Fatalf("create fee = %q", got)
	}
	if got := calls[1].form.Get("application_fee_amount"); got != "50" {
		t.Fatalf("update fee = %q", got)
	}
	if calls[4].form.Has("application_fee_amount") {
		t.Fatal("a refund carries an application fee")
	}

	// No fee configured: no fee field. Own keys: no account header.
	backends2, seen2 := fakeStripe(t)
	sc = client.New("sk_test_platform", teamBackends(TeamOptions{Account: "acct_B", Backends: backends2}))
	if _, err := sc.PaymentIntents.New(&stripego.PaymentIntentParams{Amount: stripego.Int64(1000)}); err != nil {
		t.Fatal(err)
	}
	sc = client.New("sk_test_own", teamBackends(TeamOptions{Backends: backends2}))
	if _, err := sc.PaymentIntents.New(&stripego.PaymentIntentParams{Amount: stripego.Int64(1000)}); err != nil {
		t.Fatal(err)
	}
	calls = seen2()
	if calls[0].account != "acct_B" || calls[0].form.Has("application_fee_amount") {
		t.Fatalf("fee-less connect call %+v", calls[0])
	}
	if calls[1].account != "" {
		t.Fatalf("own keys sent a connected account %q", calls[1].account)
	}
}

func TestApplicationFee(t *testing.T) {
	for _, c := range []struct {
		amount int64
		bps    int
		want   int64
	}{{1000, 0, 0}, {1000, 250, 25}, {999, 100, 9}, {0, 500, 0}, {1000, 10000, 1000}} {
		if got := ApplicationFee(c.amount, c.bps); got != c.want {
			t.Fatalf("fee(%d, %d) = %d, want %d", c.amount, c.bps, got, c.want)
		}
	}
}

// TestDispatchEvent: a succeeded payment intent confirms its order through the shared path; an
// order this team's database does not know is acknowledged and changes nothing; a bad payload is
// a 400; other events are acknowledged.
func TestDispatchEvent(t *testing.T) {
	orders := mocks.NewMockOrder(t)
	repo := mocks.NewMockRepository(t)
	repo.EXPECT().Order().Return(orders).Maybe()
	payment := &entity.Payment{PaymentInsert: entity.PaymentInsert{TransactionAmountPaymentCurrency: decimal.NewFromInt(10)}}
	orders.EXPECT().GetPaymentByOrderUUID(mock.Anything, "order-mine").Return(payment, nil)
	orders.EXPECT().OrderPaymentDone(mock.Anything, "order-mine", mock.Anything).Return(false, nil)
	orders.EXPECT().GetPaymentByOrderUUID(mock.Anything, "order-elsewhere").Return(nil, sql.ErrNoRows)
	p := &Processor{c: &Config{}, rep: repo, monCtxt: make(map[string]monEntry)}

	ev := func(typ stripego.EventType, raw string) stripego.Event {
		return stripego.Event{ID: "evt_1", Type: typ, Data: &stripego.EventData{Raw: json.RawMessage(raw)}}
	}
	ctx := context.Background()
	if code := p.DispatchEvent(ctx, ev(stripego.EventTypePaymentIntentSucceeded,
		`{"id":"pi_1","amount_received":1000,"currency":"eur","metadata":{"order_id":"order-mine"}}`)); code != http.StatusOK {
		t.Fatalf("own order: %d", code)
	}
	if code := p.DispatchEvent(ctx, ev(stripego.EventTypePaymentIntentSucceeded,
		`{"id":"pi_2","amount_received":1000,"currency":"eur","metadata":{"order_id":"order-elsewhere"}}`)); code != http.StatusOK {
		t.Fatalf("unknown order: %d", code)
	}
	if code := p.DispatchEvent(ctx, ev(stripego.EventTypePaymentIntentSucceeded, `{"id":`)); code != http.StatusBadRequest {
		t.Fatalf("bad payload: %d", code)
	}
	if code := p.DispatchEvent(ctx, ev("customer.created", `{}`)); code != http.StatusOK {
		t.Fatalf("other event: %d", code)
	}
}
