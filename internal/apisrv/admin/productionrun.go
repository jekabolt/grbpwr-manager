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
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// productionRunFKMsg is returned when a run references a missing tech card, release or size.
const productionRunFKMsg = "production run references a non-existent tech card, release or size"

// productionRunCostWriteMsg is returned when a run write carries cost articles without costing:write.
const productionRunCostWriteMsg = "costing:write is required to set production run cost articles"

// auxVariantModeShimMsg is what the deprecated ReceiveProductionRun shim says to a card that
// produces by colour. The shim receives a run from the counts already stamped on its plan lines,
// which is a flow that predates colours entirely — no client that knows how to plan a colour also
// uses it — so rather than guess at the colour breakdown it points at the command that carries one.
const auxVariantModeShimMsg = "this card receives per colour variant; use the receipt command with per-variant lines"

// productionRunVariantLinkMsg introduces the plan-time colour refusals with the context the store's
// bare error omits, so the operator reads one sentence rather than an id.
const productionRunVariantLinkMsg = "this run's colour lines do not match the tech card's colours: "

// productionRunVariantStatus maps the store's plan-time colour refusals onto gRPC codes, or returns
// nil when err is something else. A colour that is not this card's is a bad payload
// (InvalidArgument — the client sent an id it should never have offered); a retired colour is a
// state the operator can fix on the card (FailedPrecondition — reactivate it or pick another).
func productionRunVariantStatus(err error) error {
	switch {
	case errors.Is(err, entity.ErrProductionRunLineVariantUnlinked),
		// A half-coloured grid is a payload the client should never have assembled, and the fix is in
		// the grid rather than on the card.
		errors.Is(err, entity.ErrProductionRunLineVariantMixedGrid):
		return status.Error(codes.InvalidArgument, productionRunVariantLinkMsg+err.Error())
	case errors.Is(err, entity.ErrProductionRunLineVariantRetired):
		return status.Error(codes.FailedPrecondition, productionRunVariantLinkMsg+err.Error())
	}
	return nil
}

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
	ins.Actor = authsrv.GetAdminUsername(ctx)
	// ГЕЙТ ГОТОВНОСТИ (Ф6), re-computed server-side against THIS payload — never trusted from the
	// verdict the modal already holds. Saving a раскладка deliberately does not bump
	// tech_card.lock_version, so a norm can be re-measured between the modal opening and this request
	// arriving and the client has no version with which to notice. In report-only mode (the default,
	// and what an unconfigured workshop gets) this logs and returns nil.
	//
	// It runs BEFORE the planned-cost snapshot on purpose: the snapshot reads the card and, on the
	// live-card path, computes a costing — work there is no reason to do for a run that is about to
	// be refused.
	if err := s.runReadinessCreateGate(ctx, ins); err != nil {
		return nil, err
	}
	if err := s.snapshotPlannedCost(ctx, ins); err != nil {
		return nil, err
	}
	if len(ins.Costs) > 0 {
		dto.FoldProductionRunCostsToBase(ins.Costs, s.costingFx(ctx))
	}
	id, err := s.repo.ProductionRuns().CreateProductionRun(ctx, ins)
	if err != nil {
		// Colour lines are validated inside the write transaction, against the registry they commit
		// against — the plan-time counterpart of the receipt's ValidProducts re-check.
		if st := productionRunVariantStatus(err); st != nil {
			return nil, st
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, productionRunFKMsg)
		}
		slog.Default().ErrorContext(ctx, "can't create production run", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't create production run")
	}
	// РЕЗЕРВ ТКАНИ С МОМЕНТА РОЖДЕНИЯ (Ф5б.4, §4.1). Потребность здесь берётся из НОРМЫ — настилов у
	// новорождённого прогона ещё нет. Без этого вызова два прогона на один артикул оба видели бы
	// «хватает» вплоть до выдачи, а узнал бы об этом тот, кто пришёл на склад вторым.
	s.reconcileRunReservations(ctx, id, ins.Actor)
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
	ins.Actor = authsrv.GetAdminUsername(ctx)
	// PRESENCE, not magnitude (Ф6.5): the field is proto3-optional, so a client that SENT a version
	// is held to it even when that version is 0 (a fresh run's), and only a client that omitted the
	// field entirely keeps the legacy last-write-wins.
	lock := entity.LockGuardFromProto(req.ExpectedLockVersion)
	var updateErr error
	if costingWrite {
		updateErr = s.repo.ProductionRuns().UpdateProductionRun(ctx, int(req.Id), ins, lock, s.costingFx(ctx))
	} else {
		updateErr = s.repo.ProductionRuns().UpdateProductionRunPreservingCosts(
			ctx, int(req.Id), ins, lock)
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
		if st := productionRunVariantStatus(err); st != nil {
			return nil, st
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

// ordersReadAccess reports whether the caller may see orders-side data (customer identity on the
// waitlist rides with orders, not products). Mirrors productsWriteAccess.
func ordersReadAccess(ctx context.Context) bool {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return false
	}
	if az.FullAccess() {
		return true
	}
	lvl, ok := az.Perms[rbac.SectionOrders]
	return ok && lvl.Covers(entity.AccessRead)
}

// maskWaitlistEmail keeps just enough of the address to distinguish repeat signals (first rune +
// domain) while withholding the identity itself.
func maskWaitlistEmail(email string) string {
	at := strings.IndexByte(email, '@')
	if at <= 0 {
		return "***"
	}
	r := []rune(email[:at])
	return string(r[0]) + "***" + email[at:]
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
	result, st := s.executeRunReceipt(ctx, run, lines, key, entity.LockGuardFromProto(req.ExpectedLockVersion),
		strings.TrimSpace(req.Note), req.UpdateCostPrice, !req.Partial, false)
	if st != nil {
		return nil, st
	}
	// Storefront side effects of the stock-write contract — fire only on the ORIGINAL execution:
	// a replayed command carries no transitions (json:"-"), so a network retry can never re-send
	// back-in-stock emails or re-run revalidation for stock it did not book.
	if !result.Replayed {
		s.fireStockWriteSideEffects(ctx, result.StockTransitions, req.NotifyWaitlist)
	} else {
		// The one loss window (adversarial #4, accepted): a commit-ack failure on the original
		// execution means ITS effects never fired, and this replay will not re-fire them — the
		// stock is booked but the pages/emails were skipped. Loud so an operator can revalidate
		// by hand if a customer-visible page looks stale.
		slog.Default().WarnContext(ctx, "receipt replayed; storefront side effects were fired by the original execution only",
			slog.Int("run_id", int(req.RunId)), slog.Int("receipt_id", result.ReceiptID))
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
		ExpectedLockVersion: entity.LockGuardFromProto(req.ExpectedLockVersion),
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
	// Stock went back OUT of the shelves — the storefront's sold_out state may have flipped, so
	// the affected product pages must re-render (ISR only; a reversal never notifies). Fired
	// UNCONDITIONALLY on success from the card's linked colourways (a guaranteed superset of the
	// reversed receipt's products) — never coupled to the best-effort echo read below, whose
	// transient failure must not leave the storefront advertising units that just left the shelf
	// (adversarial #3).
	if linked := card.LinkedProductIDs(); len(linked) > 0 {
		s.revalidateAsync(&dto.RevalidationData{Products: linked, Hero: true})
	}
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
	// The deprecated shim has no client-supplied version to hold anyone to — it synthesizes the
	// receipt from the run's STORED counts, which an old client stamped through UpdateProductionRun.
	// So it passes the ABSENT token (legacy last-write-wins) rather than a literal 0, which under
	// Ф6.5 would now be a real expectation and would reject every run past its first save.
	result, st := s.executeRunReceipt(ctx, run, lines, key, entity.NoLockVersion(), "", req.UpdateCostPrice, true, true)
	if st != nil {
		return nil, st
	}
	// The legacy shim's request carries no notify flag — ISR only, never emails.
	if !result.Replayed {
		s.fireStockWriteSideEffects(ctx, result.StockTransitions, false)
	}
	return &pb_admin.ReceiveProductionRunResponse{CostPriceUpdated: result.CostPriceUpdated}, nil
}

// fireStockWriteSideEffects applies the stock-write contract's storefront side effects for a
// production receipt's COMMITTED transitions (mirrors UpdateVariantStock, product.go): ISR
// revalidation for every touched product + hero always; when notify is set, the back-in-stock
// notification for every A-grade variant that went 0→>0 — from the store's locked before/after,
// never a pre-read. B-grade transitions never notify: seconds are not listed on the storefront
// and NotifyMe pins grade A, so no waitlist can exist for them.
func (s *Server) fireStockWriteSideEffects(ctx context.Context, transitions []entity.StockTransition, notify bool) {
	if len(transitions) == 0 {
		return
	}
	seen := make(map[int]bool, len(transitions))
	products := make([]int, 0, len(transitions))
	for _, t := range transitions {
		if !seen[t.ProductID] {
			seen[t.ProductID] = true
			products = append(products, t.ProductID)
		}
	}
	if notify {
		// Group the notifying sizes per product and cap worker concurrency (mirroring
		// revalidateSem's 4): a wide receipt — 30 sizes back in stock at once — must not spawn
		// 30 concurrent notifyWaitlist calls against the shared 15-connection pool that also
		// serves payments (adversarial #2). One worker per product walks its sizes SEQUENTIALLY;
		// at most 4 workers run at a time. Same detachment contract as UpdateVariantStock:
		// WithoutCancel (the 0→>0 event fires exactly once and is never retried, so it must
		// survive the RPC returning), registered in revalWG so shutdown bounded-waits, panics
		// recovered per worker.
		perProduct := make(map[int][]int)
		order := make([]int, 0, len(transitions))
		for _, t := range transitions {
			if t.Grade == entity.VariantGradeB || !t.BackInStock() {
				continue
			}
			if _, ok := perProduct[t.ProductID]; !ok {
				order = append(order, t.ProductID)
			}
			perProduct[t.ProductID] = append(perProduct[t.ProductID], t.SizeID)
		}
		if len(order) > 0 {
			notifyCtx := context.WithoutCancel(ctx)
			sem := make(chan struct{}, 4)
			for _, pid := range order {
				sizes := perProduct[pid]
				s.revalWG.Add(1)
				go func(pid int, sizes []int) {
					defer s.revalWG.Done()
					defer saferun.Recover(notifyCtx, "notify-waitlist")
					sem <- struct{}{}
					defer func() { <-sem }()
					for _, sid := range sizes {
						s.notifyWaitlist(notifyCtx, pid, sid)
					}
				}(pid, sizes)
			}
		}
	}
	s.revalidateAsync(&dto.RevalidationData{Products: products, Hero: true})
}

// executeRunReceipt is the shared core of both receive RPCs: permission gates, tech-card linkage
// validation (aux detection and its colour mode included), and the store command. Returns a gRPC
// status error mapped from the command's outcome.
//
// legacyTotals marks the deprecated ReceiveProductionRun shim in both senses it has: the store's
// rollup write SETs instead of accumulating, and the aux dispatch below refuses a card that
// produces by colour, which is a shape the shim's stamped-counts flow predates.
func (s *Server) executeRunReceipt(ctx context.Context, run *entity.ProductionRun, lines []entity.ProductionRunReceiptLineInput,
	idempotencyKey string, expectedLockVersion entity.LockGuard, note string, updateCostPrice, final, legacyTotals bool) (*entity.PostProductionRunReceiptResult, error) {
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
		NormalLossRate:      s.defectNormalLossRate,
	}
	// NF-07: an auxiliary card's output is received into the material warehouse, not product stock.
	if card.Purpose == entity.TechCardPurposeAuxiliary {
		params.Aux = true
		// WHERE the output lands is NOT decided here. This read happened before the run lock, so any
		// bucket it named could be re-pointed or retired before the command runs; the store resolves
		// the destination from the card's registry inside the transaction instead. What is left here
		// is the pair of FAST-FAIL preconditions worth answering without opening a transaction — and
		// both are stable facts about how the card is configured, not about which bucket wins.
		// The same union rule the store's resolveAuxDestination applies: live colours on the card,
		// OR colour lines already on the run. The second arm is the grandfathering promise — a run
		// planned per colour stays receivable after its last colour is retired mid-flight (aux can
		// neither partial nor reverse, and cancel is refused while materials are issued, so an
		// ACTIVE-only answer here would freeze that run behind advice about an output material a
		// colour-mode card deliberately does not have).
		colourMode := false
		for i := range card.OutputVariants {
			if card.OutputVariants[i].Active {
				colourMode = true
				break
			}
		}
		if !colourMode {
			for i := range run.Lines {
				if run.Lines[i].OutputVariantId.Valid && run.Lines[i].OutputVariantId.Int32 > 0 {
					colourMode = true
					break
				}
			}
		}
		switch {
		case colourMode:
			// The deprecated shim receives from the counts stamped on the plan grid by a client that
			// predates colours entirely. Rather than let it drive a per-colour booking it was never
			// designed to express, point it at the command that carries the breakdown.
			if legacyTotals {
				return nil, status.Error(codes.FailedPrecondition, auxVariantModeShimMsg)
			}
		case !card.OutputMaterialId.Valid:
			// Legacy single-output mode with no bucket at all: nothing the store could resolve to.
			// Reported here so the operator gets a precondition they can fix rather than a
			// reload-and-retry from inside the command.
			return nil, status.Error(codes.FailedPrecondition, "auxiliary card has no output material set; set it before receiving")
		}
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
			errors.Is(err, entity.ErrProductionRunAuxPartial),
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
	return s.buildRunMaterialPlan(ctx, int(req.RunId))
}

// buildRunMaterialPlan is the ONE composition of a run's material requirement, and it has TWO
// readers now: the read-only RPC above, and the fabric reservation below (Ф5б.4).
//
// ОНА ИЗВЛЕЧЕНА ИМЕННО ЗАТЕМ, ЧТОБЫ ЧИТАТЕЛЕЙ ОСТАЛОСЬ ДВА, А ОПРЕДЕЛЕНИЙ — ОДНО. Резерв, считающий
// потребность своей вторая дорогой, рано или поздно разошёлся бы с тем, что показывает экран
// материального плана, — и разошёлся бы молча: оба числа выглядят правдоподобно, а сходятся они
// только пока их считают одинаково. Резерв под тем, что видит цех, — это не оптимизация, это
// условие, чтобы «не хватает ткани» на экране и «ткань удержана» в реестре означали одно и то же.
func (s *Server) buildRunMaterialPlan(ctx context.Context, runID int) (*pb_admin.GetProductionRunMaterialPlanResponse, error) {
	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, runID)
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
	// Ф4.6 — the настилы, and a read failure is FATAL to this request rather than degraded into an
	// empty list. An empty list here does not mean «нет настилов», it means «мы не знаем», and the
	// two produce different NUMBERS: falling back to the norm would silently re-estimate a slot the
	// cutting room has already measured, under a plan_source that says NORM with full confidence.
	// An aux card answers Applicable=false with no lays, which is the honest «настилов тут не бывает»
	// and needs no special case below.
	lays, err := s.repo.ProductionRuns().ListLays(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run lays for material plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run lays")
	}
	return dto.ComputeProductionRunMaterialPlan(run, card, onHand, issued, linked, lays.Lays), nil
}

// reconcileRunReservations makes the run's OPEN fabric claims equal its current requirement (Ф5б.4).
//
// Зовётся в двух моментах §4.1 — после рождения прогона (потребность из НОРМЫ) и после сохранения
// настила (потребность из НАСТИЛОВ). Одна функция на оба, потому что различие живёт целиком внутри
// материального плана: он сам выбирает источник и сам говорит, какой выбрал. Второй вызов, знающий
// про «настилы против нормы», был бы третьим определением потребности.
//
// УДЕРЖИВАЕТСЯ НЕПОКРЫТЫЙ ОСТАТОК, А НЕ ВАЛОВАЯ ПОТРЕБНОСТЬ: required − issued. Выданное уже ушло со
// склада, и держать его вторично значит выдумать дефицит на ровном месте — на прогоне, которому
// всё выдали, «не хватает ткани» загорелось бы ровно на всю его потребность.
//
// ОШИБКА ЗДЕСЬ НЕ РОНЯЕТ ЗАПРОС, И ЭТО НЕ СНИСХОДИТЕЛЬНОСТЬ, А ЕДИНСТВЕННЫЙ ЧЕСТНЫЙ ВЫБОР. Оба
// вызова стоят ПОСЛЕ того, как основная запись зафиксирована: прогон уже создан, настил уже сохранён.
// Вернуть отсюда ошибку значит сказать «не получилось» про запись, которая получилась, — и клиент
// повторит её, встретив оптимистичную блокировку. Резерв же идемпотентен по построению: следующее
// сохранение настила приведёт претензии к истине. Поэтому промах пишется в лог как ошибка (его
// увидят), но ответ остаётся правдой о том, что произошло с данными.
func (s *Server) reconcileRunReservations(ctx context.Context, runID int, username string) {
	plan, err := s.buildRunMaterialPlan(ctx, runID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't build material plan to reserve fabric",
			slog.Int("run_id", runID), slog.String("err", err.Error()))
		return
	}
	required := make(map[int]entity.RunMaterialRequirement, len(plan.GetRows()))
	for _, row := range plan.GetRows() {
		mid := int(row.GetMaterialId())
		if mid <= 0 {
			continue
		}
		outstanding := planDecimal(row.GetRequired()).Sub(planDecimal(row.GetIssued()))
		if !outstanding.IsPositive() {
			// Ноль в карту НЕ КЛАДЁТСЯ, и это не то же самое, что не класть строку вовсе: писатель
			// делает открытые претензии прогона РАВНЫМИ карте, поэтому отсутствие ключа снимает
			// удержание, а нулевая претензия висела бы удержанием на ноль метров — строкой, которая
			// ничего не держит и которую всё равно надо кому-то закрывать.
			continue
		}
		required[mid] = entity.RunMaterialRequirement{Qty: outstanding}
	}
	if err := s.repo.MaterialStock().SetRunMaterialReservations(ctx, runID, required, username); err != nil {
		slog.Default().ErrorContext(ctx, "can't reserve fabric for production run",
			slog.Int("run_id", runID), slog.String("err", err.Error()))
	}
}

// planDecimal reads a plan cell, treating absence as zero — the plan emits every number it knows and
// omits only what it could not compute, and «нечего вычитать» is exactly zero here.
func planDecimal(d *pb_decimal.Decimal) decimal.Decimal {
	if d == nil {
		return decimal.Zero
	}
	v, err := decimal.NewFromString(strings.TrimSpace(d.GetValue()))
	if err != nil {
		return decimal.Zero
	}
	return v
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
