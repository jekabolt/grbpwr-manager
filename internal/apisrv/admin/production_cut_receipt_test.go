package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ПРИЁМКА КРОЯ — RPC layer. The store's own decisions are proved end to end in
// store/production_cut_receipt_integration_test.go; what is proved HERE is the part that has no
// database in it and would otherwise never be exercised: the projection onto the wire, and THE ERROR
// TABLE — one refusal, one code, whichever RPC met it.
//
// A wrong code here is invisible in a store test and expensive in the client: an Aborted read as an
// Internal turns «reload and retry» into «something broke», and a FailedPrecondition read as an
// InvalidArgument sends the operator to fix a field that is not wrong.

const cutReceiptTestRunID = 3

func cutReceiptRepo(t *testing.T) (*mocks.MockRepository, *mocks.MockProductionRuns) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().IsErrForeignKeyViolation(mock.Anything).Return(false).Maybe()
	return repo, pr
}

// TestProductionRunCutReceiptProjection checks that a stored row reaches the wire whole, and that
// «no note» stays one value in both directions rather than becoming two.
func TestProductionRunCutReceiptProjection(t *testing.T) {
	repo, pr := cutReceiptRepo(t)
	now := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)
	pr.EXPECT().ListCutReceipts(mock.Anything, cutReceiptTestRunID).Return([]entity.ProductionRunCutReceipt{
		{
			Id: 11, LayId: 4, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA", SizeId: 2, SizeName: "xs",
			CutQty: 12, AcceptedQty: 10,
			Note:      sql.NullString{String: "перекрой на 2", Valid: true},
			CreatedBy: "cutter", UpdatedBy: "second-cutter", CreatedAt: now, UpdatedAt: now,
		},
		{Id: 12, LayId: 4, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA", SizeId: 3, SizeName: "s", CutQty: 4},
	}, nil)

	resp, err := (&Server{repo: repo}).ListProductionRunCutReceipts(context.Background(),
		&pb_admin.ListProductionRunCutReceiptsRequest{RunId: cutReceiptTestRunID})
	require.NoError(t, err)
	require.Len(t, resp.GetReceipts(), 2)

	first := resp.GetReceipts()[0]
	require.Equal(t, int32(11), first.GetId())
	require.Equal(t, "01CUTLAYAAAAAAAAAAAAAAAAAA", first.GetLayKey(),
		"строка адресуется СТАБИЛЬНЫМ ключом настила — id настила клиент никогда не видит")
	require.Equal(t, int32(2), first.GetSizeId())
	require.Equal(t, "xs", first.GetSizeName())
	require.Equal(t, int32(12), first.GetCutQty())
	require.Equal(t, int32(10), first.GetAcceptedQty())
	require.Equal(t, "перекрой на 2", first.GetNote())
	require.Equal(t, "second-cutter", first.GetUpdatedBy())
	require.NotNil(t, first.GetCreatedAt())

	require.Empty(t, resp.GetReceipts()[1].GetNote(), "NULL-заметка едет пустой строкой, а не «<nil>»")
}

// TestProductionRunCutReceiptEmptyNoteIsNull proves the collapse in the other direction: an empty
// note off the wire becomes an INVALID NullString, so the column holds NULL and «нет заметки» has
// one representation instead of two.
func TestProductionRunCutReceiptEmptyNoteIsNull(t *testing.T) {
	repo, pr := cutReceiptRepo(t)
	var captured entity.ProductionRunCutReceiptInsert
	pr.EXPECT().SaveCutReceipt(mock.Anything, cutReceiptTestRunID, "01CUTLAYAAAAAAAAAAAAAAAAAA",
		mock.Anything, mock.Anything).
		Run(func(_ context.Context, _ int, _ string, ins entity.ProductionRunCutReceiptInsert, _ string) {
			captured = ins
		}).
		Return(entity.ProductionRunCutReceipt{Id: 11, SizeId: 2, CutQty: 12, AcceptedQty: 10}, nil)

	resp, err := (&Server{repo: repo}).SaveProductionRunCutReceipt(context.Background(),
		&pb_admin.SaveProductionRunCutReceiptRequest{
			RunId: cutReceiptTestRunID, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA",
			Receipt: &pb_common.ProductionRunCutReceiptInsert{SizeId: 2, CutQty: 12, AcceptedQty: 10},
		})
	require.NoError(t, err)
	require.Equal(t, int32(11), resp.GetReceipt().GetId())
	require.False(t, captured.Note.Valid, "пустая строка с провода — это NULL в колонке, а не пустая заметка")
	require.Equal(t, 12, captured.CutQty)
	require.Equal(t, 10, captured.AcceptedQty)
}

// TestProductionRunCutReceiptErrorTable is the table itself, asserted through every RPC that can
// meet each refusal.
func TestProductionRunCutReceiptErrorTable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want codes.Code
	}{
		{"validation error", entity.NewFieldViolation("receipt.cut_qty", "out_of_range", "-1", "fix it"), codes.InvalidArgument},
		{"create/create race", fmt.Errorf("wrapped: %w", entity.ErrProductionRunCutReceiptConflict), codes.Aborted},
		{"auxiliary card", fmt.Errorf("wrapped: %w", entity.ErrProductionRunLayNotApplicable), codes.FailedPrecondition},
		{"unknown настил", fmt.Errorf("wrapped: %w", entity.ErrProductionRunLayNotFound), codes.NotFound},
		{"unknown pair", fmt.Errorf("wrapped: %w", entity.ErrProductionRunCutReceiptNotFound), codes.NotFound},
		{"unknown run", fmt.Errorf("wrapped: %w", sql.ErrNoRows), codes.NotFound},
		{"anything else", errors.New("connection reset"), codes.Internal},
		// A TERMINAL RUN IS NOT IN THIS TABLE ON PURPOSE. The store applies no status guard, so
		// ErrProductionRunLocked cannot arrive here; if some future edit made it possible, it would
		// fall through to Internal — which is exactly the noise that should make somebody look.
	}
	for _, tc := range cases {
		t.Run("save/"+tc.name, func(t *testing.T) {
			repo, pr := cutReceiptRepo(t)
			pr.EXPECT().SaveCutReceipt(mock.Anything, cutReceiptTestRunID, mock.Anything, mock.Anything, mock.Anything).
				Return(entity.ProductionRunCutReceipt{}, tc.err)
			_, err := (&Server{repo: repo}).SaveProductionRunCutReceipt(context.Background(),
				&pb_admin.SaveProductionRunCutReceiptRequest{
					RunId: cutReceiptTestRunID, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA",
					Receipt: &pb_common.ProductionRunCutReceiptInsert{SizeId: 2, CutQty: 1},
				})
			require.Equal(t, tc.want, status.Code(err))
		})
		t.Run("delete/"+tc.name, func(t *testing.T) {
			repo, pr := cutReceiptRepo(t)
			pr.EXPECT().DeleteCutReceipt(mock.Anything, cutReceiptTestRunID, mock.Anything, mock.Anything).
				Return(tc.err)
			_, err := (&Server{repo: repo}).DeleteProductionRunCutReceipt(context.Background(),
				&pb_admin.DeleteProductionRunCutReceiptRequest{
					RunId: cutReceiptTestRunID, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA", SizeId: 2,
				})
			require.Equal(t, tc.want, status.Code(err))
		})
		t.Run("list/"+tc.name, func(t *testing.T) {
			repo, pr := cutReceiptRepo(t)
			pr.EXPECT().ListCutReceipts(mock.Anything, cutReceiptTestRunID).Return(nil, tc.err)
			_, err := (&Server{repo: repo}).ListProductionRunCutReceipts(context.Background(),
				&pb_admin.ListProductionRunCutReceiptsRequest{RunId: cutReceiptTestRunID})
			require.Equal(t, tc.want, status.Code(err))
		})
	}
}

// TestProductionRunCutReceiptBoundaryValidation checks the two refusals that never reach the store:
// a missing run and a missing payload. Both are asserted with the repository holding NO expectation
// for the call, so a regression that let them through would fail on the unexpected call rather than
// pass quietly.
func TestProductionRunCutReceiptBoundaryValidation(t *testing.T) {
	repo, _ := cutReceiptRepo(t)
	srv := &Server{repo: repo}

	_, err := srv.ListProductionRunCutReceipts(context.Background(),
		&pb_admin.ListProductionRunCutReceiptsRequest{})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.SaveProductionRunCutReceipt(context.Background(),
		&pb_admin.SaveProductionRunCutReceiptRequest{LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA"})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	_, err = srv.SaveProductionRunCutReceipt(context.Background(),
		&pb_admin.SaveProductionRunCutReceiptRequest{RunId: cutReceiptTestRunID, LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA"})
	require.Equal(t, codes.InvalidArgument, status.Code(err), "пустой receipt — это отказ, а не апсерт нулями")

	_, err = srv.DeleteProductionRunCutReceipt(context.Background(),
		&pb_admin.DeleteProductionRunCutReceiptRequest{LayKey: "01CUTLAYAAAAAAAAAAAAAAAAAA", SizeId: 2})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}

// TestProductionRunCutReceiptRBAC pins the SECTION and the ACCESS of the three methods. The rbac
// package already has a completeness test that refuses an unmapped RPC, so presence is covered; what
// is NOT covered there is that the receipt landed in the right section with the right level. Пишет
// тот, кто ведёт цех — и это единственное, что ограничивает приёмку кроя, потому что статус прогона
// её не ограничивает (её принимают и на закрытом прогоне).
func TestProductionRunCutReceiptRBAC(t *testing.T) {
	for name, want := range map[string]entity.AccessLevel{
		"SaveProductionRunCutReceipt":   entity.AccessWrite,
		"DeleteProductionRunCutReceipt": entity.AccessWrite,
		"ListProductionRunCutReceipts":  entity.AccessRead,
	} {
		req, allowlisted, known := rbac.Lookup(rbac.MethodPrefix + name)
		require.True(t, known, "%s is not in the rbac map and is therefore denied to everyone", name)
		require.False(t, allowlisted, "%s carries the run's quantities and must not be open to any account", name)
		require.Equal(t, rbac.SectionProduction, req.Section, "%s belongs to the workshop, not to the tech card", name)
		require.Equal(t, want, req.Access, "%s is mapped at the wrong access level", name)
	}
}
