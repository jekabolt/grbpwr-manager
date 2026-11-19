package platformauth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Account lifecycle RPCs of the calling person (P0 design §3.5, §4). Contract stubs until lanes S4a / S4b.

// FinishTeamEntry ends the first-entry screen of the calling team (P0 §4): finished and skipped are
// the same, and it sticks. A team token only (a root token has no team to enter); idempotent.
func (s *Service) FinishTeamEntry(ctx context.Context, token string, _ *auth.FinishTeamEntryRequest) (*auth.FinishTeamEntryResponse, error) {
	c, err := s.me(ctx, token)
	if err != nil {
		return nil, err
	}
	if c.TeamID == 0 {
		return nil, errNoAccess()
	}
	if !s.profileWrite.Allow(c.key()) {
		return nil, errBusy()
	}
	if err := s.d.Store.FinishTeamEntry(ctx, c.TeamID, c.UserID); err != nil {
		s.log(ctx, "finish team entry", err)
		return nil, errInternal()
	}
	s.audit(ctx, c.UserID, c.TeamID, "team_entry_finish", nil)
	return &auth.FinishTeamEntryResponse{}, nil
}

// DeleteMyAccount deletes the calling person's account; RestoreAccount brings it back for 30 days.
func (s *Service) DeleteMyAccount(ctx context.Context, token string, req *auth.DeleteMyAccountRequest) (*auth.DeleteMyAccountResponse, error) {
	return nil, errNotYet()
}

// RequestAccountRestore emails a restore letter for an account deleted less than 30 days ago (public).
func (s *Service) RequestAccountRestore(ctx context.Context, req *auth.RequestAccountRestoreRequest) (*auth.RequestAccountRestoreResponse, error) {
	return nil, errNotYet()
}

// RestoreAccount brings a deleted account back by the restore letter's token or code (public).
func (s *Service) RestoreAccount(ctx context.Context, req *auth.RestoreAccountRequest) (*auth.RestoreAccountResponse, error) {
	return nil, errNotYet()
}
