package techcardanalysis

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── ПРИЁМКА B1–B8 (design §3.2) ─────────────────────────────────────────────────────────────────
//
// У КАЖДОЙ проверки здесь ДВА направления. Тест, который лишь исполняет проверку, зелен и у
// сторожа при мёртвой ветке: он доказывает, что функция вызвалась, а не что она что-то ловит.
// Поэтому у всякой из восьми есть состояние, на котором она ОБЯЗАНА заговорить, и состояние, на
// котором она ОБЯЗАНА молчать, — включая B4/B6/B7, молчащие на карточке 8 (счёты NULL, костинга в
// дампе нет): их fire-сторона построена мутацией фикстуры.
//
// t.Parallel НЕ ИСПОЛЬЗУЕТСЯ: реестр проверок — пакетный слайс.
//
// Хелперы этого файла префиксованы `bt*`; общие пробы (rtOne/rtNone/rtDump/rtFindings/rtWantRefs)
// живут в route_test.go и переиспользуются — второй экземпляр той же пробы разошёлся бы с первым.

// btFx is the fixture's currency channel: base EUR (cache.defaultCurrency), no rates at all.
var btFx = Fx{Base: "EUR"}

func btFindings(c *entity.TechCard) []Finding { return RunAudit(c, btFx).Findings }

// btAddBom appends a BOM line with a fresh id and line_key and returns it.
func btAddBom(c *entity.TechCard, b entity.TechCardBomItem) *entity.TechCardBomItem {
	maxID := 0
	for i := range c.BomItems {
		if c.BomItems[i].Id > maxID {
			maxID = c.BomItems[i].Id
		}
	}
	b.Id = maxID + 1
	if b.LineKey == "" {
		b.LineKey = fmt.Sprintf("BT-LINE-%d", b.Id)
	}
	c.BomItems = append(c.BomItems, b)
	return &c.BomItems[len(c.BomItems)-1]
}

// btLinkOpToBom links an operation to a BOM line the way the read path hands it over (both halves).
func btLinkOpToBom(op *entity.TechCardOperation, b *entity.TechCardBomItem) {
	op.BomLineKeys = append(op.BomLineKeys, b.LineKey)
	op.BomIds = append(op.BomIds, b.Id)
}

// btAddUsage puts a countable norm for that BOM line on the card's first colourway.
func btAddUsage(c *entity.TechCard, b *entity.TechCardBomItem, quantity string) {
	cw := &c.Colorways[0]
	cw.Usages = append(cw.Usages, entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: int64(b.Id), Valid: true},
		Quantity:  dec(quantity),
	})
}

// btCosting attaches a costing block; cmt="" means the CMT column stays NULL.
func btCosting(c *entity.TechCard, cmt string) {
	cost := &entity.TechCardCosting{Currency: text("EUR")}
	if cmt != "" {
		cost.CmtCost = dec(cmt)
	}
	c.Costing = cost
}

// btDropHardwareOps removes the two hardware-setting steps of card 8, last first, so the numbers of
// the survivors do not move under the second drop.
func btDropHardwareOps(c *entity.TechCard) {
	card8DropOperation(c, 480)
	card8DropOperation(c, 470)
}

// ── B1 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB1FiresOnCard8HardwareWithoutLines(t *testing.T) {
	f := rtOne(t, btFindings(card8()), "The route sets hardware; the BOM has none")
	if f.Severity != SeverityError || f.Category != CategoryBomMismatch {
		t.Errorf("B1 must be bom_mismatch/error, got %s/%s", f.Category, f.Severity)
	}
	rtWantRefs(t, f, RefOp(470), RefOp(480))
	// Текст обязан называть, ЧЕМ шаг опознан: у 470/480 глагол machine, поймала их машина.
	for _, want := range []string{"machine_type buttonhole", "machine_type button_attach", "section 'hardware'"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("B1 detail must name %q, got: %s", want, f.Detail)
		}
	}
}

func TestB1FlipsWhenTheLineExistsAndNobodySetsIt(t *testing.T) {
	c := card8()
	btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
		UnitPrice: dec("0.4000"), Currency: text("EUR"),
	})
	btDropHardwareOps(c)

	fs := btFindings(c)
	rtNone(t, fs, "The route sets hardware")
	f := rtOne(t, fs, "The BOM buys hardware; no step sets it")
	if f.Severity != SeverityWarning {
		t.Errorf("обратная сторона B1 — warning, а не %s", f.Severity)
	}
	rtWantRefs(t, f, RefBom("пуговица"))
}

func TestB1IsSilentWhenBothHalvesExist(t *testing.T) {
	c := card8()
	btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
	})
	fs := btFindings(c)
	rtNone(t, fs, "The route sets hardware")
	rtNone(t, fs, "The BOM buys hardware")
}

func TestB1IsSilentWhenNeitherHalfExists(t *testing.T) {
	c := card8()
	btDropHardwareOps(c)
	fs := btFindings(c)
	rtNone(t, fs, "The route sets hardware")
	rtNone(t, fs, "The BOM buys hardware")
}

func TestB1CatchesTheVerbAsWellAsTheMachine(t *testing.T) {
	// hardware_set БЕЗ машины: 0328 сделал machine_type законным на этом глаголе, но не
	// обязательным, и проверка, читающая только машину, такой шаг бы не увидела.
	c := card8()
	btDropHardwareOps(c)
	rtAppendOp(c, entity.TechCardOperation{
		OperationType:  entity.OpTypeHardwareSet,
		Zone:           "front",
		AssemblyInputs: []entity.OperationInput{rtUnitInput("blazer")},
		InputKeys:      []string{"blazer"},
	})
	f := rtOne(t, btFindings(c), "The route sets hardware; the BOM has none")
	if !strings.Contains(f.Detail, "operation_type hardware_set") {
		t.Errorf("B1 must name the verb half of its predicate, got: %s", f.Detail)
	}
}

// ── B2 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB2FiresOnCard8WithFortyFourSewingSteps(t *testing.T) {
	f := rtOne(t, btFindings(card8()), "sewing operations, zero thread lines")
	if f.Title != "44 sewing operations, zero thread lines" {
		t.Errorf("B2 обязана цитировать счёт: got %q", f.Title)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("B2 — warning, got %s", f.Severity)
	}
}

func TestB2IsSilencedByASingleThreadLine(t *testing.T) {
	c := card8()
	btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionThread, Name: "нитки 40/2"})
	rtNone(t, btFindings(c), "sewing operations, zero thread lines")
}

func TestB2ReadsTheKindAsWellAsTheSection(t *testing.T) {
	// Ниточность выводится ИЗ СЛОВАРЯ entity (BomKindHomeSection == 'thread'), а не из списка в
	// файле проверки: шестой вид ниток, добавленный в entity, обязан подавить находку сам.
	//
	// Строка НЕ в секции 'thread', но с ниточным видом — это форма, которую сегодняшняя запись не
	// пропустит (store сверяет пару вид↔секция), но которую может нести строка старше 0278. Ветка
	// проверяется именно на ней: на строке section='thread' она была бы неотличима от секционной.
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionOther, Name: "нитка"})
	line.Kind = text(string(entity.BomKindSewingThread))
	rtNone(t, btFindings(c), "sewing operations, zero thread lines")

	// А вид NULL в чужой секции ниточной строку не делает: NULL — «не классифицировано» (0278).
	c2 := card8()
	other := btAddBom(c2, entity.TechCardBomItem{Section: entity.BomSectionOther, Name: "прочее"})
	other.Kind = nullText()
	rtOne(t, btFindings(c2), "sewing operations, zero thread lines")
}

func TestB2IsSilentWithoutSewingSteps(t *testing.T) {
	c := card8()
	for i := range c.Operations {
		c.Operations[i].OperationType = entity.OpTypeHandwork
		c.Operations[i].MachineType = nullText()
	}
	rtNone(t, btFindings(c), "sewing operations, zero thread lines")
}

// ── B3 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB3QuotesAllThreeCountsOnCard8(t *testing.T) {
	f := rtOne(t, btFindings(card8()), "Fusing is stated in")
	for _, want := range []string{"Interlining BOM lines: 1", "marked fused: 0 of 48", "operations in the route: 0"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("B3 must quote %q, got: %s", want, f.Detail)
		}
	}
	// Находка не утверждает намерения — только счёт.
	if strings.Contains(strings.ToLower(f.Detail), "needs fusing") {
		t.Errorf("B3 не имеет права утверждать намерение: %s", f.Detail)
	}
}

func TestB3TextMovesWhenAPieceIsMarkedFused(t *testing.T) {
	c := card8()
	card8PieceByName(c, "SHLD_L").Fused = true
	f := rtOne(t, btFindings(c), "Fusing is stated in")
	if !strings.Contains(f.Detail, "marked fused: 1 of 48") {
		t.Errorf("мутация fused=1 обязана поменять второй счётчик, got: %s", f.Detail)
	}
	if !strings.Contains(f.Title, "2 of the 3") {
		t.Errorf("две из трёх половин заполнены, а заголовок говорит: %s", f.Title)
	}
}

func TestB3IsSilentWhenTheTriangleIsClosed(t *testing.T) {
	c := card8()
	card8PieceByName(c, "SHLD_L").Fused = true
	rtAppendOp(c, entity.TechCardOperation{
		OperationType:  entity.OpTypeFusing,
		Zone:           "interlining",
		AssemblyInputs: []entity.OperationInput{rtPieceInput(c, "SHLD_R")},
	})
	rtNone(t, btFindings(c), "Fusing is stated in")
}

func TestB3IsSilentWhenThereIsNoFusingAnywhere(t *testing.T) {
	c := card8()
	// Снимаем единственную прокладочную линию и её линки — треугольник становится нулевым.
	var kept []entity.TechCardBomItem
	for _, b := range c.BomItems {
		if b.LineKey != card8BomInterlining {
			kept = append(kept, b)
		}
	}
	c.BomItems = kept
	for i := range c.Operations {
		c.Operations[i].BomLineKeys = nil
		c.Operations[i].BomIds = nil
	}
	rtNone(t, btFindings(c), "Fusing is stated in")
}

// ── B4 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB4IsSilentOnCard8BecauseTheCountsAreNull(t *testing.T) {
	fs := btFindings(card8())
	rtNone(t, fs, "buttonholes against")
	rtNone(t, fs, "set than bought")
}

func TestB4FiresWhenTheTwoCountsDisagree(t *testing.T) {
	c := card8()
	card8OpByNumber(c, 470).PlacementCount = sql.NullInt32{Int32: 5, Valid: true}
	card8OpByNumber(c, 480).PlacementCount = sql.NullInt32{Int32: 4, Valid: true}

	f := rtOne(t, btFindings(c), "buttonholes against")
	if f.Title != "5 buttonholes against 4 buttons" {
		t.Errorf("B4 обязана цитировать оба счёта: %q", f.Title)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("несовпадение счётов — warning, got %s", f.Severity)
	}
	rtWantRefs(t, f, RefOp(470), RefOp(480))
}

func TestB4IsSilentWhenTheCountsAgree(t *testing.T) {
	c := card8()
	card8OpByNumber(c, 470).PlacementCount = sql.NullInt32{Int32: 4, Valid: true}
	card8OpByNumber(c, 480).PlacementCount = sql.NullInt32{Int32: 4, Valid: true}
	rtNone(t, btFindings(c), "buttonholes against")
}

func TestB4IsSilentWhenOnlyOneSideStatesItsCount(t *testing.T) {
	// Одна незаполненная строка делает сумму утверждением, которого никто не делал: об этом
	// говорит A2, а не B4.
	c := card8()
	card8OpByNumber(c, 470).PlacementCount = sql.NullInt32{Int32: 5, Valid: true}
	rtNone(t, btFindings(c), "buttonholes against")
}

func TestB4FiresWhenMoreIsSetThanBought(t *testing.T) {
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
		UnitPrice: dec("0.4000"), Currency: text("EUR"),
	})
	btAddUsage(c, line, "4")
	op := card8OpByNumber(c, 480)
	op.PlacementCount = sql.NullInt32{Int32: 6, Valid: true}
	btLinkOpToBom(op, line)

	f := rtOne(t, btFindings(c), "More пуговица set than bought")
	if f.Severity != SeverityError {
		t.Errorf("ставим больше, чем закуплено, — error, got %s", f.Severity)
	}
	rtWantRefs(t, f, RefOp(480), RefBom("пуговица"))
	if !strings.Contains(f.Detail, "colourway \"black\"") {
		t.Errorf("B4 обязана назвать колорвей, чья норма не сходится: %s", f.Detail)
	}
}

func TestB4IsSilentWhenTheSpareIsBought(t *testing.T) {
	// Запаска легальна: закупить больше, чем ставим, — нормальная практика, и quantity == счёт
	// тоже молчит.
	for _, qty := range []string{"6", "8"} {
		c := card8()
		line := btAddBom(c, entity.TechCardBomItem{
			Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
		})
		btAddUsage(c, line, qty)
		op := card8OpByNumber(c, 480)
		op.PlacementCount = sql.NullInt32{Int32: 6, Valid: true}
		btLinkOpToBom(op, line)
		rtNone(t, btFindings(c), "set than bought")
	}
}

// btMentionSlot поминает слот строкой рецепта БЕЗ собственного числа: пара существует, а норму
// несёт слот. Ровно так выглядит карточка, заполненная после 0333.
func btMentionSlot(c *entity.TechCard, b *entity.TechCardBomItem) {
	cw := &c.Colorways[0]
	cw.Usages = append(cw.Usages, entity.TechCardColorwayUsage{
		BomItemId: sql.NullInt64{Int64: int64(b.Id), Valid: true},
	})
}

func TestB4ReadsTheNormOffTheSlot(t *testing.T) {
	// Шов с волной счётных норм (0333): число стоит на слоте, строка рецепта его не повторяет.
	// Чтение по строкам молчало бы здесь совсем — то есть проверка выключалась бы ровно на тех
	// карточках, ради которых счётная норма и заводилась.
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
		QtyPerGarment: dec("6"),
	})
	btMentionSlot(c, line)
	op := card8OpByNumber(c, 480)
	op.PlacementCount = sql.NullInt32{Int32: 7, Valid: true}
	btLinkOpToBom(op, line)

	f := rtOne(t, btFindings(c), "More пуговица set than bought")
	if !strings.Contains(f.Detail, "qty_per_garment") {
		t.Errorf("находка обязана отправить чинить на строку BOM, а не в рецепт: %s", f.Detail)
	}
	if !strings.Contains(f.Detail, "6") {
		t.Errorf("напечатать надо норму слота: %s", f.Detail)
	}
}

func TestB4SumsThePairInsteadOfTakingItsSmallestRow(t *testing.T) {
	// 0295 разрешает двум строкам одного колорвея поминать один слот с разными размещениями:
	// четыре пуговицы на планке и две на манжете. Шесть установок такую карточку СХОДЯТ, и
	// построчный минимум (2) печатал бы ошибку на исправных данных.
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
	})
	btAddUsage(c, line, "4")
	btAddUsage(c, line, "2")
	op := card8OpByNumber(c, 480)
	op.PlacementCount = sql.NullInt32{Int32: 6, Valid: true}
	btLinkOpToBom(op, line)

	rtNone(t, btFindings(c), "set than bought")
}

func TestB4DoesNotSetFromTheSpareKit(t *testing.T) {
	// Запас слота уезжает в пакетик покупателю. Пять на изделие плюс три в запас — это восемь
	// закупленных и ПЯТЬ пришиваемых: шесть установок недостачу имеют, и сверка с закупаемым
	// итогом (CountablePairTotal) молча отдала бы пакетик пустым.
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionHardware, Name: "пуговица", Unit: text("pc"),
		QtyPerGarment: dec("5"), SpareQty: dec("3"),
	})
	btMentionSlot(c, line)
	op := card8OpByNumber(c, 480)
	op.PlacementCount = sql.NullInt32{Int32: 6, Valid: true}
	btLinkOpToBom(op, line)

	f := rtOne(t, btFindings(c), "More пуговица set than bought")
	if !strings.Contains(f.Detail, "5") {
		t.Errorf("сверяться надо с пришиваемым числом, а не с закупаемым: %s", f.Detail)
	}
}

func TestB4IsSilentWithoutALink(t *testing.T) {
	c := card8()
	line := btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionHardware, Name: "пуговица"})
	btAddUsage(c, line, "1")
	card8OpByNumber(c, 480).PlacementCount = sql.NullInt32{Int32: 6, Valid: true}
	rtNone(t, btFindings(c), "set than bought")
}

// ── B5а ─────────────────────────────────────────────────────────────────────────────────────────

func TestB5aFiresOnTheLiningOfCard8(t *testing.T) {
	f := rtOne(t, btFindings(card8()), `Is "подкладка" priced or is that a placeholder?`)
	if f.Category != CategoryQuestion {
		t.Errorf("B5а — вопрос, а не дефект: got category %s", f.Category)
	}
	rtWantRefs(t, f, RefBom("подкладка"))
	if !strings.Contains(f.Detail, "face value") {
		t.Errorf("сравнение через непереводимые валюты обязано называть себя: %s", f.Detail)
	}
}

func TestB5aIsSilencedByACatalogPrice(t *testing.T) {
	c := card8()
	card8BomByKey(c, card8BomLining).PriceSource = text(entity.BomPriceSourceCatalog)
	rtNone(t, btFindings(c), "placeholder")
}

func TestB5aFiresOnAZeroPrice(t *testing.T) {
	c := card8()
	card8BomByKey(c, card8BomLining).UnitPrice = dec("0")
	f := rtOne(t, btFindings(c), `Is "подкладка" priced or is that a placeholder?`)
	if !strings.Contains(f.Detail, "priced at zero") {
		t.Errorf("нулевая цена обязана называться нулевой: %s", f.Detail)
	}
}

func TestB5aIsSilentWhenTheLiningIsPricedLikeCloth(t *testing.T) {
	c := card8()
	card8BomByKey(c, card8BomLining).UnitPrice = dec("30.0000")
	rtNone(t, btFindings(c), "placeholder")
}

// ── B5б ─────────────────────────────────────────────────────────────────────────────────────────

func TestB5bFiresOnThePocketingOfCard8(t *testing.T) {
	f := rtOne(t, btFindings(card8()), `"Карманка" costs more per metre`)
	if f.Category != CategoryQuestion {
		t.Errorf("B5б — вопрос: got %s", f.Category)
	}
	rtWantRefs(t, f, RefBom("Карманка"), RefBom("основная ткань"))
	if !strings.Contains(f.Detail, "60") || !strings.Contains(f.Detail, "55") {
		t.Errorf("B5б обязана цитировать обе цены: %s", f.Detail)
	}
}

func TestB5bIsSilentWhenThePocketingIsCheaper(t *testing.T) {
	c := card8()
	card8BomByKey(c, card8BomPocketing).UnitPrice = dec("50.0000")
	rtNone(t, btFindings(c), "costs more per metre")
}

func TestB5bIsSilentWithoutARateBetweenTheCurrencies(t *testing.T) {
	// Утверждение «дороже» через неизвестный курс — выдумка. В отличие от вопроса B5а, эта
	// находка обязана молчать.
	c := card8()
	card8BomByKey(c, card8BomPocketing).Currency = text("USD")
	rtNone(t, btFindings(c), "costs more per metre")

	// А с курсом — снова говорит.
	fx := Fx{Base: "EUR", ToBase: map[string]decimal.Decimal{
		"PLN": decimal.RequireFromString("0.23"),
		"USD": decimal.RequireFromString("0.92"),
	}}
	rtOne(t, RunAudit(c, fx).Findings, "costs more per metre")
}

func TestB5bIsSilentOnANonMetreUnit(t *testing.T) {
	c := card8()
	card8BomByKey(c, card8BomPocketing).Unit = text("pc")
	rtNone(t, btFindings(c), "costs more per metre")
}

func TestB5bNeedsToBeDearerThanEveryMainLine(t *testing.T) {
	// Вторая основная ткань дороже карманки — вопроса больше нет.
	c := card8()
	btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionFabric, Purpose: text(string(entity.BomPurposeMain)),
		Name: "основная ткань 2", Unit: text("m"), UnitPrice: dec("80.0000"), Currency: text("PLN"),
	})
	rtNone(t, btFindings(c), "costs more per metre")
}

// ── B5в ─────────────────────────────────────────────────────────────────────────────────────────

func TestB5cAggregatesThreePlnLinesIntoOneFinding(t *testing.T) {
	// База — EUR (cache.defaultCurrency): без курса остаются ТРИ PLN-линии, а EUR-подкладка и есть
	// база. Единица пропуска — ВАЛЮТА: три находки об одном недостающем курсе были бы трижды одной
	// и той же работой в списке.
	f := rtOne(t, btFindings(card8()), "has no rate to EUR")
	if f.Title != "PLN has no rate to EUR: 3 lines drop out of the cost total" {
		t.Errorf("B5в: %q", f.Title)
	}
	rtWantRefs(t, f, RefBom("основная ткань"), RefBom("Плечевая"), RefBom("Карманка"))
	if rtHasRef(f, RefBom("подкладка")) {
		t.Error("EUR-линия — это база, курса ей не нужно, и якорем она быть не может")
	}
}

func TestB5cIsSilencedByTheRate(t *testing.T) {
	fx := Fx{Base: "EUR", ToBase: map[string]decimal.Decimal{"PLN": decimal.RequireFromString("0.23")}}
	rtNone(t, RunAudit(card8(), fx).Findings, "has no rate to")
}

func TestB5cAggregatesWhenMoreThanThreeCurrenciesAreUnknown(t *testing.T) {
	c := card8()
	for _, code := range []string{"USD", "GBP", "CHF", "SEK"} {
		btAddBom(c, entity.TechCardBomItem{
			Section: entity.BomSectionTrim, Name: "лента " + code, Unit: text("m"),
			UnitPrice: dec("3.0000"), Currency: text(code),
		})
	}
	f := rtOne(t, btFindings(c), "BOM currencies have no rate to EUR")
	if !strings.Contains(f.Title, "5 of 6") {
		t.Errorf("закон агрегации §3.0: пять неизвестных валют из шести — %q", f.Title)
	}
	if len(f.Refs) > 3 {
		t.Errorf("агрегированная находка несёт не больше трёх якорей-образцов, got %v", f.Refs)
	}
}

func TestB5cIgnoresAnUnpricedLine(t *testing.T) {
	c := card8()
	btAddBom(c, entity.TechCardBomItem{
		Section: entity.BomSectionTrim, Name: "тесьма", Unit: text("m"), Currency: text("JPY"),
	})
	f := rtOne(t, btFindings(c), "has no rate to EUR")
	if strings.Contains(f.Title, "JPY") {
		t.Errorf("строка без цены из итога не выпадает — выпадать нечему: %q", f.Title)
	}
}

// ── B6 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB6IsSilentOnCard8AndSaysSoOutLoud(t *testing.T) {
	res := RunAudit(card8(), btFx)
	for _, f := range res.Findings {
		if strings.Contains(f.Title, "CMT") {
			t.Fatalf("костинга в дампе нет — B6 обязана молчать, а не выдумывать: %q", f.Title)
		}
	}
	found := false
	for _, l := range res.NotChecked {
		if strings.Contains(l, "no tech_card_costing row") {
			found = true
		}
	}
	if !found {
		t.Errorf("молчание «данных нет» обязано быть сказано вслух, а NotChecked: %v", res.NotChecked)
	}
}

func TestB6FiresOnACostingWithoutCmt(t *testing.T) {
	c := card8()
	btCosting(c, "")
	c.ApprovalState = entity.TechCardApprovalInReview // не схлопывать readiness

	f := rtOne(t, btFindings(c), "Cost estimate is materials-only")
	if f.Category != CategoryReadiness || f.Severity != SeverityWarning {
		t.Errorf("B6 — readiness/warning, got %s/%s", f.Category, f.Severity)
	}
	if f.Clause == "" {
		t.Error("readiness-находка без Clause молча выпадет из схлопнутого перечисления")
	}
	rtWantRefs(t, f, RefCard)
}

func TestB6IsSilencedByTheCmtFigure(t *testing.T) {
	c := card8()
	btCosting(c, "120.00")
	c.ApprovalState = entity.TechCardApprovalInReview
	rtNone(t, btFindings(c), "Cost estimate is materials-only")
}

func TestB6IsSilentWithoutARoute(t *testing.T) {
	c := card8()
	btCosting(c, "")
	c.Operations = nil
	c.ApprovalState = entity.TechCardApprovalInReview
	rtNone(t, btFindings(c), "Cost estimate is materials-only")
}

// ── B7 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB7FiresWhenCmtHasNoSmvBehindIt(t *testing.T) {
	c := card8()
	btCosting(c, "120.00")
	c.ApprovalState = entity.TechCardApprovalInReview

	f := rtOne(t, btFindings(c), "CMT is quoted with SMV on")
	if f.Category != CategoryQuestion {
		t.Errorf("B7 — вопрос владельцу: got %s", f.Category)
	}
	if f.Title != "CMT is quoted with SMV on 0 of 48 steps" {
		t.Errorf("B7 обязана цитировать покрытие: %q", f.Title)
	}
}

func TestB7IsSilencedByTwentyPercentCoverage(t *testing.T) {
	c := card8()
	btCosting(c, "120.00")
	c.ApprovalState = entity.TechCardApprovalInReview
	for i := 0; i < 10; i++ { // 10 из 48 — чуть больше пятой части
		c.Operations[i].SMV = dec("1.5")
	}
	rtNone(t, btFindings(c), "CMT is quoted with SMV on")

	// А девять из сорока восьми — меньше пятой части, и вопрос возвращается.
	c2 := card8()
	btCosting(c2, "120.00")
	c2.ApprovalState = entity.TechCardApprovalInReview
	for i := 0; i < 9; i++ {
		c2.Operations[i].SMV = dec("1.5")
	}
	rtOne(t, btFindings(c2), "CMT is quoted with SMV on 9 of 48 steps")
}

func TestB7IsSilentWithoutACmtFigure(t *testing.T) {
	// Пустое поле — это находка B6, и второго голоса о том же поле быть не должно.
	c := card8()
	btCosting(c, "")
	c.ApprovalState = entity.TechCardApprovalInReview
	rtNone(t, btFindings(c), "CMT is quoted with SMV on")
}

// ── B8 ──────────────────────────────────────────────────────────────────────────────────────────

func TestB8AggregatesFourOfFourOnCard8(t *testing.T) {
	f := rtOne(t, btFindings(card8()), "Cutting wastage is not stated on")
	if f.Title != "Cutting wastage is not stated on 4 of 4 roll-goods lines" {
		t.Errorf("закон агрегации §3.0: одна находка с дробью, а не четыре — %q", f.Title)
	}
	if len(f.Refs) != 3 {
		t.Errorf("агрегированная находка несёт ровно три якоря-образца, got %v", f.Refs)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("NULL-процент — warning, got %s", f.Severity)
	}
}

func TestB8FilesPerLineBelowTheAggregationThreshold(t *testing.T) {
	c := card8()
	for i := range c.BomItems {
		if c.BomItems[i].LineKey != card8BomLining {
			c.BomItems[i].WastagePercent = dec("7")
		}
	}
	f := rtOne(t, btFindings(c), `"подкладка" states no cutting wastage`)
	rtWantRefs(t, f, RefBom("подкладка"))
	rtNone(t, btFindings(c), "not stated on 4 of 4")
}

func TestB8IsSilencedByAnExplicitZero(t *testing.T) {
	// NULL ≠ явный ноль: ноль — законное утверждение «отходов нет», и оно молчит.
	c := card8()
	for i := range c.BomItems {
		c.BomItems[i].WastagePercent = dec("0")
	}
	rtNone(t, btFindings(c), "states no cutting wastage")
	rtNone(t, btFindings(c), "Cutting wastage is not stated")
}

func TestB8ExcludesAMarkerSourcedLine(t *testing.T) {
	// Норма с раскладки уже несёт межлекальные потери — гросс-ап поверх неё посчитал бы их дважды.
	c := card8()
	for i := range c.BomItems {
		if c.BomItems[i].LineKey != card8BomLining {
			c.BomItems[i].WastagePercent = dec("7")
		}
	}
	cw := &c.Colorways[0]
	cw.Usages = append(cw.Usages, entity.TechCardColorwayUsage{
		BomItemId:         sql.NullInt64{Int64: int64(card8BomByKey(c, card8BomLining).Id), Valid: true},
		Consumption:       dec("1.2"),
		ConsumptionSource: text(entity.ConsumptionSourceMarker),
	})
	rtNone(t, btFindings(c), "states no cutting wastage")
}

func TestB8DoesNotExcludeADxfSourcedLine(t *testing.T) {
	// Норма по площади выкроек — НЕТТО, гросс-ап ей полагается by design (0294).
	c := card8()
	for i := range c.BomItems {
		if c.BomItems[i].LineKey != card8BomLining {
			c.BomItems[i].WastagePercent = dec("7")
		}
	}
	cw := &c.Colorways[0]
	cw.Usages = append(cw.Usages, entity.TechCardColorwayUsage{
		BomItemId:         sql.NullInt64{Int64: int64(card8BomByKey(c, card8BomLining).Id), Valid: true},
		Consumption:       dec("1.2"),
		ConsumptionSource: text(entity.ConsumptionSourceDxf),
	})
	rtOne(t, btFindings(c), `"подкладка" states no cutting wastage`)
}

func TestB8AsksAboutAWastageAboveThirty(t *testing.T) {
	c := card8()
	for i := range c.BomItems {
		c.BomItems[i].WastagePercent = dec("5")
	}
	card8BomByKey(c, card8BomMain).WastagePercent = dec("42")

	f := rtOne(t, btFindings(c), "wastes 42% in the cut")
	if f.Category != CategoryQuestion {
		t.Errorf("процент выше тридцати — вопрос, а не дефект: got %s", f.Category)
	}

	// Ровно тридцать — не «выше тридцати».
	c2 := card8()
	for i := range c2.BomItems {
		c2.BomItems[i].WastagePercent = dec("30")
	}
	rtNone(t, btFindings(c2), "in the cut")
}

func TestB8IgnoresCountableSections(t *testing.T) {
	// Счётные секции гросс-ап не гроссят вовсе — процент им не нужен.
	c := card8()
	for i := range c.BomItems {
		c.BomItems[i].WastagePercent = dec("5")
	}
	btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionHardware, Name: "пуговица"})
	btAddBom(c, entity.TechCardBomItem{Section: entity.BomSectionThread, Name: "нитка"})
	rtNone(t, btFindings(c), "states no cutting wastage")
}
