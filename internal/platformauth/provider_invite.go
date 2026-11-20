package platformauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// What the provider flow handlers (internal/platformauth/oauthflow, lane O3) need besides the
// completion: the invite a start names, the state rows, the invite acceptance with a provider.

// Keys the invite path adds to the provider refusals.
const (
	KeyInviteMismatch = "invite_mismatch" // the provider does not vouch for the invited email
	KeyExpired        = "expired"         // the invite is no longer live
)

// OAuthInvite reads the live invite of token for a provider start: the hash its state row keeps,
// its team (the start's origin rule) and whether the invited email already has an account (then the
// terms are not asked). platformstore.ErrTokenInvalid when there is none (or the token is
// malformed).
func (s *Service) OAuthInvite(ctx context.Context, token string) (inviteHash string, team tenant.Team, accountExists bool, err error) {
	if err := s.usable(); err != nil {
		return "", tenant.Team{}, false, err
	}
	tok := strings.TrimSpace(token)
	if tok == "" || len(tok) > 128 {
		return "", tenant.Team{}, false, platformstore.ErrTokenInvalid
	}
	hash := HashToken(tok)
	inv, err := s.d.Store.InviteByToken(ctx, hash, s.now())
	if err != nil {
		return "", tenant.Team{}, false, err
	}
	team, err = s.d.Store.Directory().ByID(ctx, inv.TeamID)
	if err != nil {
		return "", tenant.Team{}, false, fmt.Errorf("platformauth: invite team: %w", err)
	}
	return hash, team, inv.AccountExists, nil
}

// TermsVersion is the terms version a start records when the join page's box was ticked.
func (s *Service) TermsVersion() string { return s.d.TermsVersion }

// CreateOAuthState stores a started provider flow (live platformstore.OAuthStateTTL from now).
func (s *Service) CreateOAuthState(ctx context.Context, st platformstore.OAuthState) error {
	if err := s.usable(); err != nil {
		return err
	}
	return s.d.Store.CreateOAuthState(ctx, st, s.now())
}

// ConsumeOAuthState spends a provider flow once (platformstore.ErrOAuthStateInvalid when spent,
// expired or unknown).
func (s *Service) ConsumeOAuthState(ctx context.Context, stateHash string) (platformstore.OAuthState, error) {
	if err := s.usable(); err != nil {
		return platformstore.OAuthState{}, err
	}
	return s.d.Store.ConsumeOAuthState(ctx, stateHash, s.now())
}

// AcceptInviteWithProvider is the invite target of a provider callback: the store's AcceptInvite
// (the same four steps as the join form) with the provider's verified word in place of the
// password. Exactly one of code (a sign-in code whose exchange names the team as enter_team_slug)
// and key is non-empty: KeyInviteMismatch (the provider does not vouch for the invited email, or
// its identity is another account's), KeyExpired (the invite is gone), KeyUsePassword (an
// operator's account), KeyDisabled, KeyTryAgain. termsVersion is what the start recorded ("" = the
// box was not ticked); only a new account needs it. A person who already is an active member of
// the team is signed in as a provider sign-in (no team named).
func (s *Service) AcceptInviteWithProvider(ctx context.Context, inviteHash, termsVersion string, in platformstore.ProviderSignIn) (code, key string) {
	if s.usable() != nil {
		return "", KeyTryAgain
	}
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		email = ""
	}
	now := s.now()
	inv, err := s.d.Store.InviteByToken(ctx, inviteHash, now)
	if errors.Is(err, platformstore.ErrTokenInvalid) {
		return "", KeyExpired
	}
	if err != nil {
		s.log(ctx, "read invite", err)
		return "", KeyTryAgain
	}
	// Refused before anything is spent while the team's database is behind the binary (as the join
	// form).
	if team, err := s.d.Store.Directory().ByID(ctx, inv.TeamID); err == nil && s.schemaBehind(team) {
		return "", KeyTryAgain
	}
	raw, hash, err := NewToken()
	if err != nil {
		s.log(ctx, "sign-in code", err)
		return "", KeyTryAgain
	}
	first, last := inviteNames(inv.Name)
	res, err := s.d.Store.AcceptInvite(ctx, platformstore.AcceptParams{
		TokenHash:     inviteHash,
		Now:           now,
		HandleBase:    HandleBase(inv.Email),
		FirstName:     first,
		LastName:      last,
		Handoff:       platformstore.NewToken{TokenHash: hash, ExpiresAt: now.Add(SignInCodeTTL)},
		TermsVersion:  termsVersion,
		Stage:         s.stageInvite,
		Enable:        s.enableInvite,
		Provider:      &in,
		ProviderEmail: email,
		UnusableHash:  UnusableHash(s.d.Hasher),
	})
	switch {
	case errors.Is(err, platformstore.ErrInviteMismatch):
		return "", KeyInviteMismatch
	case errors.Is(err, platformstore.ErrProviderOperator):
		return "", KeyUsePassword
	case errors.Is(err, platformstore.ErrProviderDisabled):
		return "", KeyDisabled
	case errors.Is(err, platformstore.ErrAccountDeleted):
		return "", KeyAccountDeleted
	case errors.Is(err, platformstore.ErrAlreadyMember):
		return s.CompleteProviderSignIn(ctx, in)
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return "", KeyExpired
	case errors.Is(err, platformstore.ErrTeamReadOnly), errors.Is(err, platformstore.ErrTermsRequired):
		return "", KeyTryAgain
	case err != nil:
		s.log(ctx, "accept invite with a provider", err)
		return "", KeyTryAgain
	}
	s.revoked(ctx, res.SIDs)
	if res.EnablePending {
		slog.Default().WarnContext(ctx, "platformauth: invite accepted, team row not enabled by the join")
	}
	s.audit(ctx, res.UserID, res.TeamID, "invite_accept", map[string]string{
		"new_account": fmt.Sprint(res.Created), "provider": in.Provider,
	})
	return raw, ""
}
