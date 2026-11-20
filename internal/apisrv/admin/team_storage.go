package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetTeamStorage: the calling team's storage in use and its cap (lane X11). Teams mode off (no
// control plane) answers 0 / 0 — nothing is counted, nothing is capped.
func (s *Server) GetTeamStorage(ctx context.Context, _ *pb_admin.GetTeamStorageRequest) (*pb_admin.GetTeamStorageResponse, error) {
	out := &pb_admin.GetTeamStorageResponse{}
	st := s.platform.store
	if st == nil {
		return out, nil
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID <= 0 {
		return out, nil
	}
	rows, err := st.StorageFor(ctx, []int64{t.ID})
	if err != nil {
		return nil, status.Error(codes.Internal, "storage")
	}
	ts := rows[t.ID]
	out.UsedBytes = ts.Used
	out.CapBytes = ts.EffectiveCap(s.platform.cfg.StorageCapDefault())
	out.Full = out.CapBytes > 0 && out.UsedBytes >= out.CapBytes
	return out, nil
}
