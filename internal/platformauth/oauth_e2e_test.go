package platformauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth/oauthtest"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauthflow"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
)

// Provider sign-in end to end (teams O3): the real start and callback handlers, a fake IdP behind
// the real provider registry, and this package's Service on the test MySQL as the identity flow.

const (
	oauthRootOrigin = "https://beta.lekalo.studio"
	oauthResultPage = "https://beta.lekalo.studio/auth/oauth#"
)

// The Service is the callback's identity flow, and its refusal keys are the ones the result page
// reads (no database).
func TestOAuthFlowContract(t *testing.T) {
	var _ oauthflow.Flow = (*Service)(nil)
	for _, k := range []struct{ svc, page string }{
		{KeyUnverified, oauthflow.KeyUnverified}, {KeyDisabled, oauthflow.KeyDisabled},
		{KeyUsePassword, oauthflow.KeyUsePassword}, {KeyTryAgain, oauthflow.KeyTryAgain},
		{KeyInviteMismatch, oauthflow.KeyInviteMismatch}, {KeyExpired, oauthflow.KeyExpired},
	} {
		if k.svc != k.page {
			t.Errorf("service key %q, result page key %q", k.svc, k.page)
		}
	}
}

// e2eOrigins: the root host for a sign-in start, the team's own host for an invite start.
type e2eOrigins struct{}

func (e2eOrigins) RootOriginAllowed(o string) bool { return o == oauthRootOrigin }
func (e2eOrigins) OriginAllowed(o string, t tenant.Team, legacy bool) bool {
	return !legacy && t.Slug != "" && o == "https://"+t.Slug+".beta.lekalo.studio"
}

type oauthE2E struct {
	t   *testing.T
	f   *flow
	idp *oauthtest.IdP
	h   *oauthflow.Handler
	ip  string
}

func appleP8(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})))
}

// newOAuthE2E wires the handlers to f's Service; both providers on, the Apple key as a one-line env
// value. Every request comes from ip.
func newOAuthE2E(t *testing.T, f *flow, ip string) *oauthE2E {
	t.Helper()
	idp := oauthtest.New(t)
	reg, err := oauthflow.NewRegistry(platformstore.Config{BaseDomain: "beta.lekalo.studio", OAuth: platformstore.OAuthConfig{
		GoogleClientID: "google-client", GoogleClientSecret: "google-secret",
		AppleEnabled: "true", AppleServicesID: "apple-service", AppleTeamID: "apple-team", AppleKeyID: "apple-key",
		ApplePrivateKey: strings.ReplaceAll(appleP8(t), "\n", `\n`),
	}}, oauth.WithEndpoints(idp.Endpoints()))
	if err != nil {
		t.Fatal(err)
	}
	h := oauthflow.New(oauthflow.Deps{Flow: f.svc, Origins: e2eOrigins{}, Providers: reg, BaseDomain: "beta.lekalo.studio"})
	t.Cleanup(h.Close)
	return &oauthE2E{t: t, f: f, idp: idp, h: h, ip: ip}
}

func (e *oauthE2E) serve(req *http.Request) *httptest.ResponseRecorder {
	req.RemoteAddr = e.ip + ":40000"
	req = req.WithContext(context.WithValue(req.Context(), middleware.ClientIPKey, e.ip))
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

// start posts the start form from origin: the response and the binder cookie it set (nil = none).
func (e *oauthE2E) start(provider string, form url.Values, origin string) (*httptest.ResponseRecorder, *http.Cookie) {
	req := httptest.NewRequest(http.MethodPost, "/"+provider+"/start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", origin)
	rec := e.serve(req)
	for _, c := range rec.Result().Cookies() {
		if c.Name == oauthflow.CookieName {
			return rec, c
		}
	}
	return rec, nil
}

// authorize is the person consenting at the provider: what it hands back to the callback.
func (e *oauthE2E) authorize(authURL string) (code, state string) {
	e.t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(authURL)
	if err != nil {
		e.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusSeeOther { // Google: back by redirect
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			e.t.Fatal(err)
		}
		return u.Query().Get("code"), u.Query().Get("state")
	}
	body, _ := io.ReadAll(resp.Body) // Apple: an auto-submitting form_post page
	m := regexp.MustCompile(`name="code" value="([^"]+)".*name="state" value="([^"]+)"`).FindSubmatch(body)
	if m == nil {
		e.t.Fatalf("authorize answered %d without a code", resp.StatusCode)
	}
	return string(m[1]), string(m[2])
}

// callback is the browser coming back: Google by GET with the query, Apple by a cross-site
// form_post from appleid.apple.com; extra goes into Apple's form.
func (e *oauthE2E) callback(provider, code, state string, cookie *http.Cookie, extra url.Values) *httptest.ResponseRecorder {
	v := url.Values{"code": {code}, "state": {state}}
	for k, vs := range extra {
		v[k] = vs
	}
	var req *http.Request
	if provider == "apple" {
		req = httptest.NewRequest(http.MethodPost, "/apple/callback", strings.NewReader(v.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Origin", "https://appleid.apple.com")
	} else {
		req = httptest.NewRequest(http.MethodGet, "/google/callback?"+v.Encode(), nil)
	}
	if cookie != nil {
		req.AddCookie(&http.Cookie{Name: cookie.Name, Value: cookie.Value})
	}
	return e.serve(req)
}

// run is one whole round from the given origin: start, consent, callback.
func (e *oauthE2E) run(provider string, form url.Values, origin string) (code, key string) {
	e.t.Helper()
	rec, cookie := e.start(provider, form, origin)
	if rec.Code != http.StatusSeeOther || cookie == nil {
		e.t.Fatalf("start answered %d (cookie set: %t)", rec.Code, cookie != nil)
	}
	c, s := e.authorize(rec.Header().Get("Location"))
	return oauthOutcome(e.t, e.callback(provider, c, s, cookie, nil))
}

func (e *oauthE2E) exchange(code string) *auth.ExchangeSignInCodeResponse {
	e.t.Helper()
	ex, err := e.f.svc.ExchangeSignInCode(ctxFrom(e.ip, ""), &auth.ExchangeSignInCodeRequest{Code: code})
	if err != nil || ex.GetToken() == "" {
		e.t.Fatalf("exchange of the sign-in code: %v", err)
	}
	return ex
}

// oauthOutcome reads the 303 to the one result page: the sign-in code or the refusal key. Neither
// is printed.
func oauthOutcome(t *testing.T, rec *httptest.ResponseRecorder) (code, key string) {
	t.Helper()
	loc := rec.Header().Get("Location")
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(loc, oauthResultPage) {
		t.Fatalf("answered %d, to the result page: %t", rec.Code, strings.HasPrefix(loc, oauthResultPage))
	}
	frag, err := url.ParseQuery(strings.TrimPrefix(loc, oauthResultPage))
	if err != nil || len(frag) != 1 || (frag.Get("code") == "") == (frag.Get("error") == "") {
		t.Fatal("the result fragment is not exactly one code= or error=")
	}
	return frag.Get("code"), frag.Get("error")
}

func googleID(sub, email, hostedDomain string) oauthtest.Claims {
	c := oauthtest.DefaultClaims("https://accounts.google.com", "google-client")
	c.Subject, c.Email = &sub, &email
	if hostedDomain != "" {
		c.HostedDomain = &hostedDomain
	}
	return c
}

func appleID(sub, email string) oauthtest.Claims {
	c := oauthtest.DefaultClaims("https://appleid.apple.com", "apple-service")
	c.Subject, c.Email = &sub, &email
	return c
}

func (f *flow) oauthCount(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.st.DB().Get(&n, q, args...); err != nil {
		f.t.Fatal(err)
	}
	return n
}

// Sign-in through the provider routes (teams O3 e2e): not authoritative → unverified with nothing
// written; a first sign-in makes an account with no team; a replayed callback is expired; a
// password account links, keeps its password, reaches its team by hand-off, and its root sign-out
// ends the team session; two tabs; Apple's form_post with the SameSite=None cookie; an operator
// is sent to the password.
func TestOAuthE2ESignIn(t *testing.T) {
	j := newJoinTeam(t, "oauth-e2e")
	e := newOAuthE2E(t, j.flow, "10.9.0.1")
	ctx := ctxFrom("10.9.0.1", "")
	signin := url.Values{"target": {"signin"}}

	// Verified but not authoritative (no hosted domain, not gmail): unverified, nothing written.
	sessions := j.oauthCount(`SELECT COUNT(*) FROM user_session`)
	e.idp.SetClaims(googleID("g-o3-unv", "delivered+o3unv@resend.dev", ""))
	if code, key := e.run("google", signin, oauthRootOrigin); code != "" || key != oauthflow.KeyUnverified {
		t.Fatalf("not authoritative: code %t, key %q", code != "", key)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM platform_user WHERE email = 'delivered+o3unv@resend.dev'`) +
		j.oauthCount(`SELECT COUNT(*) FROM platform_identity`) +
		j.oauthCount(`SELECT COUNT(*) FROM user_session`) - sessions; n != 0 {
		t.Fatalf("an unverified sign-in wrote %d rows", n)
	}

	// First sign-in: an account with no team.
	const newEmail = "delivered+o3new@resend.dev"
	e.idp.SetClaims(googleID("g-o3-new", newEmail, "resend.dev"))
	rec, cookie := e.start("google", signin, oauthRootOrigin)
	c, s := e.authorize(rec.Header().Get("Location"))
	code, key := oauthOutcome(t, e.callback("google", c, s, cookie, nil))
	if key != "" {
		t.Fatalf("first sign-in: %q", key)
	}
	ex := e.exchange(code)
	if len(ex.GetTeams()) != 0 || ex.GetEnterTeamSlug() != "" {
		t.Fatalf("first sign-in: %d teams, enter %q", len(ex.GetTeams()), ex.GetEnterTeamSlug())
	}
	my, err := j.svc.ListMyTeams(ctx, ex.GetToken(), &auth.ListMyTeamsRequest{})
	if err != nil || my.GetEmail() != newEmail || len(my.GetTeams()) != 0 {
		t.Fatalf("your teams after a first sign-in = %v, %v", my, err)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM platform_identity i JOIN platform_user u ON u.id = i.user_id
WHERE u.email = ? AND i.provider = 'google' AND i.subject = 'g-o3-new'`, newEmail); n != 1 {
		t.Fatalf("%d identities for the new account", n)
	}

	// The same callback again (state, code, cookie): expired, no second sign-in code.
	tokens := j.oauthCount(`SELECT COUNT(*) FROM email_token`)
	if _, key := oauthOutcome(t, e.callback("google", c, s, cookie, nil)); key != oauthflow.KeyExpired {
		t.Fatalf("replayed callback: %q", key)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM email_token`); n != tokens {
		t.Fatalf("a replayed callback issued %d codes", n-tokens)
	}

	// The owner's password account: Google links it (authoritative), the password stays; your teams
	// → hand-off → team token; the root sign-out ends the team session (the edge's 401).
	const owner, ownerPW, slug = "delivered+b3oauth-e2e-owner@resend.dev", "owner-password-1", "oauth-e2e"
	var ownerHandle, hashBefore, hashAfter string
	if err := j.st.DB().QueryRow(`SELECT handle, password_hash FROM platform_user WHERE email = ?`, owner).Scan(&ownerHandle, &hashBefore); err != nil {
		t.Fatal(err)
	}
	j.teams.ensured[ownerHandle] = nil // the provisioner wrote the owner as the team's super
	e.idp.SetClaims(googleID("g-o3-owner", owner, "resend.dev"))
	code, key = e.run("google", signin, oauthRootOrigin)
	if key != "" {
		t.Fatalf("owner sign-in: %q", key)
	}
	ex = e.exchange(code)
	if len(ex.GetTeams()) != 1 || ex.GetTeams()[0].GetSlug() != slug {
		t.Fatalf("owner sign-in teams = %v", ex.GetTeams())
	}
	root := ex.GetToken()
	if my, err := j.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{}); err != nil || len(my.GetTeams()) != 1 || my.GetTeams()[0].GetSlug() != slug {
		t.Fatalf("owner's teams = %v, %v", my, err)
	}
	ih, err := j.svc.IssueHandoff(ctx, root, &auth.IssueHandoffRequest{TeamSlug: slug})
	if err != nil || ih.GetCode() == "" {
		t.Fatalf("hand-off: %v", err)
	}
	tctx := ctxFrom("10.9.0.1", slug)
	hx, err := j.svc.ExchangeHandoff(tctx, &auth.ExchangeHandoffRequest{Code: ih.GetCode()})
	if err != nil || hx.GetToken() == "" {
		t.Fatalf("hand-off exchange: %v", err)
	}
	teamTok := hx.GetToken()
	teamLive := func() bool { // the edge's check of a team token (apisrv/auth AdminAuthz → 401)
		t.Helper()
		cl, err := jwt.VerifyTeamToken(j.svc.d.JWT, teamTok, &jwt.Expectations{Audience: jwt.AudTeam})
		if err != nil {
			t.Fatal("the team token does not verify")
		}
		_, ok, err := j.st.TeamSession(context.Background(), cl.SessionID, cl.UserID, cl.TeamID)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !teamLive() {
		t.Fatal("the team session is not live after the hand-off")
	}
	if _, err := j.svc.SetTeamLogo(tctx, teamTok, &auth.SetTeamLogoRequest{}); err != nil {
		t.Fatalf("team call with the team token: %v", err)
	}
	if err := j.st.DB().Get(&hashAfter, `SELECT password_hash FROM platform_user WHERE email = ?`, owner); err != nil || hashAfter != hashBefore {
		t.Fatalf("the provider link touched the password (%v)", err)
	}
	if _, err := j.svc.SignIn(ctxFrom("10.9.0.2", ""), &auth.SignInRequest{Login: owner, Password: ownerPW, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT}); err != nil {
		t.Fatalf("password sign-in after the link: %v", err)
	}
	if _, err := j.svc.SignOut(ctx, root); err != nil {
		t.Fatal(err)
	}
	_, err = j.svc.ListMyTeams(ctx, root, &auth.ListMyTeamsRequest{})
	wantCode(t, err, codes.Unauthenticated, "your teams after the root sign-out")
	if teamLive() {
		t.Fatal("the team session outlived the root sign-out")
	}
	_, err = j.svc.SetTeamLogo(tctx, teamTok, &auth.SetTeamLogoRequest{})
	wantCode(t, err, codes.Unauthenticated, "team call after the root sign-out")

	// Two tabs. The later start owns the cookie: the earlier tab comes back with it and is expired,
	// the cookie left alone, the provider not asked; the later tab then signs in.
	e.idp.SetClaims(googleID("g-o3-new", newEmail, "resend.dev"))
	recA, _ := e.start("google", signin, oauthRootOrigin)
	recB, cookieB := e.start("google", signin, oauthRootOrigin)
	cA, sA := e.authorize(recA.Header().Get("Location"))
	cB, sB := e.authorize(recB.Header().Get("Location"))
	cbA := e.callback("google", cA, sA, cookieB, nil)
	if _, key := oauthOutcome(t, cbA); key != oauthflow.KeyExpired {
		t.Fatalf("earlier tab: %q", key)
	}
	if len(cbA.Header().Values("Set-Cookie")) != 0 || e.idp.LastTokenRequest().Get("code") == cA {
		t.Fatal("the earlier tab touched the cookie or reached the provider")
	}
	if code, key := oauthOutcome(t, e.callback("google", cB, sB, cookieB, nil)); key != "" {
		t.Fatalf("later tab: %q", key)
	} else if my, err := j.svc.ListMyTeams(ctx, e.exchange(code).GetToken(), &auth.ListMyTeamsRequest{}); err != nil || my.GetEmail() != newEmail {
		t.Fatalf("later tab's account = %v, %v", my, err)
	}
	// The other order: the later tab finishes first and clears the cookie; the earlier has none.
	recA, _ = e.start("google", signin, oauthRootOrigin)
	recB, cookieB = e.start("google", signin, oauthRootOrigin)
	cA, sA = e.authorize(recA.Header().Get("Location"))
	cB, sB = e.authorize(recB.Header().Get("Location"))
	cbB := e.callback("google", cB, sB, cookieB, nil)
	if _, key := oauthOutcome(t, cbB); key != "" || !strings.Contains(strings.Join(cbB.Header().Values("Set-Cookie"), ";"), "Max-Age=0") {
		t.Fatalf("later tab first: key %q, cookie cleared: %v", key, cbB.Header().Values("Set-Cookie") != nil)
	}
	if _, key := oauthOutcome(t, e.callback("google", cA, sA, nil, nil)); key != oauthflow.KeyExpired {
		t.Fatalf("earlier tab without the cookie: %q", key)
	}

	// Apple: the start sets the SameSite=None binder; Apple's cross-site form_post carries it back;
	// the user field is never read (the identity is the id_token's).
	const appleEmail = "delivered+o3apple@resend.dev"
	e.idp.SetClaims(appleID("a-o3", appleEmail))
	rec, cookie = e.start("apple", signin, oauthRootOrigin)
	if loc, err := url.Parse(rec.Header().Get("Location")); err != nil || loc.Query().Get("response_mode") != "form_post" {
		t.Fatal("the Apple provider URL does not ask for form_post")
	}
	raw := strings.Join(rec.Header().Values("Set-Cookie"), "\n")
	for _, want := range []string{oauthflow.CookieName + "=", "Path=/", "Max-Age=600", "HttpOnly", "Secure", "SameSite=None"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("binder cookie lacks %s", want)
		}
	}
	if strings.Contains(raw, "Domain=") {
		t.Fatal("a __Host- cookie with a Domain")
	}
	c, s = e.authorize(rec.Header().Get("Location"))
	cb := e.callback("apple", c, s, cookie, url.Values{"user": {`{"email":"delivered+o3someone@resend.dev","name":{"firstName":"X"}}`}})
	code, key = oauthOutcome(t, cb)
	if key != "" {
		t.Fatalf("apple sign-in: %q", key)
	}
	if !strings.Contains(strings.Join(cb.Header().Values("Set-Cookie"), ";"), "Max-Age=0") {
		t.Fatal("the Apple callback did not clear the binder")
	}
	if my, err := j.svc.ListMyTeams(ctx, e.exchange(code).GetToken(), &auth.ListMyTeamsRequest{}); err != nil || my.GetEmail() != appleEmail {
		t.Fatalf("apple account = %v, %v", my, err)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM platform_user WHERE email = 'delivered+o3someone@resend.dev'`); n != 0 {
		t.Fatal("Apple's user field made an account")
	}

	// An operator: use_password, no session and no identity; the console takes the password.
	const opPW = "Operator-Pass-7713"
	opHash, err := j.svc.d.Hasher.HashPassword(opPW)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.st.DB().Exec(`INSERT INTO platform_user (email, handle, password_hash, status, is_operator, email_verified_at)
VALUES ('delivered+b3op@resend.dev', 'o3op', ?, 'active', 1, UTC_TIMESTAMP(3))`, opHash); err != nil {
		t.Fatal(err)
	}
	e.idp.SetClaims(googleID("g-o3-op", "delivered+b3op@resend.dev", "resend.dev"))
	if code, key := e.run("google", signin, oauthRootOrigin); code != "" || key != oauthflow.KeyUsePassword {
		t.Fatalf("operator: code %t, key %q", code != "", key)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM user_session s JOIN platform_user u ON u.id = s.user_id WHERE u.handle = 'o3op'`) +
		j.oauthCount(`SELECT COUNT(*) FROM platform_identity i JOIN platform_user u ON u.id = i.user_id WHERE u.handle = 'o3op'`); n != 0 {
		t.Fatalf("an operator's provider sign-in wrote %d sessions or identities", n)
	}
	if si, err := j.svc.SignIn(ctxFrom("10.9.0.3", ""), &auth.SignInRequest{Login: "delivered+b3op@resend.dev", Password: opPW,
		Target: auth.SignInTarget_SIGN_IN_TARGET_CONSOLE}); err != nil || si.GetToken() == "" {
		t.Fatalf("console password sign-in: %v", err)
	}
}

// An invite accepted through a provider (teams O3 e2e): the start is from the team's host; another
// or a non-authoritative address is invite_mismatch with nothing joined; the invited address joins
// an existing account with its password untouched; a used invite is expired at the start; with no
// account the terms box is required, then the account is made and joins. The code names the team.
func TestOAuthE2EInvite(t *testing.T) {
	j := newJoinTeam(t, "oauth-inv")
	e := newOAuthE2E(t, j.flow, "10.9.1.1")
	const slug, teamOrigin = "oauth-inv", "https://oauth-inv.beta.lekalo.studio"
	const mem, memPW = "delivered+o3mem@resend.dev", "Member-Pass-7714"
	hash, err := j.svc.d.Hasher.HashPassword(memPW)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.st.DB().Exec(`INSERT INTO platform_user (email, handle, password_hash, status, email_verified_at)
VALUES (?, 'o3mem', ?, 'active', UTC_TIMESTAMP(3))`, mem, hash); err != nil {
		t.Fatal(err)
	}
	tok, inviteID := j.invite(mem, writeProducts)
	invite := url.Values{"target": {"invite"}, "invite_token": {tok}}
	joined := func(email string) int {
		return j.oauthCount(`SELECT COUNT(*) FROM team_member m JOIN platform_user u ON u.id = m.user_id
WHERE m.team_id = ? AND u.email = ?`, j.teamID, email)
	}

	if rec, cookie := e.start("google", invite, oauthRootOrigin); rec.Code != http.StatusForbidden || cookie != nil {
		t.Fatalf("invite start from the root host: %d", rec.Code)
	}

	// Another address, then the invited one not vouched for: invite_mismatch, nothing joined or linked.
	for _, id := range []oauthtest.Claims{
		googleID("g-o3-other", "delivered+o3other@resend.dev", "resend.dev"),
		googleID("g-o3-mem", mem, ""),
	} {
		e.idp.SetClaims(id)
		if code, key := e.run("google", invite, teamOrigin); code != "" || key != oauthflow.KeyInviteMismatch {
			t.Fatalf("mismatch: code %t, key %q", code != "", key)
		}
	}
	if n := joined(mem) + j.oauthCount(`SELECT COUNT(*) FROM platform_identity`) +
		j.oauthCount(`SELECT COUNT(*) FROM platform_user WHERE email = 'delivered+o3other@resend.dev'`) +
		j.oauthCount(`SELECT COUNT(*) FROM team_invite WHERE id = ? AND accepted_at IS NOT NULL`, inviteID); n != 0 {
		t.Fatalf("a mismatched invite wrote %d rows", n)
	}

	// The invited address, vouched for: the existing account joins; its password stays.
	e.idp.SetClaims(googleID("g-o3-mem", mem, "resend.dev"))
	code, key := e.run("google", invite, teamOrigin)
	if key != "" {
		t.Fatalf("invite match: %q", key)
	}
	if ex := e.exchange(code); ex.GetEnterTeamSlug() != slug {
		t.Fatalf("invite code enters %q, want %s", ex.GetEnterTeamSlug(), slug)
	}
	if st := j.membership(mem); st != "active" {
		t.Fatalf("membership %s", st)
	}
	var after string
	if err := j.st.DB().Get(&after, `SELECT password_hash FROM platform_user WHERE email = ?`, mem); err != nil || after != hash {
		t.Fatalf("the provider join touched the password (%v)", err)
	}
	if _, err := j.teamSignIn("10.9.1.2", mem, memPW); err != nil {
		t.Fatalf("team password sign-in after the join: %v", err)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM team_invite WHERE id = ? AND accepted_at IS NOT NULL`, inviteID); n != 1 {
		t.Fatal("the invite is still open after the join")
	}
	if rec, cookie := e.start("google", invite, teamOrigin); cookie != nil {
		t.Fatal("a used invite started a flow")
	} else if _, key := oauthOutcome(t, rec); key != oauthflow.KeyExpired {
		t.Fatalf("used invite at the start: %q", key)
	}

	// No account yet: the terms box first; then the account is made (unusable password) and joins.
	const fresh = "delivered+o3fresh@resend.dev"
	tok2, _ := j.invite(fresh, writeProducts)
	inv2 := url.Values{"target": {"invite"}, "invite_token": {tok2}}
	if rec, cookie := e.start("google", inv2, teamOrigin); rec.Code != http.StatusBadRequest || cookie != nil ||
		!strings.Contains(rec.Body.String(), "terms_required") {
		t.Fatalf("new account without the terms: %d", rec.Code)
	}
	inv2.Set("terms_accepted", "true")
	e.idp.SetClaims(googleID("g-o3-fresh", fresh, "resend.dev"))
	code, key = e.run("google", inv2, teamOrigin)
	if key != "" {
		t.Fatalf("invite, new account: %q", key)
	}
	if ex := e.exchange(code); ex.GetEnterTeamSlug() != slug || len(ex.GetTeams()) != 1 {
		t.Fatalf("new account code: enter %q, %d teams", ex.GetEnterTeamSlug(), len(ex.GetTeams()))
	}
	if st := j.membership(fresh); st != "active" {
		t.Fatalf("new account membership %s", st)
	}
	if n := j.oauthCount(`SELECT COUNT(*) FROM platform_user WHERE email = ? AND terms_version = ?`, fresh, DefaultTermsVersion); n != 1 {
		t.Fatal("the new account has no accepted terms")
	}
	_, err = j.svc.SignIn(ctxFrom("10.9.1.3", ""), &auth.SignInRequest{Login: fresh, Password: "Any-Pass-7715", Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	wantCode(t, err, codes.Unauthenticated, "password sign-in of a provider-made account")
}
