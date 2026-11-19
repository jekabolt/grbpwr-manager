package platformauth

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
)

// Device (session chain) RPCs of the calling person (P0 design §3.3). Contract stubs until lane S3.

// ListMySessions lists the live devices.
func (s *Service) ListMySessions(ctx context.Context, token string, req *auth.ListMySessionsRequest) (*auth.ListMySessionsResponse, error) {
	return nil, errNotYet
}

// RevokeMySession signs one device out.
func (s *Service) RevokeMySession(ctx context.Context, token string, req *auth.RevokeMySessionRequest) (*auth.RevokeMySessionResponse, error) {
	return nil, errNotYet
}

// RevokeMyOtherSessions signs out every other device.
func (s *Service) RevokeMyOtherSessions(ctx context.Context, token string, req *auth.RevokeMyOtherSessionsRequest) (*auth.RevokeMyOtherSessionsResponse, error) {
	return nil, errNotYet
}
