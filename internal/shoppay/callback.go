package shoppay

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Callback results on the owner's landing address (?stripe=…): finish comes with #token=… (a
// fragment: it stays out of server logs and Referer), which the signed-in owner hands to
// FinishStripeConnect.
const (
	resultFinish    = "finish"
	resultCancelled = "cancelled"
	resultError     = "error"
)

const maxOAuthCode = 256

// Callback is Stripe's OAuth redirect (CallbackPath, GET). It runs in whatever browser approved the
// account, without a session, so it writes nothing: it checks the state and sends the browser back
// to the team's admin with a sealed finish token (state + code, finishTTL). The connect happens only
// in FinishConnect, for the signed-in user who started it.
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
		back := func(res, fragment string) {
			http.Redirect(w, r, s.adminURL(t, st.Return, url.Values{"stripe": {res}})+fragment, http.StatusSeeOther)
		}
		result := func(res string) { back(res, "") }
		if !s.nonces.use(st.Nonce, time.Unix(st.Exp, 0), now) {
			result(resultError)
			return
		}
		if q.Get("error") != "" {
			result(resultCancelled)
			return
		}
		code := q.Get("code")
		if code == "" || len(code) > maxOAuthCode {
			result(resultError)
			return
		}
		tok, err := s.keys.sealFinish(finishToken{State: st, Code: code, Exp: now.Add(finishTTL).Unix()})
		if err != nil {
			slog.Default().ErrorContext(ctx, "shoppay: finish token", slog.Int64("team", t.ID), slog.String("err", err.Error()))
			result(resultError)
			return
		}
		back(resultFinish, "#token="+tok)
	})
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
