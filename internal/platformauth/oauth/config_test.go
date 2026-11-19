package oauth_test

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"net/url"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
)

func TestConfigValidate(t *testing.T) {
	privateKey, privatePEM := appleKey(t)
	_ = privateKey
	tests := []struct {
		name    string
		config  oauth.Config
		wantErr bool
	}{
		{name: "empty", config: oauth.Config{}},
		{name: "google id only", config: oauth.Config{GoogleClientID: "client"}, wantErr: true},
		{name: "google secret only", config: oauth.Config{GoogleClientSecret: "secret"}, wantErr: true},
		{name: "google complete", config: oauth.Config{
			GoogleClientID: "client", GoogleClientSecret: "secret", CallbackBaseURL: "https://api.example.test",
		}},
		{name: "apple disabled bad key", config: oauth.Config{ApplePrivateKey: "not a key"}},
		{name: "apple enabled missing", config: oauth.Config{AppleEnabled: true}, wantErr: true},
		{name: "apple enabled bad key", config: oauth.Config{
			AppleEnabled: true, AppleServicesID: "service", AppleTeamID: "team", AppleKeyID: "kid", ApplePrivateKey: "bad",
		}, wantErr: true},
		{name: "apple complete", config: oauth.Config{
			AppleEnabled: true, AppleServicesID: "service", AppleTeamID: "team", AppleKeyID: "kid", ApplePrivateKey: privatePEM,
			CallbackBaseURL: "https://api.example.test",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
			if err != nil && (test.config.GoogleClientSecret != "" && contains(err.Error(), test.config.GoogleClientSecret) ||
				test.config.ApplePrivateKey != "" && contains(err.Error(), test.config.ApplePrivateKey)) {
				t.Fatal("configuration secret appeared in error")
			}
		})
	}
}

func TestCallbackBaseURLValidation(t *testing.T) {
	base := oauth.Config{GoogleClientID: "client", GoogleClientSecret: "secret"}
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "empty", wantErr: true},
		{name: "http non-loopback", url: "http://api.example.test", wantErr: true},
		{name: "path", url: "https://api.example.test/base", wantErr: true},
		{name: "query", url: "https://api.example.test?mode=oauth", wantErr: true},
		{name: "userinfo", url: "https://user@api.example.test", wantErr: true},
		{name: "fragment", url: "https://api.example.test#oauth", wantErr: true},
		{name: "good", url: "https://api.example.test/"},
		{name: "localhost", url: "http://localhost:8081"},
		{name: "ipv4 loopback", url: "http://127.0.0.1:8081/"},
		{name: "ipv6 loopback", url: "http://[::1]:8081"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			config.CallbackBaseURL = test.url
			err := config.Validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("Validate() error = %v, want error %v", err, test.wantErr)
			}
		})
	}

	if err := (oauth.Config{CallbackBaseURL: ":// ignored without providers"}).Validate(); err != nil {
		t.Fatalf("disabled providers validated callback URL: %v", err)
	}
}

func TestEndpointSchemeValidation(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{name: "https", url: "https://idp.example.test/authorize"},
		{name: "http localhost", url: "http://localhost:9090/authorize"},
		{name: "http ipv4 loopback", url: "http://127.0.0.1:9090/authorize"},
		{name: "http ipv6 loopback", url: "http://[::1]:9090/authorize"},
		{name: "http non-loopback", url: "http://idp.example.test/authorize", wantErr: true},
		{name: "other scheme", url: "ftp://127.0.0.1/authorize", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoints := oauth.ProductionEndpoints()
			endpoints.Google.AuthorizationURL = test.url
			_, err := oauth.NewRegistry(oauth.Config{
				GoogleClientID: "client", GoogleClientSecret: "secret", CallbackBaseURL: "https://api.example.test",
			}, oauth.WithEndpoints(endpoints))
			if (err != nil) != test.wantErr {
				t.Fatalf("NewRegistry() error = %v, want error %v", err, test.wantErr)
			}
		})
	}

	for _, field := range []string{"authorization", "token", "jwks"} {
		t.Run("reject "+field, func(t *testing.T) {
			endpoints := oauth.ProductionEndpoints()
			switch field {
			case "authorization":
				endpoints.Google.AuthorizationURL = "http://idp.example.test/authorize"
			case "token":
				endpoints.Google.TokenURL = "http://idp.example.test/token"
			case "jwks":
				endpoints.Google.JWKSURL = "http://idp.example.test/jwks"
			}
			_, err := oauth.NewRegistry(oauth.Config{
				GoogleClientID: "client", GoogleClientSecret: "secret", CallbackBaseURL: "https://api.example.test",
			}, oauth.WithEndpoints(endpoints))
			if err == nil {
				t.Fatal("NewRegistry() accepted insecure endpoint")
			}
		})
	}
}

func TestRegistryAndAuthURLs(t *testing.T) {
	_, privatePEM := appleKey(t)
	config := oauth.Config{
		GoogleClientID: "google-client", GoogleClientSecret: "google-secret",
		AppleEnabled: true, AppleServicesID: "apple-service", AppleTeamID: "apple-team",
		AppleKeyID: "apple-key", ApplePrivateKey: privatePEM,
		CallbackBaseURL: "https://api.beta.lekalo.studio/",
	}
	registry, err := oauth.NewRegistry(config)
	if err != nil {
		t.Fatal(err)
	}
	googleEnabled, appleEnabled := registry.Enabled()
	if !googleEnabled || !appleEnabled {
		t.Fatalf("Enabled() = %v, %v", googleEnabled, appleEnabled)
	}
	if _, ok := registry.Provider("Google"); ok {
		t.Fatal("provider lookup accepted non-canonical name")
	}

	tests := []struct {
		name     string
		clientID string
		want     url.Values
	}{
		{
			name: "google", clientID: "google-client",
			want: url.Values{
				"client_id": {"google-client"}, "redirect_uri": {"https://api.beta.lekalo.studio/api/auth/oauth/google/callback"},
				"response_type": {"code"}, "scope": {"openid email"}, "state": {"state"}, "nonce": {"nonce"},
				"code_challenge": {"challenge"}, "code_challenge_method": {"S256"},
			},
		},
		{
			name: "apple", clientID: "apple-service",
			want: url.Values{
				"client_id": {"apple-service"}, "redirect_uri": {"https://api.beta.lekalo.studio/api/auth/oauth/apple/callback"},
				"response_type": {"code"}, "response_mode": {"form_post"}, "scope": {"email"}, "state": {"state"}, "nonce": {"nonce"},
				"code_challenge": {"challenge"}, "code_challenge_method": {"S256"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider, ok := registry.Provider(test.name)
			if !ok {
				t.Fatal("provider absent")
			}
			u, err := url.Parse(provider.AuthURL("state", "nonce", "challenge"))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(u.Query(), test.want) {
				t.Fatalf("query = %#v, want %#v", u.Query(), test.want)
			}
		})
	}

	empty, err := oauth.NewRegistry(oauth.Config{})
	if err != nil {
		t.Fatal(err)
	}
	googleEnabled, appleEnabled = empty.Enabled()
	if googleEnabled || appleEnabled {
		t.Fatal("empty registry enabled a provider")
	}
}

func TestCallbackError(t *testing.T) {
	for _, providerValue := range []string{"access_denied", "user_cancelled", "user_cancelled_authorize"} {
		if err := oauth.CallbackError(url.Values{"error": {providerValue}}); !errors.Is(err, oauth.ErrCancelled) {
			t.Fatalf("CallbackError(%q) = %v", providerValue, err)
		}
	}
	if err := oauth.CallbackError(url.Values{}); err != nil {
		t.Fatalf("no callback error = %v", err)
	}
	if err := oauth.CallbackError(url.Values{"error": {"server_error"}}); !errors.Is(err, oauth.ErrProviderUnreachable) {
		t.Fatalf("server callback error = %v", err)
	}
}

func TestRandomAndPKCEHelpers(t *testing.T) {
	state, err := oauth.NewState()
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := oauth.NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := oauth.NewVerifier()
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"state": state, "nonce": nonce, "verifier": verifier} {
		decoded, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil || len(decoded) != 32 {
			t.Fatalf("%s is not 32 random bytes: %v", name, err)
		}
	}
	if state == nonce || nonce == verifier || state == verifier {
		t.Fatal("random helpers repeated a value")
	}
	sum := sha256.Sum256([]byte(verifier))
	if got, want := oauth.Challenge(verifier), base64.RawURLEncoding.EncodeToString(sum[:]); got != want {
		t.Fatalf("Challenge() = %q, want %q", got, want)
	}
}

func appleKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: raw}))
}

func contains(haystack, needle string) bool {
	if needle == "" || len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
