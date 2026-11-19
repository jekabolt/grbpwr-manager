package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth/oauthtest"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jws"
	"github.com/lestrrat-go/jwx/jwt"
)

const (
	googleClientID = "google-client"
	testNonce      = "test-nonce-3947"
)

func TestGoogleIDTokenValidation(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)

	tests := []struct {
		name      string
		mutate    func(*oauthtest.Claims)
		wantError bool
	}{
		{name: "https issuer"},
		{name: "bare issuer", mutate: func(c *oauthtest.Claims) { c.Issuer = stringPointer("accounts.google.com") }},
		{name: "wrong issuer", mutate: func(c *oauthtest.Claims) { c.Issuer = stringPointer("https://issuer.example") }, wantError: true},
		{name: "wrong audience", mutate: func(c *oauthtest.Claims) { c.Audience = "someone-else" }, wantError: true},
		{name: "azp mismatch", mutate: func(c *oauthtest.Claims) { c.AuthorizedParty = stringPointer("someone-else") }, wantError: true},
		{name: "expired beyond skew", mutate: func(c *oauthtest.Claims) { c.ExpiresAt = timePointer(now.Add(-61 * time.Second)) }, wantError: true},
		{name: "future iat beyond skew", mutate: func(c *oauthtest.Claims) { c.IssuedAt = timePointer(now.Add(61 * time.Second)) }, wantError: true},
		{name: "nonce mismatch", mutate: func(c *oauthtest.Claims) { c.Nonce = stringPointer("another-nonce") }, wantError: true},
		{name: "missing subject", mutate: func(c *oauthtest.Claims) { c.Subject = nil }, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := googleClaims(now)
			if test.mutate != nil {
				test.mutate(&claims)
			}
			idp.SetClaims(claims)
			identity, err := exchangeWithCode(t, idp, provider, "verifier-value", testNonce)
			if test.wantError {
				if !errors.Is(err, oauth.ErrTokenInvalid) {
					t.Fatalf("Exchange() error = %v, want token invalid", err)
				}
				for _, secret := range []string{"code-value", "verifier-value", testNonce} {
					if strings.Contains(err.Error(), secret) {
						t.Fatal("flow secret appeared in error")
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if identity.Provider != oauth.ProviderGoogle || identity.Subject != "subject-1" || identity.Email != "person@example.com" || !identity.EmailVerified {
				t.Fatalf("identity = %+v", identity)
			}
		})
	}
}

func TestFakeIdPAuthorizeTokenAndPKCE(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	verifier := "test-verifier-value-which-is-long-enough-for-pkce"
	authURL := provider.AuthURL("test-state", testNonce, oauth.Challenge(verifier))
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusSeeOther {
		t.Fatalf("authorize status = %d", response.StatusCode)
	}
	location, err := response.Location()
	if err != nil {
		t.Fatal(err)
	}
	if location.Query().Get("code") == "" || location.Query().Get("state") != "test-state" {
		t.Fatalf("authorize location = %s", location)
	}
	code := location.Query().Get("code")
	if _, err := provider.Exchange(context.Background(), code, verifier, testNonce); err != nil {
		t.Fatalf("authorize-to-token flow: %v", err)
	}
	if got := idp.LastTokenRequest().Get("code_verifier"); got != verifier {
		t.Fatalf("token verifier = %q", got)
	}
	if got := idp.LastAuthorizeRequest().Get("nonce"); got != testNonce {
		t.Fatalf("authorize nonce = %q", got)
	}
	if _, err := provider.Exchange(context.Background(), code, verifier, testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
		t.Fatalf("reused code = %v", err)
	}
	wrongVerifierCode := authorizeCode(t, idp, provider, "right-verifier", testNonce)
	if _, err := provider.Exchange(context.Background(), wrongVerifierCode, "wrong-verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
		t.Fatalf("wrong PKCE verifier = %v", err)
	}
}

func TestFakeIdPStrictTokenBindings(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	verifier := "strict-binding-verifier"

	wrongRedirectCode := authorizeCode(t, idp, provider, verifier, testNonce)
	request := tokenForm(idp, wrongRedirectCode, verifier)
	request.Set("redirect_uri", "https://other.example.test/callback")
	if status, body := postToken(t, idp, request); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("wrong redirect = status %d body %#v", status, body)
	}

	wrongSecretCode := authorizeCode(t, idp, provider, verifier, testNonce)
	request = tokenForm(idp, wrongSecretCode, verifier)
	request.Set("client_secret", "wrong-secret")
	if status, body := postToken(t, idp, request); status != http.StatusUnauthorized || body["error"] != "invalid_client" {
		t.Fatalf("wrong secret = status %d body %#v", status, body)
	}

	wrongClientCode := authorizeCode(t, idp, provider, verifier, testNonce)
	request = tokenForm(idp, wrongClientCode, verifier)
	request.Set("client_id", "other-client")
	if status, body := postToken(t, idp, request); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("wrong client = status %d body %#v", status, body)
	}

	request = tokenForm(idp, "unknown-code", verifier)
	if status, body := postToken(t, idp, request); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("unknown code = status %d body %#v", status, body)
	}
}

func TestExchangeRejectsEmptyInputsBeforeNetwork(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	for _, test := range []struct {
		name         string
		code         string
		codeVerifier string
		nonce        string
	}{
		{name: "code", codeVerifier: "verifier", nonce: testNonce},
		{name: "verifier", code: "code", nonce: testNonce},
		{name: "nonce", code: "code", codeVerifier: "verifier"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := provider.Exchange(context.Background(), test.code, test.codeVerifier, test.nonce)
			if !errors.Is(err, oauth.ErrTokenInvalid) {
				t.Fatalf("Exchange() error = %v", err)
			}
		})
	}
	if got := idp.LastTokenRequest(); got != nil {
		t.Fatalf("empty input reached token endpoint: %#v", got)
	}
}

func TestEmptyExpectedNonceRejectsEmptyTokenNonce(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	claims := googleClaims(now)
	claims.Nonce = stringPointer("")
	idp.SetClaims(claims)
	verifier := "empty-nonce-verifier"
	code := authorizeCode(t, idp, provider, verifier, "")
	if _, err := provider.Exchange(context.Background(), code, verifier, ""); !errors.Is(err, oauth.ErrTokenInvalid) {
		t.Fatalf("Exchange() error = %v", err)
	}
	if got := idp.LastTokenRequest(); got != nil {
		t.Fatalf("empty nonce reached token endpoint: %#v", got)
	}
}

func TestEmailVerifiedClaimForms(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	for _, test := range []struct {
		name  string
		value interface{}
		want  bool
	}{
		{name: "boolean", value: true, want: true},
		{name: "string", value: "true", want: true},
		{name: "absent", value: nil, want: false},
		{name: "false string", value: "false", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			claims := googleClaims(now)
			claims.EmailVerified = test.value
			idp.SetClaims(claims)
			identity, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
			if err != nil {
				t.Fatal(err)
			}
			if identity.EmailVerified != test.want {
				t.Fatalf("EmailVerified = %v, want %v", identity.EmailVerified, test.want)
			}
		})
	}
}

func TestGoogleEmailAuthority(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	tests := []struct {
		name     string
		email    string
		verified bool
		hd       string
		want     bool
	}{
		{name: "gmail", email: " Person@GMAIL.COM ", verified: true, want: true},
		{name: "googlemail", email: "person@GoogleMail.com", verified: true, want: true},
		{name: "hosted domain", email: "person@foreign.example", verified: true, hd: "workspace.example", want: true},
		{name: "foreign domain", email: "person@foreign.example", verified: true},
		{name: "unverified", email: "person@gmail.com"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			claims := googleClaims(now)
			claims.Email = stringPointer(test.email)
			claims.EmailVerified = test.verified
			if test.hd != "" {
				claims.HostedDomain = stringPointer(test.hd)
			}
			idp.SetClaims(claims)
			identity, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
			if err != nil {
				t.Fatal(err)
			}
			if identity.Email != strings.TrimSpace(test.email) || identity.EmailAuthoritative != test.want {
				t.Fatalf("identity = %+v, authority want %v", identity, test.want)
			}
		})
	}
}

func TestEmptyEmailClearsEmailFlags(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider, _ := appleProvider(t, idp, now, nil)
	claims := appleClaims(now)
	claims.Email = stringPointer(" \t ")
	claims.EmailVerified = true
	claims.PrivateEmail = true
	idp.SetClaims(claims)
	identity, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Email != "" || identity.EmailVerified || identity.PrivateEmail || identity.EmailAuthoritative {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestAppleIdentityWithoutEmailAndPrivateFlag(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider, _ := appleProvider(t, idp, now, nil)
	claims := appleClaims(now)
	claims.Email = nil
	claims.EmailVerified = "true"
	claims.PrivateEmail = "true"
	idp.SetClaims(claims)
	identity, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != oauth.ProviderApple || identity.Email != "" || identity.EmailVerified || identity.PrivateEmail || identity.EmailAuthoritative {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestAppleIssuerAndEmailAuthority(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider, _ := appleProvider(t, idp, now, nil)

	claims := appleClaims(now)
	idp.SetClaims(claims)
	identity, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if !identity.EmailAuthoritative {
		t.Fatalf("identity = %+v", identity)
	}

	claims = appleClaims(now)
	claims.Issuer = stringPointer("https://issuer.example")
	idp.SetClaims(claims)
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
		t.Fatalf("wrong Apple issuer = %v", err)
	}
}

func TestSeveralAudiencesRequireAuthorizedParty(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	for _, providerName := range []string{oauth.ProviderGoogle, oauth.ProviderApple} {
		t.Run(providerName, func(t *testing.T) {
			idp := oauthtest.New(t)
			var provider oauth.Provider
			var audience string
			var claims func(time.Time) oauthtest.Claims
			if providerName == oauth.ProviderGoogle {
				provider = googleProvider(t, idp, now, nil)
				audience = googleClientID
				claims = googleClaims
			} else {
				provider, _ = appleProvider(t, idp, now, nil)
				audience = "apple-service"
				claims = appleClaims
			}
			for _, test := range []struct {
				name string
				azp  *string
				want error
			}{
				{name: "missing", want: oauth.ErrTokenInvalid},
				{name: "foreign", azp: stringPointer("other-client"), want: oauth.ErrTokenInvalid},
				{name: "matching", azp: stringPointer(audience)},
			} {
				t.Run(test.name, func(t *testing.T) {
					current := claims(now)
					current.Audience = []string{audience, "secondary-audience"}
					current.AuthorizedParty = test.azp
					idp.SetClaims(current)
					_, err := exchangeWithCode(t, idp, provider, "verifier", testNonce)
					if !errors.Is(err, test.want) {
						t.Fatalf("Exchange() error = %v, want %v", err, test.want)
					}
				})
			}
		})
	}
}

func TestIDTokenRejectsUnsafeSignatures(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	payload := validTokenPayload(t, now)
	for _, test := range []struct {
		name  string
		issue func(*testing.T, *oauthtest.IdP) string
	}{
		{
			name: "alg none",
			issue: func(t *testing.T, _ *oauthtest.IdP) string {
				header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"key-1"}`))
				return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
			},
		},
		{
			name: "HS256 with RSA public key bytes",
			issue: func(t *testing.T, idp *oauthtest.IdP) string {
				secret, err := x509.MarshalPKIXPublicKey(&idp.SigningKey().PublicKey)
				if err != nil {
					t.Fatal(err)
				}
				return signToken(t, payload, jwa.HS256, secret, idp.KeyID())
			},
		},
		{
			name: "missing kid",
			issue: func(t *testing.T, idp *oauthtest.IdP) string {
				return signToken(t, payload, jwa.RS256, idp.SigningKey(), "")
			},
		},
		{
			name: "same kid different key",
			issue: func(t *testing.T, idp *oauthtest.IdP) string {
				key, err := rsa.GenerateKey(rand.Reader, 2048)
				if err != nil {
					t.Fatal(err)
				}
				return signToken(t, payload, jwa.RS256, key, idp.KeyID())
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			idp := oauthtest.New(t)
			provider := googleProvider(t, idp, now, nil)
			setIDTokenResponse(t, idp, test.issue(t, idp))
			if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
				t.Fatalf("Exchange() error = %v", err)
			}
		})
	}
}

func TestUnknownKIDRefreshIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	if got := idp.JWKSRequests(); got != 1 {
		t.Fatalf("initial JWKS requests = %d", got)
	}
	claims := googleClaims(now)
	claims.KeyID = "unknown-key"
	idp.SetClaims(claims)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
			t.Fatalf("unknown kid attempt %d = %v", attempt+1, err)
		}
	}
	if got := idp.JWKSRequests(); got != 2 {
		t.Fatalf("JWKS requests after repeated unknown kid = %d, want 2", got)
	}
}

func TestJWKSKeyRotation(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	if err := idp.RotateSigningKey(); err != nil {
		t.Fatal(err)
	}
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatalf("exchange after rotation: %v", err)
	}
	if got := idp.JWKSRequests(); got != 2 {
		t.Fatalf("JWKS requests = %d, want 2", got)
	}
}

func TestJWKSColdFailureIsShared(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	idp.SetClaims(googleClaims(now))
	idp.SetJWKSDelay(500 * time.Millisecond)
	idp.SetJWKSResponse(http.StatusInternalServerError, []byte(`{"error":"down"}`))
	provider := googleProviderWithClock(t, idp, func() time.Time { return now }, &http.Client{Timeout: 30 * time.Millisecond})

	const callers = 10
	verifier := "parallel-verifier"
	codes := make([]string, callers)
	for index := range codes {
		codes[index] = authorizeCode(t, idp, provider, verifier, testNonce)
	}
	start := make(chan struct{})
	errs := make(chan error, callers)
	var group sync.WaitGroup
	group.Add(callers)
	startedAt := time.Now()
	for _, code := range codes {
		go func(code string) {
			defer group.Done()
			<-start
			_, err := provider.Exchange(context.Background(), code, verifier, testNonce)
			errs <- err
		}(code)
	}
	close(start)
	group.Wait()
	elapsed := time.Since(startedAt)
	close(errs)
	for err := range errs {
		if !errors.Is(err, oauth.ErrProviderUnreachable) {
			t.Fatalf("Exchange() error = %v", err)
		}
	}
	if got := idp.JWKSRequests(); got != 1 {
		t.Fatalf("JWKS requests = %d, want 1", got)
	}
	if elapsed >= 150*time.Millisecond {
		t.Fatalf("parallel cold failure took %v", elapsed)
	}
	if _, err := exchangeWithCode(t, idp, provider, verifier, testNonce); !errors.Is(err, oauth.ErrProviderUnreachable) {
		t.Fatalf("remembered cold failure = %v", err)
	}
	if got := idp.JWKSRequests(); got != 1 {
		t.Fatalf("JWKS requests during cold cooldown = %d, want 1", got)
	}
	now = now.Add(6 * time.Second)
	idp.SetJWKSDelay(0)
	idp.SetJWKSResponse(0, nil)
	if _, err := exchangeWithCode(t, idp, provider, verifier, testNonce); err != nil {
		t.Fatalf("cold retry after cooldown: %v", err)
	}
	if got := idp.JWKSRequests(); got != 2 {
		t.Fatalf("JWKS requests after cold cooldown = %d, want 2", got)
	}
}

func TestJWKSWaiterStopsWhenItsContextEnds(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	idp.SetClaims(googleClaims(now))
	transport := &blockingJWKSTransport{
		base:        http.DefaultTransport,
		jwksStarted: make(chan struct{}),
		releaseJWKS: make(chan struct{}),
		secondToken: make(chan struct{}),
	}
	defer func() {
		select {
		case <-transport.releaseJWKS:
		default:
			close(transport.releaseJWKS)
		}
	}()
	provider := googleProvider(t, idp, now, &http.Client{Transport: transport, Timeout: time.Second})
	verifier := "context-verifier"
	firstCode := authorizeCode(t, idp, provider, verifier, testNonce)
	secondCode := authorizeCode(t, idp, provider, verifier, testNonce)

	firstResult := make(chan error, 1)
	go func() {
		_, err := provider.Exchange(context.Background(), firstCode, verifier, testNonce)
		firstResult <- err
	}()
	<-transport.jwksStarted

	ctx, cancel := context.WithCancel(context.Background())
	secondResult := make(chan error, 1)
	go func() {
		_, err := provider.Exchange(ctx, secondCode, verifier, testNonce)
		secondResult <- err
	}()
	<-transport.secondToken
	cancel()
	select {
	case err := <-secondResult:
		if !errors.Is(err, oauth.ErrProviderUnreachable) {
			t.Fatalf("cancelled waiter error = %v", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cancelled waiter remained blocked")
	}

	close(transport.releaseJWKS)
	if err := <-firstResult; err != nil {
		t.Fatalf("shared fetch leader: %v", err)
	}
}

func TestJWKSRefreshesAfterTTL(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProviderWithClock(t, idp, func() time.Time { return now }, nil)
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Second)
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	if got := idp.JWKSRequests(); got != 2 {
		t.Fatalf("JWKS requests = %d, want 2", got)
	}
}

func TestStaleJWKSIsServedWhenRefreshFails(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProviderWithClock(t, idp, func() time.Time { return now }, nil)
	idp.SetClaims(googleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour + time.Second)
	idp.SetClaims(googleClaims(now))
	idp.SetJWKSResponse(http.StatusServiceUnavailable, []byte(`{"error":"down"}`))
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
			t.Fatalf("stale-key attempt %d: %v", attempt+1, err)
		}
	}
	if got := idp.JWKSRequests(); got != 2 {
		t.Fatalf("JWKS requests = %d, want 2", got)
	}
}

func TestTokenEndpointFailures(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		status int
		body   []byte
		delay  time.Duration
		client *http.Client
		want   error
	}{
		{name: "invalid grant", status: http.StatusBadRequest, body: []byte(`{"error":"invalid_grant"}`), want: oauth.ErrTokenInvalid},
		{name: "invalid client", status: http.StatusUnauthorized, body: []byte(`{"error":"invalid_client"}`), want: oauth.ErrProviderUnreachable},
		{name: "other 4xx", status: http.StatusBadRequest, body: []byte(`{"error":"unauthorized_client"}`), want: oauth.ErrProviderUnreachable},
		{name: "rate limited", status: http.StatusTooManyRequests, body: []byte(`{"error":"slow_down"}`), want: oauth.ErrProviderUnreachable},
		{name: "unparseable 4xx", status: http.StatusBadRequest, body: []byte("bad response"), want: oauth.ErrProviderUnreachable},
		{name: "5xx", status: http.StatusServiceUnavailable, body: []byte("down"), want: oauth.ErrProviderUnreachable},
		{name: "200 error", status: http.StatusOK, body: []byte(`{"error":"invalid_grant"}`), want: oauth.ErrProviderUnreachable},
		{name: "200 no id token", status: http.StatusOK, body: []byte(`{"access_token":"unused"}`), want: oauth.ErrProviderUnreachable},
		{name: "timeout", delay: 200 * time.Millisecond, client: &http.Client{Timeout: 20 * time.Millisecond}, want: oauth.ErrProviderUnreachable},
		{name: "oversized", status: http.StatusOK, body: []byte(strings.Repeat("x", (64<<10)+1)), want: oauth.ErrProviderUnreachable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			idp := oauthtest.New(t)
			if test.status != 0 || test.body != nil {
				idp.SetTokenResponse(test.status, test.body)
			}
			idp.SetTokenDelay(test.delay)
			provider := googleProvider(t, idp, now, test.client)
			_, err := provider.Exchange(context.Background(), "sensitive-code", "sensitive-verifier", "sensitive-nonce")
			if !errors.Is(err, test.want) {
				t.Fatalf("Exchange() error = %v, want %v", err, test.want)
			}
			for _, secret := range []string{"sensitive-code", "sensitive-verifier", "sensitive-nonce"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatal("flow secret appeared in error")
				}
			}
		})
	}
}

func TestHTTPRedirectsAreNotFollowed(t *testing.T) {
	var followed atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		followed.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	idp := oauthtest.New(t)
	endpoints := idp.Endpoints()
	endpoints.Google.TokenURL = redirect.URL
	registry, err := oauth.NewRegistry(googleConfig(), oauth.WithEndpoints(endpoints))
	if err != nil {
		t.Fatal(err)
	}
	provider, _ := registry.Provider(oauth.ProviderGoogle)
	if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); !errors.Is(err, oauth.ErrProviderUnreachable) {
		t.Fatalf("redirect response = %v", err)
	}
	if followed.Load() != 0 {
		t.Fatal("token redirect was followed")
	}
}

func TestAppleClientSecretJWT(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider, privateKey := appleProvider(t, idp, now, nil)
	idp.SetClaims(appleClaims(now))
	if _, err := exchangeWithCode(t, idp, provider, "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	secret := idp.LastTokenRequest().Get("client_secret")
	if secret == "" {
		t.Fatal("missing Apple client secret")
	}
	verified, err := jwt.ParseString(secret, jwt.WithVerify(jwa.ES256, &privateKey.PublicKey))
	if err != nil {
		t.Fatalf("verify Apple client secret: %v", err)
	}
	if verified.Issuer() != "apple-team" || verified.Subject() != "apple-service" ||
		len(verified.Audience()) != 1 || verified.Audience()[0] != "https://appleid.apple.com" {
		t.Fatalf("client secret claims: iss=%q sub=%q aud=%v", verified.Issuer(), verified.Subject(), verified.Audience())
	}
	if !verified.IssuedAt().Equal(now) || !verified.Expiration().Equal(now.Add(5*time.Minute)) {
		t.Fatalf("client secret times: iat=%v exp=%v", verified.IssuedAt(), verified.Expiration())
	}
	parts := strings.Split(secret, ".")
	if len(parts) != 3 {
		t.Fatal("client secret is not compact JWT")
	}
	headerJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	var header map[string]interface{}
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		t.Fatal(err)
	}
	if header["kid"] != "apple-key" || header["alg"] != "ES256" {
		t.Fatalf("client secret header = %#v", header)
	}
}

func googleProvider(t *testing.T, idp *oauthtest.IdP, now time.Time, client *http.Client) oauth.Provider {
	t.Helper()
	return googleProviderWithClock(t, idp, func() time.Time { return now }, client)
}

func googleProviderWithClock(t *testing.T, idp *oauthtest.IdP, now func() time.Time, client *http.Client) oauth.Provider {
	t.Helper()
	options := []oauth.Option{oauth.WithEndpoints(idp.Endpoints()), oauth.WithClock(now)}
	if client != nil {
		options = append(options, oauth.WithHTTPClient(client))
	}
	registry, err := oauth.NewRegistry(googleConfig(), options...)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.Provider(oauth.ProviderGoogle)
	if !ok {
		t.Fatal("Google provider absent")
	}
	return provider
}

func authorizeCode(t *testing.T, _ *oauthtest.IdP, provider oauth.Provider, verifier, nonce string) string {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Get(provider.AuthURL("state", nonce, oauth.Challenge(verifier)))
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusSeeOther && response.StatusCode != http.StatusOK {
		t.Fatalf("authorize status = %d", response.StatusCode)
	}
	code := ""
	if response.StatusCode == http.StatusSeeOther {
		location, locationErr := response.Location()
		if locationErr != nil {
			t.Fatal(locationErr)
		}
		code = location.Query().Get("code")
	} else {
		const marker = `name="code" value="`
		start := strings.Index(string(body), marker)
		if start >= 0 {
			value := string(body)[start+len(marker):]
			if end := strings.IndexByte(value, '"'); end >= 0 {
				code = value[:end]
			}
		}
	}
	if code == "" {
		t.Fatal("authorize returned no code")
	}
	return code
}

func exchangeWithCode(t *testing.T, idp *oauthtest.IdP, provider oauth.Provider, verifier, nonce string) (oauth.Identity, error) {
	t.Helper()
	code := authorizeCode(t, idp, provider, verifier, nonce)
	return provider.Exchange(context.Background(), code, verifier, nonce)
}

func tokenForm(idp *oauthtest.IdP, code, verifier string) url.Values {
	authorize := idp.LastAuthorizeRequest()
	return url.Values{
		"client_id":     {authorize.Get("client_id")},
		"client_secret": {"google-secret"},
		"code":          {code},
		"code_verifier": {verifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {authorize.Get("redirect_uri")},
	}
}

func postToken(t *testing.T, idp *oauthtest.IdP, form url.Values) (int, map[string]string) {
	t.Helper()
	response, err := http.PostForm(idp.URL()+"/token", form)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body := make(map[string]string)
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func validTokenPayload(t *testing.T, now time.Time) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]interface{}{
		"iss":            "https://accounts.google.com",
		"aud":            googleClientID,
		"sub":            "subject-1",
		"email":          "person@gmail.com",
		"email_verified": true,
		"nonce":          testNonce,
		"exp":            now.Add(5 * time.Minute).Unix(),
		"iat":            now.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func signToken(t *testing.T, payload []byte, algorithm jwa.SignatureAlgorithm, key interface{}, kid string) string {
	t.Helper()
	headers := jws.NewHeaders()
	if kid != "" {
		if err := headers.Set(jws.KeyIDKey, kid); err != nil {
			t.Fatal(err)
		}
	}
	signed, err := jws.Sign(payload, algorithm, key, jws.WithHeaders(headers))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}

func setIDTokenResponse(t *testing.T, idp *oauthtest.IdP, token string) {
	t.Helper()
	body, err := json.Marshal(map[string]string{"id_token": token})
	if err != nil {
		t.Fatal(err)
	}
	idp.SetTokenResponse(http.StatusOK, body)
}

type blockingJWKSTransport struct {
	base        http.RoundTripper
	jwksStarted chan struct{}
	releaseJWKS chan struct{}
	secondToken chan struct{}
	jwksOnce    sync.Once
	tokenCount  atomic.Int32
}

func (t *blockingJWKSTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Path == "/jwks" {
		t.jwksOnce.Do(func() { close(t.jwksStarted) })
		select {
		case <-t.releaseJWKS:
		case <-request.Context().Done():
			return nil, request.Context().Err()
		}
	}
	response, err := t.base.RoundTrip(request)
	if request.URL.Path == "/token" && err == nil && t.tokenCount.Add(1) == 2 {
		close(t.secondToken)
	}
	return response, err
}

func appleProvider(t *testing.T, idp *oauthtest.IdP, now time.Time, client *http.Client) (oauth.Provider, *ecdsa.PrivateKey) {
	t.Helper()
	privateKey, privatePEM := appleKey(t)
	options := []oauth.Option{oauth.WithEndpoints(idp.Endpoints()), oauth.WithClock(func() time.Time { return now })}
	if client != nil {
		options = append(options, oauth.WithHTTPClient(client))
	}
	registry, err := oauth.NewRegistry(oauth.Config{
		AppleEnabled: true, AppleServicesID: "apple-service", AppleTeamID: "apple-team",
		AppleKeyID: "apple-key", ApplePrivateKey: privatePEM, CallbackBaseURL: "https://api.example.test",
	}, options...)
	if err != nil {
		t.Fatal(err)
	}
	provider, ok := registry.Provider(oauth.ProviderApple)
	if !ok {
		t.Fatal("Apple provider absent")
	}
	return provider, privateKey
}

func googleConfig() oauth.Config {
	return oauth.Config{
		GoogleClientID: googleClientID, GoogleClientSecret: "google-secret", CallbackBaseURL: "https://api.example.test",
	}
}

func googleClaims(now time.Time) oauthtest.Claims {
	issuer := "https://accounts.google.com"
	subject := "subject-1"
	email := "person@example.com"
	nonce := testNonce
	return oauthtest.Claims{
		Issuer: &issuer, Audience: googleClientID, Subject: &subject, Email: &email, EmailVerified: true,
		Nonce: &nonce, ExpiresAt: timePointer(now.Add(5 * time.Minute)), IssuedAt: timePointer(now),
	}
}

func appleClaims(now time.Time) oauthtest.Claims {
	issuer := "https://appleid.apple.com"
	subject := "apple-subject"
	email := "relay@privaterelay.appleid.com"
	nonce := testNonce
	return oauthtest.Claims{
		Issuer: &issuer, Audience: "apple-service", Subject: &subject, Email: &email, EmailVerified: true,
		Nonce: &nonce, ExpiresAt: timePointer(now.Add(5 * time.Minute)), IssuedAt: timePointer(now),
	}
}

func stringPointer(value string) *string { return &value }

func timePointer(value time.Time) *time.Time { return &value }
