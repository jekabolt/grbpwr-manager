package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/preorderpayment"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
	"github.com/stripe/stripe-go/v79/form"
)

// A team shop's card processor (X18-L3). The same Processor as the storefront's, built per team:
// Stripe Connect (direct charges on the team's connected account: the platform key plus the
// Stripe-Account header, and an application fee from fee_bps) or the team's own secret key. New
// (the storefront team's processors, from env keys) is not touched by any of this.

// TeamOptions say how a team processor reaches Stripe.
type TeamOptions struct {
	// Account is the connected account (acct_…) for Stripe Connect; "" = the team's own key in Config.
	Account string
	// FeeBps is the platform fee on a Connect charge, in basis points of the amount (0 = none).
	FeeBps int
	// MonitorBase is the parent of the processor's payment monitors: it must carry the team
	// (tenant.With), since a monitor outlives the request that started it.
	MonitorBase context.Context
	// Backends overrides Stripe's endpoints (tests); nil = Stripe.
	Backends *stripe.Backends
	// Reservations releases checkout holds (nil = none). Set before the restarted monitors run.
	Reservations dependency.StockReservationManager
}

// NewTeam builds a team's card processor. ctx must carry the team: the payment method and the
// unpaid orders whose monitors restart are read from the team's database.
func NewTeam(ctx context.Context, c *Config, rep dependency.Repository, m dependency.Mailer, pmn entity.PaymentMethodName, o TeamOptions) (*Processor, error) {
	if c == nil || c.SecretKey == "" {
		return nil, errors.New("stripe: team processor needs a secret key")
	}
	if o.MonitorBase == nil {
		return nil, errors.New("stripe: team processor needs a monitor base")
	}
	if t, ok := tenant.From(o.MonitorBase); !ok || t.ID <= 0 {
		return nil, errors.New("stripe: team processor monitor base carries no team")
	}
	if o.FeeBps < 0 || o.FeeBps > 10000 {
		return nil, fmt.Errorf("stripe: fee %d bps out of range", o.FeeBps)
	}
	if cache.GetBaseCurrency() == "" {
		return nil, errors.New("base currency not configured")
	}
	pm, ok := cache.GetPaymentMethodByName(ctx, pmn)
	if !ok {
		return nil, errors.New("payment method not found")
	}
	if !isPaymentMethodCard(pm.Method) {
		return nil, errors.New("payment method is not valid for card")
	}
	ttl := c.InvoiceExpiration
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	monParentCtx, monParentCancel := context.WithCancel(o.MonitorBase)
	p := &Processor{
		c:               c,
		mailer:          m,
		stripeClient:    client.New(c.SecretKey, teamBackends(o)),
		rep:             rep,
		pm:              pm.Method,
		monCtxt:         make(map[string]monEntry),
		preOrderStore:   preorderpayment.NewStore(ttl),
		monParentCtx:    monParentCtx,
		monParentCancel: monParentCancel,
		reservationMgr:  o.Reservations,
	}
	if err := p.initAddressesFromUnpaidOrders(ctx); err != nil {
		p.StopAllMonitors(ctx)
		return nil, fmt.Errorf("can't init addresses from unpaid orders: %w", err)
	}
	return p, nil
}

// teamBackends is Stripe's endpoints for a team: with a connected account, every API and upload
// call carries its Stripe-Account header (and a payment intent its fee).
func teamBackends(o TeamOptions) *stripe.Backends {
	base := o.Backends
	if base == nil {
		base = &stripe.Backends{
			API:     stripe.GetBackend(stripe.APIBackend),
			Connect: stripe.GetBackend(stripe.ConnectBackend),
			Uploads: stripe.GetBackend(stripe.UploadsBackend),
		}
	}
	if o.Account == "" {
		return base
	}
	return &stripe.Backends{
		API:     &accountBackend{inner: base.API, account: o.Account, feeBps: o.FeeBps},
		Connect: base.Connect,
		Uploads: &accountBackend{inner: base.Uploads, account: o.Account},
	}
}

// accountBackend sends every call on behalf of one connected account (direct charges).
type accountBackend struct {
	inner   stripe.Backend
	account string
	feeBps  int
}

var _ stripe.Backend = (*accountBackend)(nil)

func isNilParams(params stripe.ParamsContainer) bool {
	if params == nil {
		return true
	}
	v := reflect.ValueOf(params)
	return v.Kind() == reflect.Ptr && v.IsNil()
}

func (b *accountBackend) withAccount(params stripe.ParamsContainer) stripe.ParamsContainer {
	if isNilParams(params) {
		params = &stripe.Params{}
	}
	params.GetParams().SetStripeAccount(b.account)
	return params
}

// fee sets the application fee of a payment intent create or update that names an amount.
func (b *accountBackend) fee(method, path string, params stripe.ParamsContainer) {
	if b.feeBps <= 0 || method != http.MethodPost {
		return
	}
	if path != "/v1/payment_intents" && !(strings.HasPrefix(path, "/v1/payment_intents/") && strings.Count(path, "/") == 3) {
		return
	}
	pi, ok := params.(*stripe.PaymentIntentParams)
	if !ok || pi == nil || pi.Amount == nil || *pi.Amount <= 0 {
		return
	}
	pi.ApplicationFeeAmount = stripe.Int64(ApplicationFee(*pi.Amount, b.feeBps))
}

// ApplicationFee is bps basis points of amount (smallest currency unit), rounded down.
func ApplicationFee(amount int64, bps int) int64 {
	if amount <= 0 || bps <= 0 {
		return 0
	}
	return amount * int64(bps) / 10000
}

func (b *accountBackend) Call(method, path, key string, params stripe.ParamsContainer, v stripe.LastResponseSetter) error {
	params = b.withAccount(params)
	b.fee(method, path, params)
	return b.inner.Call(method, path, key, params, v)
}

func (b *accountBackend) CallStreaming(method, path, key string, params stripe.ParamsContainer, v stripe.StreamingLastResponseSetter) error {
	return b.inner.CallStreaming(method, path, key, b.withAccount(params), v)
}

func (b *accountBackend) CallRaw(method, path, key string, body *form.Values, params *stripe.Params, v stripe.LastResponseSetter) error {
	if params == nil {
		params = &stripe.Params{}
	}
	params.SetStripeAccount(b.account)
	return b.inner.CallRaw(method, path, key, body, params, v)
}

func (b *accountBackend) CallMultipart(method, path, key, boundary string, body *bytes.Buffer, params *stripe.Params, v stripe.LastResponseSetter) error {
	if params == nil {
		params = &stripe.Params{}
	}
	params.SetStripeAccount(b.account)
	return b.inner.CallMultipart(method, path, key, boundary, body, params, v)
}

func (b *accountBackend) SetMaxNetworkRetries(n int64) { b.inner.SetMaxNetworkRetries(n) }

// DispatchEvent handles one verified Stripe event of this team's processor (the caller verified
// the signature and bound ctx to the team) and answers the HTTP status for Stripe: 200 handled or
// ignored, 400 a malformed payload, 500 a transient failure Stripe should retry. Same handling as
// the storefront's HandleStripeEvent: a succeeded payment intent confirms its order (idempotent,
// underpayment-guarded), a dispute is booked.
func (p *Processor) DispatchEvent(ctx context.Context, event stripe.Event) int {
	switch event.Type {
	case stripe.EventTypePaymentIntentSucceeded:
		var pi stripe.PaymentIntent
		if event.Data == nil || json.Unmarshal(event.Data.Raw, &pi) != nil {
			slog.Default().ErrorContext(ctx, "team stripe webhook: can't parse payment_intent", slog.String("event_id", event.ID))
			return http.StatusBadRequest
		}
		if err := p.confirmPaymentFromWebhook(ctx, &pi); err != nil {
			slog.Default().ErrorContext(ctx, "team stripe webhook: can't confirm payment",
				slog.String("payment_intent_id", pi.ID), slog.String("err", err.Error()))
			return http.StatusInternalServerError
		}
	case stripe.EventTypeChargeDisputeCreated, stripe.EventTypeChargeDisputeClosed:
		if event.Data == nil {
			return http.StatusOK
		}
		if err := p.recordDisputeFromWebhook(ctx, event.Data.Raw, event.Type == stripe.EventTypeChargeDisputeClosed); err != nil {
			slog.Default().ErrorContext(ctx, "team stripe webhook: can't record dispute",
				slog.String("event_id", event.ID), slog.String("err", err.Error()))
			return http.StatusInternalServerError
		}
	}
	return http.StatusOK
}

// PaymentMethod is the payment method this processor charges as (card or card-test).
func (p *Processor) PaymentMethod() entity.PaymentMethodName { return p.pm.Name }
