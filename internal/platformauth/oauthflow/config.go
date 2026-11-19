// Package oauthflow is provider sign-in over HTTP (teams O §1, §3, lane O3): the boot-time provider
// registry built from the OAUTH_* settings, and the plain chi handlers of the start and the two
// callbacks. The rules after a verified identity (link, create, join) are platformauth's; this
// package only carries the browser through the provider and back to one fixed root page.
//
// It never imports platformauth (platformauth's own tests drive it end to end).
package oauthflow

import (
	"errors"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// BuildConfig reads the OAUTH_* settings strictly, for the boot of teams mode: a provider that is
// half set, an OAUTH_APPLE_ENABLED that is not a boolean, an Apple key that does not parse or an
// unusable callback base is an error that names the variable and never prints its value. Nothing
// set is no error and no provider. The Apple key may carry literal \n sequences (one-line env
// values); they become newlines.
func BuildConfig(c platformstore.Config) (oauth.Config, error) {
	o := c.OAuth
	gid, gsecret := strings.TrimSpace(o.GoogleClientID), strings.TrimSpace(o.GoogleClientSecret)
	switch {
	case gid != "" && gsecret == "":
		return oauth.Config{}, errors.New("oauthflow: OAUTH_GOOGLE_CLIENT_SECRET is not set while OAUTH_GOOGLE_CLIENT_ID is")
	case gid == "" && gsecret != "":
		return oauth.Config{}, errors.New("oauthflow: OAUTH_GOOGLE_CLIENT_ID is not set while OAUTH_GOOGLE_CLIENT_SECRET is")
	}
	apple := false
	if raw := strings.TrimSpace(o.AppleEnabled); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return oauth.Config{}, errors.New("oauthflow: OAUTH_APPLE_ENABLED must be true or false")
		}
		apple = v
	}
	cfg := oauth.Config{GoogleClientID: gid, GoogleClientSecret: gsecret, AppleEnabled: apple}
	if apple {
		cfg.AppleServicesID = strings.TrimSpace(o.AppleServicesID)
		cfg.AppleTeamID = strings.TrimSpace(o.AppleTeamID)
		cfg.AppleKeyID = strings.TrimSpace(o.AppleKeyID)
		cfg.ApplePrivateKey = strings.TrimSpace(strings.ReplaceAll(o.ApplePrivateKey, `\n`, "\n"))
		var missing []string
		for _, v := range []struct{ name, value string }{
			{"OAUTH_APPLE_SERVICES_ID", cfg.AppleServicesID},
			{"OAUTH_APPLE_TEAM_ID", cfg.AppleTeamID},
			{"OAUTH_APPLE_KEY_ID", cfg.AppleKeyID},
			{"OAUTH_APPLE_PRIVATE_KEY", cfg.ApplePrivateKey},
		} {
			if v.value == "" {
				missing = append(missing, v.name)
			}
		}
		if len(missing) > 0 {
			return oauth.Config{}, errors.New("oauthflow: OAUTH_APPLE_ENABLED is true but " + strings.Join(missing, ", ") + " not set")
		}
	}
	if gid == "" && !apple {
		return cfg, nil
	}
	if strings.Trim(strings.TrimSpace(c.BaseDomain), ".") == "" {
		// The result page is https://<PLATFORM_BASE_DOMAIN>/auth/oauth.
		return oauth.Config{}, errors.New("oauthflow: PLATFORM_BASE_DOMAIN is not set while a provider is")
	}
	cfg.CallbackBaseURL = c.OAuthCallbackBase()
	if err := cfg.Validate(); err != nil {
		switch err.Error() {
		case "oauth: apple private key invalid":
			return oauth.Config{}, errors.New("oauthflow: OAUTH_APPLE_PRIVATE_KEY is not a PKCS#8 P-256 PEM key")
		case "oauth: invalid callback base URL":
			return oauth.Config{}, errors.New("oauthflow: the callback base (OAUTH_CALLBACK_BASE, else https://api.<PLATFORM_BASE_DOMAIN>) is not an https origin")
		}
		return oauth.Config{}, errors.New("oauthflow: OAUTH_* settings incomplete")
	}
	return cfg, nil
}

// NewRegistry is the boot-time provider registry of teams mode: BuildConfig, then the providers it
// enables. Nothing set gives an empty registry (every provider route answers 404).
func NewRegistry(c platformstore.Config, opts ...oauth.Option) (*oauth.Registry, error) {
	cfg, err := BuildConfig(c)
	if err != nil {
		return nil, err
	}
	reg, err := oauth.NewRegistry(cfg, opts...)
	if err != nil {
		// BuildConfig validated the settings; what is left is an option (tests) or an endpoint.
		return nil, errors.New("oauthflow: provider registry not built")
	}
	return reg, nil
}

// Buttons is platformauth.SignInOptions over a registry: a button only for a provider the
// registry built.
type Buttons struct{ google, apple bool }

// Options reads which providers reg built (nil = none).
func Options(reg *oauth.Registry) Buttons {
	g, a := reg.Enabled()
	return Buttons{google: g, apple: a}
}

func (b Buttons) Google() bool { return b.google }
func (b Buttons) Apple() bool  { return b.apple }
