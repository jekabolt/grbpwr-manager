package admin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// readinessStageOrder is the lifecycle indexed by entity.TechCardStageOrdinal, so "the next stage"
// is a +1 on the ordinal the regression guard already uses — one order of stages in the codebase,
// not two that can drift.
var readinessStageOrder = []entity.TechCardStage{
	entity.TechCardStageIdea, entity.TechCardStageProto, entity.TechCardStageFit,
	entity.TechCardStageSMS, entity.TechCardStagePP, entity.TechCardStageProd,
}

// GetTechCardReadiness reports what is still missing before a style can advance a stage or be
// released. ADVISORY: it computes and returns, it never blocks a write — UpdateTechCard keeps stage
// and approval_state free-standing, and the only stage rule the server enforces is the backward one.
func (s *Server) GetTechCardReadiness(ctx context.Context, req *pb_admin.GetTechCardReadinessRequest) (*pb_admin.GetTechCardReadinessResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	facts, card, err := s.repo.TechCards().GetTechCardReadinessSnapshot(ctx, tcID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't get tech card readiness", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't get tech card readiness")
	}
	if card == nil {
		slog.Default().ErrorContext(ctx, "readiness snapshot returned a nil tech card", slog.Int("tech_card_id", tcID))
		return nil, status.Error(codes.Internal, "can't get tech card readiness")
	}
	staleSignoffs := staleApprovedSignoffSections(card)
	// Р4: the `patterns` row is answered from the Ф6.3 size index instead of from
	// tech_card_size_pattern.size_id, which is a storage artefact the client fills with the smallest
	// size of the range. A read failure degrades to «no verdict» rather than failing the whole
	// checklist: this row is advisory, and one unreadable derived table must not blank the other six.
	patternIndex, ierr := s.repo.TechCards().GetTechCardPatternSizeIndex(ctx, tcID)
	if ierr != nil {
		slog.Default().ErrorContext(ctx, "can't load pattern size index for readiness",
			slog.String("err", ierr.Error()), slog.Int("tech_card_id", tcID))
		patternIndex = nil
	}

	resp := &pb_admin.GetTechCardReadinessResponse{
		CurrentStage: dto.ConvertEntityTechCardStageToPb(facts.Stage),
		NextStage:    pb_common.TechCardStage_TECH_CARD_STAGE_UNKNOWN,
	}
	if next, ok := nextTechCardStage(facts.Stage); ok {
		resp.NextStage = dto.ConvertEntityTechCardStageToPb(next)
		resp.NextStageRequirements = nextStageRequirements(next, facts, card, patternIndex)
	}
	// An empty checklist (prod, or an unrecognised stage) reads as ready, per the field's contract
	// "every next_stage_requirements entry is met"; a client gates on next_stage != UNKNOWN.
	resp.NextStageReady = allReadinessMet(resp.NextStageRequirements)
	// Слоты, из-за которых цена карточки СЕГОДНЯ не считается (Ф1). Отдельный вход, потому что
	// facts — это одна SQL-строка, а этот ответ выводится из геометрии, спецификации и рецептов.
	resp.ReleaseRequirements = releaseRequirements(facts, staleSignoffs,
		dto.TechCardCostBlockers(card, s.costingFx(ctx)), dto.TechCardAssemblyBlocker(card))
	resp.ReleaseReady = allReadinessMet(resp.ReleaseRequirements)
	// ЗАМЕЧАНИЯ СЧИТАЮТСЯ ПОСЛЕ ОБОИХ *_ready И НЕ УЧАСТВУЮТ НИ В ОДНОМ ИЗ НИХ. Это не порядок
	// строк, а всё содержание конструкции: замечание — совет, и карточка с полным их набором
	// обязана оставаться релизуемой (потому оно и не строка чек-листа — см. шапку
	// dto.TechCardAdvisories).
	assembly, variants := s.readinessAssembly(ctx, tcID)
	for _, a := range dto.TechCardAdvisories(card, assembly, variants) {
		resp.Advisories = append(resp.Advisories, &pb_admin.TechCardReadinessAdvice{Key: a.Key, Text: a.Text})
	}
	return resp, nil
}

// readinessAssembly загружает сборочную ведомость карточки и цвета её компонентов — два чтения,
// которых у чек-листа до сих пор не было.
//
// ОШИБКА ЛЮБОГО ИЗ ДВУХ НЕ РОНЯЕТ ВЕСЬ ЧЕК-ЛИСТ: возвращается nil, и проверка «компонента нет в
// спецификации» просто молчит. Ровно та же деградация, что у индекса размеров выкроек выше, и по
// той же причине — одна недоступная производная таблица не должна гасить остальные строки ответа.
// Молчание здесь безопасно вдвойне: проверка совещательная, и её отсутствие никого не блокирует.
func (s *Server) readinessAssembly(ctx context.Context, tcID int) ([]entity.StyleAssembly, map[int][]entity.TechCardOutputVariant) {
	assembly, err := s.repo.TechCards().ListStyleAssembly(ctx, tcID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load style assembly for readiness advisories",
			slog.String("err", err.Error()), slog.Int("tech_card_id", tcID))
		return nil, nil
	}
	// Выключенные строки ведомости на изделие не идут, поэтому их цвета не читаются: тот же отбор,
	// что у упаковочной спецификации (assemblyForSize), и та же экономия одного запроса.
	componentIDs := make([]int, 0, len(assembly))
	seen := map[int]bool{}
	for _, a := range assembly {
		if !a.Active || a.ComponentTechCardId <= 0 || seen[a.ComponentTechCardId] {
			continue
		}
		seen[a.ComponentTechCardId] = true
		componentIDs = append(componentIDs, a.ComponentTechCardId)
	}
	if len(componentIDs) == 0 {
		return assembly, nil
	}
	variants, err := s.repo.TechCards().ListOutputVariantsByCardIds(ctx, componentIDs)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't load component colour variants for readiness advisories",
			slog.String("err", err.Error()), slog.Int("tech_card_id", tcID))
		// Ведомость без цветов отдавать НЕЛЬЗЯ: entity.ResolveAssemblyOutput на пустом списке
		// вариантов уходит в легаси-ветку и разрешает выход по tech_card.output_material_id — то
		// есть по полю, которое у карточки с цветовыми вариантами устарело по построению. Обвинение
		// по устаревшему полю хуже молчания, поэтому молчит вся проверка целиком.
		return nil, nil
	}
	return assembly, variants
}

// nextTechCardStage returns the stage one lifecycle step ahead of s. Not ok at prod (the last stage)
// or for a stage the codebase does not know.
func nextTechCardStage(s entity.TechCardStage) (entity.TechCardStage, bool) {
	ord, ok := entity.TechCardStageOrdinal(s)
	if !ok || ord+1 >= len(readinessStageOrder) {
		return "", false
	}
	return readinessStageOrder[ord+1], true
}

// nextStageRequirements is the studio's entry condition for each stage, in display order. Keys are
// stable machine names a client switches on; labels are the sentence it shows. `first_sample` is
// reused for the proto-sample condition on purpose — both rows ask for the style's first sewn
// prototype, they only differ in how specific the demand is.
func nextStageRequirements(target entity.TechCardStage, f entity.TechCardReadinessFacts,
	card *entity.TechCard, patternIndex map[string]entity.PatternSizeIndexRow) []*pb_admin.TechCardReadinessRequirement {
	switch target {
	case entity.TechCardStageProto:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("style_number", "the style has a style number", f.HasStyleNumber, "no style number set"),
			readinessReq("bom_fabric", "the BOM has at least one fabric line", f.BomFabricLines > 0, "no fabric line in the BOM"),
			readinessReq("first_sample", "at least one sample exists", f.Samples > 0, "no sample recorded"),
		}
	case entity.TechCardStageFit:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("fitting_recorded", "a fitting has been recorded", f.Fittings > 0, "no fitting recorded"),
			readinessReq("first_sample", "a proto sample exists", f.ProtoSamples > 0, "no proto sample recorded"),
		}
	case entity.TechCardStageSMS:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("fit_approved", "a fitting was approved", f.FittingsApproved > 0, "no fitting has an approved verdict"),
			readinessReq("fittings_resolved", "every fitting change request is resolved",
				f.OpenChangeRequests == 0, openChangeRequestDetail(f.OpenChangeRequests)),
		}
	case entity.TechCardStagePP:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("sms_sample", "a salesman sample exists", f.SmsSamples > 0, "no sms sample recorded"),
			readinessReq("colorway_linked", "at least one live colourway", f.LiveColorways > 0, "no live colourway"),
			readinessReq("bom_linked", "every BOM slot has an article (a default, or a pin in every live colourway)",
				f.BomLines > 0 && f.BomLinkedLines == f.BomLines, bomLinkedDetail(f)),
		}
	case entity.TechCardStageProd:
		return []*pb_admin.TechCardReadinessRequirement{
			readinessReq("pp_sample", "a pre-production sample exists", f.PpSamples > 0, "no pp sample recorded"),
			readinessReq("run_planned", "a production run is planned", f.ProductionRuns > 0, "no production run planned"),
			patternsRequirement(card, patternIndex),
		}
	}
	return nil
}

// releaseRequirements is what a card needs before approval_state may go RELEASED — the spec the
// factory is handed. Independent of the stage checklist: a sampling-complete style can still be
// un-releasable (no costing currency, a colourway whose lab dip nobody signed).
func releaseRequirements(f entity.TechCardReadinessFacts, staleSignoffs []entity.TechCardSignoffSection,
	costBlockers []string, assemblyBlocker string) []*pb_admin.TechCardReadinessRequirement {
	return []*pb_admin.TechCardReadinessRequirement{
		readinessReq("style_number", "the style has a style number", f.HasStyleNumber, "no style number set"),
		readinessReq("size_range", "the size range is not empty", f.Sizes > 0, "no sizes in the range"),
		readinessReq("bom_fabric", "the BOM has at least one fabric line", f.BomFabricLines > 0, "no fabric line in the BOM"),
		readinessReq("costing", "the costing is filled in with a currency",
			f.HasCosting && f.HasCostingCurrency, costingDetail(f)),
		// «Костинг ЗАПОЛНЕН» и «цена СЧИТАЕТСЯ» — разные утверждения, и до Ф1 разойтись им было
		// негде: расчёт брал только вписанные нормы, а вписанная норма считается всегда. С выводом
		// нормы из геометрии появился слот, который в изделие входит, а посчитаться не может
		// (устарели выкройки, спорят пины, нет ширины) — и цена карточки становится непосчитанной,
		// НЕ ТРОГАЯ при этом product.cost_price: та остаётся прежней, её читают бухгалтерия и COGS
		// проданного, и обнулять её задним числом хуже, чем оставить. Без этой строки карточка
		// выпускалась бы со стухшей каталожной ценой, а чек-лист сообщал бы, что всё в порядке.
		//
		// Причина называется ПОИМЁННО, потому что общее «нормы нет» отправляет вписывать число
		// руками — то есть ровно туда, откуда эта фаза уводит.
		readinessReq("costing_computes", "the cost still computes for every measured fabric",
			len(costBlockers) == 0, "cost no longer computes: "+strings.Join(costBlockers, "; ")),
		readinessReq("colorway_linked", "at least one live colourway", f.LiveColorways > 0, "no live colourway"),
		// Vacuously met with no colourways: the colorway_linked row above already carries that
		// failure, and a checklist that reds the same fact twice reads as two separate problems.
		readinessReq("lab_dip", "every live colourway has an approved lab dip",
			f.LabDipPendingColorways == 0, labDipDetail(f)),
		readinessReq("signoffs", "every recorded sign-off is approved",
			f.Signoffs > 0 && f.SignoffsApproved == f.Signoffs && len(staleSignoffs) == 0,
			signoffsDetail(f, staleSignoffs)),
		// Совещательная половина правила 4. Жёсткий отказ живёт в конвертере и срабатывает уже на
		// попытке сохранить RELEASED — без этой строки чек-лист обещал бы готовность, которую
		// сохранение тут же опровергнет. На неразмеченной карточке выполняется вакуумно: узлов
		// нет, сходиться нечему, и это все сегодняшние карточки.
		readinessReq("construction_graph", "the assembly graph converges into one garment",
			assemblyBlocker == "", assemblyBlocker),
	}
}

// readinessReq builds one checklist row. A met row carries no detail — the detail field is the
// explanation of a failure, so the caller can pass it unconditionally.
func readinessReq(key, label string, met bool, detail string) *pb_admin.TechCardReadinessRequirement {
	if met {
		detail = ""
	}
	return &pb_admin.TechCardReadinessRequirement{Key: key, Label: label, Met: met, Detail: detail}
}

// readinessUnknown builds a row the server COULD NOT ANSWER. Not met, and — the load-bearing half —
// not counted as unmet either, so an absent instrument never blocks a stage the way a real failure
// does. Same discipline the run-readiness gate's UNKNOWN keeps, and for the same reason: folding it
// into either known value states something the server does not know.
func readinessUnknown(key, label, detail string) *pb_admin.TechCardReadinessRequirement {
	return &pb_admin.TechCardReadinessRequirement{Key: key, Label: label, Met: false, Unknown: true, Detail: detail}
}

// allReadinessMet reports «nothing is known to be missing». UNKNOWN rows are SKIPPED, not counted as
// failures: they mean the server had no instrument, and treating that as a failure would block every
// card at once on the day a check admits it cannot answer.
func allReadinessMet(rows []*pb_admin.TechCardReadinessRequirement) bool {
	for _, r := range rows {
		if !r.Met && !r.Unknown {
			return false
		}
	}
	return true
}

func openChangeRequestDetail(n int) string {
	if n == 1 {
		return "1 fitting change request is still open"
	}
	return fmt.Sprintf("%d fitting change requests are still open", n)
}

// bomLinkedDetail distinguishes "nothing to link" from "partly linked": an empty BOM is not
// vacuously compliant here, since no other row on the pp checklist notices it.
func bomLinkedDetail(f entity.TechCardReadinessFacts) string {
	if f.BomLines == 0 {
		return "the BOM is empty"
	}
	return fmt.Sprintf("%d of %d BOM slots have no article (no default and not pinned by every live colourway)", f.BomLines-f.BomLinkedLines, f.BomLines)
}

// patternsRequirement answers «every size in the range has a pattern» — and it is the ONE row of
// this checklist that can say «I do not know» (Р4).
//
// IT USED TO LIE, in both directions. It counted DISTINCT tech_card_size_pattern.size_id against the
// size range, but the client files every sheet of a card under the SMALLEST size of the range and
// says so in its own comment — it is a storage artefact, not a statement about the file. So a card
// with five sizes and one graded DXF read as «1 of 5» and could never pass, while a card with one
// flat sheet per size read as fully covered whether or not those files contain those sizes.
//
// The false PASS is the worse of the two, so the row stops asserting «verified» immediately rather
// than waiting for the Ф6.3 index to be populated everywhere. UNKNOWN does not count as unmet, so no
// card becomes newly blocked on the day this ships; the honest check switches itself on card by card
// as operators press «⌕ размеры в файлах».
func patternsRequirement(card *entity.TechCard, index map[string]entity.PatternSizeIndexRow) *pb_admin.TechCardReadinessRequirement {
	const (
		key   = "patterns"
		label = "every size in the range has a pattern"
	)
	ok, unknown, missing := dto.TechCardPatternSizeVerdict(card, index)
	switch {
	case ok:
		return readinessReq(key, label, true, "")
	case unknown != "":
		return readinessUnknown(key, label, unknown)
	default:
		return readinessReq(key, label, false,
			fmt.Sprintf("the pattern files do not contain %s", strings.Join(missing, ", ")))
	}
}

func costingDetail(f entity.TechCardReadinessFacts) string {
	if !f.HasCosting {
		return "no costing recorded"
	}
	return "the costing has no currency"
}

func labDipDetail(f entity.TechCardReadinessFacts) string {
	return fmt.Sprintf("%d of %d colourways have no approved lab dip", f.LabDipPendingColorways, f.LiveColorways)
}

func signoffsDetail(f entity.TechCardReadinessFacts, stale []entity.TechCardSignoffSection) string {
	if f.Signoffs == 0 {
		return "no sign-off recorded"
	}
	if len(stale) == 1 {
		return fmt.Sprintf("%s approval is stale", stale[0])
	}
	if len(stale) > 1 {
		sections := make([]string, 0, len(stale))
		for _, section := range stale {
			sections = append(sections, string(section))
		}
		return fmt.Sprintf("approvals are stale: %s", strings.Join(sections, ", "))
	}
	return fmt.Sprintf("%d of %d sign-offs are not approved", f.Signoffs-f.SignoffsApproved, f.Signoffs)
}

// staleApprovedSignoffSections compares server-owned signed digests with the current enriched card
// using the exact projection used when an approval is stamped. Empty legacy digests are unverifiable
// and therefore stale: release readiness must never treat an approval of unknown content as current.
func staleApprovedSignoffSections(card *entity.TechCard) []entity.TechCardSignoffSection {
	if card == nil {
		return nil
	}
	current := dto.TechCardSectionDigests(&card.TechCardInsert)
	stale := make(map[entity.TechCardSignoffSection]bool)
	for _, signoff := range card.Signoffs {
		if signoff.State != entity.SignoffStateApproved {
			continue
		}
		if !signoff.SignedDigest.Valid || signoff.SignedDigest.String == "" ||
			signoff.SignedDigest.String != current[signoff.Section] {
			stale[signoff.Section] = true
		}
	}
	order := []entity.TechCardSignoffSection{
		entity.SignoffDesign, entity.SignoffConstruction, entity.SignoffMaterials,
		entity.SignoffColour, entity.SignoffLabels, entity.SignoffPackaging, entity.SignoffCosting,
	}
	out := make([]entity.TechCardSignoffSection, 0, len(stale))
	for _, section := range order {
		if stale[section] {
			out = append(out, section)
		}
	}
	return out
}
