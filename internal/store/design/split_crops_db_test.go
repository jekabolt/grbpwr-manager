package design_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ РАЗРЕЗ СЧИТАЕТ ТОЛЬКО КУСКИ ЛИСТА — O-53 follow-up ════════════════════════════════════════
//
// ДЕФЕКТ. Короткое замыкание SplitPicture («у листа уже есть видимые кропы — вернуть их») читало
// derived_from БЕЗ ГЛАГОЛА. Правка листа — save as new или overwrite (0369) — тоже видимый ребёнок,
// поэтому после первой же правки разрез отвечал «уже нарезано» самой правкой и не резал ничего; а
// ответ свежего разреза тащил в «кропы» правки и спрятанные старые куски.
//
// Предикат — designSheetCropsOf: глагол crop или легаси-пустой, видимый; заменённый кусок считается.
// Форма предиката проверяется без базы (split_crops_test.go); здесь — строки.
//
// ЗАМЕНЁННЫЙ ЛИСТ НЕ РЕЖЕТСЯ ВОВСЕ (O-53 review): его место занято правкой, и разрез резал бы
// пиксели, которых на экране больше нет. Отказ — already_replaced с головой цепочки. СПРЯТАННЫЙ — тоже
// (O-53 review, раунд 3): hidden_picture.
//
// Запуск — тот же одноразовый контейнер, что и у соседних проб (см. шапку wave2_db_test.go); без
// CI=1 каждая проба пропускается ДО открытия соединения.

// splitProbe режет лист одним кадром со свежим медиа — жест «split» с экрана.
func splitProbe(t *testing.T, rep dependency.Repository, raw *sql.DB, sheetID int, view string) []entity.DesignPicture {
	t.Helper()
	crops, err := rep.Design().SplitPicture(context.Background(), entity.DesignSplitRequest{
		PictureId: sheetID, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: view}},
	})
	require.NoError(t, err)
	return crops
}

// editProbe файлит правку кадра: слой поверх его файла и флэттен — рядом (original = 0) или на
// его месте.
func editProbe(t *testing.T, rep dependency.Repository, raw *sql.DB, pic entity.DesignPicture, original int) *entity.DesignPicture {
	t.Helper()
	ctx := context.Background()
	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: pic.TechCardId, BaseMediaId: pic.MediaId, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	edit, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: pic.TechCardId, LayerId: layer.Id, ExpectedRev: layer.Rev,
		MediaId: probeMedia(t, raw), ReplacePictureId: original, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignDerivationFlatten, edit.Derivation)
	return edit
}

func childrenOf(t *testing.T, raw *sql.DB, sheetID int) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE derived_from = ?`, sheetID)
}

// ПРАВКА ЛИСТА «РЯДОМ» НЕ ЗАКРЫВАЕТ ЕГО РАЗРЕЗ.
//
// МУТАЦИЯ: вернуть короткому замыканию голое `derived_from = :id AND hidden_at IS NULL` — разрез
// отвечает правкой и не пишет ни одного куска.
//
// Правка «на месте» здесь больше не случай: лист, чьё место заняла правка, не режется вовсе — см.
// TestDesignDBSplitOfAReplacedSheetIsRefusedWithTheHead. Здесь этот случай прежде благословлял
// разрез заменённого листа (O-53 review).
func TestDesignDBSplitIsNotBlockedByAnEditOfTheSheet(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	edit := editProbe(t, rep, raw, sheet, 0)

	crops := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, crops, 1, "правка листа — не кусок: разрез обязан состояться")
	require.Equal(t, entity.DesignDerivationCrop, crops[0].Derivation)
	require.EqualValues(t, sheet.Id, crops[0].DerivedFrom.Int32)
	require.NotEqual(t, edit.Id, crops[0].Id, "правка не выдаётся за кусок")
	require.Equal(t, 2, childrenOf(t, raw, sheet.Id), "правка и один свежий кусок")
}

// ЗАМЕНЁННЫЙ ЛИСТ НЕ РЕЖЕТСЯ — ОТКАЗ НЕСЁТ ГОЛОВУ, И НЕ ПОДАНО НИЧЕГО (O-53 review).
//
// Сценарий ревью: лист перезаписан правкой (слот уехал на правку, лист подписан), и устаревшая
// вкладка режет лист. Раньше разрез проходил — предикат кусков справедливо не видит правку — и
// колода нарезалась из пикселей, которых на экране больше нет. Отказ стоит ДО короткого замыкания,
// поэтому заменённый лист с видимыми старыми кусками отказывается тоже. Голова цепочки идёт вперёд
// вместе с перезаписями; её саму резать можно.
//
// МУТАЦИИ: не проверять replaced_by (разрез проходит, у листа второй ребёнок); проверять после
// короткого замыкания (последняя половина отвечает старыми кусками вместо отказа); класть в отказ
// первую замену вместо головы.
func TestDesignDBSplitOfAReplacedSheetIsRefusedWithTheHead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	edit := editProbe(t, rep, raw, sheet, sheet.Id)

	split := func(pictureID int) ([]entity.DesignPicture, error) {
		return rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
			PictureId: pictureID, ClientRequestId: uuid.NewString(), Actor: "probe",
			Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewFront}},
		})
	}
	_, err := split(sheet.Id)
	requireHead(t, err, sheet.Id, edit.Id)
	require.Equal(t, 1, childrenOf(t, raw, sheet.Id), "у листа только правка — ни одного куска")

	edit2 := editProbe(t, rep, raw, *edit, edit.Id)
	_, err = split(sheet.Id)
	requireHead(t, err, sheet.Id, edit2.Id)

	crops, err := split(edit2.Id)
	require.NoError(t, err, "голову резать можно")
	require.Len(t, crops, 1)
	require.EqualValues(t, edit2.Id, crops[0].DerivedFrom.Int32)
}

// СПРЯТАННЫЙ КАДР НЕ РЕЖЕТСЯ — И НЕ ПОДАНО НИЧЕГО (O-53 review, раунд 3).
//
// Разрез спрятанного кадра вешал живые куски под родителя, которого не видно, и был дверью сценария
// Codex: устаревшая вкладка резала спрятанную правку куска, и сторож перезаписи листа не видел
// рождённый кусок (см. TestDesignDBOverwriteOfASheetIsHeldByACropOfAHiddenHead). Отказ —
// hidden_picture, в транзакции и ДО короткого замыкания: спрятанный кадр, чей старый кусок снова на
// виду, отказывается тоже, а не отвечает этим куском. Такое состояние собирается законными жестами —
// спрятать кусок, спрятать кадр, вернуть кусок (показ не сторожится).
//
// МУТАЦИИ: не проверять hidden_at (у спрятанного кадра появляется кусок); проверять после короткого
// замыкания (вторая половина отвечает старым куском вместо отказа); отдать hidden_plate.
func TestDesignDBSplitOfAHiddenPictureIsRefused(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	split := func(t *testing.T, pictureID int) ([]entity.DesignPicture, error) {
		t.Helper()
		return rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
			PictureId: pictureID, ClientRequestId: uuid.NewString(), Actor: "stale-tab",
			Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewFront}},
		})
	}
	hide := func(t *testing.T, id int, hidden bool) {
		t.Helper()
		_, err := rep.Design().HidePicture(ctx, id, hidden, "colleague")
		require.NoError(t, err)
	}
	pictures := func(t *testing.T) int {
		t.Helper()
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, card)
	}

	t.Run("спрятанный кадр", func(t *testing.T) {
		sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
		hide(t, sheet.Id, true)
		before := pictures(t)
		_, err := split(t, sheet.Id)
		require.ErrorIs(t, err, entity.ErrDesignHiddenPicture)
		require.NotErrorIs(t, err, entity.ErrDesignHiddenPlate)
		require.Equal(t, before, pictures(t), "отказ не подаёт ни одного куска")
		require.Zero(t, childrenOf(t, raw, sheet.Id))
	})
	t.Run("спрятанный кадр со старым куском на виду — отказ, а не старый кусок", func(t *testing.T) {
		sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
		old := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
		require.Len(t, old, 1)
		hide(t, old[0].Id, true)
		hide(t, sheet.Id, true)
		hide(t, old[0].Id, false)

		_, err := split(t, sheet.Id)
		require.ErrorIs(t, err, entity.ErrDesignHiddenPicture)
		require.Equal(t, 1, childrenOf(t, raw, sheet.Id), "второго комплекта нет")

		// Положительный контроль: показанный кадр отвечает своим куском — отказ был про видимость.
		hide(t, sheet.Id, false)
		again, err := split(t, sheet.Id)
		require.NoError(t, err)
		require.Len(t, again, 1)
		require.Equal(t, old[0].Id, again[0].Id)
	})
}

// ЗАМЕНЁННЫЙ ЛИСТ СО СТАРЫМИ ВИДИМЫМИ КУСКАМИ — ТОЖЕ ОТКАЗ, А НЕ СТАРЫЕ КУСКИ.
//
// Перезапись разрезанного листа сторож cut_sheet не пустил бы, поэтому состояние собирается мимо
// стора: лист подписан вручную. Так выглядит лист, чьи куски были спрятаны перед перезаписью и потом
// возвращены, — и устаревшей вкладке нужен ответ «режь голову», а не куски кадра, на который она
// смотреть не должна.
func TestDesignDBSplitOfAReplacedSheetIgnoresItsOldCrops(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	old := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, old, 1)
	head := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	_, err := raw.Exec(`UPDATE design_picture SET replaced_by = ? WHERE id = ?`, head.Id, sheet.Id)
	require.NoError(t, err)

	_, err = rep.Design().SplitPicture(context.Background(), entity.DesignSplitRequest{
		PictureId: sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewFront}},
	})
	requireHead(t, err, sheet.Id, head.Id)
}

// ОТВЕТ СВЕЖЕГО РАЗРЕЗА — ТОЛЬКО ЕГО КУСКИ.
//
// Лист с правкой рядом и со спрятанным старым куском: ответ повторного разреза обязан состоять из
// одного нового куска. Раньше хвост читал голое `derived_from = :id` и отдавал все три.
func TestDesignDBSplitAnswersWithTheCutAlone(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	edit := editProbe(t, rep, raw, sheet, 0)

	first := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, first, 1)
	_, err := rep.Design().HidePicture(ctx, first[0].Id, true, "probe")
	require.NoError(t, err, "плохой кусок прячут, чтобы резать снова")

	second := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewBack)
	require.Len(t, second, 1, "в ответе нет ни правки, ни спрятанного куска")
	require.NotEqual(t, first[0].Id, second[0].Id)
	require.NotEqual(t, edit.Id, second[0].Id)
	require.Equal(t, entity.DesignViewBack, second[0].GhostView.String)
}

// ПОВТОР РАЗРЕЗА ОТДАЁТ ТЕ ЖЕ КУСКИ, И ЗАМЕНЁННЫЙ КУСОК — ТОЖЕ КУСОК.
//
// Кусок, перезаписанный правкой (0369), стоит в ленте своей правкой, и лист от этого не становится
// ненарезанным. МУТАЦИЯ: добавить в предикат `replaced_by IS NULL` — лист, у которого перезаписан
// каждый кусок, режется вторым комплектом.
func TestDesignDBSplitCountsAReplacedPieceAsCut(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	cut := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, cut, 1)

	again := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, again, 1)
	require.Equal(t, cut[0].Id, again[0].Id, "повтор отдаёт тот же кусок и не режет")

	editProbe(t, rep, raw, cut[0], cut[0].Id)
	afterEdit := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, afterEdit, 1)
	require.Equal(t, cut[0].Id, afterEdit[0].Id, "заменённый кусок по-прежнему кусок листа")
	require.True(t, afterEdit[0].ReplacedBy.Valid, "голову клиент находит по replaced_by")
	require.Equal(t, 1, childrenOf(t, raw, sheet.Id), "второго комплекта нет")
}

// ЛЕГАСИ С ПУСТЫМ ГЛАГОЛОМ СЧИТАЕТСЯ КУСКОМ — как до этой правки.
//
// Строка заводится напрямую, минуя стор: через стор пустой глагол при непустом родителе уже
// невыразим. МУТАЦИЯ: выбросить легаси из IN — лист режется вторым комплектом поверх
// неклассифицированного куска.
func TestDesignDBSplitCountsALegacyChildAsCut(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	res, err := raw.Exec(`INSERT INTO design_picture
		(tech_card_id, media_id, batch_id, ordinal, kind, derived_from, derivation, source_class, layer_rev)
		VALUES (?, ?, ?, 99, 'flat', ?, '', 'ai', 1)`,
		card, probeMedia(t, raw), sheet.BatchId.Int32, sheet.Id)
	require.NoError(t, err)
	legacy, err := res.LastInsertId()
	require.NoError(t, err)

	crops := splitProbe(t, rep, raw, sheet.Id, entity.DesignViewFront)
	require.Len(t, crops, 1)
	require.EqualValues(t, legacy, crops[0].Id, "неклассифицированный ребёнок держит лист, как держал")
	require.Equal(t, 1, childrenOf(t, raw, sheet.Id))
}
