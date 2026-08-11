package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
)

// Релизная ветка плановой цены. Прогон, приколотый к tech_card_release, ценился ОДНИМ замороженным
// скаляром — ценой ПЕРВОГО колорвея карточки, — поэтому партия из 50 чёрных по €10 и 50 белых по
// €30 снимала €10 вместо €20, и вся её plan/fact вариация ехала от этого.
//
// Что здесь доказывается помимо арифметики: что считается это ИСКЛЮЧИТЕЛЬНО по замороженным числам.
// Ни GetTechCardById, ни GetCostingFxRatesToBase в этих тестах не застаблены — mockery роняет тест
// на неожиданном вызове, поэтому «живая карточка и сегодняшние курсы в расчёт не входят» доказано
// ОТСУТСТВИЕМ работы, а не только совпадением чисел. Это и есть условие молчания бейджа: снимок и
// «сегодня» читают одни и те же байты.

// relMixCard — карточка с одним слотом ткани (10 EUR/м по умолчанию слота) и двумя колорвеями:
// чёрный (product 55) берёт умолчание, белый (product 66) ПРИШПИЛИВАЕТ артикул 200 по 30 EUR/м из
// каталога. Норма 1 м на базовом размере 4. Цена изделия: чёрный 10, белый 30.
func relMixCard() *entity.TechCard {
	norm := []entity.TechCardBomSizeConsumption{{SizeId: 4, Consumption: decimal.RequireFromString("1")}}
	card := &entity.TechCard{Id: 7}
	card.SizeIds = []int{4}
	card.BaseSampleSizeId = sql.NullInt32{Int32: 4, Valid: true}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 1, Name: "Shell", Section: entity.BomSectionFabric,
		Unit:       sql.NullString{String: "m", Valid: true},
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		UnitPrice:  ndPlan("10"), Currency: sql.NullString{String: "EUR", Valid: true},
	}}
	card.Colorways = []entity.TechCardColorway{
		{Id: 55, Name: "Black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 1, Valid: true}, SizeConsumptions: norm},
			}},
		{Id: 66, Name: "White", ProductId: sql.NullInt32{Int32: 66, Valid: true},
			Usages: []entity.TechCardColorwayUsage{
				{BomItemId: sql.NullInt64{Int64: 1, Valid: true},
					MaterialId:       sql.NullInt64{Int64: 200, Valid: true},
					SizeConsumptions: norm},
			}},
	}
	// Каталог НА МОМЕНТ РЕЛИЗА: цену пина знает только он, и в блоб она попадёт лишь как
	// посчитанная цена колорвея.
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{
		200: {
			Material:    entity.Material{MaterialInsert: entity.MaterialInsert{Unit: sql.NullString{String: "m", Valid: true}}},
			LatestPrice: &entity.MaterialPrice{MaterialId: 200, Price: decimal.RequireFromString("30"), Currency: "EUR"},
		},
	}
	card.Costing = &entity.TechCardCosting{Currency: sql.NullString{String: "EUR", Valid: true}}
	return card
}

// relRelease — релиз карточки techCardID со скаляром 33. Скаляр намеренно НЕ равен ни одной цене
// колорвея и ни одному их среднему: увидев 33, тест знает, что сработал откат, а не расчёт.
func relRelease(t *testing.T, techCardID int, snapshot string) *entity.TechCardRelease {
	t.Helper()
	return &entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			Id: 5, TechCardId: techCardID, ReleaseNumber: 3,
			UnitCost: ndPlan("33"),
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
		Snapshot: snapshot,
	}
}

// relBlob замораживает карточку РОВНО ТАК ЖЕ, как релиз (snapshotReleaseIfReleased).
func relBlob(t *testing.T, card *entity.TechCard) string {
	t.Helper()
	raw, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(card, dto.CostingFx{Base: "EUR"}))
	require.NoError(t, err)
	return string(raw)
}

func relRunLine(productID, sizeID, qty int) entity.ProductionRunLine {
	ln := entity.ProductionRunLine{SizeId: sizeID, PlannedQty: qty}
	if productID > 0 {
		ln.ProductId = sql.NullInt32{Int32: int32(productID), Valid: true}
	}
	return ln
}

// relSnapshotCost прогоняет создание прогона через ту самую заморозку плановой цены.
func relSnapshotCost(t *testing.T, rel *entity.TechCardRelease, lines []entity.ProductionRunLine) *entity.ProductionRunInsert {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(rel, nil)

	ins := &entity.ProductionRunInsert{
		TechCardId: 7, ReleaseId: sql.NullInt64{Int64: 5, Valid: true},
		Status: entity.ProductionRunPlanned, Lines: lines,
	}
	require.NoError(t, (&Server{repo: repo}).snapshotPlannedCost(context.Background(), ins))
	return ins
}

// ГЛАВНОЕ ЧИСЛО: 50 чёрных по €10 и 50 белых по €30 замораживаются на €20.
func TestReleaseRunSnapshotFollowsColorwayMix(t *testing.T) {
	rel := relRelease(t, 7, relBlob(t, relMixCard()))

	ins := relSnapshotCost(t, rel, []entity.ProductionRunLine{relRunLine(55, 4, 50), relRunLine(66, 4, 50)})

	require.True(t, ins.PlannedUnitCost.Valid)
	require.Equal(t, "20", ins.PlannedUnitCost.Decimal.String(), "(10×50 + 30×50) ÷ 100")
	require.Equal(t, "EUR", ins.PlannedCurrency.String)
	require.NotEqual(t, "10", ins.PlannedUnitCost.Decimal.String(), "цена первого колорвея — ровно то, что снималось раньше")
	require.NotEqual(t, rel.UnitCost.Decimal.String(), ins.PlannedUnitCost.Decimal.String(),
		"и не скаляр релиза: партия ценится собственным миксом")
}

// Партия одного цвета и одного размера ценится ровно как прежде — своей замороженной ценой, той же,
// что стоит в скаляре, когда цвет у карточки один.
func TestReleaseRunSnapshotUnchangedForSingleColorway(t *testing.T) {
	card := relMixCard()
	card.Colorways = card.Colorways[:1] // один колорвей: скаляр релиза И ЕСТЬ его цена
	rel := relRelease(t, 7, relBlob(t, card))
	rel.UnitCost = ndPlan("10")

	ins := relSnapshotCost(t, rel, []entity.ProductionRunLine{relRunLine(55, 4, 100)})

	require.Equal(t, "10", ins.PlannedUnitCost.Decimal.String())
	require.Equal(t, rel.UnitCost.Decimal.String(), ins.PlannedUnitCost.Decimal.String(),
		"прогон одного цвета не имеет права сдвинуться от прежнего числа")
}

// ОТКАТЫ. Каждый оставляет прогон на замороженном скаляре — не на живой карточке (её чтение здесь
// уронило бы тест) и не на NULL.
func TestReleaseRunSnapshotFallsBackToFrozenScalar(t *testing.T) {
	goodBlob := relBlob(t, relMixCard())
	mixLines := []entity.ProductionRunLine{relRunLine(55, 4, 50), relRunLine(66, 4, 50)}

	cases := map[string]struct {
		rel   *entity.TechCardRelease
		lines []entity.ProductionRunLine
	}{
		// Блоб заморожен вместе со схемой, которая с тех пор уехала.
		"снапшот не читается": {rel: relRelease(t, 7, `{"techCard":{"costing":`), lines: mixLines},
		"снапшот пуст":        {rel: relRelease(t, 7, ""), lines: mixLines},
		// Две независимые ссылки прогона (release_id и tech_card_id) никем по дороге не сверяются.
		"релиз чужой карточки": {rel: relRelease(t, 8, goodBlob), lines: mixLines},
		// Заголовок прогона планируют до того, как заполнят грид.
		"линий нет": {rel: relRelease(t, 7, goodBlob), lines: nil},
		// У линии без колорвея замороженной цены цвета нет, а подставить первый колорвей — исходный дефект.
		"линия без колорвея": {rel: relRelease(t, 7, goodBlob), lines: []entity.ProductionRunLine{relRunLine(0, 4, 50)}},
		// Продукт привязали к стилю ПОСЛЕ релиза: в замороженных ценах его нет.
		"колорвея нет в релизе": {rel: relRelease(t, 7, goodBlob), lines: []entity.ProductionRunLine{relRunLine(77, 4, 50)}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			ins := relSnapshotCost(t, c.rel, c.lines)
			require.True(t, ins.PlannedUnitCost.Valid, "откат — это ПРЕЖНЕЕ число, а не пустота")
			require.Equal(t, "33", ins.PlannedUnitCost.Decimal.String(), "замороженный скаляр релиза")
			require.Equal(t, "EUR", ins.PlannedCurrency.String)
		})
	}
}

// Колорвей, которому рецепт никогда не писали, наследует цену СТИЛЯ, а не свою замороженную цифру:
// в colorway_costs у него лежат одни ручные статьи (пустой рецепт = ноль материалов), и взвесить
// партию этим числом значило бы уронить план на всю ткань. Легаси-стиль с одним авторским рецептом
// на все цвета — самая обычная карточка, а не экзотика.
func TestReleaseRunSnapshotInheritsStyleCostForRecipelessColorway(t *testing.T) {
	card := relMixCard()
	card.Colorways[1].Usages = nil // белому рецепт не писали
	card.Costing.CmtCost = ndPlan("5")
	rel := relRelease(t, 7, relBlob(t, card))

	ins := relSnapshotCost(t, rel, []entity.ProductionRunLine{relRunLine(55, 4, 50), relRunLine(66, 4, 50)})

	// Цена стиля = 1 м × 10 + CMT 5 = 15; у белого в блобе заморожено 5 (только CMT).
	require.Equal(t, "15", ins.PlannedUnitCost.Decimal.String(),
		"оба цвета стоят по цене стиля; 10 здесь означало бы, что половина партии посчитана без ткани")
}

// Валюты замороженных цен и скаляра разошлись (костинг в USD, скаляр свёрнут в базовую по курсам) —
// микс не применяется. Иначе рядом с фактическими евро легла бы сумма в другой валюте.
func TestReleaseRunSnapshotKeepsScalarOnCurrencyMismatch(t *testing.T) {
	card := relMixCard()
	card.BomItems[0].Currency = sql.NullString{String: "USD", Valid: true}
	card.Costing.Currency = sql.NullString{String: "USD", Valid: true}
	card.LinkedMaterials[200].LatestPrice.Currency = "USD"

	// Настоящий релиз, а не собранный руками: курс USD→EUR на месте, поэтому writer заморозил бы
	// скаляр в БАЗОВОЙ валюте (ComputeTechCardUnitCost предпочитает unit_cost_base), а
	// colorway_costs остались бы в валюте костинга — ровно то расхождение, ради которого сверка.
	fx := dto.CostingFx{Base: "EUR", ToBase: map[string]decimal.Decimal{"USD": decimal.RequireFromString("0.5")}}
	raw, err := protojson.Marshal(dto.ConvertEntityTechCardToPb(card, fx))
	require.NoError(t, err)
	unit, ccy := dto.ComputeTechCardUnitCost(card, fx)
	require.True(t, unit.Valid)
	require.Equal(t, "EUR", ccy, "скаляр релиза свёрнут в базовую валюту")

	rel := relRelease(t, 7, string(raw))
	rel.UnitCost = unit
	rel.Currency = sql.NullString{String: ccy, Valid: true}

	ins := relSnapshotCost(t, rel, []entity.ProductionRunLine{relRunLine(55, 4, 50), relRunLine(66, 4, 50)})

	require.Equal(t, unit.Decimal.String(), ins.PlannedUnitCost.Decimal.String(), "прогон остался на скаляре релиза")
	require.Equal(t, "EUR", ins.PlannedCurrency.String)
}

// БЕЙДЖ МОЛЧИТ. Снимок здесь НЕ ВПИСАН РУКАМИ: он берётся из той же заморозки, что и на создании
// прогона, — иначе тест сравнивал бы сегодняшнее число с константой из головы автора и продолжал бы
// проходить, даже если бы создание перестало считать микс вовсе.
func TestGetProductionRunReleaseMixTodayEqualsSnapshot(t *testing.T) {
	rel := relRelease(t, 7, relBlob(t, relMixCard()))
	lines := []entity.ProductionRunLine{relRunLine(55, 4, 50), relRunLine(66, 4, 50)}
	frozen := relSnapshotCost(t, rel, lines) // ровно то, что записало бы создание прогона

	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: *frozen}
	run.Id = 9
	repo, tc := plannedCostTodayMocks(t, run)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(rel, nil)

	resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.NotNil(t, resp.Run.PlannedUnitCost)
	require.NotNil(t, resp.Run.PlannedUnitCostToday)
	require.Equal(t, "20", resp.Run.PlannedUnitCostToday.Value, "и это именно взвешенное число, а не скаляр 33")
	require.Equal(t, resp.Run.PlannedUnitCost.Value, resp.Run.PlannedUnitCostToday.Value,
		"расхождение здесь было бы разницей ФОРМУЛ — единственное ложноположительное срабатывание бейджа, которое не от чего отличить")
}
