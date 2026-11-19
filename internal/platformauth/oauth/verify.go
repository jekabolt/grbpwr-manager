package oauth

import (
	"context"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jwk"
	"github.com/lestrrat-go/jwx/jws"
)

const (
	clockSkew              = 60 * time.Second
	unknownKIDRefreshMin   = time.Minute
	jwksTTL                = time.Hour
	coldFetchFailureMemory = 5 * time.Second
	staleFetchRetryMin     = time.Minute
)

type jwksFetch struct {
	done chan struct{}
	err  error
}

type idTokenVerifier struct {
	provider string
	audience string
	jwksURL  string
	issuers  map[string]struct{}
	client   *http.Client
	now      func() time.Time

	mu                 sync.Mutex
	keys               jwk.Set
	keysFetchedAt      time.Time
	coldFailureUntil   time.Time
	staleRefreshAfter  time.Time
	lastUnknownRefresh time.Time
	inflight           *jwksFetch
}

func newIDTokenVerifier(provider, audience, jwksURL string, issuers []string, client *http.Client, now func() time.Time) *idTokenVerifier {
	allowed := make(map[string]struct{}, len(issuers))
	for _, issuer := range issuers {
		allowed[issuer] = struct{}{}
	}
	return &idTokenVerifier{
		provider: provider,
		audience: audience,
		jwksURL:  jwksURL,
		issuers:  allowed,
		client:   client,
		now:      now,
	}
}

func (v *idTokenVerifier) verify(ctx context.Context, compact, expectedNonce string) (Identity, error) {
	header, ok := compactHeader(compact)
	if !ok || header.Algorithm != string(jwa.RS256) || header.KeyID == "" {
		return Identity{}, ErrTokenInvalid
	}
	key, err := v.verificationKey(ctx, header.KeyID)
	if err != nil {
		return Identity{}, err
	}
	payload, err := jws.Verify([]byte(compact), jwa.RS256, key)
	if err != nil {
		return Identity{}, ErrTokenInvalid
	}
	var claims idTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, ErrTokenInvalid
	}
	if _, ok := v.issuers[claims.Issuer]; !ok || !claims.Audience.Contains(v.audience) {
		return Identity{}, ErrTokenInvalid
	}
	if len(claims.Audience.Values) > 1 && claims.AuthorizedParty != v.audience {
		return Identity{}, ErrTokenInvalid
	}
	if v.provider == ProviderGoogle && claims.AuthorizedParty != "" && claims.AuthorizedParty != v.audience {
		return Identity{}, ErrTokenInvalid
	}
	now := float64(v.now().Unix())
	skew := clockSkew.Seconds()
	if !claims.Expiration.Present || !claims.IssuedAt.Present || claims.Expiration.Value < now-skew || claims.IssuedAt.Value > now+skew {
		return Identity{}, ErrTokenInvalid
	}
	if claims.Nonce == nil || subtle.ConstantTimeCompare([]byte(*claims.Nonce), []byte(expectedNonce)) != 1 || claims.Subject == "" {
		return Identity{}, ErrTokenInvalid
	}
	email := strings.TrimSpace(claims.Email)
	emailVerified := claims.EmailVerified.Value && email != ""
	privateEmail := claims.PrivateEmail.Value && email != ""
	emailAuthoritative := false
	if emailVerified {
		switch v.provider {
		case ProviderGoogle:
			emailAuthoritative = googleAuthoritativeEmail(email, claims.HostedDomain)
		case ProviderApple:
			emailAuthoritative = true
		}
	}
	return Identity{
		Provider:           v.provider,
		Subject:            claims.Subject,
		Email:              email,
		EmailVerified:      emailVerified,
		PrivateEmail:       privateEmail,
		EmailAuthoritative: emailAuthoritative,
	}, nil
}

func (v *idTokenVerifier) verificationKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	now := v.now()
	v.mu.Lock()
	keys := v.keys
	fetchedAt := v.keysFetchedAt
	coldBlocked := keys == nil && now.Before(v.coldFailureUntil)
	staleAllowed := keys != nil && now.Sub(fetchedAt) >= jwksTTL && !now.Before(v.staleRefreshAfter)
	v.mu.Unlock()

	refreshed := false
	var refreshErr error
	if keys == nil {
		if coldBlocked {
			return nil, ErrProviderUnreachable
		}
		refreshed, refreshErr = v.refreshKeys(ctx, refreshCold)
		if refreshErr != nil {
			return nil, refreshErr
		}
		keys = v.currentKeys()
	} else if staleAllowed {
		refreshed, refreshErr = v.refreshKeys(ctx, refreshStale)
		if refreshErr == nil {
			keys = v.currentKeys()
		}
	}
	if key, ok := rsaKey(keys, kid); ok {
		return key, nil
	}
	if refreshErr != nil {
		return nil, refreshErr
	}
	if refreshed {
		v.mu.Lock()
		v.lastUnknownRefresh = v.now()
		v.mu.Unlock()
		return nil, ErrTokenInvalid
	}
	_, err := v.refreshKeys(ctx, refreshUnknownKID)
	if err != nil {
		return nil, err
	}
	if key, ok := rsaKey(v.currentKeys(), kid); ok {
		return key, nil
	}
	return nil, ErrTokenInvalid
}

type refreshReason uint8

const (
	refreshCold refreshReason = iota
	refreshStale
	refreshUnknownKID
)

func (v *idTokenVerifier) refreshKeys(ctx context.Context, reason refreshReason) (bool, error) {
	now := v.now()
	v.mu.Lock()
	if v.inflight != nil {
		fetch := v.inflight
		v.mu.Unlock()
		return true, waitForJWKS(ctx, fetch)
	}
	switch reason {
	case refreshCold:
		if v.keys != nil {
			v.mu.Unlock()
			return false, nil
		}
		if now.Before(v.coldFailureUntil) {
			v.mu.Unlock()
			return false, ErrProviderUnreachable
		}
	case refreshStale:
		if now.Before(v.staleRefreshAfter) {
			v.mu.Unlock()
			return false, nil
		}
	case refreshUnknownKID:
		if now.Before(v.staleRefreshAfter) {
			v.mu.Unlock()
			return false, ErrProviderUnreachable
		}
		if !v.lastUnknownRefresh.IsZero() && now.Sub(v.lastUnknownRefresh) < unknownKIDRefreshMin {
			v.mu.Unlock()
			return false, ErrTokenInvalid
		}
		v.lastUnknownRefresh = now
	}
	fetch := &jwksFetch{done: make(chan struct{})}
	v.inflight = fetch
	v.mu.Unlock()

	go v.runJWKSFetch(fetch)
	return true, waitForJWKS(ctx, fetch)
}

func (v *idTokenVerifier) runJWKSFetch(fetch *jwksFetch) {
	keys, err := v.fetchKeys(context.Background())
	now := v.now()
	v.mu.Lock()
	if err == nil {
		v.keys = keys
		v.keysFetchedAt = now
		v.coldFailureUntil = time.Time{}
		v.staleRefreshAfter = time.Time{}
	} else if v.keys == nil {
		v.coldFailureUntil = now.Add(coldFetchFailureMemory)
	} else {
		v.staleRefreshAfter = now.Add(staleFetchRetryMin)
	}
	fetch.err = err
	v.inflight = nil
	close(fetch.done)
	v.mu.Unlock()
}

func waitForJWKS(ctx context.Context, fetch *jwksFetch) error {
	select {
	case <-ctx.Done():
		return ErrProviderUnreachable
	case <-fetch.done:
		return fetch.err
	}
}

func (v *idTokenVerifier) currentKeys() jwk.Set {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.keys
}

func googleAuthoritativeEmail(email, hostedDomain string) bool {
	if strings.TrimSpace(hostedDomain) != "" {
		return true
	}
	at := strings.LastIndexByte(email, '@')
	if at < 0 {
		return false
	}
	domain := email[at+1:]
	return strings.EqualFold(domain, "gmail.com") || strings.EqualFold(domain, "googlemail.com")
}

func (v *idTokenVerifier) fetchKeys(ctx context.Context) (jwk.Set, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.jwksURL, nil)
	if err != nil {
		return nil, ErrProviderUnreachable
	}
	req.Header.Set("Accept", "application/json")
	response, err := v.client.Do(req)
	if err != nil {
		return nil, ErrProviderUnreachable
	}
	defer response.Body.Close()
	body, tooLarge, err := readCapped(response.Body)
	if err != nil || tooLarge || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, ErrProviderUnreachable
	}
	set, err := jwk.Parse(body)
	if err != nil || set.Len() == 0 {
		return nil, ErrProviderUnreachable
	}
	return set, nil
}

func rsaKey(set jwk.Set, kid string) (*rsa.PublicKey, bool) {
	key, ok := set.LookupKeyID(kid)
	if !ok {
		return nil, false
	}
	var raw interface{}
	if err := key.Raw(&raw); err != nil {
		return nil, false
	}
	switch key := raw.(type) {
	case *rsa.PublicKey:
		return key, true
	case rsa.PublicKey:
		return &key, true
	default:
		return nil, false
	}
}

func compactHeader(compact string) (struct {
	Algorithm string `json:"alg"`
	KeyID     string `json:"kid"`
}, bool) {
	var header struct {
		Algorithm string `json:"alg"`
		KeyID     string `json:"kid"`
	}
	parts := strings.Split(compact, ".")
	if len(parts) != 3 || len(parts[0]) > 4096 {
		return header, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(raw, &header) != nil {
		return header, false
	}
	return header, true
}

type idTokenClaims struct {
	Issuer          string        `json:"iss"`
	Audience        audienceClaim `json:"aud"`
	AuthorizedParty string        `json:"azp"`
	Expiration      numericDate   `json:"exp"`
	IssuedAt        numericDate   `json:"iat"`
	Nonce           *string       `json:"nonce"`
	Subject         string        `json:"sub"`
	Email           string        `json:"email"`
	EmailVerified   booleanClaim  `json:"email_verified"`
	PrivateEmail    booleanClaim  `json:"is_private_email"`
	HostedDomain    string        `json:"hd"`
}

type audienceClaim struct {
	Values  []string
	Present bool
}

func (a *audienceClaim) UnmarshalJSON(data []byte) error {
	a.Present = true
	var one string
	if err := json.Unmarshal(data, &one); err == nil {
		a.Values = []string{one}
		return nil
	}
	return json.Unmarshal(data, &a.Values)
}

func (a audienceClaim) Contains(want string) bool {
	if !a.Present {
		return false
	}
	for _, value := range a.Values {
		if value == want {
			return true
		}
	}
	return false
}

type numericDate struct {
	Value   float64
	Present bool
}

func (n *numericDate) UnmarshalJSON(data []byte) error {
	n.Present = true
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return err
	}
	value, err := number.Float64()
	if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
		return ErrTokenInvalid
	}
	n.Value = value
	return nil
}

type booleanClaim struct {
	Value   bool
	Present bool
}

func (b *booleanClaim) UnmarshalJSON(data []byte) error {
	b.Present = true
	var value bool
	if err := json.Unmarshal(data, &value); err == nil {
		b.Value = value
		return nil
	}
	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}
	switch text {
	case "true":
		b.Value = true
	case "false":
		b.Value = false
	default:
		return ErrTokenInvalid
	}
	return nil
}
