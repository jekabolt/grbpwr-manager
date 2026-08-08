package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ПРИЁМКА КРОЯ ПРОГОНА (Ф5б.5) — three RPCs, shaped after the настил's three next door
// (production_lays.go): a list, a save that addresses ONE pair, and a delete that addresses the same
// pair. Loads, permissions and gRPC codes live here; every decision about the numbers lives in
// productionrun/cut_receipt.go, and the chain they belong to is named once, in the proto.
//
// THIS FILE COMPUTES NOTHING, and that is not an accident of a thin phase. «Заказано» and «сдано
// готовым» are per (колорвей, размер) while a receipt is per (настил, размер), and one colourway
// legitimately has several настилы — so a total assembled HERE would be one order quantity
// replicated across настилы, and any sum over the rows would double it. The client joins the chain
// against the run lines it already holds.
//
// RBAC: the three methods are registered in internal/rbac/rbac.go beside the настил's own three
// (write for save/delete, read for list, section production). rbac.Lookup fails CLOSED for an
// unmapped admin method, so a missing entry is a dead RPC rather than an open one.

// productionRunCutReceiptNotApplicableMsg explains the FailedPrecondition an auxiliary card gets.
// The machine-readable half is entity.ProductionRunLayNotApplicableKey — the SAME key the настил
// uses, because it is the same fact: no colourways, no cut pieces and no раскладки means no настилы,
// and a pair (настил, размер) is what a cutting receipt is about.
const productionRunCutReceiptNotApplicableMsg = "an auxiliary tech card has no настилы, so there is no (настил, размер) pair to report a cut on"

// ListProductionRunCutReceipts returns every cutting receipt of a run, across all of its настилы.
func (s *Server) ListProductionRunCutReceipts(ctx context.Context, req *pb_admin.ListProductionRunCutReceiptsRequest) (*pb_admin.ListProductionRunCutReceiptsResponse, error) {
	runID := int(req.GetRunId())
	if runID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	rows, err := s.repo.ProductionRuns().ListCutReceipts(ctx, runID)
	if err != nil {
		return nil, s.productionRunCutReceiptError(ctx, "list", runID, err)
	}
	return &pb_admin.ListProductionRunCutReceiptsResponse{
		Receipts: convertProductionRunCutReceipts(rows),
	}, nil
}

// SaveProductionRunCutReceipt upserts ONE pair (настил, размер) and returns the stored row.
//
// There is no expected_lock_version and no pre-flight, and both absences are decisions rather than
// gaps: the row carries no version because a receipt is a count somebody read off the table rather
// than a plan two tabs co-author (entity.ErrProductionRunCutReceiptConflict covers the create/create
// race that remains), and there is nothing to pre-flight because the numbers are checked against
// nothing — overcutting is legitimate, and the store refuses only what is meaningless on its own
// terms.
func (s *Server) SaveProductionRunCutReceipt(ctx context.Context, req *pb_admin.SaveProductionRunCutReceiptRequest) (*pb_admin.SaveProductionRunCutReceiptResponse, error) {
	runID := int(req.GetRunId())
	if runID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	if req.GetReceipt() == nil {
		return nil, status.Error(codes.InvalidArgument, "receipt is required")
	}
	saved, err := s.repo.ProductionRuns().SaveCutReceipt(ctx, runID, req.GetLayKey(),
		convertPbProductionRunCutReceiptInsert(req.GetReceipt()), authsrv.GetAdminUsername(ctx))
	if err != nil {
		return nil, s.productionRunCutReceiptError(ctx, "save", runID, err)
	}
	return &pb_admin.SaveProductionRunCutReceiptResponse{
		Receipt: convertProductionRunCutReceipt(saved),
	}, nil
}

// DeleteProductionRunCutReceipt removes the count of ONE pair — the only way to say «this size was
// never on this настил», which zeroing the two numbers cannot say.
func (s *Server) DeleteProductionRunCutReceipt(ctx context.Context, req *pb_admin.DeleteProductionRunCutReceiptRequest) (*pb_admin.DeleteProductionRunCutReceiptResponse, error) {
	runID := int(req.GetRunId())
	if runID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	if err := s.repo.ProductionRuns().DeleteCutReceipt(ctx, runID, req.GetLayKey(), int(req.GetSizeId())); err != nil {
		return nil, s.productionRunCutReceiptError(ctx, "delete", runID, err)
	}
	return &pb_admin.DeleteProductionRunCutReceiptResponse{}, nil
}

// convertPbProductionRunCutReceiptInsert reads the writable half of one pair off the wire.
//
// An empty note arrives as "" because the field is a bare string, and it is carried as an INVALID
// NullString so the column holds NULL: «no note» gets one representation instead of two. The store
// makes the same collapse for a whitespace-only note, so the rule has one definition wherever the
// payload came from.
func convertPbProductionRunCutReceiptInsert(in *pb_common.ProductionRunCutReceiptInsert) entity.ProductionRunCutReceiptInsert {
	return entity.ProductionRunCutReceiptInsert{
		SizeId:      int(in.GetSizeId()),
		CutQty:      int(in.GetCutQty()),
		AcceptedQty: int(in.GetAcceptedQty()),
		Note:        sql.NullString{String: in.GetNote(), Valid: in.GetNote() != ""},
	}
}

func convertProductionRunCutReceipts(rows []entity.ProductionRunCutReceipt) []*pb_common.ProductionRunCutReceipt {
	out := make([]*pb_common.ProductionRunCutReceipt, 0, len(rows))
	for i := range rows {
		out = append(out, convertProductionRunCutReceipt(rows[i]))
	}
	return out
}

// convertProductionRunCutReceipt projects one stored row. LayKey and not lay_id: the настил is
// addressed by its stable key everywhere the API speaks of it, and the client groups the run's rows
// by that key.
func convertProductionRunCutReceipt(r entity.ProductionRunCutReceipt) *pb_common.ProductionRunCutReceipt {
	return &pb_common.ProductionRunCutReceipt{
		Id:          int32(r.Id),
		LayKey:      r.LayKey,
		SizeId:      int32(r.SizeId),
		SizeName:    r.SizeName,
		CutQty:      int32(r.CutQty),
		AcceptedQty: int32(r.AcceptedQty),
		Note:        r.Note.String,
		CreatedBy:   r.CreatedBy,
		UpdatedBy:   r.UpdatedBy,
		CreatedAt:   timestamppb.New(r.CreatedAt),
		UpdatedAt:   timestamppb.New(r.UpdatedAt),
	}
}

// productionRunCutReceiptError maps the store's typed refusals onto gRPC codes. ONE table, so the
// three RPCs cannot answer differently for the same fact.
//
//	entity.ValidationError                     → InvalidArgument + BadRequest field violations
//	ErrProductionRunCutReceiptConflict         → Aborted (create/create race: reload and retry)
//	ErrProductionRunLayNotApplicable           → FailedPrecondition, reason lay_plan_not_applicable
//	ErrProductionRunLayNotFound                → NotFound (the настил the pair hangs off)
//	ErrProductionRunCutReceiptNotFound         → NotFound (the pair itself)
//	sql.ErrNoRows                              → NotFound (the run)
//	FK violation                               → InvalidArgument
//
// ErrProductionRunLocked IS ABSENT FROM THIS TABLE, and its absence is the decision of the phase
// rather than an oversight: the store applies no terminal-status guard, because a cutting receipt is
// a report of what happened at the table and cutting precedes sewing, which precedes receiving. A
// received, closed or cancelled run therefore still accepts one — a cancelled run most of all, since
// its cut cloth is exactly the loss somebody has to account for.
func (s *Server) productionRunCutReceiptError(ctx context.Context, op string, runID int, err error) error {
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		return apierr.Invalid(ve)
	case errors.Is(err, entity.ErrProductionRunCutReceiptConflict):
		return status.Error(codes.Aborted, entity.ErrProductionRunCutReceiptConflict.Error())
	case errors.Is(err, entity.ErrProductionRunLayNotApplicable):
		return apierr.FailedPrecondition(entity.NewFieldViolation("run_id",
			entity.ProductionRunLayNotApplicableKey, productionRunCutReceiptNotApplicableMsg,
			"an auxiliary run is planned and received through its own step 1 — there is no cloth laid here"))
	case errors.Is(err, entity.ErrProductionRunLayNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, entity.ErrProductionRunCutReceiptNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "production run not found")
	case s.repo.IsErrForeignKeyViolation(err):
		return status.Error(codes.InvalidArgument, "the cut receipt references a missing настил or size")
	}
	slog.Default().ErrorContext(ctx, "production run cut receipt call failed",
		slog.String("op", op), slog.Int("run_id", runID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+op+" the cutting receipt; try again")
}
