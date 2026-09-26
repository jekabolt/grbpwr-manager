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

// ═══ «ПЕРЕЗАПИСАТЬ» ПРАВКОЙ — 0368, O-53 ═══════════════════════════════════════════════════════
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

// СЛЕПОЙ ПОВТОР ПЕРЕЗАПИСИ ОТКАЗЫВАЕТСЯ ДО ВСТАВКИ.
//
// Флэттен без ключа идемпотентности: повтор «save as new» подаёт вторую правку (до-существующее,
// бэклог). Повтор ПЕРЕЗАПИСИ обязан быть другим — оригинал уже подписан, и сторож
// already_replaced стоит ДО вставки, поэтому второй правки нет, а слот не двигается второй раз.
//
// МУТАЦИЯ: проверять заменённость после вставки (или не проверять вовсе) — строк становится больше.
func TestDesignDBBlindOverwriteRetryFilesNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	p := newReplaceProbeSetup(t, rep, raw)

	req := p.overwrite(probeMedia(t, raw), p.sheet.Id)
	edit, err := rep.Design().FlattenEditLayer(ctx, req)
	require.NoError(t, err)
	before := countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card)
	_, revBefore, _ := probeSlotHolder(t, raw, p.slot.Id)

	_, err = rep.Design().FlattenEditLayer(ctx, req)
	require.ErrorIs(t, err, entity.ErrDesignAlreadyReplaced)
	require.Equal(t, before, countRows(t, raw, `SELECT COUNT(*) FROM design_picture WHERE tech_card_id = ?`, p.card),
		"повтор перезаписи не подаёт вторую правку")
	holder, revAfter, _ := probeSlotHolder(t, raw, p.slot.Id)
	require.EqualValues(t, edit.Id, holder.Int32)
	require.Equal(t, revBefore, revAfter, "отказанный повтор слот не трогает")
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
// Последний подслучай — положительный контроль фильтра «видимый»: спрятанный кусок лист не держит.
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
