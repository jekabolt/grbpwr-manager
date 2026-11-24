package shoppay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stripe/stripe-go/v79"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func post(h http.Handler, path string, payload []byte, sig string) int {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(payload))
	req.Header.Set("Stripe-Signature", sig)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func webhookPath(t *testing.T, r *rig, id int64) string {
	t.Helper()
	u, err := url.Parse(r.svc.WebhookURL(id))
	if err != nil || !strings.HasPrefix(u.Path, TeamWebhookPrefix) {
		t.Fatalf("webhook url %q", r.svc.WebhookURL(id))
	}
	return u.Path
}

// TestTeamWebhookCannotPayAnotherTeam: team A's webhook secret, at team A's address, naming team B's
// order, leaves that order unpaid; at team B's address it is refused; B's own event pays it.
func TestTeamWebhookCannotPayAnotherTeam(t *testing.T) {
	r := newRig(t)
	r.ownKeys(t, teamA, "sk_test_alpha", "acct_A", "whsec_alpha")
	r.ownKeys(t, teamB, "sk_test_bravo", "acct_B", "whsec_bravo")
	r.l.add(teamA, "order-a")
	r.l.add(teamB, "order-b")
	h := r.svc.TeamWebhook()
	pathA, pathB := webhookPath(t, r, teamA), webhookPath(t, r, teamB)
	if pathA == pathB {
		t.Fatal("two teams share a webhook address")
	}

	ev := paidEvent(t, "", false, "order-b")
	if code := post(h, pathA, ev, sign(ev, "whsec_alpha")); code != http.StatusOK {
		t.Fatalf("A's event at A: %d", code)
	}
	if r.l.paid(teamB, "order-b") {
		t.Fatal("team A's webhook paid team B's order")
	}
	if code := post(h, pathB, ev, sign(ev, "whsec_alpha")); code != http.StatusBadRequest {
		t.Fatalf("A-signed event at B's address: %d, want 400", code)
	}
	if r.l.paid(teamB, "order-b") {
		t.Fatal("an A-signed event paid B's order at B's address")
	}
	// Live event for a test shop, a Connect event at an own-keys address: dropped.
	live := paidEvent(t, "", true, "order-a")
	if code := post(h, pathA, live, sign(live, "whsec_alpha")); code != http.StatusOK || r.l.paid(teamA, "order-a") {
		t.Fatalf("live event for a test shop: %d paid=%v", code, r.l.paid(teamA, "order-a"))
	}
	conn := paidEvent(t, "acct_X", false, "order-a")
	if code := post(h, pathA, conn, sign(conn, "whsec_alpha")); code != http.StatusOK || r.l.paid(teamA, "order-a") {
		t.Fatalf("connect event at an own-keys address: %d paid=%v", code, r.l.paid(teamA, "order-a"))
	}
	// Positive controls.
	own := paidEvent(t, "", false, "order-b")
	if code := post(h, pathB, own, sign(own, "whsec_bravo")); code != http.StatusOK || !r.l.paid(teamB, "order-b") {
		t.Fatalf("B's own event: %d paid=%v", code, r.l.paid(teamB, "order-b"))
	}
	mine := paidEvent(t, "", false, "order-a")
	if code := post(h, pathA, mine, sign(mine, "whsec_alpha")); code != http.StatusOK || !r.l.paid(teamA, "order-a") {
		t.Fatalf("A's own event: %d paid=%v", code, r.l.paid(teamA, "order-a"))
	}
	for _, p := range r.builds() {
		if p.spec.Account != "" || p.spec.Method != entity.CARD_TEST {
			t.Fatalf("own-keys processor spec %+v", p.spec)
		}
	}
	// Unknown or malformed address: 404.
	if code := post(h, TeamWebhookPrefix+strings.Repeat("x", webhookTokenLen), mine, sign(mine, "whsec_alpha")); code != http.StatusNotFound {
		t.Fatalf("unknown address: %d", code)
	}
	if code := post(h, TeamWebhookPrefix+"short", mine, sign(mine, "whsec_alpha")); code != http.StatusNotFound {
		t.Fatalf("short address: %d", code)
	}
	// A disconnected team's address is gone.
	if err := r.svc.Disconnect(context.Background(), r.team(teamA)); err != nil {
		t.Fatal(err)
	}
	if code := post(h, pathA, mine, sign(mine, "whsec_alpha")); code != http.StatusNotFound {
		t.Fatalf("disconnected team's address: %d", code)
	}
}

// TestConnectWebhookRoutesByAccount: a platform-signed event runs under the team holding its account
// only; unknown accounts, the other mode, platform events and bad signatures change nothing.
func TestConnectWebhookRoutesByAccount(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	for id, acct := range map[int64]string{teamA: "acct_A", teamB: "acct_B"} {
		if err := r.st.ConnectTeamStripe(ctx, id, platformstore.StripeAccountState{AccountID: acct, Mode: ModeTest}, r.now); err != nil {
			t.Fatal(err)
		}
	}
	r.l.add(teamA, "order-a")
	r.l.add(teamB, "order-b")
	h := r.svc.ConnectWebhook()
	send := func(payload []byte) int {
		return post(h, ConnectWebhookPath, payload, sign(payload, testConnectSecret))
	}

	if code := send(paidEvent(t, "acct_A", false, "order-b")); code != http.StatusOK || r.l.paid(teamB, "order-b") {
		t.Fatalf("acct_A naming B's order: %d paid=%v", code, r.l.paid(teamB, "order-b"))
	}
	for _, ev := range [][]byte{
		paidEvent(t, "acct_unknown", false, "order-b"),
		paidEvent(t, "acct_B", true, "order-b"), // live event, test platform
		paidEvent(t, "", false, "order-b"),      // the platform's own event
	} {
		if code := send(ev); code != http.StatusOK || r.l.paid(teamB, "order-b") {
			t.Fatalf("foreign event: %d paid=%v", code, r.l.paid(teamB, "order-b"))
		}
	}
	bad := paidEvent(t, "acct_B", false, "order-b")
	if code := post(h, ConnectWebhookPath, bad, sign(bad, "whsec_alpha")); code != http.StatusBadRequest || r.l.paid(teamB, "order-b") {
		t.Fatalf("bad signature: %d", code)
	}
	if code := send(paidEvent(t, "acct_A", false, "order-a")); code != http.StatusOK || !r.l.paid(teamA, "order-a") {
		t.Fatalf("acct_A's own order: %d paid=%v", code, r.l.paid(teamA, "order-a"))
	}
	for _, p := range r.builds() {
		if p.spec.Team.ID != teamA || p.spec.Account != "acct_A" || p.spec.SecretKey != testPlatformKey || p.spec.Method != entity.CARD_TEST {
			t.Fatalf("processor spec %+v", p.spec)
		}
	}

	// A team whose schema is being updated: 503, Stripe retries.
	r.teams.behind[teamB] = true
	if code := send(paidEvent(t, "acct_B", false, "order-b")); code != http.StatusServiceUnavailable || r.l.paid(teamB, "order-b") {
		t.Fatalf("schema behind: %d", code)
	}
	r.teams.behind[teamB] = false

	// account.updated syncs the flags.
	upd := eventJSON(t, stripe.EventTypeAccountUpdated, "acct_A", false,
		map[string]any{"id": "acct_A", "object": "account", "charges_enabled": true, "details_submitted": true,
			"settings": map[string]any{"dashboard": map[string]any{"display_name": "alpha shop"}}})
	if code := send(upd); code != http.StatusOK {
		t.Fatalf("account.updated: %d", code)
	}
	if row := r.st.row(teamA); !row.ChargesEnabled || !row.DetailsSubmitted || row.AccountName != "alpha shop" {
		t.Fatalf("synced row %+v", row)
	}
	// An account.updated naming another account than the event's: ignored.
	cross := eventJSON(t, stripe.EventTypeAccountUpdated, "acct_A", false, map[string]any{"id": "acct_B", "object": "account", "charges_enabled": true})
	if code := send(cross); code != http.StatusOK || r.st.row(teamB).ChargesEnabled {
		t.Fatalf("cross account.updated: %d %+v", code, r.st.row(teamB))
	}

	// Deauthorized: the team's Stripe is disconnected and its processor stopped; later events drop.
	built := r.builds()
	deauth := eventJSON(t, stripe.EventTypeAccountApplicationDeauthorized, "acct_A", false, map[string]any{"id": "ca_test", "object": "application"})
	if code := send(deauth); code != http.StatusOK {
		t.Fatalf("deauthorized: %d", code)
	}
	if row := r.st.row(teamA); row.DisconnectedAt == nil || row.AccountID != nil {
		t.Fatalf("deauthorized row %+v", row)
	}
	if !built[len(built)-1].isStopped() {
		t.Fatal("a deauthorized team's processor still runs")
	}
	r.l.add(teamA, "order-a2")
	if code := send(paidEvent(t, "acct_A", false, "order-a2")); code != http.StatusOK || r.l.paid(teamA, "order-a2") {
		t.Fatalf("event after deauthorize: %d", code)
	}
	if _, ok, _ := r.st.TeamStripeByAccount(ctx, "acct_B"); !ok {
		t.Fatal("team B lost its account")
	}
}

func stateOf(t *testing.T, raw string) (string, url.Values) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Scheme + "://" + u.Host + u.Path, u.Query()
}

func callback(r *rig, q url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, CallbackPath+"?"+q.Encode(), nil)
	rec := httptest.NewRecorder()
	r.svc.Callback().ServeHTTP(rec, req)
	return rec
}

// finishToken follows the callback: it must send the browser to wantPrefix?stripe=finish with the
// token in the fragment, the OAuth code nowhere in the address, and write nothing.
func finishTokenOf(t *testing.T, rec *httptest.ResponseRecorder, wantPrefix, code string) string {
	t.Helper()
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil || rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, wantPrefix) || u.Query().Get("stripe") != resultFinish ||
		u.Query().Get("token") != "" || !strings.HasPrefix(u.Fragment, "token=") {
		t.Fatalf("callback: %d %q", rec.Code, loc)
	}
	if strings.Contains(loc, code) {
		t.Fatal("the OAuth code travels in the address")
	}
	return strings.TrimPrefix(u.Fragment, "token=")
}

func refusal(err error) string {
	return status.Code(err).String() + " " + status.Convert(err).Message()
}

func TestConnectFlow(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a, b := r.team(teamA), r.team(teamB)
	r.stripe.accounts["acct_A"] = platformstore.StripeAccountState{AccountID: "acct_A", Name: "alpha", DetailsSubmitted: true}

	for _, bad := range []string{"https://evil.example/x", "/shop#x"} {
		if _, err := r.svc.StartConnect(ctx, a, ownerA, false, bad); status.Code(err) != codes.InvalidArgument {
			t.Fatalf("return path %q: %v", bad, err)
		}
	}
	raw, err := r.svc.StartConnect(ctx, a, ownerA, false, "/shop")
	if err != nil {
		t.Fatal(err)
	}
	base, q := stateOf(t, raw)
	if base != connectAuthorizeURL || q.Get("client_id") != "ca_test" || q.Get("scope") != "read_write" ||
		q.Get("redirect_uri") != "https://api.example.com"+CallbackPath || q.Get("state") == "" {
		t.Fatalf("authorize url %s", raw)
	}
	state := q.Get("state")

	// The callback writes nothing and never exchanges the code: whoever's browser approved the
	// account only gets a token for the starter's admin.
	r.stripe.codes["code-a"] = codeGrant{acct: "acct_A"}
	tok := finishTokenOf(t, callback(r, url.Values{"state": {state}, "code": {"code-a"}}), "https://alpha.example.com/shop?stripe=finish#", "code-a")
	if _, ok, _ := r.st.TeamStripe(ctx, teamA); ok {
		t.Fatal("the callback connected an account")
	}
	if _, ok := r.stripe.codes["code-a"]; !ok {
		t.Fatal("the callback exchanged the code")
	}
	// Replay of the same state.
	if rec := callback(r, url.Values{"state": {state}, "code": {"code-a"}}); !strings.HasSuffix(rec.Header().Get("Location"), "stripe=error") {
		t.Fatalf("replayed state: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Login CSRF: team A's owner sent the link to B's owner, who approved their own account. B's
	// owner holds the token but cannot finish it — not on A, not on B, not as an operator.
	for _, c := range []struct {
		team     tenant.Team
		user     int64
		operator bool
		tok      string
		want     string
	}{
		{a, ownerB, false, tok, "PermissionDenied stripe_connect_not_yours"},
		{a, ownerA, true, tok, "PermissionDenied stripe_connect_not_yours"},
		{b, ownerB, false, tok, "FailedPrecondition stripe_connect_expired"},
		{a, ownerA, false, tok + "x", "FailedPrecondition stripe_connect_expired"},
		{a, ownerA, false, tok[:len(tok)-4] + "AAAA", "FailedPrecondition stripe_connect_expired"},
		{a, ownerA, false, "", "FailedPrecondition stripe_connect_expired"},
	} {
		if err := r.svc.FinishConnect(ctx, c.team, c.user, c.operator, c.tok); refusal(err) != c.want {
			t.Fatalf("finish team %d user %d operator %v: %v, want %s", c.team.ID, c.user, c.operator, err, c.want)
		}
	}
	if _, ok, _ := r.st.TeamStripe(ctx, teamA); ok {
		t.Fatal("a refused finish connected an account")
	}
	if _, ok, _ := r.st.TeamStripe(ctx, teamB); ok {
		t.Fatal("a refused finish connected an account to B")
	}

	// The starter finishes; once.
	if err := r.svc.FinishConnect(ctx, a, ownerA, false, tok); err != nil {
		t.Fatal(err)
	}
	row := r.st.row(teamA)
	if row.Kind != platformstore.StripeKindConnect || row.AccountID == nil || *row.AccountID != "acct_A" || row.Mode != ModeTest ||
		!row.DetailsSubmitted || row.AccountName != "alpha" {
		t.Fatalf("connected row %+v", row)
	}
	if !slices.Contains(r.st.audits, "shop.stripe_connected") {
		t.Fatalf("audits %v", r.st.audits)
	}
	if err := r.svc.FinishConnect(ctx, a, ownerA, false, tok); refusal(err) != "FailedPrecondition stripe_connect_expired" {
		t.Fatalf("token reuse: %v", err)
	}
	// A connected team gets the dashboard.
	if u, err := r.svc.StartConnect(ctx, a, ownerA, false, ""); err != nil || u != stripeDashboardURL {
		t.Fatalf("connected start: %q %v", u, err)
	}
	// Tampered or expired state: 400, nothing written.
	if rec := callback(r, url.Values{"state": {state + "x"}, "code": {"code-a2"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("tampered state: %d", rec.Code)
	}
	rawB, _ := r.svc.StartConnect(ctx, b, ownerB, false, "")
	_, qb := stateOf(t, rawB)
	r.now = r.now.Add(stateTTL + time.Minute)
	if rec := callback(r, url.Values{"state": {qb.Get("state")}, "code": {"code-a2"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("expired state: %d", rec.Code)
	}

	// finishB runs a connect of team B by user up to the finish token.
	finishB := func(user int64, operator bool, code string) string {
		t.Helper()
		raw, err := r.svc.StartConnect(ctx, b, user, operator, "")
		if err != nil {
			t.Fatal(err)
		}
		_, q := stateOf(t, raw)
		return finishTokenOf(t, callback(r, url.Values{"state": {q.Get("state")}, "code": {code}}), "https://bravo.example.com/shop?stripe=finish#", code)
	}
	// An expired finish token.
	r.stripe.codes["code-b"] = codeGrant{acct: "acct_B"}
	tb := finishB(ownerB, false, "code-b")
	r.now = r.now.Add(finishTTL + time.Second)
	if err := r.svc.FinishConnect(ctx, b, ownerB, false, tb); refusal(err) != "FailedPrecondition stripe_connect_expired" {
		t.Fatalf("expired finish: %v", err)
	}
	// A code Stripe refuses.
	if err := r.svc.FinishConnect(ctx, b, ownerB, false, finishB(ownerB, false, "code-unknown")); refusal(err) != "FailedPrecondition stripe_connect_expired" {
		t.Fatalf("refused code: %v", err)
	}
	// A live account on a test platform.
	r.stripe.codes["code-live"] = codeGrant{acct: "acct_L", live: true}
	if err := r.svc.FinishConnect(ctx, b, ownerB, false, finishB(ownerB, false, "code-live")); refusal(err) != "FailedPrecondition stripe_wrong_mode" {
		t.Fatalf("wrong mode: %v", err)
	}
	// Team B's owner connecting A's account: taken, A keeps it, nothing is deauthorized.
	r.stripe.codes["code-a2"] = codeGrant{acct: "acct_A"}
	if err := r.svc.FinishConnect(ctx, b, ownerB, false, finishB(ownerB, false, "code-a2")); refusal(err) != "AlreadyExists stripe_account_taken" {
		t.Fatalf("taken: %v", err)
	}
	if _, ok, _ := r.st.TeamStripe(ctx, teamB); ok {
		t.Fatal("team B got a row for A's account")
	}
	if len(r.stripe.deauth) != 0 {
		t.Fatal("a connect deauthorized an account")
	}
	// The starter lost ownership before the finish: refused.
	tb = finishB(ownerB, false, "code-b")
	r.st.roles[[2]int64{ownerB, teamB}] = "member"
	if err := r.svc.FinishConnect(ctx, b, ownerB, false, tb); refusal(err) != "PermissionDenied stripe_connect_not_yours" {
		t.Fatalf("demoted owner: %v", err)
	}
	if _, ok, _ := r.st.TeamStripe(ctx, teamB); ok {
		t.Fatal("a demoted owner connected an account")
	}
	// The owner said no at Stripe.
	r.st.roles[[2]int64{ownerB, teamB}] = "owner"
	rawB, _ = r.svc.StartConnect(ctx, b, ownerB, false, "/shop?tab=pay")
	_, qb = stateOf(t, rawB)
	if rec := callback(r, url.Values{"state": {qb.Get("state")}, "error": {"access_denied"}}); rec.Header().Get("Location") != "https://bravo.example.com/shop?tab=pay&stripe=cancelled" {
		t.Fatalf("cancelled: %q", rec.Header().Get("Location"))
	}
	// An operator who entered the team may connect while still an operator.
	r.teams.operators[999] = true
	if err := r.svc.FinishConnect(ctx, b, 999, true, finishB(999, true, "code-b")); err != nil {
		t.Fatalf("operator connect: %v", err)
	}
	if row := r.st.row(teamB); row.AccountID == nil || *row.AccountID != "acct_B" {
		t.Fatalf("operator connect row %+v", row)
	}

	// Disconnect waits for the team's card orders, then the row goes first, then the
	// deauthorization; idempotent.
	r.orders.set(teamA, 1)
	if err := r.svc.Disconnect(ctx, a); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("busy disconnect: %v", err)
	}
	if row := r.st.row(teamA); row.DisconnectedAt != nil || len(r.stripe.deauth) != 0 {
		t.Fatalf("a busy shop was disconnected: %+v %v", row, r.stripe.deauth)
	}
	if got := r.orders.since[len(r.orders.since)-1]; !got.Equal(r.now.Add(-RefundWindow)) {
		t.Fatalf("refund window from %v", got)
	}
	r.orders.set(teamA, 0)
	if err := r.svc.Disconnect(ctx, a); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 1) // an already disconnected shop stays disconnected
	if err := r.svc.Disconnect(ctx, a); err != nil {
		t.Fatal(err)
	}
	if row := r.st.row(teamA); row.DisconnectedAt == nil || len(r.stripe.deauth) != 1 || r.stripe.deauth[0] != "acct_A" {
		t.Fatalf("disconnect: %+v %v", row, r.stripe.deauth)
	}
}

// A live Connect replaced by another account waits for the team's card orders; the same account
// reconnects any time.
func TestConnectSwitchWaitsForOrders(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	start := func() string {
		raw, err := r.svc.StartConnect(ctx, a, ownerA, false, "")
		if err != nil {
			t.Fatal(err)
		}
		_, q := stateOf(t, raw)
		return q.Get("state")
	}
	finish := func(state, code, acct string) error {
		r.stripe.codes[code] = codeGrant{acct: acct}
		tok := finishTokenOf(t, callback(r, url.Values{"state": {state}, "code": {code}}), "https://alpha.example.com/shop?stripe=finish#", code)
		return r.svc.FinishConnect(ctx, a, ownerA, false, tok)
	}
	s1, s2, s3, s4 := start(), start(), start(), start() // started before the first connect landed
	if err := finish(s1, "code-switch-1", "acct_A"); err != nil {
		t.Fatal(err)
	}
	r.orders.set(teamA, 2)
	if err := finish(s2, "code-switch-2", "acct_A2"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("switch while busy: %v", err)
	}
	if row := r.st.row(teamA); *row.AccountID != "acct_A" {
		t.Fatalf("switched while busy: %+v", row)
	}
	if err := finish(s3, "code-switch-3", "acct_A"); err != nil {
		t.Fatalf("same account while busy: %v", err)
	}
	r.orders.set(teamA, 0)
	if err := finish(s4, "code-switch-4", "acct_A2"); err != nil {
		t.Fatalf("switch when idle: %v", err)
	}
	if row := r.st.row(teamA); *row.AccountID != "acct_A2" {
		t.Fatalf("idle switch row %+v", row)
	}
	// A live Connect answers the dashboard, never a second authorization.
	if u, err := r.svc.StartConnect(ctx, a, ownerA, false, ""); err != nil || u != stripeDashboardURL {
		t.Fatalf("connected start: %q %v", u, err)
	}
}

func TestSetKeys(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	r.stripe.own["sk_test_alpha"] = platformstore.StripeAccountState{AccountID: "acct_A", Name: "alpha", ChargesEnabled: true}
	for _, c := range []struct{ pk, sk, wh, want string }{
		{"pk_test_1", "nope", "whsec_1", "bad_secret_key"},
		{"nope", "sk_test_alpha", "whsec_1", "bad_publishable_key"},
		{"pk_live_1", "sk_test_alpha", "whsec_1", "bad_publishable_key"},
		{"pk_test_1", "sk_test_alpha", "secret", "bad_webhook_secret"},
		{"pk_test_1", "sk_test_unknown", "whsec_1", "bad_secret_key"},
	} {
		if err := r.svc.SetKeys(ctx, a, c.pk, c.sk, c.wh); status.Convert(err).Message() != c.want {
			t.Fatalf("SetKeys(%s, %s, %s) = %v, want %s", c.pk, c.sk, c.wh, err, c.want)
		}
	}
	r.stripe.ownErr = ErrStripeUnavailable
	if err := r.svc.SetKeys(ctx, a, "pk_test_1", "sk_test_alpha", "whsec_1"); status.Code(err) != codes.Unavailable {
		t.Fatalf("stripe down: %v", err)
	}
	r.stripe.ownErr = nil
	if err := r.svc.SetKeys(ctx, a, " pk_test_1 ", "sk_test_alpha", "whsec_alpha"); err != nil {
		t.Fatal(err)
	}
	row := r.st.row(teamA)
	if row.Kind != platformstore.StripeKindKeys || row.PublishableKey != "pk_test_1" || row.KeyLast4 != "lpha" || row.Mode != ModeTest ||
		row.AccountID == nil || *row.AccountID != "acct_A" || !row.ChargesEnabled {
		t.Fatalf("keys row %+v", row)
	}
	if bytes.Contains(row.SecretKeySealed, []byte("sk_test_alpha")) || bytes.Contains(row.WebhookSecretSealed, []byte("whsec_alpha")) {
		t.Fatal("a secret is stored in clear")
	}
	if sk, err := r.svc.keys.open(teamA, sealSecretKey, row.SecretKeySealed); err != nil || sk != "sk_test_alpha" {
		t.Fatalf("open secret key: %v", err)
	}
	// A blob moved to another team, or into the other column, does not open.
	if _, err := r.svc.keys.open(teamB, sealSecretKey, row.SecretKeySealed); err == nil {
		t.Fatal("team A's sealed key opened as team B's")
	}
	if _, err := r.svc.keys.open(teamA, sealWebhookSecret, row.SecretKeySealed); err == nil {
		t.Fatal("a sealed secret key opened as a webhook secret")
	}
	// The same account under another team: taken.
	if err := r.svc.SetKeys(ctx, r.team(teamB), "pk_test_1", "sk_test_alpha", "whsec_b"); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("account held by A: %v", err)
	}
	// A connected team keeps Connect until it disconnects.
	if err := r.st.ConnectTeamStripe(ctx, teamB, platformstore.StripeAccountState{AccountID: "acct_B", Mode: ModeTest}, r.now); err != nil {
		t.Fatal(err)
	}
	r.stripe.own["sk_test_bravo"] = platformstore.StripeAccountState{AccountID: "acct_B2"}
	if err := r.svc.SetKeys(ctx, r.team(teamB), "pk_test_1", "sk_test_bravo", "whsec_b"); status.Convert(err).Message() != "stripe_connected" {
		t.Fatalf("keys over connect: %v", err)
	}
	if _, err := r.svc.StartConnect(ctx, a, ownerA, false, ""); status.Convert(err).Message() != "stripe_keys_set" {
		t.Fatalf("connect over keys: %v", err)
	}

	// With card orders in flight: new keys of the same account are fine, another account waits.
	r.orders.set(teamA, 1)
	r.stripe.own["sk_test_alpha2"] = platformstore.StripeAccountState{AccountID: "acct_A", Name: "alpha", ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_2", "sk_test_alpha2", "whsec_alpha2"); err != nil {
		t.Fatalf("rotate keys while busy: %v", err)
	}
	r.stripe.own["sk_test_other"] = platformstore.StripeAccountState{AccountID: "acct_O", ChargesEnabled: true}
	if err := r.svc.SetKeys(ctx, a, "pk_test_3", "sk_test_other", "whsec_o"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("switch keys while busy: %v", err)
	}
	r.stripe.own["rk_test_restricted"] = platformstore.StripeAccountState{ChargesEnabled: true} // cannot read its account
	if err := r.svc.SetKeys(ctx, a, "pk_test_4", "rk_test_restricted", "whsec_r"); refusal(err) != "FailedPrecondition shop_payments_busy" {
		t.Fatalf("restricted key while busy: %v", err)
	}
	if row := r.st.row(teamA); *row.AccountID != "acct_A" || row.PublishableKey != "pk_test_2" {
		t.Fatalf("keys switched while busy: %+v", row)
	}
	r.orders.set(teamA, 0)
	if err := r.svc.SetKeys(ctx, a, "pk_test_3", "sk_test_other", "whsec_o"); err != nil {
		t.Fatalf("switch keys when idle: %v", err)
	}
}

func TestRegistryFor(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a := r.team(teamA)
	if _, err := r.svc.For(ctx, a, entity.CARD_TEST); status.Convert(err).Message() != MsgPaymentsNotReady {
		t.Fatalf("no stripe: %v", err)
	}
	r.ownKeys(t, teamA, "sk_test_alpha", "acct_A", "whsec_alpha")
	if _, err := r.svc.For(ctx, a, entity.CARD); status.Convert(err).Message() != MsgPaymentsNotReady {
		t.Fatalf("live card on a test shop: %v", err)
	}
	if _, err := r.svc.For(ctx, a, "bank-invoice"); status.Code(err) != codes.Unimplemented {
		t.Fatalf("not a card: %v", err)
	}
	p1, err := r.svc.For(ctx, a, entity.CARD_TEST)
	if err != nil {
		t.Fatal(err)
	}
	p2, _ := r.svc.For(ctx, a, entity.CARD_TEST)
	if p1 != p2 || len(r.builds()) != 1 {
		t.Fatalf("processor rebuilt without a change (%d builds)", len(r.builds()))
	}
	if spec := r.builds()[0].spec; spec.SecretKey != "sk_test_alpha" || spec.Account != "" || spec.Team.ID != teamA {
		t.Fatalf("spec %+v", spec)
	}
	// New keys: a new processor, the old one's monitors stopped.
	r.ownKeys(t, teamA, "sk_test_alpha2", "acct_A", "whsec_alpha")
	if !r.builds()[0].isStopped() {
		t.Fatal("old processor still runs after new keys")
	}
	p3, err := r.svc.For(ctx, a, entity.CARD_TEST)
	if err != nil || p3 == p1 || r.builds()[1].spec.SecretKey != "sk_test_alpha2" {
		t.Fatalf("rebuild: %v", err)
	}
	// A Connect row in another mode than the platform's never charges.
	if err := r.st.ConnectTeamStripe(ctx, teamB, platformstore.StripeAccountState{AccountID: "acct_B", Mode: ModeLive}, r.now); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.For(ctx, r.team(teamB), entity.CARD); status.Convert(err).Message() != MsgPaymentsNotReady {
		t.Fatalf("live account on a test platform: %v", err)
	}
	// Disconnected: refused, processor stopped.
	if err := r.svc.Disconnect(ctx, a); err != nil {
		t.Fatal(err)
	}
	if !r.builds()[1].isStopped() {
		t.Fatal("processor runs after disconnect")
	}
	if _, err := r.svc.For(ctx, a, entity.CARD_TEST); status.Convert(err).Message() != MsgPaymentsNotReady {
		t.Fatalf("after disconnect: %v", err)
	}
}

// TestStartWarmsAndStops: Start builds every live usable team's processor (monitors of unpaid orders
// restart after a deploy); Stop stops them all.
func TestStartWarmsAndStops(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	r.ownKeys(t, teamA, "sk_test_alpha", "acct_A", "whsec_alpha")
	if err := r.st.ConnectTeamStripe(ctx, teamB, platformstore.StripeAccountState{AccountID: "acct_B", Mode: ModeTest}, r.now); err != nil {
		t.Fatal(err)
	}
	r.stripe.accounts["acct_B"] = platformstore.StripeAccountState{AccountID: "acct_B", ChargesEnabled: true, Name: "bravo"}
	r.svc.Start(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for len(r.builds()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	r.svc.Stop(ctx)
	if len(r.builds()) != 2 {
		t.Fatalf("warmed %d processors", len(r.builds()))
	}
	for _, p := range r.builds() {
		if !p.isStopped() {
			t.Fatalf("team %d processor not stopped", p.spec.Team.ID)
		}
	}
	if !r.st.row(teamB).ChargesEnabled || r.st.row(teamB).AccountName != "bravo" {
		t.Fatalf("warm did not sync the account: %+v", r.st.row(teamB))
	}
	if _, err := r.svc.For(ctx, r.team(teamA), entity.CARD_TEST); status.Code(err) != codes.Unavailable {
		t.Fatalf("For after Stop: %v", err)
	}
}

func TestNotReadyWithoutMaster(t *testing.T) {
	svc := New(context.Background(), Deps{Config: Config{Master: "short"}, Store: newFakeStore(), Teams: &fakeTeams{},
		Factory: func(context.Context, ProcessorSpec) (Processor, error) { return nil, nil }})
	if err := svc.SetKeys(context.Background(), tenant.Team{ID: teamA, Slug: "alpha", Status: tenant.StatusActive}, "pk_test_1", "sk_test_1", "whsec_1"); status.Convert(err).Message() != "stripe_not_ready" {
		t.Fatalf("keys without master: %v", err)
	}
	if _, err := svc.StartConnect(context.Background(), tenant.Team{ID: teamA, Slug: "alpha", Status: tenant.StatusActive}, ownerA, false, ""); status.Convert(err).Message() != "stripe_not_ready" {
		t.Fatalf("connect without master: %v", err)
	}
	if svc.WebhookURL(teamA) != "" {
		t.Fatal("a webhook address without a master")
	}
	rec := httptest.NewRecorder()
	svc.ConnectWebhook().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, ConnectWebhookPath, strings.NewReader("{}")))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("connect webhook without config: %d", rec.Code)
	}
}

func TestStateAndPaths(t *testing.T) {
	k, err := newKeys(testMaster)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	tok := k.signState(connectState{Team: 2, User: 101, Exp: now.Add(time.Minute).Unix(), Nonce: "n1", Return: "/shop"})
	if s, err := k.verifyState(tok, now); err != nil || s.Team != 2 || s.User != 101 {
		t.Fatalf("verify: %+v %v", s, err)
	}
	other, _ := newKeys(testMaster + "-other")
	if _, err := other.verifyState(tok, now); err == nil {
		t.Fatal("a state verified under another master")
	}
	body, sig, _ := strings.Cut(tok, ".")
	forged := k.signState(connectState{Team: 3, User: 101, Exp: now.Add(time.Minute).Unix(), Nonce: "n1", Return: "/shop"})
	fBody, _, _ := strings.Cut(forged, ".")
	if _, err := k.verifyState(fBody+"."+sig, now); err == nil || body == fBody {
		t.Fatal("a state verified with another state's signature")
	}
	var n nonces
	if !n.use("n1", now.Add(time.Minute), now) || n.use("n1", now.Add(time.Minute), now) {
		t.Fatal("nonce reuse")
	}
	for p, ok := range map[string]bool{"": true, "/shop": true, "/shop?x=1": true, "//evil.com": false, "https://evil.com": false,
		"shop": false, "/a\\b": false, "/" + strings.Repeat("a", maxReturnPath): false} {
		if _, got := cleanReturn(p); got != ok {
			t.Fatalf("cleanReturn(%q) = %v", p, got)
		}
	}
	if k.webhookToken(2) == k.webhookToken(3) || k.webhookToken(2) == other.webhookToken(2) || len(k.webhookToken(2)) != webhookTokenLen {
		t.Fatal("webhook tokens collide")
	}
	if !k.webhookTokenIs(2, k.webhookToken(2)) || k.webhookTokenIs(3, k.webhookToken(2)) {
		t.Fatal("webhook token check")
	}
	c := Config{ConnectClientID: "ca_1", ConnectSecretKey: "sk_live_secret", ConnectWebhookSecret: "whsec_secret", Master: testMaster}
	if s := c.String(); strings.Contains(s, "sk_live_secret") || strings.Contains(s, "whsec_secret") || strings.Contains(s, testMaster) || strings.Contains(fmt.Sprintf("%#v %v %+v", c, c, c), "sk_live") {
		t.Fatalf("config prints a secret: %s", s)
	}
	if c.ConnectMode() != ModeLive || c.ConnectReady() || (Config{Master: testMaster}).ConnectReady() {
		t.Fatal("config readiness without the publishable key")
	}
	c.ConnectPublishableKey = "pk_test_x"
	if c.ConnectReady() {
		t.Fatal("a publishable key of the other mode is accepted")
	}
	c.ConnectPublishableKey = "pk_live_x"
	if !c.ConnectReady() {
		t.Fatal("config readiness")
	}
}

func TestConnectPublishableKeyOnlyWhileConnectIsOn(t *testing.T) {
	r := newRig(t)
	if got := r.svc.ConnectPublishableKey(); got != "pk_test_platform" {
		t.Fatalf("publishable key %q", got)
	}
	off := New(context.Background(), Deps{Config: Config{Master: testMaster, ConnectPublishableKey: "pk_test_platform"}, Store: r.st, Teams: r.teams, Factory: r.svc.d.Factory})
	if off.ConnectPublishableKey() != "" {
		t.Fatal("publishable key without Connect")
	}
}

// Logs carry Stripe's error code, never its message (it may echo the request).
func TestStripeErrCodeOnly(t *testing.T) {
	for err, want := range map[error]string{
		&stripe.Error{Msg: "No such code: ac_secret", OAuthError: "invalid_grant", HTTPStatusCode: 400}: "invalid_grant",
		&stripe.Error{Msg: "No such account: acct_x", Code: stripe.ErrorCodeResourceMissing}:            "resource_missing",
		errors.Join(ErrStripeUnavailable, &stripe.Error{HTTPStatusCode: 503}):                           "http_503",
		ErrBadSecretKey:              "bad_secret_key",
		errors.New("dial tcp: boom"): "no_answer",
	} {
		if got := stripeErrCode(err); got != want {
			t.Fatalf("stripeErrCode(%v) = %q, want %q", err, got, want)
		}
	}
}
