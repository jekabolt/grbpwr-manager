package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ПРЕДЛОЖЕНИЕ ПРОЦЕНТА РАСКРОЯ ПО ФАКТУ НАСТИЛОВ (T7 волна 2) — §4.2 спеки.
//
//	netto_настила  = Σ по секциям (слои × Σ по составу раскладки (кол-во размера × dxf-норма размера))
//	дрейф_настила  = actual_qty / netto − 1
//	предложение    = МЕДИАНА дрейфов настилов артикула, в ПРОЦЕНТАХ, для bom_item.wastage_percent
//
// ─────────────────────────────────────────────────────────────────────────────────────────────────
// ЭТО НЕ КАЛИБРОВКА КОЭФФИЦИЕНТА, И ПУТАТЬ ИХ НЕЛЬЗЯ. ПРОЧИТАТЬ ДО ПЕРВОЙ ПРАВКИ.
// ─────────────────────────────────────────────────────────────────────────────────────────────────
//
// Рядом (material_coefficient_calibration.go) живёт внешне похожий расчёт: та же медиана, тот же
// порог, те же настилы. РАЗНИЦА — В ЗНАМЕНАТЕЛЕ, и она не оформительская:
//
//   - калибровка КОЭФФИЦИЕНТА делит факт на ПЛАН-ГЕОМЕТРИЮ настила (LayPlannedGeometryOf: длина
//     раскладки × слои + концевые). Длина раскладки УЖЕ СОДЕРЖИТ межлекальные выпады — они лежат
//     внутри измеренной длины. Медиана поэтому меряет ТОЛЬКО то, чего раскладка содержать не может:
//     усадку, обход пороков, сращивание — типично 2–6%, и ложится в material.cutting_coefficient
//     (МНОЖИТЕЛЬ на артикуле);
//
//   - этот расчёт делит факт на NETTO × КОЭФФИЦИЕНТ АРТИКУЛА (знаменатель после W3). Netto — то,
//     что дала бы норма «по выкройкам» БЕЗ ЕДИНОГО ВЫПАДА (состав раскладки × пер-размерная
//     dxf-норма); коэффициент вынимает из измерения усадку и пороки, которые он уже оплачивает сам.
//     Остаток — ровно геометрия настила: межлекальные выпады + концы, типично 15–30%, и ложится в
//     bom_item.wastage_percent (ПРОЦЕНТ на строке BOM модели).
//
// ЗНАМЕНАТЕЛЬ ЗДЕСЬ ДВИНУЛСЯ ВМЕСТЕ СО СМЫСЛОМ ПОЛЯ (W3), И ИНАЧЕ БЫТЬ НЕ МОГЛО. До W3 процент
// оплачивал и усадку тоже, поэтому чистое netto было честным знаменателем. W3 сузил процент до
// геометрии настила и отдал усадку/пороки/сращивание коэффициенту, который с тех пор входит в ТЕ ЖЕ
// деньги нормы (entity.NormGrossUp). Оставь знаменатель прежним — и предложение на артикуле с
// коэффициентом оплачивало бы усадку ДВАЖДЫ: netto × (1 + процент_с_усадкой) × коэффициент.
//
// ЛИНЕЙКА СМЕНИЛАСЬ, А НЕ ФАКТ, и ответ RPC обязан это говорить (denominator_cutting_coefficient):
// значения процента, применённые ДО W3, посчитаны по старой линейке и на артикуле с коэффициентом
// с тех пор завышены примерно на него. Ничто их не переписывает молча — система никогда не
// применяет предложение сама.
//
// Это два РАЗНЫХ слагаемых отходов с разными базами, непересекающиеся по построению
// (production_material_plan.go: «the two worlds stay disjoint so no line is ever grossed twice»).
// Вписать медиану коэффициента (~4%) в поле процента (куда нужно ~20%) — занизить закупку на все
// межлекальные выпады, линейно и молча; вписать процент в коэффициент — оплатить выпады дважды.
// Поэтому здесь СВОИ имена, СВОЙ статус, СВОИ сообщения — и ни одной общей строки с калибровкой.
//
// КОЭФФИЦИЕНТ И ПРОЦЕНТ В NETTO НЕ ВХОДЯТ. Netto — чистая норма без гросс-апов, как план настила —
// чистая геометрия (решение Р4): будь любой множитель в знаменателе, медиана калибровала бы его же
// и сходилась бы к произвольной точке. Тот же аргумент некруговости, что в шапке соседа.
//
// МЕДИАНА, ПОРОГ ≥3, ПРЕДЛОЖЕНИЕ-НЕ-ПРИМЕНЕНИЕ — та же дисциплина и по тем же причинам, что у
// соседа (его шапка объясняет каждую). Порог намеренно ОДНОЙ константой: это одно и то же
// утверждение о том, сколько замеров превращают наблюдение в основание.

// MinLaysForWastageSuggestion — порог: ниже трёх настилов предложения нет. РАВЕН порогу калибровки
// коэффициента и ПРИВЯЗАН к нему, а не совпадает случайно (решение владельца, §10 п.2 спеки).
const MinLaysForWastageSuggestion = MinLaysForCoefficientSuggestion

// wastagePercentScale — знаков после запятой у bom_item.wastage_percent (DECIMAL(5,2), 0073), и
// ровно столько принимает валидация провода. Округление происходит ОДИН раз и здесь: предложение,
// которое нельзя сохранить одним нажатием, — это не предложение.
const wastagePercentScale = 2

// layNettoScale — знаков после запятой у production_run_lay.netto_qty (DECIMAL(12,3), 0296).
// LayNettoOf округляет до него сам, чтобы живой пересчёт и штамп не расходились в последнем знаке.
const layNettoScale = 3

// Границы, в которых процент вообще хранится: CHECK 0..100 (0073) + валидация dto.
var (
	wastagePercentMin = decimal.Zero
	wastagePercentMax = decimal.NewFromInt(100)
	wastageHundred    = decimal.NewFromInt(100)
	wastageOne        = decimal.NewFromInt(1)
)

// WastageSuggestionStatus is the answer to «есть ли что предложить» — СВОЙ тип, не общий с
// CoefficientSuggestionStatus: общий тип был бы приглашением перепутать два расчёта в обработчике.
//
// НУЛЕВОЕ ЗНАЧЕНИЕ — «ФАКТОВ МАЛО»: забытое поле обязано читаться как отсутствие предложения, и
// Ready никогда не выпадает по умолчанию (дисциплина соседа).
type WastageSuggestionStatus int

const (
	// WastageSuggestionTooFewFacts — настилов с netto И фактом меньше MinLaysForWastageSuggestion.
	// Это СОСТОЯНИЕ, а не ошибка: на пустой базе это ОСНОВНОЙ ответ, и клиент говорит «фактов ещё
	// нет», а не «что-то сломалось».
	WastageSuggestionTooFewFacts WastageSuggestionStatus = iota
	// WastageSuggestionOutOfRange — медиана посчитана, но поле [0,100] её не примет. Измерение
	// отдаётся, число-предложение — нет, и НЕ прижимается к границе.
	WastageSuggestionOutOfRange
	// WastageSuggestionReady — предложение есть, и его можно вписать в поле как есть.
	WastageSuggestionReady
)

func (s WastageSuggestionStatus) String() string {
	switch s {
	case WastageSuggestionTooFewFacts:
		return "TOO_FEW_FACTS"
	case WastageSuggestionOutOfRange:
		return "OUT_OF_RANGE"
	case WastageSuggestionReady:
		return "READY"
	}
	return fmt.Sprintf("WastageSuggestionStatus(%d)", int(s))
}

// LayNettoSection is ONE section of a настил as the netto arithmetic reads it: how many plies, and
// what ONE ply of its раскладка cuts. Composition comes from the ONE owner —
// entity.TechCardMarkerSummary.CompositionOrLegacy() — never from a second reader of
// tech_card_marker_size; empty means «раскладка не говорит, что кроит», and netto refuses.
type LayNettoSection struct {
	Plies       int
	MarkerLabel string
	Composition []entity.MarkerCompositionEntry
}

// LayNettoResult is netto with the third answer in the type: a value, or ABSENCE with a named
// reason. Reason is empty exactly when Qty is valid — the LayCheck.Detail discipline.
type LayNettoResult struct {
	Qty    decimal.NullDecimal
	Reason string
}

// LayNettoOf computes ONE настил's netto — the denominator of the wastage suggestion and the value
// SaveLay stamps into production_run_lay.netto_qty at fact-write time.
//
//	netto = Σ по секциям (plies × Σ по составу (quantity × dxf-норма(size)))
//
// ТОЛЬКО dxf-НОРМА ГОДИТСЯ В ЗНАМЕНАТЕЛЬ, и это несущее. 'manual' — оценка человека (может уже
// содержать его собственный запас — деление факта на неё калибрует чужую осторожность, не отходы);
// 'marker' — измеренная длина раскладки (выпады УЖЕ внутри — деление на неё меряет коэффициент, а
// это соседний расчёт). Только 'dxf' (площадь деталей ÷ раскройная ширина, 0294) — netto по
// построению. Слот без dxf-нормы поэтому не «деградирует» на другую — он честно выпадает с
// причиной.
//
// Норму размера читает usageNormForSize — ТОТ ЖЕ читатель, что у материального плана (пер-размерная
// ступень, иначе плоская): второе определение «нормы размера» разъехалось бы с планом молча.
// Артикул за настилом здесь НЕ резолвится — это работа LayArticleMaterialId у вызывающего; эта
// функция отвечает только за арифметику слота.
//
// НЕСКОЛЬКО СТРОК «НА ИЗДЕЛИЕ» НА ОДНОМ СЛОТЕ — ЛЕГАЛЬНЫ И СУММИРУЮТСЯ (правки ревью T7в2,
// MAJOR 2): сервер всюду складывает строки слота (материальный план, костинг), и netto обязано
// складывать так же — взять первую значило бы занизить знаменатель и завысить дрейф в зависимости
// от ПОРЯДКА строк рецепта. Если среди строк слота есть НЕ-dxf строка, знаменатель неполон по
// построению — отказ с названным источником, а не молчаливое смешение dxf-числа с чужим.
//
// actualUom — единица, в которой замерен факт. Netto считается в единице СЛОТА (bom_item.unit — в
// ней записана dxf-норма), и сверка единиц ОБЯЗАТЕЛЬНА (правки ревью T7в2, BLOCKER 2): пустая или
// неизвестная перечню (entity.NormalizeMaterialUnit) единица С ЛЮБОЙ СТОРОНЫ — отказ с названной
// причиной, ровно как явное несовпадение. Дробь факт/netto при неопределённой размерности — число
// ни о чём, и записанный с ним штамп молча кривил бы медиану; «не считать» здесь строго дешевле,
// чем «посчитать неизвестно что».
func LayNettoOf(card *entity.TechCard, colorwayID int, bomItemID int64, actualUom string,
	sections []LayNettoSection) LayNettoResult {

	if card == nil {
		return LayNettoResult{Reason: "the lay's tech card isn't loaded — there is nothing to compute netto from"}
	}
	if bomItemID <= 0 {
		return LayNettoResult{Reason: "the lay lost its BOM slot — there is nothing to tie netto to"}
	}
	var bom *entity.TechCardBomItem
	for i := range card.BomItems {
		if int64(card.BomItems[i].Id) == bomItemID {
			bom = &card.BomItems[i]
			break
		}
	}
	if bom == nil {
		return LayNettoResult{Reason: fmt.Sprintf("slot #%d isn't in the card — there is nothing to tie netto to", bomItemID)}
	}

	// Юзеджи слота: ВСЕ строки ИЗДЕЛИЯ (T8: строка детали — назначение материала, нормы на ней
	// нет), разрешённые в этот слот ТЕМ ЖЕ planBomLine, что и весь остальной код (легаси-юзедж
	// адресует слот позиционным индексом, и второй резолвер потерял бы его молча).
	var usages []*entity.TechCardColorwayUsage
	for i := range card.Colorways {
		cw := &card.Colorways[i]
		if !cw.ProductId.Valid || int(cw.ProductId.Int32) != colorwayID {
			continue
		}
		for j := range cw.Usages {
			u := &cw.Usages[j]
			if u.IsPieceMaterialAssignment() {
				continue
			}
			line := planBomLine(u, card.BomItems)
			if line == nil || int64(line.Id) != bomItemID {
				continue
			}
			src := u.ConsumptionSource.String
			if src != entity.ConsumptionSourceDxf {
				if src == "" {
					src = entity.ConsumptionSourceManual
				}
				return LayNettoResult{Reason: fmt.Sprintf(
					"a slot line with the %q norm — netto is taken only from “from patterns” (dxf) norms: manual is an estimate, marker already contains the waste; a denominator mixed from several sources is incomplete", src)}
			}
			usages = append(usages, u)
		}
		break
	}
	if len(usages) == 0 {
		return LayNettoResult{Reason: "the lay's tech card has no dxf norm for the slot — there is nothing to take netto from"}
	}

	// Сверка единиц — обе стороны обязаны быть названы И известны перечню (BLOCKER 2, см. шапку).
	slotUom, factUom := strings.TrimSpace(bom.Unit.String), strings.TrimSpace(actualUom)
	if slotUom == "" {
		return LayNettoResult{Reason: "the BOM slot names no unit — the dimension of netto is undefined, it can't be computed"}
	}
	if _, ok := entity.NormalizeMaterialUnit(slotUom); !ok {
		return LayNettoResult{Reason: fmt.Sprintf("the slot unit %q is not in the unit list — the dimension of netto is undefined, it can't be computed", slotUom)}
	}
	if factUom == "" {
		return LayNettoResult{Reason: "the actual names no unit — netto can't be reconciled with the actual"}
	}
	if _, ok := entity.NormalizeMaterialUnit(factUom); !ok {
		return LayNettoResult{Reason: fmt.Sprintf("the actual's unit %q is not in the unit list — netto can't be reconciled with the actual", factUom)}
	}
	if !entity.SameMaterialUnit(slotUom, factUom) {
		return LayNettoResult{Reason: fmt.Sprintf("the actual is measured in %q and the slot norm in %q — netto can't be reconciled with the actual", factUom, slotUom)}
	}

	if len(sections) == 0 {
		return LayNettoResult{Reason: "the lay has no sections — netto is zero, there is nothing to divide by"}
	}
	total := decimal.Zero
	for _, s := range sections {
		label := strings.TrimSpace(s.MarkerLabel)
		if label == "" {
			label = "an unnamed marker"
		}
		if len(s.Composition) == 0 {
			// Раскладка, не говорящая, что кроит, не превращается в «один комплект» — тот же отказ,
			// что MarkerScalarNormRefusal делает на костинге.
			return LayNettoResult{Reason: fmt.Sprintf("%s doesn't say what it cuts (no composition recorded) — the section's netto can't be computed", label)}
		}
		if s.Plies <= 0 {
			return LayNettoResult{Reason: fmt.Sprintf("%s: a section without plies — netto can't be computed", label)}
		}
		perPly := decimal.Zero
		for _, c := range s.Composition {
			if c.Quantity <= 0 {
				continue
			}
			// Норма размера = СУММА по строкам слота (MAJOR 2). Каждая строка обязана покрыть
			// размер: строка без значения — заниженный знаменатель, то есть завышенный дрейф, молча.
			perGarment := decimal.Zero
			for _, u := range usages {
				// Пара (колорвей × слот) здесь НЕ передаётся, и это не упущение: netto считается
				// только по МЕРНОМУ слоту с dxf-нормой, где счётного итога нет по построению, а
				// usages выше — отфильтрованное подмножество строк слота, а не пара. Счётная
				// строка отсекается ветвью counted строкой ниже, как и до 0333.
				norm, _, counted, ok := usageNormForSize(u, c.SizeId, nil, bom)
				if !ok || counted {
					// Непокрытый размер НАЗЫВАЕТСЯ, а не пропускается.
					return LayNettoResult{Reason: fmt.Sprintf("%s: the slot's dxf norm has no value for size #%d of the composition — netto is incomplete", label, c.SizeId)}
				}
				perGarment = perGarment.Add(norm)
			}
			perPly = perPly.Add(perGarment.Mul(decimal.NewFromInt(int64(c.Quantity))))
		}
		total = total.Add(perPly.Mul(decimal.NewFromInt(int64(s.Plies))))
	}
	if !total.IsPositive() {
		return LayNettoResult{Reason: "the lay's netto isn't positive — there is nothing to divide the actual by"}
	}
	return LayNettoResult{Qty: decimal.NullDecimal{Decimal: total.Round(layNettoScale), Valid: true}}
}

// LayWastageObservation is ONE настил's pair of numbers for THIS suggestion: the measured fact and
// the netto denominator — the stamp taken at замер time when there is one, else the live recompute
// (NettoStamped tells the two apart; NettoReason names the absence).
type LayWastageObservation struct {
	LayId  int
	LayKey string
	// TechCardId feeds the distinct-model counter: «медиана по 3 настилам» на одной чужой модели и
	// на трёх своих — разные основания, и ответ обязан их различать.
	TechCardId   int
	NettoQty     decimal.NullDecimal
	NettoReason  string
	NettoStamped bool
	// ActualQty is production_run_lay.actual_qty — в единице netto по построению штампа/пересчёта
	// (единицы сверены на записи и в LayNettoOf), поэтому дробь не требует перевода.
	ActualQty decimal.NullDecimal
	// Coefficient — коэффициент раскроя АРТИКУЛА, НА КОТОРОМ МЕРИЛСЯ ЭТОТ ФАКТ. Входит в
	// ЗНАМЕНАТЕЛЬ (netto × коэффициент), см. LayWastageDriftOf. INVALID = у артикула коэффициента
	// нет, знаменатель — чистое netto.
	//
	// НА НАБЛЮДЕНИИ, А НЕ НА ВХОДЕ ЦЕЛИКОМ, хотя сегодня он у всех настилов один: наблюдения
	// отбираются по артикулу (LayArticleMaterialId), и величина, которой делят ЭТОТ факт, обязана
	// принадлежать ЕМУ. Общее поле на входе стало бы приглашением подставить «сегодняшний артикул
	// карточки» в тот день, когда отбор перестанет быть по одному артикулу.
	Coefficient decimal.NullDecimal
}

// LayWastageDrift is ONE настил's вклад — с третьим ответом в типе, как у соседа: невошедший настил
// называет причину, потому что строка без числа и без объяснения неотличима от бага.
type LayWastageDrift struct {
	LayId  int
	LayKey string
	// Drift is факт/netto − 1. INVALID = настил в медиану не входит, причина в Skipped.
	Drift   decimal.NullDecimal
	Skipped string
}

// Counted reports whether this настил stood under the median.
func (d LayWastageDrift) Counted() bool { return d.Drift.Valid }

// LayWastageDriftOf считает дрейф ОДНОГО настила и называет причину невхождения.
func LayWastageDriftOf(o LayWastageObservation) LayWastageDrift {
	d := LayWastageDrift{LayId: o.LayId, LayKey: o.LayKey}
	switch {
	case !o.NettoQty.Valid:
		reason := strings.TrimSpace(o.NettoReason)
		if reason == "" {
			reason = "the lay's netto isn't computed — there is nothing to divide the actual by"
		}
		d.Skipped = reason
	case !o.NettoQty.Decimal.IsPositive():
		// chk_prlay_netto_qty такую строку не пропустит; вход берётся значениями, поэтому проверка
		// здесь своя.
		d.Skipped = fmt.Sprintf("the lay's netto isn't positive (%s) — there is nothing to divide by", o.NettoQty.Decimal.String())
	case !o.ActualQty.Valid:
		d.Skipped = "the lay's actual consumption isn't entered"
	case !o.ActualQty.Decimal.IsPositive():
		d.Skipped = fmt.Sprintf("the actual consumption isn't positive (%s)", o.ActualQty.Decimal.String())
	default:
		// ЗНАМЕНАТЕЛЬ = netto × КОЭФФИЦИЕНТ АРТИКУЛА (W3), а не чистое netto.
		//
		// Процент раскроя теперь означает ТОЛЬКО геометрию настила (межлекальные выпады + концы), а
		// усадку/пороки/сращивание оплачивает коэффициент — и оплачивает В ТЕХ ЖЕ деньгах нормы
		// (entity.NormGrossUp). Медиана над ЧИСТЫМ netto меряет всё сразу, поэтому вписать её в
		// сужённое поле значило бы оплатить усадку дважды: один раз процентом, второй —
		// коэффициентом. Разделив факт ещё и на коэффициент, мы вынимаем из измерения ровно то, что
		// уже оплачено, и остаток — ровно геометрия, то есть ровно то, чем процент стал.
		//
		// КРУГА НЕТ, И ЭТО ПРОВЕРЯЕМО ПО ЗНАМЕНАТЕЛЯМ. Коэффициент калибруется факт ÷
		// ПЛАН-ГЕОМЕТРИЕЙ настила (LayPlannedGeometryOf: длина раскладки × слои + концевые) —
		// величиной, в которой нет ни процента, ни коэффициента. Значит коэффициент не зависит ни
		// от netto, ни от процента, и ссылка на него здесь однонаправленна:
		//
		//	коэффициент ← (факт, геометрия настила)
		//	процент     ← (факт, netto, коэффициент)
		//
		// Петля появилась бы только если бы план настила начали гроссить коэффициентом — что Р4
		// запрещает ровно по этой причине (шапка material_coefficient_calibration.go).
		denom := o.NettoQty.Decimal
		if o.Coefficient.Valid {
			denom = denom.Mul(o.Coefficient.Decimal)
		}
		if !denom.IsPositive() {
			d.Skipped = fmt.Sprintf("the lay's denominator isn't positive (%s) — there is nothing to divide by", denom.String())
			return d
		}
		d.Drift = decimal.NullDecimal{
			Decimal: o.ActualQty.Decimal.Div(denom).Sub(wastageOne),
			Valid:   true,
		}
	}
	return d
}

// BomWastageSuggestion is the whole answer about ONE артикул.
type BomWastageSuggestion struct {
	Status WastageSuggestionStatus
	// MedianPercent is медиана дрейфов над netto, в ПРОЦЕНТАХ поля (22.00 = «+22% над netto»).
	// VALID всегда, когда настилов хватило, ДАЖЕ при OUT_OF_RANGE: измерение остаётся фактом,
	// когда поле его не принимает.
	MedianPercent decimal.NullDecimal
	// SuggestedPercent is число для поля — ТО ЖЕ измерение, отдаётся только при READY. Второй
	// единицы (множителя) здесь нет НАРОЧНО: у поля единица — процент, и множитель в ответе
	// повторил бы путаницу «процент против коэффициента» в обратную сторону.
	SuggestedPercent decimal.NullDecimal
	// LayCount is сколько настилов стоит под медианой; TechCardCount — по скольким РАЗНЫМ карточкам
	// (моделям) они. Оба едут всегда: предложение без опоры нечем взвесить, а межлекальные выпады —
	// свойство модели, и медиана по чужим моделям обязана быть видна как таковая.
	LayCount      int
	TechCardCount int
	// Drifts is каждое наблюдение в порядке входа, вошедшее и невошедшее.
	Drifts []LayWastageDrift
	// Detail is фраза для человека. Непуста всегда, включая READY.
	Detail string
}

// BomWastageSuggestionOf — предложение процента раскроя для ОДНОГО артикула по его настилам.
// Вызывающий обязан передать настилы ОДНОГО артикула (отбор — работа резолвера у вызывающего);
// правило считает и не выбирает.
func BomWastageSuggestionOf(lays []LayWastageObservation) BomWastageSuggestion {
	out := BomWastageSuggestion{Drifts: make([]LayWastageDrift, 0, len(lays))}
	counted := make([]decimal.Decimal, 0, len(lays))
	cards := make(map[int]bool, len(lays))
	for _, o := range lays {
		d := LayWastageDriftOf(o)
		out.Drifts = append(out.Drifts, d)
		if d.Counted() {
			counted = append(counted, d.Drift.Decimal)
			if o.TechCardId > 0 {
				cards[o.TechCardId] = true
			}
		}
	}
	out.LayCount = len(counted)
	out.TechCardCount = len(cards)

	// ОДНА ФОРМУЛА, ОДНА ФРАЗА. Знаменатель называется ТАМ ЖЕ, где написана формула, потому что это
	// один и тот же факт: приписать линейку отдельным предложением значило бы напечатать оператору
	// две несовместимые формулы подряд («факт ÷ netto» и «знаменатель netto × коэффициент») и
	// оставить его выбирать, какой верить. Читает это ЧЕЛОВЕК, а не ревьюер кода.
	denom := "netto"
	if c := countedDenominatorCoefficient(lays); c.Valid {
		denom = fmt.Sprintf("(netto × %s)", c.Decimal.String())
	}

	if out.LayCount < MinLaysForWastageSuggestion {
		out.Status = WastageSuggestionTooFewFacts
		out.Detail = fmt.Sprintf("not enough facts yet: %s with a measurement and netto, %d needed — a percent derived from one or two cuts is a guess dressed as a number",
			calibrationPlural(out.LayCount, "%d lay", "%d lays"), MinLaysForWastageSuggestion)
		return out
	}

	// Медиана считается в полной точности, округляется ОДИН РАЗ — до масштаба поля; диапазон
	// проверяется по округлённому числу, чтобы показанное и сохраняемое были одним числом.
	percent := medianDecimal(counted).Mul(wastageHundred).Round(wastagePercentScale)
	out.MedianPercent = decimal.NullDecimal{Decimal: percent, Valid: true}

	if percent.LessThan(wastagePercentMin) || percent.GreaterThan(wastagePercentMax) {
		// Число за границами поля не отдаётся и НЕ ПРИЖИМАЕТСЯ: зажать медиану −3% в 0 значило бы
		// сказать «выпадов нет» там, где измерение говорит «ваша netto-норма завышена — или замер
		// врёт». Медиана ниже нуля буквально означает, что ткани ушло МЕНЬШЕ безотходной нормы.
		out.Status = WastageSuggestionOutOfRange
		out.Detail = fmt.Sprintf("the median drift over %s across %s (%s) is %s%%, but the field accepts from %s to %s — check the measurements and the dxf norms: most likely one of them is wrong",
			denom,
			calibrationPlural(out.LayCount, "%d lay", "%d lays"),
			calibrationPlural(out.TechCardCount, "%d tech card", "%d tech cards"),
			percent.String(), wastagePercentMin.String(), wastagePercentMax.String())
		return out
	}

	out.Status = WastageSuggestionReady
	out.SuggestedPercent = decimal.NullDecimal{Decimal: percent, Valid: true}
	out.Detail = fmt.Sprintf("suggestion %s%% — the median of “actual ÷ %s − 1” across %s, %s; applied by hand, not automatically — and this is NOT the article's cutting coefficient: that one is measured from the marker length and contains no waste",
		percent.String(), denom,
		calibrationPlural(out.LayCount, "%d lay", "%d lays"),
		calibrationPlural(out.TechCardCount, "%d tech card", "%d tech cards"))
	return out
}

// countedDenominatorCoefficient — коэффициент, которым РЕАЛЬНО делили. Берётся у настилов, ВОШЕДШИХ
// в медиану: настил, выпавший без числа, ничего о линейке не свидетельствует, а фраза обязана
// описывать посчитанное, а не поданное на вход.
func countedDenominatorCoefficient(lays []LayWastageObservation) decimal.NullDecimal {
	for _, o := range lays {
		if LayWastageDriftOf(o).Counted() && o.Coefficient.Valid {
			return o.Coefficient
		}
	}
	return decimal.NullDecimal{}
}

// calibrationPlural substitutes n into whichever of the two forms English counting requires:
// 1 lay, 2 lays. Each form is a format with a single %d, so the number and the word can never be
// pulled apart. Only n == 1 takes the singular — 0 reads as plural ("0 lays"), which is what
// English says. Calibration details are read by a HUMAN, and "1 tech cards" is the kind of seam
// that makes a measured number look like it was assembled by a machine that did not check.
func calibrationPlural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf(one, n)
	}
	return fmt.Sprintf(many, n)
}
