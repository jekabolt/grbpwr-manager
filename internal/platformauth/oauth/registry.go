package oauth

import (
	"errors"
	"net/http"
	"net/url"
	"time"
)

const (
	ProviderGoogle = "google"
	ProviderApple  = "apple"

	defaultHTTPTimeout = 5 * time.Second
)

// ProviderEndpoints are the three network endpoints used by one provider.
type ProviderEndpoints struct {
	AuthorizationURL string
	TokenURL         string
	JWKSURL          string
}

// Endpoints holds provider endpoints. Production defaults are used for empty
// fields; tests can replace individual providers with WithEndpoints.
type Endpoints struct {
	Google ProviderEndpoints
	Apple  ProviderEndpoints
}

// ProductionEndpoints returns the provider endpoints used outside tests.
func ProductionEndpoints() Endpoints {
	return Endpoints{
		Google: ProviderEndpoints{
			AuthorizationURL: "https://accounts.google.com/o/oauth2/v2/auth",
			TokenURL:         "https://oauth2.googleapis.com/token",
			JWKSURL:          "https://www.googleapis.com/oauth2/v3/certs",
		},
		Apple: ProviderEndpoints{
			AuthorizationURL: "https://appleid.apple.com/auth/authorize",
			TokenURL:         "https://appleid.apple.com/auth/token",
			JWKSURL:          "https://appleid.apple.com/auth/keys",
		},
	}
}

type registryOptions struct {
	endpoints Endpoints
	client    *http.Client
	now       func() time.Time
}

// Option changes registry construction without reading process state.
type Option func(*registryOptions) error

// WithEndpoints replaces non-empty production endpoint fields.
func WithEndpoints(endpoints Endpoints) Option {
	return func(options *registryOptions) error {
		mergeProviderEndpoints(&options.endpoints.Google, endpoints.Google)
		mergeProviderEndpoints(&options.endpoints.Apple, endpoints.Apple)
		return nil
	}
}

// WithHTTPClient supplies transport settings. The client is copied, its
// timeout is capped at five seconds, and redirects are always disabled.
func WithHTTPClient(client *http.Client) Option {
	return func(options *registryOptions) error {
		if client == nil {
			return errors.New("oauth: nil HTTP client")
		}
		options.client = client
		return nil
	}
}

// WithClock supplies a clock for deterministic tests.
func WithClock(now func() time.Time) Option {
	return func(options *registryOptions) error {
		if now == nil {
			return errors.New("oauth: nil clock")
		}
		options.now = now
		return nil
	}
}

// Registry contains only configured and enabled providers.
type Registry struct {
	providers map[string]Provider
}

// NewRegistry validates config and builds the enabled providers.
func NewRegistry(config Config, options ...Option) (*Registry, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	ro := registryOptions{
		endpoints: ProductionEndpoints(),
		client:    &http.Client{},
		now:       time.Now,
	}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("oauth: nil option")
		}
		if err := option(&ro); err != nil {
			return nil, err
		}
	}
	client := hardenedHTTPClient(ro.client)
	registry := &Registry{providers: make(map[string]Provider, 2)}
	if config.GoogleClientID != "" {
		if err := validateEndpoints(ro.endpoints.Google); err != nil {
			return nil, err
		}
		registry.providers[ProviderGoogle] = newGoogleProvider(config, ro.endpoints.Google, client, ro.now)
	}
	if config.AppleEnabled {
		if err := validateEndpoints(ro.endpoints.Apple); err != nil {
			return nil, err
		}
		privateKey, err := parseApplePrivateKey(config.ApplePrivateKey)
		if err != nil {
			return nil, errors.New("oauth: apple private key invalid")
		}
		registry.providers[ProviderApple] = newAppleProvider(config, privateKey, ro.endpoints.Apple, client, ro.now)
	}
	return registry, nil
}

// Provider returns a configured provider by its canonical lower-case name.
func (r *Registry) Provider(name string) (Provider, bool) {
	if r == nil {
		return nil, false
	}
	provider, ok := r.providers[name]
	return provider, ok
}

// Enabled reports which provider buttons should be offered.
func (r *Registry) Enabled() (google, apple bool) {
	if r == nil {
		return false, false
	}
	_, google = r.providers[ProviderGoogle]
	_, apple = r.providers[ProviderApple]
	return google, apple
}

func hardenedHTTPClient(base *http.Client) *http.Client {
	client := *base
	if client.Timeout <= 0 || client.Timeout > defaultHTTPTimeout {
		client.Timeout = defaultHTTPTimeout
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func mergeProviderEndpoints(dst *ProviderEndpoints, src ProviderEndpoints) {
	if src.AuthorizationURL != "" {
		dst.AuthorizationURL = src.AuthorizationURL
	}
	if src.TokenURL != "" {
		dst.TokenURL = src.TokenURL
	}
	if src.JWKSURL != "" {
		dst.JWKSURL = src.JWKSURL
	}
}

func validateEndpoints(endpoints ProviderEndpoints) error {
	for _, raw := range []string{endpoints.AuthorizationURL, endpoints.TokenURL, endpoints.JWKSURL} {
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return errors.New("oauth: invalid provider endpoint")
		}
	}
	return nil
}
