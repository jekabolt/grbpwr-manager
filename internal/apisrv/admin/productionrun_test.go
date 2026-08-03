package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateProductionRun freezes the planned unit cost from a linked tech_card_release (task 11).
func TestCreateProductionRunSnapshotsPlanFromRelease(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().ProductionRuns().Return(pr)

	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			Id: 5, TechCardId: 7,
			UnitCost: decimal.NullDecimal{Decimal: decimal.RequireFromString("33.00"), Valid: true},
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
	}, nil)

	var captured *entity.ProductionRunInsert
	pr.EXPECT().CreateProductionRun(mock.Anything, mock.MatchedBy(func(r *entity.ProductionRunInsert) bool {
		captured = r
		return true
	})).Return(11, nil)

	s := &Server{repo: repo}
	resp, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7,
			ReleaseId:  5,
			Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
			Lines:      []*pb_common.ProductionRunLine{{SizeId: 1, PlannedQty: 50}},
		},
	})
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.Id)
	require.NotNil(t, captured)
	require.True(t, captured.PlannedUnitCost.Decimal.Equal(decimal.RequireFromString("33.00")), "plan cost snapshotted from release")
	require.Equal(t, "EUR", captured.PlannedCurrency.String)
}

func TestCreateProductionRunReleaseNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(nil, sql.ErrNoRows)

	s := &Server{repo: repo}
	_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 7, ReleaseId: 5,
			Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

func TestCreateProductionRunValidation(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
		Run: &pb_common.ProductionRunInsert{Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED},
	})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "missing tech_card_id")
}

func TestUpdateProductionRunCostBlindPreservationFailureIsFatal(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	pr.EXPECT().UpdateProductionRunPreservingCosts(mock.Anything, 7, mock.AnythingOfType("*entity.ProductionRunInsert"), 3).
		Return(errors.New("preservation read failed"))
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false)

	_, err := (&Server{repo: repo}).UpdateProductionRun(context.Background(), &pb_admin.UpdateProductionRunRequest{
		Id:                  7,
		ExpectedLockVersion: 3,
		Run: &pb_common.ProductionRunInsert{
			TechCardId: 9,
			Status:     pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
		},
	})
	require.Equal(t, codes.Internal, status.Code(err))
}

// receiveRun builds a mock repo whose run/card are fixed, wiring the ProductionRuns + TechCards
// mocks. run and card are returned so tests can assert on captured store args.
func receiveMocks(t *testing.T, run *entity.ProductionRun, card *entity.TechCard) (*mocks.MockRepository, *mocks.MockProductionRuns, *mocks.MockTechCards) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().TechCards().Return(tc).Maybe()
	pr.EXPECT().GetProductionRun(mock.Anything, run.Id).Return(run, nil).Maybe()
	if card != nil {
		tc.EXPECT().GetTechCardById(mock.Anything, run.TechCardId).Return(card, nil).Maybe()
	}
	return repo, pr, tc
}

// The legacy shim synthesizes the receipt command from the run's STORED counts: only counted lines
// travel, keyed by line_key, with the card's product/size linkage passed into the transaction.
func TestReceiveProductionRunShimSynthesizesReceiptCommand(t *testing.T) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 60, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}, DefectQty: sql.NullInt64{Int64: 2, Valid: true}},
			{LineKey: "K2AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 2, PlannedQty: 40, ReceivedQty: sql.NullInt64{Int64: 40, Valid: true}},
			{LineKey: "K3AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 66, Valid: true}, SizeId: 1, PlannedQty: 20},
		},
	}}
	card := &entity.TechCard{Id: 7}
	// PR6 R1: a style's linked products are its live colourways (product.style_id); LinkedProductIDs()
	// reads the enriched colourways' ProductId.
	card.Colorways = []entity.TechCardColorway{
		{Id: 55, ProductId: sql.NullInt32{Int32: 55, Valid: true}},
		{Id: 66, ProductId: sql.NullInt32{Int32: 66, Valid: true}},
	}
	card.SizeIds = []int{1, 2}

	repo, pr, _ := receiveMocks(t, run, card)
	var got entity.PostProductionRunReceiptParams
	pr.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, p entity.PostProductionRunReceiptParams) (*entity.PostProductionRunReceiptResult, error) {
			got = p
			return &entity.PostProductionRunReceiptResult{ReceiptID: 9, CostPriceUpdated: p.UpdateCostPrice}, nil
		})

	s := &Server{repo: repo}
	resp, err := s.ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4, UpdateCostPrice: true})
	require.NoError(t, err)
	require.True(t, resp.CostPriceUpdated)
	require.Equal(t, 4, got.RunID)
	require.True(t, got.UpdateCostPrice)
	require.False(t, got.Aux)
	// Only the counted lines travel, by line_key; K3 (no counts) carries no receipt fact.
	require.Equal(t, []entity.ProductionRunReceiptLineInput{
		{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 58, DefectQty: 2},
		{LineKey: "K2AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 40},
	}, got.Lines)
	require.Equal(t, map[int]bool{55: true, 66: true}, got.ValidProducts)
	require.Equal(t, map[int]bool{1: true, 2: true}, got.ValidSizes)
	require.True(t, entity.IsValidProductionRunLineKey(got.IdempotencyKey), "shim mints a shaped key")
	require.NotEmpty(t, got.RequestHash)
	require.Equal(t, 0, got.ExpectedLockVersion, "legacy path opts out of the optimistic lock")
}

// PostProductionRunReceipt maps the command's outcomes onto the API: replay is a success with
// replayed=true; a reused key with a different payload is AlreadyExists; the store's precondition
// errors keep their codes.
func TestPostProductionRunReceipt(t *testing.T) {
	key := "01AAAAAAAAAAAAAAAAAAAAAAAA"
	mkRun := func() *entity.ProductionRun {
		return &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
			TechCardId: 7, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{
				{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10},
			},
		}}
	}
	card := &entity.TechCard{Id: 7}
	card.Colorways = []entity.TechCardColorway{{Id: 55, ProductId: sql.NullInt32{Int32: 55, Valid: true}}}
	card.SizeIds = []int{1}
	lines := []*pb_admin.PostProductionRunReceiptLineInput{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 8, DefectQty: 2}}

	// happy path: receipt id + cost flag + lock version pass through; the run is re-read for the echo.
	run := mkRun()
	repo, pr, _ := receiveMocks(t, run, card)
	var got entity.PostProductionRunReceiptParams
	pr.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, p entity.PostProductionRunReceiptParams) (*entity.PostProductionRunReceiptResult, error) {
			got = p
			return &entity.PostProductionRunReceiptResult{ReceiptID: 12, CostPriceUpdated: false}, nil
		})
	resp, err := (&Server{repo: repo}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, Lines: lines, IdempotencyKey: key, ExpectedLockVersion: 3, Note: "final",
	})
	require.NoError(t, err)
	require.Equal(t, int32(12), resp.ReceiptId)
	require.False(t, resp.Replayed)
	require.NotNil(t, resp.Run, "post-command run echoed")
	require.Equal(t, 3, got.ExpectedLockVersion)
	require.Equal(t, "final", got.Note)
	require.Equal(t, key, got.IdempotencyKey)
	require.Equal(t, dto.HashProductionRunReceiptPayload(4, got.Lines, "final", false, true), got.RequestHash)

	// replay: the stored result comes back marked replayed.
	repo2, pr2, _ := receiveMocks(t, mkRun(), card)
	pr2.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		Return(&entity.PostProductionRunReceiptResult{ReceiptID: 12, CostPriceUpdated: true, Replayed: true}, nil)
	resp2, err := (&Server{repo: repo2}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, Lines: lines, IdempotencyKey: key,
	})
	require.NoError(t, err)
	require.True(t, resp2.Replayed)
	require.True(t, resp2.CostPriceUpdated)

	// same key, different payload → AlreadyExists.
	repo3, pr3, _ := receiveMocks(t, mkRun(), card)
	pr3.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).Return(nil, entity.ErrIdempotencyConflict)
	_, err = (&Server{repo: repo3}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, Lines: lines, IdempotencyKey: key,
	})
	require.Equal(t, codes.AlreadyExists, status.Code(err))

	// command error codes: already received / cancelled → FailedPrecondition, stale grid → InvalidArgument,
	// concurrent edit → Aborted.
	for storeErr, want := range map[error]codes.Code{
		entity.ErrProductionRunAlreadyReceived:    codes.FailedPrecondition,
		entity.ErrProductionRunCancelledReceive:   codes.FailedPrecondition,
		entity.ErrProductionRunReceiptLineUnknown: codes.InvalidArgument,
		entity.ErrProductionRunConflict:           codes.Aborted,
	} {
		repoN, prN, _ := receiveMocks(t, mkRun(), card)
		prN.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).Return(nil, storeErr)
		_, err = (&Server{repo: repoN}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
			RunId: 4, Lines: lines, IdempotencyKey: key,
		})
		require.Equal(t, want, status.Code(err), "store error %v", storeErr)
	}

	// shape validation: a malformed idempotency key and an empty line set never reach the store.
	sBad := &Server{repo: mocks.NewMockRepository(t)}
	_, err = sBad.PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{RunId: 4, Lines: lines, IdempotencyKey: "lowercase-not-ok"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = sBad.PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{RunId: 4, IdempotencyKey: key})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// The receipt command moves sellable stock: production:write alone (the interceptor gate) is not
// enough — products:write is enforced in-handler, failing closed without an authz in context.
func TestPostProductionRunReceiptRequiresProductsWrite(t *testing.T) {
	key := "01AAAAAAAAAAAAAAAAAAAAAAAA"
	lines := []*pb_admin.PostProductionRunReceiptLineInput{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 1}}
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress}}
	repo, _, _ := receiveMocks(t, run, &entity.TechCard{Id: 7})

	// production:write but NOT products:write → denied.
	scoped := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionProduction: entity.AccessWrite},
	})
	_, err := (&Server{repo: repo}).PostProductionRunReceipt(scoped, &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, Lines: lines, IdempotencyKey: key,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))

	// no authz in context at all → fail closed.
	_, err = (&Server{repo: repo}).PostProductionRunReceipt(context.Background(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, Lines: lines, IdempotencyKey: key,
	})
	require.Equal(t, codes.PermissionDenied, status.Code(err))
}

// TestProductionRunActualUnitCostBase covers the trusted actual-unit-cost math (moved to the entity
// so the store can seed cost_price inside its transaction). 1180 base cost / 118 received = 10.
func TestProductionRunActualUnitCostBase(t *testing.T) {
	run := &entity.ProductionRun{ProductionRunInsert: entity.ProductionRunInsert{
		Lines: []entity.ProductionRunLine{
			{SizeId: 1, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}},
			{SizeId: 2, ReceivedQty: sql.NullInt64{Int64: 60, Valid: true}},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("1180"), Valid: true}},
		},
	}}
	c := run.ActualUnitCostBase()
	require.True(t, c.Valid)
	require.True(t, c.Decimal.Equal(decimal.RequireFromString("10")), "1180 / 118 received, got %s", c.Decimal)

	// an uncosted manual article makes the figure untrustworthy → invalid.
	run.Costs = append(run.Costs, entity.ProductionRunCost{Kind: entity.ProductionRunCostCMT})
	require.False(t, run.ActualUnitCostBase().Valid, "partial fold → not trustworthy")
}

func TestReceiveProductionRunGuards(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.Colorways = []entity.TechCardColorway{{Id: 55, ProductId: sql.NullInt32{Int32: 55, Valid: true}}}
	recvLines := func(pid int32) []entity.ProductionRunLine {
		return []entity.ProductionRunLine{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: pid, Valid: true}, SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}}
	}

	// already received: the guard moved INTO the command's transaction (status is only authoritative
	// under the run lock) — the shim maps the store error to FailedPrecondition.
	run1 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunReceived, Lines: recvLines(55)}}
	repo1, pr1, _ := receiveMocks(t, run1, card)
	pr1.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).Return(nil, entity.ErrProductionRunAlreadyReceived)
	_, err := (&Server{repo: repo1}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// a counted line's product not linked to the card → InvalidArgument (store validates the fresh
	// lines against the handler's ValidProducts inside the lock).
	run2 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress, Lines: recvLines(999)}}
	repo2, pr2, _ := receiveMocks(t, run2, card)
	pr2.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).Return(nil, entity.ErrProductionRunLineProductUnlinked)
	_, err = (&Server{repo: repo2}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// a counted line with no product → FailedPrecondition.
	run3 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", SizeId: 1, PlannedQty: 10, ReceivedQty: sql.NullInt64{Int64: 10, Valid: true}}}}}
	repo3, pr3, _ := receiveMocks(t, run3, card)
	pr3.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).Return(nil, entity.ErrProductionRunLineProductMissing)
	_, err = (&Server{repo: repo3}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// no counted quantities at all → FailedPrecondition BEFORE any store call (nothing to synthesize).
	run4 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10}}}}
	repo4, _, _ := receiveMocks(t, run4, card)
	_, err = (&Server{repo: repo4}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))

	// an all-defect count IS synthesizable now (receipt v1 made all-scrap representable).
	run5 := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 10, DefectQty: sql.NullInt64{Int64: 10, Valid: true}}}}}
	repo5, pr5, _ := receiveMocks(t, run5, card)
	pr5.EXPECT().PostProductionRunReceipt(mock.Anything, mock.MatchedBy(func(p entity.PostProductionRunReceiptParams) bool {
		return len(p.Lines) == 1 && p.Lines[0].GoodQty == 0 && p.Lines[0].DefectQty == 10
	})).Return(&entity.PostProductionRunReceiptResult{ReceiptID: 3}, nil)
	_, err = (&Server{repo: repo5}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.NoError(t, err)
}

func TestGetProductionRunNotFoundAndList(t *testing.T) {
	// NotFound
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	pr.EXPECT().GetProductionRun(mock.Anything, 404).Return(nil, sql.ErrNoRows)
	s := &Server{repo: repo}
	_, err := s.GetProductionRun(context.Background(), &pb_admin.GetProductionRunRequest{Id: 404})
	require.Equal(t, codes.NotFound, status.Code(err))

	// invalid status filter → InvalidArgument (no store call)
	s2 := &Server{repo: mocks.NewMockRepository(t)}
	_, err = s2.ListProductionRuns(context.Background(), &pb_admin.ListProductionRunsRequest{Status: "bogus"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
