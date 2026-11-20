package platform

import (
	"context"
	"strconv"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// maxStorageCap bounds the cap the console can set (1 PB): a typo never wraps the counter.
const maxStorageCap = int64(1) << 50

// withStorage fills used / cap of console rows (lane X11): one control-plane query for all of them.
// The cap is the one that applies (the team's own, else TEAM_STORAGE_CAP_GB); 0 = no cap.
func (s *Server) withStorage(ctx context.Context, ids []int64, rows ...*pb_admin.PlatformTeam) error {
	st, err := s.st.StorageFor(ctx, ids)
	if err != nil {
		return storeErr(ctx, "team storage", err)
	}
	def := s.cfg.StorageCapDefault()
	for i, row := range rows {
		if row == nil || i >= len(ids) {
			continue
		}
		ts := st[ids[i]]
		row.StorageUsedBytes = ts.Used
		row.StorageCapBytes = ts.EffectiveCap(def)
	}
	return nil
}

// SetTeamStorageCap sets a team's storage cap: cap_bytes > 0, or no_cap. Team #1 too (it starts
// with no cap). Uploads over the cap are refused; reads never are.
func (s *Server) SetTeamStorageCap(ctx context.Context, req *pb_admin.SetTeamStorageCapRequest) (*pb_admin.SetTeamStorageCapResponse, error) {
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	var capBytes int64
	switch {
	case req.GetNoCap() && req.GetCapBytes() == 0:
		capBytes = 0
	case !req.GetNoCap() && req.GetCapBytes() > 0 && req.GetCapBytes() <= maxStorageCap:
		capBytes = req.GetCapBytes()
	default:
		return nil, status.Error(codes.InvalidArgument, "storage_cap")
	}
	t, err := s.team(ctx, req.GetTeamSlug())
	if err != nil {
		return nil, err
	}
	if err := s.st.SetStorageCap(ctx, t.ID, capBytes); err != nil {
		return nil, storeErr(ctx, "set storage cap", err)
	}
	s.audit(ctx, c.UserID, t.ID, "team_storage_cap", map[string]string{"cap_bytes": strconv.FormatInt(capBytes, 10)})
	team, err := s.refreshed(ctx, t.Slug)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetTeamStorageCapResponse{Team: team}, nil
}
