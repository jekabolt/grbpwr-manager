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
	if strings.TrimSpace(p.MagicLinkBaseURL) == "" {
		return nil, fmt.Errorf("storefront_auth.magic_link_base_url is required")
	}

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
		accessJwtAuth:              jwtauth.New("HS256", []byte(p.AccessJWTSecret), nil),
		accessTTL:                  at,
		accessExpectations:         accessExp,
		accessIssueOpts:            accessOpts,
		accessJtiRevocationEnabled: p.AccessJtiRevocationEnabled,
		refreshTTL:                 rt,
		loginChallengeTTL:  lt,
		loginPepper:        p.LoginPepper,
		refreshPepper:      p.RefreshPepper,
		magicLinkBaseURL:   strings.TrimRight(strings.TrimSpace(p.MagicLinkBaseURL), "/"),
	}, nil
}
