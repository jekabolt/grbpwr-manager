package frontend

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
)

type storefrontAuthRuntime struct {
	accessJwtAuth              *jwtauth.JWTAuth
	accessTTL                  time.Duration
	accessExpectations         *jwt.Expectations
	accessIssueOpts            *jwt.IssueOpts
	accessJtiRevocationEnabled bool
	refreshTTL                 time.Duration
	loginChallengeTTL          time.Duration
	loginPepper                string
	refreshPepper              string
	magicLinkBaseURL           string
	teamID                     int64 // 0 = the storefront team's (env keys); else a shop team's derived keys
}

func newStorefrontAuthRuntime(p *storefront.Config) (*storefrontAuthRuntime, error) {
	if p == nil {
		return nil, fmt.Errorf("storefront auth config is required")
	}
	if p.AccessJWTSecret == "" {
		return nil, fmt.Errorf("storefront_auth.access_jwt_secret is required")
	}
	if p.LoginPepper == "" || p.RefreshPepper == "" {
		return nil, fmt.Errorf("storefront_auth.login_pepper and refresh_pepper are required")
	}
	// Reject weak symmetric secrets/peppers: the storefront access token is HS256
	// and the login/refresh peppers are HMAC keys, so a short value makes them
	// forgeable. Fail closed at startup rather than in prod.
	for name, v := range map[string]string{
		"storefront_auth.access_jwt_secret": p.AccessJWTSecret,
		"storefront_auth.login_pepper":      p.LoginPepper,
		"storefront_auth.refresh_pepper":    p.RefreshPepper,
	} {
		if err := jwt.RequireStrongSecret(name, v); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(p.MagicLinkBaseURL) == "" {
		return nil, fmt.Errorf("storefront_auth.magic_link_base_url is required")
	}

	rt := authTimings(p)
	rt.accessJwtAuth = jwtauth.New("HS256", []byte(p.AccessJWTSecret), nil)
	rt.loginPepper = p.LoginPepper
	rt.refreshPepper = p.RefreshPepper
	rt.magicLinkBaseURL = strings.TrimRight(strings.TrimSpace(p.MagicLinkBaseURL), "/")
	return rt, nil
}

// defaultShopMagicLinkPath is the storefront's magic-link page; a team shop's letter links to it on
// the shop's own address when no STOREFRONT_AUTH_MAGIC_LINK_BASE_URL is set.
const defaultShopMagicLinkPath = "/login"

// newShopBaseRuntime is the template team shops derive their customer keys over when there is no
// storefront team (TENANT_STOREFRONT_TEAM_ID=none): TTLs, issuer and jti from STOREFRONT_AUTH_*, no
// legacy keys, which deriveShopAuth always replaces. It never fails: a team shop needs only
// STOREFRONT_AUTH_SHOP_MASTER.
func newShopBaseRuntime(p *storefront.Config) *storefrontAuthRuntime {
	if p == nil {
		p = &storefront.Config{}
	}
	rt := authTimings(p)
	rt.magicLinkBaseURL = strings.TrimRight(strings.TrimSpace(p.MagicLinkBaseURL), "/")
	if rt.magicLinkBaseURL == "" {
		rt.magicLinkBaseURL = defaultShopMagicLinkPath
	}
	return rt
}

// authTimings is the key-free part of a customer-auth runtime: TTLs, issuer/audience and jti.
func authTimings(p *storefront.Config) *storefrontAuthRuntime {
	at := 15 * time.Minute
	if p.AccessJWTTTL != "" {
		parsed, err := time.ParseDuration(p.AccessJWTTTL)
		if err != nil {
			slog.Default().Warn("invalid storefront_auth.access_jwt_ttl, using default 15m",
				slog.String("value", p.AccessJWTTTL), slog.String("err", err.Error()))
		} else {
			at = parsed
		}
	}
	rt := 720 * time.Hour
	if p.RefreshTTL != "" {
		parsed, err := time.ParseDuration(p.RefreshTTL)
		if err != nil {
			slog.Default().Warn("invalid storefront_auth.refresh_ttl, using default 720h",
				slog.String("value", p.RefreshTTL), slog.String("err", err.Error()))
		} else {
			rt = parsed
		}
	}
	lt := 10 * time.Minute
	if p.LoginChallengeTTL != "" {
		parsed, err := time.ParseDuration(p.LoginChallengeTTL)
		if err != nil {
			slog.Default().Warn("invalid storefront_auth.login_challenge_ttl, using default 10m",
				slog.String("value", p.LoginChallengeTTL), slog.String("err", err.Error()))
		} else {
			lt = parsed
		}
	}

	var accessExp *jwt.Expectations
	var accessOpts *jwt.IssueOpts
	if p.AccessJWTIssuer != "" || p.AccessJWTAudience != "" {
		accessExp = &jwt.Expectations{Issuer: p.AccessJWTIssuer, Audience: p.AccessJWTAudience}
		accessOpts = &jwt.IssueOpts{Issuer: p.AccessJWTIssuer, Audience: p.AccessJWTAudience}
	}
	if accessOpts != nil && p.AccessJtiRevocationEnabled {
		accessOpts.IncludeJti = true
	} else if p.AccessJtiRevocationEnabled {
		accessOpts = &jwt.IssueOpts{IncludeJti: true}
	}

	return &storefrontAuthRuntime{
		accessTTL:                  at,
		accessExpectations:         accessExp,
		accessIssueOpts:            accessOpts,
		accessJtiRevocationEnabled: p.AccessJtiRevocationEnabled,
		refreshTTL:                 rt,
		loginChallengeTTL:          lt,
	}
}
