package shoppay

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Connect results on the owner's landing address (?stripe=…).
const (
	resultConnected = "connected"
	resultCancelled = "cancelled"
	resultTaken     = "taken"
	resultError     = "error"
)

const maxOAuthCode = 256

// Callback is Stripe's OAuth redirect (CallbackPath, GET): it checks the state, that its starter
// still owns (or operates) the team, exchanges the code, stores the connected account and sends the
// owner back to the team's admin. It never deauthorizes an account (an account another team holds
// must stay connected there).
func (s *Service) Callback() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if !s.connectReady() || s.d.BaseDomain == "" {
			http.NotFound(w, r)
			return
		}
		ctx := r.Context()
		q := r.URL.Query()
		now := s.d.Now()
		st, err := s.keys.verifyState(q.Get("state"), now)
		if err != nil {
			http.Error(w, "bad state", http.StatusBadRequest)
			return
		}
		t, err := s.d.Teams.TeamByID(ctx, st.Team)
		if err != nil || t.Status != tenant.StatusActive {
			http.Error(w, "bad state", http.StatusBadRequest)
			return
		}
		back := func(result string) {
			http.Redirect(w, r, s.adminURL(t, st.Return, url.Values{"stripe": {result}}), http.StatusSeeOther)
		}
		if !s.nonces.use(st.Nonce, time.Unix(st.Exp, 0), now) {
			back(resultError)
			return
		}
		if q.Get("error") != "" {
			back(resultCancelled)
			return
		}
		code := q.Get("code")
		if code == "" || len(code) > maxOAuthCode {
			back(resultError)
			return
		}
		if !s.mayConnect(ctx, st) {
			slog.Default().WarnContext(ctx, "shoppay: connect by a non-owner refused", slog.Int64("team", t.ID))
			back(resultError)
			return
		}
		back(s.finishConnect(ctx, t, st, code, clientIP(r)))
	})
}

func (s *Service) finishConnect(ctx context.Context, t tenant.Team, st connectState, code, ip string) string {
	acct, livemode, err := s.d.Stripe.ExchangeCode(ctx, code)
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: oauth exchange", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		return resultError
	}
	mode := modeOf(livemode)
	if mode != s.d.Config.ConnectMode() {
		return resultError
	}
	row, has, err := s.d.Store.TeamStripe(ctx, t.ID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: team stripe", slog.String("err", err.Error()))
		return resultError
	}
	if has && row.DisconnectedAt == nil && row.Kind == platformstore.StripeKindKeys {
		// Own keys were saved meanwhile: the owner disconnects them first.
		return resultError
	}
	a, err := s.d.Stripe.ConnectedAccount(ctx, acct)
	if err != nil {
		// Connected all the same; account.updated brings the flags.
		slog.Default().WarnContext(ctx, "shoppay: connected account read", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		a = platformstore.StripeAccountState{}
	}
	a.AccountID, a.Mode = acct, mode
	if err := s.d.Store.ConnectTeamStripe(ctx, t.ID, a, s.d.Now()); err != nil {
		if errors.Is(err, platformstore.ErrStripeAccountTaken) {
			return resultTaken
		}
		slog.Default().ErrorContext(ctx, "shoppay: connect write", slog.Int64("team", t.ID), slog.String("err", err.Error()))
		return resultError
	}
	if err := s.d.Store.AuditAuth(ctx, st.User, t.ID, "shop.stripe_connected", map[string]string{"mode": mode}, ip); err != nil {
		slog.Default().WarnContext(ctx, "shoppay: audit", slog.String("err", err.Error()))
	}
	s.changed(ctx, t)
	return resultConnected
}

// mayConnect: the state's starter is still the team's owner, or still a console operator.
func (s *Service) mayConnect(ctx context.Context, st connectState) bool {
	if st.Operator {
		ok, err := s.d.Teams.Operator(ctx, st.User)
		return err == nil && ok
	}
	m, ok, err := s.d.Store.Membership(ctx, st.User, st.Team)
	return err == nil && ok && m.Role == "owner"
}
