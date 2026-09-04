package design_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ЦВЕТОВОГО ПЛАНА (0364). Обвязка общая с wave2_db_test.go — probeRepository /
// probeCard / probeMedia; без CI=1 всё пропускается ДО открытия соединения, а имя базы, не похожее
// на пробное, отвергается отдельно. Запуск описан в шапке того файла.
//
// ⚠ ПОЧЕМУ ЭТО ВООБЩЕ ЖИВАЯ ПРОБА, А НЕ ТАБЛИЦА НАД ЧИСТОЙ ФУНКЦИЕЙ. Проверяемое здесь свойство —
// compare-and-set — существует ТОЛЬКО в базе: оно живёт в предикате `WHERE rev = :expected` и в
// числе затронутых строк. Замена его моком означала бы пробу, которая красит собственную заглушку;
// именно так CAS однажды и разошёлся с тем, что делает SQL.

// designProbePlanCard — карточка, её флэт и покрашенная карта. Обе картинки заводятся настоящими
// строками media: граница карточки читает реестр ссылок, и «ничейный свежий файл» — это
// утверждение о СТРОКЕ, а не о числе.
func designProbePlanCard(t *testing.T) (cardID, baseMedia, mapMedia int) {
	t.Helper()
	_, raw := probeRepository(t)
	return probeCard(t, raw), probeMedia(t, raw), probeMedia(t, raw)
}

// probePlanOf читает план ТЕМ ЖЕ путём, каким его читает экран, — одним чтением полосы. Отдельного
// глагола-читателя у плана нет намеренно (см. шапку colour_plan.go), и проба, завёдшая себе свой,
// проверяла бы не тот путь, который поедет на бету.
func probePlanOf(t *testing.T, cardID int) *entity.DesignColourPlan {
	t.Helper()
	rep, _ := probeRepository(t)
	band, err := rep.Design().GetBand(context.Background(), cardID, 1)
	require.NoError(t, err)
	return band.ColourPlan
}

func probePlanSave(card, base, mapMedia int, rev int, cloths []entity.DesignColourCloth) entity.DesignColourPlanSave {
	return entity.DesignColourPlanSave{
		TechCardId:  card,
		ExpectedRev: rev,
		Maps: []entity.DesignColourMap{{
			MediaId: mapMedia, View: entity.DesignViewFront, BaseMediaId: base,
			Palette: []entity.DesignColourSwatch{{Hex: "#3a7bd5", Px: 40000}, {Hex: "#ff0000", Px: 900}},
		}},
		Cloths: cloths,
		Actor:  "probe",
	}
}

// TestDesignDBColourPlanCAS — ЖИЗНЬ ОДНОГО ПЛАНА, ПРОЧИТАННАЯ ПО РЕВИЗИЯМ.
//
// Таблица случаев, и порядок в ней несущий: каждый шаг оставляет строку в состоянии, на котором
// стоит следующий. Проверяется ровно то, что покупает CAS, — что двадцать минут покраски нельзя
// потерять молча.
func TestDesignDBColourPlanCAS(t *testing.T) {
	rep, _ := probeRepository(t)
	ctx := context.Background()
	card, base, mapMedia := designProbePlanCard(t)

	// НЕТ ПЛАНА — nil, А НЕ ПУСТЫШКА. Пустой план это «покрасили и стёрли», состояние, сделанное
	// руками; подменив одно другим, полоса сообщила бы rev 0 у несуществующей строки.
	require.Nil(t, probePlanOf(t, card),
		"у карточки без покраски плана нет, и это ответ, а не пустой документ")

	// РОЖДЕНИЕ ТРЕБУЕТ rev 0. Клиент, echo'нувший чужую ревизию, смотрит на план, которого здесь
	// нет: принять это значило бы исполнить просьбу, которой никто не делал.
	_, err := rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 3, nil))
	require.Error(t, err)
	require.True(t, errors.Is(err, entity.ErrDesignColourPlanRevMismatch),
		"рождение под чужой ревизией — это CAS-отказ, а не тихая вставка")

	// РОЖДЕНИЕ. Первая ревизия — 1: rev 0 значит «плана нет», и вернуть 0 у существующей строки
	// означало бы два разных состояния под одним числом.
	plan, err := rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 0,
		[]entity.DesignColourCloth{{Hex: "#3a7bd5", Words: "main jersey"}}))
	require.NoError(t, err)
	require.Equal(t, 1, plan.Rev)
	require.Len(t, plan.Maps, 1)
	require.Equal(t, entity.DesignViewFront, plan.Maps[0].View)
	require.Len(t, plan.Maps[0].Palette, 2, "палитра доезжает целиком: это замкнутое множество ярлыков")
	require.Len(t, plan.Cloths, 1)
	require.Equal(t, "probe", plan.UpdatedBy)

	// ВТОРОЕ РОЖДЕНИЕ ПОД ТЕМ ЖЕ rev 0 — ОТКАЗ. Это ровно случай «пока мы шли, план завёл сосед»:
	// вызывающий полагал, что рождает план, поэтому честный ответ — перечитать и продолжить чужой.
	_, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 0, nil))
	require.True(t, errors.Is(err, entity.ErrDesignColourPlanRevMismatch),
		"второе рождение — не дубликат, который можно проглотить")

	// УСТАРЕВШИЙ ПИСАТЕЛЬ ОТКАЗАН, И НИЧЕГО НЕ ЗАПИСАНО. Это и есть та работа, которую CAS
	// защищает: у соседа на экране r1, у нас уже r1 → его следующее сохранение под r0 не должно
	// стереть наше.
	_, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 0,
		[]entity.DesignColourCloth{{Hex: "#ff0000", Words: "СТЁРТО БЫ"}}))
	require.True(t, errors.Is(err, entity.ErrDesignColourPlanRevMismatch))

	after := probePlanOf(t, card)
	require.NotNil(t, after)
	require.Equal(t, 1, after.Rev, "отказанный писатель не двигает ревизию")
	require.Len(t, after.Cloths, 1)
	require.Equal(t, "main jersey", after.Cloths[0].Words,
		"⚠ ЭТО ГЛАВНОЕ УТВЕРЖДЕНИЕ ФАЙЛА: отказ обязан оставить документ ровно таким, каким он был")

	// СОШЁЛСЯ — ЗАПИСАЛОСЬ, И ДОКУМЕНТ ЗАМЕНИЛСЯ ЦЕЛИКОМ. Замена целиком — это то, чем перекраска
	// вида убирает цвет ВМЕСТЕ с его назначением; патч оставил бы половину плана утверждать про
	// цвет, которого больше нет.
	plan, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 1,
		[]entity.DesignColourCloth{{Hex: "#ff0000", Words: "contrast rib"}}))
	require.NoError(t, err)
	require.Equal(t, 2, plan.Rev)
	require.Len(t, plan.Cloths, 1)
	require.Equal(t, "contrast rib", plan.Cloths[0].Words,
		"сохранение заменяет документ, а не дописывает в него")

	// ПУСТОЙ ПЛАН ЗАКОНЕН: «покрасили и стёрли» — состояние, которое человек сделал руками.
	plan, err = rep.Design().SetColourPlan(ctx, entity.DesignColourPlanSave{
		TechCardId: card, ExpectedRev: 2, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, 3, plan.Rev)
	require.Empty(t, plan.Maps)
	require.Empty(t, plan.Cloths)

	// ⚠ ОЧИСТКА — ЭТО ТА ЖЕ ЗАПИСЬ, И ВТОРОГО ГЛАГОЛА ДЛЯ НЕЁ НЕТ. Здесь стоял DeleteColourPlan:
	// он нёс ОДИН tech_card_id, ревизию не сверял и упасть не мог — то есть устаревшая вкладка
	// сносила чужую покраску молча. Вот тот самый жест, и теперь он ОТКАЗЫВАЕТ.
	_, err = rep.Design().SetColourPlan(ctx, entity.DesignColourPlanSave{
		TechCardId: card, ExpectedRev: 2, Actor: "устаревшая вкладка",
	})
	require.True(t, errors.Is(err, entity.ErrDesignColourPlanRevMismatch),
		"⚠ «очистить» с устаревшей ревизией не имеет права снести чужой план")
	require.Len(t, probePlanOf(t, card).Cloths, 0, "положительный контроль: план всё ещё пуст на rev 3")

	// И ЛЕСТНИЦА РЕВИЗИЙ ПЕРЕЖИВАЕТ ОЧИСТКУ, в отличие от удаления, которое сбрасывало её в ноль:
	// покрасить снова — это следующий rev, а не новая строка с нулевой историей.
	plan, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 3, nil))
	require.NoError(t, err)
	require.Equal(t, 4, plan.Rev)
}

// TestDesignDBColourPlanRefusesWhatTheDoorCannotSee — три границы, КАЖДАЯ В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО
// И ЗАПИСЬ, и каждая из них раньше отвечала не тем.
//
//   - НЕИЗВЕСТНАЯ КАРТОЧКА: существование не спрашивалось вовсе, INSERT ловил 1452 от
//     fk_design_colour_plan_card, а маппится в этом сторе только 1062 — значит ожидаемый отказ
//     уезжал клиенту как Internal 500 и писал ERROR в лог дежурному;
//   - НЕСУЩЕСТВУЮЩЕЕ МЕДИА: у JSON-колонок внешнего ключа нет ВОВСЕ, поэтому план мог навсегда
//     назвать картинку, которой не было, а дверь прогона заморозила бы этот номер в `params`;
//   - ЧУЖАЯ ПОЛКА: проверялась в обработчике ОТДЕЛЬНЫМ чтением полосы до открытия транзакции —
//     единственная граница фичи, стоявшая не там, где пишут.
func TestDesignDBColourPlanRefusesWhatTheDoorCannotSee(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, base, mapMedia := designProbePlanCard(t)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПЕРВЫМ: законный план проходит, иначе всё ниже зеленело бы у правила
	// «отказывать всему».
	_, err := rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 0, nil))
	require.NoError(t, err)

	// НЕИЗВЕСТНАЯ КАРТОЧКА — NotFound, а не «сломался сервер».
	_, err = rep.Design().SetColourPlan(ctx, probePlanSave(card+1_000_000, base, mapMedia, 0, nil))
	require.True(t, errors.Is(err, entity.ErrDesignNotFound),
		"неизвестная карточка — ОЖИДАЕМЫЙ отказ, а не 1452 в логе дежурного")

	// НЕСУЩЕСТВУЮЩЕЕ МЕДИА — отказ называет id, который прислали.
	var maxMedia int
	require.NoError(t, raw.QueryRow(`SELECT COALESCE(MAX(id), 0) FROM media`).Scan(&maxMedia))
	_, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, maxMedia+1_000, 1, nil))
	require.True(t, errors.Is(err, entity.ErrDesignInvalidArgument),
		"план не имеет права навсегда назвать картинку, которой не было")

	// ЧУЖАЯ ПОЛКА — та же граница, теперь внутри транзакции записи.
	otherCard, _, _ := designProbeCard(t, rep, raw)
	require.NotEqual(t, card, otherCard)
	otherAsset, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: otherCard, Kind: entity.DesignAssetKindFabric, Name: "чужой джерси", Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 1,
		[]entity.DesignColourCloth{{Hex: "#3a7bd5", AssetId: otherAsset.Id}}))
	require.True(t, errors.Is(err, entity.ErrDesignInvalidArgument),
		"полка чужой карточки не встаёт в план")

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: СВОЯ полка проходит той же дорогой.
	mineAsset, err := rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, Kind: entity.DesignAssetKindFabric, Name: "свой джерси", Actor: "probe",
	})
	require.NoError(t, err)
	plan, err := rep.Design().SetColourPlan(ctx, probePlanSave(card, base, mapMedia, 1,
		[]entity.DesignColourCloth{{Hex: "#3a7bd5", AssetId: mineAsset.Id}}))
	require.NoError(t, err)
	require.Equal(t, 2, plan.Rev)
}

// TestDesignDBColourPlanRefusesAForeignPicture — ГРАНИЦА КАРТОЧКИ, ПРОВЕРЕННАЯ В ТОЙ ЖЕ
// ТРАНЗАКЦИИ, ЧТО И ЗАПИСЬ.
//
// Карта уезжает ПОСТАВЩИКУ как вход прогона и рисуется клиентом на экране, поэтому непроверенное
// поле означает, что план карточки A показывает и отправляет картинку карточки B. Правило
// ОТРИЦАТЕЛЬНОЕ: свежезагруженный ничейный PNG — обычный случай — проходит.
func TestDesignDBColourPlanRefusesAForeignPicture(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	mine, base, mapMedia := designProbePlanCard(t)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ПЕРВЫМ: ничейные файлы проходят. Без него отказ ниже зеленел бы и у
	// правила «отвергать всё», которое закрыло бы фичу целиком.
	_, err := rep.Design().SetColourPlan(ctx, probePlanSave(mine, base, mapMedia, 0, nil))
	require.NoError(t, err, "ничейный свежий файл — обычный случай, и он обязан проходить")

	// А теперь тот же файл, подшитый ЧУЖОЙ карточке.
	otherCard, otherPicA, _ := designProbeCard(t, rep, raw)
	require.NotEqual(t, mine, otherCard)
	var otherMedia int
	require.NoError(t, raw.QueryRow(
		`SELECT media_id FROM design_picture WHERE id = ?`, otherPicA).Scan(&otherMedia))

	_, err = rep.Design().SetColourPlan(ctx, entity.DesignColourPlanSave{
		TechCardId: mine, ExpectedRev: 1, Actor: "probe",
		Maps: []entity.DesignColourMap{{
			MediaId: otherMedia, View: entity.DesignViewFront, BaseMediaId: base,
		}},
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, entity.ErrDesignForeignMedia),
		"картинка чужой карточки не встаёт в план и не уезжает поставщику")
}
