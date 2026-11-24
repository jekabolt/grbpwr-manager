package shoppay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Store is the control plane the shop payments read and write (platformstore.Store).
type Store interface {
	TeamStripe(ctx context.Context, teamID int64) (platformstore.TeamStripe, bool, error)
	TeamStripeByAccount(ctx context.Context, accountID string) (platformstore.TeamStripe, bool, error)
	LiveStripeTeams(ctx context.Context, kind string) ([]int64, error)
	ConnectTeamStripe(ctx context.Context, teamID int64, a platformstore.StripeAccountState, now time.Time) error
	SetTeamStripeKeys(ctx context.Context, teamID int64, k platformstore.TeamStripeKeys, now time.Time) error
	SyncTeamStripe(ctx context.Context, teamID int64, a platformstore.StripeAccountState) (bool, error)
	DisconnectTeamStripe(ctx context.Context, teamID int64, onlyAccount string, now time.Time) (bool, error)
	Membership(ctx context.Context, userID, teamID int64) (platformstore.MemberTeam, bool, error)
	AuditAuth(ctx context.Context, actorUserID, teamID int64, action string, detail map[string]string, ip string) error
}

// Teams resolves teams (tenantbind.Binder).
type Teams interface {
	TeamByID(ctx context.Context, id int64) (tenant.Team, error)
	SchemaBehind(t tenant.Team) bool
	Operator(ctx context.Context, userID int64) (bool, error)
}

// Deps wire a Service.
type Deps struct {
	Config Config
	// APIBase is the scheme and host of the API (platformstore.Config.APIBase): the Connect redirect
	// and the webhook addresses live there.
	APIBase string
	// BaseDomain is PLATFORM_BASE_DOMAIN: a team's admin is https://<slug>.<BaseDomain>.
	BaseDomain string
	Store      Store
	Teams      Teams
	Factory    Factory
	// Stripe is nil = the real Stripe with Config's platform key.
	Stripe StripeAPI
	// Changed runs after a team's payments changed (optional): drop this instance's cached answers
	// about the shop and redraw its profile (the storefront reads the Stripe account / key there).
	// ctx always carries the team.
	Changed func(ctx context.Context, teamID int64)
	// Now is nil = time.Now.
	Now func() time.Time
}

// Service is the team shops' Stripe: the owner's actions, the Connect callback, the webhooks and
// the processors.
type Service struct {
	d      Deps
	keys   *keys
	reg    *Registry
	nonces nonces
}

// Paths the service answers on (mounted by the HTTP server).
const (
	CallbackPath       = "/api/auth/stripe/callback"
	ConnectWebhookPath = "/api/webhooks/stripe/connect"
	TeamWebhookPrefix  = "/api/webhooks/stripe/team/"
)

const (
	connectAuthorizeURL = "https://connect.stripe.com/oauth/authorize"
	stripeDashboardURL  = "https://dashboard.stripe.com/"
	maxKeyLen           = 255
)

// New builds the service. A missing or short SHOP_STRIPE_MASTER keeps every team payment off (the
// owner sees stripe_not_ready) and never stops a boot.
func New(ctx context.Context, d Deps) *Service {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Stripe == nil {
		d.Stripe = NewStripeAPI(d.Config, nil)
	}
	d.APIBase = strings.TrimRight(strings.TrimSpace(d.APIBase), "/")
	d.BaseDomain = strings.ToLower(strings.Trim(strings.TrimSpace(d.BaseDomain), "."))
	s := &Service{d: d}
	if strings.TrimSpace(d.Config.Master) != "" {
		k, err := newKeys(d.Config.Master)
		if err != nil {
			slog.Default().ErrorContext(ctx, "shoppay: team payments off", slog.String("err", err.Error()))
		} else {
			s.keys = k
		}
	}
	s.reg = newRegistry(d.Store, d.Teams, d.Config, s.keys, d.Factory)
	return s
}

func (s *Service) connectReady() bool {
	return s.keys != nil && s.d.Config.ConnectReady() && s.d.Factory != nil
}
func (s *Service) keysReady() bool { return s.keys != nil && s.d.Factory != nil }

func stripeNotReady() error { return status.Error(codes.Unimplemented, "stripe_not_ready") }

// Start warms the live teams' processors in the background (monitors of unpaid orders, account sync).
func (s *Service) Start(ctx context.Context) {
	if s.keys == nil || s.d.Factory == nil {
		return
	}
	s.reg.start(ctx, s.syncRow)
}

// Stop stops every team processor's payment monitors. Call before the databases close.
func (s *Service) Stop(ctx context.Context) { s.reg.stop(ctx) }

// For is the invoicer of team t's checkout (see Registry.For).
func (s *Service) For(ctx context.Context, t tenant.Team, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	if s.d.Factory == nil {
		return nil, notReady()
	}
	return s.reg.For(tenant.With(ctx, t), t, pm)
}

// ConnectPublishableKey is the platform's publishable key a connected team's storefront loads
// Stripe.js with (with the team's account); "" while Connect is off.
func (s *Service) ConnectPublishableKey() string {
	if !s.connectReady() {
		return ""
	}
	return strings.TrimSpace(s.d.Config.ConnectPublishableKey)
}

// WebhookURL is the address a team with its own keys gives Stripe for its webhook endpoint; "" while
// own keys are off on this installation.
func (s *Service) WebhookURL(teamID int64) string {
	if !s.keysReady() || s.d.APIBase == "" || teamID <= 0 {
		return ""
	}
	return s.d.APIBase + TeamWebhookPrefix + s.keys.webhookToken(teamID)
}

// StartConnect is the address the owner goes to: Stripe's Connect authorization for a team without
// a live Connect, the Stripe dashboard for a team already connected (finishing onboarding happens
// there). The OAuth callback finishes the connect.
func (s *Service) StartConnect(ctx context.Context, t tenant.Team, userID int64, operator bool, returnPath string) (string, error) {
	if !s.connectReady() || s.d.APIBase == "" || s.d.BaseDomain == "" {
		return "", stripeNotReady()
	}
	ret, ok := cleanReturn(returnPath)
	if !ok {
		return "", status.Error(codes.InvalidArgument, "bad_return_path")
	}
	row, has, err := s.d.Store.TeamStripe(ctx, t.ID)
	if err != nil {
		return "", err
	}
	if has && row.DisconnectedAt == nil {
		if row.Kind == platformstore.StripeKindKeys {
			return "", status.Error(codes.FailedPrecondition, "stripe_keys_set")
		}
		s.syncRow(ctx, t, row)
		return stripeDashboardURL, nil
	}
	nonce, err := newNonce()
	if err != nil {
		return "", err
	}
	st := s.keys.signState(connectState{Team: t.ID, User: userID, Operator: operator, Exp: s.d.Now().Add(stateTTL).Unix(), Nonce: nonce, Return: ret})
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", strings.TrimSpace(s.d.Config.ConnectClientID))
	q.Set("scope", "read_write")
	q.Set("state", st)
	q.Set("redirect_uri", s.d.APIBase+CallbackPath)
	return connectAuthorizeURL + "?" + q.Encode(), nil
}

// SetKeys stores a team's own Stripe keys: the secret key is checked with Stripe (the account it
// opens, the mode), the secret key and the webhook secret are sealed, the publishable key is kept.
func (s *Service) SetKeys(ctx context.Context, t tenant.Team, pk, sk, whsec string) error {
	if !s.keysReady() {
		return stripeNotReady()
	}
	pk, sk, whsec = strings.TrimSpace(pk), strings.TrimSpace(sk), strings.TrimSpace(whsec)
	mode := keyMode(sk)
	switch {
	case mode == "" || len(sk) > maxKeyLen || strings.ContainsAny(sk, " \t\r\n"):
		return status.Error(codes.InvalidArgument, "bad_secret_key")
	case publishableMode(pk) == "" || len(pk) > maxKeyLen || strings.ContainsAny(pk, " \t\r\n"):
		return status.Error(codes.InvalidArgument, "bad_publishable_key")
	case publishableMode(pk) != mode:
		// A live publishable key with a test secret key (or the reverse) never takes a payment.
		return status.Error(codes.InvalidArgument, "bad_publishable_key")
	case !strings.HasPrefix(whsec, "whsec_") || len(whsec) > maxKeyLen || strings.ContainsAny(whsec, " \t\r\n"):
		return status.Error(codes.InvalidArgument, "bad_webhook_secret")
	}
	row, has, err := s.d.Store.TeamStripe(ctx, t.ID)
	if err != nil {
		return err
	}
	if has && row.DisconnectedAt == nil && row.Kind == platformstore.StripeKindConnect {
		return status.Error(codes.FailedPrecondition, "stripe_connected")
	}
	acct, err := s.d.Stripe.OwnAccount(ctx, sk)
	switch {
	case errors.Is(err, ErrBadSecretKey):
		return status.Error(codes.InvalidArgument, "bad_secret_key")
	case errors.Is(err, ErrStripeUnavailable):
		return status.Error(codes.Unavailable, "stripe_unavailable")
	case err != nil:
		return status.Error(codes.InvalidArgument, "bad_secret_key")
	}
	acct.Mode = mode
	skSealed, err := s.keys.seal(t.ID, sealSecretKey, sk)
	if err != nil {
		return err
	}
	whSealed, err := s.keys.seal(t.ID, sealWebhookSecret, whsec)
	if err != nil {
		return err
	}
	err = s.d.Store.SetTeamStripeKeys(ctx, t.ID, platformstore.TeamStripeKeys{StripeAccountState: acct, PublishableKey: pk,
		SecretKeySealed: skSealed, WebhookSecretSealed: whSealed, KeyLast4: last4(sk)}, s.d.Now())
	if err != nil {
		return storeErr(err)
	}
	s.changed(ctx, t)
	return nil
}

// Disconnect ends a team's Stripe: the row is disconnected first (no more charges here), then a
// connected account is deauthorized at Stripe on a best-effort basis. Idempotent.
func (s *Service) Disconnect(ctx context.Context, t tenant.Team) error {
	if s.d.Factory == nil {
		return stripeNotReady()
	}
	row, has, err := s.d.Store.TeamStripe(ctx, t.ID)
	if err != nil {
		return err
	}
	if !has || row.DisconnectedAt != nil {
		s.reg.Drop(ctx, t.ID)
		return nil
	}
	acct := ""
	if row.AccountID != nil {
		acct = *row.AccountID
	}
	if _, err := s.d.Store.DisconnectTeamStripe(ctx, t.ID, "", s.d.Now()); err != nil {
		return err
	}
	s.changed(ctx, t)
	if row.Kind == platformstore.StripeKindConnect && acct != "" && s.d.Config.ConnectReady() {
		if err := s.d.Stripe.Deauthorize(ctx, acct); err != nil {
			// The account stays authorized at Stripe; its events find no team here and are dropped.
			slog.Default().WarnContext(ctx, "shoppay: deauthorize", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		}
	}
	return nil
}

// changed forgets the team's processor and its cached shop answers.
func (s *Service) changed(ctx context.Context, t tenant.Team) {
	ctx = tenant.With(ctx, t) // the Connect callback's request carries no team
	s.reg.Drop(ctx, t.ID)
	if s.d.Changed != nil {
		s.d.Changed(ctx, t.ID)
	}
}

// syncRow refreshes a live row's account state from Stripe (best effort).
func (s *Service) syncRow(ctx context.Context, t tenant.Team, row platformstore.TeamStripe) {
	var acct platformstore.StripeAccountState
	var err error
	switch row.Kind {
	case platformstore.StripeKindConnect:
		if row.AccountID == nil || !s.d.Config.ConnectReady() {
			return
		}
		acct, err = s.d.Stripe.ConnectedAccount(ctx, *row.AccountID)
	case platformstore.StripeKindKeys:
		if s.keys == nil {
			return
		}
		sk, oerr := s.keys.open(t.ID, sealSecretKey, row.SecretKeySealed)
		if oerr != nil {
			slog.Default().WarnContext(ctx, "shoppay: sealed key does not open", slog.Int64("team", t.ID))
			return
		}
		acct, err = s.d.Stripe.OwnAccount(ctx, sk)
	default:
		return
	}
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: account sync", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		return
	}
	rowAcct := ""
	if row.AccountID != nil {
		rowAcct = *row.AccountID
	}
	if acct.AccountID != rowAcct {
		// Own keys now open another account than the one stored: keep the row as it is.
		return
	}
	s.sync(ctx, t.ID, acct)
}

func (s *Service) sync(ctx context.Context, teamID int64, acct platformstore.StripeAccountState) {
	ok, err := s.d.Store.SyncTeamStripe(ctx, teamID, acct)
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: account sync write", slog.Int64("team", teamID), slog.String("err", err.Error()))
		return
	}
	if ok && s.d.Changed != nil {
		s.d.Changed(ctx, teamID)
	}
}

func storeErr(err error) error {
	switch {
	case errors.Is(err, platformstore.ErrStripeAccountTaken):
		return status.Error(codes.AlreadyExists, "stripe_account_taken")
	case errors.Is(err, platformstore.ErrWrongTeamStatus):
		return status.Error(codes.FailedPrecondition, "team_not_active")
	}
	return err
}

// adminURL is the page on team t's admin the owner lands on.
func (s *Service) adminURL(t tenant.Team, path string, q url.Values) string {
	u := "https://" + t.Slug + "." + s.d.BaseDomain + path
	if len(q) > 0 {
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		u += sep + q.Encode()
	}
	return u
}

func clientIP(r *http.Request) string { return middleware.ClientIPFromRequest(r) }
