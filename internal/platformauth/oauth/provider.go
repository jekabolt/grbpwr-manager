package oauth

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/lestrrat-go/jwx/jwa"
	"github.com/lestrrat-go/jwx/jws"
	"github.com/lestrrat-go/jwx/jwt"
)

const (
	maxProviderBody      = 64 << 10
	appleIssuer          = "https://appleid.apple.com"
	appleClientSecretTTL = 5 * time.Minute
)

// Identity is the verified provider identity. Email may be empty, notably on
// Apple callbacks after the first consent.
type Identity struct {
	Provider      string
	Subject       string
	Email         string
	EmailVerified bool
	PrivateEmail  bool
}

// Provider performs the provider-specific authorization and token flow.
type Provider interface {
	AuthURL(state, nonce, codeChallenge string) string
	Exchange(ctx context.Context, code, codeVerifier, nonce string) (Identity, error)
}

type provider struct {
	name         string
	clientID     string
	clientSecret string
	redirectURL  string
	endpoints    ProviderEndpoints
	client       *http.Client
	verifier     *idTokenVerifier
	now          func() time.Time

	appleTeamID     string
	appleKeyID      string
	applePrivateKey *ecdsa.PrivateKey
}

func newGoogleProvider(config Config, endpoints ProviderEndpoints, client *http.Client, now func() time.Time) Provider {
	return &provider{
		name:         ProviderGoogle,
		clientID:     config.GoogleClientID,
		clientSecret: config.GoogleClientSecret,
		redirectURL:  callbackURL(config.CallbackBaseURL, ProviderGoogle),
		endpoints:    endpoints,
		client:       client,
		verifier: newIDTokenVerifier(ProviderGoogle, config.GoogleClientID, endpoints.JWKSURL,
			[]string{"https://accounts.google.com", "accounts.google.com"}, client, now),
		now: now,
	}
}

func newAppleProvider(config Config, privateKey *ecdsa.PrivateKey, endpoints ProviderEndpoints, client *http.Client, now func() time.Time) Provider {
	return &provider{
		name:            ProviderApple,
		clientID:        config.AppleServicesID,
		redirectURL:     callbackURL(config.CallbackBaseURL, ProviderApple),
		endpoints:       endpoints,
		client:          client,
		verifier:        newIDTokenVerifier(ProviderApple, config.AppleServicesID, endpoints.JWKSURL, []string{appleIssuer}, client, now),
		now:             now,
		appleTeamID:     config.AppleTeamID,
		appleKeyID:      config.AppleKeyID,
		applePrivateKey: privateKey,
	}
}

func callbackURL(base, name string) string {
	return strings.TrimRight(base, "/") + "/api/auth/oauth/" + name + "/callback"
}

func (p *provider) AuthURL(state, nonce, codeChallenge string) string {
	u, _ := url.Parse(p.endpoints.AuthorizationURL)
	query := url.Values{
		"client_id":             {p.clientID},
		"redirect_uri":          {p.redirectURL},
		"response_type":         {"code"},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	if p.name == ProviderGoogle {
		query.Set("scope", "openid email")
	} else {
		query.Set("scope", "email")
		query.Set("response_mode", "form_post")
	}
	u.RawQuery = query.Encode()
	return u.String()
}

func (p *provider) Exchange(ctx context.Context, code, codeVerifier, nonce string) (Identity, error) {
	secret := p.clientSecret
	if p.name == ProviderApple {
		var err error
		secret, err = p.appleClientSecret()
		if err != nil {
			return Identity{}, ErrProviderUnreachable
		}
	}
	form := url.Values{
		"client_id":     {p.clientID},
		"client_secret": {secret},
		"code":          {code},
		"code_verifier": {codeVerifier},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {p.redirectURL},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoints.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Identity{}, ErrProviderUnreachable
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	response, err := p.client.Do(req)
	if err != nil {
		return Identity{}, ErrProviderUnreachable
	}
	defer response.Body.Close()
	body, tooLarge, err := readCapped(response.Body)
	if err != nil || tooLarge {
		return Identity{}, ErrProviderUnreachable
	}
	if response.StatusCode >= 400 && response.StatusCode < 500 {
		return Identity{}, ErrTokenInvalid
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Identity{}, ErrProviderUnreachable
	}
	var tokenResponse struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &tokenResponse); err != nil || tokenResponse.IDToken == "" {
		return Identity{}, ErrTokenInvalid
	}
	return p.verifier.verify(ctx, tokenResponse.IDToken, nonce)
}

func (p *provider) appleClientSecret() (string, error) {
	now := p.now().UTC().Truncate(time.Second)
	token := jwt.New()
	for claim, value := range map[string]interface{}{
		jwt.IssuerKey:     p.appleTeamID,
		jwt.SubjectKey:    p.clientID,
		jwt.AudienceKey:   appleIssuer,
		jwt.IssuedAtKey:   now,
		jwt.ExpirationKey: now.Add(appleClientSecretTTL),
	} {
		if err := token.Set(claim, value); err != nil {
			return "", err
		}
	}
	headers := jws.NewHeaders()
	if err := headers.Set(jws.KeyIDKey, p.appleKeyID); err != nil {
		return "", err
	}
	signed, err := jwt.Sign(token, jwa.ES256, p.applePrivateKey, jwt.WithJwsHeaders(headers))
	if err != nil {
		return "", err
	}
	return string(signed), nil
}

func readCapped(reader io.Reader) (body []byte, tooLarge bool, err error) {
	body, err = io.ReadAll(io.LimitReader(reader, maxProviderBody+1))
	if err != nil {
		return nil, false, err
	}
	if len(body) > maxProviderBody {
		return nil, true, nil
	}
	return body, false, nil
}
