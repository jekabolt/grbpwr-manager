package shoppay

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The connect's second half. Stripe's callback lands in whatever browser approved the account, so it
// writes nothing: it seals the state and the code into a short-lived finish token and sends that
// browser to the team's admin. Only the signed-in user who started the connect, still the team's
// owner (or still an operator), finishes it there (FinishConnect). A team owner who sends the
// authorize link to someone else gets a token only that browser holds, and it is useless to it.

const (
	finishTTL         = 5 * time.Minute
	maxFinishTokenLen = 2048
)

var errBadFinish = errors.New("shoppay: bad finish token")

// finishToken is what the callback hands the admin, sealed (AES-GCM under SHOP_STRIPE_MASTER, AAD
// naming the team): the OAuth code never travels in clear in a URL.
type finishToken struct {
	State connectState `json:"s"`
	Code  string       `json:"c"`
	Exp   int64        `json:"e"`
}

func finishAAD(teamID int64) string { return "stripe_connect_finish:" + strconv.FormatInt(teamID, 10) }

func (k *keys) sealFinish(f finishToken) (string, error) {
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	blob, err := k.finish.Seal(string(b), finishAAD(f.State.Team))
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(blob), nil
}

// openFinish opens a finish token of team teamID and checks its expiry.
func (k *keys) openFinish(teamID int64, tok string, now time.Time) (finishToken, error) {
	if tok == "" || len(tok) > maxFinishTokenLen {
		return finishToken{}, errBadFinish
	}
	blob, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil {
		return finishToken{}, errBadFinish
	}
	plain, err := k.finish.Open(blob, finishAAD(teamID))
	if err != nil {
		return finishToken{}, errBadFinish
	}
	var f finishToken
	if err := json.Unmarshal([]byte(plain), &f); err != nil {
		return finishToken{}, errBadFinish
	}
	if f.State.Team != teamID || f.State.User <= 0 || f.State.Nonce == "" || f.Code == "" || len(f.Code) > maxOAuthCode ||
		now.Unix() > f.Exp {
		return finishToken{}, errBadFinish
	}
	return f, nil
}

func errConnectExpired() error {
	return status.Error(codes.FailedPrecondition, "stripe_connect_expired")
}

// FinishConnect connects the Stripe account of a finish token to team t. userID is the signed-in
// caller (operator = an entered console operator): it must be the user who started the connect,
// still the team's owner (or still an operator). Single use.
func (s *Service) FinishConnect(ctx context.Context, t tenant.Team, userID int64, operator bool, token string) error {
	if !s.connectReady() {
		return stripeNotReady()
	}
	now := s.d.Now()
	f, err := s.keys.openFinish(t.ID, token, now)
	if err != nil {
		return errConnectExpired()
	}
	st := f.State
	if st.User != userID || st.Operator != operator {
		slog.Default().WarnContext(ctx, "shoppay: connect finish by another user refused", slog.Int64("team", t.ID))
		return status.Error(codes.PermissionDenied, "stripe_connect_not_yours")
	}
	if !s.mayConnect(ctx, st) {
		return status.Error(codes.PermissionDenied, "stripe_connect_not_yours")
	}
	if !s.finished.use(st.Nonce, time.Unix(f.Exp, 0), now) {
		return errConnectExpired()
	}
	return s.finishConnect(ctx, t, st, f.Code)
}

func (s *Service) finishConnect(ctx context.Context, t tenant.Team, st connectState, code string) error {
	ctx = tenant.With(ctx, t)
	// Own keys saved meanwhile refuse before anything is authorized at Stripe.
	if row, has, err := s.d.Store.TeamStripe(ctx, t.ID); err != nil {
		return err
	} else if has && row.DisconnectedAt == nil && row.Kind == platformstore.StripeKindKeys {
		return status.Error(codes.FailedPrecondition, "stripe_keys_set")
	}
	acct, livemode, err := s.d.Stripe.ExchangeCode(ctx, code)
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: oauth exchange", slog.Int64("team", t.ID), slog.String("stripe", stripeErrCode(err)))
		return errConnectExpired()
	}
	// A refusal below leaves the account authorized for the platform and connected to no team. It is
	// not deauthorized: the authorization is one per platform and account, not per team, and another
	// team's finish for the same account may have exchanged its code already (outside any lock, since
	// the account is only known after the exchange). Deauthorizing here could cut that shop off. An
	// authorization with no team row is never charged through; a later connect reuses it.
	return s.connectExchanged(ctx, t, st, acct, livemode)
}

// connectExchanged connects account acct (just authorized by the OAuth exchange) to team t.
func (s *Service) connectExchanged(ctx context.Context, t tenant.Team, st connectState, acct string, livemode bool) error {
	mode := modeOf(livemode)
	if mode != s.d.Config.ConnectMode() {
		return status.Error(codes.FailedPrecondition, "stripe_wrong_mode")
	}
	row, has, err := s.d.Store.TeamStripe(ctx, t.ID)
	if err != nil {
		return err
	}
	if has && row.DisconnectedAt == nil {
		if row.Kind == platformstore.StripeKindKeys {
			// Own keys were saved meanwhile: the owner disconnects them first.
			return status.Error(codes.FailedPrecondition, "stripe_keys_set")
		}
		if row.AccountID == nil || *row.AccountID != acct {
			// Another account replaces the live one: its paid orders must be settled first.
			if err := s.paymentsIdle(ctx, t); err != nil {
				return err
			}
		}
	}
	a, err := s.d.Stripe.ConnectedAccount(ctx, acct)
	if err != nil {
		// Connected all the same; account.updated brings the flags.
		slog.Default().WarnContext(ctx, "shoppay: connected account read", slog.Int64("team", t.ID), slog.String("stripe", stripeErrCode(err)))
		a = platformstore.StripeAccountState{}
	}
	a.AccountID, a.Mode = acct, mode
	if err := s.d.Store.ConnectTeamStripe(ctx, t.ID, a, s.d.Now()); err != nil {
		return storeErr(err)
	}
	detail := map[string]string{"mode": mode}
	if st.Operator {
		detail["operator"] = "true"
	}
	if err := s.d.Store.AuditAuth(ctx, st.User, t.ID, "shop.stripe_connected", detail, middleware.GetClientIP(ctx)); err != nil {
		slog.Default().WarnContext(ctx, "shoppay: audit", slog.String("err", err.Error()))
	}
	s.changed(ctx, t)
	return nil
}

// stripeErrCode names a Stripe failure for the log without Stripe's message.
func stripeErrCode(err error) string {
	var se *stripe.Error
	switch {
	case errors.Is(err, ErrBadSecretKey):
		return "bad_secret_key"
	case !errors.As(err, &se):
		return "no_answer"
	}
	switch {
	case se.OAuthError != "":
		return se.OAuthError
	case se.Code != "":
		return string(se.Code)
	case se.Type != "":
		return string(se.Type)
	}
	return "http_" + strconv.Itoa(se.HTTPStatusCode)
}
