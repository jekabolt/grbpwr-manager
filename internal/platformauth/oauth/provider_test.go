package oauth_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth/oauthtest"
	"github.com/lestrrat-go/jwx/jwa"
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
			identity, err := provider.Exchange(context.Background(), "code-value", "verifier-value", testNonce)
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
	if _, err := provider.Exchange(context.Background(), location.Query().Get("code"), verifier, testNonce); err != nil {
		t.Fatalf("authorize-to-token flow: %v", err)
	}
	if got := idp.LastTokenRequest().Get("code_verifier"); got != verifier {
		t.Fatalf("token verifier = %q", got)
	}
	if got := idp.LastAuthorizeRequest().Get("nonce"); got != testNonce {
		t.Fatalf("authorize nonce = %q", got)
	}
	if _, err := provider.Exchange(context.Background(), "fake-code", "wrong-verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
		t.Fatalf("wrong PKCE verifier = %v", err)
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
			identity, err := provider.Exchange(context.Background(), "code", "verifier", testNonce)
			if err != nil {
				t.Fatal(err)
			}
			if identity.EmailVerified != test.want {
				t.Fatalf("EmailVerified = %v, want %v", identity.EmailVerified, test.want)
			}
		})
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
	identity, err := provider.Exchange(context.Background(), "code", "verifier", testNonce)
	if err != nil {
		t.Fatal(err)
	}
	if identity.Provider != oauth.ProviderApple || identity.Email != "" || !identity.EmailVerified || !identity.PrivateEmail {
		t.Fatalf("identity = %+v", identity)
	}
}

func TestUnknownKIDRefreshIsBounded(t *testing.T) {
	now := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	idp := oauthtest.New(t)
	provider := googleProvider(t, idp, now, nil)
	idp.SetClaims(googleClaims(now))
	if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	if got := idp.JWKSRequests(); got != 1 {
		t.Fatalf("initial JWKS requests = %d", got)
	}
	claims := googleClaims(now)
	claims.KeyID = "unknown-key"
	idp.SetClaims(claims)
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); !errors.Is(err, oauth.ErrTokenInvalid) {
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
	if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); err != nil {
		t.Fatal(err)
	}
	if err := idp.RotateSigningKey(); err != nil {
		t.Fatal(err)
	}
	idp.SetClaims(googleClaims(now))
	if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); err != nil {
		t.Fatalf("exchange after rotation: %v", err)
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
		{name: "4xx", status: http.StatusBadRequest, body: []byte(`{"error":"invalid_grant"}`), want: oauth.ErrTokenInvalid},
		{name: "5xx", status: http.StatusServiceUnavailable, body: []byte("down"), want: oauth.ErrProviderUnreachable},
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
	if _, err := provider.Exchange(context.Background(), "code", "verifier", testNonce); err != nil {
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
	options := []oauth.Option{oauth.WithEndpoints(idp.Endpoints()), oauth.WithClock(func() time.Time { return now })}
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
