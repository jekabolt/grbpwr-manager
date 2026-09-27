package design_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/store/techcard"
	"github.com/stretchr/testify/require"
)

// ═══ «ПЕРЕЗАПИСАТЬ» ПРАВКОЙ — 0369, O-53 ═══════════════════════════════════════════════════════
//
// Владелец: после правки в воркбенче FLAT спрашивать «overwrite или save as new». Перезапись — ТОТ
// ЖЕ флэттен: правка файлится сиблингом, ничего не перепикселивается и не прячется, а в той же
// транзакции слот, где стоял оригинал, переезжает на правку (slot_rev + 1) и оригинал получает
// replaced_by.
//
// ⚠ ПОЧЕМУ ОБВЯЗКА КОНТЕЙНЕРНАЯ. Все утверждения — про то, что легло В СТРОКИ двух таблиц одной
// транзакцией: переезд слота идёт через setBenchSlotTx (CAS и сторожа постановки), штамп — отдельным
// UPDATE под `replaced_by IS NULL`, отказ обязан не подать НИЧЕГО. Мок ответил бы то, что ему велели.
// Решение сторожей и их порядок проверены без базы в entity (design_replace_test.go).
//
// Запуск — тот же одноразовый контейнер, что и у соседних проб (см. шапку wave2_db_test.go); без
// CI=1 каждая проба пропускается ДО открытия соединения.

// replaceProbeSetup — лист в слоте `front` и слой правки поверх его файла: состояние воркбенча в
// момент вопроса «overwrite или save as new».
type replaceProbeSetup struct {
	card  int
	sheet entity.DesignPicture
	slot  *entity.DesignBenchSlot
	layer *entity.DesignEditLayer
}

func newReplaceProbeSetup(t *testing.T, rep dependency.Repository, raw *sql.DB) replaceProbeSetup {
	t.Helper()
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: sheet.Id, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)
	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, BaseMediaId: sheet.MediaId, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	return replaceProbeSetup{card: card, sheet: sheet, slot: slot, layer: layer}
}

func (p replaceProbeSetup) overwrite(media, original int) entity.DesignEditLayerFlatten {
	return entity.DesignEditLayerFlatten{
		TechCardId: p.card, LayerId: p.layer.Id, ExpectedRev: p.layer.Rev, MediaId: media,
		ReplacePictureId: original, Actor: "overwriter",
	}
}

// probeReplacedBy читает колонку НАПРЯМУЮ, минуя стор: проба про то, что лежит в базе, не спрашивает
// об этом ту же функцию, которую проверяет.
func probeReplacedBy(t *testing.T, raw *sql.DB, pictureID int) sql.NullInt32 {
	t.Helper()
	var got sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT replaced_by FROM design_picture WHERE id = ?`, pictureID).Scan(&got))
	return got
}

// probeSlotHolder — кто стоит в слоте и на какой ревизии, тоже мимо стора.
func probeSlotHolder(t *testing.T, raw *sql.DB, slotID int) (picture sql.NullInt32, rev int, setBy string) {
	t.Helper()
	require.NoError(t, raw.QueryRow(
		`SELECT picture_id, slot_rev, set_by FROM design_bench_slot WHERE id = ?`, slotID).Scan(&picture, &rev, &setBy))
	return picture, rev, setBy
}

// ПЕРЕЗАПИСЬ: ПРАВКА — СИБЛИНГ, СЛОТ НА НЕЙ, ОРИГИНАЛ ЦЕЛ И ПОДПИСАН.
//
// МУТАЦИИ, НА КОТОРЫХ КРАСНЕЕТ: не вызвать переезд слота (слот остаётся на оригинале); переносить
// слот своим UPDATE мимо setBenchSlotTx без инкремента (ревизия не растёт, и чужая вкладка не узнает
// о переезде); не штамповать оригинал; «перезаписать» подменой медиа или прятаньем (media_id и
// hidden_at оригинала сравниваются с исходными).
func TestDesignDBOverwriteMovesTheSlotAndStampsTheOriginal(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err)

	// Правка — ОБЫЧНЫЙ флэттен-сиблинг оригинала под той же строкой, и сама не заменена.
	require.Equal(t, entity.DesignDerivationFlatten, edit.Derivation)
	require.EqualValues(t, p.sheet.Id, edit.DerivedFrom.Int32)
	require.Equal(t, p.sheet.BatchId, edit.BatchId, "звено цепочки лежит под строкой оригинала")
	require.Equal(t, p.sheet.RunId, edit.RunId)
	require.False(t, edit.ReplacedBy.Valid, "голова цепочки не заменена")

	// Слот переехал — через CAS, с ревизией +1 и подписью того, кто перезаписал.
	holder, rev, setBy := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, edit.Id, holder.Int32, "слот, где стоял оригинал, держит правку")
	require.Equal(t, p.slot.SlotRev+1, rev, "переезд — постановка, и ревизия слота растёт")
	require.Equal(t, "overwriter", setBy)

	// Оригинал подписан и НЕ тронут: тот же файл, не спрятан.
	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
	var media int
	var hidden sql.NullTime
	require.NoError(t, raw.QueryRow(`SELECT media_id, hidden_at FROM design_picture WHERE id = ?`,
		p.sheet.Id).Scan(&media, &hidden))
	require.Equal(t, p.sheet.MediaId, media, "пиксели оригинала не подменяются")
	require.False(t, hidden.Valid, "оригинал не прячется")

	// И ЭТО ВИДНО ЧЕРЕЗ ЧТЕНИЕ ПОЛОСЫ, а не только в ответе пишущей двери: забытый тег `db` прошёл
	// бы все проверки выше и отдал бы клиенту ноль.
	band, err := rep.Design().GetBand(ctx, p.card, 12)
	require.NoError(t, err)
	var seen *entity.DesignPicture
	for i := range band.Batches {
		for j := range band.Batches[i].Pictures {
			if band.Batches[i].Pictures[j].Id == p.sheet.Id {
				seen = &band.Batches[i].Pictures[j]
			}
		}
	}
	require.NotNil(t, seen, "оригинал остаётся в полосе")
	require.EqualValues(t, edit.Id, seen.ReplacedBy.Int32)
	var front *entity.DesignBenchSlot
	for i := range band.Bench {
		if band.Bench[i].Id == p.slot.Id {
			front = &band.Bench[i]
		}
	}
	require.NotNil(t, front)
	require.NotNil(t, front.Picture)
	require.Equal(t, edit.Id, front.Picture.Id, "верстак полосы рисует правку")
}

// probeRequestKey — ключ жеста, как он лёг в строку, мимо стора.
func probeRequestKey(t *testing.T, raw *sql.DB, pictureID int) sql.NullString {
	t.Helper()
	var got sql.NullString
	require.NoError(t, raw.QueryRow(`SELECT request_key FROM design_picture WHERE id = ?`, pictureID).Scan(&got))
	return got
}

// requireHead — отказ already_replaced и голова, которую он несёт.
func requireHead(t *testing.T, err error, named, head int) {
	t.Helper()
	require.ErrorIs(t, err, entity.ErrDesignAlreadyReplaced)
	var replaced *entity.DesignReplacedError
	require.ErrorAs(t, err, &replaced, "already_replaced обязан нести голову цепочки")
	require.Equal(t, named, replaced.PictureId)
	require.Equal(t, head, replaced.HeadPictureId)
}

// ПОВТОР ПЕРЕЗАПИСИ БЕЗ КЛЮЧА ОТКАЗЫВАЕТСЯ ДО ВСТАВКИ — И НАЗЫВАЕТ ГОЛОВУ.
//
// Без ключа повтор неотличим от чужой перезаписи, и честный ответ ему — already_replaced: оригинал
// уже подписан, сторож стоит ДО вставки, второй правки нет, слот второй раз не двигается. Отказ несёт
// голову цепочки — после второй перезаписи это уже не первая правка, а вторая.
//
// МУТАЦИИ: проверять заменённость после вставки (или не проверять вовсе) — строк становится больше;
// класть в отказ replaced_by названного кадра вместо головы — вторая половина краснеет.
func TestDesignDBOverwriteRetryWithoutAKeyIsRefusedWithTheHead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	req := p.overwrite(probeMedia(t, raw), p.sheet.Id)
	edit, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	require.False(t, probeRequestKey(t, raw, edit.Id).Valid, "без ключа — NULL, а не пустая строка")
	before := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	_, revBefore, _ := probeSlotHolder(t, raw, p.slot.Id)

	_, err = rep.Design().FlattenEditLayer(ctx, req)
	requireHead(t, err, p.sheet.Id, edit.Id)
	require.Equal(t, before, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card),
		"повтор перезаписи не подаёт вторую правку")
	holder, revAfter, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, edit.Id, holder.Int32)
	require.Equal(t, revBefore, revAfter, "отказанный повтор слот не трогает")

	// Правку перезаписывают в свою очередь — голова уезжает вперёд.
	edit2 := editProbe(t, rep, raw, *edit, edit.Id)
	_, err = rep.Design().FlattenEditLayer(ctx, req)
	requireHead(t, err, p.sheet.Id, edit2.Id)
}

// ПОВТОР ПЕРЕЗАПИСИ С КЛЮЧОМ — УСПЕХ, И ОТВЕТ ЕМУ — ПРАВКА ПЕРВОЙ ПОПЫТКИ (0370).
//
// Ответ потерян, клиент повторяет тот же запрос с тем же ключом: он обязан получить ту же правку, а
// не already_replaced на собственный успех. И получает её В ЛЮБОМ ПОСЛЕДУЮЩЕМ СОСТОЯНИИ: после того
// как коллега сохранил слой (CAS по ревизии больше не сошёлся бы) и после того как правку
// перезаписали в свою очередь (она приходит со своим replaced_by).
//
// МУТАЦИИ: не писать ключ во вставку (повтор получает already_replaced); искать повтор ПОСЛЕ сторожей
// (повтор после сохранения слоя получает layer_rev_mismatch, после второй перезаписи —
// already_replaced); отвечать головой вместо правки первой попытки (третья половина краснеет).
func TestDesignDBOverwriteReplayWithTheKeyReturnsTheEdit(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	req := p.overwrite(probeMedia(t, raw), p.sheet.Id)
	req.ClientRequestId = uuid.NewString()
	edit, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	require.Equal(t, req.ClientRequestId, probeRequestKey(t, raw, edit.Id).String, "ключ лёг на правку")
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	before := pictures()
	_, revBefore, _ := probeSlotHolder(t, raw, p.slot.Id)

	replay, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err, "повтор с ключом — успех, а не already_replaced")
	require.Equal(t, edit.Id, replay.Id)
	require.NotNil(t, replay.Media, "ответ повтора — полный кадр, с медиа, как ответ первой попытки")
	require.Equal(t, before, pictures(), "повтор не подаёт вторую правку")
	_, revAfter, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.Equal(t, revBefore, revAfter, "повтор слот не двигает")

	// Коллега сохранил слой: CAS первой попытки больше не сходится, повтор — всё равно тот же ответ.
	_, err = rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: p.card, LayerId: p.layer.Id, ExpectedRev: p.layer.Rev, Strokes: probeStrokes(), Actor: "colleague",
	})
	require.NoError(t, err)
	replay, err = rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err, "повтор отвечается до CAS слоя")
	require.Equal(t, edit.Id, replay.Id)

	// Правку перезаписали: повтор получает СВОЮ правку — с её replaced_by, — а не чужую голову.
	edit2 := editProbe(t, rep, raw, *edit, edit.Id)
	replay, err = rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	require.Equal(t, edit.Id, replay.Id, "ответ повтору — кадр первой попытки, а не нынешняя голова")
	require.EqualValues(t, edit2.Id, replay.ReplacedBy.Int32)
	require.Equal(t, before+1, pictures(), "одна правка первой попытки и одна — второй перезаписи")
}

// ПОВТОР «SAVE AS NEW» С КЛЮЧОМ НЕ ПОДАЁТ ВТОРОГО СИБЛИНГА (0370).
//
// Контроль — тот же запрос без ключа: он подаёт второго сиблинга, как и до поля. Без контроля проба
// зеленела бы и на флэттене, который не подаёт повторов вообще никогда.
//
// МУТАЦИИ: искать повтор только в режиме перезаписи (второй сиблинг); считать пустой ключ ключом
// (контроль перестаёт подавать второго).
func TestDesignDBSaveAsNewReplayWithTheKeyReturnsTheSibling(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}

	req := p.overwrite(probeMedia(t, raw), 0)
	req.ClientRequestId = uuid.NewString()
	sibling, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	before := pictures()
	replay, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	require.Equal(t, sibling.Id, replay.Id)
	require.Equal(t, before, pictures(), "повтор с ключом не подаёт второго сиблинга")

	blind := p.overwrite(probeMedia(t, raw), 0)
	_, err = rep.Design().FlattenEditLayer(ctx, blind)
	require.NoError(t, err)
	_, err = rep.Design().FlattenEditLayer(ctx, blind)
	require.NoError(t, err)
	require.Equal(t, before+2, pictures(), "без ключа — поведение до поля: каждый повтор — сиблинг")
}

// КЛЮЧ, ПОТРАЧЕННЫЙ НА ДРУГОЙ ФЛЭТТЕН, — invalid_argument, И НИЧЕГО НЕ ПОДАНО.
//
// Тот же ключ под другим местом, другим режимом или другой ревизией — ошибка клиента, а не повтор:
// вернуть ему чужой ответ значило бы соврать, что его жест исполнен. Ключ живёт в пределах
// карточки: на другой карточке тот же ключ — просто новый жест.
//
// МУТАЦИИ: не сверять режим (перезапись под ключом «рядом» отвечает сиблингом, слот не двигается, а
// клиент считает, что поставил правку на место); не сверять ревизию; искать ключ без карточки
// (последняя половина краснеет).
func TestDesignDBKeySpentOnAnotherFlattenIsRefused(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	key := uuid.NewString()

	beside := p.overwrite(probeMedia(t, raw), 0)
	beside.ClientRequestId = key
	_, err := rep.Design().FlattenEditLayer(ctx, beside)
	require.NoError(t, err)
	before := pictures()

	t.Run("тот же ключ — перезапись", func(t *testing.T) {
		req := p.overwrite(probeMedia(t, raw), p.sheet.Id)
		req.ClientRequestId = key
		_, err := rep.Design().FlattenEditLayer(ctx, req)
		require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
		require.Equal(t, before, pictures())
		holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
		require.EqualValues(t, p.sheet.Id, holder.Int32, "слот не тронут")
		require.Equal(t, p.slot.SlotRev, rev)
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "оригинал не подписан")
	})
	t.Run("тот же ключ — другая ревизия", func(t *testing.T) {
		saved, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
			TechCardId: p.card, LayerId: p.layer.Id, ExpectedRev: p.layer.Rev, Strokes: probeStrokes(), Actor: "probe",
		})
		require.NoError(t, err)
		req := p.overwrite(probeMedia(t, raw), 0)
		req.ExpectedRev = saved.Rev
		req.ClientRequestId = key
		_, err = rep.Design().FlattenEditLayer(ctx, req)
		require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
		require.Equal(t, before, pictures())
	})
	t.Run("тот же ключ — другая карточка", func(t *testing.T) {
		other := newReplaceProbeSetup(t, rep, raw)
		req := other.overwrite(probeMedia(t, raw), 0)
		req.ClientRequestId = key
		pic, err := rep.Design().FlattenEditLayer(ctx, req)
		require.NoError(t, err, "ключ живёт в пределах карточки")
		require.Equal(t, other.card, pic.TechCardId)
	})
}

// ЦЕПОЧКА: ПРАВКУ МОЖНО ПЕРЕЗАПИСАТЬ В СВОЮ ОЧЕРЕДЬ.
//
// Слот идёт за головой, каждый оригинал помнит СВОЮ замену, и все звенья — под одной строкой.
func TestDesignDBOverwriteChainsAndTheSlotFollowsTheHead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	editMedia := probeMedia(t, raw)
	edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(editMedia, p.sheet.Id))
	require.NoError(t, err)

	// Правку правят слоем поверх ЕЁ файла — один слой на подложку.
	layer2, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: p.card, BaseMediaId: editMedia, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	edit2, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: p.card, LayerId: layer2.Id, ExpectedRev: layer2.Rev, MediaId: probeMedia(t, raw),
		ReplacePictureId: edit.Id, Actor: "overwriter",
	})
	require.NoError(t, err)

	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32, "оригинал помнит свою замену")
	require.EqualValues(t, edit2.Id, probeReplacedBy(t, raw, edit.Id).Int32)
	require.False(t, probeReplacedBy(t, raw, edit2.Id).Valid, "голова — единственное звено с NULL")
	require.Equal(t, p.sheet.BatchId, edit2.BatchId, "вся цепочка под строкой оригинала")

	holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, edit2.Id, holder.Int32, "слот идёт за головой")
	require.Equal(t, p.slot.SlotRev+2, rev)
}

// «SAVE AS NEW» ОСТАЁТСЯ ТЕМ, ЧЕМ БЫЛ: слот на оригинале, оригинал не подписан.
//
// Контроль на ложную зелень проб выше: без него они зеленели бы и на флэттене, который переносит
// слот ВСЕГДА.
func TestDesignDBSaveAsNewLeavesTheOriginalInItsPlace(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), 0))
	require.NoError(t, err)
	require.EqualValues(t, p.sheet.Id, edit.DerivedFrom.Int32)

	holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, p.sheet.Id, holder.Int32, "рядом — значит рядом: слот не тронут")
	require.Equal(t, p.slot.SlotRev, rev)
	require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid)
}

// ОРИГИНАЛ БЕЗ СЛОТА: ПЕРЕЕЗЖАТЬ НЕЧЕМУ, ШТАМП ВСЁ РАВНО СТАВИТСЯ.
//
// Правка из воркбенча — не всегда правка плиты верстака: лист, который никто не поставил в слот,
// тоже можно перезаписать, и тогда вся перезапись — это штамп.
func TestDesignDBOverwriteOfAnUnplacedPictureOnlyStamps(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, BaseMediaId: sheet.MediaId, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)

	edit, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: layer.Id, ExpectedRev: layer.Rev, MediaId: probeMedia(t, raw),
		ReplacePictureId: sheet.Id, Actor: "overwriter",
	})
	require.NoError(t, err)
	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, sheet.Id).Int32)
	require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM design_bench_slot WHERE tech_card_id = ?`, card),
		"слота не было — и не появилось")
}

// ОТКАЗЫ ПЕРЕЗАПИСИ НЕ ПОДАЮТ НИЧЕГО.
//
// Каждый отказ проверяется ДВАЖДЫ: словом и взглядом в таблицы — отказ без второй половины совместим
// с правкой, которая всё-таки легла, и со слотом, который всё-таки уехал.
//
// Последний подслучай — положительный контроль: спрятанный кусок, под которым ничего не стоит, лист
// не держит.
func TestDesignDBOverwriteRefusalsFileNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	other := probePicture(t, rep, raw, p.card, entity.DesignPictureKindFlat)
	foreignCard := probeCard(t, raw)
	foreign := probePicture(t, rep, raw, foreignCard, entity.DesignPictureKindFlat)

	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	requireNothingFiled := func(t *testing.T, before int) {
		t.Helper()
		require.Equal(t, before, pictures(), "отказ не подаёт правку")
		holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
		require.EqualValues(t, p.sheet.Id, holder.Int32, "отказ не двигает слот")
		require.Equal(t, p.slot.SlotRev, rev)
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "отказ не штампует оригинал")
	}

	t.Run("кадр не под слоем", func(t *testing.T) {
		before := pictures()
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), other.Id))
		require.ErrorIs(t, err, entity.ErrDesignReplaceMismatch)
		requireNothingFiled(t, before)
	})
	t.Run("кадр чужой карточки", func(t *testing.T) {
		before := pictures()
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), foreign.Id))
		require.ErrorIs(t, err, entity.ErrDesignReplaceMismatch)
		requireNothingFiled(t, before)
	})
	t.Run("слой с чистого листа", func(t *testing.T) {
		blank, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
			TechCardId: p.card, Strokes: probeStrokes(), Actor: "probe",
		})
		require.NoError(t, err)
		before := pictures()
		_, err = rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
			TechCardId: p.card, LayerId: blank.Id, ExpectedRev: blank.Rev, MediaId: probeMedia(t, raw),
			ReplacePictureId: p.sheet.Id, Actor: "overwriter",
		})
		require.ErrorIs(t, err, entity.ErrDesignReplaceMismatch)
		requireNothingFiled(t, before)
	})

	// Режем лист на один кусок — и перезапись закрывается, пока кусок на виду. Кусок режется из
	// листа, стоящего в слоте: разрез слот не трогает.
	crops, err := rep.Design().SplitPicture(ctx, entity.DesignSplitRequest{
		PictureId: p.sheet.Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: probeMedia(t, raw), ViewKey: entity.DesignViewBack}},
	})
	require.NoError(t, err)
	require.Len(t, crops, 1)

	t.Run("лист разрезан", func(t *testing.T) {
		before := pictures()
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.ErrorIs(t, err, entity.ErrDesignCutSheet)
		requireNothingFiled(t, before)
	})
	t.Run("спрятанный кусок лист не держит", func(t *testing.T) {
		_, err := rep.Design().HidePicture(ctx, crops[0].Id, true, "probe")
		require.NoError(t, err)
		edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.NoError(t, err)
		require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
	})
}

// СПРЯТАННЫЙ ОРИГИНАЛ НЕ ПЕРЕЗАПИСЫВАЕТСЯ, А ПРАВКА РЯДОМ С НИМ ЛОЖИТСЯ (27.09).
//
// Клиент закрывает «overwrite» над спрятанным кадром сам; отказ hidden_picture — задний пояс в
// транзакции флэттена: перезапись сделала бы правку преемником кадра, которого на экране нет. Кадр
// здесь вне слота — спрятанный в слоте не стоит (hide отказывает in_slot), — и прячется законным
// жестом.
//
// Подслучаи идут по порядку и опираются друг на друга:
//   - перезапись спрятанного кадра — hidden_picture с починкой в тексте, и не подано ничего: ни правки,
//     ни штампа, ни слота; кадр как был спрятан;
//   - «save as new» того же слоя проходит: правка ложится сиблингом спрятанного кадра, видимой, а кадр
//     не подписан — этот отказ её не касается;
//   - показанный кадр перезаписывается: отказ был про видимость (положительный контроль);
//   - заменённый И спрятанный кадр — already_replaced с головой, а не hidden_picture: слепой повтор
//     перезаписи узнаёт себя, хотя оригинал с тех пор спрятали.
//
// МУТАЦИИ: снять сторож (первая перезапись проходит и штампует кадр); применить его к «save as new»
// (сиблинг отказывается); судить спрятанность раньше заменённости (последний подслучай получает
// hidden_picture без головы).
func TestDesignDBOverwriteOfAHiddenPictureIsRefused(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)
	sheet := probePicture(t, rep, raw, card, entity.DesignPictureKindFlat)
	layer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: card, BaseMediaId: sheet.MediaId, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	flatten := func(t *testing.T, original int) (*entity.DesignPicture, error) {
		t.Helper()
		return rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
			TechCardId: card, LayerId: layer.Id, ExpectedRev: layer.Rev, MediaId: probeMedia(t, raw),
			ReplacePictureId: original, Actor: "overwriter",
		})
	}
	hide := func(t *testing.T, id int, hidden bool) {
		t.Helper()
		_, err := rep.Design().HidePicture(ctx, id, hidden, "colleague")
		require.NoError(t, err)
	}
	isHidden := func(t *testing.T, id int) bool {
		t.Helper()
		var hidden bool
		require.NoError(t, raw.QueryRow(`SELECT hidden_at IS NOT NULL FROM design_picture WHERE id = ?`, id).Scan(&hidden))
		return hidden
	}
	pictures := func(t *testing.T) int {
		t.Helper()
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, card)
	}
	hide(t, sheet.Id, true)

	t.Run("перезапись спрятанного кадра — отказ, и не подано ничего", func(t *testing.T) {
		before := pictures(t)
		_, err := flatten(t, sheet.Id)
		require.ErrorIs(t, err, entity.ErrDesignHiddenPicture)
		require.NotErrorIs(t, err, entity.ErrDesignHiddenPlate)
		require.Contains(t, err.Error(), "save the edit as a new picture")
		require.Equal(t, before, pictures(t), "отказ не подаёт правку")
		require.False(t, probeReplacedBy(t, raw, sheet.Id).Valid, "отказ не штампует оригинал")
		require.True(t, isHidden(t, sheet.Id), "отказ не показывает кадр")
		require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM design_bench_slot WHERE tech_card_id = ?`, card),
			"отказ ничего не ставит в слот")
	})
	var beside *entity.DesignPicture
	t.Run("save as new спрятанного кадра проходит", func(t *testing.T) {
		var err error
		beside, err = flatten(t, 0)
		require.NoError(t, err)
		require.EqualValues(t, sheet.Id, beside.DerivedFrom.Int32, "правка — сиблинг спрятанного кадра")
		require.False(t, beside.HiddenAt.Valid, "правка рождается видимой")
		require.False(t, probeReplacedBy(t, raw, sheet.Id).Valid, "рядом — значит рядом")
		require.True(t, isHidden(t, sheet.Id))
	})
	var edit *entity.DesignPicture
	t.Run("показанный кадр перезаписывается", func(t *testing.T) {
		hide(t, sheet.Id, false)
		var err error
		edit, err = flatten(t, sheet.Id)
		require.NoError(t, err, "отказ был про видимость")
		require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, sheet.Id).Int32)
	})
	t.Run("заменённый и спрятанный — already_replaced с головой", func(t *testing.T) {
		require.NotNil(t, beside, "подслучаи выше обязаны были подать обе правки")
		require.NotNil(t, edit)
		// Видимых детей у прячущегося кадра быть не должно (live_crop_parent): сперва прячут обе правки.
		hide(t, beside.Id, true)
		hide(t, edit.Id, true)
		hide(t, sheet.Id, true)
		before := pictures(t)
		_, err := flatten(t, sheet.Id)
		require.NotErrorIs(t, err, entity.ErrDesignHiddenPicture)
		requireHead(t, err, sheet.Id, edit.Id)
		require.Equal(t, before, pictures(t))
	})
}

// КУСОК, ПЕРЕЗАПИСАННЫЙ СВОЕЙ ПРАВКОЙ, ЛИСТ ДЕРЖИТ (O-53 review).
//
// Сценарий ревью дословно: лист разрезан на кусок, кусок перезаписан правкой, затем перезаписывают
// лист. Правка куска нарезана из прежних пикселей листа, и перезапись листа оставила бы на экране
// две живые ветки одного листа. Отказ — cut_sheet, и ничего не подано.
//
// МУТАЦИЯ: вернуть в чтение ветки (designBranchCropsOf) `replaced_by IS NULL` — перезапись листа
// проходит.
func TestDesignDBOverwriteOfASheetIsHeldByAnOverwrittenPiece(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	crops := splitProbe(t, rep, raw, p.sheet.Id, entity.DesignViewBack)
	require.Len(t, crops, 1)
	pieceEdit := editProbe(t, rep, raw, crops[0], crops[0].Id)
	require.EqualValues(t, pieceEdit.Id, probeReplacedBy(t, raw, crops[0].Id).Int32, "кусок заменён своей правкой")

	before := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.ErrorIs(t, err, entity.ErrDesignCutSheet)
	require.Equal(t, before, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card))
	holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, p.sheet.Id, holder.Int32, "отказ не двигает слот")
	require.Equal(t, p.slot.SlotRev, rev)
	require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "отказ не штампует лист")
}

// probeSourceLayer — слой, из которого кадр расплющен (0371), мимо стора.
func probeSourceLayer(t *testing.T, raw *sql.DB, pictureID int) sql.NullInt32 {
	t.Helper()
	var got sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT source_layer_id FROM design_picture WHERE id = ?`, pictureID).Scan(&got))
	return got
}

// СПРЯТАННЫЙ КУСОК С ВИДИМОЙ ПРАВКОЙ ЛИСТ ДЕРЖИТ — СЦЕНАРИЙ УСТАРЕВШЕЙ ВКЛАДКИ (O-53 review, раунд 2).
//
// По ревью: лист S разрезан на кусок C; C открыт в редакторе; C спрятали; устаревшая вкладка
// перезаписывает спрятанный C — правка E рождается видимой; затем перезаписывают S. Сторож смотрел
// на строку C (спрятана) и пускал перезапись S, пока E, нарезанная из прежних пикселей S, стоит на
// экране. Кусок судится всей веткой: E на виду — лист держится. Положительный контроль: спрятали E —
// от ветки на экране не осталось ничего, и лист свободен.
//
// Перезапись спрятанного C теперь отказывается (hidden_picture, 27.09) — первый шаг пробы это и
// проверяет, — поэтому то же состояние собирается жестами, которые законны и сегодня: C показывают и
// перезаписывают правкой E; прячут E, затем C (видимых детей у C уже нет); возвращают E — показ не
// сторожится. Строки выходят ровно те, что оставляла устаревшая вкладка: C спрятан и заменён E, E на
// виду.
//
// МУТАЦИЯ: судить кусок по его строке (вернуть hidden_at IS NULL в чтение ветки designBranchCropsOf
// или читать HiddenAt одного куска вместо его ветки) — первая перезапись листа проходит.
func TestDesignDBOverwriteOfASheetIsHeldByAHiddenPieceWithAVisibleEdit(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	hide := func(t *testing.T, id int, hidden bool) {
		t.Helper()
		_, err := rep.Design().HidePicture(ctx, id, hidden, "colleague")
		require.NoError(t, err)
	}

	crops := splitProbe(t, rep, raw, p.sheet.Id, entity.DesignViewBack)
	require.Len(t, crops, 1)
	piece := crops[0]
	// Кусок открыт в редакторе — слой поверх его файла.
	pieceLayer, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: p.card, BaseMediaId: piece.MediaId, Strokes: probeStrokes(), Actor: "stale-tab",
	})
	require.NoError(t, err)
	overwritePiece := func() (*entity.DesignPicture, error) {
		return rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
			TechCardId: p.card, LayerId: pieceLayer.Id, ExpectedRev: pieceLayer.Rev,
			MediaId: probeMedia(t, raw), ReplacePictureId: piece.Id, Actor: "stale-tab",
		})
	}
	// Кусок прячут из другой вкладки — и устаревшая вкладка его больше не перезапишет.
	hide(t, piece.Id, true)
	before := pictures()
	_, err = overwritePiece()
	require.ErrorIs(t, err, entity.ErrDesignHiddenPicture, "спрятанный кусок не перезаписывается")
	require.Equal(t, before, pictures(), "отказ не подаёт правку куска")
	require.False(t, probeReplacedBy(t, raw, piece.Id).Valid, "отказ не штампует кусок")

	// То же состояние — законными жестами.
	hide(t, piece.Id, false)
	edit, err := overwritePiece()
	require.NoError(t, err)
	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, piece.Id).Int32)
	hide(t, edit.Id, true)
	hide(t, piece.Id, true)
	hide(t, edit.Id, false)

	before = pictures()
	_, err = rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.ErrorIs(t, err, entity.ErrDesignCutSheet, "видимая голова ветки держит лист")
	require.Equal(t, before, pictures(), "отказ не подаёт правку листа")
	holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, p.sheet.Id, holder.Int32, "отказ не двигает слот")
	require.Equal(t, p.slot.SlotRev, rev)
	require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "отказ не штампует лист")

	// Правку спрятали — на экране от куска не осталось ничего, и лист свободен.
	_, err = rep.Design().HidePicture(ctx, edit.Id, true, "colleague")
	require.NoError(t, err)
	sheetEdit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err, "спрятанная ветка лист не держит")
	require.EqualValues(t, sheetEdit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
}

// КУСОК, ОТРЕЗАННЫЙ ОТ СПРЯТАННОЙ ГОЛОВЫ, ДЕРЖИТ ЛИСТ — СЦЕНАРИЙ CODEX (O-53 review, раунд 3).
//
// Дословно по ревью: лист S разрезан на кусок C; C (вне слота) перезаписан правкой E; E спрятали;
// устаревшая вкладка режет спрятанную E на F — F рождается видимой; затем перезаписывают S. Сторож
// раунда 2 доходил по цепочке замен C до спрятанной головы E, считал ноль и пускал перезапись S, пока
// F, нарезанная из прежних пикселей S, стоит на экране.
//
// Разрез спрятанной E теперь отказывается (TestDesignDBSplitOfAHiddenPictureIsRefused), поэтому то же
// состояние собирается жестами, которые законны и сегодня: F режут от E, пока E на виду; прячут F,
// затем E (видимых детей у E уже нет); возвращают F — показ не сторожится. Строки выходят ровно те,
// что оставляла устаревшая вкладка: C на виду, E спрятана, F от E на виду.
//
// Затем прячут и сам C — теперь лист держит ТОЛЬКО F, через две спрятанные строки: этот случай
// отпускали и суд по строке куска (раунд 1), и суд по голове цепочки (раунд 2). Положительный
// контроль: спрятали F — на экране от ветки не осталось ничего, и перезапись листа проходит.
//
// МУТАЦИИ (entity.DesignStandingPieces): судить по голове цепочки; судить по строке куска; выбросить
// ребро разреза — второй отказ становится перезаписью. Сузить чтение сторожа до кусков листа — тоже
// краснеет: E и F не прочитаны, замена C ведёт мимо прочитанного, и второй отказ становится Internal.
func TestDesignDBOverwriteOfASheetIsHeldByACropOfAHiddenHead(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	pictures := func(t *testing.T) int {
		t.Helper()
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	hide := func(t *testing.T, id int, hidden bool) {
		t.Helper()
		_, err := rep.Design().HidePicture(ctx, id, hidden, "colleague")
		require.NoError(t, err)
	}
	requireHeld := func(t *testing.T, why string) {
		t.Helper()
		before := pictures(t)
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.ErrorIs(t, err, entity.ErrDesignCutSheet, why)
		require.Equal(t, before, pictures(t), "отказ не подаёт правку листа")
		holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
		require.EqualValues(t, p.sheet.Id, holder.Int32, "отказ не двигает слот")
		require.Equal(t, p.slot.SlotRev, rev)
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "отказ не штампует лист")
	}

	cut := splitProbe(t, rep, raw, p.sheet.Id, entity.DesignViewBack)
	require.Len(t, cut, 1)
	c := cut[0]
	e := editProbe(t, rep, raw, c, c.Id)
	require.EqualValues(t, e.Id, probeReplacedBy(t, raw, c.Id).Int32, "C заменён правкой E")
	under := splitProbe(t, rep, raw, e.Id, entity.DesignViewBack)
	require.Len(t, under, 1)
	f := under[0]
	require.EqualValues(t, e.Id, f.DerivedFrom.Int32, "F отрезан от E")
	hide(t, f.Id, true)
	hide(t, e.Id, true)
	hide(t, f.Id, false)

	requireHeld(t, "сценарий Codex: C на виду, E спрятана, F от E на виду")

	hide(t, c.Id, true)
	requireHeld(t, "C и E спрятаны, F от спрятанной головы на виду — лист держит F")

	hide(t, f.Id, true)
	sheetEdit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err, "от ветки куска на экране не осталось ничего")
	require.EqualValues(t, sheetEdit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
}

// ПЕРЕЗАПИСЬ ЛИСТА НЕ ЖДЁТ КАДРОВ КАРТОЧКИ ВНЕ ЕГО ВЕТКИ (O-53 review, раунд 4).
//
// Сторож cut_sheet раунда 3 читал все кадры карточки одним SELECT, и под SERIALIZABLE этот SELECT
// ставил разделяемый замок на каждый кадр карточки: чужая транзакция, державшая любой кадр той же
// карточки, останавливала перезапись до своего конца (или до innodb_lock_wait_timeout). Теперь ветка
// читается уровнями по id родителей и целей, и кадры вне её не читаются вовсе.
//
// ЧЕМ ЭТО ДОКАЗАНО. Журнала запросов у стора нет, поэтому НАБОР запросов отсюда не проверить — его
// проверяет entity (TestDesignLoadBranchReadsWhatTheWalkNeeds: какие id называет каждый вызов, и что
// шум карточки не читается ни разу). Здесь проверено то, ради чего набор менялся, — след замков:
// транзакция коллеги держит под эксклюзивным замком двадцать четыре посторонних корня той же
// карточки (каждый — в своей пачке), и перезапись листа, у которого есть спрятанный кусок,
// обязана пройти до конца, не дождавшись её.
//
// МУТАЦИЯ: вернуть чтение всей карточки — перезапись упирается в замки коллеги и не укладывается в
// десять секунд (innodb_lock_wait_timeout по умолчанию — пятьдесят).
func TestDesignDBOverwriteDoesNotLockTheRestOfTheCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	cut := splitProbe(t, rep, raw, p.sheet.Id, entity.DesignViewBack)
	require.Len(t, cut, 1)
	_, err := rep.Design().HidePicture(ctx, cut[0].Id, true, "probe")
	require.NoError(t, err, "спрятанный кусок лист не держит — перезапись обязана пройти")

	roots := make([]any, 0, 24)
	for i := 0; i < 24; i++ {
		roots = append(roots, probePicture(t, rep, raw, p.card, entity.DesignPictureKindFlat).Id)
	}
	media := probeMedia(t, raw)

	colleague, err := raw.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = colleague.Rollback() }()
	_, err = colleague.ExecContext(ctx,
		`UPDATE design_picture SET selected = 1 WHERE id IN (?`+strings.Repeat(", ?", len(roots)-1)+`)`, roots...)
	require.NoError(t, err)

	wait, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	started := time.Now()
	edit, err := rep.Design().FlattenEditLayer(wait, p.overwrite(media, p.sheet.Id))
	require.NoError(t, err, "перезапись не ждёт кадров вне ветки листа (ждала %v)", time.Since(started))
	require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
}

// КРОП ЛИСТА НА ЧУЖОЙ КАРТОЧКЕ — ПОРЧА, И ПЕРЕЗАПИСЬ ПО НЕЙ НЕ ПРОХОДИТ (O-53 review, раунд 4).
//
// Ни один писатель такой строки не делает: разрез кладёт кроп на карточку родителя, а derived_from
// без FK. Поэтому строка заводится напрямую: видимый кроп листа S, записанный на другую карточку.
// Скан по карточке раунда 3 её не видел, и S уходил под правку молча. Теперь ветка читается по
// derived_from без предиката карточки, кроп приходит, и чтение ветки называет его порчей тут же
// (раунд 5; обход сказал бы те же слова): ошибка без сентинела полосы (клиенту Internal), и не подано
// ничего.
//
// МУТАЦИИ: вернуть предикат карточки в запрос кропов (перезапись проходит); снять сверку карточки и в
// чтении ветки, и в обходе (видимый кроп держит лист, и ответ — cut_sheet, будто кусок законный).
func TestDesignDBOverwriteRefusesACropFiledOnAnotherCard(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	other := probeCard(t, raw)
	_, err := raw.Exec(`INSERT INTO design_picture
		(tech_card_id, media_id, ordinal, kind, derived_from, derivation, source_class, layer_rev)
		VALUES (?, ?, 0, 'flat', ?, 'crop', 'uploaded', 0)`,
		other, probeMedia(t, raw), p.sheet.Id)
	require.NoError(t, err)

	before := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	_, err = rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.Error(t, err)
	require.Contains(t, err.Error(), "belongs to tech card")
	for _, sentinel := range []error{entity.ErrDesignCutSheet, entity.ErrDesignNotFound,
		entity.ErrDesignInvalidArgument, entity.ErrDesignAlreadyReplaced, entity.ErrDesignReplaceMismatch} {
		require.NotErrorIs(t, err, sentinel, "порча — Internal, а не отказ полосы")
	}
	require.Equal(t, before, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card))
	holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, p.sheet.Id, holder.Int32, "порча не двигает слот")
	require.Equal(t, p.slot.SlotRev, rev)
	require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "порча не штампует лист")
}

// КАДР НА ТЕХНИЧЕСКОМ ЛИСТЕ НЕ ПЕРЕЗАПИСЫВАЕТСЯ — И ОТКАЗ НЕ ПОДАЁТ НИЧЕГО (27.09).
//
// Лист — строки tech_card_media с category = 'technical' (TechCard.technical_media): тех-пакет
// печатает их плитами, и перезапись оставила бы на листе оригинал, а слот верстака отдала бы правке —
// две плиты FRONT в одном тех-пакете. Строка листа заводится напрямую, той формой, какой её кладёт
// сейв карточки.
//
// Положительные контроли — в той же пробе, иначе она зеленела бы и на стороже, отказывающем всегда:
//   - «save as new» того же кадра, пока он на листе, проходит: правка рядом ничьего места не занимает;
//   - снятый с листа кадр перезаписывается, хотя тот же файл лежит на МУДБОРДЕ этой карточки и на
//     техническом листе ДРУГОЙ — ни то, ни другое не лист этой карточки.
//
// И ПОРЯДОК В ТРАНЗАКЦИИ: заменённый кадр, который потом поставили на лист, получает already_replaced
// с головой — слепой повтор перезаписи узнаёт себя по этому слову, а не по листу.
//
// МУТАЦИИ: снять сторож (перезапись проходит, слот уезжает); применить его и к «save as new»
// (сиблинг отказывается); снять из чтения категорию или карточку (третий подслучай отказывает);
// читать лист раньше, чем судится заменённость (последний подслучай получает technical_sheet).
func TestDesignDBOverwriteRefusesAPictureOnTheTechnicalSheet(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	other := probeCard(t, raw)

	// put кладёт файл листа p.sheet на список карточки card — так, как его кладёт сейв карточки.
	put := func(t *testing.T, card int, category string) int64 {
		t.Helper()
		kind := map[string]string{"technical": "front", "moodboard": "moodboard"}[category]
		res, err := raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, kind, category, display_order)
			VALUES (?, ?, ?, ?, 0)`, card, p.sheet.MediaId, kind, category)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	onTheSheet := put(t, p.card, "technical")

	t.Run("кадр на листе — отказ, и не подано ничего", func(t *testing.T) {
		before := pictures()
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.ErrorIs(t, err, entity.ErrDesignTechnicalSheet)
		require.Contains(t, err.Error(), "take it off the sheet first, or save the edit as a new picture")
		for _, other := range []error{entity.ErrDesignCutSheet, entity.ErrDesignAlreadyReplaced, entity.ErrDesignReplaceMismatch} {
			require.NotErrorIs(t, err, other)
		}
		require.Equal(t, before, pictures(), "отказ не подаёт правку")
		holder, rev, _ := probeSlotHolder(t, raw, p.slot.Id)
		require.EqualValues(t, p.sheet.Id, holder.Int32, "отказ не двигает слот")
		require.Equal(t, p.slot.SlotRev, rev)
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "отказ не штампует оригинал")
	})
	t.Run("save as new того же кадра на листе проходит", func(t *testing.T) {
		edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), 0))
		require.NoError(t, err)
		require.EqualValues(t, p.sheet.Id, edit.DerivedFrom.Int32)
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "рядом — значит рядом")
	})
	var edit *entity.DesignPicture
	t.Run("снятый с листа кадр перезаписывается: мудборд и лист чужой карточки не держат", func(t *testing.T) {
		_, err := raw.Exec(`DELETE FROM tech_card_media WHERE id = ?`, onTheSheet)
		require.NoError(t, err)
		put(t, p.card, "moodboard")
		put(t, other, "technical")
		edit, err = rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.NoError(t, err)
		require.EqualValues(t, edit.Id, probeReplacedBy(t, raw, p.sheet.Id).Int32)
		holder, _, _ := probeSlotHolder(t, raw, p.slot.Id)
		require.EqualValues(t, edit.Id, holder.Int32, "слот уехал на правку")
	})
	t.Run("заменённый кадр, поставленный на лист, — already_replaced с головой, а не лист", func(t *testing.T) {
		require.NotNil(t, edit, "подслучай выше обязан был перезаписать кадр")
		// Строка кладётся МИМО СТОРА: сейв карточки такой лист больше не собирает (D-57,
		// TestDesignDBCardSaveRefusesAReplacedPictureOnTheSheet). Это лист, собранный до сторожа, —
		// и ровно на нём порядок отказов ещё виден.
		put(t, p.card, "technical")
		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.NotErrorIs(t, err, entity.ErrDesignTechnicalSheet)
		requireHead(t, err, p.sheet.Id, edit.Id)
	})
}

// ═══ ЗЕРКАЛО technical_sheet НА СЕЙВЕ КАРТОЧКИ (27.09, D-57) ═══════════════════════════════════
//
// Инвариант один на две двери: ни одна не добавляет на технический лист карточки новое или лишнее
// вхождение файла её заменённого кадра (строки листа, собранного до сторожа, живут, пока их не снимут).
// Перезапись держит его отказом technical_sheet, сейв — поимённым отказом replaced_picture
// (entity.DesignSheetReplacedRefusal). Пробы ниже ходят НАСТОЯЩИМИ путями обеих дверей: сейв —
// через rep.TechCards().UpdateTechCard (SERIALIZABLE-обёртка с повтором 1213/1205), перезапись —
// через rep.Design().FlattenEditLayer.

// onSheet / onBoard — строка сейва: лист и мудборд.
func onSheet(media int) entity.TechCardMediaItem {
	return entity.TechCardMediaItem{MediaId: media, Category: entity.TechCardMediaCategoryTechnical, Kind: entity.TechCardMediaFront}
}

func onBoard(media int) entity.TechCardMediaItem {
	return entity.TechCardMediaItem{MediaId: media, Category: entity.TechCardMediaCategoryMoodboard, Kind: entity.TechCardMediaMoodboard}
}

// probeSheetPayload — карточка, прочитанная стором, с этим списком медиа, и версия, от которой её
// сохранять. Сейв — полная замена списка, поэтому список пробы и есть лист после сейва.
func probeSheetPayload(t *testing.T, rep dependency.Repository, card int, media ...entity.TechCardMediaItem) (*entity.TechCardInsert, int) {
	t.Helper()
	tc, err := rep.TechCards().GetTechCardById(context.Background(), card)
	require.NoError(t, err)
	payload := tc.TechCardInsert
	payload.Media = media
	return &payload, tc.LockVersion
}

// probeSaveSheet — сейв карточки настоящим путём. Ошибка возвращается, а не проверяется: отказ и
// есть то, о чём пробы спрашивают.
func probeSaveSheet(t *testing.T, rep dependency.Repository, card int, media ...entity.TechCardMediaItem) error {
	t.Helper()
	payload, version := probeSheetPayload(t, rep, card, media...)
	return rep.TechCards().UpdateTechCard(context.Background(), card, payload, version)
}

// requireSheetRefusal — поимённый отказ сейва: строка листа (с нуля на проводе, с единицы человеку)
// и голова цепочки, которую он называет.
func requireSheetRefusal(t *testing.T, err error, item, head int) {
	t.Helper()
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve, "отказ сейва — поимённый ValidationError, а не Internal и не конфликт")
	require.Equal(t, fmt.Sprintf("technical_media[%d].media_id", item), ve.Field)
	require.Equal(t, entity.DesignSheetReplacedReason, ve.Reason)
	require.Equal(t, fmt.Sprintf("technical sheet item %d: this drawing was replaced by picture #%d — "+
		"put the replacement on the sheet, or take this one off", item+1, head), ve.HowToFix)
}

// probeSheetRows — строки листа карточки с этим файлом, мимо стора.
func probeSheetRows(t *testing.T, raw *sql.DB, card, media int) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM tech_card_media
		WHERE tech_card_id = ? AND media_id = ? AND category = 'technical'`, card, media)
}

// probeReplacedOnSheet — сам инвариант, в строках: файлы листа карточки, принадлежащие её
// заменённым кадрам. На карточке без унаследованного листа (собранного до сторожа) обязан быть нулём
// после любого исхода любой пары операций.
func probeReplacedOnSheet(t *testing.T, raw *sql.DB, card int) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM tech_card_media m
		JOIN design_picture p ON p.tech_card_id = m.tech_card_id AND p.media_id = m.media_id
		WHERE m.tech_card_id = ? AND m.category = 'technical' AND p.replaced_by IS NOT NULL`, card)
}

// probeLockVersion — версия карточки, мимо стора.
func probeLockVersion(t *testing.T, raw *sql.DB, card int) int {
	t.Helper()
	var v int
	require.NoError(t, raw.QueryRow(`SELECT lock_version FROM tech_card WHERE id = ?`, card).Scan(&v))
	return v
}

// ПОВТОР ПО КЛЮЧУ ОТВЕЧАЕТСЯ РАНЬШЕ ЛИСТА (D-57).
//
// Перезапись с ключом прошла, ответ потерян, а файл оригинала тем временем оказался на листе. Сейв
// карточки поставить его туда больше не может (сторож D-57, проба ниже), поэтому строка кладётся мимо
// стора: это лист, собранный до сторожа, и ровно на нём порядок виден. Повтор того же ключа обязан
// получить правку первой попытки, а не technical_sheet: ответ «файл на листе» на собственный успех
// оставил бы клиента с правкой, о которой он не знает. Контроль — тот же запрос без ключа: он не
// повтор, и ему отвечает already_replaced с головой (раньше листа по порядку отказов).
//
// МУТАЦИЯ: читать лист раньше, чем искать повтор по ключу, — повтор получает technical_sheet.
func TestDesignDBOverwriteReplayWithTheKeyOutranksTheSheet(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}

	req := p.overwrite(probeMedia(t, raw), p.sheet.Id)
	req.ClientRequestId = uuid.NewString()
	edit, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	before := pictures()

	_, err = raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, kind, category, display_order)
		VALUES (?, ?, 'front', 'technical', 0)`, p.card, p.sheet.MediaId)
	require.NoError(t, err)

	replay, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err, "повтор с ключом — успех, а не technical_sheet")
	require.Equal(t, edit.Id, replay.Id, "ответ повтору — правка первой попытки")
	require.Equal(t, before, pictures(), "повтор не подаёт второй правки")

	noKey := req
	noKey.ClientRequestId = ""
	_, err = rep.Design().FlattenEditLayer(ctx, noKey)
	require.NotErrorIs(t, err, entity.ErrDesignTechnicalSheet)
	requireHead(t, err, p.sheet.Id, edit.Id)
}

// ─── КОНТРОЛЬНАЯ ТОЧКА ВНУТРИ ТРАНЗАКЦИИ ───
//
// Стор принимает обёртку транзакции конструктором (techcard.New / design.New), поэтому проба строит
// СВОИ экземпляры обоих сторов над обёрткой репозитория (rep.Tx — та же SERIALIZABLE с повтором
// 1213/1205), чей колбэк получает хендл, который ОДИН раз, на первой попытке, останавливается сразу
// после названного чтения и ждёт сигнала. Кода продукта это не касается.
//
// ТОЧКА СТОИТ ПОСЛЕ ПРОЧИТАННОГО ЗНАЧЕНИЯ. Оба чтения идут вызовами, которые возвращаются уже с
// разобранным результатом: SelectContext вычитывает все строки в dest, GetContext — скаляр (чтение
// листа перезаписи, designOnTechnicalSheet, идёт им). Точка после QueryRowxContext стояла бы между
// запросом и Scan — не барьер после чтения.

// checkpointDB — хендл транзакции, останавливающийся после чтения, которое называет at.
type checkpointDB struct {
	dependency.DB
	at   func(query string) bool
	stop func()
}

func (c checkpointDB) SelectContext(ctx context.Context, dest any, query string, args ...any) error {
	err := c.DB.SelectContext(ctx, dest, query, args...)
	if c.at(query) {
		c.stop()
	}
	return err
}

func (c checkpointDB) GetContext(ctx context.Context, dest any, query string, args ...any) error {
	err := c.DB.GetContext(ctx, dest, query, args...)
	if c.at(query) {
		c.stop()
	}
	return err
}

// checkpointRep — репозиторий транзакции с этим хендлом вместо своего.
type checkpointRep struct {
	dependency.Repository
	db dependency.DB
}

func (c checkpointRep) DB() dependency.DB { return c.db }

// checkpointTx — rep.Tx, чей колбэк видит checkpointDB.
func checkpointTx(rep dependency.Repository, at func(string) bool, stop func()) func(context.Context, func(context.Context, dependency.Repository) error) error {
	return func(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
		return rep.Tx(ctx, func(ctx context.Context, txRep dependency.Repository) error {
			return f(ctx, checkpointRep{Repository: txRep, db: checkpointDB{DB: txRep.DB(), at: at, stop: stop}})
		})
	}
}

// ДВА ПОРЯДКА И ВЫНУЖДЕННОЕ ПЕРЕПЛЕТЕНИЕ: СЕЙВ ЛИСТА ПРОТИВ ПЕРЕЗАПИСИ — РОВНО ОДИН ПРОХОДИТ (D-57).
//
// Сейв первым — перезапись видит файл на листе (technical_sheet). Перезапись первой — сейв видит
// замену (replaced_picture, строка и голова). Третий подслучай — то, чего два последовательных не
// покажут: обе двери ПРОЧИТАЛИ чистое состояние (сейв — кадры листа, перезапись — лист), и ни одна
// ещё не писала. Контрольные точки держат обе транзакции ровно там, пока обе не дойдут, и отпускают
// вместе; дальше каждая упирается в замок чтения другой — SERIALIZABLE-замки, взятые этими чтениями,
// — и InnoDB выбирает жертву дедлока. Какую — решает база, и проба это не угадывает: она утверждает
// только то, что верно при любой жертве и при любом числе повторов внутри обёрток. Жертва перечитывает
// всё заново на повторе (точка на повторе уже не держит) и отказывает своим отказом; ровно одна
// операция проходит; инвариант в строках цел.
//
// Если одна из точек не достигнута за отведённое время, проба КРАСНЕЕТ, а не молча проходит
// последовательным порядком: её смысл — именно переплетение.
//
// МУТАЦИИ: снять сторож сейва (порядок «перезапись первой» проходит, переплетение кончается двумя
// успехами); читать кадры сейва на другом хендле (переплетение кончается двумя успехами — чтение без
// замка не мешает штампу).
func TestDesignDBSheetSaveAndOverwriteCloseInEitherOrder(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()

	t.Run("сейв первым — перезапись получает technical_sheet", func(t *testing.T) {
		p := newReplaceProbeSetup(t, rep, raw)
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(p.sheet.MediaId)))
		pictures := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)

		_, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.ErrorIs(t, err, entity.ErrDesignTechnicalSheet)
		require.Equal(t, pictures, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card),
			"отказ не подаёт правку")
		require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid)
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, p.sheet.MediaId), "лист сейва на месте")
	})

	t.Run("перезапись первой — сейв получает replaced_picture и не пишет ничего", func(t *testing.T) {
		p := newReplaceProbeSetup(t, rep, raw)
		edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
		require.NoError(t, err)
		version := probeLockVersion(t, raw, p.card)

		requireSheetRefusal(t, probeSaveSheet(t, rep, p.card, onSheet(p.sheet.MediaId)), 0, edit.Id)
		require.Equal(t, version, probeLockVersion(t, raw, p.card), "отказ откатывает и шапку")
		require.Zero(t, probeSheetRows(t, raw, p.card, p.sheet.MediaId))
	})

	t.Run("обе двери прочитали, ни одна не писала — ровно одна проходит", func(t *testing.T) {
		guardRead := func(q string) bool {
			return strings.Contains(q, "FROM design_picture") && strings.Contains(q, "replaced_by IS NOT NULL")
		}
		sheetRead := func(q string) bool {
			return strings.Contains(q, "SELECT EXISTS") && strings.Contains(q, "FROM tech_card_media")
		}
		// Раунд целиком ограничен: обе операции идут под контекстом с дедлайном, контрольные точки и
		// результаты ждутся select'ом с таймером, точка сама отпускает на отмене контекста. Зависшая
		// база роняет пробу, а не вешает прогон; безусловного ожидания здесь нет.
		race := func(t *testing.T, round int) string {
			p := newReplaceProbeSetup(t, rep, raw)
			media := probeMedia(t, raw)
			payload, version := probeSheetPayload(t, rep, p.card, onSheet(p.sheet.MediaId))

			opCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			defer cancel()
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseAll()
			saveRead, flatRead := make(chan struct{}), make(chan struct{})
			var saveOnce, flatOnce sync.Once
			hold := func(once *sync.Once, reached chan struct{}) func() {
				return func() {
					once.Do(func() {
						close(reached)
						select {
						case <-release:
						case <-opCtx.Done():
						}
					})
				}
			}
			base := storeutil.Base{DB: rep.DB(), Now: time.Now}
			saveTx := checkpointTx(rep, guardRead, hold(&saveOnce, saveRead))
			flatTx := checkpointTx(rep, sheetRead, hold(&flatOnce, flatRead))
			cards := techcard.New(base, saveTx, saveTx, func() dependency.Repository { return rep })
			band := design.New(base, flatTx, flatTx)

			type result struct {
				save bool
				err  error
				edit *entity.DesignPicture
			}
			results := make(chan result, 2) // буфер: опоздавшая операция не блокируется на упавшей пробе
			go func() {
				err := cards.UpdateTechCard(opCtx, p.card, payload, version)
				results <- result{save: true, err: err}
			}()
			go func() {
				edit, err := band.FlattenEditLayer(opCtx, p.overwrite(media, p.sheet.Id))
				results <- result{err: err, edit: edit}
			}()

			forced := true
			checkpointsDue := time.NewTimer(30 * time.Second)
			defer checkpointsDue.Stop()
		checkpoints:
			for _, reached := range []chan struct{}{saveRead, flatRead} {
				select {
				case <-reached:
				case <-checkpointsDue.C:
					forced = false
					break checkpoints
				}
			}
			releaseAll()

			var saveErr, flatErr error
			var edit *entity.DesignPicture
			resultsDue := time.NewTimer(100 * time.Second)
			defer resultsDue.Stop()
			for got := 0; got < 2; got++ {
				select {
				case r := <-results:
					if r.save {
						saveErr = r.err
					} else {
						flatErr, edit = r.err, r.edit
					}
				case <-resultsDue.C:
					t.Fatalf("round %d: the save and the overwrite did not both return in time", round)
				}
			}
			require.True(t, forced, "round %d: the two reads did not both happen before either write", round)

			switch {
			case saveErr == nil && flatErr == nil:
				t.Fatalf("round %d: both committed — the sheet holds the file of replaced picture %d", round, p.sheet.Id)
			case saveErr == nil:
				require.ErrorIs(t, flatErr, entity.ErrDesignTechnicalSheet, "round %d", round)
				require.False(t, probeReplacedBy(t, raw, p.sheet.Id).Valid, "round %d", round)
				require.Equal(t, 1, probeSheetRows(t, raw, p.card, p.sheet.MediaId), "round %d", round)
				require.Zero(t, probeReplacedOnSheet(t, raw, p.card), "round %d: инвариант в строках", round)
				return "save won"
			case flatErr == nil:
				requireSheetRefusal(t, saveErr, 0, edit.Id)
				require.Zero(t, probeSheetRows(t, raw, p.card, p.sheet.MediaId), "round %d", round)
				require.Zero(t, probeReplacedOnSheet(t, raw, p.card), "round %d: инвариант в строках", round)
				return "overwrite won"
			}
			t.Fatalf("round %d: both refused — save: %v; overwrite: %v", round, saveErr, flatErr)
			return ""
		}
		outcomes := map[string]int{}
		for round := 0; round < 3; round++ {
			outcomes[race(t, round)]++
		}
		t.Logf("outcomes: %v", outcomes)
	})
}

// СЕЙВ СУДИТ ПЕРЕХОД: УНАСЛЕДОВАННЫЙ ЛИСТ СОХРАНЯЕТСЯ, НОВОЕ ВХОЖДЕНИЕ ОТКАЗЫВАЕТ (D-57, раунд 3).
//
// Лист, собранный до сторожа, держит файл заменённого кадра — на бете такое возможно: перезапись
// уехала раньше technical_sheet. Строка кладётся мимо стора (так её оставил сейв до сторожа), и
// карточка проживает через настоящий сейв всё, что с ней делают: правку другого поля при
// нетронутом листе, перестановку, вторую копию файла, снятие и возврат, голову цепочки.
//
// МУТАЦИИ: судить весь входящий лист (первая половина отказывает — карточка несохраняема); сравнивать
// множества (третья проходит — одна унаследованная строка разрешила вторую копию); читать сохранённый
// лист после переписи (все вхождения выглядят унаследованными — третья и четвёртая проходят).
func TestDesignDBCardSaveJudgesTheSheetTransition(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO tech_card_media (tech_card_id, media_id, kind, category, display_order)
		VALUES (?, ?, 'front', 'technical', 0)`, p.card, p.sheet.MediaId)
	require.NoError(t, err)
	other := probeMedia(t, raw)
	legacy := p.sheet.MediaId

	t.Run("лист как есть, правка другого поля — проходит", func(t *testing.T) {
		payload, version := probeSheetPayload(t, rep, p.card, onSheet(legacy))
		payload.Notes = sql.NullString{String: "edited beside a legacy sheet", Valid: true}
		require.NoError(t, rep.TechCards().UpdateTechCard(ctx, p.card, payload, version))
		var notes sql.NullString
		require.NoError(t, raw.QueryRow(`SELECT notes FROM tech_card WHERE id = ?`, p.card).Scan(&notes))
		require.Equal(t, "edited beside a legacy sheet", notes.String, "правка легла")
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, legacy), "унаследованная строка на месте")
	})
	t.Run("переставлен за другой файл — проходит", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(other), onSheet(legacy)))
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, legacy))
	})
	t.Run("вторая копия — отказ первому вхождению сверх сохранённого, и не записано ничего", func(t *testing.T) {
		version := probeLockVersion(t, raw, p.card)
		err := probeSaveSheet(t, rep, p.card, onSheet(legacy), onSheet(other), onSheet(legacy))
		requireSheetRefusal(t, err, 2, edit.Id)
		require.Equal(t, version, probeLockVersion(t, raw, p.card))
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, legacy))
	})
	t.Run("снят — и обратно не встаёт", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(other)))
		require.Zero(t, probeSheetRows(t, raw, p.card, legacy))
		requireSheetRefusal(t, probeSaveSheet(t, rep, p.card, onSheet(other), onSheet(legacy)), 1, edit.Id)
		require.Zero(t, probeSheetRows(t, raw, p.card, legacy))
	})
	t.Run("голова цепочки — проходит", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(other), onSheet(edit.MediaId)))
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, edit.MediaId))
	})
}

// СЕЙВ КАРТОЧКИ НЕ СТАВИТ НА ЛИСТ ФАЙЛ ЗАМЕНЁННОГО КАДРА — И ТОЛЬКО ЕГО (D-57).
//
// МУТАЦИИ: снять сторож (первый подслучай сохраняется); проверять и мудборд (третий отказывает); не
// смотреть на replaced_by (второй отказывает: голова — кадр этой карточки); не смотреть на карточку
// (четвёртый отказывает); назвать не голову (пятый называет первую правку).
func TestDesignDBCardSaveRefusesAReplacedPictureOnTheSheet(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	edit, err := rep.Design().FlattenEditLayer(ctx, p.overwrite(probeMedia(t, raw), p.sheet.Id))
	require.NoError(t, err)
	// Чужая карточка со своим заменённым кадром.
	q := newReplaceProbeSetup(t, rep, raw)
	_, err = rep.Design().FlattenEditLayer(ctx, q.overwrite(probeMedia(t, raw), q.sheet.Id))
	require.NoError(t, err)

	t.Run("файл заменённого кадра вторым на листе — отказ с местом и головой, и не записано ничего", func(t *testing.T) {
		first := probeMedia(t, raw)
		version := probeLockVersion(t, raw, p.card)
		err := probeSaveSheet(t, rep, p.card, onBoard(probeMedia(t, raw)), onSheet(first), onSheet(p.sheet.MediaId))
		requireSheetRefusal(t, err, 1, edit.Id)
		require.Equal(t, version, probeLockVersion(t, raw, p.card), "отказ откатывает шапку")
		require.Zero(t, probeSheetRows(t, raw, p.card, first), "и строку листа перед отказавшей")
	})
	t.Run("голова цепочки на листе проходит", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(edit.MediaId)))
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, edit.MediaId))
	})
	t.Run("тот же файл на мудборде проходит", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onBoard(p.sheet.MediaId), onSheet(edit.MediaId)))
	})
	t.Run("заменённый кадр чужой карточки этот лист не держит", func(t *testing.T) {
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(q.sheet.MediaId)))
		require.Equal(t, 1, probeSheetRows(t, raw, p.card, q.sheet.MediaId))
	})
	t.Run("голова уехала вперёд — отказ называет новую", func(t *testing.T) {
		// Лист пуст, иначе перезапись правки сама получила бы technical_sheet.
		require.NoError(t, probeSaveSheet(t, rep, p.card))
		edit2 := editProbe(t, rep, raw, *edit, edit.Id)
		requireSheetRefusal(t, probeSaveSheet(t, rep, p.card, onSheet(p.sheet.MediaId)), 0, edit2.Id)
		requireSheetRefusal(t, probeSaveSheet(t, rep, p.card, onSheet(edit.MediaId)), 0, edit2.Id)
		require.NoError(t, probeSaveSheet(t, rep, p.card, onSheet(edit2.MediaId)))
	})
}

// КЛЮЧ ПРИВЯЗАН К СЛОЮ — ДРУГОЙ СЛОЙ ТОЙ ЖЕ КАРТОЧКИ НА ТОЙ ЖЕ РЕВИЗИИ ОТВЕТА НЕ ПОЛУЧАЕТ (0371).
//
// Сценарий ревью: слои L1 и L2 одной карточки, оба на ревизии 1, над разными листами. «Save as new»
// L1 под ключом K, затем тот же K от L2: раньше повтор отдавал кадр L1 как успех L2, не прочитав L2.
// Теперь кадр помнит свой слой (source_layer_id), и чужой слой получает invalid_argument, ничего не
// подав. То же — в режиме перезаписи.
//
// МУТАЦИИ: не писать source_layer_id во вставку (первая проверка краснеет, а повтор того же слоя
// отказывается — см. TestDesignDBOverwriteReplayWithTheKeyReturnsTheEdit); не сверять слой (оба
// подслучая отдают чужой кадр и не краснеют на ошибке).
func TestDesignDBKeyIsBoundToItsLayer(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)
	other := probePicture(t, rep, raw, p.card, entity.DesignPictureKindFlat)
	layer2, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
		TechCardId: p.card, BaseMediaId: other.MediaId, Strokes: probeStrokes(), Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, p.layer.Rev, layer2.Rev, "оба слоя на одной ревизии — ровно сценарий ревью")
	pictures := func() int {
		return countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	}
	fromLayer2 := func(replace int, key string) entity.DesignEditLayerFlatten {
		return entity.DesignEditLayerFlatten{
			TechCardId: p.card, LayerId: layer2.Id, ExpectedRev: layer2.Rev, MediaId: probeMedia(t, raw),
			ReplacePictureId: replace, ClientRequestId: key, Actor: "overwriter",
		}
	}

	t.Run("рядом", func(t *testing.T) {
		key := uuid.NewString()
		first := p.overwrite(probeMedia(t, raw), 0)
		first.ClientRequestId = key
		filed, err := rep.Design().FlattenEditLayer(ctx, first)
		require.NoError(t, err)
		require.EqualValues(t, p.layer.Id, probeSourceLayer(t, raw, filed.Id).Int32, "кадр помнит свой слой")

		before := pictures()
		_, err = rep.Design().FlattenEditLayer(ctx, fromLayer2(0, key))
		require.ErrorIs(t, err, entity.ErrDesignInvalidArgument, "чужой слой не получает кадр L1 как свой успех")
		require.Equal(t, before, pictures())
	})
	t.Run("на место", func(t *testing.T) {
		key := uuid.NewString()
		first := p.overwrite(probeMedia(t, raw), p.sheet.Id)
		first.ClientRequestId = key
		_, err := rep.Design().FlattenEditLayer(ctx, first)
		require.NoError(t, err)

		before := pictures()
		_, err = rep.Design().FlattenEditLayer(ctx, fromLayer2(other.Id, key))
		require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
		require.Equal(t, before, pictures())
		require.False(t, probeReplacedBy(t, raw, other.Id).Valid, "второй лист не подписан")
	})
	t.Run("без ключа слой L2 подаёт своё", func(t *testing.T) {
		pic, err := rep.Design().FlattenEditLayer(ctx, fromLayer2(0, ""))
		require.NoError(t, err)
		require.EqualValues(t, layer2.Id, probeSourceLayer(t, raw, pic.Id).Int32)
		require.EqualValues(t, other.Id, pic.DerivedFrom.Int32)
	})
}
