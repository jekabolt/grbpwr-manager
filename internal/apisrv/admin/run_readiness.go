package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CheckProductionRunReadiness judges a run that does not exist yet — the Ф6 gate.
//
// Read-only. It shares its ENTIRE verdict path with the re-check inside CreateProductionRun
// (loadRunReadiness → dto.ComputeProductionRunReadiness), and that is not tidiness: the modal and
// the command must never be able to disagree, and two code paths judging «is this run creatable»
// would eventually do exactly that.
func (s *Server) CheckProductionRunReadiness(ctx context.Context, req *pb_admin.CheckProductionRunReadinessRequest) (*pb_admin.CheckProductionRunReadinessResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	cells := dto.RunReadinessCellsFromPb(req.GetCells())
	colorways := normalizeReadinessColorwayIds(req.GetColorwayIds(), cells)
	res, err := s.loadRunReadiness(ctx, runReadinessQuery{
		TechCardId:  int(req.GetTechCardId()),
		ReleaseId:   int(req.GetReleaseId()),
		ColorwayIds: colorways,
		Cells:       cells,
		RunId:       int(req.GetRunId()),
	})
	if err != nil {
		return nil, err
	}
	// NOT stripped for an account without costing:read, and the absence of a strip here is
	// deliberate rather than forgotten: coverage is metres and pieces, unit_coverage is garments,
	// and no field on this message carries money. GetProductionRun beside it does strip, which is
	// exactly why this has to be said out loud.
	return dto.RunReadinessToPb(res), nil
}

// runReadinessQuery is what a caller asks about. The same struct serves the RPC and the create-time
// re-check, so the two cannot drift in what they load.
type runReadinessQuery struct {
	TechCardId  int
	ReleaseId   int
	ColorwayIds []int
	Cells       []entity.RunReadinessCell
	// RunId > 0 re-checks an EXISTING run. It changes no rule; it is only where
	// actual_wastage_percent comes from, which moves the metreage and nothing else.
	RunId int
}

// loadRunReadiness gathers every fact the gate reads and evaluates it.
//
// The reads mirror GetProductionRunMaterialPlan's recipe exactly — one card read plus on-hand over
// the union of articles — because the gate runs the same plan engine over a SYNTHETIC run built from
// the request instead of a stored one. Two extra reads sit on top: the narrowest measured lot width
// per article (Ф5а.1, for the width rule) and the card's pattern size index (Ф6.3).
func (s *Server) loadRunReadiness(ctx context.Context, q runReadinessQuery) (dto.RunReadinessResult, error) {
	var out dto.RunReadinessResult
	card, err := s.repo.TechCards().GetTechCardById(ctx, q.TechCardId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return out, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card for run readiness", slog.String("err", err.Error()))
		return out, status.Error(codes.Internal, "can't load tech card")
	}

	in := dto.RunReadinessInput{
		Card:        card,
		ReleaseId:   q.ReleaseId,
		ColorwayIds: q.ColorwayIds,
		Cells:       q.Cells,
	}

	// The release is resolved but a MISSING one is not an error here: «release_id names nothing» is a
	// verdict the gate reports as a BLOCKER row with a target, not a 404 that tells the modal nothing
	// about the other eleven checks.
	if q.ReleaseId > 0 {
		rel, err := s.repo.TechCards().GetTechCardRelease(ctx, q.ReleaseId)
		switch {
		case err == nil && rel != nil:
			meta := rel.TechCardReleaseMeta
			in.Release = &meta
		case errors.Is(err, sql.ErrNoRows):
			// leave nil — release_frozen says so
		case err != nil:
			slog.Default().ErrorContext(ctx, "can't load release for run readiness", slog.String("err", err.Error()))
			return out, status.Error(codes.Internal, "can't load release")
		}
	}

	settings, err := s.repo.Workshop().GetSettings(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load workshop settings for run readiness", slog.String("err", err.Error()))
		return out, status.Error(codes.Internal, "can't load workshop settings")
	}
	in.Settings = settings

	// An EXISTING run contributes its actual cutting wastage and its already-issued materials. A
	// FUTURE run has neither, which is why the modal's metreage is systematically different from the
	// same run's figure a minute after creation — labelled «оценка по норме», not hidden.
	if q.RunId > 0 {
		run, err := s.repo.ProductionRuns().GetProductionRun(ctx, q.RunId)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Default().ErrorContext(ctx, "can't load run for readiness re-check", slog.String("err", err.Error()))
			return out, status.Error(codes.Internal, "can't load production run")
		}
		if run != nil {
			in.ActualWastagePercent = run.ActualWastagePercent
			in.Issued = dto.AggregateRunMaterialIssues(run.MaterialMovements)
		}
	}

	matIDs := readinessMaterialIDs(card, in.Issued)
	onHand := make(map[int]decimal.Decimal, len(matIDs))
	for _, mid := range matIDs {
		st, err := s.repo.MaterialStock().GetMaterialStock(ctx, mid)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				onHand[mid] = decimal.Zero // never received → no stock row yet
				continue
			}
			slog.Default().ErrorContext(ctx, "can't load material stock for run readiness", slog.String("err", err.Error()))
			return out, status.Error(codes.Internal, "can't load material stock")
		}
		onHand[mid] = st.OnHand
	}
	in.OnHand = onHand

	widths, err := s.repo.MaterialStock().NarrowestMeasuredLotWidths(ctx, matIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load measured lot widths for run readiness", slog.String("err", err.Error()))
		return out, status.Error(codes.Internal, "can't load material lots")
	}
	in.NarrowestMeasuredLotCm = widths

	index, err := s.repo.TechCards().GetTechCardPatternSizeIndex(ctx, q.TechCardId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load pattern size index for run readiness", slog.String("err", err.Error()))
		return out, status.Error(codes.Internal, "can't load pattern size index")
	}
	in.PatternSizeIndex = index

	return dto.ComputeProductionRunReadiness(in), nil
}

// readinessMaterialIDs is the union of articles the plan may resolve: slot defaults, colourway pins,
// and anything already issued to a run being re-checked. Same union GetProductionRunMaterialPlan
// builds, and for the same reason — a pinned article that is no slot's default would otherwise have
// no on-hand key and read as a 100% shortage while the shelf is full.
func readinessMaterialIDs(card *entity.TechCard, issued map[int]decimal.Decimal) []int {
	seen := map[int]bool{}
	ids := make([]int, 0, len(card.BomItems))
	add := func(id int) {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for i := range card.BomItems {
		if card.BomItems[i].MaterialId.Valid {
			add(int(card.BomItems[i].MaterialId.Int64))
		}
	}
	for i := range card.Colorways {
		for j := range card.Colorways[i].Usages {
			if u := &card.Colorways[i].Usages[j]; u.MaterialId.Valid {
				add(int(u.MaterialId.Int64))
			}
		}
	}
	extra := make([]int, 0, len(issued))
	for mid := range issued {
		extra = append(extra, mid)
	}
	sort.Ints(extra)
	for _, mid := range extra {
		add(mid)
	}
	sort.Ints(ids)
	return ids
}

// normalizeReadinessColorwayIds de-duplicates the requested colourways and, when the caller named
// none, falls back to the ones the grid mentions.
//
// The fallback exists for the create-time re-check, whose only statement of intent IS the grid. The
// RPC keeps both fields because the modal must be able to show a verdict for a colourway whose
// quantities have not been typed yet — otherwise there is nothing to grey its checkbox with, and the
// operator ticks it, types numbers, and only then learns it was never eligible.
func normalizeReadinessColorwayIds(ids []int32, cells []entity.RunReadinessCell) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		v := int(id)
		if v <= 0 || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	if len(out) > 0 {
		return out
	}
	return dto.RunReadinessColorwayIdsFromCells(cells)
}

// --- ГЕЙТ НА СОЗДАНИИ ---------------------------------------------------------------------------

// runReadinessCreateGate re-computes the verdict against the SUBMITTED payload and either refuses
// the create or logs and lets it through.
//
// THE RE-CHECK CANNOT BE OPTIMISED AWAY by trusting the verdict the modal already got, and the
// reason is specific rather than general caution: saving a раскладка deliberately does NOT bump
// tech_card.lock_version (so that an open nesting editor does not 409 on every mouse move). So a
// norm can be re-measured between the modal opening and the request arriving, and the client has NO
// VERSION TO NOTICE IT WITH. The client whose snapshot went stale is the same client that is
// submitting. Server-side re-computation is the only defence there is.
//
// In report-only mode it logs STRUCTURALLY — the keys, the card, the colourways — because that log
// IS the кампания Д3 worklist and the evidence for deciding when blocking mode can be switched on.
func (s *Server) runReadinessCreateGate(ctx context.Context, ins *entity.ProductionRunInsert) error {
	cells := dto.RunReadinessCellsFromRunLines(ins.Lines)
	releaseID := 0
	if ins.ReleaseId.Valid {
		releaseID = int(ins.ReleaseId.Int64)
	}
	res, err := s.loadRunReadiness(ctx, runReadinessQuery{
		TechCardId:  ins.TechCardId,
		ReleaseId:   releaseID,
		ColorwayIds: dto.RunReadinessColorwayIdsFromCells(cells),
		Cells:       cells,
	})
	if err != nil {
		// A CARD THAT DOES NOT EXIST is not the gate's refusal to make, and saying so here would
		// change an error this command has always returned. snapshotPlannedCost right below already
		// answers «tech_card_id does not exist» with InvalidArgument, and the store's FK backs it up;
		// turning that into the gate's NotFound would be a silent contract change bolted onto an
		// unrelated feature. There is also nothing to gate: a card with no rows has no readiness.
		if status.Code(err) == codes.NotFound {
			return nil
		}
		// Everything else is an infrastructure failure, already mapped to a status naming what failed.
		// It travels out as it is: a gate that cannot compute must not silently pass, and refusing
		// loudly is the fail-closed side of that choice.
		return err
	}
	blockers := res.Report.Blockers()
	if len(blockers) == 0 {
		return nil
	}
	keys := make([]string, 0, len(blockers))
	for _, b := range blockers {
		keys = append(keys, b.Key)
	}
	unready := res.Report.UnreadyColorways()
	cwIDs := make([]int, 0, len(unready))
	for _, c := range unready {
		cwIDs = append(cwIDs, c.ColorwayId)
	}
	if !res.Report.BlockingEnabled {
		slog.Default().WarnContext(ctx, "run_readiness_blocked",
			slog.Bool("enforced", false),
			slog.Int("tech_card_id", ins.TechCardId),
			slog.String("keys", strings.Join(keys, ",")),
			slog.String("unready_colorway_ids", joinIntsForLog(cwIDs)),
			slog.Int("unknown_count", res.Report.UnknownCount()))
		return nil
	}
	slog.Default().WarnContext(ctx, "run_readiness_blocked",
		slog.Bool("enforced", true),
		slog.Int("tech_card_id", ins.TechCardId),
		slog.String("keys", strings.Join(keys, ",")),
		slog.String("unready_colorway_ids", joinIntsForLog(cwIDs)),
		slog.Int("unknown_count", res.Report.UnknownCount()))
	return apierr.FailedPreconditionMany(
		"the production run readiness gate refused this run; fix the listed blockers or ask the workshop to turn blocking mode off",
		runReadinessViolations(res.Report, ins.Lines))
}

// runReadinessViolations turns the blockers into one FieldViolation each, addressed at the input the
// operator can actually change.
//
// The field path matters: a colourway-scoped blocker points at `colorway_ids[i]` so the modal can
// mark the row it came from, and a card- or run-scoped one points at the card or the grid. The
// description leads with the stable KEY and then the human sentence, so a client may branch on the
// key without parsing prose, and a human reading the raw status still gets a sentence.
func runReadinessViolations(r entity.RunReadinessReport, lines []entity.ProductionRunLine) []*entity.ValidationError {
	var out []*entity.ValidationError
	for _, f := range r.Card {
		if f.Blocks() {
			out = append(out, entity.NewFieldViolation("run.tech_card_id", f.Key, "", f.Detail))
		}
	}
	// colorway_ids[i] is indexed over the colourways of THIS report in request order, which is the
	// order the modal built its list in.
	for i, c := range r.Colorways {
		for _, f := range c.Findings {
			if !f.Blocks() {
				continue
			}
			out = append(out, entity.NewFieldViolation(
				fmt.Sprintf("colorway_ids[%d]", i), f.Key,
				fmt.Sprintf("colourway %q (#%d)", c.Name, c.ColorwayId), f.Detail))
		}
	}
	for _, f := range r.Run {
		if !f.Blocks() {
			continue
		}
		field := "run.lines"
		// A size blocker is addressable at the exact cell, so it is addressed there.
		if f.Target.SizeId > 0 {
			for i := range lines {
				if lines[i].SizeId == f.Target.SizeId {
					field = fmt.Sprintf("run.lines[%d].size_id", i)
					break
				}
			}
		}
		out = append(out, entity.NewFieldViolation(field, f.Key, "", f.Detail))
	}
	return out
}

func joinIntsForLog(v []int) string {
	parts := make([]string, 0, len(v))
	for _, x := range v {
		parts = append(parts, fmt.Sprintf("%d", x))
	}
	return strings.Join(parts, ",")
}

// --- Ф6.3: ЗАПИСЬ ИНДЕКСА РАЗМЕРОВ --------------------------------------------------------------

// PutTechCardPatternSizeIndex stores the size tokens the client parsed out of one fabric scope's DXF
// block names. See the proto contract for why the parse stays in the client and the fingerprint
// stays on the server.
func (s *Server) PutTechCardPatternSizeIndex(ctx context.Context, req *pb_admin.PutTechCardPatternSizeIndexRequest) (*pb_admin.PutTechCardPatternSizeIndexResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	if strings.TrimSpace(req.GetScopeKey()) == "" {
		return nil, status.Error(codes.InvalidArgument, "scope_key is required")
	}
	// An EMPTY token list is legal and is stored as such: «the files carry no size coding» is an
	// answer, and refusing to record it would leave the gate saying «nobody ran the audit» forever on
	// a card whose audit ran and found nothing to find.
	res, err := s.repo.TechCards().PutTechCardPatternSizeIndex(ctx, entity.PatternSizeIndexWrite{
		TechCardId:    int(req.GetTechCardId()),
		ScopeKey:      strings.TrimSpace(req.GetScopeKey()),
		SheetLineKeys: req.GetSheetLineKeys(),
		SizeTokens:    req.GetSizeTokens(),
		ParsedBy:      authsrv.GetAdminUsername(ctx),
	})
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		if s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.InvalidArgument, "tech_card_id does not exist")
		}
		slog.Default().ErrorContext(ctx, "can't write pattern size index", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't write pattern size index")
	}
	// The token→size resolution happens HERE, on every read and every write, never at storage time.
	// Storing size ids instead would make a size added to the range AFTER the parse read as «not in
	// the files» forever — a false blocker that no re-upload could clear. Tokens make the card fix
	// itself the moment somebody extends the range.
	found := make(map[string]bool, len(res.StoredTokens))
	for _, t := range res.StoredTokens {
		found[t] = true
	}
	resolved := 0
	for _, sid := range res.CardSizeIds {
		size, ok := cache.GetSizeById(sid)
		if ok && entity.SizeCoveredByTokens(size.Name, found) {
			resolved++
		}
	}
	return &pb_admin.PutTechCardPatternSizeIndexResponse{
		SheetFingerprint:  res.SheetFingerprint,
		ResolvedSizeCount: int32(resolved),
	}, nil
}
