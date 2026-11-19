package platform

import (
	"context"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListFeatures is the switchable feature catalog, in its order.
func (s *Server) ListFeatures(ctx context.Context, _ *pb_admin.ListFeaturesRequest) (*pb_admin.ListFeaturesResponse, error) {
	if _, err := caller(ctx); err != nil {
		return nil, err
	}
	cat := feature.Catalog()
	out := &pb_admin.ListFeaturesResponse{Features: make([]*pb_admin.FeatureInfo, 0, len(cat))}
	for _, info := range cat {
		out.Features = append(out.Features, &pb_admin.FeatureInfo{
			Key: string(info.Key), Title: info.Title, Group: info.Group,
			DefaultOn: info.DefaultOn, TeamOneOnly: info.TeamOneOnly,
		})
	}
	return out, nil
}

// SetTeamFeature switches one feature for one team. TeamOneOnly keys are not switchable for any
// team; a team waiting for approval has nothing to switch.
func (s *Server) SetTeamFeature(ctx context.Context, req *pb_admin.SetTeamFeatureRequest) (*pb_admin.SetTeamFeatureResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	key := feature.Key(strings.TrimSpace(req.GetFeature()))
	if !feature.Valid(key) {
		return nil, status.Error(codes.InvalidArgument, "unknown_feature")
	}
	for _, info := range feature.Catalog() {
		if info.Key == key && info.TeamOneOnly {
			return nil, status.Error(codes.FailedPrecondition, "team_one_only")
		}
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.Status == string(tenant.StatusReview) {
		return nil, status.Error(codes.FailedPrecondition, "wrong_status")
	}
	if err := s.st.SetFeature(ctx, t.ID, string(key), req.GetEnabled(), c.UserID); err != nil {
		return nil, storeErr(ctx, "set feature", err)
	}
	s.dropTeam(t.ID)
	s.audit(ctx, c.UserID, t.ID, "team_feature", map[string]string{
		"feature": string(key), "enabled": strconv.FormatBool(req.GetEnabled()),
	})
	team, err := s.refreshed(ctx, t.Slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetTeamFeatureResponse{Team: team}, nil
}
