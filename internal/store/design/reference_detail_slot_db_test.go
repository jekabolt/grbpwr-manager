package design_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ РЕФЕРЕНС РОЛИ `detail` ЗНАЕТ, КАКОЙ ИМЕННО ДЕТАЛИ ОН ПРО — 0360, J-9 ═════════════════════
//
// Человек выбирает референсу роль `detail` и вписывает имя детали; клиент при этом делает два
// письма — роль в design_reference и ПУСТОЙ слот верстака с этим именем. До этой колонки строки
// были друг другу чужими, и ячейка референса умела напечатать только голое слово `detail`, хотя
// имя человек уже напечатал сам.
//
// ССЫЛКА, А НЕ КОПИЯ ИМЕНИ: имя детали переименовываемо, и копия разошлась бы с оригиналом молча.

// ЧЕТЫРЕ ЧЛЕНА ПРАВИЛА, И КАЖДЫЙ ОБЯЗАН УМЕТЬ КРАСНЕТЬ ОТДЕЛЬНО: записать, СОХРАНИТЬ при нуле,
// ОЧИСТИТЬ при смене роли, обнулиться по FK при удалении слота.
func TestDesignDBReferenceDetailSlotWriteKeepClearAndFK(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    card,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "collar",
		Actor:         "probe",
	})
	require.NoError(t, err)
	require.Positive(t, slot.Id)
	require.Equal(t, "collar", slot.DetailName.String)

	// (1) ЗАПИСЬ.
	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: slot.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, ref.DetailSlotId.Valid)
	require.Equal(t, int32(slot.Id), ref.DetailSlotId.Int32)

	// (2) ПОВТОР С НУЛЁМ СОХРАНЯЕТ СВЯЗЬ.
	//
	// ⚠ ЭТО САМЫЙ ОСТРЫЙ ЧЛЕН ПРАВИЛА. Ноль на проводе неотличим от незаполненного поля, поэтому
	// вкладка, которая правит ЗАПИСКУ и о детали не думает вовсе, шлёт сюда ноль. Прочитай его как
	// «сотри» — и связь исчезает без единого жеста человека; ровно эта беда уже случилась однажды
	// с самой запиской (0348).
	ref, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: 0, Note: "только воротник", Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.True(t, ref.DetailSlotId.Valid, "ноль это «про слот ничего не сказано», а не «сотри»")
	require.Equal(t, int32(slot.Id), ref.DetailSlotId.Int32)
	require.Equal(t, "только воротник", ref.Note.String)

	// (3) СМЕНА РОЛИ ОЧИЩАЕТ СВЯЗЬ. Референс, переставший быть деталью, не может продолжать
	// указывать на деталь — иначе клиент напечатал бы имя детали над картинкой роли `front`.
	ref, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewFront,
		DetailSlotId: slot.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.False(t, ref.DetailSlotId.Valid,
		"у роли, отличной от detail, связь с деталью обязана быть пустой — даже если её прислали")

	// (4) FK ON DELETE SET NULL. Вернуть роль детали, удалить слот — строка обязана ВЫЖИТЬ и
	// честно замолчать, а не исчезнуть вместе с адресом.
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: slot.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)

	require.NoError(t, rep.Design().DeleteDetailSlot(ctx, slot.Id))

	require.Equal(t, 1,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference WHERE tech_card_id = ? AND media_id = ?`,
			card, media),
		"удаление адреса не имеет права уносить картинку из промпта")
	require.Equal(t, 0,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference
			WHERE tech_card_id = ? AND media_id = ? AND detail_slot_id IS NOT NULL`, card, media),
		"FK обязан обнулить указатель, а не оставить его смотреть в никуда")
}

// СВЯЗЬ ПЕРЕЖИВАЕТ ЧТЕНИЕ ПОЛОСЫ.
//
// ⚠ НЕ ПОВТОР ПРОБЫ ВЫШЕ: та читает ответ пишущей двери, эта поднимает строку заново тем самым
// `SELECT *`, которым полосу читает клиент. Забытый тег `db` прошёл бы первую пробу целиком.
func TestDesignDBReferenceDetailSlotSurvivesTheBandRead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    card,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "cuff",
		Actor:         "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: slot.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)

	band, err := rep.Design().GetBand(ctx, card, 50)
	require.NoError(t, err)

	var found bool
	for _, r := range band.References {
		if r.MediaId != media {
			continue
		}
		found = true
		require.True(t, r.DetailSlotId.Valid)
		require.Equal(t, int32(slot.Id), r.DetailSlotId.Int32)
	}
	require.True(t, found, "референс обязан быть в полосе — иначе проба ничего не утверждает")
}

// ГРАНИЦА КАРТОЧКИ: ДЕТАЛЬ ЧУЖОЙ КАРТОЧКИ ОТКАЗЫВАЕТСЯ.
//
// ⚠ FK ОДИН ЭТОГО НЕ ЗАКРЫВАЕТ — он проверяет лишь СУЩЕСТВОВАНИЕ строки, а слоты всех карточек
// живут в одной таблице. Без этой двери референс карточки A указал бы на деталь карточки B, и
// клиент, который РИСУЕТ ИМЯ по этому id, напечатал бы человеку чужое слово.
//
// ОБЕ КАРТОЧКИ НАСТОЯЩИЕ И ОБЕ НЕПУСТЫЕ — сломанный скоуп на карточке-одиночке выглядит идеально.
func TestDesignDBReferenceDetailSlotRefusesAForeignAndANonDetailSlot(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine, other := probeCard(t, raw), probeCard(t, raw)
	media := probeMedia(t, raw)

	foreign, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    other,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "их воротник",
		Actor:         "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: mine, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: foreign.Id, Ordinal: 1, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
		"деталь ЧУЖОЙ карточки не адресуется отсюда")
	require.Equal(t, 0,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference WHERE tech_card_id = ?`, mine),
		"отказ обязан откатить и саму роль — иначе полуписьмо")

	// НЕ-ДЕТАЛЬ СВОЕЙ КАРТОЧКИ — ТОЖЕ ОТКАЗ. Связь называется detail_slot_id и осмысленна только
	// у детали; слот стороны имени не несёт вовсе (detail_name у него NULL), и указатель на него
	// клиенту нечего было бы напечатать.
	side, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: mine,
		Slot:       entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId:  probePicture(t, rep, raw, mine, entity.DesignPictureKindFlat).Id,
		Actor:      "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: mine, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: side.Id, Ordinal: 1, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
		"слот стороны — не деталь, и указывать на него detail_slot_id нечем")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: своя деталь проходит. Без него обе проверки выше зеленели бы и на
	// стороже, который отказывает ВСЕГДА, — то есть на полностью мёртвой возможности.
	ok, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    mine,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "мой воротник",
		Actor:         "probe",
	})
	require.NoError(t, err)
	ref, err := rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: mine, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: ok.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, int32(ok.Id), ref.DetailSlotId.Int32)
}

// ОТРИЦАТЕЛЬНЫЙ АДРЕС ОТВЕРГАЕТСЯ, А НЕ ЧИТАЕТСЯ КАК «СОТРИ» (F4).
//
// ⚠ У ПРАВИЛА БЫЛ ЧЕТВЁРТЫЙ, НЕЗАДУМАННЫЙ ЧЛЕН. «Сохранить» ловило ровно ноль, «записать» — строго
// положительное, поэтому минус проваливался мимо обоих и доезжал до NULL: связь исчезала МОЛЧА, на
// вызове, который про деталь ничего не говорил. Ни одна дверь знака не проверяла.
func TestDesignDBReferenceDetailSlotRefusesANegativeId(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	media := probeMedia(t, raw)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    card,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "hem",
		Actor:         "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: slot.Id, Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)

	// Тот самый вызов «правлю записку», который стирал связь.
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: media, Role: entity.DesignViewDetail,
		DetailSlotId: -1, Note: "editing the note", Ordinal: 1, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument,
		"минус приходит от сломанного писателя, и принять его молчком значит стереть связь")

	require.Equal(t, 1,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference
			WHERE tech_card_id = ? AND media_id = ? AND detail_slot_id = ?`, card, media, slot.Id),
		"отказ обязан оставить связь нетронутой")
}

// ПОВТОРНЫЙ РАЗРЕЗ НЕ УНОСИТ ИМЯ ДЕТАЛИ, ВПИСАННОЕ ЧЕЛОВЕКОМ (F3).
//
// ⚠ ВТОРОЙ ПИСАТЕЛЬ РОЛИ — апсерт внутри SplitPicture — обнулял detail_slot_id БЕЗУСЛОВНО, даже
// когда кадр режется с видом `detail` и роль не меняется вовсе. Записка строкой выше от этого
// защищена намеренно («не перечислена — не затирается»), а связь с деталью не была.
func TestDesignDBSplitDoesNotEraseAHumanSetDetailLink(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:    card,
		Slot:          entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		NewDetailName: "pocket",
		Actor:         "probe",
	})
	require.NoError(t, err)

	cropMedia := probeMedia(t, raw)
	_, err = rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames:   []entity.DesignSplitFrame{{MediaId: cropMedia, ViewKey: entity.DesignViewDetail}},
		ForInput: true,
	})
	require.NoError(t, err)

	// Человек называет деталь для этого самого кадра.
	_, err = rep.Design().SetReferenceRole(ctx, entity.DesignReferenceRole{
		TechCardId: card, MediaId: cropMedia, Role: entity.DesignViewDetail,
		DetailSlotId: slot.Id, Note: "именно этот карман", Ordinal: 1, Actor: "probe",
	})
	require.NoError(t, err)

	// Второй разрез ДРУГОГО листа, называющий ТО ЖЕ медиа тем же видом — путь, на котором апсерт
	// роли срабатывает второй раз по существующей строке.
	other := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	_, err = rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: other.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames:   []entity.DesignSplitFrame{{MediaId: cropMedia, ViewKey: entity.DesignViewDetail}},
		ForInput: true,
	})
	require.NoError(t, err)

	require.Equal(t, 1,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference
			WHERE tech_card_id = ? AND media_id = ? AND detail_slot_id = ?`, card, cropMedia, slot.Id),
		"разрез не знает адреса на верстаке и потому не имеет права его стирать")

	// ⚠ И ОБРАТНАЯ ПОЛОВИНА: когда роль ВСЁ-ТАКИ меняется на не-деталь, связь обязана исчезнуть —
	// иначе «сохранить» превратилось бы в «никогда не очищать», а это уже другой дефект.
	third := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	_, err = rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: third.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames:   []entity.DesignSplitFrame{{MediaId: cropMedia, ViewKey: entity.DesignViewFront}},
		ForInput: true,
	})
	require.NoError(t, err)
	require.Equal(t, 0,
		countRows(t, raw, `SELECT COUNT(*) FROM design_reference
			WHERE tech_card_id = ? AND media_id = ? AND detail_slot_id IS NOT NULL`, card, cropMedia),
		"роль перестала быть деталью — связь обязана очиститься")
}
