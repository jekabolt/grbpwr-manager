package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

// Ф5б.4 — ПРОВОДКА РЕЗЕРВА ТКАНИ, а не его арифметика.
//
// Сам писатель претензий и его жизненный цикл проверены в internal/store; здесь пиннится ровно то,
// что живёт в хендлере и больше нигде: КАКОЕ ЧИСЛО уезжает в резерв. Ответ — НЕПОКРЫТЫЙ ОСТАТОК,
// required − issued, и это не деталь реализации, а единственный вариант, который не врёт: выданная
// ткань уже ушла со склада, и удержать её второй раз значит выдумать дефицит ровно на прогоне,
// которому всё выдали.
//
// Фикстуры берутся у Ф4.6 (f46PlanCard / f46PlanRun) СОЗНАТЕЛЬНО: резерв обязан удерживать ровно то
// число, которое материальный план показывает на экране, и общая фикстура — самый дешёвый способ
// сделать расхождение между ними видимым здесь, а не на складе.

// reserveMocks wires the repo for one reconcile pass over run 9.
func reserveMocks(t *testing.T, issued []entity.MaterialMovement) (*Server, *mocks.MockMaterialStock) {
	t.Helper()
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	run := f46PlanRun()
	run.MaterialMovements = issued
	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(run, nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(f46PlanCard(), nil)
	ms.EXPECT().GetMaterialStock(mock.Anything, 100).
		Return(&entity.MaterialStock{MaterialId: 100, OnHand: decimal.RequireFromString("500")}, nil)
	// Настилов нет — потребность берётся из НОРМЫ: 100 изделий × 2 м = 200 м.
	pr.EXPECT().ListLays(mock.Anything, 9).Return(entity.ProductionRunLayList{Applicable: true}, nil)
	return &Server{repo: repo}, ms
}

// Без единой выдачи удерживается вся потребность.
func TestRunReservationHoldsTheWholeRequirementWhenNothingIssued(t *testing.T) {
	s, ms := reserveMocks(t, nil)

	var got map[int]entity.RunMaterialRequirement
	ms.EXPECT().SetRunMaterialReservations(mock.Anything, 9, mock.Anything, "cutter").
		Run(func(_ context.Context, _ int, req map[int]entity.RunMaterialRequirement, _ string) {
			got = req
		}).Return(nil).Once()

	s.reconcileRunReservations(context.Background(), 9, "cutter")
	require.Len(t, got, 1)
	require.True(t, got[100].Qty.Equal(decimal.RequireFromString("200")),
		"100 изделий × 2 м, ничего не выдано — держим все 200 м, получили %s", got[100].Qty)
}

// ВЫДАННОЕ ВЫЧИТАЕТСЯ. Это тот самый двойной счёт, ради которого остаток и считается: 200 м
// потребности при 150 м выданных — это 50 м, которые ещё надо удержать, а не 200.
func TestRunReservationHoldsOnlyTheOutstandingRemainder(t *testing.T) {
	s, ms := reserveMocks(t, []entity.MaterialMovement{{
		MaterialId:   100,
		MovementType: entity.MaterialMovementIssueProduction,
		Quantity:     decimal.RequireFromString("150"),
	}})

	var got map[int]entity.RunMaterialRequirement
	ms.EXPECT().SetRunMaterialReservations(mock.Anything, 9, mock.Anything, "cutter").
		Run(func(_ context.Context, _ int, req map[int]entity.RunMaterialRequirement, _ string) {
			got = req
		}).Return(nil).Once()

	s.reconcileRunReservations(context.Background(), 9, "cutter")
	require.True(t, got[100].Qty.Equal(decimal.RequireFromString("50")),
		"200 м нужно, 150 м уже выдано — держать осталось 50 м, получили %s", got[100].Qty)
}

// ВЫДАНО ВСЁ ⇒ КЛЮЧА В КАРТЕ НЕТ ВОВСЕ, и это не то же самое, что нулевая претензия. Писатель делает
// открытые претензии прогона РАВНЫМИ карте, поэтому отсутствие ключа СНИМАЕТ удержание — а претензия
// на ноль метров висела бы строкой, которая ничего не держит и которую всё равно надо закрывать.
func TestRunReservationDropsTheMaterialOnceEverythingIsIssued(t *testing.T) {
	s, ms := reserveMocks(t, []entity.MaterialMovement{{
		MaterialId:   100,
		MovementType: entity.MaterialMovementIssueProduction,
		Quantity:     decimal.RequireFromString("200"),
	}})

	var got map[int]entity.RunMaterialRequirement
	ms.EXPECT().SetRunMaterialReservations(mock.Anything, 9, mock.Anything, "cutter").
		Run(func(_ context.Context, _ int, req map[int]entity.RunMaterialRequirement, _ string) {
			got = req
		}).Return(nil).Once()

	s.reconcileRunReservations(context.Background(), 9, "cutter")
	require.NotContains(t, got, 100, "выдано всё — удерживать нечего, и строки быть не должно")
}

// ПРОМАХ РЕЗЕРВА НЕ ПАДАЕТ НАРУЖУ. Оба вызова стоят ПОСЛЕ зафиксированной основной записи, поэтому
// вернуть отсюда ошибку значило бы сказать «не получилось» про запись, которая получилась.
func TestRunReservationFailureIsSwallowedBecauseTheWriteAlreadyCommitted(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(nil, errors.New("db is down"))

	// Не паникует и ничего не возвращает — сигнатура без ошибки И ЕСТЬ это решение, произнесённое
	// типом: у вызывающего нет способа уронить запрос на промахе резерва, даже если он захочет.
	(&Server{repo: repo}).reconcileRunReservations(context.Background(), 9, "cutter")
}

// Строка плана без артикула каталога в резерв не едет: претензия к материалу #0 не закрывается ничем.
func TestRunReservationSkipsRowsWithoutACatalogueMaterial(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().ProductionRuns().Return(pr).Maybe()
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	card := f46PlanCard()
	// Слот без привязки к каталогу — свободный текст в BOM, законное состояние карточки.
	card.BomItems[0].MaterialId = sql.NullInt64{}
	card.LinkedMaterials = nil
	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(f46PlanRun(), nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)
	pr.EXPECT().ListLays(mock.Anything, 9).Return(entity.ProductionRunLayList{Applicable: true}, nil)

	var got map[int]entity.RunMaterialRequirement
	ms.EXPECT().SetRunMaterialReservations(mock.Anything, 9, mock.Anything, "cutter").
		Run(func(_ context.Context, _ int, req map[int]entity.RunMaterialRequirement, _ string) {
			got = req
		}).Return(nil).Once()

	s := &Server{repo: repo}
	s.reconcileRunReservations(context.Background(), 9, "cutter")
	require.NotContains(t, got, 0, "материал #0 — не материал")
}
