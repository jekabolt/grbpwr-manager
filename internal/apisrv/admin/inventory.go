package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ReceiveMaterialStock records a purchase-in of a material (new-flow NF-01). Setting a unit cost
// writes confidential price data, so it requires costing:write; a quantity-only receipt does not.
func (s *Server) ReceiveMaterialStock(ctx context.Context, req *pb_admin.ReceiveMaterialStockRequest) (*pb_admin.ReceiveMaterialStockResponse, error) {
	ins, err := dto.ConvertPbReceiveMaterialStock(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if ins.UnitCost.Valid {
		if _, write := s.costingAccess(ctx); !write {
			return nil, status.Error(codes.PermissionDenied, "costing:write is required to record a material cost")
		}
	}
	ins.AdminUsername = authsrv.GetAdminUsername(ctx)
	m, err := s.repo.MaterialStock().ReceiveMaterialStock(ctx, ins)
	if err != nil {
		return nil, mapInventoryErr(ctx, "receive material stock", err)
	}
	return &pb_admin.ReceiveMaterialStockResponse{Movement: s.movementToPb(ctx, m)}, nil
}

// IssueMaterialStock issues (or returns) material to a production run or a sample.
func (s *Server) IssueMaterialStock(ctx context.Context, req *pb_admin.IssueMaterialStockRequest) (*pb_admin.IssueMaterialStockResponse, error) {
	ins, err := dto.ConvertPbIssueMaterialStock(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ins.AdminUsername = authsrv.GetAdminUsername(ctx)
	m, err := s.repo.MaterialStock().IssueMaterialStock(ctx, ins)
	if err != nil {
		return nil, mapInventoryErr(ctx, "issue material stock", err)
	}
	return &pb_admin.IssueMaterialStockResponse{Movement: s.movementToPb(ctx, m)}, nil
}

// AdjustMaterialStock records a stock count (set/adjust) or a write-off.
func (s *Server) AdjustMaterialStock(ctx context.Context, req *pb_admin.AdjustMaterialStockRequest) (*pb_admin.AdjustMaterialStockResponse, error) {
	ins, err := dto.ConvertPbAdjustMaterialStock(req)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	ins.AdminUsername = authsrv.GetAdminUsername(ctx)
	m, err := s.repo.MaterialStock().AdjustMaterialStock(ctx, ins)
	if err != nil {
		return nil, mapInventoryErr(ctx, "adjust material stock", err)
	}
	return &pb_admin.AdjustMaterialStockResponse{Movement: s.movementToPb(ctx, m)}, nil
}

// GetMaterialStock returns a material's on-hand balance and valuation.
func (s *Server) GetMaterialStock(ctx context.Context, req *pb_admin.GetMaterialStockRequest) (*pb_admin.GetMaterialStockResponse, error) {
	if req.GetMaterialId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material_id is required")
	}
	st, err := s.repo.MaterialStock().GetMaterialStock(ctx, int(req.GetMaterialId()))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "material not found")
		}
		slog.Default().ErrorContext(ctx, "can't get material stock", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get material stock")
	}
	pb := dto.ConvertEntityMaterialStockToPb(*st, cache.GetBaseCurrency())
	if read, _ := s.costingAccess(ctx); !read {
		stripMaterialStockCosting(pb)
	}
	return &pb_admin.GetMaterialStockResponse{Stock: pb}, nil
}

// ListMaterialStock returns the warehouse list (materials joined with balance + valuation).
func (s *Server) ListMaterialStock(ctx context.Context, req *pb_admin.ListMaterialStockRequest) (*pb_admin.ListMaterialStockResponse, error) {
	rows, err := s.repo.MaterialStock().ListMaterialStock(ctx, entity.MaterialStockFilter{
		Section:       req.GetSection(),
		Query:         req.GetQ(),
		WithStockOnly: req.GetWithStockOnly(),
		BelowMinOnly:  req.GetBelowMinOnly(),
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list material stock", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list material stock")
	}
	read, _ := s.costingAccess(ctx)
	base := cache.GetBaseCurrency()
	out := make([]*pb_common.MaterialStockRow, 0, len(rows))
	for _, r := range rows {
		pb := dto.ConvertEntityMaterialStockRowToPb(r, base)
		if !read {
			stripMaterialStockRowCosting(pb)
		}
		out = append(out, pb)
	}
	return &pb_admin.ListMaterialStockResponse{Rows: out}, nil
}

// ListMaterialMovements returns the movement ledger.
func (s *Server) ListMaterialMovements(ctx context.Context, req *pb_admin.ListMaterialMovementsRequest) (*pb_admin.ListMaterialMovementsResponse, error) {
	// The date bounds go into a SQL DATE() comparison — a malformed value would surface as a DB
	// error (500) instead of a clean InvalidArgument (g25-15).
	for _, b := range []struct{ name, v string }{
		{"occurred_from", req.GetOccurredFrom()},
		{"occurred_to", req.GetOccurredTo()},
	} {
		if b.v == "" {
			continue
		}
		if _, err := time.Parse("2006-01-02", b.v); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "%s must be a YYYY-MM-DD date", b.name)
		}
	}
	movements, total, err := s.repo.MaterialStock().ListMaterialMovements(ctx, int(req.GetLimit()), int(req.GetOffset()), entity.MaterialMovementFilter{
		MaterialId:      int(req.GetMaterialId()),
		ProductionRunId: int(req.GetProductionRunId()),
		SampleId:        int(req.GetSampleId()),
		MovementType:    dto.MaterialMovementTypeFromPb(req.GetMovementType()),
		OccurredFrom:    req.GetOccurredFrom(),
		OccurredTo:      req.GetOccurredTo(),
	})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list material movements", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list material movements")
	}
	read, _ := s.costingAccess(ctx)
	out := make([]*pb_common.MaterialMovement, 0, len(movements))
	for _, m := range movements {
		pb := dto.ConvertEntityMaterialMovementToPb(m)
		if !read {
			stripMaterialMovementCosting(pb)
		}
		out = append(out, pb)
	}
	return &pb_admin.ListMaterialMovementsResponse{Movements: out, Total: int32(total)}, nil
}

// UpsertPackagingBom full-replaces the global packaging recipe consumed on ship (gap-07 v2 B).
func (s *Server) UpsertPackagingBom(ctx context.Context, req *pb_admin.UpsertPackagingBomRequest) (*pb_admin.UpsertPackagingBomResponse, error) {
	items, err := dto.ConvertPbPackagingBomToEntity(req.GetItems())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.repo.MaterialStock().UpsertPackagingBom(ctx, items); err != nil {
		return nil, mapInventoryErr(ctx, "upsert packaging bom", err)
	}
	return &pb_admin.UpsertPackagingBomResponse{}, nil
}

// ListPackagingBom returns the packaging recipe (material name/unit + per-order/per-item quantities).
func (s *Server) ListPackagingBom(ctx context.Context, _ *pb_admin.ListPackagingBomRequest) (*pb_admin.ListPackagingBomResponse, error) {
	items, err := s.repo.MaterialStock().ListPackagingBom(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list packaging bom", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list packaging bom")
	}
	return &pb_admin.ListPackagingBomResponse{Items: dto.PackagingBomListToPb(items)}, nil
}

// ListMaterialLots returns a material's structured lots / rolls (gap-07 v2 D). The lot's unit_cost is
// confidential and stripped without costing:read.
func (s *Server) ListMaterialLots(ctx context.Context, req *pb_admin.ListMaterialLotsRequest) (*pb_admin.ListMaterialLotsResponse, error) {
	if req.GetMaterialId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "material_id is required")
	}
	lots, err := s.repo.MaterialStock().ListMaterialLots(ctx, int(req.GetMaterialId()), req.GetIncludeArchived())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list material lots", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list material lots")
	}
	out := dto.MaterialLotListToPb(lots)
	if read, _ := s.costingAccess(ctx); !read {
		for _, l := range out {
			l.UnitCost = nil
		}
	}
	return &pb_admin.ListMaterialLotsResponse{Lots: out}, nil
}

// movementToPb converts a movement, stripping confidential cost fields without costing:read.
func (s *Server) movementToPb(ctx context.Context, m entity.MaterialMovement) *pb_common.MaterialMovement {
	pb := dto.ConvertEntityMaterialMovementToPb(m)
	if read, _ := s.costingAccess(ctx); !read {
		stripMaterialMovementCosting(pb)
	}
	return pb
}

// mapInventoryErr maps warehouse store errors to gRPC codes: guarded conditions become
// FailedPrecondition/InvalidArgument; anything else is a logged Internal.
func mapInventoryErr(ctx context.Context, what string, err error) error {
	switch {
	case errors.Is(err, entity.ErrInsufficientMaterialStock):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrExcessiveMaterialReturn):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrMaterialArchived):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrMaterialIssueTargetInvalid):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrMaterialNotFound):
		return status.Error(codes.InvalidArgument, err.Error())
	}
	slog.Default().ErrorContext(ctx, "can't "+what, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+what)
}
