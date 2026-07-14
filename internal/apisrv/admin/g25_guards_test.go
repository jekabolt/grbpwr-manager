package admin

import (
	"context"
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestCreateProductionRunStatusGuard (g25-01): a run is born planned/in_progress — creating it
// straight into received/closed/cancelled is refused BEFORE any repo access (a run created as
// received would fake booked stock and be immediately immutable for both update and delete).
func TestCreateProductionRunStatusGuard(t *testing.T) {
	s := &Server{} // the guard fires before the repo is touched
	for _, st := range []pb_common.ProductionRunStatus{
		pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_RECEIVED,
		pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CLOSED,
		pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_CANCELLED,
	} {
		_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
			Run: &pb_common.ProductionRunInsert{TechCardId: 1, Status: st},
		})
		require.Error(t, err, st.String())
		require.Equal(t, codes.InvalidArgument, status.Code(err), st.String())
	}
}

// TestListMaterialMovementsDateGuard (g25-15): malformed occurred_from/occurred_to bounds are a
// clean InvalidArgument before the store is touched — not a SQL DATE() error surfacing as a 500.
func TestListMaterialMovementsDateGuard(t *testing.T) {
	s := &Server{}
	for name, req := range map[string]*pb_admin.ListMaterialMovementsRequest{
		"garbage from": {OccurredFrom: "вчера"},
		"bad month":    {OccurredTo: "2026-13-99"},
		"not a date":   {OccurredFrom: "2026/01/01"},
	} {
		_, err := s.ListMaterialMovements(context.Background(), req)
		require.Error(t, err, name)
		require.Equal(t, codes.InvalidArgument, status.Code(err), name)
	}
}
