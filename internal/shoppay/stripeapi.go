package shoppay

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/client"
)

// StripeAPI is what the shop payments ask Stripe outside a team's processor: the Connect OAuth
// exchange and deauthorization, a connected account's state, and the account behind a team's own key.
type StripeAPI interface {
	// ExchangeCode turns an OAuth code into the connected account (acct_…) and its mode.
	ExchangeCode(ctx context.Context, code string) (account string, livemode bool, err error)
	// Deauthorize disconnects the account from the platform.
	Deauthorize(ctx context.Context, account string) error
	// ConnectedAccount reads a connected account with the platform key.
	ConnectedAccount(ctx context.Context, account string) (platformstore.StripeAccountState, error)
	// OwnAccount reads the account behind a team's own secret key: ErrBadSecretKey when Stripe
	// refuses the key, AccountID "" when a restricted key may not read its account.
	OwnAccount(ctx context.Context, secretKey string) (platformstore.StripeAccountState, error)
}

// ErrBadSecretKey: Stripe refused the key (401).
var ErrBadSecretKey = errors.New("shoppay: stripe refused the secret key")

// ErrStripeUnavailable: Stripe did not answer usefully (network, 5xx, rate limit).
var ErrStripeUnavailable = errors.New("shoppay: stripe unavailable")

type liveStripe struct {
	clientID    string
	platformKey string
	backends    *stripe.Backends
}

// NewStripeAPI is the real Stripe. backends overrides Stripe's endpoints (tests); nil = Stripe.
func NewStripeAPI(cfg Config, backends *stripe.Backends) StripeAPI {
	return &liveStripe{clientID: strings.TrimSpace(cfg.ConnectClientID), platformKey: strings.TrimSpace(cfg.ConnectSecretKey), backends: backends}
}

func (l *liveStripe) api() stripe.Backend {
	if l.backends != nil && l.backends.API != nil {
		return l.backends.API
	}
	return stripe.GetBackend(stripe.APIBackend)
}

func (l *liveStripe) connect() stripe.Backend {
	if l.backends != nil && l.backends.Connect != nil {
		return l.backends.Connect
	}
	return stripe.GetBackend(stripe.ConnectBackend)
}

func (l *liveStripe) ExchangeCode(ctx context.Context, code string) (string, bool, error) {
	if l.platformKey == "" {
		return "", false, errors.New("shoppay: no platform key")
	}
	// ClientSecret is always set: stripe-go would otherwise send the process-wide stripe.Key.
	p := &stripe.OAuthTokenParams{Params: stripe.Params{Context: ctx}, GrantType: stripe.String("authorization_code"),
		Code: stripe.String(code), ClientSecret: stripe.String(l.platformKey)}
	tok := &stripe.OAuthToken{}
	if err := l.connect().Call(http.MethodPost, "/oauth/token", l.platformKey, p, tok); err != nil {
		return "", false, err
	}
	if !strings.HasPrefix(tok.StripeUserID, "acct_") {
		return "", false, errors.New("shoppay: oauth token without an account")
	}
	return tok.StripeUserID, tok.Livemode, nil
}

func (l *liveStripe) Deauthorize(ctx context.Context, account string) error {
	if l.platformKey == "" || l.clientID == "" {
		return errors.New("shoppay: connect is not configured")
	}
	p := &stripe.DeauthorizeParams{Params: stripe.Params{Context: ctx}, ClientID: stripe.String(l.clientID), StripeUserID: stripe.String(account)}
	return l.connect().Call(http.MethodPost, "/oauth/deauthorize", l.platformKey, p, &stripe.Deauthorize{})
}

func (l *liveStripe) ConnectedAccount(ctx context.Context, account string) (platformstore.StripeAccountState, error) {
	a := &stripe.Account{}
	if err := l.api().Call(http.MethodGet, stripe.FormatURLPath("/v1/accounts/%s", account), l.platformKey,
		&stripe.AccountParams{Params: stripe.Params{Context: ctx}}, a); err != nil {
		return platformstore.StripeAccountState{}, stripeErr(err)
	}
	return accountState(a, keyMode(l.platformKey)), nil
}

func (l *liveStripe) OwnAccount(ctx context.Context, secretKey string) (platformstore.StripeAccountState, error) {
	mode := keyMode(secretKey)
	a := &stripe.Account{}
	err := l.api().Call(http.MethodGet, "/v1/account", secretKey, &stripe.Params{Context: ctx}, a)
	if err == nil {
		return accountState(a, mode), nil
	}
	var se *stripe.Error
	if !errors.As(err, &se) || se.HTTPStatusCode != http.StatusForbidden {
		return platformstore.StripeAccountState{}, stripeErr(err)
	}
	// A restricted key without account access: it is still a working key when it can read payments.
	sc := client.New(secretKey, &stripe.Backends{API: l.api(), Connect: l.connect(), Uploads: l.api()})
	it := sc.PaymentIntents.List(&stripe.PaymentIntentListParams{ListParams: stripe.ListParams{Context: ctx, Limit: stripe.Int64(1), Single: true}})
	it.Next()
	if err := it.Err(); err != nil {
		return platformstore.StripeAccountState{}, stripeErr(err)
	}
	return platformstore.StripeAccountState{Mode: mode, ChargesEnabled: true}, nil
}

func stripeErr(err error) error {
	var se *stripe.Error
	if errors.As(err, &se) {
		switch se.HTTPStatusCode {
		case http.StatusUnauthorized:
			return ErrBadSecretKey
		case http.StatusForbidden, http.StatusNotFound, http.StatusBadRequest:
			return err
		}
	}
	return errors.Join(ErrStripeUnavailable, err)
}

// accountState is a Stripe account as the team's row keeps it: never an email or a person's name
// beyond the account's own display name.
func accountState(a *stripe.Account, mode string) platformstore.StripeAccountState {
	st := platformstore.StripeAccountState{AccountID: a.ID, Mode: mode, ChargesEnabled: a.ChargesEnabled,
		PayoutsEnabled: a.PayoutsEnabled, DetailsSubmitted: a.DetailsSubmitted}
	if a.Settings != nil && a.Settings.Dashboard != nil {
		st.Name = strings.TrimSpace(a.Settings.Dashboard.DisplayName)
	}
	if st.Name == "" && a.BusinessProfile != nil {
		st.Name = strings.TrimSpace(a.BusinessProfile.Name)
	}
	return st
}
