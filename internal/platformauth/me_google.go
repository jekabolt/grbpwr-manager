package platformauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Google connect and disconnect from the profile (P0 §3.7). Connect is three steps: StartGoogleLink
// (bearer) issues a single-use ticket; the page form-POSTs it to /api/auth/oauth/google/start with
// target=link (ConsumeLinkTicket binds the flow to this browser); the callback calls
// CompleteGoogleLink and sends the browser back to the profile with ?google=<key>.

// MaxGoogleLinksPerHr bounds StartGoogleLink per account.
const MaxGoogleLinksPerHr = 20

// Keys of a Google connect's return to the profile (?google=), besides KeyUnverified, KeyExpired and
// KeyTryAgain.
const (
	KeyLinked        = "linked"         // connected (or it already was this Google account)
	KeyTaken         = "taken"          // this Google account is connected to another account
	KeyAlreadyLinked = "already_linked" // the account got another Google account meanwhile
)

// Google answers. Built per call: the gate counts package-level values.
func errAlreadyLinked() error { return status.Error(codes.AlreadyExists, "already_linked") }
func errOnlySignIn() error    { return status.Error(codes.FailedPrecondition, "only_sign_in") }
func errGoogleOff() error     { return status.Error(codes.FailedPrecondition, "google_off") }

// StartGoogleLink issues the ticket the page form-POSTs to start a Google connect: single use, live
// platformstore.LinkTicketTTL, stored hashed, bound to the account and to the team host the call came
// from (the root host for a root token). already_linked when the account has Google.
func (s *Service) StartGoogleLink(ctx context.Context, token string, _ *auth.StartGoogleLinkRequest) (*auth.StartGoogleLinkResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.options.Google() {
		return nil, errGoogleOff()
	}
	if !s.googleLink.Allow(c.key()) {
		return nil, errBusy()
	}
	slug := ""
	if c.TeamID > 0 {
		team, err := s.d.Store.Directory().ByID(ctx, c.TeamID)
		if err != nil {
			s.log(ctx, "link team", err)
			return nil, errInternal()
		}
		slug = team.Slug
	}
	raw, hash, err := NewToken()
	if err != nil {
		s.log(ctx, "link ticket", err)
		return nil, errInternal()
	}
	err = s.d.Store.CreateLinkTicket(ctx, hash, c.UserID, slug, s.now())
	switch {
	case errors.Is(err, platformstore.ErrAlreadyLinked):
		return nil, errAlreadyLinked()
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case err != nil:
		s.log(ctx, "link ticket", err)
		return nil, errInternal()
	}
	return &auth.StartGoogleLinkResponse{Ticket: raw}, nil
}

// UnlinkMyGoogle disconnects Google; sessions stay. only_sign_in when Google is the account's only
// way in (no password of its own, no other provider). Nothing connected is not an error.
func (s *Service) UnlinkMyGoogle(ctx context.Context, token string, _ *auth.UnlinkMyGoogleRequest) (*auth.UnlinkMyGoogleResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if !s.profileWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	n, err := s.d.Store.UnlinkGoogle(ctx, c.UserID)
	switch {
	case errors.Is(err, platformstore.ErrOnlySignIn):
		return nil, errOnlySignIn()
	case errors.Is(err, platformstore.ErrUserNotFound):
		return nil, errAuth()
	case err != nil:
		s.log(ctx, "unlink google", err)
		return nil, errInternal()
	}
	if n > 0 {
		s.audit(ctx, c.UserID, c.TeamID, "google_unlink", nil)
	}
	return &auth.UnlinkMyGoogleResponse{}, nil
}

// ConsumeLinkTicket spends a Google-connect ticket for a provider start: the account it connects and
// the team host it returns to (the zero Team: the root host). platformstore.ErrTokenInvalid when the
// ticket is malformed, unknown, spent or expired, or its team is gone.
func (s *Service) ConsumeLinkTicket(ctx context.Context, ticket string) (int64, tenant.Team, error) {
	if err := s.usable(); err != nil {
		return 0, tenant.Team{}, err
	}
	tok := strings.TrimSpace(ticket)
	if tok == "" || len(tok) > 128 {
		return 0, tenant.Team{}, platformstore.ErrTokenInvalid
	}
	uid, slug, err := s.d.Store.ConsumeLinkTicket(ctx, HashToken(tok), s.now())
	if err != nil {
		return 0, tenant.Team{}, err
	}
	if slug == "" {
		return uid, tenant.Team{}, nil
	}
	team, err := s.d.Store.Directory().BySlug(ctx, slug)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return 0, tenant.Team{}, platformstore.ErrTokenInvalid
		}
		return 0, tenant.Team{}, fmt.Errorf("platformauth: link team: %w", err)
	}
	return uid, team, nil
}

// CompleteGoogleLink is the link target of a provider callback: it connects the verified Google
// identity to the account the flow named (platformstore.CompleteGoogleLink's rules; startedAt is when
// the flow began) and returns the ?google= key: KeyLinked, KeyTaken, KeyAlreadyLinked, KeyUnverified,
// KeyExpired (the account is gone or its password changed since the start) or KeyTryAgain.
func (s *Service) CompleteGoogleLink(ctx context.Context, userID int64, startedAt time.Time, in platformstore.ProviderSignIn) string {
	if s.usable() != nil {
		return KeyTryAgain
	}
	// Store only the exact address that passed the account-identity rule ("" fails it).
	email, err := NormalizeEmail(in.Email)
	if err != nil {
		email = ""
	}
	in.Email = email
	err = s.d.Store.CompleteGoogleLink(ctx, userID, in, startedAt, s.now())
	switch {
	case err == nil:
		s.audit(ctx, userID, 0, "google_link", nil)
		return KeyLinked
	case errors.Is(err, platformstore.ErrLinkTaken):
		return KeyTaken
	case errors.Is(err, platformstore.ErrAlreadyLinked):
		return KeyAlreadyLinked
	case errors.Is(err, platformstore.ErrProviderUnverified):
		return KeyUnverified
	case errors.Is(err, platformstore.ErrTokenInvalid):
		return KeyExpired
	default:
		s.log(ctx, "google link", err)
		return KeyTryAgain
	}
}
