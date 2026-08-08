package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// gateFixture wires the reads the gate makes around a card that is UNREADY in exactly one way: its
// size range is empty, so card_size_range is a BLOCKER and nothing else is.
func gateFixture(t *testing.T, blocking bool) *Server {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	ws := mocks.NewMockWorkshop(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().Workshop().Return(ws).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	settings := &entity.WorkshopSettings{}
	if blocking {
		settings.RunReadinessBlocking = sql.NullBool{Bool: true, Valid: true}
	}
	ws.EXPECT().GetSettings(mock.Anything).Return(settings, nil).Maybe()
	ms.EXPECT().NarrowestMeasuredLotWidths(mock.Anything, mock.Anything).
		Return(map[int]decimal.NullDecimal{}, nil).Maybe()
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(&entity.TechCard{
		Id: 7,
		TechCardInsert: entity.TechCardInsert{
			// No sizes and no pieces: two BLOCKERs, so the refusal has to name more than one.
			Purpose: entity.TechCardPurposeSellable,
		},
	}, nil).Maybe()
	tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, 7).
		Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()
	// Report-only lets the create proceed, and the planned-cost snapshot then prices the live card.
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil).Maybe()
	// Nothing else is expected: in blocking mode the run must never reach the store, which is what
	// the absence of a CreateProductionRun expectation asserts.
	if !blocking {
		pr.EXPECT().CreateProductionRun(mock.Anything, mock.Anything).Return(42, nil).Maybe()
		// Ф5б.4 резервирует ткань сразу после рождения прогона; здесь эта ветка сознательно уводится
		// в свой лучший-из-возможных путь — тест про ГЕЙТ, а не про материальный план.
		expectRunReservationReconcileStandsDown(t, pr, 42)
	}
	return &Server{repo: repo}
}

func gateRequest() *pb_admin.CreateProductionRunRequest {
	return &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7,
			Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines:      []*pb_common.ProductionRunLine{{ProductId: 5, SizeId: 1, PlannedQty: 10}},
		},
	}
}

// TestRunReadinessCreateGateReportOnlyCreatesAnyway is acceptance probe 2: the SAME payload that
// blocking mode refuses is created in report-only mode. This is the whole reason the setting's
// unconfigured state has a defined behaviour — on the day Ф6 ships not one card carries a norm with
// recorded conditions, and «no verdict ⇒ refuse» would have refused everyone.
func TestRunReadinessCreateGateReportOnlyCreatesAnyway(t *testing.T) {
	s := gateFixture(t, false)
	resp, err := s.CreateProductionRun(context.Background(), gateRequest())
	require.NoError(t, err, "report-only mode logs and proceeds")
	require.Equal(t, int32(42), resp.Id)
}

// TestRunReadinessCreateGateBlockingRefusesWithEveryReason is acceptance probes 1 and 3: the run is
// refused THROUGH THE API (not only in the modal), with FailedPrecondition, and the refusal names
// EVERY blocker by its stable key rather than the first one — an operator fixing one reason per
// round trip is the failure the many-violation form exists to prevent.
func TestRunReadinessCreateGateBlockingRefusesWithEveryReason(t *testing.T) {
	s := gateFixture(t, true)
	_, err := s.CreateProductionRun(context.Background(), gateRequest())
	require.Error(t, err)
	require.Equal(t, codes.FailedPrecondition, status.Code(err),
		"the payload is well-formed; it is the STATE that refuses it")

	var br *errdetails.BadRequest
	for _, d := range status.Convert(err).Details() {
		if v, ok := d.(*errdetails.BadRequest); ok {
			br = v
		}
	}
	require.NotNil(t, br, "the refusal must carry a BadRequest the client can bind to inputs")
	keys := map[string]bool{}
	for _, v := range br.GetFieldViolations() {
		require.NotEmpty(t, v.GetField(), "every violation addresses an input")
		// The description LEADS with the stable key, so a client may branch without parsing prose.
		require.NotEmpty(t, v.GetDescription())
		for k := range entity.RunReadinessKeyGroups {
			if len(v.GetDescription()) >= len(k) && v.GetDescription()[:len(k)] == k {
				keys[k] = true
			}
		}
	}
	require.Contains(t, keys, entity.RunReadinessKeyCardSizeRange)
	require.Contains(t, keys, entity.RunReadinessKeyCardPieces,
		"a refusal names EVERY blocker, not the first one")
}

// TestCheckProductionRunReadinessIsReadOnlyAndAnswersInBothModes: the RPC reports the mode and what
// create WOULD do, and it never writes. would_block is sent explicitly rather than left to the
// client's «!ready && blocking_enabled» because that sentence is what the modal prints.
func TestCheckProductionRunReadinessReportsTheMode(t *testing.T) {
	for _, blocking := range []bool{false, true} {
		s := gateFixture(t, blocking)
		resp, err := s.CheckProductionRunReadiness(context.Background(), &pb_admin.CheckProductionRunReadinessRequest{
			TechCardId:  7,
			ColorwayIds: []int32{5},
			Cells:       []*pb_admin.ProductionRunReadinessCell{{ColorwayId: 5, SizeId: 1, PlannedQty: 10}},
		})
		require.NoError(t, err)
		require.False(t, resp.GetReady(), "the fixture card has no size range")
		require.Equal(t, blocking, resp.GetBlockingEnabled())
		require.Equal(t, blocking, resp.GetWouldBlock(),
			"would_block is the answer to «what will create do», not a client-side derivation")
		require.NotEmpty(t, resp.GetCard(), "the response lists every check, including the passing ones")
	}
}

// TestCheckProductionRunReadinessRequiresACard: an id of zero is a client bug, not a verdict.
func TestCheckProductionRunReadinessRequiresACard(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.CheckProductionRunReadiness(context.Background(), &pb_admin.CheckProductionRunReadinessRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
