package admin

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Ф4.6 — ХЕНДЛЕР МАТЕРИАЛ-ПЛАНА ЧИТАЕТ НАСТИЛЫ.
//
// Арифметика вся в dto (там же и её тесты); здесь пиннится ПРОВОД: что настилы вообще доезжают до
// расчёта, и что отказ их прочитать НЕ вырождается в тихий откат к норме.

func f46PlanCard() *entity.TechCard {
	card := &entity.TechCard{Id: 7}
	card.BomItems = []entity.TechCardBomItem{{
		Id: 501, Name: "Основная ткань", Section: entity.BomSectionFabric,
		MaterialId: sql.NullInt64{Int64: 100, Valid: true},
		Unit:       sql.NullString{String: "m", Valid: true},
	}}
	card.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "black", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{{
			BomItemId:   sql.NullInt64{Int64: 501, Valid: true},
			Consumption: decimal.NullDecimal{Decimal: decimal.RequireFromString("2"), Valid: true},
		}},
	}}
	card.LinkedMaterials = map[int]entity.MaterialWithPrice{100: {Material: entity.Material{
		Id: 100, MaterialInsert: entity.MaterialInsert{
			Name: "Cotton twill", Unit: sql.NullString{String: "m", Valid: true},
		},
	}}}
	return card
}

func f46PlanRun() *entity.ProductionRun {
	return &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: 7,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 100},
		},
	}}
}

// Настилы прогона доезжают до расчёта: ответ подписан LAYS и несёт измеренный метраж, а не норму.
func TestGetProductionRunMaterialPlanReadsTheLays(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(f46PlanRun(), nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(f46PlanCard(), nil)
	ms.EXPECT().GetMaterialStock(mock.Anything, 100).Return(&entity.MaterialStock{MaterialId: 100, OnHand: decimal.Zero}, nil)
	pr.EXPECT().ListLays(mock.Anything, 9).Return(entity.ProductionRunLayList{
		Applicable: true,
		Lays: []entity.ProductionRunLay{{
			Id: 1, RunId: 9, LayKey: "LAY1", ColorwayId: 55,
			BomItemId: sql.NullInt64{Int64: 501, Valid: true}, BomLineKey: "SLOTKEY",
			Mode: entity.ProductionLayModeFaceUp, EndLossCm: decimal.RequireFromString("2"),
			Name: "настил-1",
			Sections: []entity.ProductionRunLaySection{{
				MarkerId: 9001, Plies: 20, MarkerName: "раскладка",
				MarkerUsedLengthCm: decimal.NullDecimal{Decimal: decimal.RequireFromString("300"), Valid: true},
			}},
		}},
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetProductionRunMaterialPlan(context.Background(), &pb_admin.GetProductionRunMaterialPlanRequest{RunId: 9})
	require.NoError(t, err)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, resp.PlanSource)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, "60.8", resp.Rows[0].Required.Value, "20 × 300 см + 2 × 2 × 20 см = 6080 см")
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_LAYS, resp.Rows[0].Source)
}

// Прочитать настилы не удалось ⇒ ЗАПРОС ПАДАЕТ. Пустой список здесь означал бы «настилов нет», а не
// «мы не знаем», и разница между этими двумя предложениями — целое число в потребности под
// уверенной подписью NORM.
func TestGetProductionRunMaterialPlanFailsWhenLaysCannotBeRead(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(f46PlanRun(), nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(f46PlanCard(), nil)
	ms.EXPECT().GetMaterialStock(mock.Anything, 100).Return(&entity.MaterialStock{MaterialId: 100, OnHand: decimal.Zero}, nil).Maybe()
	pr.EXPECT().ListLays(mock.Anything, 9).Return(entity.ProductionRunLayList{}, errors.New("boom"))

	s := &Server{repo: repo}
	_, err := s.GetProductionRunMaterialPlan(context.Background(), &pb_admin.GetProductionRunMaterialPlanRequest{RunId: 9})
	require.Error(t, err)
	require.Equal(t, codes.Internal, status.Code(err))
}

// Вспомогательная карточка отвечает Applicable=false без настилов — это честное «настилов тут не
// бывает», и особого случая в хендлере оно не требует: потребность просто остаётся нормой.
func TestGetProductionRunMaterialPlanAuxCardStaysOnTheNorm(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	pr := mocks.NewMockProductionRuns(t)
	tc := mocks.NewMockTechCards(t)
	ms := mocks.NewMockMaterialStock(t)
	repo.EXPECT().ProductionRuns().Return(pr)
	repo.EXPECT().TechCards().Return(tc).Maybe()
	repo.EXPECT().MaterialStock().Return(ms).Maybe()

	pr.EXPECT().GetProductionRun(mock.Anything, 9).Return(f46PlanRun(), nil)
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(f46PlanCard(), nil)
	ms.EXPECT().GetMaterialStock(mock.Anything, 100).Return(&entity.MaterialStock{MaterialId: 100, OnHand: decimal.Zero}, nil)
	pr.EXPECT().ListLays(mock.Anything, 9).Return(entity.ProductionRunLayList{
		Applicable: false, NotApplicableReason: entity.ProductionRunLayNotApplicableKey,
	}, nil)

	s := &Server{repo: repo}
	resp, err := s.GetProductionRunMaterialPlan(context.Background(), &pb_admin.GetProductionRunMaterialPlanRequest{RunId: 9})
	require.NoError(t, err)
	require.Equal(t, pb_admin.ProductionRunCoverageSource_PRODUCTION_RUN_COVERAGE_SOURCE_NORM, resp.PlanSource)
	require.Equal(t, "200", resp.Rows[0].Required.Value, "2 × 100, норма без отхода")
}
