package runpackaccess

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// Тело манифеста наряда: ПРОСТОЙ snake_case JSON, руками, не протобуф. Потребитель — публичная
// страница без proto-рантайма и без сгенерённого клиента (тот же ход, что у манифеста вьюера
// выкроек: PvManifest в admin-client описан вручную).
//
// Структуры объявлены ЗДЕСЬ И СВОИ, а не переиспользованы из проекций, даже там, где чужая
// структура сегодня денег не несёт. Переиспользованная структура — это обещание чужого автора не
// добавлять в неё цену; собственная — это то, что цену в неё добавить негде.

// Manifest — весь наряд одним документом.
type Manifest struct {
	RunId       int    `json:"run_id"`
	StyleNumber string `json:"style_number"`
	StyleName   string `json:"style_name"`
	// ReleaseId / ReleaseNumber — ревизия, ПО КОТОРОЙ КРОЯТ: 0 = прогон не привязан к релизу и
	// считается по живой карте. Читаются из строки прогона, а не из ответа проекции кат-листа,
	// хотя та называет их тоже: одно число, приезжающее из двух источников, рано или поздно
	// приедет из них по-разному, а шапка обязана быть верной даже когда проекция деградировала.
	ReleaseId     int `json:"release_id"`
	ReleaseNumber int `json:"release_number"`
	// Factory — фабрика прогона (supplier.name); пусто = не назначена.
	Factory string `json:"factory"`
	// Status — состояние прогона как есть. Отменённый и закрытый прогон отдаётся ТАК ЖЕ: бумага у
	// швеи уже на столе, и правильный ответ на «эту партию отменили» — показать это словом, а не
	// сделать ссылку неотличимой от битой. Выключает ссылку отзыв (epoch/revoked_at), и это
	// отдельное, осознанное действие.
	Status         string `json:"status"`
	PlannedStartAt string `json:"planned_start_at"` // RFC3339; пусто = не задано
	PromisedAt     string `json:"promised_at"`
	// RunLockVersion — версия прогона на момент расчёта. Наряд живой: количества берутся из
	// прогона, а прогон правят, поэтому документ НАЗЫВАЕТ свою версию, чтобы напечатанное можно
	// было сравнить с текущим вместо того, чтобы молча показать режущему по бумаге другие числа.
	RunLockVersion int    `json:"run_lock_version"`
	GeneratedAt    string `json:"generated_at"`

	// Sizes — размерная ось партии в порядке градации карты; размеры, которых в градации нет
	// (линия под выпавшим размером), идут следом в порядке появления, а безразмерная линия
	// aux-прогона (size_id 0) — последней.
	Sizes []ManifestSize `json:"sizes"`
	// Lines — матрица колорвей × размер с ПЛАНОВЫМИ количествами. Ни выпущенного, ни брака здесь
	// нет: наряд — это задание на партию, а не отчёт по ней.
	Lines         []ManifestLine `json:"lines"`
	GarmentsTotal int            `json:"garments_total"`

	// CutList / CutBlockers / CutCaveats / PiecesToCutTotal — проекция кат-листа партии
	// (dto.ComputeProductionRunCutPlan): что из чего выкроить.
	CutList          []ManifestCutRow     `json:"cut_list"`
	PiecesToCutTotal int                  `json:"pieces_to_cut_total"`
	CutBlockers      []ManifestCutBlocker `json:"cut_blockers"`
	CutCaveats       []string             `json:"cut_caveats"`

	// Настилы прогона. LaysApplicable=false с причиной — это ЯВНОЕ «настилов тут не бывает»
	// (aux-карта), а не пустой список: пустой список читается как приглашение их завести.
	LaysApplicable          bool          `json:"lays_applicable"`
	LaysNotApplicableReason string        `json:"lays_not_applicable_reason"`
	Lays                    []ManifestLay `json:"lays"`

	// MaterialBlockers — пары (слот, колорвей), которые материальный план НЕ смог посчитать. Из
	// всего материального плана в наряд едут только они: остальное — про закупку и деньги, а это
	// про «кроить не из чего», то есть про то, ради чего цех останавливается и спрашивает.
	MaterialBlockers []ManifestMaterialBlocker `json:"material_blockers"`
}

// ManifestSize — одна колонка размерной оси. Id 0 = безразмерная линия.
type ManifestSize struct {
	Id   int    `json:"id"`
	Name string `json:"name"`
}

// ManifestLine — одна строка матрицы: колорвей (или aux-цвет) и его план по размерам.
type ManifestLine struct {
	ColorwayId        int               `json:"colorway_id"` // product id; 0 = линия без продукта
	ColorwayName      string            `json:"colorway_name"`
	OutputVariantId   int               `json:"output_variant_id"` // aux-цвет; 0 = продаваемая линия
	OutputVariantName string            `json:"output_variant_name"`
	BySize            []ManifestLineQty `json:"by_size"`
	PlannedTotal      int               `json:"planned_total"`
}

// ManifestLineQty — плановое количество изделий одной клетки.
type ManifestLineQty struct {
	SizeId     int    `json:"size_id"`
	SizeName   string `json:"size_name"`
	PlannedQty int    `json:"planned_qty"`
}

// ManifestCutRow — одна деталь одного колорвея из кат-листа: спецификация из карты, артикул, из
// которого её кроят в этом колорвее, и количества по размерам партии.
//
// pieces_to_cut = pieces_per_garment × garments, и БОЛЬШЕ НИЧЕГО: cut_symmetry ничего не
// умножает, она едет словами для закройщика.
type ManifestCutRow struct {
	PieceId           int    `json:"piece_id"`
	PieceLineKey      string `json:"piece_line_key"`
	PieceName         string `json:"piece_name"`
	ColorwayId        int    `json:"colorway_id"`
	ColorwayName      string `json:"colorway_name"`
	OutputVariantId   int    `json:"output_variant_id"`
	OutputVariantName string `json:"output_variant_name"`

	PiecesPerGarment int `json:"pieces_per_garment"`
	// CutSymmetry — серверное написание («mirrored»/«fold»/«identical»); ПУСТО = не размечено,
	// и пусто оно именно потому, что «не размечено» — не разновидность симметрии.
	CutSymmetry string `json:"cut_symmetry"`
	Grainline   string `json:"grainline"`
	Fused       bool   `json:"fused"`

	BomItemId  int64  `json:"bom_item_id"`
	SlotName   string `json:"slot_name"`
	Section    string `json:"section"`
	MaterialId int    `json:"material_id"`
	// MaterialName — ЧТО реально кладут на настил в этом колорвее; Pinned отличает пин рецепта от
	// унаследованного дефолта слота. Одинаковое имя роли у трёх колорвеев стирает ровно ту
	// разницу, ради которой закройщик и смотрит в наряд.
	MaterialName       string `json:"material_name"`
	Pinned             bool   `json:"pinned"`
	FusingBomItemId    int64  `json:"fusing_bom_item_id"`
	FusingMaterialName string `json:"fusing_material_name"`

	BySize           []ManifestCutQty `json:"by_size"`
	GarmentsTotal    int              `json:"garments_total"`
	PiecesToCutTotal int              `json:"pieces_to_cut_total"`
}

// ManifestCutQty — сколько изделий этого размера в партии и сколько панелей детали это даёт.
type ManifestCutQty struct {
	SizeId      int    `json:"size_id"`
	SizeName    string `json:"size_name"`
	Garments    int    `json:"garments"`
	PiecesToCut int    `json:"pieces_to_cut"`
}

// ManifestCutBlocker — деталь × колорвей, которую наряд не смог привязать к артикулу. Отдельным
// списком, а не строкой с прочерком: прочерк теряется среди сорока таких же строк.
type ManifestCutBlocker struct {
	PieceId      int    `json:"piece_id"`
	PieceName    string `json:"piece_name"`
	ColorwayId   int    `json:"colorway_id"`
	ColorwayName string `json:"colorway_name"`
	Garments     int    `json:"garments"`
	Reason       string `json:"reason"`
}

// ManifestMaterialBlocker — слот × колорвей, который материальный план не смог посчитать.
// Key — стабильный машинный код причины (no_article | no_norm), Reason — человеческая фраза.
type ManifestMaterialBlocker struct {
	SlotName     string `json:"slot_name"`
	ColorwayId   int    `json:"colorway_id"`
	ColorwayName string `json:"colorway_name"`
	PlannedQty   int    `json:"planned_qty"`
	Key          string `json:"key"`
	Reason       string `json:"reason"`
}

// ManifestLay — настил прогона, кратко: что настилаем, из какого слота, сколько слоёв и какие
// размеры лежат на раскладке. Mode едет вместе с ними, потому что это инструкция настильщику
// (лицом вверх или лицом к лицу), а не аналитика.
type ManifestLay struct {
	Name         string `json:"name"`
	ColorwayId   int    `json:"colorway_id"`
	ColorwayName string `json:"colorway_name"`
	// SlotName пусто, когда слот удалён из BOM (FK SET NULL); SlotLineKey остаётся — настил,
	// потерявший слот, обязан уметь НАЗВАТЬ потерянное, а не замолчать.
	SlotName    string               `json:"slot_name"`
	SlotLineKey string               `json:"slot_line_key"`
	Mode        string               `json:"mode"`
	TotalPlies  int                  `json:"total_plies"`
	Sections    []ManifestLaySection `json:"sections"`
}

// ManifestLaySection — одна секция настила: раскладка и сколько её слоёв настилают.
type ManifestLaySection struct {
	MarkerName string `json:"marker_name"`
	Plies      int    `json:"plies"`
	// Sizes — СОСТАВ раскладки: сколько изделий каждого размера лежит в ОДНОМ слое. Не умножено на
	// слои специально: выход настила считает dto (LayCoverage) с учётом режима настилания, и
	// вторая арифметика того же числа разошлась бы с первой ровно тогда, когда это дороже всего.
	Sizes []ManifestLaySize `json:"sizes"`
}

// ManifestLaySize — одна строка состава раскладки.
type ManifestLaySize struct {
	SizeId         int    `json:"size_id"`
	SizeName       string `json:"size_name"`
	GarmentsPerPly int    `json:"garments_per_ply"`
}

// buildManifest собирает документ: узкое чтение наряда, живая карта как спецификация, проекция
// кат-листа, блокеры материального плана и настилы.
//
// ЦЕЛАЯ КАРТА ЧИТАЕТСЯ КАК ВХОД. dto.ComputeProductionRunCutPlan принимает *entity.TechCard, и
// узкого чтения карты под неё не существует; собрать второе (частичное) означало бы завести вторую
// спецификацию, которая обязана совпадать с первой и не совпадёт. Деньги карты в ответ не попадают
// не потому, что их отсюда вычёркивают, а потому что в структурах выше их нет.
func (s *Service) buildManifest(ctx context.Context, runID int) (*Manifest, error) {
	pack, err := s.runs.GetRunPack(ctx, runID)
	if err != nil {
		// sql.ErrNoRows проходит без обёртки — обработчик отличает «прогон исчез» через errors.Is.
		return nil, err
	}
	card, err := s.cards.GetTechCardById(ctx, pack.Run.TechCardId)
	if err != nil {
		return nil, fmt.Errorf("load tech card %d of run %d: %w", pack.Run.TechCardId, runID, err)
	}
	lays, err := s.runs.ListLays(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load lays of run %d: %w", runID, err)
	}

	markerIDs := make([]int, 0, len(lays.Lays))
	seenMarker := make(map[int]bool, len(lays.Lays))
	for i := range lays.Lays {
		for _, sec := range lays.Lays[i].Sections {
			if sec.MarkerId > 0 && !seenMarker[sec.MarkerId] {
				seenMarker[sec.MarkerId] = true
				markerIDs = append(markerIDs, sec.MarkerId)
			}
		}
	}
	composition, err := s.runs.GetRunPackMarkerSizes(ctx, markerIDs)
	if err != nil {
		return nil, fmt.Errorf("load lay composition of run %d: %w", runID, err)
	}

	// Релиз называется МЕТОЙ, без блоба: сам снапшот — многокилобайтная JSON-строка, и тянуть её на
	// каждое сканирование QR ради номера ревизии было бы платой ни за что. UnitCost/Currency меты
	// остаются пустыми осознанно: они денежные, и в этот путь не входят вовсе.
	var release *entity.TechCardReleaseMeta
	if pack.Run.ReleaseId.Valid && pack.ReleaseNumber > 0 {
		release = &entity.TechCardReleaseMeta{
			Id:            int(pack.Run.ReleaseId.Int64),
			TechCardId:    pack.Run.TechCardId,
			ReleaseNumber: pack.ReleaseNumber,
			CreatedAt:     pack.ReleasedAt.Time,
		}
	}
	cutPlan := dto.ComputeProductionRunCutPlan(&pack.Run, card, release)

	// ЧЕСТНОСТЬ ПРО РЕВИЗИЮ. Контракт проекции говорит: спецификация — это СНАПШОТ релиза, если
	// прогон к нему привязан. Снапшот лежит proto-JSON блобом (tech_card_release.snapshot), а
	// обратного преобразования блоба в entity.TechCard в репозитории нет ни одного — есть только
	// pb→entity для ВСТАВКИ. Поэтому сюда уходит живая карта, и шапка, называющая «Rev.N», обязана
	// сказать об этом вслух: цех, который кроит по бумаге, должен узнать о расхождении из документа,
	// а не из последствий. Снимается вместе с появлением пути «снапшот → спецификация» — тогда
	// удаляется и эта оговорка, и ветка вокруг неё.
	caveats := append([]string{}, cutPlan.GetCaveats()...)
	if release != nil {
		caveats = append(caveats, "кат-лист посчитан по ЖИВОЙ карте, а не по снапшоту релиза Rev."+
			strconv.Itoa(release.ReleaseNumber)+": спецификация могла измениться после релиза")
	}

	// Материальный план считается БЕЗ остатков, выданного и справочника артикулов (nil, nil, nil):
	// из него берутся только блокеры, а те резолвятся из карты и прогона. Справочник — это
	// entity.MaterialWithPrice, то есть цены; не передать его — единственный способ гарантировать,
	// что цена в этот расчёт не попадала даже в памяти.
	materialPlan := dto.ComputeProductionRunMaterialPlan(&pack.Run, card, nil, nil, nil, lays.Lays)

	sizes, lines, garments := buildLineMatrix(card, pack.Lines)

	m := &Manifest{
		RunId:          pack.Run.Id,
		StyleNumber:    pack.StyleNumber,
		StyleName:      pack.StyleName,
		ReleaseNumber:  pack.ReleaseNumber,
		Factory:        pack.FactoryName,
		Status:         string(pack.Run.Status),
		PlannedStartAt: rfc3339(pack.Run.PlannedStartAt),
		PromisedAt:     rfc3339(pack.Run.PromisedAt),
		RunLockVersion: pack.Run.LockVersion,
		GeneratedAt:    time.Now().UTC().Format(time.RFC3339),

		Sizes:         sizes,
		Lines:         lines,
		GarmentsTotal: garments,

		CutList:          cutRows(cutPlan),
		PiecesToCutTotal: int(cutPlan.GetPiecesToCutTotal()),
		CutBlockers:      cutBlockers(cutPlan),
		CutCaveats:       caveats,

		LaysApplicable:          lays.Applicable,
		LaysNotApplicableReason: lays.NotApplicableReason,
		Lays:                    manifestLays(lays.Lays, composition),

		MaterialBlockers: materialBlockers(materialPlan),
	}
	if release != nil {
		m.ReleaseId = release.Id
	}
	return m, nil
}

// rfc3339 рендерит опциональную дату. Отсутствующая едет ПУСТОЙ СТРОКОЙ, а не нулевым временем:
// «1970-01-01» на бумаге читается как дата, а не как «не задано».
func rfc3339(t sql.NullTime) string {
	if !t.Valid {
		return ""
	}
	return t.Time.UTC().Format(time.RFC3339)
}

// buildLineMatrix раскладывает линии прогона в матрицу колорвей × размер.
//
// Ось размеров идёт в порядке ГРАДАЦИИ КАРТЫ, а не в порядке появления в гриде: цех читает ряд
// слева направо от меньшего к большему, и порядок, зависящий от того, в каком порядке кто-то
// заводил клетки, читался бы как ошибка размерного ряда. Размер, которого в градации карты уже нет
// (его успели убрать после планирования), не выбрасывается, а идёт следом — линия под ним несёт
// реальное плановое количество, и потерять её значило бы недокроить партию.
func buildLineMatrix(card *entity.TechCard, lines []entity.RunPackLine) ([]ManifestSize, []ManifestLine, int) {
	order := make(map[int]int, len(card.SizeIds))
	for i, id := range card.SizeIds {
		order[id] = i
	}

	type key struct{ colorway, variant int }
	type cellKey struct {
		row  key
		size int
	}
	names := make(map[int]string, len(lines))
	present := make(map[int]bool, len(lines))
	var extra []int
	rowByKey := make(map[key]*ManifestLine)
	cellByKey := make(map[cellKey]int) // → индекс клетки внутри BySize своей строки
	rows := make([]*ManifestLine, 0, len(lines))
	total := 0

	for i := range lines {
		ln := lines[i]
		sizeID := ln.SizeId
		if !present[sizeID] {
			present[sizeID] = true
			names[sizeID] = ln.SizeName
			if _, ok := order[sizeID]; !ok {
				extra = append(extra, sizeID)
			}
		}
		k := key{colorway: int(ln.ProductId.Int32), variant: int(ln.OutputVariantId.Int32)}
		row, ok := rowByKey[k]
		if !ok {
			row = &ManifestLine{
				ColorwayId:        k.colorway,
				ColorwayName:      ln.ColorwayName,
				OutputVariantId:   k.variant,
				OutputVariantName: ln.OutputVariantName,
			}
			rowByKey[k] = row
			rows = append(rows, row)
		}
		// Клетки СУММИРУЮТСЯ по (колорвей, цвет, размер), а не добавляются подряд: грид прогона
		// законно держит несколько строк на одну пару (их правят и разбивают по ключу линии, и
		// уникальности по паре в схеме нет — dto считает такие строки суммой, см.
		// TestDuplicateRunLinesForOnePairAreSummed). Печать «S 10 / S 6» вместо «S 16» — это не
		// косметика: закройщик кроит по первой встреченной колонке.
		ck := cellKey{row: k, size: sizeID}
		if idx, ok := cellByKey[ck]; ok {
			row.BySize[idx].PlannedQty += ln.PlannedQty
		} else {
			cellByKey[ck] = len(row.BySize)
			row.BySize = append(row.BySize, ManifestLineQty{
				SizeId:     sizeID,
				SizeName:   ln.SizeName,
				PlannedQty: ln.PlannedQty,
			})
		}
		row.PlannedTotal += ln.PlannedQty
		total += ln.PlannedQty
	}

	axis := make([]ManifestSize, 0, len(present))
	for _, id := range card.SizeIds {
		if present[id] {
			axis = append(axis, ManifestSize{Id: id, Name: names[id]})
		}
	}
	// Размеры вне градации — в порядке появления, а безразмерная линия (0) последней: она бывает
	// только у aux-прогона, где размера нет вовсе, и в середине ряда читалась бы как дыра.
	sort.SliceStable(extra, func(i, j int) bool { return extra[i] != 0 && extra[j] == 0 })
	for _, id := range extra {
		axis = append(axis, ManifestSize{Id: id, Name: names[id]})
	}

	// Клетки строки выстраиваются по ТОЙ ЖЕ оси, что и заголовок. Порядок клеток внутри строки
	// приезжает из базы (по size_id), а ось — из градации карты, и совпадают они только случайно:
	// расходятся — и колонки грида на бумаге разъезжаются относительно заголовка, то есть партия
	// шьётся не в тех размерах. Пропущенные клетки не дозаполняются нулями: «не запланировано» и
	// «запланировано ноль» — разные утверждения, и выбор между прочерком и нулём принадлежит тому,
	// кто рисует, а не тому, кто считает.
	axisPos := make(map[int]int, len(axis))
	for i, sz := range axis {
		axisPos[sz.Id] = i
	}
	out := make([]ManifestLine, 0, len(rows))
	for _, r := range rows {
		sort.SliceStable(r.BySize, func(i, j int) bool {
			return axisPos[r.BySize[i].SizeId] < axisPos[r.BySize[j].SizeId]
		})
		out = append(out, *r)
	}
	return axis, out, total
}

// cutRows переносит строки проекции кат-листа в собственные структуры документа.
func cutRows(plan *pb_admin.GetProductionRunCutPlanResponse) []ManifestCutRow {
	src := plan.GetRows()
	out := make([]ManifestCutRow, 0, len(src))
	for _, r := range src {
		bySize := make([]ManifestCutQty, 0, len(r.GetBySize()))
		for _, q := range r.GetBySize() {
			bySize = append(bySize, ManifestCutQty{
				SizeId:      int(q.GetSizeId()),
				SizeName:    q.GetSizeName(),
				Garments:    int(q.GetGarments()),
				PiecesToCut: int(q.GetPiecesToCut()),
			})
		}
		out = append(out, ManifestCutRow{
			PieceId:            int(r.GetPieceId()),
			PieceLineKey:       r.GetPieceLineKey(),
			PieceName:          r.GetPieceName(),
			ColorwayId:         int(r.GetColorwayId()),
			ColorwayName:       r.GetColorwayName(),
			OutputVariantId:    int(r.GetOutputVariantId()),
			OutputVariantName:  r.GetOutputVariantName(),
			PiecesPerGarment:   int(r.GetPiecesPerGarment()),
			CutSymmetry:        cutSymmetryWord(r.GetCutSymmetry()),
			Grainline:          r.GetGrainline(),
			Fused:              r.GetFused(),
			BomItemId:          r.GetBomItemId(),
			SlotName:           r.GetSlotName(),
			Section:            bomSectionWord(r.GetSection()),
			MaterialId:         int(r.GetMaterialId()),
			MaterialName:       r.GetMaterialName(),
			Pinned:             r.GetPinned(),
			FusingBomItemId:    r.GetFusingBomItemId(),
			FusingMaterialName: r.GetFusingMaterialName(),
			BySize:             bySize,
			GarmentsTotal:      int(r.GetGarmentsTotal()),
			PiecesToCutTotal:   int(r.GetPiecesToCutTotal()),
		})
	}
	return out
}

func cutBlockers(plan *pb_admin.GetProductionRunCutPlanResponse) []ManifestCutBlocker {
	src := plan.GetBlockers()
	out := make([]ManifestCutBlocker, 0, len(src))
	for _, b := range src {
		out = append(out, ManifestCutBlocker{
			PieceId:      int(b.GetPieceId()),
			PieceName:    b.GetPieceName(),
			ColorwayId:   int(b.GetColorwayId()),
			ColorwayName: b.GetColorwayName(),
			Garments:     int(b.GetGarments()),
			Reason:       b.GetReason(),
		})
	}
	return out
}

// materialBlockers берёт из материального плана ТОЛЬКО блокеры. bom_item_id слота сюда не едет: на
// бумаге он ничего не значит, а имя слота значит.
func materialBlockers(plan *pb_admin.GetProductionRunMaterialPlanResponse) []ManifestMaterialBlocker {
	src := plan.GetBlockers()
	out := make([]ManifestMaterialBlocker, 0, len(src))
	for _, b := range src {
		out = append(out, ManifestMaterialBlocker{
			SlotName:     b.GetSlotName(),
			ColorwayId:   int(b.GetColorwayId()),
			ColorwayName: b.GetColorwayName(),
			PlannedQty:   int(b.GetPlannedQty()),
			Key:          b.GetKey(),
			Reason:       b.GetReason(),
		})
	}
	return out
}

// manifestLays рендерит настилы кратко: имя, колорвей, слот, режим, слои и состав раскладок.
func manifestLays(lays []entity.ProductionRunLay, composition map[int][]entity.RunPackMarkerSize) []ManifestLay {
	out := make([]ManifestLay, 0, len(lays))
	for i := range lays {
		l := lays[i]
		sections := make([]ManifestLaySection, 0, len(l.Sections))
		for _, sec := range l.Sections {
			sizes := make([]ManifestLaySize, 0, len(composition[sec.MarkerId]))
			for _, c := range composition[sec.MarkerId] {
				sizes = append(sizes, ManifestLaySize{
					SizeId:         c.SizeId,
					SizeName:       c.SizeName,
					GarmentsPerPly: c.Quantity,
				})
			}
			sections = append(sections, ManifestLaySection{
				MarkerName: sec.MarkerName,
				Plies:      sec.Plies,
				Sizes:      sizes,
			})
		}
		out = append(out, ManifestLay{
			Name:         l.Name,
			ColorwayId:   l.ColorwayId,
			ColorwayName: l.ColorwayName,
			SlotName:     l.BomItemName.String,
			SlotLineKey:  l.BomLineKey,
			Mode:         string(l.Mode),
			TotalPlies:   l.TotalPlies(),
			Sections:     sections,
		})
	}
	return out
}

// cutSymmetryWord и bomSectionWord переводят проектные енумы в СЕРВЕРНОЕ написание — ровно те
// строки, что лежат в БД и в entity-константах («mirrored», «fabric»). Написание выводится из
// имени значения, а не из ручного switch: switch молча отдал бы пустую строку для значения,
// добавленного в енум завтра, и деталь приехала бы в цех без указания симметрии.
func cutSymmetryWord(v pb_common.TechCardPieceCutSymmetry) string {
	if v == pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN {
		// «Не размечено» — это отсутствие разметки, а не её разновидность: пустая строка, чтобы
		// клиент не напечатал слово «unknown» там, где закройщик ждёт указание.
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(v.String(), "TECH_CARD_PIECE_CUT_SYMMETRY_"))
}

func bomSectionWord(v pb_common.TechCardBomSection) string {
	if v == pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_UNKNOWN {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(v.String(), "TECH_CARD_BOM_SECTION_"))
}
