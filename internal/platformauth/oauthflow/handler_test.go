package oauthflow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth/oauthtest"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
)

const (
	rootOrigin = "https://beta.lekalo.studio"
	acmeOrigin = "https://acme.beta.lekalo.studio"
	resultURL  = "https://beta.lekalo.studio/auth/oauth"
)

// fakeFlow is the identity service: state rows in memory, completions answer what the test set.
type fakeFlow struct {
	mu        sync.Mutex
	states    map[string]platformstore.OAuthState
	created   []platformstore.OAuthState
	consumes  int
	createErr error

	inviteHash   string
	inviteTeam   tenant.Team
	inviteExists bool
	inviteErr    error

	code, key string
	signIns   []platformstore.ProviderSignIn
	accepts   []acceptCall

	// The Google connect (handler_link_test.go): the live ticket, whom it connects and where it
	// returns, the completion's key and what it was called with.
	linkTicket string
	linkUID    int64
	linkTeam   tenant.Team
	linkErr    error
	linkKey    string
	ticketsUse int
	links      []linkCall
}

type acceptCall struct {
	inviteHash, terms string
	in                platformstore.ProviderSignIn
}

func newFakeFlow() *fakeFlow {
	return &fakeFlow{
		states:     map[string]platformstore.OAuthState{},
		inviteHash: strings.Repeat("a", 64),
		inviteTeam: tenant.Team{ID: 7, Slug: "acme"},
		code:       "signin-code-value",
		linkTicket: "link-ticket-value",
		linkUID:    42,
		linkTeam:   tenant.Team{ID: 7, Slug: "acme"},
		linkKey:    KeyLinked,
	}
}

func (f *fakeFlow) OAuthInvite(_ context.Context, token string) (string, tenant.Team, bool, error) {
	if f.inviteErr != nil {
		return "", tenant.Team{}, false, f.inviteErr
	}
	if token != "invite-token-value" {
		return "", tenant.Team{}, false, platformstore.ErrTokenInvalid
	}
	return f.inviteHash, f.inviteTeam, f.inviteExists, nil
}

func (f *fakeFlow) TermsVersion() string { return "2026-11" }

func (f *fakeFlow) CreateOAuthState(_ context.Context, st platformstore.OAuthState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	if st.ExpiresAt.IsZero() {
		st.ExpiresAt = fakeExpires
	}
	f.states[st.StateHash] = st
	f.created = append(f.created, st)
	return nil
}

func (f *fakeFlow) ConsumeOAuthState(_ context.Context, h string) (platformstore.OAuthState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.consumes++
	st, ok := f.states[h]
	if !ok {
		return platformstore.OAuthState{}, platformstore.ErrOAuthStateInvalid
	}
	delete(f.states, h)
	return st, nil
}

func (f *fakeFlow) CompleteProviderSignIn(_ context.Context, in platformstore.ProviderSignIn) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signIns = append(f.signIns, in)
	if f.key != "" {
		return "", f.key
	}
	return f.code, ""
}

func (f *fakeFlow) AcceptInviteWithProvider(_ context.Context, inviteHash, terms string, in platformstore.ProviderSignIn) (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.accepts = append(f.accepts, acceptCall{inviteHash: inviteHash, terms: terms, in: in})
	if f.key != "" {
		return "", f.key
	}
	return f.code, ""
}

func (f *fakeFlow) lastCreated(t *testing.T) platformstore.OAuthState {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.created) == 0 {
		t.Fatal("no state row created")
	}
	return f.created[len(f.created)-1]
}

// fakeOrigins are the binder's rules over its own canonical form, so case variants of an admitted
// origin pass here as they do at the edge.
type fakeOrigins struct{}

func (fakeOrigins) RootOriginAllowed(o string) bool {
	return tenantbind.CanonicalOrigin(o) == rootOrigin
}
func (fakeOrigins) OriginAllowed(o string, t tenant.Team, legacy bool) bool {
	return !legacy && t.Slug != "" && tenantbind.CanonicalOrigin(o) == "https://"+t.Slug+".beta.lekalo.studio"
}
func (fakeOrigins) CanonicalOrigin(o string) string { return tenantbind.CanonicalOrigin(o) }

type rig struct {
	idp  *oauthtest.IdP
	flow *fakeFlow
	h    *Handler
}

// newRig wires the handler to a fake IdP through the real registry (BuildConfig included).
func newRig(t *testing.T, google, apple bool) *rig {
	t.Helper()
	idp := oauthtest.New(t)
	o := platformstore.OAuthConfig{}
	if google {
		o.GoogleClientID, o.GoogleClientSecret = "google-client", "google-secret"
	}
	if apple {
		o.AppleEnabled, o.AppleServicesID, o.AppleTeamID, o.AppleKeyID = "true", "apple-service", "apple-team", "apple-key"
		o.ApplePrivateKey = strings.ReplaceAll(strings.TrimSpace(p8Key(t)), "\n", `\n`)
	}
	reg, err := NewRegistry(platformCfg(o), oauth.WithEndpoints(idp.Endpoints()))
	if err != nil {
		t.Fatal(err)
	}
	flow := newFakeFlow()
	h := New(Deps{Flow: flow, Origins: fakeOrigins{}, Providers: reg, BaseDomain: "Beta.Lekalo.Studio."})
	t.Cleanup(h.Close)
	return &rig{idp: idp, flow: flow, h: h}
}

func (r *rig) serve(req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

// from makes req arrive from ip.
func from(req *http.Request, ip string) *http.Request {
	req.RemoteAddr = ip + ":4000"
	return req
}

func formReq(method, target, body, origin string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return req
}

// start posts a start and returns the response, its binder cookie and the provider URL.
func (r *rig) start(t *testing.T, provider, body, origin string) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	rec := r.serve(formReq(http.MethodPost, "/"+provider+"/start", body, origin))
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return rec, c
		}
	}
	return rec, nil
}

// authorize follows the provider URL at the fake IdP and returns what it hands back to the
// callback (code and state).
func (r *rig) authorize(t *testing.T, authURL string) (code, state string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusSeeOther {
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			t.Fatal(err)
		}
		return u.Query().Get("code"), u.Query().Get("state")
	}
	body, _ := io.ReadAll(resp.Body)
	m := regexp.MustCompile(`name="code" value="([^"]+)".*name="state" value="([^"]+)"`).FindSubmatch(body)
	if m == nil {
		t.Fatalf("authorize answered %d without a code", resp.StatusCode)
	}
	return string(m[1]), string(m[2])
}

func googleCallback(code, state string, cookie *http.Cookie) *http.Request {
	q := url.Values{}
	if code != "" {
		q.Set("code", code)
	}
	if state != "" {
		q.Set("state", state)
	}
	req := httptest.NewRequest(http.MethodGet, "/google/callback?"+q.Encode(), nil)
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	return req
}

func appleCallback(form url.Values, cookie *http.Cookie) *http.Request {
	req := formReq(http.MethodPost, "/apple/callback", form.Encode(), "https://appleid.apple.com")
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	return req
}

func wantResult(t *testing.T, rec *httptest.ResponseRecorder, fragment string) {
	t.Helper()
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d, want 303 to the result page (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Location"); got != resultURL+"#"+fragment {
		t.Fatalf("Location %q, want %s#%s", got, resultURL, fragment)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("the 303 to the result page carries a %d-byte body", rec.Body.Len())
	}
}

func setCookieHeader(rec *httptest.ResponseRecorder) string {
	return strings.Join(rec.Header().Values("Set-Cookie"), "\n")
}

// captureLogs points slog.Default at a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// The one table: every outcome row, every completion key, every provider error.
func TestKeyTable(t *testing.T) {
	rows := []struct {
		o    outcome
		want string
	}{
		{outStateInvalid, "expired"},
		{outBinderMismatch, "expired"},
		{outCancelled, "cancelled"},
		{outTokenInvalid, "try_again"},
		{outProviderUnreachable, "try_again"},
		{outUnverified, "unverified"},
		{outInviteMismatch, "invite_mismatch"},
		{outInviteGone, "expired"},
		{outDisabled, "disabled"},
		{outUsePassword, "use_password"},
		{outAccountDeleted, "account_deleted"},
		{outLimited, "try_again"},
		{outInternal, "try_again"},
	}
	if len(rows) != int(outInternal) {
		t.Fatalf("%d rows for %d outcomes: a new outcome needs its row", len(rows), int(outInternal))
	}
	keys := map[string]bool{}
	for _, r := range rows {
		if got := r.o.key(); got != r.want {
			t.Errorf("outcome %d: key %q, want %q", r.o, got, r.want)
		}
		keys[r.o.key()] = true
	}
	if len(keys) != 8 {
		t.Errorf("the table reaches %d keys, want the eight: %v", len(keys), keys)
	}
	if got := outcome(0).key(); got != KeyTryAgain {
		t.Errorf("an unset outcome answers %q", got)
	}
	completions := map[string]string{
		KeyUnverified: "unverified", KeyInviteMismatch: "invite_mismatch", KeyExpired: "expired",
		KeyDisabled: "disabled", KeyUsePassword: "use_password", KeyTryAgain: "try_again",
		KeyAccountDeleted: "account_deleted", KeyCancelled: "try_again", "": "try_again", "something_new": "try_again",
	}
	for k, want := range completions {
		if got := completionOutcome(k).key(); got != want {
			t.Errorf("completion key %q answers %q, want %q", k, got, want)
		}
	}
	provider := []struct {
		err  error
		want string
	}{
		{oauth.ErrCancelled, "cancelled"},
		{oauth.ErrTokenInvalid, "try_again"},
		{oauth.ErrProviderUnreachable, "try_again"},
		{errors.New("other"), "try_again"},
		{oauth.CallbackError(url.Values{"error": {"access_denied"}}), "cancelled"},
		{oauth.CallbackError(url.Values{"error": {"user_cancelled_authorize"}}), "cancelled"},
		{oauth.CallbackError(url.Values{"error": {"server_error"}}), "try_again"},
	}
	for _, p := range provider {
		if got := providerOutcome(p.err).key(); got != p.want {
			t.Errorf("provider error %v answers %q, want %q", p.err, got, p.want)
		}
	}
}

func TestMethodStrictness(t *testing.T) {
	r := newRig(t, true, true)
	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/google/start", http.StatusMethodNotAllowed},
		{http.MethodPut, "/apple/start", http.StatusMethodNotAllowed},
		{http.MethodPost, "/google/callback", http.StatusMethodNotAllowed},
		{http.MethodHead, "/google/callback", http.StatusMethodNotAllowed},
		{http.MethodGet, "/apple/callback", http.StatusMethodNotAllowed},
		{http.MethodPut, "/apple/callback", http.StatusMethodNotAllowed},
		{http.MethodPost, "/twitter/start", http.StatusNotFound},
		{http.MethodGet, "/twitter/callback", http.StatusNotFound},
		{http.MethodGet, "/", http.StatusNotFound},
	}
	for _, c := range cases {
		rec := r.serve(formReq(c.method, c.path, "target=signin", rootOrigin))
		if rec.Code != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, rec.Code, c.want)
		}
	}
	if len(r.flow.created) != 0 || r.flow.consumes != 0 || len(r.idp.LastAuthorizeRequest()) != 0 {
		t.Fatal("a refused method reached the flow or the provider")
	}
}

// Unknown or disabled provider: 404 on its start and callback; nothing set: every route 404.
func TestProviderNotAvailable(t *testing.T) {
	r := newRig(t, true, false)
	if rec := r.serve(formReq(http.MethodPost, "/apple/start", "target=signin", rootOrigin)); rec.Code != http.StatusNotFound {
		t.Errorf("apple start with apple off = %d, want 404", rec.Code)
	}
	if rec := r.serve(appleCallback(url.Values{"state": {"s"}, "code": {"c"}}, nil)); rec.Code != http.StatusNotFound {
		t.Errorf("apple callback with apple off = %d, want 404", rec.Code)
	}
	reg, err := NewRegistry(platformCfg(platformstore.OAuthConfig{}))
	if err != nil {
		t.Fatal(err)
	}
	flow := newFakeFlow()
	for _, d := range []Deps{
		{Flow: flow, Origins: fakeOrigins{}, Providers: reg, BaseDomain: "beta.lekalo.studio"}, // nothing set
		{},
		{Origins: fakeOrigins{}, Providers: r.h.d.Providers, BaseDomain: "beta.lekalo.studio"}, // no service
		{Flow: flow, Origins: fakeOrigins{}, Providers: r.h.d.Providers},                       // no base domain
	} {
		h := New(d)
		for _, req := range []*http.Request{
			formReq(http.MethodPost, "/google/start", "target=signin", rootOrigin),
			googleCallback("c", "s", nil),
			appleCallback(url.Values{"state": {"s"}}, nil),
		} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", req.Method, req.URL.Path, rec.Code)
			}
		}
		h.Close()
	}
	if len(flow.created) != 0 || flow.consumes != 0 {
		t.Fatal("an unavailable provider reached the flow")
	}
}

func TestStartOriginPerTarget(t *testing.T) {
	r := newRig(t, true, true)
	invite := "target=invite&invite_token=invite-token-value&terms_accepted=true"
	cases := []struct {
		name, body, origin string
		want               int
	}{
		{"signin from root", "target=signin", rootOrigin, http.StatusSeeOther},
		{"signin from a team host", "target=signin", acmeOrigin, http.StatusForbidden},
		{"signin without origin", "target=signin", "", http.StatusForbidden},
		{"signin from elsewhere", "target=signin", "https://evil.example", http.StatusForbidden},
		{"invite from its team host", invite, acmeOrigin, http.StatusSeeOther},
		{"invite from root", invite, rootOrigin, http.StatusForbidden},
		{"invite from another team", invite, "https://other.beta.lekalo.studio", http.StatusForbidden},
		{"invite without origin", invite, "", http.StatusForbidden},
		{"no target", "", rootOrigin, http.StatusBadRequest},
		{"unknown target", "target=console", rootOrigin, http.StatusBadRequest},
		{"invite without token", "target=invite", acmeOrigin, http.StatusBadRequest},
	}
	for _, c := range cases {
		before := len(r.flow.created)
		rec, cookie := r.start(t, "google", c.body, c.origin)
		if rec.Code != c.want {
			t.Errorf("%s: %d, want %d", c.name, rec.Code, c.want)
			continue
		}
		made := len(r.flow.created) - before
		if c.want == http.StatusSeeOther {
			if made != 1 || cookie == nil {
				t.Errorf("%s: %d rows, cookie %v", c.name, made, cookie != nil)
			}
		} else if made != 0 || cookie != nil {
			t.Errorf("%s: refused start made %d rows (cookie %v)", c.name, made, cookie != nil)
		}
	}
}

// The start's row, cookie and provider URL belong together: binder and state stored as hashes,
// the PKCE challenge of the row's verifier, the row's nonce.
func TestStartCookieAndRow(t *testing.T) {
	r := newRig(t, true, true)
	for _, p := range []string{"google", "apple"} {
		rec, cookie := r.start(t, p, "target=signin", rootOrigin)
		if rec.Code != http.StatusSeeOther || cookie == nil {
			t.Fatalf("%s start: %d, cookie %v", p, rec.Code, cookie != nil)
		}
		raw := setCookieHeader(rec)
		for _, attr := range []string{CookieName + "=", "Path=/", "Max-Age=600", "HttpOnly", "Secure", "SameSite=None"} {
			if !strings.Contains(raw, attr) {
				t.Errorf("%s cookie %q lacks %s", p, raw, attr)
			}
		}
		if strings.Contains(strings.ToLower(raw), "domain=") {
			t.Errorf("%s cookie %q carries a Domain (a __Host- cookie must not)", p, raw)
		}
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s start headers %v", p, rec.Header())
		}
		row := r.flow.lastCreated(t)
		loc, err := url.Parse(rec.Header().Get("Location"))
		if err != nil || !strings.HasPrefix(loc.String(), r.idp.URL()) {
			t.Fatalf("%s start redirects to %q, not the provider", p, rec.Header().Get("Location"))
		}
		q := loc.Query()
		if row.Provider != p || row.Target != "signin" || row.InviteHash != "" || row.TermsVersion != "" {
			t.Errorf("%s row %+v", p, row)
		}
		if row.BinderHash != hashHex(cookie.Value) || row.StateHash != hashHex(q.Get("state")) {
			t.Errorf("%s row does not hash the cookie and the state", p)
		}
		if q.Get("code_challenge") != oauth.Challenge(row.PKCEVerifier) || q.Get("code_challenge_method") != "S256" {
			t.Errorf("%s challenge is not the row verifier's", p)
		}
		if q.Get("nonce") != row.Nonce || row.Nonce == "" {
			t.Errorf("%s nonce is not the row's", p)
		}
		if strings.Contains(loc.String(), row.PKCEVerifier) || strings.Contains(loc.String(), cookie.Value) {
			t.Errorf("%s provider URL carries the verifier or the binder", p)
		}
		if strings.Contains(q.Get("redirect_uri"), "?") || q.Get("redirect_uri") != "https://api.beta.lekalo.studio/api/auth/oauth/"+p+"/callback" {
			t.Errorf("%s redirect_uri %q", p, q.Get("redirect_uri"))
		}
	}
}

func TestStartInvite(t *testing.T) {
	r := newRig(t, true, false)
	// A dead invite: the result page, no row.
	rec, _ := r.start(t, "google", "target=invite&invite_token=dead-token&terms_accepted=true", acmeOrigin)
	wantResult(t, rec, "error=expired")
	// A new account without the box ticked: refused before any row (the client must not let it).
	for _, terms := range []string{"", "false", "nope"} {
		rec, _ = r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted="+terms, acmeOrigin)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "terms_required") {
			t.Errorf("terms %q: %d %q, want 400 terms_required", terms, rec.Code, rec.Body.String())
		}
	}
	if len(r.flow.created) != 0 {
		t.Fatal("a refused invite start made a row")
	}
	rec, _ = r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted=true", acmeOrigin)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("invite start %d", rec.Code)
	}
	row := r.flow.lastCreated(t)
	if row.Target != "invite" || row.InviteHash != r.flow.inviteHash || row.TermsVersion != "2026-11" {
		t.Fatalf("invite row %+v", row)
	}
	// An existing account needs no terms.
	r.flow.inviteExists = true
	rec, _ = r.start(t, "google", "target=invite&invite_token=invite-token-value", acmeOrigin)
	if rec.Code != http.StatusSeeOther || r.flow.lastCreated(t).TermsVersion != "" {
		t.Fatalf("existing account invite start %d %+v", rec.Code, r.flow.lastCreated(t))
	}
	// The store failing: the result page.
	r.flow.createErr = errors.New("db down")
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	wantResult(t, rec, "error=try_again")
	if cookie != nil {
		t.Fatal("a start whose row was not stored set a cookie")
	}
}

func TestBodyCap(t *testing.T) {
	r := newRig(t, true, true)
	big := "target=signin&pad=" + strings.Repeat("x", maxBodyBytes)
	if rec := r.serve(formReq(http.MethodPost, "/google/start", big, rootOrigin)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("start over the cap = %d, want 413", rec.Code)
	}
	// Without a Content-Length the reader stops it.
	req := formReq(http.MethodPost, "/google/start", big, rootOrigin)
	req.ContentLength = -1
	req.Body = io.NopCloser(strings.NewReader(big))
	if rec := r.serve(req); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("chunked start over the cap = %d, want 413", rec.Code)
	}
	// Under the cap passes.
	if rec := r.serve(formReq(http.MethodPost, "/google/start", "target=signin&pad="+strings.Repeat("x", 1000), rootOrigin)); rec.Code != http.StatusSeeOther {
		t.Errorf("start under the cap = %d", rec.Code)
	}
	// Apple's callback over the cap: the result page, nothing consumed.
	consumes := r.flow.consumes
	rec := r.serve(formReq(http.MethodPost, "/apple/callback", "state=s&code=c&pad="+strings.Repeat("x", maxBodyBytes), "https://appleid.apple.com"))
	wantResult(t, rec, "error=try_again")
	if r.flow.consumes != consumes || len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("an oversized callback reached the flow or the provider")
	}
}

// The start: the loose guard per IP in front; the strict limit per IP + the origin's canonical
// form, counted only once the origin passed its rule.
func TestStartLimiters(t *testing.T) {
	r := newRig(t, true, false)
	// variant(i) upper-cases the letters of the root origin picked by the bits of i.
	variant := func(i int) string {
		b, bit := []byte(rootOrigin), 0
		for k, c := range b {
			if c >= 'a' && c <= 'z' {
				if i>>bit&1 == 1 {
					b[k] = c - 'a' + 'A'
				}
				bit++
			}
		}
		return string(b)
	}
	spellings := map[string]bool{}
	for i := 1; i <= perMinute; i++ {
		spellings[variant(i)] = true
		rec, _ := r.start(t, "google", "target=signin", variant(i))
		if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), r.idp.URL()) {
			t.Fatalf("start %d (%s) = %d", i, variant(i), rec.Code)
		}
	}
	if len(spellings) != perMinute || spellings[rootOrigin] {
		t.Fatalf("%d distinct spellings, want %d case variants", len(spellings), perMinute)
	}
	rows := len(r.flow.created)
	rec, cookie := r.start(t, "google", "target=signin", variant(perMinute+1))
	wantResult(t, rec, "error=try_again")
	if cookie != nil || len(r.flow.created) != rows {
		t.Fatal("a limited start made a row")
	}
	// Per IP + origin: the same IP from a team host still starts.
	if rec, _ := r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted=true", acmeOrigin); rec.Code != http.StatusSeeOther {
		t.Fatalf("start from another origin = %d", rec.Code)
	}

	// Origins the rules refuse never get a bucket, under any spelling.
	const ip = "198.51.100.20"
	for i := 0; i < 30; i++ {
		o := fmt.Sprintf("https://Evil-%d.Example", i)
		if rec := r.serve(from(formReq(http.MethodPost, "/google/start", "target=signin", o), ip)); rec.Code != http.StatusForbidden {
			t.Fatalf("refused origin %d = %d", i, rec.Code)
		}
		for _, key := range []string{ip + "|" + o, ip + "|" + strings.ToLower(o)} {
			if n := r.h.startLimit.GetRemaining(key); n != perMinute {
				t.Fatalf("a refused origin took a start slot (%d left)", n)
			}
		}
	}
	if rec := r.serve(from(formReq(http.MethodPost, "/google/start", "target=signin", rootOrigin), ip)); rec.Code != http.StatusSeeOther {
		t.Fatalf("a valid start after refused origins = %d", rec.Code)
	}
	if n := r.h.startLimit.GetRemaining(ip + "|" + rootOrigin); n != perMinute-1 {
		t.Fatalf("the root bucket has %d left, want %d", n, perMinute-1)
	}

	// The guard: guardPerMinute starts per IP whatever they carry; the next is refused before the
	// form or the origin is read.
	const ip2 = "198.51.100.21"
	for i := 0; i < guardPerMinute; i++ {
		if rec := r.serve(from(formReq(http.MethodPost, "/google/start", "target=signin", "https://evil.example"), ip2)); rec.Code != http.StatusForbidden {
			t.Fatalf("guarded start %d = %d", i+1, rec.Code)
		}
	}
	rows = len(r.flow.created)
	wantResult(t, r.serve(from(formReq(http.MethodPost, "/google/start", "target=signin", rootOrigin), ip2)), "error=try_again")
	if len(r.flow.created) != rows {
		t.Fatal("a guarded start made a row")
	}
}

// The callback: junk (no live state) from a shared address never spends a person's flow — the
// strict per-IP limit sits right before the exchange it protects; the loose guard in front only
// caps how often one address reaches the state table.
func TestCallbackLimiters(t *testing.T) {
	r := newRig(t, true, false)
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	junk := 2 * perMinute
	for i := 0; i < junk; i++ {
		wantResult(t, r.serve(googleCallback("c", "unknown-state", nil)), "error=expired")
	}
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "code="+r.flow.code)
	if r.idp.LastTokenRequest().Get("code") != code {
		t.Fatal("junk callbacks kept a valid flow from the exchange")
	}
	// The guard: guardPerMinute callbacks per IP in all; the next is refused before the state table.
	for i := junk + 1; i < guardPerMinute; i++ {
		wantResult(t, r.serve(googleCallback("c", "unknown-state", nil)), "error=expired")
	}
	consumes := r.flow.consumes
	wantResult(t, r.serve(googleCallback("c", "unknown-state", nil)), "error=try_again")
	if r.flow.consumes != consumes {
		t.Fatal("a guarded callback reached the state table")
	}
	wantResult(t, r.serve(from(googleCallback("c", "unknown-state", nil), "198.51.100.7")), "error=expired")

	// The strict limit: perMinute exchanges per IP; the next flow is spent (row used, cookie
	// cleared) without calling the provider.
	r = newRig(t, true, false)
	for i := 0; i < perMinute; i++ {
		rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
		code, state := r.authorize(t, rec.Header().Get("Location"))
		wantResult(t, r.serve(googleCallback(code, state, cookie)), "code="+r.flow.code)
	}
	rec, cookie = r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted=true", acmeOrigin)
	code, state = r.authorize(t, rec.Header().Get("Location"))
	consumes = r.flow.consumes
	cb := r.serve(googleCallback(code, state, cookie))
	wantResult(t, cb, "error=try_again")
	if r.flow.consumes != consumes+1 || !strings.Contains(setCookieHeader(cb), "Max-Age=0") {
		t.Fatal("the limited callback did not spend its row and clear the cookie")
	}
	if r.idp.LastTokenRequest().Get("code") == code || len(r.flow.signIns)+len(r.flow.accepts) != perMinute {
		t.Fatal("the limited callback reached the provider or a completion")
	}
}

// Every 303 of a flow is a Location and nothing else: http.Redirect would echo the target into an
// HTML body on a GET — the sign-in code of the result page, the state and nonce of a provider URL.
func TestRedirectsCarryNoBody(t *testing.T) {
	r := newRig(t, true, false)
	empty := func(what string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") == "" {
			t.Fatalf("%s: %d without a Location", what, rec.Code)
		}
		if rec.Body.Len() != 0 || rec.Header().Get("Content-Type") != "" {
			t.Fatalf("%s: a %d-byte body (%q)", what, rec.Body.Len(), rec.Header().Get("Content-Type"))
		}
	}
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	empty("start", rec)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	cb := r.serve(googleCallback(code, state, cookie))
	empty("successful callback", cb)
	if !strings.HasPrefix(cb.Header().Get("Location"), resultURL+"#code=") {
		t.Fatal("the successful callback did not land on the result page with a code")
	}
	failed := r.serve(googleCallback(code, state, cookie))
	empty("failing callback", failed)
	if failed.Header().Get("Location") != resultURL+"#error=expired" {
		t.Fatal("the replayed callback did not land on expired")
	}
	empty("refused start", r.serve(formReq(http.MethodPost, "/google/start", "target=invite&invite_token=dead-token&terms_accepted=true", acmeOrigin)))
}

// The happy path, Google: start → provider → callback → completion, the cookie cleared, the code on
// the result page, the row's verifier and nonce used, nothing of the flow in the logs.
func TestGoogleFlow(t *testing.T) {
	logs := captureLogs(t)
	r := newRig(t, true, false)
	hd := "example.com"
	claims := oauthtest.DefaultClaims("https://accounts.google.com", "google-client")
	claims.HostedDomain = &hd
	r.idp.SetClaims(claims)
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	row := r.flow.lastCreated(t)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	cb := r.serve(googleCallback(code, state, cookie))
	wantResult(t, cb, "code="+r.flow.code)
	if raw := setCookieHeader(cb); !strings.Contains(raw, CookieName+"=;") || !strings.Contains(raw, "Max-Age=0") ||
		!strings.Contains(raw, "SameSite=None") || !strings.Contains(raw, "Secure") {
		t.Errorf("the cookie was not cleared: %q", raw)
	}
	tok := r.idp.LastTokenRequest()
	if tok.Get("code_verifier") != row.PKCEVerifier || tok.Get("code") != code {
		t.Error("the exchange did not use the row's verifier")
	}
	if len(r.flow.signIns) != 1 || len(r.flow.accepts) != 0 {
		t.Fatalf("completions: %d sign-ins, %d invites", len(r.flow.signIns), len(r.flow.accepts))
	}
	in := r.flow.signIns[0]
	if in.Provider != "google" || in.Subject != "subject-1" || in.Email != "person@example.com" || !in.EmailVerified || !in.EmailAuthoritative {
		t.Fatalf("identity handed on %+v", in)
	}
	// Replay: the row is spent.
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "error=expired")
	if len(r.flow.signIns) != 1 {
		t.Fatal("a replayed callback completed twice")
	}
	for _, secret := range []string{code, state, cookie.Value, row.PKCEVerifier, row.Nonce, r.flow.code} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("a flow value reached the logs")
		}
	}
	// The completion's refusal keys pass through the table.
	for _, key := range []string{"unverified", "disabled", "use_password", "account_deleted", "try_again", "bogus"} {
		r.flow.key = key
		rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
		code, state := r.authorize(t, rec.Header().Get("Location"))
		want := key
		if key == "bogus" {
			want = "try_again"
		}
		wantResult(t, r.serve(googleCallback(code, state, cookie)), "error="+want)
	}
}

// The invite target goes to the invite completion with the row's invite hash and terms.
func TestInviteFlow(t *testing.T) {
	r := newRig(t, true, false)
	rec, cookie := r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted=true", acmeOrigin)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "code="+r.flow.code)
	if len(r.flow.accepts) != 1 || len(r.flow.signIns) != 0 {
		t.Fatalf("completions: %d invites, %d sign-ins", len(r.flow.accepts), len(r.flow.signIns))
	}
	if a := r.flow.accepts[0]; a.inviteHash != r.flow.inviteHash || a.terms != "2026-11" || a.in.Subject != "subject-1" {
		t.Fatalf("invite completion %+v", a)
	}
	for _, key := range []string{"invite_mismatch", "expired", "use_password"} {
		r.flow.key = key
		rec, cookie := r.start(t, "google", "target=invite&invite_token=invite-token-value&terms_accepted=true", acmeOrigin)
		code, state := r.authorize(t, rec.Header().Get("Location"))
		wantResult(t, r.serve(googleCallback(code, state, cookie)), "error="+key)
	}
}

// The order: a callback without the binder cookie (or with another flow's) spends the row and
// never reaches the provider; a provider error is read before any exchange.
func TestCallbackOrder(t *testing.T) {
	r := newRig(t, true, true)
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	code, state := r.authorize(t, rec.Header().Get("Location"))

	cb := r.serve(googleCallback(code, state, nil))
	wantResult(t, cb, "error=expired")
	if len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("a callback without the cookie reached the provider")
	}
	if setCookieHeader(cb) != "" {
		t.Error("a callback without the cookie touched the cookie")
	}
	// The row is spent: the right cookie now finds nothing.
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "error=expired")
	if len(r.idp.LastTokenRequest()) != 0 || len(r.flow.signIns) != 0 {
		t.Fatal("a spent row reached the provider")
	}

	// Another flow's cookie: refused, the cookie left alone (it may be a later tab's).
	rec, _ = r.start(t, "google", "target=signin", rootOrigin)
	code, state = r.authorize(t, rec.Header().Get("Location"))
	cb = r.serve(googleCallback(code, state, &http.Cookie{Name: CookieName, Value: "not-this-flow"}))
	wantResult(t, cb, "error=expired")
	if setCookieHeader(cb) != "" || len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("a foreign cookie was cleared or reached the provider")
	}

	// A Google state on Apple's callback: the row is Google's, refused before the cookie.
	rec, cookie = r.start(t, "google", "target=signin", rootOrigin)
	_, state = r.authorize(t, rec.Header().Get("Location"))
	wantResult(t, r.serve(appleCallback(url.Values{"state": {state}, "code": {"c"}}, cookie)), "error=expired")
	if len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("a cross-provider state reached the provider")
	}

	// The provider said no: cancelled, no exchange; any other provider error: try_again + one log.
	logs := captureLogs(t)
	for _, c := range []struct{ errCode, want string }{{"access_denied", "cancelled"}, {"server_error", "try_again"}} {
		rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
		_, state := r.authorize(t, rec.Header().Get("Location"))
		q := url.Values{"state": {state}, "error": {c.errCode}, "error_description": {"desc-value-7"}}
		req := httptest.NewRequest(http.MethodGet, "/google/callback?"+q.Encode(), nil)
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie.Value})
		wantResult(t, r.serve(req), "error="+c.want)
	}
	if len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("a provider error reached the exchange")
	}
	if n := strings.Count(logs.String(), "oauth: provider unreachable"); n != 1 {
		t.Fatalf("%d unreachable lines, want 1: %q", n, logs.String())
	}
	if !strings.Contains(logs.String(), "provider=google") || strings.Contains(logs.String(), "desc-value-7") {
		t.Fatalf("unreachable line %q", logs.String())
	}

	// Token endpoint refusing (invalid_grant): try_again, no unreachable line; failing: one more line.
	logs.Reset()
	r.idp.SetTokenResponse(http.StatusBadRequest, []byte(`{"error":"invalid_grant"}`))
	rec, cookie = r.start(t, "google", "target=signin", rootOrigin)
	code, state = r.authorize(t, rec.Header().Get("Location"))
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "error=try_again")
	if strings.Contains(logs.String(), "provider unreachable") {
		t.Fatalf("an invalid token was logged as unreachable: %q", logs.String())
	}
	r.idp.SetTokenResponse(http.StatusBadGateway, []byte(`oops`))
	rec, cookie = r.start(t, "google", "target=signin", rootOrigin)
	code, state = r.authorize(t, rec.Header().Get("Location"))
	wantResult(t, r.serve(googleCallback(code, state, cookie)), "error=try_again")
	if n := strings.Count(logs.String(), "oauth: provider unreachable"); n != 1 || strings.Contains(logs.String(), code) {
		t.Fatalf("unreachable logging %q", logs.String())
	}
	if len(r.flow.signIns) != 0 {
		t.Fatal("a failed exchange completed")
	}
	// No state, or one too long: expired without a lookup.
	consumes := r.flow.consumes
	wantResult(t, r.serve(googleCallback("c", "", cookie)), "error=expired")
	wantResult(t, r.serve(googleCallback("c", strings.Repeat("s", maxStateLen+1), cookie)), "error=expired")
	if r.flow.consumes != consumes {
		t.Fatal("an empty or oversized state was looked up")
	}
}

// Google's values come from the query only; Apple's from the POST body only.
func TestCallbackValueSources(t *testing.T) {
	r := newRig(t, true, true)
	rec, cookie := r.start(t, "google", "target=signin", rootOrigin)
	code, state := r.authorize(t, rec.Header().Get("Location"))
	req := formReq(http.MethodGet, "/google/callback", url.Values{"state": {state}, "code": {code}}.Encode(), "")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie.Value})
	wantResult(t, r.serve(req), "error=expired")

	r.idp.SetClaims(oauthtest.DefaultClaims("https://appleid.apple.com", "apple-service"))
	rec, cookie = r.start(t, "apple", "target=signin", rootOrigin)
	code, state = r.authorize(t, rec.Header().Get("Location"))
	q := url.Values{"state": {state}, "code": {code}}
	req = formReq(http.MethodPost, "/apple/callback?"+q.Encode(), "", "https://appleid.apple.com")
	req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie.Value})
	wantResult(t, r.serve(req), "error=expired")
	if len(r.idp.LastTokenRequest()) != 0 || len(r.flow.signIns) != 0 {
		t.Fatal("a value from the wrong place reached the provider")
	}
}

// Apple's form_post: a cross-site POST carrying the SameSite=None cookie; its user field is never
// read (the identity is the id_token's).
func TestAppleFormPost(t *testing.T) {
	r := newRig(t, false, true)
	r.idp.SetClaims(oauthtest.DefaultClaims("https://appleid.apple.com", "apple-service"))
	rec, cookie := r.start(t, "apple", "target=signin", rootOrigin)
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if loc.Query().Get("response_mode") != "form_post" {
		t.Fatalf("apple authorize URL without form_post: %q", loc.RawQuery)
	}
	code, state := r.authorize(t, rec.Header().Get("Location"))
	form := url.Values{"state": {state}, "code": {code}, "user": {`{"email":"someone-else@example.com","name":{"firstName":"X"}}`}}
	cb := r.serve(appleCallback(form, cookie))
	wantResult(t, cb, "code="+r.flow.code)
	if !strings.Contains(setCookieHeader(cb), "Max-Age=0") {
		t.Error("the cookie was not cleared")
	}
	if len(r.flow.signIns) != 1 {
		t.Fatalf("%d sign-ins", len(r.flow.signIns))
	}
	if in := r.flow.signIns[0]; in.Provider != "apple" || in.Email != "person@example.com" || !in.EmailAuthoritative {
		t.Fatalf("apple identity %+v", in)
	}
}

// Two tabs: the later start owns the cookie; the earlier tab's callback is refused as expired
// without reaching the provider and leaves the later flow intact.
func TestTwoTabs(t *testing.T) {
	r := newRig(t, true, false)
	recA, _ := r.start(t, "google", "target=signin", rootOrigin)
	recB, cookieB := r.start(t, "google", "target=signin", rootOrigin)
	codeA, stateA := r.authorize(t, recA.Header().Get("Location"))
	codeB, stateB := r.authorize(t, recB.Header().Get("Location"))
	cbA := r.serve(googleCallback(codeA, stateA, cookieB)) // the browser now holds B's cookie
	wantResult(t, cbA, "error=expired")
	if setCookieHeader(cbA) != "" || len(r.idp.LastTokenRequest()) != 0 {
		t.Fatal("the earlier tab touched the cookie or reached the provider")
	}
	wantResult(t, r.serve(googleCallback(codeB, stateB, cookieB)), "code="+r.flow.code)
}
