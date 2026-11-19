package platformauth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Account lifecycle RPCs of the calling person (P0 design §3.5, §4). Contract stubs until lanes S4a / S4b.

// FinishTeamEntry ends the first-entry screen of the calling team.
func (s *Service) FinishTeamEntry(ctx context.Context, token string, req *auth.FinishTeamEntryRequest) (*auth.FinishTeamEntryResponse, error) {
	return nil, errNotYet()
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
