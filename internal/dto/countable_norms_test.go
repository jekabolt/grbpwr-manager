package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/encoding/protojson"
)

// СЧЁТНАЯ НОРМА СЛОТА НА ПРОВОДЕ И В ЧИСЛАХ (0333).
//
// Здесь проверяются ровно те три вещи, которых entity/countable_test.go видеть не может: контракт
// присутствия на проводе (очистить ≠ не прислать), совпадение ПОТРЕБНОСТИ с ДЕНЬГАМИ и то, что
// новые колонки переживают клон и релизный снапшот.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены):
//  1. `countableOmitted := !qtyPerGarment.Valid && !spareQty.Valid` (наивная проверка ПО ЗНАЧЕНИЮ
//     вместо указателей) — TestCountableWirePresenceContract падает на очистке: поле становится
//     неочищаемым, ровно как описано в 03-slot-count.md;
//  2. usageNormForSize перестаёт звать резолвер пары — падает TestCountableDemandMatchesTheMoney:
//     цех получает 600 штук там, где оплачено 700.

func cnNullDec(v string) decimal.NullDecimal { return nd2(v) }

func cnPbDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

// TestCountableWirePresenceContract — ЛОВУШКА ПРОВОДА целиком. У google.type.Decimal нет
// `optional`, а nullDecimalFromPb считает пустым И nil, И Decimal{Value:""}, поэтому «очистить» и
// «не прислали» различаются ТОЛЬКО присутствием указателя. Наивный флаг по значению сделал бы поле
// неочищаемым навсегда: оператор стёр бы шестёрку, а сервер оставил бы её.
func TestCountableWirePresenceContract(t *testing.T) {
	parse := func(b *pb_common.TechCardBomItem) entity.TechCardBomItem {
		t.Helper()
		b.Section = pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE
		b.Name = "Horn button 18L"
		out, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{b})
		require.NoError(t, err)
		require.Len(t, out, 1)
		return out[0]
	}

	// Ни одной половины на проводе — вкладка со старым бандлом. «Не трогай»: store оставит колонки
	// как лежат (IF(:countable_omitted, …)).
	got := parse(&pb_common.TechCardBomItem{})
	require.True(t, got.CountableOmitted)
	require.False(t, got.QtyPerGarment.Valid)
	require.False(t, got.SpareQty.Valid)

	// Значение пришло — пишем обе половины.
	got = parse(&pb_common.TechCardBomItem{QtyPerGarment: cnPbDec("6")})
	require.False(t, got.CountableOmitted)
	require.Equal(t, "6", got.QtyPerGarment.Decimal.String())
	require.False(t, got.SpareQty.Valid, "запас не прислан вместе с количеством — значит его нет")

	// ЯВНАЯ ПУСТОТА = ОЧИСТИТЬ. Это и есть единственная дверь, через которую число снимается со
	// слота, и она обязана отличаться от предыдущего случая.
	got = parse(&pb_common.TechCardBomItem{QtyPerGarment: cnPbDec(""), SpareQty: cnPbDec("")})
	require.False(t, got.CountableOmitted, "явная пустота — это распоряжение, а не молчание")
	require.False(t, got.QtyPerGarment.Valid)
	require.False(t, got.SpareQty.Valid)

	// Пара живёт как одно целое: присланная ЛЮБАЯ половина означает «пиши обе».
	got = parse(&pb_common.TechCardBomItem{SpareQty: cnPbDec("1")})
	require.False(t, got.CountableOmitted)
	require.Equal(t, "1", got.SpareQty.Decimal.String())

	// Отрицательного количества не бывает — отказ field-tagged, а не сырой 3819 из CHECK'а.
	_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{{
		Section:       pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
		Name:          "Horn button 18L",
		QtyPerGarment: cnPbDec("-1"),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "qty_per_garment")
}

// TestCountableSlotSurvivesTheCloneRoundTrip — клон строит payload САМ, теми же конвертерами
// (CloneStyleForSeason: ConvertEntityTechCardToPb → GetTechCard → ConvertPbTechCardInsertToEntity),
// и транспортных флагов не эмитит. Новые колонки обязаны переехать в клон, иначе сезонная копия
// теряет счётную норму молча.
func TestCountableSlotSurvivesTheCloneRoundTrip(t *testing.T) {
	card := countableCard()
	full := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"})
	require.NotNil(t, full.GetTechCard())

	insert, err := ConvertPbTechCardInsertToEntity(full.GetTechCard())
	require.NoError(t, err)

	var slot *entity.TechCardBomItem
	for i := range insert.BomItems {
		if insert.BomItems[i].Name == "Horn button 18L" {
			slot = &insert.BomItems[i]
		}
	}
	require.NotNil(t, slot, "счётный слот доехал до payload'а клона")
	require.Equal(t, "6", slot.QtyPerGarment.Decimal.String())
	require.Equal(t, "1", slot.SpareQty.Decimal.String())
	require.False(t, slot.CountableOmitted,
		"payload клона обязан ПИСАТЬ пару: флаг negative, и «не трогай» на вставке означало бы NULL")
}

// TestCountableSlotIsInTheReleaseSnapshot — релизный блоб это protojson карточки; поле, не
// доехавшее до провода, не доедет и до заморозки, а восстановить его потом будет неоткуда.
func TestCountableSlotIsInTheReleaseSnapshot(t *testing.T) {
	blob, err := protojson.Marshal(ConvertEntityTechCardToPb(countableCard(), CostingFx{Base: "EUR"}))
	require.NoError(t, err)
	require.True(t, strings.Contains(string(blob), `"qtyPerGarment"`), "снапшот несёт количество слота")
	require.True(t, strings.Contains(string(blob), `"spareQty"`), "снапшот несёт запас слота")
}

// TestCountableInheritedValueIsNeverWrittenBackToTheRow — унаследованное число НЕ подставляется в
// строку рецепта: на проводе quantity строки остаётся пустым, а деньги пары она несёт как носитель.
// Стоит подставить — и «технолог сказал 6» перестанет отличаться от «подставилось 6».
func TestCountableInheritedValueIsNeverWrittenBackToTheRow(t *testing.T) {
	card := countableCard()
	cw := &card.Colorways[0]
	usages := ConvertRecipeUsagesToPb(cw.Usages, card.BomItems, card.Pieces, nil, nil)
	require.Len(t, usages, 2)
	for i, u := range usages {
		require.Nil(t, u.Quantity, "строка %d: наследование не записывается в quantity", i)
	}
	require.Equal(t, "14", usages[0].LineTotal.Value, "(6 + запас 1) × 2 — один раз на пару")
	require.Equal(t, "0", usages[1].LineTotal.Value, "второе размещение той же пары денег не добавляет")
}

// TestCountableDemandMatchesTheMoney — ПОТРЕБНОСТЬ И СЕБЕСТОИМОСТЬ ЧИТАЮТ ОДНО ОПРЕДЕЛЕНИЕ.
// Расхождение здесь означает, что цех получает не то количество, которое оплачено: на 100 изделиях
// это 700 штук против 600, линейно по запасу и молча.
func TestCountableDemandMatchesTheMoney(t *testing.T) {
	card := countableCard()
	run := &entity.ProductionRun{Id: 9, ProductionRunInsert: entity.ProductionRunInsert{
		TechCardId: card.Id,
		Lines: []entity.ProductionRunLine{
			{ProductId: sql.NullInt32{Int32: 55, Valid: true}, SizeId: 1, PlannedQty: 100},
		},
	}}
	resp := ComputeProductionRunMaterialPlan(run, card, nil, nil, nil, nil)
	require.Len(t, resp.Rows, 1)
	require.Equal(t, int32(900), resp.Rows[0].MaterialId)
	require.Equal(t, "700", resp.Rows[0].Required.Value,
		"(6 пришитых + 1 запасная) × 100 изделий, ОДИН раз на пару — не 600 и не 1400")
	require.Empty(t, resp.Blockers, "слот со счётной нормой не является строкой без нормы")
}

// TestCountableEstimateStopsCallingTheSlotUnnormed — смета: строка с ценой и без числа честно
// поднимает has_no_norm, а заполнение СЛОТА этот флаг снимает и даёт те же деньги, что карточка.
func TestCountableEstimateStopsCallingTheSlotUnnormed(t *testing.T) {
	fx := CostingFx{Base: "EUR"}

	unnormed := countableCard()
	unnormed.BomItems[0].QtyPerGarment = decimal.NullDecimal{}
	unnormed.BomItems[0].SpareQty = decimal.NullDecimal{}
	est := ComputeStyleCostEstimate(unnormed, 0, nil, fx)
	require.Contains(t, est.Caveat, "no consumption", "непосчитанная счётная строка обязана быть названа")

	est = ComputeStyleCostEstimate(countableCard(), 0, nil, fx)
	require.NotContains(t, est.Caveat, "no consumption", "число на слоте снимает замечание")
	require.Len(t, est.Materials, 2)
	require.Equal(t, "14.00", est.Materials[0].LineTotalBase.Value, "(6 + 1) × 2 на пару")
	require.Equal(t, "0.00", est.Materials[1].LineTotalBase.Value, "второе размещение — ноль, не второе начисление")
	require.Equal(t, "14.00", est.MaterialsPerUnitBase.Value)
}

// countableCard — карточка с ОДНИМ счётным слотом (6 пришивается, 1 в пакетик, 2 EUR за штуку) и
// ДВУМЯ размещениями этого слота в одном колорвее: «планка» и «манжета». Ровно та форма, которую
// 0295 разрешает дословно и на которой ломается построчное чтение слотового числа.
func countableCard() *entity.TechCard {
	c := &entity.TechCard{Id: 7}
	c.Name = "Shirt"
	c.StyleNumber = sql.NullString{String: "S-7", Valid: true}
	c.BomItems = []entity.TechCardBomItem{{
		Id:            101,
		Name:          "Horn button 18L",
		Section:       entity.BomSectionHardware,
		MaterialId:    sql.NullInt64{Int64: 900, Valid: true},
		Unit:          sql.NullString{String: "pcs", Valid: true},
		UnitPrice:     cnNullDec("2"),
		Currency:      sql.NullString{String: "EUR", Valid: true},
		QtyPerGarment: cnNullDec("6"),
		SpareQty:      cnNullDec("1"),
	}}
	c.Colorways = []entity.TechCardColorway{{
		Id: 1, Name: "White", ProductId: sql.NullInt32{Int32: 55, Valid: true},
		Usages: []entity.TechCardColorwayUsage{
			{BomItemId: sql.NullInt64{Int64: 101, Valid: true}, Placement: sql.NullString{String: "front placket", Valid: true}},
			{BomItemId: sql.NullInt64{Int64: 101, Valid: true}, Placement: sql.NullString{String: "cuff", Valid: true}},
		},
	}}
	return c
}
