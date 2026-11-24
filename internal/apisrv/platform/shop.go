package platform

import (
	"context"
	"slices"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Shops in the console (X18): each team row says whether its shop is off / open / closed /
// checkout_off and its main address; the operator can close a shop (and reopen it).

// SetShopInvalidator wires the edge's shop cache drop (tenantbind.Binder.InvalidateShop + CORS).
func (s *Server) SetShopInvalidator(fn func(teamID int64)) { s.dropShop = fn }

func (s *Server) storefrontID() int64 {
	if s.cfg.StorefrontTeamID > 0 {
		return s.cfg.StorefrontTeamID
	}
	if s.cfg.NoShop() {
		return 0
	}
	return tenant.TeamOneID
}

// withShop fills shop / shop_host of console rows: one control-plane query for all of them.
func (s *Server) withShop(ctx context.Context, ids []int64, rows ...*pb_admin.PlatformTeam) error {
	sums, err := s.st.ShopSummaries(ctx, ids)
	if err != nil {
		return storeErr(ctx, "shop summaries", err)
	}
	sf := s.storefrontID()
	for i, row := range rows {
		if row == nil || i >= len(ids) {
			continue
		}
		if ids[i] == sf {
			row.Shop = "open"
			continue
		}
		sum := sums[ids[i]]
		row.ShopHost = sum.PrimaryHost
		features := slices.Contains(row.Features, string(feature.Products)) && slices.Contains(row.Features, string(feature.Orders))
		switch {
		case !sum.Enabled:
			row.Shop = "off"
		case sum.Closed || !features || row.Status != statusActive:
			row.Shop = "closed"
		case !sum.Payable:
			row.Shop = "checkout_off"
		default:
			row.Shop = "open"
		}
	}
	return nil
}

// SetTeamShop closes a team's shop by the operator (closed=true) or reopens it; the team's own
// switch is not touched. The storefront team's shop (today's storefront) is not closed here.
func (s *Server) SetTeamShop(ctx context.Context, req *pb_admin.SetTeamShopRequest) (*pb_admin.SetTeamShopResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if t.ID == s.storefrontID() {
		return nil, status.Error(codes.FailedPrecondition, "shop_legacy")
	}
	if err := s.st.SetShopClosed(ctx, t.ID, req.GetClosed()); err != nil {
		return nil, storeErr(ctx, "set shop closed", err)
	}
	if s.dropShop != nil {
		s.dropShop(t.ID)
	}
	action := "team_shop_reopened"
	if req.GetClosed() {
		action = "team_shop_closed"
	}
	s.audit(ctx, c.UserID, t.ID, action, map[string]string{})
	team, err := s.refreshed(ctx, t.Slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetTeamShopResponse{Team: team}, nil
}
