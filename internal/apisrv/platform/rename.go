package platform

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxTeamName is the longest team name, in runes (team.name VARCHAR(120)).
const maxTeamName = 120

// RenameTeam changes a team's address and / or name. A new address must be valid, not reserved and
// never used before (live or retired); the old one redirects for 90 days and is never reused.
// Team #1 keeps its address; its name may change.
func (s *Server) RenameTeam(ctx context.Context, req *pb_admin.RenameTeamRequest) (*pb_admin.RenameTeamResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	newSlug := strings.ToLower(strings.TrimSpace(req.GetNewSlug()))
	newName := strings.TrimSpace(req.GetNewName())
	if newSlug == t.Slug {
		newSlug = ""
	}
	if newName == t.Name {
		newName = ""
	}
	if newSlug == "" && newName == "" {
		if strings.TrimSpace(req.GetNewSlug()) == "" && strings.TrimSpace(req.GetNewName()) == "" {
			return nil, status.Error(codes.InvalidArgument, "nothing_to_change")
		}
		team, err := s.refreshed(ctx, t.Slug)
		if err != nil {
			return nil, err
		}
		return &pb_admin.RenameTeamResponse{Team: team}, nil
	}
	if newSlug != "" {
		if t.ID == tenant.TeamOneID {
			return nil, status.Error(codes.FailedPrecondition, "team_one")
		}
		if err := tenant.ValidSlug(newSlug); err != nil {
			return nil, status.Error(codes.InvalidArgument, "bad_slug")
		}
	}
	if newName != "" && utf8.RuneCountInString(newName) > maxTeamName {
		return nil, status.Error(codes.InvalidArgument, "bad_name")
	}
	if t.Status == string(tenant.StatusReview) {
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	if err := s.st.RenameTeam(ctx, t.ID, newSlug, newName, s.nowUTC()); err != nil {
		return nil, storeErr(ctx, "rename team", err)
	}
	s.dropTeam(t.ID)
	detail := map[string]string{"slug": t.Slug}
	if newSlug != "" {
		detail["new_slug"] = newSlug
	}
	if newName != "" {
		detail["new_name"] = newName
	}
	s.audit(ctx, c.UserID, t.ID, "team_rename", detail)
	slug := t.Slug
	if newSlug != "" {
		slug = newSlug
	}
	team, err := s.refreshed(ctx, slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.RenameTeamResponse{Team: team}, nil
}
