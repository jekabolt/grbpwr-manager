package dto

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПРОЕКЦИЯ ПРЕДЛОЖЕНИЯ ПРОЦЕНТА НА ПРОВОД — §9: статусы, UNSPECIFIED никогда, проценты на границе
// провода, штамп против живого пересчёта, разведение артикулов тем же резолвером.

// wastageLay — настил с фактом на слоте 5 карточки wastageCard. markerID=1; штамп netto — по
// желанию теста.
func wastageLay(id, colorwayID int, actualQty string, netto string) entity.ProductionRunLayFact {
	l := entity.ProductionRunLayFact{TechCardId: 7, TechCardName: "худи"}
	l.Id = id
	l.RunId = 900
	l.LayKey = "L" + decimal.NewFromInt(int64(id)).String()
	l.Name = "настил " + decimal.NewFromInt(int64(id)).String()
	l.ColorwayId = colorwayID
	l.BomItemId = sql.NullInt64{Int64: 5, Valid: true}
	l.ActualAt = sql.NullTime{Time: time.Unix(1700000000, 0), Valid: true}
	l.ActualQty = decimal.NullDecimal{Decimal: decimal.RequireFromString(actualQty), Valid: true}
	l.ActualUom = sql.NullString{String: "m", Valid: true}
	l.Sections = []entity.ProductionRunLaySection{{Id: id, LayId: id, MarkerId: 1, Plies: 10, MarkerName: "раскладка А"}}
	if netto != "" {
		l.NettoQty = decimal.NullDecimal{Decimal: decimal.RequireFromString(netto), Valid: true}
		// Базис штампа (MAJOR 4) — как его пишет SaveLay: копия факта, над которым netto считалось.
		l.NettoBasisQty = l.ActualQty
		l.NettoBasisUom = l.ActualUom
	}
	return l
}

// wastageMarkers — раскладка 1 кроит 2 изделия р.1 + 1 изделие р.2 за слой; живое netto одного
// настила на 10 слоёв по нормам wastageCard = 10 × (2×1.20 + 1×1.40) = 38.
func wastageMarkers() map[int]entity.TechCardMarkerSummary {
	m := entity.TechCardMarkerSummary{Id: 1, Name: "раскладка А"}
	m.Composition = []entity.MarkerCompositionEntry{{SizeId: 1, Quantity: 2}, {SizeId: 2, Quantity: 1}}
	return map[int]entity.TechCardMarkerSummary{1: m}
}

func wastageIn(materialID int, lays ...entity.ProductionRunLayFact) BomWastageCalibrationInput {
	return BomWastageCalibrationInput{
		MaterialId: materialID,
		Lays:       lays,
		Cards:      map[int]*entity.TechCard{7: wastageCard()},
		Markers:    wastageMarkers(),
	}
}

func TestBuildBomWastageSuggestionStampBeatsLiveNorm(t *testing.T) {
	// Штамп 40 против живого пересчёта 38: если бы проекция пересчитывала, дрейф был бы +21.05%, а
	// не +15% — числа выбраны так, чтобы подмена знаменателя не могла сойти за округление.
	got := BuildBomWastageSuggestion(wastageIn(42,
		wastageLay(1, 100, "46", "40"),
		wastageLay(2, 100, "46", "40"),
		wastageLay(3, 100, "46", "40"),
	))
	require.Equal(t, pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_READY, got.GetStatus(), got.GetDetail())
	require.Equal(t, "15", got.GetSuggestedWastagePercent().GetValue())
	require.Equal(t, "15", got.GetMedianOverNettoPercent().GetValue())
	require.EqualValues(t, 3, got.GetLayCount())
	require.EqualValues(t, 1, got.GetTechCardCount())
	require.Len(t, got.GetDrifts(), 3)
	for _, d := range got.GetDrifts() {
		require.True(t, d.GetNettoStamped(), "знаменатель со штампа обязан быть назван штампом — живая норма могла уйти")
		require.Equal(t, "40", d.GetNettoQty().GetValue())
		require.Equal(t, "15", d.GetDriftPercent().GetValue())
		require.Empty(t, d.GetSkipped())
		require.Equal(t, "худи", d.GetTechCardName(), "медиана по чужой модели обязана быть видна как таковая")
	}
}

func TestBuildBomWastageSuggestionFallsBackToLiveNorm(t *testing.T) {
	// Без штампа: netto пересчитывается по живой dxf-норме через состав раскладки — 38, дрейф
	// 41.8/38 = +10%.
	got := BuildBomWastageSuggestion(wastageIn(42,
		wastageLay(1, 100, "41.8", ""),
		wastageLay(2, 100, "41.8", ""),
		wastageLay(3, 100, "41.8", ""),
	))
	require.Equal(t, pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_READY, got.GetStatus(), got.GetDetail())
	require.Equal(t, "10", got.GetSuggestedWastagePercent().GetValue())
	for _, d := range got.GetDrifts() {
		require.False(t, d.GetNettoStamped())
		require.Equal(t, "38", d.GetNettoQty().GetValue())
	}

	t.Run("удалённая раскладка оставляет секцию без состава — настил выпадает с причиной", func(t *testing.T) {
		in := wastageIn(42, wastageLay(1, 100, "41.8", ""))
		in.Markers = map[int]entity.TechCardMarkerSummary{}
		got := BuildBomWastageSuggestion(in)
		require.EqualValues(t, 0, got.GetLayCount())
		require.Len(t, got.GetDrifts(), 1)
		require.Contains(t, got.GetDrifts()[0].GetSkipped(), "состав не записан")
	})
}

// MAJOR 4 (правки ревью T7в2): штамп, чей базис разошёлся с фактом (откат DO — старый бинарь правил
// факт, не зная про netto-колонки), ОТБРАСЫВАЕТСЯ, а не принимается: настил падает на живой
// пересчёт, а когда и тот невозможен — причина называет правку мимо штампа.
func TestBuildBomWastageSuggestionDiscardsDesyncedStamp(t *testing.T) {
	desynced := func(id int) entity.ProductionRunLayFact {
		l := wastageLay(id, 100, "41.8", "40")
		// Старый бинарь переписал факт 46 → 41.8; базис остался от старого замера.
		l.NettoBasisQty = decimal.NullDecimal{Decimal: decimal.RequireFromString("46"), Valid: true}
		return l
	}
	got := BuildBomWastageSuggestion(wastageIn(42, desynced(1), desynced(2), desynced(3)))
	require.Equal(t, pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_READY, got.GetStatus(), got.GetDetail())
	require.Equal(t, "10", got.GetSuggestedWastagePercent().GetValue(),
		"поверь код штампу 40 — дрейф был бы +4.5%: несогласованный знаменатель кривил бы медиану молча")
	for _, d := range got.GetDrifts() {
		require.False(t, d.GetNettoStamped(), "отброшенный штамп не смеет называться штампом")
		require.Equal(t, "38", d.GetNettoQty().GetValue(), "знаменатель — живой пересчёт, не осиротевший штамп")
	}

	t.Run("живой пересчёт тоже невозможен — причина называет правку мимо штампа", func(t *testing.T) {
		in := wastageIn(42, desynced(1))
		in.Markers = map[int]entity.TechCardMarkerSummary{} // раскладка удалена: пересчитать не из чего
		got := BuildBomWastageSuggestion(in)
		require.EqualValues(t, 0, got.GetLayCount())
		require.Len(t, got.GetDrifts(), 1)
		require.Contains(t, got.GetDrifts()[0].GetSkipped(), "мимо штампа")
		require.Contains(t, got.GetDrifts()[0].GetSkipped(), "состав не записан")
	})
}

// MAJOR 5 (правки ревью T7в2): окно отбора едет в ответ — «медиана по N» без named-окна
// непроверяема.
func TestBuildBomWastageSuggestionNamesTheWindow(t *testing.T) {
	in := wastageIn(42,
		wastageLay(1, 100, "46", "40"),
		wastageLay(2, 100, "46", "40"),
		wastageLay(3, 100, "46", "40"),
	)
	in.ConsideredLayCount, in.TotalMeasuredLayCount = 3, 120
	got := BuildBomWastageSuggestion(in)
	require.EqualValues(t, 3, got.GetConsideredLayCount())
	require.EqualValues(t, 120, got.GetTotalMeasuredLayCount())
	require.Contains(t, got.GetDetail(), "120", "усечённое окно называется и человеку, не только полем")

	t.Run("окно не усечено — фраза не обвешивается счётчиками", func(t *testing.T) {
		in.ConsideredLayCount, in.TotalMeasuredLayCount = 3, 3
		got := BuildBomWastageSuggestion(in)
		require.EqualValues(t, 3, got.GetConsideredLayCount())
		require.NotContains(t, got.GetDetail(), "рассмотрены")
	})
}

func TestBuildBomWastageSuggestionEmptyBaseIsTheMainlineAnswer(t *testing.T) {
	// На бете настилов НОЛЬ: TOO_FEW_FACTS — основной путь после выката, и он обязан быть
	// СОСТОЯНИЕМ с человеческой фразой, не ошибкой и не UNSPECIFIED.
	got := BuildBomWastageSuggestion(wastageIn(42))
	require.Equal(t, pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_TOO_FEW_FACTS, got.GetStatus())
	require.NotEqual(t, pb_common.BomWastageSuggestionStatus_BOM_WASTAGE_SUGGESTION_STATUS_UNSPECIFIED, got.GetStatus())
	require.EqualValues(t, 0, got.GetLayCount())
	require.NotEmpty(t, got.GetDetail())
	require.Nil(t, got.GetSuggestedWastagePercent())
	require.Nil(t, got.GetMedianOverNettoPercent())
}

func TestBuildBomWastageSuggestionResolvesArticleWithTheOneResolver(t *testing.T) {
	// Карточка не загружена — артикул за настилом не разрешается, настил выбывает МОЛЧА (это замер
	// неизвестно чьего артикула, не «пропущенный замер этого»).
	in := wastageIn(42, wastageLay(1, 100, "46", "40"))
	in.Cards = map[int]*entity.TechCard{}
	got := BuildBomWastageSuggestion(in)
	require.EqualValues(t, 0, got.GetLayCount())
	require.Empty(t, got.GetDrifts())

	t.Run("настил чужого артикула не входит вовсе", func(t *testing.T) {
		got := BuildBomWastageSuggestion(wastageIn(77, wastageLay(1, 100, "46", "40")))
		require.Empty(t, got.GetDrifts(), "слот 5 разрешается в артикул 42, а спросили про 77")
	})
}
