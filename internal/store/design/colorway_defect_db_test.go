package design_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/product"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ДЕФЕКТОВ ОСИ КОЛОРВЕЯ, найденных адверсарным ревью (D1, D2, D4, D5, D6, D7, D9).
//
// У всех семи один и тот же корень: ярус, который ЗНАЕТ колорвей, не спрашивает его у того, кто
// колорвей держит, — и расхождение выходит МОЛЧАЛИВЫМ, потому что обе половины по отдельности
// законны. Поэтому и пробы здесь живые: каждая из них читает базу тем же путём, каким её читает
// сторож, и падает без правки, а не рядом с ней.
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там.

// draftColorway — колорвей карточки в статусе DRAFT: единственный, который вообще разрешено
// перепривязывать (entity.ErrColorwayNotDraft у всех прочих).
func draftColorway(t *testing.T, raw *sql.DB, cardID int, code string) int {
	t.Helper()
	id := probeColorway(t, raw, cardID, code)
	_, err := raw.Exec(`UPDATE product SET lifecycle_status = ? WHERE id = ?`,
		uint8(entity.ColorwayStatusDraft), id)
	require.NoError(t, err)
	return id
}

func styleLock(t *testing.T, raw *sql.DB, styleID int) int {
	t.Helper()
	var lv int
	require.NoError(t, raw.QueryRow(`SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&lv))
	return lv
}

// ─────────────────────── D1: перепривязка колорвея против полосы ───────────────────────

// КОЛОРВЕЙ НЕ УЕЗЖАЕТ НА ДРУГОЙ СТИЛЬ, ПОКА ЕГО НАЗЫВАЕТ ПОЛОСА ИСХОДНОЙ КАРТОЧКИ.
//
// ЧТО БЫЛО СЛОМАНО: RelinkDraftColorway меняет product.style_id и не трогает design_run,
// design_picture и design_bench_slot. После переезда карточка A оставалась с рядами, называющими
// колорвей карточки B, а незакрытый прогон продолжал штамповать на A новые кадры чужого
// колорвея. Ни один сторож не срабатывал: каждая строка по отдельности законна.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять вызов refuseRelinkWithDesignRows из RelinkDraftColorway.
func TestDesignDBRelinkRefusesAColorwayTheDesignBandStillNames(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	target := probeCard(t, raw)
	ctx := context.Background()

	// Три независимых держателя, каждый — своя таблица и свой FK. Проверяются все три, потому что
	// сторож обходит их по одному и пропуск любого не виден по остальным.
	// color_code — CHAR(3) и UNIQUE(style_id, color_code), поэтому коды раздаются вручную.
	for _, holder := range []struct {
		name string
		code string
		hold func(cw int) func()
	}{
		{"картинка", "BLK", func(cw int) func() {
			media := probeMedia(t, raw)
			batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
				TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
				Items: []entity.DesignUploadItem{
					{MediaId: media, Kind: entity.DesignPictureKindRender, ColorwayId: cw},
				},
			})
			require.NoError(t, err)
			return func() {
				_, _ = raw.Exec(`DELETE FROM design_picture WHERE id = ?`, batch.Pictures[0].Id)
			}
		}},
		{"слот верстака", "BLU", func(cw int) func() {
			media := probeMedia(t, raw)
			batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
				TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
				Items: []entity.DesignUploadItem{
					{MediaId: media, Kind: entity.DesignPictureKindRender, ColorwayId: cw},
				},
			})
			require.NoError(t, err)
			slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
				TechCardId: card,
				Slot: entity.DesignSlotRef{
					ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
					ColorwayId: entity.DesignColorwayRef(cw),
				},
				PictureId: batch.Pictures[0].Id, Actor: "probe",
			})
			require.NoError(t, err)
			return func() {
				_, _ = raw.Exec(`DELETE FROM design_bench_slot WHERE id = ?`, slot.Id)
				_, _ = raw.Exec(`DELETE FROM design_picture WHERE id = ?`, batch.Pictures[0].Id)
			}
		}},
		{"прогон", "GRN", func(cw int) func() {
			resetBudget(t, raw, "10.00")
			started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
				TechCardId: card, ClientRequestId: uuid.NewString(),
				Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
				PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
				ColorwayId:    cw,
			})
			require.NoError(t, err)
			return func() { _, _ = raw.Exec(`DELETE FROM design_run WHERE id = ?`, started.Run.Id) }
		}},
	} {
		t.Run(holder.name, func(t *testing.T) {
			cw := draftColorway(t, raw, card, holder.code)
			release := holder.hold(cw)

			err := rep.Products().RelinkDraftColorway(ctx, cw, target,
				styleLock(t, raw, card), styleLock(t, raw, target))
			require.ErrorIs(t, err, entity.ErrColorwayHasDesignRows,
				"колорвей, который называет полоса карточки, обязан быть ОТКАЗАН, а не увезён")

			// ОТКАЗ НИЧЕГО НЕ ТРОНУЛ: обе стороны стоят там же, где стояли.
			var styleAfter int
			require.NoError(t, raw.QueryRow(`SELECT style_id FROM product WHERE id = ?`, cw).Scan(&styleAfter))
			require.Equal(t, card, styleAfter, "отказ обязан быть без единой записи")

			// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: снимаем держателя — и отказывает уже НЕ этот сторож.
			// (Дальше по пути стоит ре-минт SKU, которому нужны сезонные факты цели; его исход
			// здесь не проверяется, проверяется только то, что дверь открыл именно наш сторож.)
			release()
			err = rep.Products().RelinkDraftColorway(ctx, cw, target,
				styleLock(t, raw, card), styleLock(t, raw, target))
			require.NotErrorIs(t, err, entity.ErrColorwayHasDesignRows,
				"без строк полосы этот сторож обязан молчать — иначе проба зелена не по своей причине")
		})
	}
}

// ─────────────────────── D2: колорвей рядом со slot_id ───────────────────────

// НАЗВАННЫЙ И НЕСОГЛАСНЫЙ КОЛОРВЕЙ ПРИ АДРЕСАЦИИ ПО id — ОТКАЗ, А НЕ МОЛЧАНИЕ.
//
// ЧТО БЫЛО СЛОМАНО: парсер выбрасывал колорвей у ссылки по slot_id, и сервер отвечал OK на
// просьбу, которой никто не исполнил — «положи в слот 7, он колорвея 5», где слот 7 флэтовый либо
// стоит на колорвее 6.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть в setBenchSlotTx безусловное `cw = rowCw` без блока
// req.Slot.ColorwayId.Stated().
func TestDesignDBSlotIdRefusesAContradictoryColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, flatPic, _ := designProbeCard(t, rep, raw)
	cwA, cwB := probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
	ctx := context.Background()

	flatSlot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot:       entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId:  flatPic, Actor: "probe",
	})
	require.NoError(t, err)

	picA := uploadRenderPlate(t, rep, raw, card, cwA)
	renderSlot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindRender,
			ColorwayId: entity.DesignColorwayRef(cwA),
		},
		PictureId: picA, Actor: "probe",
	})
	require.NoError(t, err)

	byID := func(slot int, cw entity.DesignColorwayRef, rev, pic int) error {
		_, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
			TechCardId:      card,
			Slot:            entity.DesignSlotRef{SlotId: slot, ColorwayId: cw},
			PictureId:       pic,
			ExpectedSlotRev: rev,
			Actor:           "probe",
		})
		return err
	}

	// У ФЛЭТОВОГО СЛОТА ОСИ НЕТ ПО СУЩЕСТВУ — назвать её значит просить невыразимого.
	require.ErrorIs(t, byID(flatSlot.Id, entity.DesignColorwayRef(cwA), flatSlot.SlotRev, flatPic),
		entity.ErrDesignColorwayForbidden)

	// У ОСНОГО — НЕСОГЛАСНЫЙ КОЛОРВЕЙ ЭТО mismatch, в обе стороны.
	require.ErrorIs(t, byID(renderSlot.Id, entity.DesignColorwayRef(cwB), renderSlot.SlotRev, picA),
		entity.ErrDesignColorwayMismatch)
	require.ErrorIs(t, byID(renderSlot.Id, entity.DesignColorwayUnattributed, renderSlot.SlotRev, picA),
		entity.ErrDesignColorwayMismatch,
		"явно названный безколорвейный верстак против слота колорвея — тоже противоречие")

	// А МОЛЧАНИЕ ПО-ПРЕЖНЕМУ ЗНАЧИТ «НЕ НАЗВАЛ»: сегодняшний клиент колорвея по id не шлёт, и
	// ложный отказ ему был бы дефектом ровно того же размера.
	require.NoError(t, byID(renderSlot.Id, 0, renderSlot.SlotRev, picA))
	require.NoError(t, byID(flatSlot.Id, 0, flatSlot.SlotRev, flatPic))
	// И СОГЛАСНЫЙ — тоже проходит: сторож отказывает противоречию, а не всякому названному.
	var rev int
	require.NoError(t, raw.QueryRow(`SELECT slot_rev FROM design_bench_slot WHERE id = ?`,
		renderSlot.Id).Scan(&rev))
	require.NoError(t, byID(renderSlot.Id, entity.DesignColorwayRef(cwA), rev, picA))
}

// ─────────────────────── D4: загрузка с постановкой не подменяет адрес ───────────────────────

// НАЗВАННЫЙ АДРЕС ЕДЕТ КАК НАЗВАН — составная дверь отказывает ровно там же, где прямая.
//
// ЧТО БЫЛО СЛОМАНО: в RegisterBatch цель с ColorwayId == 0 переписывалась колорвеем самой
// картинки. Ноль при этом значил ДВА разных намерения, и второе — «я назвал БЕЗКОЛОРВЕЙНЫЙ
// верстак» — молча превращалось в первое: загрузка рендера колорвея 5 с явным адресом
// неатрибутированного верстака отвечала OK и клала кадр в верстак 5. Прямая дверь на том же
// запросе отказывает colorway_mismatch'ем; разошлись бы они навсегда, потому что у кадра и слота
// колорвеи СОВПАДАЛИ по построению и ни один сторож ниже сработать не мог.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть `if target.ColorwayId == 0` вместо
// `if !target.ColorwayId.Stated()`.
func TestDesignDBUploadAndPlaceRefusesInsteadOfRedirecting(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	register := func(target entity.DesignSlotRef) (*entity.DesignBatchResult, error) {
		tgt := target
		return rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
			TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
			Items: []entity.DesignUploadItem{
				{MediaId: probeMedia(t, raw), Kind: entity.DesignPictureKindRender, ColorwayId: cw},
			},
			Target: &tgt,
		})
	}

	// ЯВНО НАЗВАННЫЙ БЕЗКОЛОРВЕЙНЫЙ ВЕРСТАК ПРОТИВ КАДРА КОЛОРВЕЯ — ОТКАЗ.
	_, err := register(entity.DesignSlotRef{
		ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
		ColorwayId: entity.DesignColorwayUnattributed,
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayMismatch,
		"адрес назван вслух — составная дверь обязана отказать так же, как прямая")

	// А МОЛЧАНИЕ ПО-ПРЕЖНЕМУ ПОДСТАВЛЯЕТ КОЛОРВЕЙ ФАЙЛА: жест «положи ЭТОТ файл на ЭТУ сторону»
	// колорвея не называет вовсе, и отказывать ему значило бы закрыть главную дверь волны.
	out, err := register(entity.DesignSlotRef{
		ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Slot)
	require.Equal(t, cw, entity.DesignColorwayOrNone(out.Slot.ColorwayId),
		"не названный адрес по-прежнему наследует колорвей самого файла")
}

// ─────────────────────── D6: воркер не минтует флэт с колорвеем ───────────────────────

// РОД ОТВЕТА ПРОТИВ ОСИ ЗАДАНИЯ — ОТКАЗ, А НЕ ТИХОЕ ОБНУЛЕНИЕ.
//
// ЧТО БЫЛО СЛОМАНО: CompleteRun проверял, что род ответа есть в словаре, и НЕЗАВИСИМО копировал
// колорвей строки. Прогон рендера колорвея 5, чей воркер вернул Kind="flat", записывал ФЛЭТ С
// КОЛОРВЕЕМ 5 — состояние, которое все три двери записи объявляют невыразимым. Обход всех
// сторожей разом, и притом с чёрного хода.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять блок DesignPictureKindTakesColorway из цикла CompleteRun.
func TestDesignDBCompleteRunRefusesAKindThatCannotCarryTheRunsColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")
	ctx := context.Background()

	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    cw,
	})
	require.NoError(t, err)
	claimed, err := rep.Design().ClaimRuns(ctx, 1, time.Minute, uuid.NewString())
	require.NoError(t, err)
	require.Len(t, claimed, 1)

	_, err = rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		Outputs: []entity.DesignPictureInsert{
			{MediaId: probeMedia(t, raw), Ordinal: 0, Kind: entity.DesignPictureKindFlat},
		},
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"флэт с колорвеем 5 не должен быть выразим и через воркера тоже")

	// НИ ОДНОЙ СТРОКИ НЕ РОДИЛОСЬ, и прогон не закрыт: отказ обязан быть целиком.
	var pics, done int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_picture WHERE run_id = ?`,
		claimed[0].Id).Scan(&pics))
	require.Zero(t, pics)
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM design_run WHERE id = ? AND status = 'done'`,
		claimed[0].Id).Scan(&done))
	require.Zero(t, done)

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: тот же прогон с законным родом закрывается и наследует колорвей.
	filed, err := rep.Design().CompleteRun(ctx, entity.DesignRunComplete{
		RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		Outputs: []entity.DesignPictureInsert{
			{MediaId: probeMedia(t, raw), Ordinal: 0, Kind: entity.DesignPictureKindRender},
		},
	})
	require.NoError(t, err)
	require.Len(t, filed.Pictures, 1)
	require.Equal(t, cw, entity.DesignColorwayOrNone(filed.Pictures[0].ColorwayId))
	_ = started
}

// ─────────────────────── D7: чей колорвей наследует флэттен ───────────────────────

// ФЛЭТТЕН СПРАШИВАЕТ СЛОЙ, А НЕ ПОРЯДОК ВСТАВКИ.
//
// ЧТО БЫЛО СЛОМАНО: родителя искали по паре (карточка, base_media_id) и брали ПЕРВУЮ строку по
// id, игнорируя хранимый source_picture_id. Один файл законно регистрируется на карточке дважды с
// разными колорвеями (тот же мультивью, залитый на другой цвет), и флэттен наследовал колорвей
// произвольной плиты — то есть уезжал в чужой верстак по броску монеты.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: (а) убрать ветку layer.SourcePictureId из FlattenEditLayer — вторая
// половина пробы получит колорвей чужой плиты; (б) убрать проверку расхождения — первая половина
// перестанет отказывать.
func TestDesignDBFlattenPrefersTheLayersOwnSourcePicture(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	strokes := json.RawMessage(`[{"d":"M0 0 L1 1"}]`)

	// ⚠ У КАЖДОЙ ПОЛОВИНЫ СВОЯ КАРТОЧКА, И ЭТО НЕ ГИГИЕНА, А ОГРАНИЧЕНИЕ СХЕМЫ:
	// uq_design_edit_layer_base держит ОДИН слой на пару (карточка, base_media_id), а обеим
	// половинам нужен слой над ОДНИМ И ТЕМ ЖЕ неоднозначным файлом.
	//
	// ambiguous — «один файл, две регистрации, два колорвея»: состояние, которое ось сделала
	// законным и которое и превратило «первую строку по id» в бросок монеты.
	ambiguous := func(t *testing.T) (card, shared, first, second, cwA, cwB int) {
		t.Helper()
		card, _, _ = designProbeCard(t, rep, raw)
		cwA, cwB = probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
		shared = probeMedia(t, raw)
		file := func(cw int) int {
			batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
				TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
				Items: []entity.DesignUploadItem{
					{MediaId: shared, Kind: entity.DesignPictureKindRender, ColorwayId: cw},
				},
			})
			require.NoError(t, err)
			return batch.Pictures[0].Id
		}
		first, second = file(cwA), file(cwB)
		require.Less(t, first, second, "первая по id — плита колорвея A; именно её брала догадка")
		return
	}

	// (а) СЛОЙ БЕЗ source_picture_id НА НЕОДНОЗНАЧНОМ ФАЙЛЕ — ОТКАЗ, А НЕ ДОГАДКА.
	t.Run("слепой слой отказывает", func(t *testing.T) {
		card, shared, _, _, _, _ := ambiguous(t)
		blind, err := rep.Design().SaveEditLayer(ctx, entity.DesignEditLayerSave{
			TechCardId: card, BaseMediaId: shared, Strokes: strokes, Actor: "probe",
		})
		require.NoError(t, err)
		_, err = rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
			TechCardId: card, LayerId: blind.Id, ExpectedRev: blind.Rev,
			MediaId: probeMedia(t, raw), Actor: "probe",
		})
		require.ErrorIs(t, err, entity.ErrDesignAmbiguousFlattenBase,
			"файл называет два колорвея, слой не назвал ни одного — выбирать нечем и выбирать нельзя")
	})

	// (б) СЛОЙ, НАЗВАВШИЙ ПЛИТУ, НАСЛЕДУЕТ ЕЁ КОЛОРВЕЙ — и это ВТОРАЯ плита, не первая по id.
	t.Run("названная плита выигрывает у первой по id", func(t *testing.T) {
		card, shared, _, second, _, cwB := ambiguous(t)
		named, err := rep.Design().ImportVector(ctx, entity.DesignVectorImport{
			TechCardId: card, ClientRequestId: uuid.NewString(),
			SourceMediaId: probeMedia(t, raw), SourcePictureId: second,
			Origin: entity.DesignLayerOriginVectorised, BaseMediaId: shared,
			Strokes: strokes, Actor: "probe",
		})
		require.NoError(t, err)
		flat, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
			TechCardId: card, LayerId: named.Id, ExpectedRev: named.Rev,
			MediaId: probeMedia(t, raw), Actor: "probe",
		})
		require.NoError(t, err)
		require.Equal(t, cwB, entity.DesignColorwayOrNone(flat.ColorwayId),
			"флэттен наследует колорвей НАЗВАННОЙ плиты, а не первой по id")
		require.Equal(t, second, int(flat.DerivedFrom.Int32),
			"и родословную — оттуда же: колорвей и derived_from обязаны называть ОДНУ плиту")
	})
}

// ─────────────────────── D9: ключ повтора связан с колорвеем ───────────────────────

// ТОТ ЖЕ КЛЮЧ С ДРУГИМ КОЛОРВЕЕМ — ПРОТИВОРЕЧИЕ, А НЕ ПОВТОР.
//
// ЧТО БЫЛО СЛОМАНО: client_request_id разрешался по одной только карточке, а вставка идёт с
// ON DUPLICATE KEY UPDATE id = LAST_INSERT_ID(id) и колонок не трогает. Повтор ключа со сменённым
// колорвеем возвращал СТАРУЮ строку и OK: клиент видел «загружено/запущено», сервер хранил
// прежний цвет, и разошлись бы они молча и навсегда.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: снять блок `if out.Idempotent` из RegisterBatch (первая половина) либо
// сравнение колорвея из ветки повтора StartRun (вторая).
func TestDesignDBIdempotencyKeysAreBoundToTheColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cwA, cwB := probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
	ctx := context.Background()

	// ─── ЗАГРУЗКА ───
	media := probeMedia(t, raw)
	upload := func(req string, cw int) (*entity.DesignBatchResult, error) {
		return rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
			TechCardId: card, ClientRequestId: req, Actor: "probe",
			Items: []entity.DesignUploadItem{
				{MediaId: media, Kind: entity.DesignPictureKindRender, ColorwayId: cw},
			},
		})
	}
	req := uuid.NewString()
	firstBatch, err := upload(req, cwA)
	require.NoError(t, err)
	require.False(t, firstBatch.Idempotent)

	_, err = upload(req, cwB)
	require.ErrorIs(t, err, entity.ErrDesignColorwayMismatch,
		"повтор ключа с другим колорвеем — другой запрос, и ему полагается отказ")

	// НАСТОЯЩИЙ ПОВТОР ПО-ПРЕЖНЕМУ ИДЕМПОТЕНТЕН: сторож бьёт по противоречию, а не по ретраю.
	same, err := upload(req, cwA)
	require.NoError(t, err)
	require.True(t, same.Idempotent)
	require.Equal(t, firstBatch.Pictures[0].Id, same.Pictures[0].Id)
	require.Equal(t, cwA, entity.DesignColorwayOrNone(same.Pictures[0].ColorwayId),
		"и колорвей строки остался тем, каким записан")

	// ─── ПРОГОН ───
	resetBudget(t, raw, "10.00")
	// Params замораживают ПРОСЬБУ ровно так, как их пишет хендлер: различитель повтора читает
	// именно её (F7), и стенд без params проверял бы путь, которым живой вызов не ходит.
	start := func(reqID string, cw int) (*entity.DesignRunStarted, error) {
		return rep.Design().StartRun(ctx, entity.DesignRunStart{
			TechCardId: card, ClientRequestId: reqID,
			Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
			PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
			ColorwayId:    cw, ColorwayStated: true,
			Params: json.RawMessage(fmt.Sprintf(`{"colorway_id":%d}`, cw)),
		})
	}
	runReq := uuid.NewString()
	firstRun, err := start(runReq, cwA)
	require.NoError(t, err)

	_, err = start(runReq, cwB)
	require.ErrorIs(t, err, entity.ErrDesignColorwayMismatch,
		"3D/рендер колорвея A и колорвея B — два разных прогона и двое разных денег")

	repeat, err := start(runReq, cwA)
	require.NoError(t, err)
	require.True(t, repeat.Idempotent)
	require.Equal(t, firstRun.Run.Id, repeat.Run.Id)
}

// ─────────────────────── F1: удаление колорвея НАЗЫВАЕТ полосу ───────────────────────

// ОПЕРАТОР ВИДИТ ПОЛОСУ DESIGN В ВЕРДИКТЕ, КОТОРЫЙ ПОДПИСЫВАЕТ.
//
// ЧТО БЫЛО СЛОМАНО: перечисление фактов удаления знало 24 вида ссылок и НИ ОДНОГО из полосы.
// Диалог показывал медиа, теги и переводы, молчал про верстак и кадры, оператор соглашался — и
// весь рендер-верстак колорвея уходил каскадом, а оплаченные рендеры становились
// неатрибутированными. Сетка безопасности 1451 этого не ловит по построению: она видит только
// RESTRICT, а все три FK полосы — SET NULL и CASCADE.
//
// ПОЧЕМУ НЕ БЛОКЕР, А НАЗВАННЫЕ ПОСЛЕДСТВИЯ. Граница владельца у удаления — «удаляем то, чего
// НИКОГДА НЕ БЫЛО» (не продан, не в партии, не в настиле, нет остатка). Первое, что делают с
// новым колорвеем, — генерят ему рендер; сделав полосу блокером, мы закрыли бы удаление ровно у
// тех черновиков, ради которых фича написана, и вернули бы список, зарастающий опечатками. Сама
// 0356 выбрала SET NULL/CASCADE ИМЕННО ЧТОБЫ колорвей оставался удаляемым («RESTRICT сделал бы
// полосу причиной, по которой колорвей нельзя удалить»). Асимметрия с перепривязкой (D1, где
// ОТКАЗ) настоящая и названа: перенос оставил бы на чужой карточке ЛОЖНУЮ атрибуцию, которую
// потом не отличить от правды, а удаление оставляет ЧЕСТНОЕ «не знаем» — и оператор про него
// предупреждён.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать любой из трёх подсчётов из readColorwayDeletionFacts.
func TestDesignDBColorwayDeletionVerdictNamesTheDesignBand(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")
	ctx := context.Background()

	pic := uploadRenderPlate(t, rep, raw, card, cw)
	_, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
			ColorwayId: entity.DesignColorwayRef(cw),
		},
		PictureId: pic, Actor: "probe",
	})
	require.NoError(t, err)
	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    cw, ColorwayStated: true,
	})
	require.NoError(t, err)
	// ⚠ ПРОГОН ДОВОДИТСЯ ДО ТЕРМИНАЛЬНОГО СОСТОЯНИЯ, И ЭТО РАЗДЕЛЕНИЕ ДВУХ ПРАВИЛ, А НЕ ПОДГОНКА.
	// Живой прогон теперь ЗАПРЕЩАЕТ удаление (N3) — у этого есть своя проба. Здесь предмет другой:
	// ЗАКОНЧЕННАЯ история удалению не мешает, но обязана быть НАЗВАНА в вердикте. Оставить прогон
	// живым значило бы проверять здесь блокер и ничего не сказать про именование.
	_, err = raw.Exec(`UPDATE design_run SET status = 'done', claim_token = NULL WHERE id = ?`,
		started.Run.Id)
	require.NoError(t, err)

	verdict, err := rep.Products().EvaluateColorwayDeletion(ctx, cw)
	require.NoError(t, err)
	require.True(t, verdict.Deletable,
		"законченная полоса не блокирует удаление — она обязана быть НАЗВАНА, а не запретить")

	reasons := func(list []entity.ColorwayDeletionEntry) map[string]int {
		m := map[string]int{}
		for _, e := range list {
			m[e.Reason] = e.Count
		}
		return m
	}
	cascade, orphans := reasons(verdict.Cascade), reasons(verdict.Orphans)
	require.Equal(t, 1, cascade[entity.ColorwayCascadeDesignBenchSlot],
		"верстак — адрес колорвея и уходит с ним; оператор обязан это увидеть")
	require.Equal(t, 1, orphans[entity.ColorwayOrphanDesignPicture],
		"оплаченный кадр переживёт удаление и станет неатрибутированным — необратимо и невидимо")
	require.Equal(t, 1, orphans[entity.ColorwayOrphanDesignRun],
		"строка истории потеряет колорвей, который её замороженные params продолжают называть")
}

// ─────────────────────── F2/F7: полоса переживает смерть колорвея ───────────────────────

// УНАСЛЕДОВАННЫЙ КОЛОРВЕЙ, КОТОРОГО БОЛЬШЕ НЕТ, НЕ ДЕЛАЕТ ПРОГОН НЕПОВТОРИМЫМ.
//
// ЧТО БЫЛО СЛОМАНО (F2): реран без параметров наследует колорвей из замороженных params, стор
// проверял его строго, и после удаления колорвея такой прогон отказывался `foreign_colorway`
// ВЕЧНО — притом клиенту, который колорвея не называл и написать иначе не мог.
//
// И ВТОРАЯ ПОЛОВИНА (F7): различитель повтора смотрел на ЖИВУЮ колонку, которую тот же FK гасит в
// NULL, — так что настоящий ретрай того же запроса после удаления получал `colorway_mismatch`
// «уже открыт для 0, просят 7». Оба дефекта — один корень: живое зеркало спутали с записью
// просьбы.
//
// МУТАЦИИ, КОТОРЫЕ ЛОВИТ: (а) вернуть безусловный assertColorwayOfCard в StartRun; (б) вернуть
// различителю `prior.ColorwayId` вместо parseRunParams(prior.Params).ColorwayId.
func TestDesignDBRunSurvivesTheDeletionOfItsColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")
	ctx := context.Background()

	req := uuid.NewString()
	start := func(reqID string, stated bool) (*entity.DesignRunStarted, error) {
		return rep.Design().StartRun(ctx, entity.DesignRunStart{
			TechCardId: card, ClientRequestId: reqID,
			Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
			PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
			ColorwayId:    cw, ColorwayStated: stated,
			// Замороженные params несут просьбу — ровно то, что переживает удаление колонки.
			Params: json.RawMessage(fmt.Sprintf(`{"colorway_id":%d}`, cw)),
		})
	}
	first, err := start(req, true)
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(first.Run.ColorwayId))

	// КОЛОРВЕЙ УМИРАЕТ. FK гасит колонку прогона; замороженные params продолжают называть id.
	_, err = raw.Exec(`DELETE FROM product WHERE id = ?`, cw)
	require.NoError(t, err)
	var live sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT colorway_id FROM design_run WHERE id = ?`, first.Run.Id).Scan(&live))
	require.False(t, live.Valid, "SET NULL гасит колонку — на этом и держатся оба дефекта")

	// (F7) НАСТОЯЩИЙ РЕТРАЙ ТОГО ЖЕ ЗАПРОСА ПО-ПРЕЖНЕМУ ИДЕМПОТЕНТЕН.
	same, err := start(req, true)
	require.NoError(t, err, "ключ идемпотентности не должен ломаться от удаления колорвея")
	require.True(t, same.Idempotent)
	require.Equal(t, first.Run.Id, same.Run.Id)

	// (F2) РЕРАН С УНАСЛЕДОВАННЫМ КОЛОРВЕЕМ ПРОХОДИТ И ЕДЕТ НЕАТРИБУТИРОВАННЫМ — ровно в той же
	// паре (колонка пуста, params помнят), в которой оказался его родитель.
	rerun, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    cw, ColorwayStated: false, RerunOf: first.Run.Id,
		Params: json.RawMessage(fmt.Sprintf(`{"colorway_id":%d}`, cw)),
	})
	require.NoError(t, err, "унаследованный колорвей не должен делать прогон неповторимым навсегда")
	require.Zero(t, entity.DesignColorwayOrNone(rerun.Run.ColorwayId),
		"деградация до неатрибутированного — ровно то, что FK сделал с родителем")

	// А НАЗВАННЫЙ КЛИЕНТОМ — по-прежнему отказывается: за него ручались.
	_, err = rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    cw, ColorwayStated: true,
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignColorway,
		"кто назвал колорвей сам — тот за него и отвечает")
}

// ─────────────────────── N1: сторож перепривязки знает ВСЕ таблицы ───────────────────────

// СПИСОК ДЕРЖАТЕЛЕЙ РАВЕН СХЕМЕ, И ЭТО ПРОВЕРЯЕТСЯ, А НЕ ОБЕЩАЕТСЯ.
//
// ЧТО БЫЛО СЛОМАНО: 0356 завела три ссылки на product(id), 0357 — четвёртую (design_asset), и
// сторож перепривязки о ней не узнал. Прошлая проба перечисляла держателей РУКАМИ, поэтому она
// осталась зелёной, пока новый держатель проходил мимо, — второе перечисление одного факта,
// расходящееся молча.
//
// Эта проба спрашивает СХЕМУ: все FK из таблиц полосы на product(id) обязаны быть в списке
// сторожа. Пятый держатель провалит её в тот же день, когда появится, без чьей-либо памяти.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать design_asset из designColorwayHolders.
func TestDesignDBRelinkGuardCoversEveryColorwayHolder(t *testing.T) {
	_, raw := probeRepository(t)

	// ⚠ ПАРА (ТАБЛИЦА, КОЛОНКА), А НЕ ИМЯ ТАБЛИЦЫ (T8): вторая колонка на уже известной таблице
	// полосы при сверке по именам прошла бы незамеченной — множество имён совпало бы, а держатель
	// остался бы вне сторожа.
	//
	// ⚠ И ОСТАТОЧНАЯ ГРАНИЦА, НАЗВАННАЯ ВСЛУХ: скоуп задан ПРЕФИКСОМ `design_`. Держатель полосы,
	// названный иначе, этой пробой не увидится. Снять префикс нельзя — тогда в множество попадут
	// все ссылки на product(id) из доменов товара и производства (order_item, product_size,
	// tech_card_colorway_usage, production_run_lay…), которых сторож перепривязки не касается и
	// касаться не должен. То есть полнота здесь держится на СОГЛАШЕНИИ ОБ ИМЕНОВАНИИ таблиц
	// полосы, и это единственное звено проверки, за которым не стоит машина.
	rows, err := raw.Query(`
		SELECT DISTINCT k.TABLE_NAME, k.COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE k
		WHERE k.TABLE_SCHEMA = DATABASE()
		  AND k.REFERENCED_TABLE_NAME = 'product'
		  AND k.REFERENCED_COLUMN_NAME = 'id'
		  AND k.TABLE_NAME LIKE 'design\_%'`)
	require.NoError(t, err)
	defer rows.Close()
	schema := [][2]string{}
	for rows.Next() {
		var table, column string
		require.NoError(t, rows.Scan(&table, &column))
		schema = append(schema, [2]string{table, column})
	}
	require.NoError(t, rows.Err())
	require.NotEmpty(t, schema, "положительный контроль: запрос обязан ВИДЕТЬ ссылки, иначе он зелен пустотой")

	require.ElementsMatch(t, schema, product.DesignColorwayHolderColumns(),
		"каждая ссылка полосы на колорвей обязана быть у сторожа перепривязки — иначе колорвей "+
			"уедет на чужой стиль, оставив здесь строки, которые его называют")

	// И ВТОРОЕ ПЕРЕЧИСЛЕНИЕ ТОГО ЖЕ ФАКТА — ВЕРДИКТ УДАЛЕНИЯ (T3). Сторож перепривязки и диалог
	// удаления читают ОДИН набор таблиц, но до этой проверки перечисляли его порознь: схемная
	// проба смотрела только на первый, поэтому будущая 0358 покраснила бы её, кто-то дописал бы
	// таблицу в список — и удаление продолжило бы молчать про новые строки. Сетка 1451 их не
	// ловит: SET NULL и CASCADE она не видит вовсе.
	require.ElementsMatch(t, schema, product.DesignColorwayDeletionCountedColumns(),
		"каждая ссылка полосы обязана быть ПОСЧИТАНА вердиктом удаления и названа оператору")
}

// И САМ ЧЕТВЁРТЫЙ ДЕРЖАТЕЛЬ ДЕЙСТВИТЕЛЬНО ДЕРЖИТ.
func TestDesignDBRelinkRefusesAColorwayWornByAShelfAsset(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	target := probeCard(t, raw)
	cw := draftColorway(t, raw, card, "BLK")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "main jersey")
	_, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: asset.Id, ColorwayId: cw, Actor: "probe",
	})
	require.NoError(t, err)

	err = rep.Products().RelinkDraftColorway(ctx, cw, target,
		styleLock(t, raw, card), styleLock(t, raw, target))
	require.ErrorIs(t, err, entity.ErrColorwayHasDesignRows,
		"ассет, назначенный тканью колорвея, держит его на этой карточке")
}

// ─────────────────────── N3: живая оплаченная работа БЛОКИРУЕТ удаление ───────────────────────

// ЗАКОНЧЕННАЯ ИСТОРИЯ — ПРЕДУПРЕЖДЕНИЕ, ЖИВОЕ ЗАДАНИЕ — ЗАПРЕТ.
//
// ЧТО БЫЛО СЛОМАНО: вердикт считал ВСЕ прогоны одной кучей и звал их сиротами. Но pending/running
// — это оплаченная работа, которая ЕЩЁ СДЕЛАЕТ своё: воркер закроет её после удаления, кадры
// родятся с colorway_id = NULL, и на карточке появятся НОВЫЕ неатрибутированные кадры, которых не
// было в списке, подписанном оператором. Верстак к тому моменту уже уйдёт каскадом.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать фильтр статусов из подсчёта design_runs_live.
func TestDesignDBLiveDesignRunBlocksColorwayDeletion(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")
	ctx := context.Background()

	started, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1, Author: "probe",
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		ColorwayId:    cw, ColorwayStated: true,
	})
	require.NoError(t, err)

	verdict, err := rep.Products().EvaluateColorwayDeletion(ctx, cw)
	require.NoError(t, err)
	require.False(t, verdict.Deletable,
		"пока задание идёт, удалять цвет нельзя: оно допишет кадры уже после согласия оператора")
	var blocked int
	for _, b := range verdict.Blockers {
		if b.Reason == entity.ColorwayBlockerDesignRunLive {
			blocked = b.Count
		}
	}
	require.Equal(t, 1, blocked, "блокер обязан быть НАЗВАН и посчитан")

	// ⚠ И САМ ГЛАГОЛ УДАЛЕНИЯ, А НЕ ТОЛЬКО СУХОЙ ПРОГОН (T6). Обе проверки выше спрашивают
	// EvaluateColorwayDeletion; название пробы обещает, что живой прогон БЛОКИРУЕТ УДАЛЕНИЕ, и
	// доказать это может только DeleteColorway. Сегодня они делят один предикат
	// (readColorwayDeletionFacts + ClassifyColorwayDeletion), но именно поэтому расхождение — то,
	// что случится однажды и молча: сухой прогон продолжит отказывать, а глагол удалит.
	_, err = rep.Products().DeleteColorway(ctx, cw)
	require.ErrorIs(t, err, entity.ErrColorwayNotDeletable,
		"глагол удаления обязан отказать тем же правилом, каким отказал сухой прогон")
	var alive int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM product WHERE id = ?`, cw).Scan(&alive))
	require.Equal(t, 1, alive, "отказ обязан быть без единой записи")

	// А ЗАКОНЧЕННЫЙ ПРОГОН — снова только предупреждение: терминальная история ничего не допишет.
	_, err = raw.Exec(`UPDATE design_run SET status = 'cancelled', claim_token = NULL WHERE id = ?`,
		started.Run.Id)
	require.NoError(t, err)
	verdict, err = rep.Products().EvaluateColorwayDeletion(ctx, cw)
	require.NoError(t, err)
	require.True(t, verdict.Deletable, "законченная история удалению не мешает")
	var orphaned int
	for _, o := range verdict.Orphans {
		if o.Reason == entity.ColorwayOrphanDesignRun {
			orphaned = o.Count
		}
	}
	require.Equal(t, 1, orphaned, "но названа она обязана быть")
}

// ─────────────────────── N2: Upsert не превращает ткань в фурнитуру ───────────────────────

// ЗАПРЕТ ОБЯЗАН ДЕЙСТВОВАТЬ ЧЕРЕЗ ВСЕ ДВЕРИ, А НЕ ЧЕРЕЗ ОДНУ.
//
// SetAssetColorway отказывает назначить колорвей фурнитуре — а Upsert менял РОД, сохраняя
// колорвей, и тот же запретный конец достигался с другой стороны.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: снять сторож смены рода из UpsertAsset.
func TestDesignDBUpsertCannotTurnAnAssignedFabricIntoHardware(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	asset := probeAsset(t, rep, card, "main jersey")
	_, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: asset.Id, ColorwayId: cw, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, AssetId: asset.Id,
		Kind: entity.DesignAssetKindHardware, Name: "zip", Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"фурнитура с колорвеем невыразима через выделенный глагол — значит и через Upsert тоже")

	// И СОСТОЯНИЕ НЕ ТРОНУТО: отказ обязан быть целиком.
	var kind string
	var colorway sql.NullInt32
	require.NoError(t, raw.QueryRow(`SELECT kind, colorway_id FROM design_asset WHERE id = ?`, asset.Id).
		Scan(&kind, &colorway))
	require.Equal(t, entity.DesignAssetKindFabric, kind)
	require.Equal(t, cw, entity.DesignColorwayOrNone(colorway))

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ: сняли ткань с цвета — смена рода проходит.
	_, err = rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: asset.Id, ColorwayId: 0, Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().UpsertAsset(ctx, entity.DesignAssetUpsert{
		TechCardId: card, AssetId: asset.Id,
		Kind: entity.DesignAssetKindHardware, Name: "zip", Actor: "probe",
	})
	require.NoError(t, err, "сторож бьёт по НАЗНАЧЕННОЙ ткани, а не по всякой смене рода")
}

// ─────────────────────── N9: ключ делает две ткани одного цвета невыразимыми ───────────────────────

// ПОЯС СХЕМЫ, А НЕ ТОЛЬКО СТОРОЖ В GO.
//
// Кража остаётся механизмом, но инвариант «одна ткань на колорвей» теперь ещё и НЕ ЗАПИСЫВАЕТСЯ:
// прямая вставка мимо стора обязана падать на uq_design_asset_colorway. Это и есть разница между
// «проверяется пробой» и «не может быть записано».
func TestDesignDBAssetColorwayUniquenessIsInTheSchema(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	ctx := context.Background()

	first := probeAsset(t, rep, card, "main jersey")
	second := probeAsset(t, rep, card, "contrast rib")
	_, err := rep.Design().SetAssetColorway(ctx, entity.DesignAssetColorwaySet{
		TechCardId: card, AssetId: first.Id, ColorwayId: cw, Actor: "probe",
	})
	require.NoError(t, err)

	// Мимо стора — прямым UPDATE, то есть тем писателем, который про кражу не знает.
	//
	// ⚠ ПРОВЕРЯЕТСЯ 1062 И ИМЯ КЛЮЧА, А НЕ ПРОСТО «БЫЛА ОШИБКА» (T8). Голый require.Error зелен и
	// в мире, где колонки нет вовсе («Unknown column»), — то есть проба, чей ВЕСЬ смысл в разнице
	// между «стережёт проба» и «нельзя записать», проходила бы там, где записывать нечего.
	_, err = raw.Exec(`UPDATE design_asset SET colorway_id = ? WHERE id = ?`, cw, second.Id)
	require.Error(t, err)
	var me *mysql.MySQLError
	require.ErrorAs(t, err, &me)
	require.EqualValues(t, 1062, me.Number, "отказ обязан быть нарушением УНИКАЛЬНОСТИ: %v", err)
	require.Contains(t, me.Message, "uq_design_asset_colorway",
		"и именно того ключа, который эту единственность и держит")

	// А НИЧЬИХ АССЕТОВ КЛЮЧ НЕ ОГРАНИЧИВАЕТ — и это ровно то, что от него требуется: NULL != NULL.
	third := probeAsset(t, rep, card, "lining")
	require.Zero(t, entity.DesignColorwayOrNone(third.ColorwayId))
	fourth := probeAsset(t, rep, card, "interlining")
	require.Zero(t, entity.DesignColorwayOrNone(fourth.ColorwayId))
}

// ─────────────────────── N6: 3D не деградирует, оно отказывает ───────────────────────

// ДЕГРАДАЦИЯ ЧЕСТНА ДЛЯ РЕНДЕРА И ЛОЖНА ДЛЯ 3D.
//
// У рендера колорвей — АТРИБУЦИЯ: прогон уезжает неатрибутированным, замороженные params помнят
// просимый id, и ничего другого строка о цвете не утверждала. У 3D колорвей ВЫБИРАЕТ ВЕРСТАК, и к
// моменту записи снимок входов уже заморожен против верстака названного цвета. Обнулить колонку
// здесь значит записать строку, чьи inputs описывают верстак 5, а колонка — безколорвейный, и
// выход лёг бы на ЧУЖОЙ верстак.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: убрать DesignRunKindReadsColorwayBench из условия деградации — 3D снова
// начнёт уезжать неатрибутированным.
func TestDesignDBThreedRefusesADeletedColorwayInsteadOfDegrading(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")
	ctx := context.Background()

	inherited := func(kind string) error {
		_, err := rep.Design().StartRun(ctx, entity.DesignRunStart{
			TechCardId: card, ClientRequestId: uuid.NewString(),
			Kind: kind, RequestedOutputs: 1, Author: "probe",
			PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
			ColorwayId:    cw, ColorwayStated: false,
			Params: json.RawMessage(fmt.Sprintf(`{"colorway_id":%d}`, cw)),
		})
		return err
	}

	// Пока колорвей жив — оба рода проходят (положительный контроль).
	require.NoError(t, inherited(entity.DesignRunKindRender))
	require.NoError(t, inherited(entity.DesignRunKindThreed))

	_, err := raw.Exec(`DELETE FROM product WHERE id = ?`, cw)
	require.NoError(t, err)

	// РЕНДЕР ДЕГРАДИРУЕТ — его реран обязан оставаться возможным навсегда.
	require.NoError(t, inherited(entity.DesignRunKindRender),
		"у рендера колорвей — атрибуция, и её потеря честна")

	// 3D ОТКАЗЫВАЕТ — его входы уже описывают верстак, которого нет.
	require.ErrorIs(t, inherited(entity.DesignRunKindThreed), entity.ErrDesignForeignColorway,
		"3D читает верстак колорвея; обнулить колонку значило бы соврать о том, из чего собран прогон")

	// И НИ ОДНОЙ СТРОКИ 3D НЕ РОДИЛОСЬ ПОСЛЕ УДАЛЕНИЯ: отказ обязан быть до денег.
	var threed int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_run WHERE tech_card_id = ? AND kind = 'threed'`, card).Scan(&threed))
	require.Equal(t, 1, threed, "остался ровно тот 3D, что завёлся при живом колорвее")
}
