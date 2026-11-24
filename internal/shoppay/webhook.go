package shoppay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"github.com/stripe/stripe-go/v79/webhook"
)

// The two webhook endpoints. Isolation between teams rests on two things only: the event's
// signature (the platform's Connect secret, or the one team's own webhook secret behind its secret
// address) and the team the event is then routed to (the connected account's row, or the address's
// team) — the processor runs under that team's context, so an order another team holds is simply
// not found. An event that does not belong here is acknowledged (200) and changes nothing.

const maxWebhookBody = 1 << 16

func readWebhook(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
		return nil, false
	}
	return payload, true
}

func constructEvent(payload []byte, sig, secret string) (stripe.Event, error) {
	// The endpoint's API version is whatever the account set when it was created; the fields read
	// here (ids, amounts, metadata, account flags) are stable across versions.
	return webhook.ConstructEventWithOptions(payload, sig, secret, webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
}

func modeOf(livemode bool) string {
	if livemode {
		return ModeLive
	}
	return ModeTest
}

// ConnectWebhook is the platform's Connect endpoint (ConnectWebhookPath): events of every connected
// account, routed by event.account to the team holding it.
func (s *Service) ConnectWebhook() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secret := strings.TrimSpace(s.d.Config.ConnectWebhookSecret)
		if secret == "" || !s.connectReady() {
			http.NotFound(w, r)
			return
		}
		payload, ok := readWebhook(w, r)
		if !ok {
			return
		}
		ev, err := constructEvent(payload, r.Header.Get("Stripe-Signature"), secret)
		if err != nil {
			slog.Default().WarnContext(r.Context(), "shoppay: connect webhook signature")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(s.connectEvent(r.Context(), ev))
	})
}

func (s *Service) connectEvent(ctx context.Context, ev stripe.Event) int {
	acct := ev.Account
	if acct == "" || modeOf(ev.Livemode) != s.d.Config.ConnectMode() {
		return http.StatusOK
	}
	row, ok, err := s.d.Store.TeamStripeByAccount(ctx, acct)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: connect webhook team", slog.String("err", err.Error()))
		return http.StatusInternalServerError
	}
	if !ok || row.Mode != s.d.Config.ConnectMode() {
		slog.Default().InfoContext(ctx, "shoppay: connect event for no team", slog.String("type", string(ev.Type)), slog.String("event_id", ev.ID))
		return http.StatusOK
	}
	t, code, ok := s.eventTeam(ctx, row.TeamID)
	if !ok {
		return code
	}
	tctx := tenant.With(ctx, t)
	switch ev.Type {
	case stripe.EventTypeAccountUpdated:
		var a stripe.Account
		if ev.Data == nil || json.Unmarshal(ev.Data.Raw, &a) != nil {
			return http.StatusBadRequest
		}
		if a.ID == acct {
			s.sync(tctx, t.ID, accountState(&a, row.Mode))
		}
		return http.StatusOK
	case stripe.EventTypeAccountApplicationDeauthorized:
		done, err := s.d.Store.DisconnectTeamStripe(tctx, t.ID, acct, s.d.Now())
		if err != nil {
			slog.Default().ErrorContext(ctx, "shoppay: deauthorized", slog.String("err", err.Error()))
			return http.StatusInternalServerError
		}
		if done {
			s.changed(tctx, t)
			if err := s.d.Store.AuditAuth(tctx, 0, t.ID, "shop.stripe_deauthorized", map[string]string{}, ""); err != nil {
				slog.Default().WarnContext(ctx, "shoppay: audit", slog.String("err", err.Error()))
			}
		}
		return http.StatusOK
	}
	return s.dispatch(tctx, t, row, ev)
}

// TeamWebhook is a team's own-keys endpoint (TeamWebhookPrefix + the team's secret token): events of
// the team's own Stripe account, signed with its webhook secret.
func (s *Service) TeamWebhook() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.keysReady() {
			http.NotFound(w, r)
			return
		}
		token := strings.TrimPrefix(r.URL.Path, TeamWebhookPrefix)
		if len(token) != webhookTokenLen || strings.Contains(token, "/") {
			http.NotFound(w, r)
			return
		}
		payload, ok := readWebhook(w, r)
		if !ok {
			return
		}
		w.WriteHeader(s.teamEvent(r.Context(), token, payload, r.Header.Get("Stripe-Signature")))
	})
}

func (s *Service) teamEvent(ctx context.Context, token string, payload []byte, sig string) int {
	ids, err := s.d.Store.LiveStripeTeams(ctx, platformstore.StripeKindKeys)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: team webhook teams", slog.String("err", err.Error()))
		return http.StatusInternalServerError
	}
	var teamID int64
	for _, id := range ids {
		if s.keys.webhookTokenIs(id, token) {
			teamID = id
		}
	}
	if teamID == 0 {
		return http.StatusNotFound
	}
	row, ok, err := s.d.Store.TeamStripe(ctx, teamID)
	if err != nil {
		return http.StatusInternalServerError
	}
	if !ok || row.DisconnectedAt != nil || row.Kind != platformstore.StripeKindKeys {
		return http.StatusNotFound
	}
	whsec, err := s.keys.open(teamID, sealWebhookSecret, row.WebhookSecretSealed)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shoppay: sealed webhook secret does not open", slog.Int64("team", teamID))
		return http.StatusInternalServerError
	}
	ev, err := constructEvent(payload, sig, whsec)
	if err != nil {
		slog.Default().WarnContext(ctx, "shoppay: team webhook signature", slog.Int64("team", teamID))
		return http.StatusBadRequest
	}
	// A Connect event (the team's account is itself a platform) or the other mode is not this shop's.
	if ev.Account != "" || modeOf(ev.Livemode) != row.Mode {
		return http.StatusOK
	}
	t, code, ok := s.eventTeam(ctx, teamID)
	if !ok {
		return code
	}
	tctx := tenant.With(ctx, t)
	if ev.Type == stripe.EventTypeAccountUpdated {
		var a stripe.Account
		if ev.Data == nil || json.Unmarshal(ev.Data.Raw, &a) != nil {
			return http.StatusBadRequest
		}
		if row.AccountID != nil && a.ID == *row.AccountID {
			s.sync(tctx, t.ID, accountState(&a, row.Mode))
		}
		return http.StatusOK
	}
	return s.dispatch(tctx, t, row, ev)
}

// eventTeam resolves the team an event is for: 503 while its schema is being updated (Stripe
// retries), 200 (dropped) when the team cannot take it.
func (s *Service) eventTeam(ctx context.Context, id int64) (tenant.Team, int, bool) {
	t, err := s.reg.usableTeam(ctx, id)
	switch {
	case errors.Is(err, errSchemaBehind):
		return tenant.Team{}, http.StatusServiceUnavailable, false
	case err != nil:
		slog.Default().WarnContext(ctx, "shoppay: event for an unusable team", slog.Int64("team", id), slog.String("err", err.Error()))
		return tenant.Team{}, http.StatusOK, false
	}
	return t, 0, true
}

// dispatch hands a payment event to the team's processor (ctx carries the team).
func (s *Service) dispatch(ctx context.Context, t tenant.Team, row platformstore.TeamStripe, ev stripe.Event) int {
	switch ev.Type {
	case stripe.EventTypePaymentIntentSucceeded, stripe.EventTypeChargeDisputeCreated, stripe.EventTypeChargeDisputeClosed:
	default:
		return http.StatusOK
	}
	p, err := s.reg.processor(ctx, t, row)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	return p.DispatchEvent(ctx, ev)
}
