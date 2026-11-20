package platformauth

import (
	"context"
	"errors"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Provider sign-in, the parts that call no provider (teams O §1, §2, §12): which buttons to show,
// the completion a callback hands its verified result to, and the one-time code's exchange.

// SignInOptions says which provider buttons the sign-in page shows. Lane O3 swaps in its provider
// registry through Deps.SignInOptions; the default reads the settings.
type SignInOptions interface {
	Google() bool
	Apple() bool
}

// configOptions is the default SignInOptions: a provider is offered only when every value it needs
// is set (Google: client id and secret; Apple: OAUTH_APPLE_ENABLED true and its four values).
type configOptions struct{ c platformstore.OAuthConfig }

// OptionsFromConfig is the default SignInOptions over the OAUTH_* settings.
func OptionsFromConfig(c platformstore.OAuthConfig) SignInOptions { return configOptions{c: c} }

func set(v string) bool { return strings.TrimSpace(v) != "" }

func (o configOptions) Google() bool { return set(o.c.GoogleClientID) && set(o.c.GoogleClientSecret) }

func (o configOptions) Apple() bool {
	return o.c.AppleOn() && set(o.c.AppleServicesID) && set(o.c.AppleTeamID) && set(o.c.AppleKeyID) && set(o.c.ApplePrivateKey)
}

// Fixed keys of a provider sign-in refusal (the callback puts one in #error=).
const (
	KeyUnverified  = "unverified"   // the provider's email cannot back a new link (empty, unverified, not authoritative)
	KeyDisabled    = "disabled"     // the account is disabled
	KeyUsePassword = "use_password" // an operator's account: password only
	KeyTryAgain    = "try_again"    // anything else (a race, the store, the service unusable)
)

// GetSignInOptions tells the sign-in page which provider buttons to show (public, no lookup).
func (s *Service) GetSignInOptions(_ context.Context, _ *auth.GetSignInOptionsRequest) (*auth.GetSignInOptionsResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	return &auth.GetSignInOptionsResponse{Google: s.options.Google(), Apple: s.options.Apple()}, nil
}

// CompleteProviderSignIn is what the provider callback (lane O3) calls with a verified id_token's
// claims: exactly one of code (the raw one-time sign-in code for #code=, 60 s) and key (KeyUnverified,
// KeyDisabled, KeyUsePassword, KeyTryAgain) is non-empty. The rules are platformstore's
// CompleteProviderSignIn; the email is normalised here ("" when it is not a usable address).
func (s *Service) CompleteProviderSignIn(ctx context.Context, in platformstore.ProviderSignIn) (code, key string) {
	if s.usable() != nil {
		return "", KeyTryAgain
	}
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		email = ""
	}
	// Store only the exact address that passed the account-identity rule. A provider may still
	// authenticate an already-linked subject without a usable current email.
	in.Email = email
	now := s.now()
	raw, hash, err := NewToken()
	if err != nil {
		s.log(ctx, "sign-in code", err)
		return "", KeyTryAgain
	}
	res, err := s.d.Store.CompleteProviderSignIn(ctx, in, platformstore.ProviderParams{
		Now: now, Email: email, HandleBase: HandleBase(email), UnusableHash: UnusableHash(s.d.Hasher),
		Code: platformstore.NewToken{Purpose: PurposeSignIn, TokenHash: hash, ExpiresAt: now.Add(SignInCodeTTL)},
	})
	switch {
	case errors.Is(err, platformstore.ErrProviderUnverified):
		return "", KeyUnverified
	case errors.Is(err, platformstore.ErrProviderDisabled):
		return "", KeyDisabled
	case errors.Is(err, platformstore.ErrProviderOperator):
		return "", KeyUsePassword
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return "", KeyTryAgain
	case err != nil:
		s.log(ctx, "provider sign-in", err)
		return "", KeyTryAgain
	}
	s.revoked(ctx, res.SIDs)
	switch {
	case res.Created:
		s.audit(ctx, res.UserID, 0, "oauth_signup", map[string]string{"provider": in.Provider})
	case res.Claimed:
		s.audit(ctx, res.UserID, 0, "oauth_claim", map[string]string{"provider": in.Provider})
	case res.Linked:
		s.audit(ctx, res.UserID, 0, "oauth_link", map[string]string{"provider": in.Provider})
	}
	return raw, ""
}

// IssueSignInCode gives an active, non-operator account a fresh sign-in code (60 s) outside a
// provider completion — lane O3's invite path, after the invite was accepted: enterTeamID > 0 makes
// the exchange answer that team as enter_team_slug. Returns the raw code; ErrTokenInvalid (from the
// store) when the account may not sign in this way.
func (s *Service) IssueSignInCode(ctx context.Context, userID, enterTeamID int64) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	now := s.now()
	raw, hash, err := NewToken()
	if err != nil {
		return "", err
	}
	if err := s.d.Store.IssueSignInCode(ctx, userID, enterTeamID, platformstore.NewToken{
		Purpose: PurposeSignIn, TokenHash: hash, ExpiresAt: now.Add(SignInCodeTTL),
	}, now); err != nil {
		return "", err
	}
	return raw, nil
}

// ExchangeSignInCode swaps the one-time code of a provider sign-in for a root token (30 days from
// now, never renewed), with the account's teams; enter_team_slug names the team an invite just let
// the person into, when that membership is live. On the code-per-IP limiter; every refusal is the
// generic code answer.
func (s *Service) ExchangeSignInCode(ctx context.Context, req *auth.ExchangeSignInCodeRequest) (*auth.ExchangeSignInCodeResponse, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if !s.codeIP.Allow(middleware.GetClientIP(ctx)) {
		return nil, errCode()
	}
	code := strings.TrimSpace(req.GetCode())
	if code == "" || len(code) > 128 {
		return nil, errCode()
	}
	sid, err := NewSessionID()
	if err != nil {
		s.log(ctx, "session id", err)
		return nil, errInternal()
	}
	ctx = withSessionClient(ctx)
	now := s.now()
	res, err := s.d.Store.ExchangeSignInCode(ctx, HashToken(code), sid, now)
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return nil, errCode()
	}
	if err != nil {
		s.log(ctx, "exchange sign-in code", err)
		return nil, errInternal()
	}
	tok, err := s.mint(ctx, res.User, sid, 0, jwt.AudRoot, RootSessionTTL, false, nil, now)
	if err != nil {
		return nil, err
	}
	teams, err := s.d.Store.MemberTeams(ctx, res.User.ID)
	if err != nil {
		s.log(ctx, "member teams", err)
		return nil, errInternal()
	}
	resp := &auth.ExchangeSignInCodeResponse{Token: tok, Teams: s.teamRefs(teams)}
	if res.EnterTeamID > 0 {
		for _, t := range teams {
			if t.TeamID == res.EnterTeamID {
				resp.EnterTeamSlug = t.Slug
				break
			}
		}
	}
	s.audit(ctx, res.User.ID, 0, "signin", map[string]string{"target": "root", "by": "provider"})
	return resp, nil
}

// revoked hands sessions a transaction revoked to the edge (Hooks.SessionsRevoked), best effort:
// the edge's TTL bounds a miss.
func (s *Service) revoked(ctx context.Context, sids []string) {
	if len(sids) > 0 && s.d.Hooks.SessionsRevoked != nil {
		s.d.Hooks.SessionsRevoked(ctx, sids)
	}
}
