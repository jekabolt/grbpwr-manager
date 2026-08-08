package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Ф6.7. planned_unit_cost is a snapshot taken ONCE, at creation, and deliberately never re-taken on
// update — so the card under it can move (a re-measured norm moves the card's price and not the
// run's) and the operator has no way to notice. planned_unit_cost_today is the same formula run
// against today's inputs, so the client can print «плановая цена — снапшот от <created_at>; карточка
// сегодня даёт X».
//
// Что здесь доказывается, кроме арифметики: что бейдж загорается только на РАЗНИЦЕ ДАННЫХ. Оба числа
// считает одна функция (plannedUnitCostFor) и рендерит один хелпер, поэтому «карточка не менялась ⇒
// числа РАВНЫ» — это тест не на равенство двух копий формулы, а на отсутствие второй копии.

func ndPlan(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// plannedCostCard is a card whose unit cost is materials (3 units of a fabric priced at unitPrice,
// grossed by the BOM line's 5% cutting-wastage estimate) plus a CMT of 10, all in ccy — the shape the
// dto costing tests use. At unitPrice 2 in EUR it prices at 3×2×1.05 + 10 = 16.30.
func plannedCostCard(id int, ccy, unitPrice string) *entity.TechCard {
	return &entity.TechCard{Id: id, TechCardInsert: entity.TechCardInsert{
		SizeQuantities: []entity.TechCardSizeQuantity{{SizeId: 4, OrderQty: 100}},
		BomItems: []entity.TechCardBomItem{{
			Section: entity.BomSectionFabric, Name: "shell",
			UnitPrice:      ndPlan(unitPrice),
			Currency:       sql.NullString{String: ccy, Valid: ccy != ""},
			WastagePercent: ndPlan("5"),
		}},
		Colorways: []entity.TechCardColorway{{Name: "Black", Usages: []entity.TechCardColorwayUsage{
			{BomItemIndex: sql.NullInt32{Int32: 0, Valid: true}, Consumption: ndPlan("3")},
		}}},
		Costing: &entity.TechCardCosting{CmtCost: ndPlan("10"), Currency: sql.NullString{String: ccy, Valid: ccy != ""}},
	}}
}

// plannedCostRun is a run on card 7 carrying the given frozen snapshot (empty string = no snapshot,
// which is what a non-base costing leaves behind).
func plannedCostRun(snapshot string) *entity.ProductionRun {
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7, Status: entity.ProductionRunPlanned,
		Lines: []entity.ProductionRunLine{{SizeId: 4, PlannedQty: 50}},
	}}
	if snapshot != "" {
		run.PlannedUnitCost = ndPlan(snapshot)
		run.PlannedCurrency = sql.NullString{String: "EUR", Valid: true}
	}
	return run
}

// plannedCostTodayMocks wires the single-run read. GetCostingFxRatesToBase is deliberately NOT
// stubbed here: the release path and the card-read-failure path never reach it, and a test that does
// not reach it must not be told that it did.
func plannedCostTodayMocks(t *testing.T, run *entity.ProductionRun) (*mocks.MockRepository, *mocks.MockTechCards) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	// Maybe(), а не безусловное ожидание: путь без costing:read до карточки НЕ ДОХОДИТ, и именно это
	// там и проверяется. Обязательное ожидание превратило бы «работа не сделана» в провал хелпера.
	repo.EXPECT().TechCards().Return(tc).Maybe()
	pr.EXPECT().GetProductionRun(mock.Anything, run.Id).Return(run, nil)
	return repo, tc
}

// Карточка не двигалась ⇒ сегодняшнее число РАВНО снимку, и бейдж молчит.
func TestGetProductionRunPlannedCostTodayEqualsSnapshotOnUnchangedCard(t *testing.T) {
	run := plannedCostRun("16.3")
	repo, tc := plannedCostTodayMocks(t, run)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(plannedCostCard(7, "EUR", "2"), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)

	resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.NotNil(t, resp.Run.PlannedUnitCost)
	require.NotNil(t, resp.Run.PlannedUnitCostToday)
	require.Equal(t, "16.3", resp.Run.PlannedUnitCostToday.Value)
	require.Equal(t, resp.Run.PlannedUnitCost.Value, resp.Run.PlannedUnitCostToday.Value,
		"an unchanged card must reproduce the snapshot EXACTLY — a difference here would be a difference of formulas, which is the one false positive the badge cannot survive")
}

// Карточка подорожала ⇒ числа расходятся. Это и есть случай бейджа: снимок остался прежним, потому
// что UpdateProductionRun его не пересчитывает, а карточка сегодня даёт другое.
func TestGetProductionRunPlannedCostTodayDivergesWhenCardGotDearer(t *testing.T) {
	run := plannedCostRun("16.3")
	repo, tc := plannedCostTodayMocks(t, run)
	// Fabric re-priced 2 → 3: materials 3×3×1.05 = 9.45, + cmt 10 = 19.45.
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(plannedCostCard(7, "EUR", "3"), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)

	resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.Equal(t, "16.3", resp.Run.PlannedUnitCost.Value, "the frozen snapshot is never re-taken on read")
	require.NotNil(t, resp.Run.PlannedUnitCostToday)
	require.Equal(t, "19.45", resp.Run.PlannedUnitCostToday.Value)
	require.NotEqual(t, resp.Run.PlannedUnitCost.Value, resp.Run.PlannedUnitCostToday.Value)
}

// Небазовая валюта: снимок пуст (setPlannedCostIfBase отказался его писать) — и сегодняшнее число
// обязано быть пустым ПО ТОЙ ЖЕ отсечке. ПУСТО, А НЕ НОЛЬ: ноль здесь читался бы как «карточка
// сегодня бесплатна», то есть как гигантское расхождение, которого нет.
func TestGetProductionRunPlannedCostTodayEmptyOnNonBaseCurrency(t *testing.T) {
	run := plannedCostRun("") // no snapshot: exactly what a USD costing leaves behind
	repo, tc := plannedCostTodayMocks(t, run)
	// USD costing with no USD→EUR rate on file: the figure exists, but not in the base currency.
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(plannedCostCard(7, "USD", "2"), nil)
	tc.EXPECT().GetCostingFxRatesToBase(mock.Anything).Return(nil, nil)

	resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.Nil(t, resp.Run.PlannedUnitCost)
	require.Nil(t, resp.Run.PlannedUnitCostToday, "empty means «сегодня посчитать не удалось», and empty is nil — never a zero decimal")
}

// Расчёт провалился ⇒ прогон всё равно открывается. Карточку могли удалить, чтение могло упасть;
// ни то ни другое не имеет права уронить чтение прогона.
func TestGetProductionRunSurvivesFailedTodayComputation(t *testing.T) {
	for name, cardErr := range map[string]error{
		"card deleted":    sql.ErrNoRows,
		"card read fails": errors.New("tech card read exploded"),
	} {
		t.Run(name, func(t *testing.T) {
			run := plannedCostRun("16.3")
			repo, tc := plannedCostTodayMocks(t, run)
			tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(nil, cardErr)

			resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
			require.NoError(t, err, "the run must still be returned")
			require.NotNil(t, resp.Run)
			require.Equal(t, int32(9), resp.Run.Id)
			require.Equal(t, "16.3", resp.Run.PlannedUnitCost.Value, "the stored snapshot is unaffected")
			require.Nil(t, resp.Run.PlannedUnitCostToday)
		})
	}
}

// Прогон, приколотый к релизу, ценится замороженным скаляром — значит и сегодняшнее его число берётся
// ИЗ РЕЛИЗА, а не из живой карточки. Числа совпадают по построению, и молчание бейджа тут правильно:
// такой прогон ценой карточки не управляется. GetTechCardById не застабан НАМЕРЕННО — вызов живой
// карточки здесь провалил бы тест.
func TestGetProductionRunPlannedCostTodayKeepsReleaseFrozen(t *testing.T) {
	run := plannedCostRun("33")
	run.ReleaseId = sql.NullInt64{Int64: 5, Valid: true}
	repo, tc := plannedCostTodayMocks(t, run)
	tc.EXPECT().GetTechCardRelease(mock.Anything, 5).Return(&entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			Id: 5, TechCardId: 7,
			UnitCost: ndPlan("33"),
			Currency: sql.NullString{String: "EUR", Valid: true},
		},
	}, nil)

	resp, err := (&Server{repo: repo}).GetProductionRun(fullAccessCtx(), &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.Equal(t, "33", resp.Run.PlannedUnitCostToday.Value)
	require.Equal(t, resp.Run.PlannedUnitCost.Value, resp.Run.PlannedUnitCostToday.Value)
}

// На СПИСКЕ поле пусто на каждой строке — это лишний расчёт костинга на строку. TechCards() не
// застабан вовсе, поэтому «пусто» здесь доказано не только по значению, но и по отсутствию работы.
func TestListProductionRunsLeavesPlannedCostTodayEmpty(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	runs := []entity.ProductionRun{
		{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
			TechCardId: 7, Status: entity.ProductionRunPlanned,
			PlannedUnitCost: ndPlan("16.3"),
			PlannedCurrency: sql.NullString{String: "EUR", Valid: true},
		}},
		{Id: 10, ProductionRunInsert: entity.ProductionRunInsert{TechCardId: 8, Status: entity.ProductionRunInProgress}},
	}
	pr.EXPECT().ListProductionRuns(mock.Anything, 0, 0, mock.AnythingOfType("entity.ProductionRunListFilter")).
		Return(runs, len(runs), nil)

	resp, err := (&Server{repo: repo}).ListProductionRuns(fullAccessCtx(), &pb_admin.ListProductionRunsRequest{})
	require.NoError(t, err)
	require.Len(t, resp.Runs, 2)
	for _, r := range resp.Runs {
		require.Nil(t, r.PlannedUnitCostToday, "run %d: the today-figure is a single-read luxury", r.Id)
	}
	require.Equal(t, "16.3", resp.Runs[0].PlannedUnitCost.Value, "the stored snapshot still travels on the list")
}

// Без costing:read сегодняшняя плановая цена НЕ СЧИТАЕТСЯ ВОВСЕ, а не считается-и-стирается.
//
// Карточка и курсы здесь НАМЕРЕННО не застаблены: mockery роняет тест на неожиданном вызове,
// поэтому «не посчитали» доказывается ОТСУТСТВИЕМ работы, а не только пустым полем на проводе.
// Так утечка невозможна ни при какой перестановке строк в хендлере, тогда как проверка одного лишь
// пустого поля пережила бы возврат к порядку «положили → стёрли».
func TestGetProductionRunStripsPlannedCostTodayWithoutCostingRead(t *testing.T) {
	run := plannedCostRun("16.3")
	repo, _ := plannedCostTodayMocks(t, run)

	ctx := authsrv.PutAdminAuthz(context.Background(), authsrv.AdminAuthz{
		Perms: map[string]entity.AccessLevel{rbac.SectionProduction: entity.AccessRead},
	})
	resp, err := (&Server{repo: repo}).GetProductionRun(ctx, &pb_admin.GetProductionRunRequest{Id: 9})
	require.NoError(t, err)
	require.Nil(t, resp.Run.PlannedUnitCostToday, "production:read without costing must not see the recomputed plan price")
	require.Nil(t, resp.Run.PlannedUnitCost, "…nor the frozen one it is compared against")
	require.Empty(t, resp.Run.PlannedCurrency)
	// Quantities survive: the strip removes money, not the run.
	require.Equal(t, int32(9), resp.Run.Id)
	require.Len(t, resp.Run.Run.Lines, 1)
}
