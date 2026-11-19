package platform

import (
	"context"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// SetTeamStatus is the console's pause: active ↔ suspended (read-only). Team #1 is never paused;
// any other status is refused.
func (s *Server) SetTeamStatus(ctx context.Context, req *pb_admin.SetTeamStatusRequest) (*pb_admin.SetTeamStatusResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.ID == tenant.TeamOneID {
		return nil, status.Error(codes.FailedPrecondition, "team_one")
	}
	if err := s.st.SetTeamReadOnly(ctx, t.ID, req.GetReadOnly()); err != nil {
		return nil, storeErr(ctx, "set team status", err)
	}
	s.dropTeam(t.ID)
	s.audit(ctx, c.UserID, t.ID, "team_status", map[string]string{"read_only": strconv.FormatBool(req.GetReadOnly())})
	team, err := s.refreshed(ctx, t.Slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetTeamStatusResponse{Team: team}, nil
}
