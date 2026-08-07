package admin

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"
)

// НАСТИЛЫ (Ф4) — handler tests. They pin the four things that cannot be checked by reading the
// arithmetic: how often a blob is parsed, which store error becomes which gRPC code, that an aux card
// is refused BEFORE anything else is read, and that the half of the marker RBAC rule the map cannot
// see is actually enforced.

const (
	layTestRunID    = 3
	layTestCardID   = 7
	layTestColorway = 100
	layTestSlotID   = 5
	layTestMarkerID = 900
	layTestSizeID   = 1
	layTestMaterial = 42
)

// layRepo wires the four sub-repos the lay plan touches.
type layRepo struct {
	repo *mocks.MockRepository
	pr   *mocks.MockProductionRuns
	tc   *mocks.MockTechCards
	ms   *mocks.MockMaterialStock
	ws   *mocks.MockWorkshop
}

func newLayRepo(t *testing.T) layRepo {
	t.Helper()
	r := layRepo{
		repo: mocks.NewMockRepository(t), pr: mocks.NewMockProductionRuns(t),
		tc: mocks.NewMockTechCards(t), ms: mocks.NewMockMaterialStock(t), ws: mocks.NewMockWorkshop(t),
	}
	r.repo.EXPECT().ProductionRuns().Return(r.pr).Maybe()
	r.repo.EXPECT().TechCards().Return(r.tc).Maybe()
	r.repo.EXPECT().MaterialStock().Return(r.ms).Maybe()
	r.repo.EXPECT().Workshop().Return(r.ws).Maybe()
	return r
}

// layTestCard is a one-cloth, one-piece card: полочка (mirrored, 2 per garment) cut from the main
// fabric slot, which colourway 100 buys article 42 for.
func layTestCard() *entity.TechCard {
	card := &entity.TechCard{Id: layTestCardID}
	card.Purpose = entity.TechCardPurposeSellable
	card.BomItems = []entity.TechCardBomItem{{
		Id: layTestSlotID, LineKey: "B1", Section: entity.BomSectionFabric, Name: "основная ткань",
		MaterialId: sql.NullInt64{Int64: layTestMaterial, Valid: true},
	}}
	card.Pieces = []entity.TechCardPiece{{
		Id: 1, Name: "полочка", LineKey: "P1", PiecesPerGarment: 2,
		CutSymmetry: sql.NullString{String: string(entity.PieceCutSymmetryMirrored), Valid: true},
		Materials: []entity.TechCardPieceMaterial{{
			ColorwayID: layTestColorway, BomItemId: sql.NullInt64{Int64: layTestSlotID, Valid: true},
		}},
	}}
	card.Colorways = []entity.TechCardColorway{{
		Name: "BLACK", ProductId: sql.NullInt32{Int32: layTestColorway, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{BomItemId: sql.NullInt64{Int64: layTestSlotID, Valid: true}}},
	}}
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{layTestMaterial: layTestMaterialRow()}
	return card
}

func layTestMaterialRow() entity.MaterialWithPrice {
	var m entity.MaterialWithPrice
	m.Name = "ART-4410 saint"
	return m
}

func layTestRun() *entity.ProductionRun {
	run := &entity.ProductionRun{Id: layTestRunID}
	run.TechCardId = layTestCardID
	run.Lines = []entity.ProductionRunLine{{
		LineKey: "L1", ProductId: sql.NullInt32{Int32: layTestColorway, Valid: true},
		SizeId: layTestSizeID, PlannedQty: 10,
	}}
	return run
}

// layTestMarker is a schema-4 раскладка of this run, on this slot, cutting one garment of the size
// with both hands of the piece present.
func layTestMarker(t *testing.T) *entity.TechCardMarker {
	t.Helper()
	l := &pb_common.TechCardMarkerLayout{
		SchemaVersion: 4,
		Composition:   []*pb_common.TechCardMarkerCompositionEntry{{SizeId: layTestSizeID, Quantity: 1}},
		Pieces: []*pb_common.TechCardMarkerPiece{{
			PieceId: 1, Name: "полочка", PieceLineKey: "P1", SizeId: layTestSizeID, Quantity: 2,
		}},
		Placements: []*pb_common.TechCardMarkerPlacement{
			{PieceId: 1}, {PieceId: 1, Flipped: true},
		},
	}
	blob, err := protojson.Marshal(l)
	require.NoError(t, err)
	return &entity.TechCardMarker{
		TechCardMarkerSummary: entity.TechCardMarkerSummary{
			Id: layTestMarkerID, TechCardId: layTestCardID, Name: "основная 40-42",
			RunId:      sql.NullInt64{Int64: layTestRunID, Valid: true},
			BomItemId:  sql.NullInt64{Int64: layTestSlotID, Valid: true},
			ColorwayId: sql.NullInt64{Int64: layTestColorway, Valid: true},
			Composition: []entity.MarkerCompositionEntry{
				{SizeId: layTestSizeID, Quantity: 1},
			},
			FabricWidthCm: decimal.NewFromInt(150),
			UsedLengthCm:  decimal.NewFromInt(200),
		},
		Layout: string(blob),
	}
}

// layTestLay is ONE настил with two sections that name THE SAME раскладка — a ступенчатый настил, and
// the shape that makes the memo observable.
func layTestLay(layKey string, plies int) entity.ProductionRunLay {
	return entity.ProductionRunLay{
		Id: 1, RunId: layTestRunID, LayKey: layKey, ColorwayId: layTestColorway, ColorwayName: "BLACK",
		BomItemId:  sql.NullInt64{Int64: layTestSlotID, Valid: true},
		BomLineKey: "B1",
		Mode:       entity.ProductionLayModeFaceUp,
		EndLossCm:  decimal.NewFromInt(2),
		Sections: []entity.ProductionRunLaySection{
			{Id: 11, SectionKey: "S1", MarkerId: layTestMarkerID, Plies: plies, Position: 0,
				MarkerUsedLengthCm: decimal.NullDecimal{Decimal: decimal.NewFromInt(200), Valid: true}},
			{Id: 12, SectionKey: "S2", MarkerId: layTestMarkerID, Plies: plies, Position: 1,
				MarkerUsedLengthCm: decimal.NullDecimal{Decimal: decimal.NewFromInt(200), Valid: true}},
		},
		QtySnapshot: []entity.ProductionRunLayQtyEntry{{SizeId: layTestSizeID, Qty: 10}},
		QtyCurrent:  []entity.ProductionRunLayQtyEntry{{SizeId: layTestSizeID, Qty: 10}},
	}
}

// TestListProductionRunLaysParsesEachBlobOnce is §14 п.16 as a test.
//
// A раскладка legitimately stands in several sections of several настилов, and every section asks the
// same blob the same questions. One GetMarker per DISTINCT marker id is the observable half of «one
// protojson parse per marker per request»: the load and the distillation happen in the same function,
// so a second load would be a second parse. Mockery's .Once() turns a regression into a failure
// instead of a slow page nobody measures.
func TestListProductionRunLaysParsesEachBlobOnce(t *testing.T) {
	r := newLayRepo(t)
	lays := []entity.ProductionRunLay{layTestLay("LAY1", 10), layTestLay("LAY2", 4)}
	lays[1].Id = 2

	r.pr.EXPECT().ListLays(mock.Anything, layTestRunID).
		Return(entity.ProductionRunLayList{Applicable: true, Lays: lays}, nil).Once()
	r.pr.EXPECT().GetProductionRun(mock.Anything, layTestRunID).Return(layTestRun(), nil).Once()
	r.tc.EXPECT().GetTechCardById(mock.Anything, layTestCardID).Return(layTestCard(), nil).Once()
	// FOUR sections across TWO настилов, ONE marker: exactly one read, and therefore one parse.
	r.tc.EXPECT().GetMarker(mock.Anything, layTestMarkerID).Return(layTestMarker(t), nil).Once()
	r.tc.EXPECT().ListRunMarkers(mock.Anything, layTestRunID).
		Return([]entity.TechCardMarkerSummary{layTestMarker(t).TechCardMarkerSummary}, nil).Once()
	r.ms.EXPECT().NarrowestMeasuredLotWidths(mock.Anything, []int{layTestMaterial}).
		Return(map[int]decimal.NullDecimal{}, nil).Once()
	r.ws.EXPECT().GetSettings(mock.Anything).Return(&entity.WorkshopSettings{}, nil).Once()

	resp, err := (&Server{repo: r.repo}).ListProductionRunLays(context.Background(),
		&pb_admin.ListProductionRunLaysRequest{RunId: layTestRunID})
	require.NoError(t, err)
	require.True(t, resp.GetApplicable())
	require.Len(t, resp.GetLays(), 2)
	require.Len(t, resp.GetRunMarkers(), 1)
	require.Equal(t, int32(layTestRunID), resp.GetRunMarkers()[0].GetProductionRunId(),
		"the picker has to be able to tell a run's раскладка from the card's")

	lay := layPlanLayByKey(resp, "LAY1")
	require.NotNil(t, lay)
	require.Equal(t, int32(20), lay.GetTotalPlies())
	// 2 секции × 200 см × 10 слоёв = 4000 см ткани; концевые = 2 × 2 см × 20 слоёв = 80 см.
	require.Equal(t, "4000", lay.GetClothLengthCm().GetValue())
	require.Equal(t, "80", lay.GetEndLossTotalCm().GetValue())
	require.Equal(t, "4080", lay.GetPlannedLengthCm().GetValue())
	require.Equal(t, int32(layTestMaterial), lay.GetMaterialId())
	require.Nil(t, lay.GetStackHeightCm(),
		"без толщины ткани высота НЕ ОТДАЁТСЯ — «0 см, влезает» это самый уверенный неверный ответ")

	// Покрытие: 20 слоёв × (1 как нарисовано + 1 отражённая) = по 20 рук, деталь mirrored n=2 ⇒ 20
	// изделий против плана в 10 ⇒ клетка ЗАКРЫТА.
	require.Len(t, resp.GetCoverage(), 1)
	require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK, resp.GetCoverage()[0].GetStatus())
	require.Equal(t, int32(10), resp.GetCoverage()[0].GetCoveredQty())
	require.NotEmpty(t, resp.GetPieceYields())

	// UNKNOWN'ы — СЕРВЕРНЫЙ счёт. Здесь их минимум два: длина стола и предел стопки не настроены.
	require.Positive(t, resp.GetUnknownCount())
	byKey := map[string]*pb_common.ProductionLayCheck{}
	for _, c := range lay.GetChecks() {
		byKey[c.GetKey()] = c
	}
	require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_UNKNOWN,
		byKey["lay_stack_height"].GetStatus(), "толщина не задана ⇒ UNKNOWN, не OK")
	require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK,
		byKey["lay_mode_parity"].GetStatus())
	require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK,
		byKey["lay_slot_detached"].GetStatus())
	require.Len(t, lay.GetSections(), 2)
	require.NotEmpty(t, lay.GetSections()[0].GetChecks(), "годность маркера проверяется НА ЧТЕНИИ тоже")
}

// TestListProductionRunLaysJudgesFitnessOnRead is §14 п.6 as a test, and it is the reason the fitness
// predicates are not confined to the write path.
//
// `fk_tcm_bom` is ON DELETE SET NULL: deleting a BOM line from the CARD's tab detaches every раскладка
// taken on it, touching neither the настил nor the run. A настил that was fit yesterday has to stop
// LOOKING fit today — a check that ran only at save time would leave it green until somebody edited
// it, which may be never.
func TestListProductionRunLaysJudgesFitnessOnRead(t *testing.T) {
	r := newLayRepo(t)
	detached := layTestMarker(t)
	detached.BomItemId = sql.NullInt64{} // слот BOM удалили из карточки уже ПОСЛЕ сохранения настила

	r.pr.EXPECT().ListLays(mock.Anything, layTestRunID).Return(entity.ProductionRunLayList{
		Applicable: true, Lays: []entity.ProductionRunLay{layTestLay("LAY1", 10)},
	}, nil).Once()
	r.pr.EXPECT().GetProductionRun(mock.Anything, layTestRunID).Return(layTestRun(), nil).Once()
	r.tc.EXPECT().GetTechCardById(mock.Anything, layTestCardID).Return(layTestCard(), nil).Once()
	r.tc.EXPECT().GetMarker(mock.Anything, layTestMarkerID).Return(detached, nil).Once()
	r.tc.EXPECT().ListRunMarkers(mock.Anything, layTestRunID).Return(nil, nil).Once()
	r.ms.EXPECT().NarrowestMeasuredLotWidths(mock.Anything, []int{layTestMaterial}).
		Return(map[int]decimal.NullDecimal{}, nil).Once()
	r.ws.EXPECT().GetSettings(mock.Anything).Return(&entity.WorkshopSettings{}, nil).Once()

	resp, err := (&Server{repo: r.repo}).ListProductionRunLays(context.Background(),
		&pb_admin.ListProductionRunLaysRequest{RunId: layTestRunID})
	require.NoError(t, err)
	lay := layPlanLayByKey(resp, "LAY1")
	require.NotNil(t, lay)
	var scope *pb_common.ProductionLayCheck
	for _, c := range lay.GetSections()[0].GetChecks() {
		if c.GetKey() == "lay_marker_scope" {
			scope = c
		}
	}
	require.NotNil(t, scope, "область маркера проверяется на КАЖДОМ чтении, не только при сохранении")
	require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_BLOCKER, scope.GetStatus())
	require.NotEmpty(t, scope.GetDetail())
	// Настил сам по себе цел — слот на месте, — поэтому lay_slot_detached молчит, а красное несёт
	// именно секция. Две находки о РАЗНЫХ фактах, и схлопывать их на сервере было бы потерей одной.
	for _, c := range lay.GetChecks() {
		if c.GetKey() == "lay_slot_detached" {
			require.Equal(t, pb_common.ProductionLayCheckStatus_PRODUCTION_LAY_CHECK_STATUS_OK, c.GetStatus())
		}
	}
}

// TestListProductionRunLaysStopsAtAnAuxCard pins §1.9: the aux answer is EXPLICIT, and it is reached
// without reading the card, the markers, the articles or the workshop — the mock repo has no
// expectation for any of them, so a stray read fails the test.
func TestListProductionRunLaysStopsAtAnAuxCard(t *testing.T) {
	r := newLayRepo(t)
	r.pr.EXPECT().ListLays(mock.Anything, layTestRunID).Return(entity.ProductionRunLayList{
		Applicable:          false,
		NotApplicableReason: entity.ProductionRunLayNotApplicableKey,
	}, nil).Once()

	resp, err := (&Server{repo: r.repo}).ListProductionRunLays(context.Background(),
		&pb_admin.ListProductionRunLaysRequest{RunId: layTestRunID})
	require.NoError(t, err)
	require.False(t, resp.GetApplicable())
	require.Equal(t, entity.ProductionRunLayNotApplicableKey, resp.GetNotApplicableReason())
	require.Empty(t, resp.GetLays(), "пустой список читался бы как «настилов пока нет»; поэтому рядом стоит applicable=false")
	require.Empty(t, resp.GetCoverage())
}

// TestProductionRunLayErrorMapping is the store-error → gRPC-code table of §4 of the task, exercised
// through the RPCs that can raise each one. A code is a contract: Aborted tells the client to reload
// and retry, FailedPrecondition tells it to fix state, InvalidArgument tells it to fix the payload —
// and a client cannot tell those apart from prose.
func TestProductionRunLayErrorMapping(t *testing.T) {
	save := func(t *testing.T, storeErr error) error {
		t.Helper()
		r := newLayRepo(t)
		// The pre-flight stands aside when the run cannot be read, which is what puts the store's own
		// refusal on the wire — the case this table is about.
		r.pr.EXPECT().GetProductionRun(mock.Anything, layTestRunID).Return(nil, sql.ErrNoRows).Once()
		r.pr.EXPECT().SaveLay(mock.Anything, layTestRunID, mock.Anything, mock.Anything, false, mock.Anything).
			Return(entity.ProductionRunLay{}, storeErr).Once()
		_, err := (&Server{repo: r.repo}).SaveProductionRunLay(context.Background(), &pb_admin.SaveProductionRunLayRequest{
			RunId: layTestRunID,
			Lay: &pb_common.ProductionRunLayInsert{
				ColorwayId: layTestColorway, BomLineKey: "B1",
				Mode:     pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_UP,
				Sections: []*pb_common.ProductionRunLaySectionInsert{{MarkerId: layTestMarkerID, Plies: 10}},
			},
		})
		return err
	}

	t.Run("conflict → Aborted, in the run's own copy", func(t *testing.T) {
		err := save(t, entity.ErrProductionRunLayConflict)
		require.Equal(t, codes.Aborted, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "reload and retry")
	})

	t.Run("terminal run → FailedPrecondition", func(t *testing.T) {
		err := save(t, entity.ErrProductionRunLocked)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
	})

	t.Run("aux card → FailedPrecondition under the stable key", func(t *testing.T) {
		err := save(t, entity.ErrProductionRunLayNotApplicable)
		require.Equal(t, codes.FailedPrecondition, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), entity.ProductionRunLayNotApplicableKey,
			"the machine-readable half must survive onto the wire")
	})

	t.Run("field violation → InvalidArgument", func(t *testing.T) {
		err := save(t, entity.NewFieldViolation("lay.bom_line_key", "not_found", "B9", "pick a cloth line"))
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "lay.bom_line_key")
	})

	t.Run("missing run → NotFound", func(t *testing.T) {
		err := save(t, sql.ErrNoRows)
		require.Equal(t, codes.NotFound, status.Code(err))
	})

	t.Run("delete of an unknown настил → NotFound", func(t *testing.T) {
		r := newLayRepo(t)
		// WRAPPED, as the store actually returns it: the mapping has to go through errors.Is, never
		// through equality — the store names the настил and the run in the same sentence.
		r.pr.EXPECT().DeleteLay(mock.Anything, layTestRunID, "LAY9").
			Return(fmt.Errorf("%w: lay %q of run %d", entity.ErrProductionRunLayNotFound, "LAY9", layTestRunID)).Once()
		_, err := (&Server{repo: r.repo}).DeleteProductionRunLay(context.Background(),
			&pb_admin.DeleteProductionRunLayRequest{RunId: layTestRunID, LayKey: "LAY9"})
		require.Equal(t, codes.NotFound, status.Code(err))
	})
}

// TestSaveProductionRunLayRefusesAForeignMarker pins the WRITE half of §8.3 that the store cannot
// judge: `colorway_id` on a раскладка is THREE-valued (NULL = общая), and reading it as an int in SQL
// would pair two absences and call the pair an agreement. The refusal is FailedPrecondition — the
// payload is well-formed, the раскладка it names is the problem.
func TestSaveProductionRunLayRefusesAForeignMarker(t *testing.T) {
	r := newLayRepo(t)
	foreign := layTestMarker(t)
	foreign.ColorwayId = sql.NullInt64{Int64: 999, Valid: true} // измерена по другому цвету

	r.pr.EXPECT().GetProductionRun(mock.Anything, layTestRunID).Return(layTestRun(), nil).Once()
	r.tc.EXPECT().GetTechCardById(mock.Anything, layTestCardID).Return(layTestCard(), nil).Once()
	r.tc.EXPECT().GetMarker(mock.Anything, layTestMarkerID).Return(foreign, nil).Once()

	_, err := (&Server{repo: r.repo}).SaveProductionRunLay(context.Background(), &pb_admin.SaveProductionRunLayRequest{
		RunId: layTestRunID,
		Lay: &pb_common.ProductionRunLayInsert{
			ColorwayId: layTestColorway, BomLineKey: "B1",
			Mode:     pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_UP,
			Sections: []*pb_common.ProductionRunLaySectionInsert{{SectionKey: "S1", MarkerId: layTestMarkerID, Plies: 10}},
		},
	})
	require.Equal(t, codes.FailedPrecondition, status.Code(err))
	require.Contains(t, status.Convert(err).Message(), "колорвею")
}

// TestSaveTechCardMarkerRunOwnershipNeedsProductionWrite NAILS DOWN THE INVISIBLE HALF OF THE RULE
// (решение Р3).
//
// The RBAC map holds ONE section per method and holds tech_cards:write for SaveTechCardMarker, so the
// second requirement — production:write when the раскладка is taken FOR A RUN — exists only as a line
// of handler code. A rule the map cannot see is a rule that drifts, which is precisely why the
// decision that created it also demanded this test. The precedent is PostProductionRunReceipt, whose
// products:write half lives the same way.
func TestSaveTechCardMarkerRunOwnershipNeedsProductionWrite(t *testing.T) {
	scoped := func(perms map[string]entity.AccessLevel) context.Context {
		return authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{Perms: perms})
	}
	markerFor := func(runID int32) *pb_common.TechCardMarkerInsert {
		return &pb_common.TechCardMarkerInsert{
			Name: "основная 40-42", Source: string(entity.MarkerSourceAuto), ProductionRunId: runID,
			FabricWidthCm: &pb_decimal.Decimal{Value: "150"},
			UsedLengthCm:  &pb_decimal.Decimal{Value: "200"},
			PlacedCount:   1, TotalCount: 1,
			Layout: &pb_common.TechCardMarkerLayout{
				SchemaVersion: 4,
				// СОСТАВ ездит в блобе и больше нигде (Ф2), поэтому он здесь, а не на вставке.
				Composition: []*pb_common.TechCardMarkerCompositionEntry{{SizeId: layTestSizeID, Quantity: 1}},
				Pieces:      []*pb_common.TechCardMarkerPiece{{PieceId: 1, PieceLineKey: "P1"}},
				Placements:  []*pb_common.TechCardMarkerPlacement{{PieceId: 1}},
			},
		}
	}

	t.Run("tech_cards:write alone cannot mint a run's раскладка", func(t *testing.T) {
		// No repo expectation at all: the refusal must land BEFORE the store is touched.
		s := &Server{repo: mocks.NewMockRepository(t)}
		_, err := s.SaveTechCardMarker(
			scoped(map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite}),
			&pb_admin.SaveTechCardMarkerRequest{TechCardId: layTestCardID, Marker: markerFor(layTestRunID)})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
		require.Contains(t, status.Convert(err).Message(), "production:write")
	})

	t.Run("a context that never passed the interceptor fails closed", func(t *testing.T) {
		s := &Server{repo: mocks.NewMockRepository(t)}
		_, err := s.SaveTechCardMarker(context.Background(),
			&pb_admin.SaveTechCardMarkerRequest{TechCardId: layTestCardID, Marker: markerFor(layTestRunID)})
		require.Equal(t, codes.PermissionDenied, status.Code(err))
	})

	t.Run("production:write passes the gate and the save proceeds", func(t *testing.T) {
		r := newLayRepo(t)
		r.tc.EXPECT().SaveMarker(mock.Anything, layTestCardID, 0, mock.Anything, mock.Anything).
			Return(1, nil).Once()
		_, err := (&Server{repo: r.repo}).SaveTechCardMarker(
			scoped(map[string]entity.AccessLevel{
				rbac.SectionTechCards:  entity.AccessWrite,
				rbac.SectionProduction: entity.AccessWrite,
			}),
			&pb_admin.SaveTechCardMarkerRequest{TechCardId: layTestCardID, Marker: markerFor(layTestRunID)})
		require.NoError(t, err)
	})

	t.Run("a CARD раскладка is untouched by the rule", func(t *testing.T) {
		r := newLayRepo(t)
		r.tc.EXPECT().SaveMarker(mock.Anything, layTestCardID, 0, mock.Anything, mock.Anything).
			Return(1, nil).Once()
		_, err := (&Server{repo: r.repo}).SaveTechCardMarker(
			scoped(map[string]entity.AccessLevel{rbac.SectionTechCards: entity.AccessWrite}),
			&pb_admin.SaveTechCardMarkerRequest{TechCardId: layTestCardID, Marker: markerFor(0)})
		require.NoError(t, err, "production:write is required for a RUN's раскладка, not for every save")
	})
}

// TestProductionWriteAccess pins the access decision itself, including the fail-closed default.
func TestProductionWriteAccess(t *testing.T) {
	require.False(t, productionWriteAccess(context.Background()), "no authz in context → closed")
	require.True(t, productionWriteAccess(authsrv.PutAdminAuthz(context.Background(),
		authsrv.AdminAuthz{Super: true})))
	require.False(t, productionWriteAccess(authsrv.PutAdminAuthz(context.Background(),
		authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionProduction: entity.AccessRead}})),
		"read is not write")
	require.True(t, productionWriteAccess(authsrv.PutAdminAuthz(context.Background(),
		authsrv.AdminAuthz{Perms: map[string]entity.AccessLevel{rbac.SectionProduction: entity.AccessWrite}})))
}
