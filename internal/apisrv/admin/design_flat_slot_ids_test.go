package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ═══ `flat_slot_ids` — УБРАТЬ ОДНУ ПЛИТУ, НЕ ВЫНИМАЯ ЕЁ ИЗ СЛОТА (J-10) ════════════════════════
//
// До этого списка у человека было ровно два ответа про плиты верстака — «все» и «ни одной», — и
// чтобы не отдавать модели ОДНУ мешающую плиту, ему приходилось вынуть её из слота, то есть
// изменить состояние карточки ради параметра одного прогона.
//
// ⚠ ЧТО ЗДЕСЬ ОБЯЗАНО КРАСНЕТЬ, ПОМИМО САМОГО ОТБОРА: пустой список обязан значить «ВСЕ». Это и
// есть совместимость — каждый замороженный прогон и каждый клиент, не знающий поля, продолжают
// значить ровно то, что значили. Проба на одно только сужение прошла бы и на правке, которая
// читает пустой список как «ни одной», а такая правка молча обезоруживает КАЖДЫЙ старый прогон.

// flatBenchOfThree — три заполненных флэт-слота одной карточки, различимые и по id, и по медиа.
func flatBenchOfThree() []entity.DesignBenchSlot {
	return []entity.DesignBenchSlot{
		{Id: 1, TechCardId: 41, ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: 11, Valid: true},
			Picture:   &entity.DesignPicture{Id: 11, MediaId: 111, Kind: entity.DesignPictureKindFlat}},
		{Id: 2, TechCardId: 41, ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: 12, Valid: true},
			Picture:   &entity.DesignPicture{Id: 12, MediaId: 112, Kind: entity.DesignPictureKindFlat}},
		{Id: 3, TechCardId: 41, ViewKey: entity.DesignViewSideL, Kind: entity.DesignPictureKindFlat,
			PictureId: sql.NullInt32{Int32: 13, Valid: true},
			Picture:   &entity.DesignPicture{Id: 13, MediaId: 113, Kind: entity.DesignPictureKindFlat}},
	}
}

func mediaOfSlots(slots []*pb_common.DesignInputSlot) []int32 {
	out := make([]int32, 0, len(slots))
	for _, s := range slots {
		out = append(out, s.GetMediaId())
	}
	return out
}

func TestFlatSlotIdsNarrowsThePlatesAndEmptyStillMeansAll(t *testing.T) {
	bench := flatBenchOfThree()

	cases := []struct {
		name      string
		kind      string
		params    *pb_common.DesignRunParams
		wantMedia []int32
	}{
		{
			// НАЗВАЛИ ОДИН СЛОТ — УЕХАЛА ОДНА ПЛИТА.
			name: "one named slot travels alone",
			kind: entity.DesignRunKindFlat,
			params: &pb_common.DesignRunParams{
				UseFlatSlots: true, FlatSlotIds: []int32{2},
			},
			wantMedia: []int32{112},
		},
		{
			name: "two named slots travel, the third is excluded",
			kind: entity.DesignRunKindFlat,
			params: &pb_common.DesignRunParams{
				UseFlatSlots: true, FlatSlotIds: []int32{1, 3},
			},
			wantMedia: []int32{111, 113},
		},
		{
			// ПУСТОЙ СПИСОК — СЕГОДНЯШНЕЕ ПОВЕДЕНИЕ, ТО ЕСТЬ ВСЕ ЗАПОЛНЕННЫЕ СЛОТЫ. Это тот самый
			// член правила, ради которого поле вообще можно было добавить, не переписав историю.
			name:      "an empty list is today's behaviour and means every filled slot",
			kind:      entity.DesignRunKindFlat,
			params:    &pb_common.DesignRunParams{UseFlatSlots: true},
			wantMedia: []int32{111, 112, 113},
		},
		{
			// «НИ ОДНОЙ» УЖЕ ИМЕЕТ СВОЁ ПРАВОПИСАНИЕ, И ОНО НЕ ЗДЕСЬ. Список без выключателя не
			// включает верстак сам по себе — иначе поле стало бы вторым выключателем.
			name:      "the list alone does not switch the bench on",
			kind:      entity.DesignRunKindFlat,
			params:    &pb_common.DesignRunParams{FlatSlotIds: []int32{1}},
			wantMedia: []int32{},
		},
		{
			// ⚠ РЕНДЕР ИГНОРИРУЕТ СПИСОК ЦЕЛИКОМ. Плиты флэтов — СОДЕРЖАНИЕ этого рода, а не
			// опция; сужать их этим полем значило бы дать одному полю два разных смысла на разных
			// маршрутах. Без этой строки правка, применившая фильтр везде, прошла бы молча и
			// обезоружила бы главный маршрут полосы.
			name: "render ignores the list — its plates are the kind's content, not an option",
			kind: entity.DesignRunKindRender,
			params: &pb_common.DesignRunParams{
				UseFlatSlots: true, FlatSlotIds: []int32{2},
			},
			wantMedia: []int32{111, 112, 113},
		},
		{
			// ⚠ ЭТА СТРОКА ОПИСЫВАЕТ ВТОРУЮ ПОЛОВИНУ ПРАВИЛА, А НЕ ПОВЕДЕНИЕ ДВЕРИ. Отбор сужает
			// ПЕРЕСЕЧЕНИЕМ с этим верстаком, поэтому чужой карточке отсюда не достаётся ничего —
			// и это ровно то свойство, ради которого пересечение и выбрано.
			//
			// НО ПУСТОЙ РЕЗУЛЬТАТ САМ ПО СЕБЕ ДО ОТБОРА НЕ ДОХОДИТ: устаревший адрес отвергает
			// дверь designRefuseForeignFlatSlots ДО ДЕНЕГ (см. TestRefuseForeignFlatSlots).
			// В первой редакции двери не было, и эта проба УТВЕРЖДАЛА ДЕФЕКТ — «оплачен и пуст» —
			// как норму. Здесь она осталась только как описание пересечения.
			name: "the selection intersects, so an unmatched id contributes nothing (the door refuses it earlier)",
			kind: entity.DesignRunKindFlat,
			params: &pb_common.DesignRunParams{
				UseFlatSlots: true, FlatSlotIds: []int32{9999},
			},
			wantMedia: []int32{},
		},
		{
			// НОЛЬ И ОТРИЦАТЕЛЬНОЕ — НЕ АДРЕСА, и они не делают список пустым: список, в котором
			// ЕСТЬ настоящий адрес, сужает по нему.
			name: "zeroes in the list are not addresses and do not widen it back",
			kind: entity.DesignRunKindFlat,
			params: &pb_common.DesignRunParams{
				UseFlatSlots: true, FlatSlotIds: []int32{0, 3},
			},
			wantMedia: []int32{113},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			slots, plates := designSelectBench(designInputSources{
				Kind: c.kind, Bench: bench, Params: c.params,
			})
			require.Equal(t, c.wantMedia, mediaOfSlots(slots))
			// ⚠ ДВЕ ПРОЕКЦИИ ОДНОГО ЦИКЛА обязаны согласиться. `source_picture_ids` — это то,
			// чем панель прогона поднимает саму плиту; разойдись он со снимком, история назвала бы
			// входом плиту, которой модель не видела.
			require.Len(t, plates, len(c.wantMedia),
				"штамп исходных плит обязан считать те же слоты, что и снимок")
		})
	}
}

// СУЖЕНИЕ НЕ ЛЕЗЕТ НА ВЫБОРОЧНЫЙ ПУТЬ ПРАВКИ. `fix_slot_ids` — своя дверь и свой смысл; два
// независимых сужения одного списка разошлись бы ровно там, где человек сузил дважды, и тише
// всего — на прогоне, где он сузил обоими.
func TestFlatSlotIdsDoesNotTouchTheSelectiveFixPath(t *testing.T) {
	bench := flatBenchOfThree()

	slots, _ := designSelectBench(designInputSources{
		Kind:  entity.DesignRunKindFlat,
		Bench: bench,
		Params: &pb_common.DesignRunParams{
			// Правка названной плиты: `use_flat_slots` не сказан вовсе, зато сказан fix_slot_ids.
			FixSlotIds: []int32{1},
			// …и список J-10, который на этом пути обязан быть НЕ ПРИМЕНЁН.
			FlatSlotIds: []int32{2},
		},
	})
	require.Equal(t, []int32{111}, mediaOfSlots(slots),
		"выборочная правка сужает своими fix_slot_ids; flat_slot_ids на этом пути молчит")
}

// ═══ ДВЕРЬ ДЛЯ `flat_slot_ids` — «ПРИНЯТО И НЕ ИСПОЛНЕНО» ЗАКРЫТО (F5) ════════════════════════
//
// Поле сужает плиты пересечением с верстаком, поэтому адрес, не называющий ни одного пригодного
// слота, просто ни с чем не совпадал: прогон СОЗДАВАЛСЯ, ОПЛАЧИВАЛСЯ и замерзал со снимком «плит
// нет» — неотличимо от use_flat_slots=false и без отказа, который человек мог бы прочесть, хотя он
// именно ПОПРОСИЛ послать эти плиты.
func TestRefuseForeignFlatSlots(t *testing.T) {
	bench := flatBenchOfThree()
	src := designInputSources{Kind: entity.DesignRunKindFlat, Bench: bench}

	// ⚠ ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПЕРВЫМ: настоящий адрес обязан ПРОХОДИТЬ. Без него все отказы ниже
	// зеленели бы и на двери, которая отвергает всё подряд, — то есть на мёртвой возможности.
	require.NoError(t, designRefuseForeignFlatSlots(41,
		&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{1, 3}}, bench, src))

	// УСТАРЕВШИЙ ИЛИ ЧУЖОЙ АДРЕС — ОТКАЗ ДО ДЕНЕГ.
	err := designRefuseForeignFlatSlots(41,
		&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{9999}}, bench, src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "flat_slot_ids")
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	// ЧАСТИЧНОЕ СОВПАДЕНИЕ — ТОЖЕ ОТКАЗ. Молча исполнить половину просьбы значит отдать модели не
	// тот набор плит, который человек назвал, и не сказать ему об этом.
	require.Error(t, designRefuseForeignFlatSlots(41,
		&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{1, 9999}}, bench, src))

	// НОЛЬ — НЕ АДРЕС, и дверь говорит это прямо, а не пропускает его в отбор, где он растворится.
	require.Error(t, designRefuseForeignFlatSlots(41,
		&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{0}}, bench, src))

	// СЛОТ БЕЗ ПЛИТЫ НЕПРИГОДЕН — предикат двери обязан совпадать с отбором, который пустой слот
	// выбрасывает. Дверь, не спросившая про плиту, приняла бы адрес, который прогон не исполнит.
	empty := append(flatBenchOfThree(), entity.DesignBenchSlot{
		Id: 4, TechCardId: 41, ViewKey: entity.DesignViewSideR, Kind: entity.DesignPictureKindFlat,
	})
	require.Error(t, designRefuseForeignFlatSlots(41,
		&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{4}}, empty,
		designInputSources{Kind: entity.DesignRunKindFlat, Bench: empty}))

	// ─── ГДЕ ДВЕРЬ ОБЯЗАНА МОЛЧАТЬ ───
	//
	// Каждая строка — отдельный способ сделать дверь слишком громкой; ложный отказ здесь стоит
	// человеку прогона, который он имел право запустить.
	silent := []struct {
		name   string
		card   int
		params *pb_common.DesignRunParams
		kind   string
	}{
		{"пустой список значит ВСЕ плиты", 41,
			&pb_common.DesignRunParams{UseFlatSlots: true}, entity.DesignRunKindFlat},
		{"выключатель выключен — плит нет по другой причине", 41,
			&pb_common.DesignRunParams{FlatSlotIds: []int32{9999}}, entity.DesignRunKindFlat},
		{"не флэт — поле игнорируется целиком", 41,
			&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{9999}},
			entity.DesignRunKindRender},
		{"выборочная правка сужает себя сама", 41,
			&pb_common.DesignRunParams{UseFlatSlots: true, FlatSlotIds: []int32{9999},
				FixTargets: []string{entity.DesignViewFront}}, entity.DesignRunKindFlat},
	}
	for _, c := range silent {
		t.Run(c.name, func(t *testing.T) {
			require.NoError(t, designRefuseForeignFlatSlots(c.card, c.params, bench,
				designInputSources{Kind: c.kind, Bench: bench}))
		})
	}
}
