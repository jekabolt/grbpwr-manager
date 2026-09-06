package design_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// B7: УСЫНОВЛЕНИЕ ПРИ ПОСТАНОВКЕ — «СДЕЛАТЬ ИЗ СЕМПЛА КОЛОРВЕЙ» ОДНИМ ЖЕСТОМ.
//
// Верстак 0 — это и есть «семпл»: законный, вечный, со своим разделом выходов. До этой волны плиту
// семпла нельзя было положить в столбец колорвея N вовсе (`colorway_mismatch` в обе стороны), и
// единственным путём оставалась КОПИЯ — RegisterUpload того же медиа под N: дубликаты в outputs
// под общим потолком 60, «uploaded» в истории вместо прогона, слот без штампа ревизии, на который
// слеп сторож 3D «четыре стороны ОДНОЙ ревизии».
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там.

// ⚠ ЧТО ИМЕННО ЛОВИТ ЭТА ПРОБА — ДВА РАЗНЫХ ФАКТА, И ОДНОГО МАЛО.
//
// Первый: постановка прошла. Второй: КОЛОНКА КАДРА ИЗМЕНИЛАСЬ. Проба, спрашивающая только слот,
// осталась бы зелёной у реализации, которая пропускает постановку и колорвей не пишет, — а такая
// плита стоит в верстаке N, но лежит в разделе выходов 0 и в SIDES колорвея не появляется вовсе.
// Поэтому колонка читается ПРЯМО ИЗ БАЗЫ, а не из ответа: ответ собирает тот же код, что и пишет.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять adoptPictureIntoColorway и вернуть безусловный отказ — падает
// первая же строка; оставить отказ, но не писать колонку — падает чтение из raw и разделы полосы.
func TestDesignDBUnattributedPlateIsAdoptedByTheColorwayBench(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	pic := uploadRenderPlate(t, rep, raw, card, 0)

	// ДО ПОСТАНОВКИ ПЛИТА ЛЕЖИТ В РАЗДЕЛЕ СЕМПЛА — иначе замер ниже ничего не сдвигает.
	band, err := rep.Design().GetBand(context.Background(), card, 5)
	require.NoError(t, err)
	require.Equal(t, 1, band.OutputsTotalByColorway[0],
		"стенд обязан начинаться с неатрибутированной плиты, иначе проба измеряет пустоту")
	require.Zero(t, band.OutputsTotalByColorway[cw])

	slot, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
			ColorwayId: entity.DesignColorwayRef(cw),
		},
		PictureId: pic, Actor: "probe",
	})
	require.NoError(t, err,
		"плита семпла обязана вставать в столбец колорвея: 0 у кадра значит «не сказано»")
	require.Equal(t, cw, entity.DesignColorwayOrNone(slot.ColorwayId), "слот — колорвея N")
	require.Equal(t, int32(pic), slot.PictureId.Int32, "и он занят именно этой плитой")
	require.NotNil(t, slot.Picture)
	require.Equal(t, cw, entity.DesignColorwayOrNone(slot.Picture.ColorwayId),
		"ответ постановки везёт УЖЕ усыновлённую плиту, а не ту, что была прочитана до записи")

	// КОЛОНКА САМОГО КАДРА — прямым чтением, мимо всякой сборки ответа.
	var colorway sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT colorway_id FROM design_picture WHERE id = ?`, pic).
		Scan(&colorway))
	require.True(t, colorway.Valid, "усыновление обязано записать колорвей в саму плиту")
	require.Equal(t, int32(cw), colorway.Int32)

	// И ПОЛОСА ПЕРЕСЧИТЫВАЕТ ЭТО ЧТЕНИЕМ: раздел выходов ключуется колорвеем КАДРА
	// (designCardOutputsColorway), значит усыновлённая плита УХОДИТ из семпла и ПРИХОДИТ в N —
	// ровно то, ради чего жест и делается. А множество занятых render-верстаков открывает дверь
	// 3D колорвею N, потому что теперь у него занят слот.
	band, err = rep.Design().GetBand(context.Background(), card, 5)
	require.NoError(t, err)
	require.Equal(t, 1, band.OutputsTotalByColorway[cw],
		"усыновлённая плита считается в разделе своего нового колорвея")
	require.Zero(t, band.OutputsTotalByColorway[0],
		"и уходит из раздела семпла — иначе она была бы в двух разделах сразу")
	require.ElementsMatch(t, []int{cw}, band.RenderBenchColorways,
		"занятый верстак колорвея — дверь 3D, и она открывается тем же жестом")
}

// ГРАНИЦЫ УСЫНОВЛЕНИЯ: ОНО ОДНОНАПРАВЛЕННОЕ.
//
// Ноль у кадра значит «не сказано», и вписать в пробел ответ — не потеря. Колорвей N у кадра —
// сказанное, и постановка его не переписывает: ни на чужой (N→M), ни на пустоту (N→0). Текст
// отказа в обоих направлениях остался прежним — `colorway_mismatch`, тот же, что печатает клиент.
//
// ⚠ ВТОРАЯ ПОЛОВИНА ЗАМЕРА — ЧТО ОТКАЗ НИЧЕГО НЕ НАПИСАЛ. Проба, спрашивающая только код ошибки,
// осталась бы зелёной у реализации, которая усыновляет ВСЕГДА, а потом падает на CAS: плита уже
// сменила бы колорвей. Колонка читается после каждого отказа.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ (ЗАМЕРЕНО): снять `picCw == 0` из условия усыновления И предикат
// `colorway_id IS NULL` из его UPDATE'а — N→M становится успехом, плита уезжает в чужой колорвей.
// ⚠ СНЯТЬ ТОЛЬКО ОДНО ИЗ ДВУХ — ПРОБА ОСТАЁТСЯ ЗЕЛЁНОЙ, и это не дыра в ней, а второй пояс: без
// `picCw == 0` до записи доходит и постановка атрибутированной плиты, но предикат SQL находит ноль
// строк и отказ печатается тем же mismatch'ем. Названо вслух, потому что мутация, которую ловит
// ОДИН из двух сторожей, выглядит как непокрытая, пока не сказано, что её ловит другой.
func TestDesignDBAdoptionDoesNotRewriteAStatedColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cwA, cwB := probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
	pic := uploadRenderPlate(t, rep, raw, card, cwA)

	place := func(cw entity.DesignColorwayRef) error {
		_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
			TechCardId: card,
			Slot: entity.DesignSlotRef{
				ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindRender, ColorwayId: cw,
			},
			PictureId: pic, Actor: "probe",
		})
		return err
	}
	stillA := func(t *testing.T) {
		t.Helper()
		var colorway sql.NullInt32
		require.NoError(t, raw.QueryRow(`SELECT colorway_id FROM design_picture WHERE id = ?`, pic).
			Scan(&colorway))
		require.True(t, colorway.Valid)
		require.Equal(t, int32(cwA), colorway.Int32,
			"отказ обязан быть ещё и НЕ-записью: откатившаяся атрибуция хуже отказа")
	}

	// N → M: чужой колорвей.
	require.ErrorIs(t, place(entity.DesignColorwayRef(cwB)), entity.ErrDesignColorwayMismatch,
		"усыновление вписывает ответ в пробел, а не переписывает чужой")
	stillA(t)

	// N → 0: обнуление атрибуции. Названо сентинелом, потому что голый 0 у ЗАПРОСА значит
	// «не назвал» и подставил бы колорвей самой плиты, то есть проверял бы не то направление.
	require.ErrorIs(t, place(entity.DesignColorwayUnattributed), entity.ErrDesignColorwayMismatch,
		"безколорвейный верстак не стирает атрибуцию, которая у кадра есть")
	stillA(t)
}

// СОСТАВНАЯ ДВЕРЬ УСЫНОВЛЯЕТ ТЕМ ЖЕ СТОРОЖЕМ И ГОВОРИТ ОБ ЭТОМ ОБЕИМИ ПОЛОВИНАМИ ОТВЕТА.
//
// RegisterBatch ставит плиту ТЕМ ЖЕ setBenchSlotTx, поэтому усыновление достаётся ей даром. А вот
// СПИСОК КАДРОВ в её ответе прочитан ДО постановки — и без отдельной строки в pictures.go ответ
// расходился бы сам с собой: слот сказал бы «колорвей N», а кадр рядом — «колорвея нет». Клиент
// рисует пилюлю по кадру.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять синхронизацию pics[0].ColorwayId в RegisterBatch — слот останется
// правдив, а список кадров начнёт врать нулём.
func TestDesignDBUploadWithColorwayTargetAdoptsAndSaysSoTwice(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")

	// Кадр колорвея не называет — цель называет. Это ровно тот жест, которым лист артефактов
	// кладёт файл в строку колорвея: `+ media` в ячейке столбца N.
	out, err := rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: probeMedia(t, raw), Kind: entity.DesignPictureKindRender},
		},
		Target: &entity.DesignSlotRef{
			ViewKey: entity.DesignViewSideL, Kind: entity.DesignPictureKindRender,
			ColorwayId: entity.DesignColorwayRef(cw),
		},
	})
	require.NoError(t, err)
	require.NotNil(t, out.Slot)
	require.Equal(t, cw, entity.DesignColorwayOrNone(out.Slot.ColorwayId))
	require.Len(t, out.Pictures, 1)
	require.Equal(t, cw, entity.DesignColorwayOrNone(out.Pictures[0].ColorwayId),
		"обе половины одного ответа обязаны называть один колорвей")

	var colorway sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT colorway_id FROM design_picture WHERE id = ?`,
		out.Pictures[0].Id).Scan(&colorway))
	require.Equal(t, int32(cw), colorway.Int32)
}

// ФЛЭТ УСЫНОВЛЕНИЕМ НЕ ПРОНИКАЕТ НА ОСЬ. Чертёж изделия один на все цвета (L-4), и род без оси
// колорвея по-прежнему отказывает `colorway_forbidden` РАНЬШЕ, чем дело доходит до плиты: новое
// условие стоит за этим сторожем, а не вместо него.
func TestDesignDBAdoptionDoesNotOpenTheFlatBenchToColorways(t *testing.T) {
	rep, raw := probeRepository(t)
	card, flatPic, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")

	_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindFlat,
			ColorwayId: entity.DesignColorwayRef(cw),
		},
		PictureId: flatPic, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"усыновление — про пробел в атрибуции, а не про появление оси там, где её нет")
}
