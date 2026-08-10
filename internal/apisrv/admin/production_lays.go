package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// НАСТИЛЫ ПРОГОНА (Ф4) — три RPC и ничего кроме них.
//
// A настил is its own aggregate with its own commands, and UpdateProductionRun does not know this
// file exists. That is the structural repair of what killed migration 0119 (dropped by 0243): a full
// replace of a run's children on every run save erased whatever a direct API caller had written, and
// production_run_cost still demonstrates the failure mode next door. Here a настил the payload does
// not mention is not touched, and only DeleteProductionRunLay removes one.
//
// РАЗДЕЛЕНИЕ ТРУДА В ЭТОМ ФАЙЛЕ. Loads, permissions and gRPC codes — here. Every number, every
// verdict and every wire field — in internal/dto (production_lay_{yield,coverage,checks,view}.go),
// pure and unit-tested without a database. A handler that computed anything would be a second
// definition of it, and §6.4 forbids exactly that for the one thing two RPCs both answer: покрытие.

// productionRunLayConflictMsg is THE SAME COPY the run's own optimistic lock uses. Two objects, one
// sentence, because the client shows one toast and the operator has one thing to do: reload.
const productionRunLayConflictMsg = "production run lay was modified concurrently; reload and retry"

// productionRunLayNotApplicableMsg explains the FailedPrecondition an auxiliary card gets. The
// machine-readable half is entity.ProductionRunLayNotApplicableKey, carried as the violation's
// reason so a client can branch without parsing prose.
const productionRunLayNotApplicableMsg = "an auxiliary tech card has no colourways, no cut pieces and no раскладки, so it has no lay plan"

// productionRunMarkerWriteMsg is what an account without production:write is told when it tries to
// take a раскладка FOR A RUN through SaveTechCardMarker (решение Р3).
const productionRunMarkerWriteMsg = "production:write is required to take a раскладка for a production run (re-login if the permission was just granted)"

// productionWriteAccess reports whether the caller may write the production section. Mirrors
// productsWriteAccess, including its fail-closed behaviour on a context that never passed the RBAC
// interceptor: a handler-side second requirement that defaulted to «allowed» would be worse than not
// having one, because it would look enforced.
func productionWriteAccess(ctx context.Context) bool {
	az, ok := authsrv.GetAdminAuthz(ctx)
	if !ok {
		return false
	}
	if az.FullAccess() {
		return true
	}
	lvl, ok := az.Perms[rbac.SectionProduction]
	return ok && lvl.Covers(entity.AccessWrite)
}

// ListProductionRunLays returns the run's whole lay plan: the настилы, the coverage grid, the
// per-cell diagnostics, and the run's own раскладки for the editor's picker.
func (s *Server) ListProductionRunLays(ctx context.Context, req *pb_admin.ListProductionRunLaysRequest) (*pb_admin.ListProductionRunLaysResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	return s.loadRunLayPlan(ctx, int(req.GetRunId()), newLayMarkerCache(s.repo.TechCards()))
}

// SaveProductionRunLay creates or updates ONE настил and diffs its sections by key.
//
// It addresses exactly one настил and can see no other, which is why saving one can never disturb
// another — and why a настил written through this RPC survives somebody saving the run from the UI.
func (s *Server) SaveProductionRunLay(ctx context.Context, req *pb_admin.SaveProductionRunLayRequest) (*pb_admin.SaveProductionRunLayResponse, error) {
	runID := int(req.GetRunId())
	if runID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	ins, err := dto.ConvertPbProductionRunLayInsertToEntity(req.GetLay())
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	markers := newLayMarkerCache(s.repo.TechCards())
	// ГОДНОСТЬ НА ЗАПИСИ (§8.3): the subset that must REFUSE rather than report. The store already
	// enforces parity and the marker's card/run/slot scope inside its transaction; what it cannot ask
	// is the COLOURWAY binding, because «0 = общая раскладка» is a three-valued reading that lives in
	// dto.ColorwayBindingOf and must not be re-expressed as SQL. Best-effort by construction: if the
	// pre-flight cannot resolve what it needs, it stands aside and the store speaks — a guard that
	// turned a lookup failure into a refusal would block saves for a reason the operator cannot see.
	//
	// ШТАМП NETTO (0296) ЗДЕСЬ НЕ СЧИТАЕТСЯ, И ЭТО РЕШЕНИЕ (правки ревью T7в2, MAJOR 1): знаменатель
	// считает SaveLay ВНУТРИ транзакции записи факта (computeLayNettoStamp) — посчитанный до неё, он
	// мог бы разъехаться с рецептом, под которым факт коммитится, а ошибка чтения здесь превращалась
	// бы в тихий NULL-штамп.
	if st := s.preflightLayForSave(ctx, runID, ins, markers); st != nil {
		return nil, st
	}

	saved, err := s.repo.ProductionRuns().SaveLay(ctx, runID, ins,
		// PRESENCE, НЕ ВЕЛИЧИНА. У настила нет легаси-клиентов, поэтому отсутствие токена на
		// СУЩЕСТВУЮЩЕМ настиле — отказ, а не last-write-wins: the run's opt-out exists only because
		// clients predating its lock echo a bare 0, and a new object does not inherit that hole.
		entity.LockGuardFromProto(req.ExpectedLockVersion), req.GetReaffirmQuantities(),
		authsrv.GetAdminUsername(ctx))
	if err != nil {
		return nil, s.productionRunLayError(ctx, "save", runID, err)
	}

	// РЕЗЕРВ ПЕРЕСЧИТЫВАЕТСЯ ПОД ИЗМЕРЕННОЕ (Ф5б.4, §4.1). Настил — это замер вместо оценки, поэтому
	// после его сохранения удерживать надо ровно то, что он показал: норма могла и завышать, и
	// занижать, а держать ткань по числу, которое цех уже опроверг, — значит либо запирать чужой
	// рулон, либо не запереть свой. Идемпотентно: тот же настил, сохранённый дважды, не пишет в
	// реестр ничего.
	s.reconcileRunReservations(ctx, runID, authsrv.GetAdminUsername(ctx))

	// ОДИН ПУТЬ ЧТЕНИЯ. The saved настил is projected by rebuilding the whole plan and picking it out,
	// so a save and the list that follows it cannot disagree about a single field — checks, coverage
	// and totals included. It costs a handful of reads on a path nobody calls in a loop.
	//
	// A failure HERE is reported even though the write committed: the alternative is a second, poorer
	// projection of the same row, and two projections drift. The client's retry then meets the
	// optimistic lock («reload and retry»), which lands it on the state its write produced.
	plan, err := s.loadRunLayPlan(ctx, runID, markers)
	if err != nil {
		return nil, err
	}
	for _, l := range plan.GetLays() {
		if l.GetLayKey() == saved.LayKey {
			return &pb_admin.SaveProductionRunLayResponse{Lay: l}, nil
		}
	}
	slog.Default().ErrorContext(ctx, "saved production run lay is missing from its own plan",
		slog.Int("run_id", runID), slog.String("lay_key", saved.LayKey))
	return nil, status.Error(codes.Internal, "can't render the saved настил; reload the run")
}

// DeleteProductionRunLay removes ONE настил (its sections cascade).
func (s *Server) DeleteProductionRunLay(ctx context.Context, req *pb_admin.DeleteProductionRunLayRequest) (*pb_admin.DeleteProductionRunLayResponse, error) {
	if req.GetRunId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "run_id is required")
	}
	if err := s.repo.ProductionRuns().DeleteLay(ctx, int(req.GetRunId()), req.GetLayKey()); err != nil {
		return nil, s.productionRunLayError(ctx, "delete", int(req.GetRunId()), err)
	}
	return &pb_admin.DeleteProductionRunLayResponse{}, nil
}

// loadRunLayPlan gathers every fact the plan reads and hands it to the pure builder.
//
// The marker cache is passed IN so a save can reuse the very rows its pre-flight already read: one
// GetMarker (and therefore one protojson parse) per distinct marker per REQUEST, however many
// sections of however many настилов name it (§14 п.16).
func (s *Server) loadRunLayPlan(ctx context.Context, runID int, markers *layMarkerCache) (*pb_admin.ListProductionRunLaysResponse, error) {
	list, err := s.repo.ProductionRuns().ListLays(ctx, runID)
	if err != nil {
		return nil, s.productionRunLayError(ctx, "list", runID, err)
	}
	if !list.Applicable {
		// §1.9: aux-карточка. Отдаётся ЯВНО и БЕЗ единого дальнейшего чтения — ни покрытия, ни маркеров,
		// ни карточки: считать там нечего, и пустой список читался бы как приглашение построить настил.
		return &pb_admin.ListProductionRunLaysResponse{
			Applicable:          false,
			NotApplicableReason: list.NotApplicableReason,
		}, nil
	}

	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "production run not found")
		}
		slog.Default().ErrorContext(ctx, "can't load production run for lay plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load production run")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't load tech card for lay plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}

	// The geometry of every marker the настилы name. A row that has vanished is TOLERATED here rather
	// than fatal: it leaves the marker absent from the map, the section's lay_marker_scope goes
	// BLOCKER, and the operator sees which section to fix — which is more useful than a 500 on the
	// whole page.
	for i := range list.Lays {
		for _, sec := range list.Lays[i].Sections {
			if _, err := markers.get(ctx, sec.MarkerId); err != nil {
				if errors.Is(err, entity.ErrMarkerNotFound) {
					slog.Default().WarnContext(ctx, "lay section names a marker that no longer exists",
						slog.Int("run_id", runID), slog.Int("marker_id", sec.MarkerId))
					continue
				}
				slog.Default().ErrorContext(ctx, "can't load marker for lay plan", slog.String("err", err.Error()))
				return nil, status.Error(codes.Internal, "can't load раскладка")
			}
		}
	}

	runMarkers, err := s.repo.TechCards().ListRunMarkers(ctx, runID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list run markers for lay plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list раскладки of this run")
	}

	// Артикулы, которые колорвеи пинят на слоты настилов СЕГОДНЯ — identity and the narrowest measured
	// lot, the two halves the width and stack-height checks read.
	matIDs := dto.LayPlanMaterialIds(card, list.Lays)
	materials := make(map[int]entity.MaterialWithPrice, len(matIDs))
	for mid, m := range card.LinkedMaterials {
		materials[mid] = m
	}
	for _, mid := range matIDs {
		if _, ok := materials[mid]; ok {
			continue
		}
		// Best-effort, exactly as the material plan does it: a miss degrades the two checks to UNKNOWN
		// («сравнивать не с чем»), which is the truth, rather than failing the page.
		if m, merr := s.repo.TechCards().GetMaterial(ctx, mid); merr == nil && m != nil {
			materials[mid] = *m
		}
	}
	var lots map[int]decimal.NullDecimal
	if len(matIDs) > 0 {
		lots, err = s.repo.MaterialStock().NarrowestMeasuredLotWidths(ctx, matIDs)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't load measured lot widths for lay plan", slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't load material lots")
		}
	}

	settings, err := s.repo.Workshop().GetSettings(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load workshop settings for lay plan", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load workshop settings")
	}

	return dto.BuildProductionRunLayPlan(dto.LayPlanInput{
		Run:                    run,
		Card:                   card,
		Lays:                   list.Lays,
		Markers:                markers.all(),
		RunMarkers:             runMarkers,
		Materials:              materials,
		NarrowestMeasuredLotCm: lots,
		Settings:               settings,
	}), nil
}

// preflightLayForSave runs dto.ValidateLayForSave over the payload and returns a status error when it
// refuses. Returns nil both when the настил passes and when the pre-flight could not be assembled —
// see the call site for why standing aside is the correct failure mode.
func (s *Server) preflightLayForSave(ctx context.Context, runID int, ins entity.ProductionRunLayInsert,
	markers *layMarkerCache) error {

	run, err := s.repo.ProductionRuns().GetProductionRun(ctx, runID)
	if err != nil || run == nil {
		return nil // the store re-reads the run under its own lock and answers properly
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, run.TechCardId)
	if err != nil || card == nil {
		return nil
	}
	bomItemID := layBomItemByLineKey(card, ins.BomLineKey)
	if !bomItemID.Valid {
		// The slot key names nothing on this card. The store answers that with a field violation on
		// lay.bom_line_key; judging marker scope against an unresolved slot here would report the
		// WRONG fault («сопоставить маркер не с чем») for a payload whose real problem has a name.
		return nil
	}
	for i, sec := range ins.Sections {
		if _, err := markers.get(ctx, sec.MarkerId); err != nil {
			if errors.Is(err, entity.ErrMarkerNotFound) {
				return apierr.Invalid(entity.NewFieldViolation(
					fmt.Sprintf("lay.sections[%d].marker_id", i), "not_found",
					fmt.Sprintf("раскладка %d", sec.MarkerId),
					"pick a раскладка taken for THIS run on this настил's cloth slot"))
			}
			slog.Default().ErrorContext(ctx, "can't load marker for lay save", slog.String("err", err.Error()))
			return status.Error(codes.Internal, "can't load раскладка")
		}
	}
	err = dto.ValidateLayForSave(dto.LayWriteCheckInput(runID, card.Id, bomItemID, ins, markers.all()))
	if err == nil {
		return nil
	}
	// Чётность слоёв — свойство ПЕЙЛОАДА, и store'овская проверка того же факта отвечает
	// InvalidArgument полем; один факт обязан иметь один код, какой бы гвард ни сработал первым.
	// Область маркера — свойство СОСТОЯНИЯ (раскладка принадлежит другому прогону, карточке, цвету):
	// запрос корректен, его блокирует то, на что он ссылается.
	if errors.Is(err, dto.ErrLayModeParity) {
		return status.Error(codes.InvalidArgument, err.Error())
	}
	return status.Error(codes.FailedPrecondition, err.Error())
}

// layBomItemByLineKey resolves the настил's cloth slot the way the настил ADDRESSES it — by stable
// line_key, the world of 0230/0159. This is a direct key lookup and NOT a second copy of the
// piece→slot resolver (planBomLine): that one exists because tech_card_piece_material may reference
// a slot by a legacy POSITIONAL index, and nothing on this path does.
func layBomItemByLineKey(card *entity.TechCard, lineKey string) sql.NullInt64 {
	if card == nil || lineKey == "" {
		return sql.NullInt64{}
	}
	for i := range card.BomItems {
		if card.BomItems[i].LineKey == lineKey {
			return sql.NullInt64{Int64: int64(card.BomItems[i].Id), Valid: true}
		}
	}
	return sql.NullInt64{}
}

// productionRunLayError maps the store's typed refusals onto gRPC codes. ONE table, so the two write
// RPCs cannot answer differently for the same fact.
//
//	entity.ValidationError                     → InvalidArgument + BadRequest field violations
//	ErrProductionRunLayConflict                → Aborted (the run's own copy: reload and retry)
//	ErrProductionRunLocked                     → FailedPrecondition
//	ErrProductionRunLayNotApplicable           → FailedPrecondition, reason lay_plan_not_applicable
//	ErrProductionRunLayNotFound                → NotFound
//	sql.ErrNoRows                              → NotFound (the run itself)
//	FK violation                               → InvalidArgument
func (s *Server) productionRunLayError(ctx context.Context, op string, runID int, err error) error {
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		return apierr.Invalid(ve)
	case errors.Is(err, entity.ErrProductionRunLayConflict):
		return status.Error(codes.Aborted, productionRunLayConflictMsg)
	case errors.Is(err, entity.ErrProductionRunLayNotApplicable):
		return apierr.FailedPrecondition(entity.NewFieldViolation("run_id",
			entity.ProductionRunLayNotApplicableKey, productionRunLayNotApplicableMsg,
			"plan an auxiliary run through its own step 1 — there is no cloth to lay here"))
	case errors.Is(err, entity.ErrProductionRunLocked):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrProductionRunLayNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "production run not found")
	case s.repo.IsErrForeignKeyViolation(err):
		return status.Error(codes.InvalidArgument, "настил references a missing run, colourway, BOM line or раскладка")
	}
	slog.Default().ErrorContext(ctx, "production run lay write failed",
		slog.String("op", op), slog.Int("run_id", runID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't "+op+" the настил; try again")
}

// layMarkerCache is the memo of §14 п.16: ONE GetMarker and ONE protojson parse per distinct
// marker_id per request.
//
// Without it ListProductionRunLays would distil a blob per SECTION, and a marker legitimately stands
// in several sections of several настилов (a ступенчатый настил is exactly that). The cache is
// per-request and lives no longer than the call: markers are edited elsewhere, and a longer-lived
// cache would serve yesterday's geometry to today's verdict.
//
// It is NEVER reachable from the runs LIST: nothing in ListProductionRuns builds one, and the plan
// is loaded only by the run's own detail RPCs.
type layMarkerCache struct {
	repo dependency.TechCards
	byID map[int]dto.LayPlanMarker
}

func newLayMarkerCache(repo dependency.TechCards) *layMarkerCache {
	return &layMarkerCache{repo: repo, byID: make(map[int]dto.LayPlanMarker)}
}

// get returns the distilled marker, loading and parsing it at most once. The negative case is NOT
// cached: a marker that failed to load is a fact about this attempt, and a store error remembered as
// «absent» would turn one hiccup into a page-wide verdict.
func (c *layMarkerCache) get(ctx context.Context, markerID int) (dto.LayPlanMarker, error) {
	if m, ok := c.byID[markerID]; ok {
		return m, nil
	}
	row, err := c.repo.GetMarker(ctx, markerID)
	if err != nil {
		return dto.LayPlanMarker{}, err
	}
	// THE distillation, and the only one: the legacy состав is folded in from the summary here (a blob
	// of schema < 4 carries none), so no caller has to remember to do it and none can do it twice.
	m := dto.DistilLayPlanMarker(row)
	c.byID[markerID] = m
	return m, nil
}

func (c *layMarkerCache) all() map[int]dto.LayPlanMarker { return c.byID }

// layPlanLayByKey is a test-facing convenience: the настил a response carries under one key.
func layPlanLayByKey(resp *pb_admin.ListProductionRunLaysResponse, layKey string) *pb_common.ProductionRunLay {
	for _, l := range resp.GetLays() {
		if l.GetLayKey() == layKey {
			return l
		}
	}
	return nil
}
