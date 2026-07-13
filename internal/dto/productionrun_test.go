package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func TestConvertPbProductionRunInsertToEntity(t *testing.T) {
	rq := int32(58)
	dq := int32(2)
	e, err := ConvertPbProductionRunInsertToEntity(&pb_common.ProductionRunInsert{
		TechCardId: 7,
		ReleaseId:  3,
		Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS,
		Notes:      "batch A",
		Sizes: []*pb_common.ProductionRunSize{
			{SizeId: 1, PlannedQty: 60, ReceivedQty: &rq, DefectQty: &dq},
			{SizeId: 2, PlannedQty: 40}, // received/defect unset
		},
	})
	require.NoError(t, err)
	require.Equal(t, 7, e.TechCardId)
	require.True(t, e.ReleaseId.Valid)
	require.EqualValues(t, 3, e.ReleaseId.Int64)
	require.Equal(t, entity.ProductionRunInProgress, e.Status)
	require.False(t, e.PlannedUnitCost.Valid, "plan cost is never taken from the client")
	require.Len(t, e.Sizes, 2)
	require.True(t, e.Sizes[0].ReceivedQty.Valid)
	require.EqualValues(t, 58, e.Sizes[0].ReceivedQty.Int64)
	require.False(t, e.Sizes[1].ReceivedQty.Valid, "unset received stays NULL")

	// round-trip back to pb preserves presence
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: *e}
	pb := ConvertEntityProductionRunToPb(run)
	require.Equal(t, int32(9), pb.Id)
	require.Equal(t, pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_IN_PROGRESS, pb.Run.Status)
	require.EqualValues(t, 3, pb.Run.ReleaseId)
	require.Len(t, pb.Run.Sizes, 2)
	require.NotNil(t, pb.Run.Sizes[0].ReceivedQty)
	require.EqualValues(t, 58, *pb.Run.Sizes[0].ReceivedQty)
	require.Nil(t, pb.Run.Sizes[1].ReceivedQty, "unset received stays absent")
}

func TestConvertPbProductionRunInsertValidation(t *testing.T) {
	cases := map[string]*pb_common.ProductionRunInsert{
		"missing tech_card_id": {Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED},
		"unknown status":       {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_UNKNOWN},
		"duplicate size": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Sizes: []*pb_common.ProductionRunSize{{SizeId: 1, PlannedQty: 1}, {SizeId: 1, PlannedQty: 2}}},
		"zero size_id": {TechCardId: 1, Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Sizes: []*pb_common.ProductionRunSize{{SizeId: 0, PlannedQty: 1}}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ConvertPbProductionRunInsertToEntity(in)
			require.Error(t, err)
		})
	}
}

func TestNormalizeProductionRunStatusFilter(t *testing.T) {
	st, err := NormalizeProductionRunStatusFilter(" Received ")
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunReceived, st)

	st, err = NormalizeProductionRunStatusFilter("")
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunStatus(""), st)

	_, err = NormalizeProductionRunStatusFilter("bogus")
	require.Error(t, err)
}
