package platformauth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Account lifecycle RPCs of the calling person (P0 design §3.5, §4). Contract stubs until lanes S4a / S4b.

// FinishTeamEntry ends the first-entry screen of the calling team.
func (s *Service) FinishTeamEntry(ctx context.Context, token string, req *auth.FinishTeamEntryRequest) (*auth.FinishTeamEntryResponse, error) {
	return nil, errNotYet
}

// DeleteMyAccount deletes the calling person's account.
func (s *Service) DeleteMyAccount(ctx context.Context, token string, req *auth.DeleteMyAccountRequest) (*auth.DeleteMyAccountResponse, error) {
	return nil, errNotYet
}
