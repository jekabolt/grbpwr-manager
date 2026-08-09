package dto

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ОТДАЧА ПЛАНА НАСТИЛОВ (Ф4, шаг 10) — the projection from stored rows onto the three RPCs' wire
// shapes. Everything here is a PURE FUNCTION of what the handler loaded; nothing in this file
// touches a store, a clock or a context.
//
// WHY IT IS HERE AND NOT IN THE HANDLER. Two of the facts a lay needs — «какой слот BOM кроит эту
// деталь» and «какой артикул колорвей пинит на этот слот» — are resolved by planBomLine, which is
// UNEXPORTED in production_material_plan.go and must stay the single resolver: понимание только
// позиционного bom_item_index уже давало ПУСТОЙ материал-план на бете (§14 п.5), and a second
// resolver in package admin would be exactly that bug with a new address. A handler in another
// package cannot call it; this file can, so the projection lives beside the arithmetic it depends
// on and the handler stays what a handler should be — loads, error codes, permissions.
//
// ЧЕГО ЗДЕСЬ НЕТ. No coverage arithmetic (ComputeLayCoverage), no fitness predicate
// (production_lay_checks.go), no blob distillation rule (production_lay_yield.go). This file CALLS
// all three and adds nothing to any of them; every number it emits is one of theirs.

// LayPlanMarker is ONE раскладка as the lay plan reads it: the row's own facts plus its blob,
// distilled EXACTLY ONCE.
//
// The single distillation is the contract, not an optimisation (§14 п.16). A marker stands in
// several sections of several lays, and every section asks the same blob the same questions; a
// struct that carried the raw string would invite one protojson parse per section, per size, per
// piece. The caller memoises by marker_id and hands the result in here.
type LayPlanMarker struct {
	Summary entity.TechCardMarkerSummary
	// Yield is nil when the blob could not be distilled — see YieldErr. NIL IS NEVER «ничего не
	// кроит»: the checks read it as UNKNOWN and coverage refuses to prove anything off the section.
	Yield    *MarkerYield
	YieldErr error
	// Caveats are the sentences the distillation itself produced — a legacy blob whose состав could
	// not be supplied from the summary, above all. They travel to the response's caveats.
	Caveats []string
}

// DistilLayPlanMarker parses one stored marker blob and FOLDS IN THE LEGACY СОСТАВ.
//
// THE FOLD IS THE CALLER'S JOB AND THIS IS THE CALLER. MarkerYieldFromBlob says so in as many
// words: a blob of schema < 4 carries no composition at all, because before Ф2 the состав lived on
// the marker SUMMARY — a row the pure yield file never sees. Without the fold TotalUnits stays 0,
// CompositionKnown() answers false, and every per-layer question about that marker returns UNKNOWN.
//
// It cannot divide by zero and that is structural rather than lucky: PerLayerInstances refuses
// outright unless CompositionKnown(), splitByComposition guards totalUnits <= 0, and
// WithSummaryComposition itself refuses a garment count below 1 — so the only reachable states are
// «известный состав с положительным TotalUnits» and «UNKNOWN». There is no path on which a marker
// silently behaves as if it cut one garment.
func DistilLayPlanMarker(m *entity.TechCardMarker) LayPlanMarker {
	if m == nil {
		return LayPlanMarker{YieldErr: fmt.Errorf("marker row is missing")}
	}
	out := LayPlanMarker{Summary: m.TechCardMarkerSummary}
	y, err := MarkerYieldFromBlob(m.Layout)
	if err != nil {
		out.YieldErr = err
		return out
	}
	if !y.CompositionKnown() {
		comp := m.CompositionOrLegacy()
		switch {
		case len(comp) == 1:
			merged, ferr := y.WithSummaryComposition(comp[0].SizeId, comp[0].Quantity)
			if ferr != nil {
				out.Caveats = append(out.Caveats, fmt.Sprintf(
					"marker %q: the composition from the summary was not applied (%v) — there is no way to say what it cuts",
					markerLabel(m.TechCardMarkerSummary), ferr))
			} else {
				y = merged
			}
		case len(comp) == 0:
			// A раскладка that no longer states what it cuts (a Down that dropped the projection, a
			// partial restore). Withheld, never read as one garment — the same refusal
			// MarkerScalarNormRefusal makes on the costing side.
			out.Caveats = append(out.Caveats, fmt.Sprintf(
				"marker %q does not say how many units it cuts — coverage over it is not computed",
				markerLabel(m.TechCardMarkerSummary)))
		default:
			// A multi-size состав beside a blob that predates состав. WithSummaryComposition takes ONE
			// size by design, and picking one of several would be an invention.
			out.Caveats = append(out.Caveats, fmt.Sprintf(
				"marker %q: a schema-%d blob carries no composition, while the summary names %d sizes — coverage over it is not computed",
				markerLabel(m.TechCardMarkerSummary), y.SchemaVersion, len(comp)))
		}
	}
	out.Yield = &y
	return out
}

func markerLabel(m entity.TechCardMarkerSummary) string {
	if n := strings.TrimSpace(m.Name); n != "" {
		return n
	}
	return fmt.Sprintf("#%d", m.Id)
}

// LayPlanInput is everything ListProductionRunLays reads, already loaded.
type LayPlanInput struct {
	Run  *entity.ProductionRun
	Card *entity.TechCard
	Lays []entity.ProductionRunLay
	// Markers is marker_id → its distilled facts, for every marker ANY section names. A missing key is
	// tolerated and produces a BLOCKER on the section rather than a panic; the handler is expected to
	// have loaded them all.
	Markers map[int]LayPlanMarker
	// RunMarkers are the раскладки of THIS run — what the editor picks from.
	RunMarkers             []entity.TechCardMarkerSummary
	Materials              map[int]entity.MaterialWithPrice
	NarrowestMeasuredLotCm map[int]decimal.NullDecimal
	Settings               *entity.WorkshopSettings
}

// BuildProductionRunLayPlan assembles the whole ListProductionRunLays response.
//
// ПОКРЫТИЕ СЧИТАЕТСЯ РОВНО ОДИН РАЗ, ComputeLayCoverage'ом, and everything downstream reads its
// result: the grid, the popover diagnostics, and the per-lay перекрой. §6.4 forbids a second
// definition of coverage, and the cheapest way to keep that promise is to have no second call.
func BuildProductionRunLayPlan(in LayPlanInput) *pb_admin.ListProductionRunLaysResponse {
	if in.Card == nil || in.Run == nil {
		return &pb_admin.ListProductionRunLaysResponse{
			Applicable:          false,
			NotApplicableReason: "the lay plan is not built: the run or the card is not loaded",
		}
	}
	if in.Card.Purpose == entity.TechCardPurposeAuxiliary {
		// §1.9 / §6.3: у вспомогательной карточки нет ни колорвеев, ни деталей кроя. Отдаётся ЯВНО, не
		// пустым списком — пустой список читается как приглашение построить настил.
		return &pb_admin.ListProductionRunLaysResponse{
			Applicable:          false,
			NotApplicableReason: entity.ProductionRunLayNotApplicableKey,
		}
	}

	b := &layPlanBuilder{in: in, seenCaveat: map[string]bool{}}
	cov := ComputeLayCoverage(LayCoverageInput{
		Card:  in.Card,
		Lines: in.Run.Lines,
		Lays:  b.coverageLays(),
	})

	out := &pb_admin.ListProductionRunLaysResponse{
		Applicable:  true,
		Coverage:    cov.CoverageCellsPb(),
		PieceYields: cov.PieceYieldsPb(),
	}
	// UNKNOWN'ы СКЛАДЫВАЮТСЯ, а не заменяются: LayCoverage counts what COVERAGE could not answer, and
	// says so in its own comment. Проверки годности молчат о своих — их считаем здесь.
	unknown := cov.UnknownCount
	for _, c := range cov.Caveats {
		b.addCaveat("%s", c)
	}
	for _, f := range cov.Findings {
		// Findings have no field of their own on this message, and dropping them would undo §14 п.4:
		// a run line no настил can cover has to SAY SO, not quietly shrink the denominator.
		b.addCaveat("%s: %s", f.Key, f.Detail)
	}
	for _, m := range in.Markers {
		for _, c := range m.Caveats {
			b.addCaveat("%s", c)
		}
	}

	for i := range in.Lays {
		lay, checks := b.lay(&in.Lays[i], cov)
		out.Lays = append(out.Lays, lay)
		unknown += countUnknownChecks(checks)
	}

	out.RunMarkers = make([]*pb_common.TechCardMarkerSummary, 0, len(in.RunMarkers))
	for _, m := range in.RunMarkers {
		out.RunMarkers = append(out.RunMarkers, TechCardMarkerSummaryToPb(m))
	}
	out.UnknownCount = int32(unknown)
	out.Caveats = b.sortedCaveats()
	return out
}

// countUnknownChecks is the server's own count of «не смогли проверить». It is emitted as a field so
// the client never recomputes it: a client that summed the badges it happens to render would report
// a different number the moment it filtered one of them out.
func countUnknownChecks(checks []LayCheck) int {
	n := 0
	for _, c := range checks {
		if c.Status == LayCheckStatusUnknown {
			n++
		}
	}
	return n
}

type layPlanBuilder struct {
	in         LayPlanInput
	caveats    []string
	seenCaveat map[string]bool
}

func (b *layPlanBuilder) addCaveat(format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	if s == "" || b.seenCaveat[s] {
		return
	}
	b.seenCaveat[s] = true
	b.caveats = append(b.caveats, s)
}

func (b *layPlanBuilder) sortedCaveats() []string {
	out := append([]string(nil), b.caveats...)
	sort.Strings(out)
	return out
}

// coverageLays projects the stored lays onto what ComputeLayCoverage reads. The distilled blob rides
// along per section — one parse per marker for the whole request, not one per section.
func (b *layPlanBuilder) coverageLays() []Lay {
	out := make([]Lay, 0, len(b.in.Lays))
	for i := range b.in.Lays {
		l := &b.in.Lays[i]
		cl := Lay{
			LayKey:     l.LayKey,
			Name:       l.Name,
			ColorwayID: l.ColorwayId,
			Mode:       LayFaceMode(l.Mode),
		}
		if l.BomItemId.Valid {
			cl.BomItemID = l.BomItemId.Int64
		}
		for _, s := range l.Sections {
			sec := LaySection{MarkerID: s.MarkerId, Plies: s.Plies}
			m, ok := b.in.Markers[s.MarkerId]
			switch {
			case !ok:
				sec.YieldErr = fmt.Errorf("marker %d is not loaded", s.MarkerId)
			case m.YieldErr != nil:
				sec.YieldErr = m.YieldErr
			case m.Yield != nil:
				sec.Yield = *m.Yield
			}
			cl.Sections = append(cl.Sections, sec)
		}
		out = append(out, cl)
	}
	return out
}

// lay builds one настил's wire message and returns every finding it carries (lay-level and
// section-level both), so the caller can count the UNKNOWNs without walking the proto back.
func (b *layPlanBuilder) lay(l *entity.ProductionRunLay, cov LayCoverage) (*pb_common.ProductionRunLay, []LayCheck) {
	card := b.in.Card
	mode := LayFaceMode(l.Mode)
	materialID := LayArticleMaterialId(card, l.ColorwayId, bomItemIdOf(l))
	article := b.article(materialID)

	sections := make([]LayCheckSection, 0, len(l.Sections))
	for _, s := range l.Sections {
		sections = append(sections, LayCheckSection{
			SectionKey: s.SectionKey,
			Plies:      s.Plies,
			Marker:     b.markerFacts(s.MarkerId),
		})
	}

	in := LayCheckInput{
		Lay: LayIdentity{
			RunId:      l.RunId,
			TechCardId: card.Id,
			ColorwayId: l.ColorwayId,
			BomItemId:  l.BomItemId,
			BomLineKey: l.BomLineKey,
			Name:       l.Name,
		},
		Mode:     mode,
		Sections: sections,
		Article:  article,
		// ЛОТ ПОДАЁТСЯ В ПРОВЕРКИ, ИНАЧЕ `lay_lot_width` ВЕЧНО UNKNOWN. Замер ширины рулона приезжает
		// джойном вместе с настилом (LotMeasuredWidthCm) — сырым, с кромкой; кромку снимает сам
		// предикат, и снимать её здесь значило бы вычесть дважды, всегда в разрешающую сторону.
		Lot: LayLotFacts{
			LotId:           l.LotId,
			LotCode:         l.LotCode,
			MeasuredWidthCm: l.LotMeasuredWidthCm,
		},
		Limits:        b.limits(),
		BomLines:      entity.FabricDirectionLinesOfBom(card.BomItems),
		PieceSymmetry: cardPieceSymmetry(card),
		QtySnapshot:   layQtyEntries(l.QtySnapshot),
		QtyCurrent:    layQtyEntries(l.QtyCurrent),
	}

	checks := ProductionLayChecks(in)
	// ПЕРЕКРОЙ СЧИТАЕТСЯ НА РАЗМЕР, и результат схлопывается в ОДНУ находку. See layOvercutCheck for
	// the argument; the substitution keeps §8's order of the table, which is part of the contract
	// (a client diffing two reads of one настил must not have to sort).
	for i := range checks {
		if checks[i].Key == LayCheckKeyOvercut {
			checks[i] = b.layOvercutCheck(l, mode, cov)
		}
	}

	stack := LayStackHeightVerdict(in.TotalPlies(), article.FabricThicknessMm, in.Limits.MaxStackHeightCm)

	cloth := decimal.Zero
	pbSections := make([]*pb_common.ProductionRunLaySection, 0, len(l.Sections))
	all := append([]LayCheck(nil), checks...)
	for i, s := range l.Sections {
		facts := sections[i].Marker
		length := facts.UsedLengthCm.Mul(decimal.NewFromInt(int64(s.Plies)))
		cloth = cloth.Add(length)
		secChecks := ProductionLaySectionChecks(in, sections[i])
		all = append(all, secChecks...)
		pbSections = append(pbSections, &pb_common.ProductionRunLaySection{
			Id:                int32(s.Id),
			SectionKey:        s.SectionKey,
			MarkerId:          int32(s.MarkerId),
			MarkerName:        facts.Name,
			Plies:             int32(s.Plies),
			Position:          int32(s.Position),
			MarkerLengthCm:    pbDecimalFromDecimal(facts.UsedLengthCm),
			MarkerWidthCm:     pbDecimalFromDecimal(facts.FabricWidthCm),
			SectionLengthCm:   pbDecimalFromDecimal(length),
			MarkerComposition: b.markerCompositionPb(s.MarkerId),
			Checks:            LayChecksPb(secChecks),
		})
	}

	// Концевые потери — на ОДИН КОНЕЦ ОДНОГО СЛОЯ, полные = 2 × end_loss × Σ слоёв (§7.2). Ступенчатый
	// настил считается по слоям СЕКЦИЙ, то есть консервативно: занижение потерь дороже завышения.
	endLoss := l.EndLossCm.Mul(decimal.NewFromInt(2)).Mul(decimal.NewFromInt(int64(in.TotalPlies())))

	out := &pb_common.ProductionRunLay{
		Id:              int32(l.Id),
		LayKey:          l.LayKey,
		ColorwayId:      int32(l.ColorwayId),
		ColorwayName:    l.ColorwayName,
		BomItemId:       int32(bomItemIdOf(l)),
		BomLineKey:      l.BomLineKey,
		BomItemName:     pbStringFromNull(l.BomItemName),
		MaterialId:      int32(materialID),
		MaterialName:    article.Name,
		Mode:            layModePb(l.Mode),
		EndLossCm:       pbDecimalFromDecimal(l.EndLossCm),
		Name:            l.Name,
		Note:            pbStringFromNull(l.Note),
		DisplayOrder:    int32(l.DisplayOrder),
		LockVersion:     int32(l.LockVersion),
		Sections:        pbSections,
		QtySnapshot:     layQtyEntriesPb(l.QtySnapshot),
		QtyCurrent:      layQtyEntriesPb(l.QtyCurrent),
		QuantitiesStale: l.QuantitiesStale,
		TotalPlies:      int32(in.TotalPlies()),
		ClothLengthCm:   pbDecimalFromDecimal(cloth),
		EndLossTotalCm:  pbDecimalFromDecimal(endLoss),
		PlannedLengthCm: pbDecimalFromDecimal(cloth.Add(endLoss)),
		// ВЫСОТА ОТСУТСТВУЕТ, А НЕ РАВНА НУЛЮ, когда её нечем посчитать: «0 см, влезает» — самый
		// уверенный неверный ответ, который эта фаза может дать (§13 п.13).
		StackHeightCm: pbDecimalFromNull(stack.HeightCm),
		Checks:        LayChecksPb(checks),
		CreatedBy:     l.CreatedBy,
		UpdatedBy:     l.UpdatedBy,
		CreatedAt:     timestamppb.New(l.CreatedAt),
		UpdatedAt:     timestamppb.New(l.UpdatedAt),

		// РУЛОН. Обе половины едут ВСЕГДА, потому что различать их обязан клиент: пустой id при
		// НЕПУСТОМ коде — это «лот удалён из справочника, но настил всё ещё может его НАЗВАТЬ» (FK
		// стоит на SET NULL, и снимок кода — то, чем эта уступка оплачена), а обе пустые — просто
		// незаполненное поле. История, которую нельзя переписать, и пустая форма.
		LotId:   int32(l.LotId.Int64),
		LotCode: l.LotCode,

		// ФАКТ, как его ввёл человек, вместе с тем, КТО и КОГДА. Без автора и времени это просто
		// число в поле; с ними — свидетельство, которое можно оспорить.
		ActualQty:    pbDecimalFromNull(l.ActualQty),
		ActualUom:    pbMaterialUnit(l.ActualUom.String),
		ActualMethod: layActualMethodPb(entity.ProductionLayActualMethod(l.ActualMethod.String)),
		ActualBy:     l.ActualBy,
		ActualAt:     layActualAtPb(l.ActualAt),

		// ДРЕЙФ — В ПРОЦЕНТАХ, а entity считает его ДОЛЕЙ. Умножение живёт ровно здесь, на границе
		// провода, и один раз: имя поля на проводе (actual_drift_percent) — единственное место, где
		// эта единица названа, и разъехаться им нельзя.
		//
		// Отсутствует, а не ноль, когда сравнивать нечего: нет факта, нет плана, либо единица факта
		// не длина (килограммы в сантиметры без плотности и ширины не переводятся). «Нечего
		// сравнить» и «сошлось ровно» обязаны выглядеть по-разному — иначе цех прочитает первое как
		// второе и успокоится.
		ActualDriftPercent: layDriftPercentPb(cloth.Add(endLoss), l),
	}
	return out, all
}

// layActualAtPb keeps «замера не было» absent instead of turning it into the zero instant: a
// timestamp of 1 January year 1 reads as a date, and a date is a claim that somebody measured.
func layActualAtPb(t sql.NullTime) *timestamppb.Timestamp {
	if !t.Valid {
		return nil
	}
	return timestamppb.New(t.Time)
}

// layDriftPercentPb turns the entity's fraction into the wire's percent, or into ABSENCE.
func layDriftPercentPb(plannedCm decimal.Decimal, l *entity.ProductionRunLay) *pb_decimal.Decimal {
	v := entity.ProductionRunLayDrift(plannedCm, *l)
	if !v.Known {
		return nil
	}
	return pbDecimalFromDecimal(v.Drift.Mul(decimal.NewFromInt(100)).Round(2))
}

// layOvercutCheck asks `lay_overcut` ONCE PER SIZE and folds the answers into one finding.
//
// ПОЧЕМУ НА РАЗМЕР. Перекрой — это ткань, которая не станет изделием, and pieces are graded: ten
// spare fronts in size M cannot become size L garments. Asked once over the sizes summed, an excess
// in one size cancels a shortfall in another and the настил reports «ровно столько, сколько нужно»
// while a stack of M panels goes to the bin. So the arithmetic is asked per (колорвей, размер) —
// exactly the cell coverage judged — and LayOvercutCheck is CALLED, never re-implemented.
//
// ПОЧЕМУ ОДНА НАХОДКА НА ВЫХОДЕ. `key` is what a client keys its copy, its deep link and its «я про
// это знаю» state on; N findings under one key would collide in all three. The fold uses
// WorstLayCheckStatus — the same ladder — and names the size in each line of the detail.
//
// НАЗВАННАЯ ГРАНИЦА: когда на одну пару (колорвей, слот) построено ДВА настила, covered_qty — это
// покрытие ПАРЫ, а выкроенное считается по ЭТОМУ настилу. Разница уходит в разрешающую сторону
// (need оказывается больше, чем этот настил выкроил, и перекрой не срабатывает), что предпочтительнее
// обратного: обвинить настил в перекрое за ткань, которую расстелил соседний, значит научить цех не
// верить бейджу.
func (b *layPlanBuilder) layOvercutCheck(l *entity.ProductionRunLay, mode LayFaceMode, cov LayCoverage) LayCheck {
	pieces := b.slotPieces(l)
	var (
		statuses []LayCheckStatus
		details  []string
		label    string
	)
	for _, cell := range cov.Cells {
		if cell.ColorwayID != l.ColorwayId {
			continue
		}
		cuts := b.pieceCuts(l, mode, cell.SizeID, pieces)
		// Известное покрытие — только доказанное. UNKNOWN-клетка отдаёт covered_qty как НИЖНЮЮ границу
		// (см. CoverageCell.CoveredQty), и сравнивать выкроенное с нижней границей значило бы объявлять
		// перекроем то, чего никто не доказал.
		covered := LayCoveredQty{Qty: cell.CoveredQty, Known: cell.Status != CoverageStatusUnknown}
		c := LayOvercutCheck(cuts, covered)
		label = c.Label
		statuses = append(statuses, c.Status)
		if c.Detail != "" {
			details = append(details, fmt.Sprintf("size #%d: %s", cell.SizeID, c.Detail))
		}
	}
	if len(statuses) == 0 {
		// Ни одной клетки этого колорвея: сравнивать не с чем. Canonical shape taken from the predicate
		// itself rather than hand-written, so the label and the prose cannot drift from it.
		return LayOvercutCheck(nil, LayCoveredQty{})
	}
	out := LayCheck{Key: LayCheckKeyOvercut, Label: label, Status: WorstLayCheckStatus(statuses...)}
	if out.Status != LayCheckStatusOK {
		out.Detail = strings.Join(details, "; ")
	}
	return out
}

// pieceCuts is what THIS настил physically cut of each of its slot's pieces at ONE size — §6.2 шаги
// 1-2 summed over its own sections, through the very functions coverage uses.
//
// A piece whose sections could not answer comes back with an INVALID symmetry, i.e. as an UNKNOWN.
// It is deliberately not dropped: a dropped piece is one the fold never judges, and «мы её не
// посчитали» would read as «перекроя по ней нет».
func (b *layPlanBuilder) pieceCuts(l *entity.ProductionRunLay, mode LayFaceMode, sizeID int,
	pieces []*entity.TechCardPiece) []LayPieceCut {

	out := make([]LayPieceCut, 0, len(pieces))
	for _, p := range pieces {
		cut := MarkerPieceCounts{}
		chirality := true
		answered := true
		for _, s := range l.Sections {
			m, ok := b.in.Markers[s.MarkerId]
			if !ok || m.Yield == nil {
				answered = false
				continue
			}
			chirality = chirality && m.Yield.ChiralityKnown()
			inst := m.Yield.PerLayerInstances(p.LineKey, sizeID)
			if !inst.Known {
				answered = false
				continue
			}
			cutBySection, err := LayerCutInstances(inst.Counts, mode, s.Plies)
			if err != nil {
				// Нечётные слои в режиме лицом к лицу: секция не вносит НИЧЕГО (доказанный ноль, не
				// пробел). Вердикт о самом настиле ставит lay_mode_parity, здесь остаётся арифметика.
				continue
			}
			cut.AsDrawn += cutBySection.AsDrawn
			cut.Mirrored += cutBySection.Mirrored
		}
		lp := LayPieceCut{
			PieceLineKey:     p.LineKey,
			PieceName:        p.Name,
			Cut:              cut,
			Symmetry:         p.CutSymmetry,
			PiecesPerGarment: p.PiecesPerGarment,
			ChiralityKnown:   chirality,
		}
		if !answered {
			// «Сколько выкроено» — пол, а не факт. Пол, прочитанный как факт, — это выдуманный ответ в
			// любую сторону, поэтому деталь уходит в UNKNOWN, а причина — в оговорки.
			lp.Symmetry = sql.NullString{}
			b.addCaveat("lay %q: could not count how many of piece %q were cut — the section's marker cannot be read or does not know its composition",
				layNameOf(l), pieceNameOf(p))
		}
		out = append(out, lp)
	}
	return out
}

// slotPieces are the card's cut-pieces this настил is responsible for: those whose slot FOR THIS
// COLOURWAY is the настил's own. Resolved through pieceSlotBomLine — the recipe's resolver, not a
// second one (§14 п.5).
func (b *layPlanBuilder) slotPieces(l *entity.ProductionRunLay) []*entity.TechCardPiece {
	if !l.BomItemId.Valid {
		// Настил без слота (fk_prlay_bom SET NULL) не отвечает ни за одну деталь: сказать, что он кроил,
		// нельзя вовсе. lay_slot_detached уже назвал это блокером.
		return nil
	}
	out := make([]*entity.TechCardPiece, 0, len(b.in.Card.Pieces))
	for i := range b.in.Card.Pieces {
		p := &b.in.Card.Pieces[i]
		m := pieceMaterialForColorway(p, l.ColorwayId)
		if m == nil {
			continue
		}
		bom := pieceSlotBomLine(m, b.in.Card.BomItems)
		if bom == nil || int64(bom.Id) != l.BomItemId.Int64 {
			continue
		}
		out = append(out, p)
	}
	return out
}

// markerFacts projects one loaded marker onto what the fitness predicates read.
//
// A marker the caller did not load comes back as a ZEROED row with only its id, and that is the
// safe direction: LayMarkerScopeCheck then reports «снят с карточки #0 / это КАРТОЧНЫЙ маркер» as a
// BLOCKER instead of the section quietly passing every check.
func (b *layPlanBuilder) markerFacts(markerID int) LayMarkerFacts {
	m, ok := b.in.Markers[markerID]
	if !ok {
		return LayMarkerFacts{Id: markerID}
	}
	out := LayMarkerFacts{
		Id:            m.Summary.Id,
		Name:          m.Summary.Name,
		TechCardId:    m.Summary.TechCardId,
		RunId:         int(m.Summary.RunId.Int64),
		BomItemId:     m.Summary.BomItemId,
		ColorwayId:    m.Summary.ColorwayId,
		FabricWidthCm: m.Summary.FabricWidthCm,
		UsedLengthCm:  m.Summary.UsedLengthCm,
		Yield:         m.Yield,
	}
	if out.Id == 0 {
		out.Id = markerID
	}
	return out
}

func (b *layPlanBuilder) markerCompositionPb(markerID int) []*pb_common.ProductionRunLayQtyEntry {
	m, ok := b.in.Markers[markerID]
	if !ok {
		return nil
	}
	comp := m.Summary.CompositionOrLegacy()
	out := make([]*pb_common.ProductionRunLayQtyEntry, 0, len(comp))
	for _, e := range comp {
		out = append(out, &pb_common.ProductionRunLayQtyEntry{SizeId: int32(e.SizeId), Qty: int32(e.Quantity)})
	}
	return out
}

func (b *layPlanBuilder) limits() LayWorkshopLimits {
	if b.in.Settings == nil {
		return LayWorkshopLimits{}
	}
	return LayWorkshopLimits{
		MaxStackHeightCm:     b.in.Settings.EffectiveMaxStackHeightCm(),
		CuttingTableLengthCm: b.in.Settings.CuttingTableLengthCm,
	}
}

// article is the АРТИКУЛ the колорвей pins for this настил's slot TODAY — a fresh read, never a
// snapshot (§11): cloth whose article was swapped after the настил was built is re-judged here.
func (b *layPlanBuilder) article(materialID int) LayArticleFacts {
	out := LayArticleFacts{MaterialId: materialID}
	if materialID <= 0 {
		return out
	}
	m, ok := b.in.Materials[materialID]
	if !ok {
		// Идентичность не загрузилась: ширины и толщины нет ⇒ обе проверки отвечают UNKNOWN, что и есть
		// правда. Подставлять ноль здесь означало бы «ткань нулевой ширины», то есть блокер из пустоты.
		return out
	}
	out.Name = m.Name
	out.NominalUsableWidthCm = m.UsableFabricWidthCm()
	out.SelvedgeCm = m.FabricSelvedgeCm()
	out.FabricThicknessMm = m.EffectiveFabricThicknessMm()
	out.NarrowestMeasuredLotCm = b.in.NarrowestMeasuredLotCm[materialID]
	return out
}

// LayArticleMaterialId resolves the article a колорвей pins on one BOM slot: the colourway's own
// usage when it has one, the slot's default otherwise. 0 = неизвестен.
//
// Через planBomLine, как и всё остальное в этом файле: рецепт и настил обязаны согласиться в том,
// какой слот чем кроится, и разойдутся они молча — обе стороны вернут какой-то слот.
func LayArticleMaterialId(card *entity.TechCard, colorwayID int, bomItemID int64) int {
	if card == nil || bomItemID <= 0 {
		return 0
	}
	var bom *entity.TechCardBomItem
	for i := range card.BomItems {
		if int64(card.BomItems[i].Id) == bomItemID {
			bom = &card.BomItems[i]
			break
		}
	}
	if bom == nil {
		return 0
	}
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		if !cw.ProductId.Valid || int(cw.ProductId.Int32) != colorwayID {
			continue
		}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			line := planBomLine(u, card.BomItems)
			if line == nil || int64(line.Id) != bomItemID {
				continue
			}
			if id, _ := u.EffectiveMaterialId(bom); id > 0 {
				return id
			}
		}
	}
	if bom.MaterialId.Valid {
		return int(bom.MaterialId.Int64)
	}
	return 0
}

// LayPlanMaterialIds is the set of articles a lay plan will ask about — what the handler fetches
// identity and lot widths for. Derived from the SAME resolver the projection uses, so the plan can
// never ask about an article it then fails to find.
func LayPlanMaterialIds(card *entity.TechCard, lays []entity.ProductionRunLay) []int {
	seen := map[int]bool{}
	out := make([]int, 0, len(lays))
	for i := range lays {
		id := LayArticleMaterialId(card, lays[i].ColorwayId, bomItemIdOf(&lays[i]))
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// LayWriteCheckInput is the SUBSET of LayCheckInput that ValidateLayForSave reads — the write-path
// half of §8.3, built from a payload that has not been stored yet.
//
// Only the mode, the sections and the identity are filled, and the omissions are the point: the
// article, the workshop limits and the quantity snapshot judge things that CANNOT refuse a save (a
// stack too tall for the knife, a marker wider than the cloth, a stale snapshot are findings, not
// refusals), so loading them here would buy nothing and would make a save fail when a lot table did
// not answer.
func LayWriteCheckInput(runID, techCardID int, bomItemID sql.NullInt64, ins entity.ProductionRunLayInsert,
	markers map[int]LayPlanMarker) LayCheckInput {

	b := &layPlanBuilder{in: LayPlanInput{Markers: markers}, seenCaveat: map[string]bool{}}
	sections := make([]LayCheckSection, 0, len(ins.Sections))
	for _, s := range ins.Sections {
		sections = append(sections, LayCheckSection{
			SectionKey: s.SectionKey,
			Plies:      s.Plies,
			Marker:     b.markerFacts(s.MarkerId),
		})
	}
	return LayCheckInput{
		Lay: LayIdentity{
			RunId:      runID,
			TechCardId: techCardID,
			ColorwayId: ins.ColorwayId,
			BomItemId:  bomItemID,
			BomLineKey: ins.BomLineKey,
			Name:       ins.Name,
		},
		Mode:     LayFaceMode(ins.Mode),
		Sections: sections,
	}
}

// ConvertPbProductionRunLayInsertToEntity is the wire → domain conversion of ONE настил.
//
// qty_snapshot is absent from the wire on purpose and therefore absent here: the server computes it
// from ITS OWN run lines. A snapshot accepted from a client could be forged, and a forged snapshot
// silences the «количества изменились» badge — the one signal it exists to raise.
func ConvertPbProductionRunLayInsertToEntity(pb *pb_common.ProductionRunLayInsert) (entity.ProductionRunLayInsert, error) {
	var out entity.ProductionRunLayInsert
	if pb == nil {
		return out, entity.NewFieldViolation("lay", "required", "", "send the lay to save")
	}
	mode, ok := layModeFromPb(pb.GetMode())
	if !ok {
		return out, entity.NewFieldViolation("lay.mode", "unknown_mode", pb.GetMode().String(),
			"pick FACE_UP or FACE_TO_FACE")
	}
	endLoss := decimal.Zero
	if v := pb.GetEndLossCm(); v != nil && strings.TrimSpace(v.GetValue()) != "" {
		d, err := decimal.NewFromString(strings.TrimSpace(v.GetValue()))
		if err != nil {
			return out, entity.NewFieldViolation("lay.end_loss_cm", "not_a_number", v.GetValue(),
				fmt.Sprintf("enter a decimal number of centimetres per ONE end of ONE ply, e.g. 2 (%v)", err))
		}
		endLoss = d
	}
	out = entity.ProductionRunLayInsert{
		LayKey:       strings.TrimSpace(pb.GetLayKey()),
		ColorwayId:   int(pb.GetColorwayId()),
		BomLineKey:   strings.TrimSpace(pb.GetBomLineKey()),
		Mode:         mode,
		EndLossCm:    endLoss,
		Name:         strings.TrimSpace(pb.GetName()),
		DisplayOrder: int(pb.GetDisplayOrder()),
	}
	if note := strings.TrimSpace(pb.GetNote()); note != "" {
		out.Note = sql.NullString{String: note, Valid: true}
	}
	if err := layLotAndActualFromPb(pb, &out); err != nil {
		return entity.ProductionRunLayInsert{}, err
	}
	out.Sections = make([]entity.ProductionRunLaySectionInsert, 0, len(pb.GetSections()))
	for _, s := range pb.GetSections() {
		out.Sections = append(out.Sections, entity.ProductionRunLaySectionInsert{
			SectionKey: strings.TrimSpace(s.GetSectionKey()),
			MarkerId:   int(s.GetMarkerId()),
			Plies:      int(s.GetPlies()),
			Position:   int(s.GetPosition()),
		})
	}
	return out, nil
}

// layLotAndActualFromPb reads the ONLY two fields of this message where SILENCE IS NOT ERASURE.
//
// Всё остальное в ProductionRunLayInsert — полная замена состояния: не прислал имя, значит имя
// стёрлось. Лот и факт устроены иначе, потому что их пишет ДРУГОЙ ЧЕЛОВЕК В ДРУГОЙ МОМЕНТ: замер
// делает цех после того, как рулон уже раскроен, а число слоёв правит планировщик — на экране, где
// факта не видно. Оптимистичная блокировка от этого не спасает: она ловит УСТАРЕВШУЮ копию, а
// здесь копия свежая, просто писатель про поле не знает. Стёртый замер не переснять.
//
// Три состояния на каждое поле, и голым lot_id их не выразить — int32 не различает «не трогай» и
// «отвяжи», оба приезжают нулём. Поэтому намерение отвязать произносится ФЛАГОМ:
//
//	lot_id > 0        → привязать       clear_lot = true    → отвязать      иначе → не трогать
//	actual_qty задан  → записать факт   clear_actual = true → снять факт    иначе → не трогать
//
// ФЛАГ ПОБЕЖДАЕТ ЗНАЧЕНИЕ, а не наоборот: «отвяжи, и вот тебе заодно id» — это противоречие в самом
// запросе, и разрешать его в пользу привязки значило бы молча сделать обратное тому, что нажали.
//
// Половина формы (единица выбрана, количество ещё нет) НЕ ПИШЕТСЯ ВОВСЕ и ошибкой не является:
// одностороннюю импликацию «факт целиком или никак» схема формулирует как «есть количество ⇒ есть
// единица и метод», и наполовину заполненная форма ей не противоречит — ей просто нечего записать.
func layLotAndActualFromPb(pb *pb_common.ProductionRunLayInsert, out *entity.ProductionRunLayInsert) error {
	switch {
	case pb.GetClearLot():
		unbind := 0
		out.LotId = &unbind
	case pb.GetLotId() > 0:
		bind := int(pb.GetLotId())
		out.LotId = &bind
	}

	if pb.GetClearActual() {
		// Факт без количества = снять факт целиком, вместе с автором и временем: «замер отозван» и
		// «замер равен нулю» — разные утверждения, и второе схема не примет (chk_prlay_actual_qty).
		out.Actual = &entity.ProductionRunLayActualInput{}
		return nil
	}

	qty := pb.GetActualQty()
	if qty == nil || strings.TrimSpace(qty.GetValue()) == "" {
		return nil
	}
	d, err := decimal.NewFromString(strings.TrimSpace(qty.GetValue()))
	if err != nil {
		return entity.NewFieldViolation("lay.actual_qty", "not_a_number", qty.GetValue(),
			fmt.Sprintf("enter how much cloth actually went into this lay, e.g. 94.92 (%v)", err))
	}
	actual := entity.ProductionRunLayActualInput{Qty: decimal.NullDecimal{Decimal: d, Valid: true}}
	if u, ok := materialUnitFromPb(pb.GetActualUom()); ok {
		actual.Uom = u
	}
	if m, ok := layActualMethodFromPb(pb.GetActualMethod()); ok {
		actual.Method = m
	}
	// Незаполненные единица и метод сюда доезжают ПУСТЫМИ, а не отвергаются здесь: их обязательность
	// при заданном количестве — правило домена, и живёт оно в entity.ValidateProductionRunLayActual,
	// которое зовёт store. Вторая проверка тут дала бы второе сообщение об одной ошибке, и они
	// разошлись бы при первой же правке формулировки.
	out.Actual = &actual
	return nil
}

// layActualMethodPb / layActualMethodFromPb — ЕДИНСТВЕННЫЙ перевод между хранимым словарём
// (chk_prlay_actual_method) и перечислением провода. UNSPECIFIED никогда не становится методом:
// незаданное поле — это не «замерили рулон до и после».
func layActualMethodPb(m entity.ProductionLayActualMethod) pb_common.ProductionLayActualMethod {
	switch m {
	case entity.ProductionLayActualMethodRollBeforeAfter:
		return pb_common.ProductionLayActualMethod_PRODUCTION_LAY_ACTUAL_METHOD_ROLL_BEFORE_AFTER
	case entity.ProductionLayActualMethodWeighed:
		return pb_common.ProductionLayActualMethod_PRODUCTION_LAY_ACTUAL_METHOD_WEIGHED
	}
	return pb_common.ProductionLayActualMethod_PRODUCTION_LAY_ACTUAL_METHOD_UNSPECIFIED
}

func layActualMethodFromPb(m pb_common.ProductionLayActualMethod) (entity.ProductionLayActualMethod, bool) {
	switch m {
	case pb_common.ProductionLayActualMethod_PRODUCTION_LAY_ACTUAL_METHOD_ROLL_BEFORE_AFTER:
		return entity.ProductionLayActualMethodRollBeforeAfter, true
	case pb_common.ProductionLayActualMethod_PRODUCTION_LAY_ACTUAL_METHOD_WEIGHED:
		return entity.ProductionLayActualMethodWeighed, true
	}
	return "", false
}

// layModePb / layModeFromPb are the ONE translation between the stored dictionary (chk_prlay_mode)
// and the wire enum. UNSPECIFIED never becomes a mode: an unset field is not «лицом вверх».
func layModePb(m entity.ProductionLayMode) pb_common.ProductionLayMode {
	switch m {
	case entity.ProductionLayModeFaceUp:
		return pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_UP
	case entity.ProductionLayModeFaceToFace:
		return pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_TO_FACE
	}
	return pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_UNSPECIFIED
}

func layModeFromPb(m pb_common.ProductionLayMode) (entity.ProductionLayMode, bool) {
	switch m {
	case pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_UP:
		return entity.ProductionLayModeFaceUp, true
	case pb_common.ProductionLayMode_PRODUCTION_LAY_MODE_FACE_TO_FACE:
		return entity.ProductionLayModeFaceToFace, true
	}
	return "", false
}

// cardPieceSymmetry is piece_line_key → КАК КРОИТСЯ, over the WHOLE card: LayMirrorExpansionCheck
// looks pieces up by the keys the BLOB carries, which may name a piece bound to another slot.
// A missing key and an INVALID value mean the same thing there — «не размечено» ⇒ UNKNOWN.
func cardPieceSymmetry(card *entity.TechCard) map[string]sql.NullString {
	out := make(map[string]sql.NullString, len(card.Pieces))
	for i := range card.Pieces {
		if key := card.Pieces[i].LineKey; key != "" {
			out[key] = card.Pieces[i].CutSymmetry
		}
	}
	return out
}

func layQtyEntries(entries []entity.ProductionRunLayQtyEntry) []LayQtyEntry {
	out := make([]LayQtyEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, LayQtyEntry{SizeId: e.SizeId, Qty: e.Qty})
	}
	return out
}

func layQtyEntriesPb(entries []entity.ProductionRunLayQtyEntry) []*pb_common.ProductionRunLayQtyEntry {
	out := make([]*pb_common.ProductionRunLayQtyEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &pb_common.ProductionRunLayQtyEntry{SizeId: int32(e.SizeId), Qty: int32(e.Qty)})
	}
	return out
}

func bomItemIdOf(l *entity.ProductionRunLay) int64 {
	if l == nil || !l.BomItemId.Valid {
		return 0
	}
	return l.BomItemId.Int64
}

func layNameOf(l *entity.ProductionRunLay) string {
	if n := strings.TrimSpace(l.Name); n != "" {
		return n
	}
	return l.LayKey
}

func pieceNameOf(p *entity.TechCardPiece) string {
	if n := strings.TrimSpace(p.Name); n != "" {
		return n
	}
	if p.LineKey != "" {
		return p.LineKey
	}
	return fmt.Sprintf("#%d", p.Id)
}
