package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"slices"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
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
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		if errors.Is(err, entity.ErrProductionRunReceivedImmutable) || errors.Is(err, entity.ErrProductionRunHasMovements) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		}
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

// ReceiveProductionRun receives a multi-colourway run into stock and transitions it to `received`,
// optionally seeding each received product's cost_price from the run's actual unit cost. The run is
// multi-product now: every line's received_qty is booked into that line's own product. Each such
// product must be linked to the run's tech card and at least one line must carry a received qty.
func (s *Server) ReceiveProductionRun(ctx context.Context, req *pb_admin.ReceiveProductionRunRequest) (*pb_admin.ReceiveProductionRunResponse, error) {
	if req.RunId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for receive", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}
	if run.Status == entity.ProductionRunReceived || run.Status == entity.ProductionRunClosed {
		return nil, status.Error(codes.FailedPrecondition, "production run has already been received")
	}
	// every received product must be linked to the run's tech card.
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load tech card for receive", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	// NF-07: an auxiliary card's output is received into the material warehouse, not product stock.
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		return s.receiveAuxiliaryRun(ctx, run, card)
	}
	// group each line's received quantity by product → size. (run_id, product_id, size_id) is unique
	// so no accumulation collisions; a received line without a product, or with a product not in the
	// card, is a precondition failure.
	perProduct := make(map[int]map[int]int)
	for _, ln := range run.Lines {
		if !ln.ReceivedQty.Valid || ln.ReceivedQty.Int64 <= 0 {
			continue
		}
		if !ln.ProductId.Valid {
			return nil, status.Error(codes.FailedPrecondition, entity.ErrProductionRunLineProductMissing.Error())
		}
		pid := int(ln.ProductId.Int32)
		if !slices.Contains(card.ProductIds, pid) {
			return nil, status.Error(codes.InvalidArgument, "a received line's product is not linked to the run's tech card")
		}
		if perProduct[pid] == nil {
			perProduct[pid] = make(map[int]int)
		}
		perProduct[pid][ln.SizeId] = int(ln.ReceivedQty.Int64)
	}
	if len(perProduct) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "run has no received quantities; set received_qty on the lines first")
	}
	// optional cost_price update from the run's (base-currency) actual unit cost — one style-level
	// figure applied to every received product.
	var costPrice decimal.NullDecimal
	if req.UpdateCostPrice {
		costPrice = dto.ProductionRunActualUnitCostBase(run)
	}
	if err := s.repo.ProductionRuns().ReceiveProductionRun(ctx, int(req.RunId), perProduct, authsrv.GetAdminUsername(ctx), costPrice); err != nil {
		if errors.Is(err, entity.ErrProductionRunAlreadyReceived) {
			return nil, status.Error(codes.FailedPrecondition, "production run has already been received")
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't receive production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't receive production run")
	}
	return &pb_admin.ReceiveProductionRunResponse{CostPriceUpdated: costPrice.Valid}, nil
}

// receiveAuxiliaryRun receives an auxiliary card's run output into its output material (NF-07): the
// finished item (dust bag, shopper) lands in the warehouse as a receipt_production whose unit cost
// is the run's actual per-unit base cost, so the packaging's stock value reflects real production
// cost. Auxiliary lines must not link products, and the card must have declared an output material.
func (s *Server) receiveAuxiliaryRun(ctx context.Context, run *entity.ProductionRun, card *entity.TechCard) (*pb_admin.ReceiveProductionRunResponse, error) {
	if !card.OutputMaterialId.Valid {
		return nil, status.Error(codes.FailedPrecondition, "auxiliary card has no output material set; set it before receiving")
	}
	var total int64
	for _, ln := range run.Lines {
		if ln.ProductId.Valid {
			return nil, status.Error(codes.InvalidArgument, "auxiliary run lines cannot link products")
		}
		if ln.ReceivedQty.Valid && ln.ReceivedQty.Int64 > 0 {
			total += ln.ReceivedQty.Int64
		}
	}
	if total == 0 {
		return nil, status.Error(codes.FailedPrecondition, "run has no received quantities; set received_qty on the lines first")
	}
	// per-unit base cost from the run actuals (manual costs + materials-from-stock); invalid when
	// the actuals have no base — the receipt is then uncosted and the average is left unmoved.
	unitCost := dto.ProductionRunActualUnitCostBase(run)
	if err := s.repo.ProductionRuns().ReceiveAuxiliaryProductionRun(ctx, run.Id, int(card.OutputMaterialId.Int64),
		decimal.NewFromInt(total), unitCost, authsrv.GetAdminUsername(ctx)); err != nil {
		if errors.Is(err, entity.ErrProductionRunAlreadyReceived) {
			return nil, status.Error(codes.FailedPrecondition, "production run has already been received")
		}
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't receive auxiliary production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't receive auxiliary production run")
	}
	// no product cost_price for an auxiliary run (the material average moved instead).
	return &pb_admin.ReceiveProductionRunResponse{CostPriceUpdated: false}, nil
}

// GetProductionRunMaterialPlan estimates the run's material requirement from its lines' colourway
// norms against on-hand and already-issued stock (NF-06 §6.2). Read-only; writes nothing.
func (s *Server) GetProductionRunMaterialPlan(ctx context.Context, req *pb_admin.GetProductionRunMaterialPlanRequest) (*pb_admin.GetProductionRunMaterialPlanResponse, error) {
	if req.RunId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for material plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card for material plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	// on-hand for every catalog material referenced by the card's BOM (a bounded, small set).
	onHand := make(map[int]decimal.Decimal)
	for i := range card.BomItems {
		b := &card.BomItems[i]
		if !b.MaterialId.Valid {
			continue
		}
		mid := int(b.MaterialId.Int64)
		if _, done := onHand[mid]; done {
			continue
		}
		st, err := s.repo.MaterialStock().GetMaterialStock(ctx, mid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				onHand[mid] = decimal.Zero // never received → no stock row yet
				continue
			}
			slog.Default().ErrorContext(ctx, "can't load material stock for material plan", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't load material stock")
		}
		onHand[mid] = st.OnHand
	}
	issued := dto.AggregateRunMaterialIssues(run.MaterialMovements)
	return dto.ComputeProductionRunMaterialPlan(run, card, onHand, issued), nil
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
