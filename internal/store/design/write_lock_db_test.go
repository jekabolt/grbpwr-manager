package design_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// ═══ ЗАПИСЬ НЕ ЖДЁТ ЧУЖИХ КАДРОВ КАРТОЧКИ — №244 ═══════════════════════════════════════════════
//
// resolveMedia размечает углы undo/redo (annotateEditChains, T28 v2 ece15de) по цепочкам ВСЕЙ
// карточки — сканом idx_design_picture_card, как только хоть один кадр ответа звено цепочки (правка
// или заменённый кадр). Под SERIALIZABLE этот скан ставит S на каждый кадр карточки, и запись,
// собиравшая ответ внутри своей транзакции, ждала любую чужую транзакцию, державшую любой кадр той же
// карточки (и сама держала их S до коммита). Перезапись это уже прошла (c169b2c,
// TestDesignDBOverwriteDoesNotLockTheRestOfTheCard); здесь остальные двери записи: постановка в слот
// (и её отказ), сокрытие, возврат, выбор, повтор разреза, повтор пачки и пометка детали. Ответ теперь
// собирается после коммита, в снимке чтения (resolveMediaAfterCommit / resolveSlotMediaAfterCommit).
//
// UNDO/REDO ЗДЕСЬ НЕТ, И ЭТО НЕ ПРОПУСК: шаг цепочки берёт звенья цепочек карточки FOR UPDATE сканом
// той же idx_design_picture_card намеренно (две вкладки идут друг за другом, locks.go), так что кадры
// карточки он запирает и без resolveMedia. Его ответ тоже собирается после коммита — это проверяет
// TestDesignDBUndoAnswerIsResolvedAfterCommit на содержимом, а не на замках.
//
// ЧЕМ ДОКАЗАНО — тем же следом замков, что у перезаписи: коллега держит под X двадцать четыре
// посторонних корня той же карточки, и запись, чей ответ несёт звено цепочки, обязана пройти, не
// дождавшись его.
//
// МУТАЦИЯ: вернуть resolveMedia / attachSlotPictures внутрь транзакции записи — проба упирается в
// замки коллеги и не укладывается в десять секунд ("failed to read design edit chains: context
// deadline exceeded"). Статически то же сторожит scripts/check-design-lock-order.sh.

// holdForeignRoots заводит n посторонних корней карточки и держит их под X в транзакции коллеги до
// конца теста. По строке на UPDATE точным равенством по PRIMARY — почему не одним IN, сказано в
// TestDesignDBOverwriteDoesNotLockTheRestOfTheCard.
func holdForeignRoots(t *testing.T, rep dependency.Repository, raw *sql.DB, card, n int) {
	t.Helper()
	ctx := context.Background()
	roots := make([]int, 0, n)
	for i := 0; i < n; i++ {
		roots = append(roots, probePicture(t, rep, raw, card, entity.DesignPictureKindFlat).Id)
	}
	colleague, err := raw.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = colleague.Rollback() })
	for _, id := range roots {
		_, err = colleague.ExecContext(ctx, `UPDATE design_picture SET selected = 1 WHERE id = ?`, id)
		require.NoError(t, err)
	}
}

// noWait — срок, за который запись обязана пройти мимо замков коллеги. innodb_lock_wait_timeout по
// умолчанию — пятьдесят секунд, так что ожидание замка его не уложит.
func noWait(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ПОСТАНОВКА ПРАВКИ В СЛОТ — И ОТКАЗ ПОСТАНОВКИ — НЕ ЖДУТ ЧУЖИХ КАДРОВ КАРТОЧКИ.
//
// Плита — правка B, перезаписавшая A: звено цепочки, её углы читаются по всей карточке. Слот сначала
// снят, затем B ставится обратно; ответ обязан нести и файл, и углы (undo к A), собранные уже после
// коммита. Отказ по устаревшей ревизии отдаёт текущее состояние слота с плитой — тоже без ожидания.
func TestDesignDBBenchPutDoesNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	c := newChainProbe(t, rep, raw)
	_, rev, _ := probeSlotHolder(t, raw, c.slot.Id)
	unmarked, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: c.card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: 0, ExpectedSlotRev: rev, Actor: "probe",
	})
	require.NoError(t, err)
	holdForeignRoots(t, rep, raw, c.card, 24)

	started := time.Now()
	slot, err := rep.Design().SetBenchSlot(noWait(t), entity.DesignBenchSlotSet{
		TechCardId: c.card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: c.edit.Id, ExpectedSlotRev: unmarked.SlotRev, Actor: "probe",
	})
	require.NoError(t, err, "постановка не ждёт кадров вне своей плиты (ждала %v)", time.Since(started))
	holder, _, _ := probeSlotHolder(t, raw, c.slot.Id)
	require.EqualValues(t, c.edit.Id, holder.Int32)
	require.NotNil(t, slot.Picture)
	require.Equal(t, c.edit.Id, slot.Picture.Id)
	require.NotNil(t, slot.Picture.Media, "файл плиты собран после коммита")
	require.True(t, slot.Picture.CanUndo, "углы плиты собраны после коммита")
	require.Equal(t, c.sheet.Id, slot.Picture.UndoToId)

	started = time.Now()
	stale, err := rep.Design().SetBenchSlot(noWait(t), entity.DesignBenchSlotSet{
		TechCardId: c.card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: 0, ExpectedSlotRev: unmarked.SlotRev, Actor: "stale tab",
	})
	require.ErrorIs(t, err, entity.ErrDesignSlotRevMismatch, "отказ не ждёт кадров карточки (ждал %v)", time.Since(started))
	require.NotNil(t, stale, "отказ несёт текущее состояние слота")
	require.Equal(t, slot.SlotRev, stale.SlotRev)
	require.NotNil(t, stale.Picture)
	require.Equal(t, c.edit.Id, stale.Picture.Id)
	require.NotNil(t, stale.Picture.Media)
	require.True(t, stale.Picture.CanUndo)
}

// СОКРЫТИЕ, ВОЗВРАТ И ВЫБОР ПРАВКИ НЕ ЖДУТ ЧУЖИХ КАДРОВ КАРТОЧКИ.
//
// Кадр — правка «рядом» (save as new): флэттен, то есть звено цепочки, вне слота и без кусков, так что
// все три глагола законны. Каждый ответ несёт файл кадра, собранный после коммита, и то, что глагол
// записал. Правка — слоем самой обвязки (второй слой над тем же файлом — layer_rev_mismatch).
func TestDesignDBHideAndSelectDoNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	p := newReplaceProbeSetup(t, rep, raw)
	edit, err := rep.Design().FlattenEditLayer(context.Background(), p.overwrite(probeMedia(t, raw), 0))
	require.NoError(t, err)
	require.Equal(t, entity.DesignDerivationFlatten, edit.Derivation, "правка рядом — флэттен, звено цепочки")
	require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "рядом, а не на месте")
	holdForeignRoots(t, rep, raw, p.card, 24)

	started := time.Now()
	hidden, err := rep.Design().HidePicture(noWait(t), edit.Id, true, "probe")
	require.NoError(t, err, "сокрытие не ждёт кадров карточки (ждало %v)", time.Since(started))
	require.True(t, hidden.HiddenAt.Valid)
	require.NotNil(t, hidden.Media, "файл кадра собран после коммита")

	started = time.Now()
	shown, err := rep.Design().HidePicture(noWait(t), edit.Id, false, "probe")
	require.NoError(t, err, "возврат не ждёт кадров карточки (ждал %v)", time.Since(started))
	require.False(t, shown.HiddenAt.Valid)
	require.NotNil(t, shown.Media)

	started = time.Now()
	chosen, err := rep.Design().SetPictureSelected(noWait(t), edit.Id, true, "probe")
	require.NoError(t, err, "выбор не ждёт кадров карточки (ждал %v)", time.Since(started))
	require.True(t, chosen.Selected)
	require.NotNil(t, chosen.Media)
}

// ПОВТОР РАЗРЕЗА, ЧЕЙ КУСОК УЖЕ ПЕРЕЗАПИСАН, НЕ ЖДЁТ ЧУЖИХ КАДРОВ КАРТОЧКИ.
//
// Лист S разрезан на C, C перезаписан правкой E (C — заменённый кадр, звено цепочки). Повтор разреза
// отдаёт стоящий кусок C (короткое замыкание designSheetCropsOf), и его углы читаются по всей
// карточке — после коммита.
func TestDesignDBSplitReplayDoesNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	p := newReplaceProbeSetup(t, rep, raw)
	cut := splitProbe(t, rep, raw, p.sheet.Id, entity.DesignViewBack)
	require.Len(t, cut, 1)
	e := editProbe(t, rep, raw, cut[0], cut[0].Id)
	require.EqualValues(t, e.Id, probeReplacedBy(t, raw, cut[0].Id).Int32)
	media := probeMedia(t, raw)
	holdForeignRoots(t, rep, raw, p.card, 24)

	started := time.Now()
	again, err := rep.Design().SplitPicture(noWait(t), entity.DesignSplitRequest{
		PictureId: p.sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: media, ViewKey: entity.DesignViewBack}},
	})
	require.NoError(t, err, "повтор разреза не ждёт кадров карточки (ждал %v)", time.Since(started))
	require.Len(t, again, 1, "повтор отдаёт стоящий кусок, а не режет заново")
	require.Equal(t, cut[0].Id, again[0].Id)
	require.NotNil(t, again[0].Media, "файл куска собран после коммита")
}

// ПОВТОР ПАЧКИ, ЧЕЙ КАДР С ТЕХ ПОР ПЕРЕЗАПИСАН, НЕ ЖДЁТ ЧУЖИХ КАДРОВ КАРТОЧКИ.
//
// Загрузка P, затем перезапись P правкой E: P заменён, E — флэттен под той же пачкой, оба звенья
// цепочки. Слепой повтор той же пачки (сетевой таймаут) отдаёт её кадры, и их углы читаются по всей
// карточке — после коммита. Ответ несёт файлы и углы: E текущая, undo к P.
func TestDesignDBUploadReplayDoesNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	req := entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: probeMedia(t, raw), Kind: entity.DesignPictureKindFlat}},
	}
	first, err := rep.Design().RegisterBatch(ctx, req)
	require.NoError(t, err)
	require.Len(t, first.Pictures, 1)
	pic := first.Pictures[0]
	edit := editProbe(t, rep, raw, pic, pic.Id)
	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, pic.Id).Int32, "P заменён правкой E")
	holdForeignRoots(t, rep, raw, card, 24)

	started := time.Now()
	again, err := rep.Design().RegisterBatch(noWait(t), req)
	require.NoError(t, err, "повтор пачки не ждёт кадров карточки (ждал %v)", time.Since(started))
	require.True(t, again.Idempotent, "повтор узнан, второй пачки нет")
	byID := map[int]entity.DesignPicture{}
	for _, p := range again.Pictures {
		byID[p.Id] = p
	}
	replayed, ok := byID[pic.Id]
	require.True(t, ok, "повтор отдаёт поданный кадр, а не второй")
	require.NotNil(t, replayed.Media, "файл кадра собран после коммита")
	head, ok := byID[edit.Id]
	require.True(t, ok, "правка E лежит под той же пачкой")
	require.NotNil(t, head.Media)
	require.True(t, head.CanUndo, "углы собраны после коммита")
	require.Equal(t, pic.Id, head.UndoToId)
	require.Equal(t, again.Pictures, again.Batch.Pictures, "обе половины ответа размечены одинаково")
}

// ПОМЕТКА ДЕТАЛИ НЕ ЖДЁТ ЧУЖИХ КАДРОВ КАРТОЧКИ.
//
// SetDetailKept судит устарелость по всему верстаку, а на верстаке две плиты-звена: правка B во
// `front` и правка F (перезаписавшая Q) в детали. Внутри транзакции нужны строки плит (RunId), а не
// их файлы: снятие пометки обязано пройти мимо замков коллеги, и ответ несёт файл и углы плиты детали,
// собранные после коммита.
func TestDesignDBDetailKeptDoesNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	c := newChainProbe(t, rep, raw)
	q := probePicture(t, rep, raw, c.card, entity.DesignPictureKindFlat)
	f := editProbe(t, rep, raw, q, q.Id)
	detail, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: c.card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		PictureId: f.Id, NewDetailName: "pocket", Actor: "probe",
	})
	require.NoError(t, err)
	holdForeignRoots(t, rep, raw, c.card, 24)

	started := time.Now()
	slot, err := rep.Design().SetDetailKept(noWait(t), entity.DesignDetailKeptSet{
		TechCardId: c.card, SlotId: detail.Id, Keep: false, Actor: "probe",
	})
	require.NoError(t, err, "пометка детали не ждёт кадров карточки (ждала %v)", time.Since(started))
	require.Equal(t, detail.Id, slot.Id)
	require.False(t, slot.Kept)
	require.NotNil(t, slot.Picture)
	require.Equal(t, f.Id, slot.Picture.Id)
	require.NotNil(t, slot.Picture.Media, "файл плиты собран после коммита")
	require.True(t, slot.Picture.CanUndo, "углы плиты собраны после коммита")
	require.Equal(t, q.Id, slot.Picture.UndoToId)
}

// ОТВЕТ UNDO СОБИРАЕТСЯ ПОСЛЕ КОММИТА — И НЕСЁТ ВСЁ, ЧТО НЁС.
//
// Замков шагу цепочки это не снимает (см. шапку), поэтому проверено содержимое: каждое звено с файлом
// и углами, плита переехавшего слота с файлом и углами текущей версии.
func TestDesignDBUndoAnswerIsResolvedAfterCommit(t *testing.T) {
	rep, raw := probeRepository(t)
	c := newChainProbe(t, rep, raw)

	res, err := c.undo(rep)
	require.NoError(t, err)
	require.Equal(t, c.sheet.Id, res.CurrentPictureId)
	require.Len(t, res.Pictures, 2)
	for _, p := range res.Pictures {
		require.NotNil(t, p.Media, "файл звена %d собран после коммита", p.Id)
	}
	a := resultPicture(t, res, c.sheet.Id)
	require.True(t, a.CanRedo, "углы звена собраны после коммита")
	require.Len(t, res.Slots, 1)
	require.NotNil(t, res.Slots[0].Picture)
	require.Equal(t, c.sheet.Id, res.Slots[0].Picture.Id)
	require.NotNil(t, res.Slots[0].Picture.Media, "файл плиты собран после коммита")
	require.True(t, res.Slots[0].Picture.CanRedo, "углы плиты собраны после коммита")
}
