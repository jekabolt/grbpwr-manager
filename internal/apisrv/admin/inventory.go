package admin

import (
	"context"
	"errors"
	"log/slog"

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
	movements, total, err := s.repo.MaterialStock().ListMaterialMovements(ctx, int(req.GetLimit()), int(req.GetOffset()), entity.MaterialMovementFilter{
		MaterialId:      int(req.GetMaterialId()),
		ProductionRunId: int(req.GetProductionRunId()),
		SampleId:        int(req.GetSampleId()),
		MovementType:    dto.MaterialMovementTypeFromPb(req.GetMovementType()),
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
	case errors.Is(err, entity.ErrMaterialArchived):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrMaterialIssueTargetInvalid):
		return status.Error(codes.FailedPrecondition, err.Error())
	}
	slog.Default().ErrorContext(ctx, "can't "+what, slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+what)
}
