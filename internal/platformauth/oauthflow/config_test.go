package oauthflow

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// p8Key is a fresh Apple-style .p8 key (PKCS#8 P-256 PEM).
func p8Key(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func platformCfg(o platformstore.OAuthConfig) platformstore.Config {
	return platformstore.Config{BaseDomain: "beta.lekalo.studio", OAuth: o}
}

// The boot matrix (O3 brief): what each OAUTH_* shape builds, and that a refusal names the
// variable and never prints a value.
func TestBootMatrix(t *testing.T) {
	key := p8Key(t)
	oneLine := strings.ReplaceAll(strings.TrimSpace(key), "\n", `\n`)
	if strings.Contains(oneLine, "\n") || !strings.Contains(oneLine, `\n`) {
		t.Fatal("the one-line key is not one line")
	}
	apple := func(mut func(*platformstore.OAuthConfig)) platformstore.OAuthConfig {
		o := platformstore.OAuthConfig{
			AppleEnabled: "true", AppleServicesID: "svc-value-7", AppleTeamID: "team-value-7",
			AppleKeyID: "kid-value-7", ApplePrivateKey: key,
		}
		if mut != nil {
			mut(&o)
		}
		return o
	}
	cases := []struct {
		name          string
		cfg           platformstore.Config
		google, apple bool
		wantErr       string   // substring of the error ("" = none)
		secrets       []string // never in the error
	}{
		{name: "nothing set", cfg: platformCfg(platformstore.OAuthConfig{})},
		{name: "nothing set, apple false", cfg: platformCfg(platformstore.OAuthConfig{AppleEnabled: "false", AppleTeamID: "x"})},
		{name: "google only", cfg: platformCfg(platformstore.OAuthConfig{GoogleClientID: " gid-value-7 ", GoogleClientSecret: "gsecret-value-7"}), google: true},
		{name: "google id only", cfg: platformCfg(platformstore.OAuthConfig{GoogleClientID: "gid-value-7"}),
			wantErr: "OAUTH_GOOGLE_CLIENT_SECRET", secrets: []string{"gid-value-7"}},
		{name: "google secret only", cfg: platformCfg(platformstore.OAuthConfig{GoogleClientSecret: "gsecret-value-7"}),
			wantErr: "OAUTH_GOOGLE_CLIENT_ID", secrets: []string{"gsecret-value-7"}},
		{name: "apple enabled junk", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) { o.AppleEnabled = "yes please" })),
			wantErr: "OAUTH_APPLE_ENABLED", secrets: []string{"yes please"}},
		{name: "apple on, team id missing", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) { o.AppleTeamID = " " })),
			wantErr: "OAUTH_APPLE_TEAM_ID", secrets: []string{"svc-value-7", "kid-value-7", "BEGIN"}},
		{name: "apple on, key missing", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) { o.ApplePrivateKey = "" })),
			wantErr: "OAUTH_APPLE_PRIVATE_KEY", secrets: []string{"svc-value-7", "team-value-7"}},
		{name: "apple on, key junk", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) { o.ApplePrivateKey = "junk-key-value-7" })),
			wantErr: "OAUTH_APPLE_PRIVATE_KEY", secrets: []string{"junk-key-value-7"}},
		{name: "apple on, PEM key", cfg: platformCfg(apple(nil)), apple: true},
		{name: "apple on, literal \\n key", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) { o.ApplePrivateKey = oneLine })), apple: true},
		{name: "both", cfg: platformCfg(apple(func(o *platformstore.OAuthConfig) {
			o.GoogleClientID, o.GoogleClientSecret = "gid-value-7", "gsecret-value-7"
		})), google: true, apple: true},
		{name: "bad callback base", cfg: platformCfg(platformstore.OAuthConfig{
			GoogleClientID: "gid-value-7", GoogleClientSecret: "gsecret-value-7", CallbackBase: "http://api.example.com/x?y",
		}), wantErr: "OAUTH_CALLBACK_BASE", secrets: []string{"gsecret-value-7", "api.example.com"}},
		{name: "no base domain", cfg: platformstore.Config{OAuth: platformstore.OAuthConfig{
			GoogleClientID: "gid-value-7", GoogleClientSecret: "gsecret-value-7",
		}}, wantErr: "PLATFORM_BASE_DOMAIN", secrets: []string{"gsecret-value-7"}},
		{name: "local callback base", cfg: platformCfg(platformstore.OAuthConfig{
			GoogleClientID: "gid-value-7", GoogleClientSecret: "gsecret-value-7", CallbackBase: "http://localhost:8081",
		}), google: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reg, err := NewRegistry(c.cfg)
			if c.wantErr != "" {
				if err == nil {
					t.Fatalf("boot passed, want an error naming %s", c.wantErr)
				}
				if !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("error %q does not name %s", err, c.wantErr)
				}
				for _, s := range c.secrets {
					if strings.Contains(err.Error(), s) {
						t.Fatalf("error %q prints a value", err)
					}
				}
				if _, berr := BuildConfig(c.cfg); berr == nil || berr.Error() != err.Error() {
					t.Fatalf("BuildConfig = %v, NewRegistry = %v: the boot check and the registry disagree", berr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("boot refused: %v", err)
			}
			g, a := reg.Enabled()
			if g != c.google || a != c.apple {
				t.Fatalf("enabled google=%t apple=%t, want %t %t", g, a, c.google, c.apple)
			}
			b := Options(reg)
			if b.Google() != c.google || b.Apple() != c.apple {
				t.Fatalf("buttons google=%t apple=%t, want %t %t", b.Google(), b.Apple(), c.google, c.apple)
			}
		})
	}
}

// The key's literal \n become newlines, and the callbacks are under https://api.<base> by default.
func TestBuildConfigValues(t *testing.T) {
	key := p8Key(t)
	oneLine := strings.ReplaceAll(strings.TrimSpace(key), "\n", `\n`)
	cfg, err := BuildConfig(platformCfg(platformstore.OAuthConfig{
		GoogleClientID: "gid", GoogleClientSecret: "gsecret",
		AppleEnabled: " TRUE ", AppleServicesID: "svc", AppleTeamID: "team", AppleKeyID: "kid", ApplePrivateKey: oneLine,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ApplePrivateKey != strings.TrimSpace(key) {
		t.Fatal("the literal \\n of the key were not turned into newlines")
	}
	if cfg.CallbackBaseURL != "https://api.beta.lekalo.studio" {
		t.Fatalf("callback base %q", cfg.CallbackBaseURL)
	}
	if Options(nil).Google() || Options(nil).Apple() {
		t.Fatal("a nil registry offers a button")
	}
}
