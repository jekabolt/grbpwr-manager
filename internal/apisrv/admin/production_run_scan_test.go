package admin

import (
	"context"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// scanRuns builds n runs of 10 planned garments each — the amortisation denominator, one page and
// one garment at a time.
func scanRuns(n int) []entity.ProductionRun {
	runs := make([]entity.ProductionRun, 0, n)
	for i := 0; i < n; i++ {
		runs = append(runs, entity.ProductionRun{
			Id: i + 1,
			ProductionRunInsert: entity.ProductionRunInsert{
				Lines: []entity.ProductionRunLine{{SizeId: 4, PlannedQty: 10}},
			},
		})
	}
	return runs
}

// expectPagedRuns wires the store to serve runs in the pages listAllProductionRuns must walk. Each
// page is EXPECTED at its own offset, so a reader that stops after the first one fails twice: on the
// number below and on the unmet expectation.
func expectPagedRuns(pr *mocks.MockProductionRuns, tcID int, runs []entity.ProductionRun) {
	total := len(runs)
	for off := 0; off < total; off += productionRunPageSize {
		end := off + productionRunPageSize
		if end > total {
			end = total
		}
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, off,
			entity.ProductionRunListFilter{TechCardId: tcID}).Return(runs[off:end], total, nil).Once()
	}
}

// 101 прогонов — это 1010 запланированных изделий, а не 1000. Одна страница в 100 прогонов была
// молчаливым потолком на ЗНАМЕНАТЕЛЕ: у стиля со 101 партией вся разработка делилась на первые сто,
// и R&D на единицу выходила завышенной ровно на долю невидимой партии — при том, что возвращённый
// стором total выбрасывался и сказать «цифра неполная» было некому.
func TestStyleEconomicsAmortisesOverEveryRunNotJustTheFirstPage(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	mtr := mocks.NewMockMetrics(t)
	fit := mocks.NewMockFittings(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().Metrics().Return(mtr)
	repo.EXPECT().Fittings().Return(fit)
	repo.EXPECT().ProductionRuns().Return(pr)

	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(runMixSnapshotCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
	tc.EXPECT().ListTechCardDevExpenses(mock.Anything, 7).Return([]entity.TechCardDevExpense{
		{Kind: "sample", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("1010"), Valid: true}},
	}, nil)
	mtr.EXPECT().GetStyleMargin(mock.Anything, 7).Return(nil, nil)
	mtr.EXPECT().GetStyleSampleSummary(mock.Anything, 7).Return(entity.StyleSampleSummary{}, nil)
	mtr.EXPECT().GetStyleMaterialsFromStock(mock.Anything, 7).Return(entity.StyleMaterialsFromStock{}, nil)
	fit.EXPECT().ListFittings(mock.Anything, styleEconomicsFittingScan, 0, entity.Descending, 0, 0, 7).Return(nil, 0, nil)
	expectPagedRuns(pr, 7, scanRuns(101))

	s := &Server{repo: repo}
	resp, err := s.GetStyleEconomics(fullAccessCtx(), &pb_admin.GetStyleEconomicsRequest{TechCardId: 7})
	require.NoError(t, err)
	e := resp.Economics

	require.EqualValues(t, 101, e.Production.Runs, "сто первая партия существует и на карточке")
	require.EqualValues(t, 1010, e.Production.PlannedQtyTotal, "101 × 10, а не 100 × 10")
	require.EqualValues(t, 1010, e.DevCost.OrderQty,
		"знаменатель амортизации — тот же, что и напечатанное рядом plan-количество")
	// 1010 EUR разработки на 1010 изделий = ровно 1 на единицу поверх стилевых 27. Обрезка на сотне
	// дала бы 1010/1000 = 1.01 → 28.01: правдоподобное число, которое ни с чем не сходится.
	//
	// ПРОВЕРКА НА NIL ПЕРЕД РАЗЫМЕНОВАНИЕМ — не косметика. Без неё падение этого теста было не
	// провалом, а ПАНИКОЙ, а паника убивает тестовый бинарь: из 241 теста пакета выполнялось 108,
	// и в тени оставались, в частности, таблица истинности щита узлов сборки и весь набор
	// ИИ-генератора по оборудованию. Утверждение осталось прежним и по-прежнему красное — но
	// теперь оно красное ОДНО, а не вместе с половиной пакета.
	require.NotNil(t, e.DevCost, "экономика без блока разработки — уже провал, дальше смотреть нечего")
	require.NotNil(t, e.DevCost.UnitCostWithDev, "себестоимость с разработкой не посчитана")
	require.Equal(t, "28", e.DevCost.UnitCostWithDev.Value)
}

// Тот же знаменатель во второй команде: обе считают одну амортизацию одного стиля, и разойтись в ней
// они не имеют права.
func TestListTechCardDevExpensesAmortisesOverEveryRun(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	fit := mocks.NewMockFittings(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().TechCards().Return(tc)
	repo.EXPECT().Fittings().Return(fit)
	repo.EXPECT().ProductionRuns().Return(pr)

	tc.EXPECT().ListTechCardDevExpenses(mock.Anything, 7).Return([]entity.TechCardDevExpense{
		{Kind: "sample", AmountBase: decimal.NullDecimal{Decimal: decimal.RequireFromString("1010"), Valid: true}},
	}, nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(runMixSnapshotCard(), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(map[string]decimal.Decimal{}, nil)
	fit.EXPECT().ListFittings(mock.Anything, devExpenseFittingScan, 0, entity.Descending, 0, 0, 7).Return(nil, 0, nil)
	expectPagedRuns(pr, 7, scanRuns(101))

	s := &Server{repo: repo}
	resp, err := s.ListTechCardDevExpenses(fullAccessCtx(), &pb_admin.ListTechCardDevExpensesRequest{TechCardId: 7})
	require.NoError(t, err)
	require.NotNil(t, resp.Summary, "сводка не посчитана — дальше разыменовывать нечего")
	require.EqualValues(t, 1010, resp.Summary.OrderQty)
	// NotNil перед разыменованием по той же причине, что у соседа выше: паника в тесте убивает
	// бинарь и уносит с собой всё, что стояло в очереди после него.
	require.NotNil(t, resp.Summary.UnitCostWithDev, "себестоимость с разработкой не посчитана")
	require.Equal(t, "28", resp.Summary.UnitCostWithDev.Value)
}

// Обход прекращается, когда стор исчерпан, а не когда сошлось с total: страница на границе (ровно
// productionRunPageSize прогонов при том же total) не имеет права уйти во второй запрос, а пустая
// страница обязана останавливать цикл, даже если total ей противоречит.
func TestListAllProductionRunsStopsWhenTheStoreIsExhausted(t *testing.T) {
	t.Run("full page equal to total is one request", func(t *testing.T) {
		pr := mocks.NewMockProductionRuns(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().ProductionRuns().Return(pr)
		runs := scanRuns(productionRunPageSize)
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, 0,
			entity.ProductionRunListFilter{TechCardId: 3}).Return(runs, len(runs), nil).Once()

		s := &Server{repo: repo}
		got, err := s.listAllProductionRuns(context.Background(), entity.ProductionRunListFilter{TechCardId: 3})
		require.NoError(t, err)
		require.Len(t, got, productionRunPageSize)
	})

	t.Run("empty page ends the walk whatever total claims", func(t *testing.T) {
		pr := mocks.NewMockProductionRuns(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().ProductionRuns().Return(pr)
		runs := scanRuns(productionRunPageSize)
		// total врёт (999), рядов больше нет — цикл обязан закончиться, а не крутиться.
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, 0,
			entity.ProductionRunListFilter{}).Return(runs, 999, nil).Once()
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, productionRunPageSize,
			entity.ProductionRunListFilter{}).Return(nil, 999, nil).Once()

		s := &Server{repo: repo}
		got, err := s.listAllProductionRuns(context.Background(), entity.ProductionRunListFilter{})
		require.NoError(t, err)
		require.Len(t, got, productionRunPageSize)
	})

	t.Run("a failing page fails the walk", func(t *testing.T) {
		pr := mocks.NewMockProductionRuns(t)
		repo := mocks.NewMockRepository(t)
		repo.EXPECT().ProductionRuns().Return(pr)
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, 0,
			entity.ProductionRunListFilter{}).Return(scanRuns(productionRunPageSize), 200, nil).Once()
		pr.EXPECT().ListProductionRuns(mock.Anything, productionRunPageSize, productionRunPageSize,
			entity.ProductionRunListFilter{}).Return(nil, 0, context.DeadlineExceeded).Once()

		s := &Server{repo: repo}
		got, err := s.listAllProductionRuns(context.Background(), entity.ProductionRunListFilter{})
		require.Error(t, err, "половина списка — не список: делить по ней значит делить по обрезку")
		require.Nil(t, got)
	})
}
