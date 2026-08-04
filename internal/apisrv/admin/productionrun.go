package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// productionRunFKMsg is returned when a run references a missing tech card, release or size.
const productionRunFKMsg = "production run references a non-existent tech card, release or size"

// productionRunCostWriteMsg is returned when a run write carries cost articles without costing:write.
const productionRunCostWriteMsg = "costing:write is required to set production run cost articles"

// CreateProductionRun creates a run and snapshots its planned unit cost.
func (s *Server) CreateProductionRun(ctx context.Context, req *pb_admin.CreateProductionRunRequest) (*pb_admin.CreateProductionRunResponse, error) {
	if _, write := s.costingAccess(ctx); !write && productionRunInsertHasCostingData(req.GetRun()) {
		return nil, status.Error(codes.PermissionDenied, productionRunCostWriteMsg)
	}
	ins, err := dto.ConvertPbProductionRunInsertToEntity(req.GetRun())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// A run is born planned/in_progress. Creating it straight into received/closed would mark stock
	// as booked that never was (bypassing ReceiveProductionRun) and — since received/closed runs are
	// immutable for update AND delete — leave a permanently stuck row (g25-01); cancelled makes no
	// sense at birth either.
	if ins.Status != entity.ProductionRunPlanned && ins.Status != entity.ProductionRunInProgress {
		return nil, status.Error(codes.InvalidArgument, "a production run is created as planned or in_progress; received/closed/cancelled are reached through their flows")
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
	_, costingWrite := s.costingAccess(ctx)
	if !costingWrite && productionRunInsertHasCostingData(req.GetRun()) {
		return nil, status.Error(codes.PermissionDenied, productionRunCostWriteMsg)
	}
	ins, err := dto.ConvertPbProductionRunInsertToEntity(req.GetRun())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// Costs are handed to the store UNFOLDED: the store first carries each unchanged article's stored
	// amount_base over (under the run lock), then folds only what is genuinely new or changed. Folding
	// here would mark every base Valid and turn that preservation into a no-op.
	// The cost-blind path reloads and preserves stored articles under the run's FOR UPDATE lock. Its
	// read is load-bearing: any failure aborts before the store's full-replace can delete cost rows.
	var updateErr error
	if costingWrite {
		updateErr = s.repo.ProductionRuns().UpdateProductionRun(ctx, int(req.Id), ins, int(req.ExpectedLockVersion), s.costingFx(ctx))
	} else {
		updateErr = s.repo.ProductionRuns().UpdateProductionRunPreservingCosts(
			ctx, int(req.Id), ins, int(req.ExpectedLockVersion))
	}
	if err := updateErr; err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		// A stale expected_lock_version means a concurrent edit — Aborted tells the client to reload and
		// retry (mirrors UpdateTechCard) (#9).
		if errors.Is(err, entity.ErrProductionRunConflict) {
			return nil, status.Error(codes.Aborted, "production run was modified concurrently; reload and retry")
		}
		// A received/closed run is immutable; receive must go through ReceiveProductionRun; moving an
		// open run to cancelled/closed while material is still issued to it would strand that stock
		// outside WIP with no receive or write-off (nf09-03); and a run never moves to another tech
		// card (g25-13) — all are caller-fixable preconditions.
		if errors.Is(err, entity.ErrProductionRunReceivedImmutable) ||
			errors.Is(err, entity.ErrProductionRunReceiveViaUpdate) ||
			errors.Is(err, entity.ErrProductionRunHasOpenIssues) ||
			errors.Is(err, entity.ErrProductionRunCardChange) {
			return nil, status.Error(codes.FailedPrecondition, err.Error())
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
		if errors.Is(err, entity.ErrProductionRunReceivedImmutable) || errors.Is(err, entity.ErrProductionRunHasMovements) ||
			errors.Is(err, entity.ErrProductionRunHasReceiptHistory) {
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
	pb := dto.ConvertEntityProductionRunToPb(run)
	if read, _ := s.costingAccess(ctx); !read {
		stripProductionRunCosting(pb)
	}
	return &pb_admin.GetProductionRunResponse{Run: pb}, nil
}

// ListProductionRuns returns runs matching the optional tech-card / status filter, newest-first.
func (s *Server) ListProductionRuns(ctx context.Context, req *pb_admin.ListProductionRunsRequest) (*pb_admin.ListProductionRunsResponse, error) {
	st, err := dto.NormalizeProductionRunStatusFilter(req.Status)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	runs, total, err := s.repo.ProductionRuns().ListProductionRuns(ctx, int(req.Limit), int(req.Offset),
		entity.ProductionRunListFilter{
			TechCardId:  int(req.TechCardId),
			Status:      st,
			StaleDays:   int(req.StaleDays),
			OverdueOnly: req.OverdueOnly,
		})
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list production runs", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list production runs")
	}
	read, _ := s.costingAccess(ctx)
	out := make([]*pb_common.ProductionRun, 0, len(runs))
	for i := range runs {
		pb := dto.ConvertEntityProductionRunToPb(&runs[i])
		if !read {
			stripProductionRunCosting(pb)
		}
		out = append(out, pb)
	}
	return &pb_admin.ListProductionRunsResponse{Runs: out, Total: int32(total)}, nil
}

// productsWriteAccess reports whether the caller may move sellable product stock (products:write).
// The receipt command books units straight into product_size — the same surface UpsertProduct's
// stock edits gate behind the products section — so production:write alone is not enough (plan 05
// amendment 6). Fails closed without an authz in context, mirroring costingAccessFor.
func productsWriteAccess(ctx context.Context) bool {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return false
	}
	if az.FullAccess() {
		return true
	}
	lvl, ok := az.Perms[rbac.SectionProducts]
	return ok && lvl.Covers(entity.AccessWrite)
}

// PostProductionRunReceipt is the atomic receiving command (Phase 4, receipt v1). See the proto
// contract for semantics; this handler validates shape + permissions + tech-card linkage and hands
// the store one transaction to execute.
func (s *Server) PostProductionRunReceipt(ctx context.Context, req *pb_admin.PostProductionRunReceiptRequest) (*pb_admin.PostProductionRunReceiptResponse, error) {
	if req.RunId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if !entity.IsValidProductionRunLineKey(key) {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key must be exactly 26 characters of [0-9A-Z] (mint an uppercase ULID per user intent)")
	}
	// The LEGACY prefix is reserved for migration backfills (0231 keys legacy receipts LEGACY<id>
	// and re-runs graft plan-grid lines onto receipts under that family). A Crockford ULID can
	// never start with 'L', so no real client is affected — only a crafted key is refused.
	if strings.HasPrefix(key, "LEGACY") {
		return nil, status.Error(codes.InvalidArgument, "idempotency_key prefix LEGACY is reserved for migration backfills")
	}
	lines, err := dto.ConvertPbReceiptLinesToEntity(req.Lines)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	// An empty FINAL is the short-close of a partially received run (the store validates the
	// status); an empty PARTIAL books nothing and means nothing.
	if len(lines) == 0 && req.Partial {
		return nil, status.Error(codes.InvalidArgument, "at least one receipt line is required for a partial receipt")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for receipt", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}
	result, st := s.executeRunReceipt(ctx, run, lines, key, int(req.ExpectedLockVersion),
		strings.TrimSpace(req.Note), req.UpdateCostPrice, !req.Partial, false)
	if st != nil {
		return nil, st
	}
	resp := &pb_admin.PostProductionRunReceiptResponse{
		ReceiptId:        int32(result.ReceiptID),
		CostPriceUpdated: result.CostPriceUpdated,
		Replayed:         result.Replayed,
	}
	// Echo the post-command run so the client renders the received state without a second round
	// trip. Best-effort: the receipt is committed; a read failure here must not fail the command.
	if run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId)); err == nil {
		pb := dto.ConvertEntityProductionRunToPb(run)
		if read, _ := s.costingAccess(ctx); !read {
			stripProductionRunCosting(pb)
		}
		resp.Run = pb
	} else {
		slog.Default().ErrorContext(ctx, "can't reload production run after receipt", slog.String("err", err.Error()))
	}
	return resp, nil
}

// ReverseProductionRunReceipt undoes one receipt of a run (Phase 6, plan 05). The handler owns
// RBAC (production:write via the interceptor, products:write and costing:write here), the aux
// refusal, and the tech-card reseed figures; every stateful precondition lives in the store under
// the run lock. See the proto contract for full semantics.
func (s *Server) ReverseProductionRunReceipt(ctx context.Context, req *pb_admin.ReverseProductionRunReceiptRequest) (*pb_admin.ReverseProductionRunReceiptResponse, error) {
	if req.RunId <= 0 || req.ReceiptId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id and receipt_id are required")
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		return nil, status.Error(codes.InvalidArgument, "a non-empty reason is required to reverse a receipt")
	}
	if !productsWriteAccess(ctx) {
		return nil, status.Error(codes.PermissionDenied, "products:write is required to reverse booked production stock (re-login if the permission was just granted)")
	}
	// The reversal can rewrite cost_price (rolling the run's claim back to the card estimate), so
	// costing:write is unconditional — unlike receive, there is no flag to opt out of the money side.
	if _, write := s.costingAccess(ctx); !write {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to reverse a receipt (it rolls back cost_price)")
	}
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for reversal", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load tech card for reversal", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		return nil, status.Error(codes.FailedPrecondition, entity.ErrProductionRunReversalAux.Error())
	}
	// Tech-card estimates for every product the receipt might have stocked (the card's linked
	// set is small and bounded) — the store applies them only to products whose cost_price this
	// run still claims. Same per-colourway computation as the receive-time seed; a colourway that
	// cannot be costed (or is not in base currency) stays absent from the map → the claim clears
	// to honestly-unknown NULL instead of borrowing a wrong figure.
	fx := s.costingFx(ctx)
	base := cache.GetBaseCurrency()
	reseed := make(map[int]entity.ProductCostReseed)
	for _, pid := range card.LinkedProductIDs() {
		unit, currency := dto.ComputeColorwayUnitCost(card, pid, fx)
		if !unit.Valid || !strings.EqualFold(currency, base) {
			continue
		}
		est := entity.ProductCostReseed{Cost: decimal.NullDecimal{Decimal: unit.Decimal, Valid: true}}
		if bd, ok := dto.ComputeColorwayCostBreakdownBase(card, pid, fx); ok {
			if b, merr := json.Marshal(bd); merr == nil {
				est.Breakdown = sql.NullString{String: string(b), Valid: true}
			}
		}
		reseed[pid] = est
	}
	result, err := s.repo.ProductionRuns().ReverseProductionRunReceipt(ctx, entity.ReverseProductionRunReceiptParams{
		RunID:               int(req.RunId),
		ReceiptID:           int(req.ReceiptId),
		Reason:              reason,
		ExpectedLockVersion: int(req.ExpectedLockVersion),
		Username:            authsrv.GetAdminUsername(ctx),
		CardID:              card.Id,
		Reseed:              reseed,
	})
	if err != nil {
		var shortErr *entity.ProductionRunReversalShortfallError
		switch {
		case errors.As(err, &shortErr):
			return nil, status.Error(codes.FailedPrecondition, shortErr.Error())
		case errors.Is(err, entity.ErrProductionRunReceiptAlreadyReversed),
			errors.Is(err, entity.ErrProductionRunReversalOfReversal),
			errors.Is(err, entity.ErrProductionRunReversalClosedRun),
			errors.Is(err, entity.ErrProductionRunReversalPeriodClosed),
			errors.Is(err, entity.ErrProductionRunReversalFinalFirst):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case errors.Is(err, entity.ErrProductionRunReceiptNotFound):
			return nil, status.Error(codes.NotFound, err.Error())
		case errors.Is(err, entity.ErrProductionRunConflict):
			return nil, status.Error(codes.Aborted, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't reverse production run receipt", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't reverse production run receipt")
	}
	resp := &pb_admin.ReverseProductionRunReceiptResponse{ReversalReceiptId: int32(result.ReversalReceiptID)}
	// Echo the post-command run, same best-effort contract as the receipt command.
	if run, err := s.repo.ProductionRuns().GetProductionRun(ctx, int(req.RunId)); err == nil {
		pb := dto.ConvertEntityProductionRunToPb(run)
		if read, _ := s.costingAccess(ctx); !read {
			stripProductionRunCosting(pb)
		}
		resp.Run = pb
	} else {
		slog.Default().ErrorContext(ctx, "can't reload production run after reversal", slog.String("err", err.Error()))
	}
	return resp, nil
}

// ReceiveProductionRun receives a run using its STORED received_qty/defect_qty counts. DEPRECATED
// (Phase 4): a thin shim over the receipt command, kept one release for old clients that stamp
// counts via UpdateProductionRun first. It mints a server-side idempotency key per call — replay
// protection degrades to the run's own already-received guard, exactly the old behaviour.
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
	// A partially received run's line counters are CUMULATIVE rollups over already-booked receipts
	// (Phase 5) — synthesizing "one final receipt" from them would book every already-received unit
	// a second time. Only the receipt command knows how to close such a series.
	if run.Status == entity.ProductionRunPartiallyReceived {
		return nil, status.Error(codes.FailedPrecondition,
			"this run has partial receipts; finish it through PostProductionRunReceipt (the new receive flow)")
	}
	// Synthesize the receipt lines from the stored counts (the old client's step-1 update wrote
	// them). Lines with no count carry no receipt fact.
	lines := make([]entity.ProductionRunReceiptLineInput, 0, len(run.Lines))
	for _, ln := range run.Lines {
		good := int(ln.ReceivedQty.Int64)
		defect := int(ln.DefectQty.Int64)
		if good <= 0 && defect <= 0 {
			continue
		}
		lines = append(lines, entity.ProductionRunReceiptLineInput{
			LineKey: ln.LineKey, GoodQty: good, DefectQty: defect,
		})
	}
	if len(lines) == 0 {
		return nil, status.Error(codes.FailedPrecondition, "run has no received quantities; set received_qty on the lines first")
	}
	key, err := entity.MintProductionRunLineKey()
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't mint receipt idempotency key", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't receive production run")
	}
	// The shim always synthesizes a FINAL receipt — the pre-receipt flow had no partial concept.
	// The shim's lines ARE the stored totals — LegacyTotals stops the rollup write from adding
	// them to themselves (the Phase 5 accumulate regression halved every shim unit cost).
	result, st := s.executeRunReceipt(ctx, run, lines, key, 0, "", req.UpdateCostPrice, true, true)
	if st != nil {
		return nil, st
	}
	return &pb_admin.ReceiveProductionRunResponse{CostPriceUpdated: result.CostPriceUpdated}, nil
}

// executeRunReceipt is the shared core of both receive RPCs: permission gates, tech-card linkage
// validation (aux detection included), and the store command. Returns a gRPC status error mapped
// from the command's outcome.
func (s *Server) executeRunReceipt(ctx context.Context, run *entity.ProductionRun, lines []entity.ProductionRunReceiptLineInput,
	idempotencyKey string, expectedLockVersion int, note string, updateCostPrice, final, legacyTotals bool) (*entity.PostProductionRunReceiptResult, error) {
	runID := run.Id
	// Moving sellable stock needs products:write on top of production:write (the RBAC interceptor
	// gate). An account granted the permission after login must re-login — permissions ride in the JWT.
	if !productsWriteAccess(ctx) {
		return nil, status.Error(codes.PermissionDenied, "products:write is required to book production stock (re-login if the permission was just granted)")
	}
	// update_cost_price seeds every received product's cost_price from the run's actual unit cost —
	// a confidential figure written into the margin chain, so it needs costing:write on top.
	// Rejected rather than silently ignored; receiving without the flag stays open to a warehouse role.
	if _, write := s.costingAccess(ctx); !write && updateCostPrice {
		return nil, status.Error(codes.PermissionDenied, "costing:write is required to seed product cost_price from a run; receive with update_cost_price=false")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load tech card for receive", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}
	params := entity.PostProductionRunReceiptParams{
		RunID:               runID,
		Lines:               lines,
		IdempotencyKey:      idempotencyKey,
		RequestHash:         dto.HashProductionRunReceiptPayload(runID, lines, note, updateCostPrice, final),
		ExpectedLockVersion: expectedLockVersion,
		Note:                note,
		UpdateCostPrice:     updateCostPrice,
		Username:            authsrv.GetAdminUsername(ctx),
		BaseCurrency:        cache.GetBaseCurrency(),
		Final:               final,
		LegacyTotals:        legacyTotals,
	}
	// NF-07: an auxiliary card's output is received into the material warehouse, not product stock.
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		if !card.OutputMaterialId.Valid {
			return nil, status.Error(codes.FailedPrecondition, "auxiliary card has no output material set; set it before receiving")
		}
		params.Aux = true
		params.OutputMaterialID = int(card.OutputMaterialId.Int64)
	} else {
		// The card's product/size linkage travels INTO the transaction: the store re-validates the
		// fresh plan lines against these sets under the run lock, so a racing line edit cannot book
		// stock into a product this handler never saw.
		linkedProducts := card.LinkedProductIDs()
		validProduct := make(map[int]bool, len(linkedProducts))
		for _, id := range linkedProducts {
			validProduct[id] = true
		}
		validSize := make(map[int]bool, len(card.SizeIds))
		for _, id := range card.SizeIds {
			validSize[id] = true
		}
		params.ValidProducts = validProduct
		params.ValidSizes = validSize
	}
	result, err := s.repo.ProductionRuns().PostProductionRunReceipt(ctx, params)
	if err != nil {
		switch {
		case errors.Is(err, entity.ErrProductionRunAlreadyReceived):
			return nil, status.Error(codes.FailedPrecondition, "production run has already been received")
		case errors.Is(err, entity.ErrProductionRunCancelledReceive),
			errors.Is(err, entity.ErrProductionRunLineProductMissing),
			errors.Is(err, entity.ErrProductionRunNothingReceived):
			return nil, status.Error(codes.FailedPrecondition, err.Error())
		case errors.Is(err, entity.ErrProductionRunConflict),
			errors.Is(err, entity.ErrProductionRunConcurrentModification):
			return nil, status.Error(codes.Aborted, err.Error())
		case errors.Is(err, entity.ErrProductionRunReceiptLineUnknown),
			errors.Is(err, entity.ErrProductionRunLineProductUnlinked),
			errors.Is(err, entity.ErrProductionRunLineSizeUnlinked):
			return nil, status.Error(codes.InvalidArgument, err.Error())
		case errors.Is(err, entity.ErrIdempotencyConflict):
			return nil, status.Error(codes.AlreadyExists, err.Error())
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Error(codes.NotFound, "production run not found")
		case s.repo.IsErrForeignKeyViolation(err):
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't post production run receipt", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't post production run receipt")
	}
	return result, nil
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
	// on-hand + identity for the UNION of articles the plan may resolve: BOM slot defaults,
	// colourway pins, AND materials already issued to the run (a bounded, small set). A pinned
	// article that is not also some slot's default previously had no on-hand key, so the plan
	// reported 100% shortage on it while the shelf held plenty; an issued-but-no-longer-required
	// material without identity rendered as "material #id" with a false zero on hand.
	issued := dto.AggregateRunMaterialIssues(run.MaterialMovements)
	matIDs := make([]int, 0, len(card.BomItems)+len(issued))
	seen := map[int]bool{}
	addID := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			matIDs = append(matIDs, id)
		}
	}
	for i := range card.BomItems {
		if card.BomItems[i].MaterialId.Valid {
			addID(int(card.BomItems[i].MaterialId.Int64))
		}
	}
	for i := range card.Colorways {
		for j := range card.Colorways[i].Usages {
			if u := &card.Colorways[i].Usages[j]; u.MaterialId.Valid {
				addID(int(u.MaterialId.Int64))
			}
		}
	}
	issuedIDs := make([]int, 0, len(issued))
	for mid := range issued {
		issuedIDs = append(issuedIDs, mid)
	}
	sort.Ints(issuedIDs)
	for _, mid := range issuedIDs {
		addID(mid)
	}
	linked := card.LinkedMaterials
	if linked == nil {
		linked = make(map[int]entity.MaterialWithPrice, len(matIDs))
	}
	onHand := make(map[int]decimal.Decimal, len(matIDs))
	for _, mid := range matIDs {
		if _, ok := linked[mid]; !ok {
			// Best-effort identity fetch for labels/units; a miss degrades to slot snapshots.
			if m, merr := s.repo.TechCards().GetMaterial(ctx, mid); merr == nil && m != nil {
				linked[mid] = *m
			}
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
	return dto.ComputeProductionRunMaterialPlan(run, card, onHand, issued, linked), nil
}

// snapshotPlannedCost freezes the run's planned unit cost at plan time: from the linked
// tech_card_release (task 11) when one is given, otherwise from the live tech card's computed
// costing. A missing tech card is rejected up front (rather than surfacing as an FK error); a
// costing that cannot be folded to base leaves the snapshot null (the run still saves).
//
// The snapshot is stored ONLY when it is in the base currency. Actual cost is always base, so a
// costing-currency plan beside it produces a variance that is pure FX; NULL is the honest value and
// every read path already tolerates it (a run with no plan simply reports no variance).
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
		setPlannedCostIfBase(ins, rel.UnitCost, rel.Currency.String)
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
	// Snapshot from the live card, using the run's ACTUAL cutting wastage when set (it overrides every
	// BOM line's estimate for this run's plan) — unset falls back to each line's BOM estimate. This
	// keeps plan-vs-actual honest about the run's real marker/lay efficiency (the actuals side is
	// measured from material issues). The release path above is a frozen scalar and is left as-is.
	unit, currency := dto.ComputeTechCardUnitCostWithWastage(card, s.costingFx(ctx), ins.ActualWastagePercent)
	setPlannedCostIfBase(ins, unit, currency)
	return nil
}

// setPlannedCostIfBase writes the planned-cost snapshot only when it is denominated in the base
// currency; anything else (including a figure with no currency recorded) leaves both columns NULL.
func setPlannedCostIfBase(ins *entity.ProductionRunInsert, unit decimal.NullDecimal, currency string) {
	if !unit.Valid || !strings.EqualFold(strings.TrimSpace(currency), cache.GetBaseCurrency()) {
		ins.PlannedUnitCost = decimal.NullDecimal{}
		ins.PlannedCurrency = sql.NullString{}
		return
	}
	ins.PlannedUnitCost = unit
	ins.PlannedCurrency = sql.NullString{String: currency, Valid: true}
}
