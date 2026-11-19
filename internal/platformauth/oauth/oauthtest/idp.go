// Package oauthtest provides an in-process OAuth/OIDC identity provider for
// provider and HTTP-flow tests. It must never be mounted in a running app.
package oauthtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jws"
)

// Claims controls the next ID token. Nil pointer fields are omitted. A nil
// Nonce uses the nonce most recently received by the authorize endpoint.
type Claims struct {
	Issuer          *string
	Audience        interface{}
	AuthorizedParty *string
	Subject         *string
	Email           *string
	EmailVerified   interface{}
	PrivateEmail    interface{}
	HostedDomain    *string
	Nonce           *string
	ExpiresAt       *time.Time
	IssuedAt        *time.Time
	KeyID           string
	Extra           map[string]interface{}
}

// DefaultClaims returns a complete, currently valid claim set. Nonce is left
// nil so an authorize-to-token round trip binds it automatically.
func DefaultClaims(issuer, audience string) Claims {
	now := time.Now().UTC().Truncate(time.Second)
	subject := "subject-1"
	email := "person@example.com"
	return Claims{
		Issuer:        &issuer,
		Audience:      audience,
		Subject:       &subject,
		Email:         &email,
		EmailVerified: true,
		ExpiresAt:     timePointer(now.Add(5 * time.Minute)),
		IssuedAt:      timePointer(now),
	}
}

// IdP is an httptest OAuth/OIDC server with rotatable RSA signing keys.
type IdP struct {
	server *httptest.Server

	mu            sync.Mutex
	key           *rsa.PrivateKey
	kid           string
	olderKey      *rsa.PrivateKey
	olderKID      string
	keySequence   int
	claims        Claims
	lastAuthorize url.Values
	lastToken     url.Values
	codeSequence  int
	grants        map[string]authorizationGrant
	clientSecrets map[string]string
	jwksRequests  int

	tokenResponseSet bool
	tokenStatus      int
	tokenBody        []byte
	tokenDelay       time.Duration

	jwksResponseSet bool
	jwksStatus      int
	jwksBody        []byte
	jwksDelay       time.Duration
}

type authorizationGrant struct {
	nonce       string
	challenge   string
	redirectURI string
	clientID    string
}

// New starts an IdP and registers cleanup with t.
func New(t testing.TB) *IdP {
	t.Helper()
	idp, err := NewServer()
	if err != nil {
		t.Fatalf("start fake IdP: %v", err)
	}
	t.Cleanup(idp.Close)
	return idp
}

// NewServer starts an IdP for callers that do not have a testing.TB.
func NewServer() (*IdP, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	olderKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	idp := &IdP{
		key:           key,
		kid:           "key-1",
		olderKey:      olderKey,
		olderKID:      "key-0",
		keySequence:   1,
		claims:        DefaultClaims("https://accounts.google.com", "google-client"),
		grants:        make(map[string]authorizationGrant),
		clientSecrets: map[string]string{"google-client": "google-secret"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/authorize", idp.handleAuthorize)
	mux.HandleFunc("/token", idp.handleToken)
	mux.HandleFunc("/jwks", idp.handleJWKS)
	idp.server = httptest.NewServer(mux)
	return idp, nil
}

// Close stops the fake provider.
func (i *IdP) Close() {
	if i != nil && i.server != nil {
		i.server.Close()
	}
}

// URL returns the fake provider's base URL.
func (i *IdP) URL() string { return i.server.URL }

// Endpoints returns replacements suitable for oauth.WithEndpoints. Both
// providers use the same fake handlers.
func (i *IdP) Endpoints() oauth.Endpoints {
	provider := oauth.ProviderEndpoints{
		AuthorizationURL: i.server.URL + "/authorize",
		TokenURL:         i.server.URL + "/token",
		JWKSURL:          i.server.URL + "/jwks",
	}
	return oauth.Endpoints{Google: provider, Apple: provider}
}

// SetClaims replaces the claim knobs used by the token endpoint.
func (i *IdP) SetClaims(claims Claims) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.claims = claims
}

// SetTokenResponse makes the token endpoint return a fixed response.
func (i *IdP) SetTokenResponse(status int, body []byte) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenResponseSet = true
	i.tokenStatus = status
	i.tokenBody = append([]byte(nil), body...)
}

// ResetTokenResponse restores signed ID-token responses.
func (i *IdP) ResetTokenResponse() {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenResponseSet = false
	i.tokenStatus = 0
	i.tokenBody = nil
}

// SetTokenDelay delays token responses, respecting request cancellation.
func (i *IdP) SetTokenDelay(delay time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.tokenDelay = delay
}

// SetJWKSResponse makes the JWKS endpoint return a fixed response. A zero
// status restores the default two-key response.
func (i *IdP) SetJWKSResponse(status int, body []byte) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.jwksResponseSet = status != 0
	i.jwksStatus = status
	i.jwksBody = append([]byte(nil), body...)
}

// SetJWKSDelay delays JWKS responses, respecting request cancellation.
func (i *IdP) SetJWKSDelay(delay time.Duration) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.jwksDelay = delay
}

// RotateSigningKey replaces the JWKS and token signing key.
func (i *IdP) RotateSigningKey() error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.keySequence++
	i.olderKey = i.key
	i.olderKID = i.kid
	i.key = key
	i.kid = fmt.Sprintf("key-%d", i.keySequence)
	return nil
}

// SigningKey returns the current test-only RSA signing key.
func (i *IdP) SigningKey() *rsa.PrivateKey {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.key
}

// KeyID returns the current signing key ID.
func (i *IdP) KeyID() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.kid
}

// JWKSRequests reports how often the JWKS endpoint was fetched.
func (i *IdP) JWKSRequests() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.jwksRequests
}

// LastAuthorizeRequest returns a copy of the last authorize query.
func (i *IdP) LastAuthorizeRequest() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cloneValues(i.lastAuthorize)
}

// LastTokenRequest returns a copy of the last parsed token form.
func (i *IdP) LastTokenRequest() url.Values {
	i.mu.Lock()
	defer i.mu.Unlock()
	return cloneValues(i.lastToken)
}

// IssueToken signs claims with the current key without calling an endpoint.
func (i *IdP) IssueToken(claims Claims) (string, error) {
	i.mu.Lock()
	key := i.key
	kid := i.kid
	i.mu.Unlock()
	if claims.KeyID != "" {
		kid = claims.KeyID
	}
	payload := make(map[string]interface{}, len(claims.Extra)+10)
	for name, value := range claims.Extra {
		payload[name] = value
	}
	putString(payload, "iss", claims.Issuer)
	if claims.Audience != nil {
		payload["aud"] = claims.Audience
	}
	putString(payload, "azp", claims.AuthorizedParty)
	putString(payload, "sub", claims.Subject)
	putString(payload, "email", claims.Email)
	if claims.EmailVerified != nil {
		payload["email_verified"] = claims.EmailVerified
	}
	if claims.PrivateEmail != nil {
		payload["is_private_email"] = claims.PrivateEmail
	}
	putString(payload, "hd", claims.HostedDomain)
	putString(payload, "nonce", claims.Nonce)
	if claims.ExpiresAt != nil {
		payload["exp"] = claims.ExpiresAt.Unix()
	}
	if claims.IssuedAt != nil {
		payload["iat"] = claims.IssuedAt.Unix()
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.KeyIDKey, kid); err != nil {
		return "", err
	}
	signed, err := jws.Sign(raw, jwa.RS256, key, jws.WithHeaders(headers))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func (i *IdP) handleAuthorize(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	redirect, err := url.Parse(query.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" || redirect.Host == "" || query.Get("client_id") == "" {
		http.Error(w, "bad authorization request", http.StatusBadRequest)
		return
	}
	i.mu.Lock()
	i.lastAuthorize = query
	i.codeSequence++
	code := fmt.Sprintf("fake-code-%d", i.codeSequence)
	i.grants[code] = authorizationGrant{
		nonce:       query.Get("nonce"),
		challenge:   query.Get("code_challenge"),
		redirectURI: query.Get("redirect_uri"),
		clientID:    query.Get("client_id"),
	}
	i.mu.Unlock()
	if query.Get("response_mode") == "form_post" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, `<form method="post" action="%s"><input name="code" value="%s"><input name="state" value="%s"></form>`,
			template.HTMLEscapeString(redirect.String()), template.HTMLEscapeString(code), template.HTMLEscapeString(query.Get("state")))
		return
	}
	values := redirect.Query()
	values.Set("code", code)
	values.Set("state", query.Get("state"))
	redirect.RawQuery = values.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusSeeOther)
}

func (i *IdP) handleToken(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	i.mu.Lock()
	i.lastToken = cloneValues(r.PostForm)
	delay := i.tokenDelay
	fixed := i.tokenResponseSet
	status := i.tokenStatus
	body := append([]byte(nil), i.tokenBody...)
	claims := i.claims
	expectedSecret, secretRegistered := i.clientSecrets[r.PostForm.Get("client_id")]
	i.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	if fixed {
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	if (secretRegistered && r.PostForm.Get("client_secret") != expectedSecret) ||
		(!secretRegistered && r.PostForm.Get("client_secret") == "") {
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	i.mu.Lock()
	grant, ok := i.grants[r.PostForm.Get("code")]
	if ok {
		delete(i.grants, r.PostForm.Get("code"))
	}
	i.mu.Unlock()
	if !ok || r.PostForm.Get("grant_type") != "authorization_code" ||
		r.PostForm.Get("client_id") != grant.clientID || r.PostForm.Get("redirect_uri") != grant.redirectURI ||
		grant.challenge == "" || oauth.Challenge(r.PostForm.Get("code_verifier")) != grant.challenge {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant")
		return
	}
	if claims.Nonce == nil {
		claims.Nonce = &grant.nonce
	}
	token, err := i.IssueToken(claims)
	if err != nil {
		http.Error(w, "signing failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id_token": token, "token_type": "Bearer"})
}

func (i *IdP) handleJWKS(w http.ResponseWriter, r *http.Request) {
	i.mu.Lock()
	i.jwksRequests++
	key := i.key
	kid := i.kid
	olderKey := i.olderKey
	olderKID := i.olderKID
	delay := i.jwksDelay
	fixed := i.jwksResponseSet
	status := i.jwksStatus
	body := append([]byte(nil), i.jwksBody...)
	i.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-r.Context().Done():
			return
		}
	}
	if fixed {
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(body)
		return
	}
	jwkKey, err := publicJWK(key, kid)
	if err != nil {
		http.Error(w, "key failed", http.StatusInternalServerError)
		return
	}
	olderJWK, err := publicJWK(olderKey, olderKID)
	if err != nil {
		http.Error(w, "key failed", http.StatusInternalServerError)
		return
	}
	set := jwk.NewSet()
	set.Add(jwkKey)
	set.Add(olderJWK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(set)
}

func publicJWK(key *rsa.PrivateKey, kid string) (jwk.Key, error) {
	jwkKey, err := jwk.New(&key.PublicKey)
	if err != nil {
		return nil, err
	}
	_ = jwkKey.Set(jwk.KeyIDKey, kid)
	_ = jwkKey.Set(jwk.AlgorithmKey, jwa.RS256)
	_ = jwkKey.Set(jwk.KeyUsageKey, jwk.ForSignature)
	return jwkKey, nil
}

func writeOAuthError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func putString(dst map[string]interface{}, name string, value *string) {
	if value != nil {
		dst[name] = *value
	}
}

func timePointer(value time.Time) *time.Time { return &value }

func cloneValues(values url.Values) url.Values {
	if values == nil {
		return nil
	}
	clone := make(url.Values, len(values))
	for name, entries := range values {
		clone[name] = append([]string(nil), entries...)
	}
	return clone
}
