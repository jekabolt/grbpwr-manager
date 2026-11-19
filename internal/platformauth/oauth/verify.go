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
	clockSkew            = 60 * time.Second
	unknownKIDRefreshMin = time.Minute
)

type idTokenVerifier struct {
	provider string
	audience string
	jwksURL  string
	issuers  map[string]struct{}
	client   *http.Client
	now      func() time.Time

	mu                 sync.Mutex
	keys               jwk.Set
	lastUnknownRefresh time.Time
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
	return Identity{
		Provider:      v.provider,
		Subject:       claims.Subject,
		Email:         claims.Email,
		EmailVerified: claims.EmailVerified.Value,
		PrivateEmail:  claims.PrivateEmail.Value,
	}, nil
}

func (v *idTokenVerifier) verificationKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.keys == nil {
		keys, err := v.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		v.keys = keys
	}
	if key, ok := rsaKey(v.keys, kid); ok {
		return key, nil
	}
	now := v.now()
	if !v.lastUnknownRefresh.IsZero() && now.Sub(v.lastUnknownRefresh) < unknownKIDRefreshMin {
		return nil, ErrTokenInvalid
	}
	v.lastUnknownRefresh = now
	keys, err := v.fetchKeys(ctx)
	if err != nil {
		return nil, err
	}
	v.keys = keys
	if key, ok := rsaKey(v.keys, kid); ok {
		return key, nil
	}
	return nil, ErrTokenInvalid
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
