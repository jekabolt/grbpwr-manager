package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"google.golang.org/protobuf/proto"
)

// CreateProductionRun freezes the planned unit cost from a linked tech_card_release (task 11).
func TestCreateProductionRunSnapshotsPlanFromRelease(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().ProductionRuns().Return(pr)
	expectRunReadinessGatePasses(t, repo, tc, 7)

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
	expectRunReservationReconcileStandsDown(t, pr, 11)

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

// A colour line carries into the store, and the store's plan-time refusals map to codes the client
// can act on: a colour that is not this card's is a bad payload (InvalidArgument), a RETIRED colour
// is a card state the operator can fix (FailedPrecondition, "reactivate it or pick another"). Both
// messages carry the store's detail — the colour id — because that is the actionable half.
func TestCreateProductionRunMapsColourVariantRefusals(t *testing.T) {
	plan := func(t *testing.T, storeErr error) error {
		t.Helper()
		repo := mocks.NewMockRepository(t)
		tc := mocks.NewMockTechCards(t)
		pr := mocks.NewMockProductionRuns(t)
		repo.EXPECT().TechCards().Return(tc)
		repo.EXPECT().ProductionRuns().Return(pr)
		expectRunReadinessGatePasses(t, repo, tc, 7)
		// Plan the cost from a release, so the fixture needs no costing-FX rate set — the colour
		// refusal is what this test is about.
		tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(&entity.TechCardRelease{
			TechCardReleaseMeta: entity.TechCardReleaseMeta{Id: 5, TechCardId: 7},
		}, nil)
		pr.EXPECT().CreateProductionRun(mock.Anything, mock.MatchedBy(func(r *entity.ProductionRunInsert) bool {
			// The colour reaches the store on the line, not as a product.
			return len(r.Lines) == 1 && r.Lines[0].OutputVariantId.Int32 == 4 && !r.Lines[0].ProductId.Valid
		})).Return(0, storeErr)

		s := &Server{repo: repo}
		_, err := s.CreateProductionRun(context.Background(), &pb_admin.CreateProductionRunRequest{
			Run: &pb_common.ProductionRunInsert{
				TechCardId: 7, ReleaseId: 5,
				Status: pb_common.ProductionRunStatus_PRODUCTION_RUN_STATUS_PLANNED,
				Lines:  []*pb_common.ProductionRunLine{{OutputVariantId: 4, PlannedQty: 50}},
			},
		})
		return err
	}

	err := plan(t, fmt.Errorf("%w: colour variant 4 is not a colour of tech card 7",
		entity.ErrProductionRunLineVariantUnlinked))
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "colour variant 4")

	err = plan(t, fmt.Errorf("%w: colour variant 4", entity.ErrProductionRunLineVariantRetired))
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "reactivate the colour or pick another")
}

func TestCreateProductionRunReleaseNotFound(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	expectRunReadinessGatePasses(t, repo, tc, 7)
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
	pr.EXPECT().UpdateProductionRunPreservingCosts(mock.Anything, 7, mock.AnythingOfType("*entity.ProductionRunInsert"), entity.LockVersion(3)).
		Return(errors.New("preservation read failed"))
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false)

	_, err := (&Server{repo: repo}).UpdateProductionRun(context.Background(), &pb_admin.UpdateProductionRunRequest{
		Id:                  7,
		ExpectedLockVersion: proto.Int32(3),
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
	require.False(t, got.ExpectedLockVersion.Present(),
		"the deprecated shim sends NO version at all — the legacy opt-out is the token's absence, not a literal 0 (Ф6.5)")
}

// auxColourRun is a variant-mode aux run: one product-less line per ACTIVE colour, and a card whose
// registry says which warehouse bucket each colour produces into.
func auxColourRun() (*entity.ProductionRun, *entity.TechCard) {
	run := &entity.ProductionRun{Id: 4, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{
			{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", OutputVariantId: sql.NullInt32{Int32: 1, Valid: true}, PlannedQty: 60,
				ReceivedQty: sql.NullInt64{Int64: 60, Valid: true}},
			{LineKey: "K2AAAAAAAAAAAAAAAAAAAAAAAA", OutputVariantId: sql.NullInt32{Int32: 2, Valid: true}, PlannedQty: 40,
				ReceivedQty: sql.NullInt64{Int64: 40, Valid: true}},
		},
	}}
	card := &entity.TechCard{Id: 7}
	card.Purpose = entity.TechCardPurposeAuxiliary
	// A legacy single output is present AND ignored: colours win the moment one is active.
	card.OutputMaterialId = sql.NullInt64{Int64: 99, Valid: true}
	card.OutputVariants = []entity.TechCardOutputVariant{
		{TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{Id: 1, ColorCode: "BLK", MaterialId: 31, Active: true}, TechCardId: 7},
		{TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{Id: 2, ColorCode: "WHT", MaterialId: 32, Active: true}, TechCardId: 7},
		// Retired: deliberately absent from the map the command receives, so a line still naming it is
		// a stale grid the store refuses rather than a bucket the card gave up being revived.
		{TechCardOutputVariantInsert: entity.TechCardOutputVariantInsert{Id: 3, ColorCode: "RED", MaterialId: 33, Active: false}, TechCardId: 7},
	}
	return run, card
}

// The handler marks the run auxiliary and stops there: WHERE the output lands — one bucket per
// colour, or a legacy card's single one — is resolved by the store from the card's registry INSIDE
// the receipt transaction (review F1/F2), because this read happens before the run lock and any
// bucket it named could be re-pointed or retired before the command runs.
func TestPostProductionRunReceiptAuxLeavesTheDestinationToTheStore(t *testing.T) {
	run, card := auxColourRun()
	repo, pr, _ := receiveMocks(t, run, card)
	var got entity.PostProductionRunReceiptParams
	pr.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		RunAndReturn(func(_ context.Context, p entity.PostProductionRunReceiptParams) (*entity.PostProductionRunReceiptResult, error) {
			got = p
			return &entity.PostProductionRunReceiptResult{ReceiptID: 21}, nil
		})

	_, err := (&Server{repo: repo}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, IdempotencyKey: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		Lines: []*pb_admin.PostProductionRunReceiptLineInput{
			{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 60},
			{LineKey: "K2AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 40},
		},
	})
	require.NoError(t, err)
	require.True(t, got.Aux)
	require.Empty(t, got.ValidProducts, "an aux run books no product stock")
}

// The one aux precondition still worth answering without opening a transaction: a legacy card with
// no colours AND no output material has no destination the store could resolve to, and the operator
// gets a fixable precondition instead of a reload-and-retry from inside the command.
func TestPostProductionRunReceiptAuxWithNoColoursAndNoOutputMaterial(t *testing.T) {
	run, card := auxColourRun()
	card.OutputVariants = nil
	card.OutputMaterialId = sql.NullInt64{}
	// A card with zero variant rows cannot have colour lines on its runs (the delete pre-check and
	// the FK forbid removing a referenced variant), so the true legacy state is a colourless grid.
	for i := range run.Lines {
		run.Lines[i].OutputVariantId = sql.NullInt32{}
	}
	repo, _, _ := receiveMocks(t, run, card)
	_, err := (&Server{repo: repo}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, IdempotencyKey: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		Lines: []*pb_admin.PostProductionRunReceiptLineInput{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 60}},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "no output material")

	// A card whose colours are ALL retired is in that same legacy mode — but it HAS an output
	// material, so it passes the gate and the store decides the rest.
	run2, card2 := auxColourRun()
	for i := range card2.OutputVariants {
		card2.OutputVariants[i].Active = false
	}
	repoB, prB, _ := receiveMocks(t, run2, card2)
	prB.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		Return(&entity.PostProductionRunReceiptResult{ReceiptID: 22}, nil)
	_, err = (&Server{repo: repoB}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, IdempotencyKey: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		Lines: []*pb_admin.PostProductionRunReceiptLineInput{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 60}},
	})
	require.NoError(t, err)

	// And the grandfathering promise at this gate: all colours retired, NO output material either —
	// but the run's own lines still carry colours, so the union rule opens colour mode and the store
	// (which reads the registry fresh, retired rows included) decides the booking. An ACTIVE-only
	// answer here would freeze the run behind advice about an output material the card never had.
	run3, card3 := auxColourRun()
	for i := range card3.OutputVariants {
		card3.OutputVariants[i].Active = false
	}
	card3.OutputMaterialId = sql.NullInt64{}
	repoC, prC, _ := receiveMocks(t, run3, card3)
	prC.EXPECT().PostProductionRunReceipt(mock.Anything, mock.Anything).
		Return(&entity.PostProductionRunReceiptResult{ReceiptID: 23}, nil)
	_, err = (&Server{repo: repoC}).PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{
		RunId: 4, IdempotencyKey: "01AAAAAAAAAAAAAAAAAAAAAAAA",
		Lines: []*pb_admin.PostProductionRunReceiptLineInput{{LineKey: "K1AAAAAAAAAAAAAAAAAAAAAAAA", GoodQty: 60}},
	})
	require.NoError(t, err)
}

// The deprecated shim receives from counts already stamped on the plan grid — a flow that predates
// colours. Rather than let it drive a per-colour booking it was never designed to express, it points
// at the command that carries the breakdown.
func TestReceiveProductionRunShimRefusesColourModeCard(t *testing.T) {
	run, card := auxColourRun()
	repo, _, _ := receiveMocks(t, run, card)
	_, err := (&Server{repo: repo}).ReceiveProductionRun(fullAccessCtx(), &pb_admin.ReceiveProductionRunRequest{RunId: 4})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "per colour variant")
	require.Contains(t, status.Convert(err).Message(), "receipt command")
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
		RunId: 4, Lines: lines, IdempotencyKey: key, ExpectedLockVersion: proto.Int32(3), Note: "final",
	})
	require.NoError(t, err)
	require.Equal(t, int32(12), resp.ReceiptId)
	require.False(t, resp.Replayed)
	require.NotNil(t, resp.Run, "post-command run echoed")
	require.Equal(t, entity.LockVersion(3), got.ExpectedLockVersion)
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

	// shape validation: a malformed idempotency key and an empty PARTIAL line set never reach the
	// store. (An empty FINAL is legal — the short-close of a partially received run — and is pinned
	// with real schema in TestProductionReceiptPartialFlow.)
	sBad := &Server{repo: mocks.NewMockRepository(t)}
	_, err = sBad.PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{RunId: 4, Lines: lines, IdempotencyKey: "lowercase-not-ok"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	_, err = sBad.PostProductionRunReceipt(fullAccessCtx(), &pb_admin.PostProductionRunReceiptRequest{RunId: 4, IdempotencyKey: key, Partial: true})
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

// expectRunReadinessGatePasses stubs the reads the Ф6 create-time gate makes, with a card that
// raises no BLOCKER, so a test about something else is not also a test about the gate.
//
// It is deliberately a HELPER rather than a switch: there is no way to turn the gate off, and adding
// one for tests would have meant adding one for production. The gate's own behaviour is tested in
// TestRunReadinessCreateGate* and in the dto layer.
// expectRunReservationReconcileStandsDown lets a create-path test ignore Ф5б.4 without pretending it
// is not there.
//
// CreateProductionRun reserves fabric after the run is born, and that reconcile re-reads the run to
// compose its material plan. A test about the PLANNED-COST SNAPSHOT or about the READINESS GATE has
// no business also mocking a material plan — so the run read is stubbed to fail, which drives the
// reconcile down its best-effort path: it logs and returns, exactly as it does in production when
// the plan cannot be composed.
//
// ЭТО НЕ ГЛУШИТ ПРОВЕРКУ РЕЗЕРВА, А ПЕРЕАДРЕСУЕТ ЕЁ. Какое ЧИСЛО уезжает в резерв, проверяет
// f5b_run_reservation_wiring_test.go на общей с Ф4.6 фикстуре.
//
// ONCE, А НЕ MAYBE, И ЭТО ВТОРАЯ ПОЛОВИНА СМЫСЛА. Maybe промолчал бы, если бы вызов резерва из
// CreateProductionRun кто-то удалил, — то есть хелпер, написанный ради «не мешать», сам стал бы тем,
// что прячет пропажу. Once превращает его в противоположность: оба теста, которые его зовут, ЗАОДНО
// доказывают, что резерв на рождении прогона случается ровно один раз.
func expectRunReservationReconcileStandsDown(t *testing.T, pr *mocks.MockProductionRuns, runID int) {
	t.Helper()
	pr.EXPECT().GetProductionRun(mock.Anything, runID).
		Return(nil, errors.New("run read unavailable in this test")).Once()
}

func expectRunReadinessGatePasses(t *testing.T, repo *mocks.MockRepository, tc *mocks.MockTechCards, cardID int) {
	t.Helper()
	ws := mocks.NewMockWorkshop(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().Workshop().Return(ws).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()
	// Unset: report-only, which is what an unconfigured workshop gets and what the gate defaults to.
	ws.EXPECT().GetSettings(mock.Anything).Return(&entity.WorkshopSettings{}, nil).Maybe()
	ms.EXPECT().NarrowestMeasuredLotWidths(mock.Anything, mock.Anything).
		Return(map[int]decimal.NullDecimal{}, nil).Maybe()
	tc.EXPECT().GetTechCardById(mock.Anything, cardID).Return(&entity.TechCard{
		Id:             cardID,
		TechCardInsert: entity.TechCardInsert{SizeIds: []int{1}, Pieces: []entity.TechCardPiece{{LineKey: "P1", Name: "перед"}}},
	}, nil).Maybe()
	tc.EXPECT().GetTechCardPatternSizeIndex(mock.Anything, cardID).
		Return(map[string]entity.PatternSizeIndexRow{}, nil).Maybe()
}
