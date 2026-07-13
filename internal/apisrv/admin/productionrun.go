package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// productionRunFKMsg is returned when a run references a missing tech card, release or size.
const productionRunFKMsg = "production run references a non-existent tech card, release or size"

// CreateProductionRun creates a run and snapshots its planned unit cost.
func (s *Server) CreateProductionRun(ctx context.Context, req *pb_admin.CreateProductionRunRequest) (*pb_admin.CreateProductionRunResponse, error) {
	ins, err := dto.ConvertPbProductionRunInsertToEntity(req.GetRun())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if err := s.snapshotPlannedCost(ctx, ins); err != nil {
		return nil, err
	}
	if len(ins.Costs) > 0 {
		dto.FoldProductionRunCostsToBase(ins.Costs, s.costingFx(ctx))
	}
	id, err := s.repo.ProductionRuns().CreateProductionRun(ctx, ins)
	if err != nil {
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't create production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create production run")
	}
	return &pb_admin.CreateProductionRunResponse{Id: int32(id)}, nil
}

// UpdateProductionRun updates a run's header and size grid. The planned-cost snapshot is frozen
// at plan time and is never re-taken here.
func (s *Server) UpdateProductionRun(ctx context.Context, req *pb_admin.UpdateProductionRunRequest) (*pb_admin.UpdateProductionRunResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "production run id is required")
	}
	ins, err := dto.ConvertPbProductionRunInsertToEntity(req.GetRun())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	if len(ins.Costs) > 0 {
		dto.FoldProductionRunCostsToBase(ins.Costs, s.costingFx(ctx))
	}
	if err := s.repo.ProductionRuns().UpdateProductionRun(ctx, int(req.Id), ins); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't update production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't update production run")
	}
	return &pb_admin.UpdateProductionRunResponse{}, nil
}

// DeleteProductionRun deletes a run (size grid cascades).
func (s *Server) DeleteProductionRun(ctx context.Context, req *pb_admin.DeleteProductionRunRequest) (*pb_admin.DeleteProductionRunResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "production run id is required")
	}
	if err := s.repo.ProductionRuns().DeleteProductionRun(ctx, int(req.Id)); err != nil {
		slog.Default().ErrorContext(ctx, "can't delete production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't delete production run")
	}
	return &pb_admin.DeleteProductionRunResponse{}, nil
}

// GetProductionRun returns a run with its size grid.
func (s *Server) GetProductionRun(ctx context.Context, req *pb_admin.GetProductionRunRequest) (*pb_admin.GetProductionRunResponse, error) {
	if req.Id <= 0 {
		return nil, status.Error(codes.InvalidArgument, "production run id is required")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.Id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't get production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get production run")
	}
	return &pb_admin.GetProductionRunResponse{Run: dto.ConvertEntityProductionRunToPb(run)}, nil
}

// ListProductionRuns returns runs matching the optional tech-card / status filter, newest-first.
func (s *Server) ListProductionRuns(ctx context.Context, req *pb_admin.ListProductionRunsRequest) (*pb_admin.ListProductionRunsResponse, error) {
	st, err := dto.NormalizeProductionRunStatusFilter(req.Status)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	runs, total, err := s.repo.ProductionRuns().ListProductionRuns(ctx, int(req.Limit), int(req.Offset),
		entity.ProductionRunListFilter{TechCardId: int(req.TechCardId), Status: st})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list production runs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list production runs")
	}
	out := make([]*pb_common.ProductionRun, 0, len(runs))
	for i := range runs {
		out = append(out, dto.ConvertEntityProductionRunToPb(&runs[i]))
	}
	return &pb_admin.ListProductionRunsResponse{Runs: out, Total: int32(total)}, nil
}

// snapshotPlannedCost freezes the run's planned unit cost at plan time: from the linked
// tech_card_release (task 11) when one is given, otherwise from the live tech card's computed
// costing. A missing tech card is rejected up front (rather than surfacing as an FK error); a
// costing that cannot be folded to base leaves the snapshot null (the run still saves).
func (s *Server) snapshotPlannedCost(ctx context.Context, ins *entity.ProductionRunInsert) error {
	if ins.ReleaseId.Valid {
		rel, err := s.repo.TechCards().GetTechCardRelease(ctx, int(ins.ReleaseId.Int64))
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return status.Error(codes.InvalidArgument, "release_id does not exist")
			}
			slog.Default().ErrorContext(ctx, "can't load release for planned cost", slog.String("err", err.Error()))
			return status.Error(codes.Internal, "can't load release")
		}
		ins.PlannedUnitCost = rel.UnitCost
		ins.PlannedCurrency = rel.Currency
		return nil
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, ins.TechCardId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return status.Error(codes.InvalidArgument, "tech_card_id does not exist")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card for planned cost", slog.String("err", err.Error()))
		return status.Error(codes.Internal, "can't load tech card")
	}
	unit, currency := dto.ComputeTechCardUnitCost(card, s.costingFx(ctx))
	ins.PlannedUnitCost = unit
	if unit.Valid && currency != "" {
		ins.PlannedCurrency = sql.NullString{String: currency, Valid: true}
	}
	return nil
}
