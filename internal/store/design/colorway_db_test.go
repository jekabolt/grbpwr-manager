package design_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ЖИВЫЕ ПРОБЫ ОСИ КОЛОРВЕЯ (0356, L-2/L-3) — то, что чистые пробы admin/entity доказать не могут:
// сторожа setBenchSlotTx/RegisterBatch/StartRun читают базу в своей транзакции, и единственный
// способ доказать их — настоящая запись через настоящий стор.
//
// Запуск — тот же одноразовый контейнер, что у wave2_db_test.go (CI=1 + MYSQL_*), см. шапку там.

// probeColorway — колорвей карточки: строка product со style_id карточки (0138, слияние доменов
// 0151). Чистка регистрируется ПОСЛЕ probeCard, то есть исполняется РАНЬШЕ (LIFO) — style_id
// держит tech_card RESTRICT'ом, и продукт обязан уйти первым. code — из посеянной палитры 0130
// (color_code держит словарь RESTRICT'ом), и в пределах одной карточки коды обязаны различаться:
// UNIQUE(style_id, color_code).
func probeColorway(t *testing.T, raw *sql.DB, cardID int, code string) int {
	t.Helper()
	thumb := probeMedia(t, raw)
	res, err := raw.Exec(`INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'probe', ?, '#333333', 'US', ?, ?)`,
		"DSGCW-"+uuid.NewString()[:18], code, thumb, cardID)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = raw.Exec(`DELETE FROM product WHERE id = ?`, id) })
	return int(id)
}

// uploadRenderPlate — рендер-кадр колорвея cw (0 = неатрибутированный), заведённый настоящей
// регистрацией пачки — тем же путём, каким ходит хендлер.
func uploadRenderPlate(t *testing.T, rep dependency.Repository, raw *sql.DB, card, cw int) int {
	t.Helper()
	media := probeMedia(t, raw)
	batch, err := rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: media, Kind: entity.DesignPictureKindRender, ColorwayId: cw},
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.Pictures, 1)
	return batch.Pictures[0].Id
}

// ФЛЭТОВЫЙ ВЕРСТАК НЕ ПРИНИМАЕТ КОЛОРВЕЙ — «флэт с колорвеем» невыразим через дверь постановки
// (L-4). ОТКАЗ, а не молчаливый сброс: сброшенное значение — принятая и не исполненная просьба.
func TestDesignDBFlatBenchRefusesAColorway(t *testing.T) {
	rep, raw := probeRepository(t)
	card, picA, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")

	_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot:       entity.DesignSlotRef{ViewKey: entity.DesignViewFront, ColorwayId: entity.DesignColorwayRef(cw)},
		PictureId:  picA, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"флэтовый адрес с колорвеем обязан быть отказан, а не молча обнулён")

	// И загрузка флэта с колорвеем — тот же отказ у той же оси.
	media := probeMedia(t, raw)
	_, err = rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: media, Kind: entity.DesignPictureKindFlat, ColorwayId: cw},
		},
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden)
}

// ДВА КОЛОРВЕЯ ДЕРЖАТ ОДИН ВИД ОДНОВРЕМЕННО — сердце L-2. До оси второй колорвей ВЫТЕСНЯЛ бы
// первый с адреса front; теперь front колорвея A, front колорвея B и легаси-front — три живых
// слота под одним UNIQUE-ключом, потому что колорвей вошёл в exclusive_key.
func TestDesignDBTwoColorwaysHoldTheSameViewAtOnce(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cwA, cwB := probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
	picA := uploadRenderPlate(t, rep, raw, card, cwA)
	picB := uploadRenderPlate(t, rep, raw, card, cwB)
	picLegacy := uploadRenderPlate(t, rep, raw, card, 0)

	put := func(cw, pic int) *entity.DesignBenchSlot {
		slot, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
			TechCardId: card,
			Slot: entity.DesignSlotRef{
				ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender, ColorwayId: entity.DesignColorwayRef(cw),
			},
			PictureId: pic, Actor: "probe",
		})
		require.NoError(t, err)
		return slot
	}
	slotA := put(cwA, picA)
	slotB := put(cwB, picB)
	slotL := put(0, picLegacy)

	require.NotEqual(t, slotA.Id, slotB.Id, "front колорвея A и front колорвея B — два слота")
	require.NotEqual(t, slotA.Id, slotL.Id, "легаси-front — третий, безколорвейный")
	require.Equal(t, cwA, entity.DesignColorwayOrNone(slotA.ColorwayId))
	require.Equal(t, cwB, entity.DesignColorwayOrNone(slotB.ColorwayId))
	require.Zero(t, entity.DesignColorwayOrNone(slotL.ColorwayId))
	require.Equal(t, entity.DesignBenchExclusiveKey(entity.DesignViewFront, cwA), slotA.ExclusiveKey)
	require.Equal(t, entity.DesignViewFront, slotL.ExclusiveKey,
		"легаси-адрес остался байт в байт — старые слоты достижимы старым ключом")

	// И ПОВТОРНАЯ постановка в ТОТ ЖЕ колорвейный адрес — CAS той же строки, а не второй слот.
	displaced := uploadRenderPlate(t, rep, raw, card, cwA)
	again, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender, ColorwayId: entity.DesignColorwayRef(cwA),
		},
		PictureId: displaced, ExpectedSlotRev: slotA.SlotRev, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, slotA.Id, again.Id, "тот же адрес — та же строка")
	require.Equal(t, slotA.SlotRev+1, again.SlotRev)
}

// КОЛОРВЕЙ ПЛИТЫ ОБЯЗАН СОВПАСТЬ С КОЛОРВЕЕМ СЛОТА — в обе стороны, и «не назван» тоже значение:
// неатрибутированную плиту не принимает именованный верстак (атрибуцию постановкой не выдумывают),
// а именованную — безколорвейный (постановка не стирает атрибуцию).
func TestDesignDBPlateAndSlotColorwaysMustMatch(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cwA, cwB := probeColorway(t, raw, card, "BLK"), probeColorway(t, raw, card, "WHT")
	picA := uploadRenderPlate(t, rep, raw, card, cwA)
	picLegacy := uploadRenderPlate(t, rep, raw, card, 0)

	try := func(cw, pic int) error {
		_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
			TechCardId: card,
			Slot: entity.DesignSlotRef{
				ViewKey: entity.DesignViewBack, Kind: entity.DesignPictureKindRender, ColorwayId: entity.DesignColorwayRef(cw),
			},
			PictureId: pic, Actor: "probe",
		})
		return err
	}
	require.ErrorIs(t, try(cwB, picA), entity.ErrDesignColorwayMismatch,
		"рендер колорвея A в верстаке колорвея B печатал бы чужой цвет")
	require.ErrorIs(t, try(cwA, picLegacy), entity.ErrDesignColorwayMismatch,
		"неатрибутированная плита не атрибутируется постановкой")
	require.ErrorIs(t, try(0, picA), entity.ErrDesignColorwayMismatch,
		"именованная плита не теряет атрибуцию в легаси-верстаке")
	require.NoError(t, try(cwA, picA), "свой колорвей — свой верстак")
}

// ЧУЖОЙ КОЛОРВЕЙ — ОТКАЗ ГРАНИЦЫ КАРТОЧКИ у каждой из трёх дверей: постановка, загрузка, прогон.
func TestDesignDBForeignColorwayIsRefusedAtEveryDoor(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	otherCard := probeCard(t, raw)
	foreign := probeColorway(t, raw, otherCard, "BLK")

	_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender, ColorwayId: entity.DesignColorwayRef(foreign),
		},
		PictureId: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignColorway)

	media := probeMedia(t, raw)
	_, err = rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: media, Kind: entity.DesignPictureKindRender, ColorwayId: foreign},
		},
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignColorway)

	resetBudget(t, raw, "10.00")
	_, err = rep.Design().StartRun(context.Background(), entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1,
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Author:        "probe", ColorwayId: foreign,
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignColorway,
		"чужой колорвей не должен ни занять деньги дня, ни замёрзнуть в истории")
}

// ПРОГОН ЗАПИСЫВАЕТ СВОЙ КОЛОРВЕЙ, флэтовому роду колорвей отказан, а КАДРЫ ПРОГОНА наследуют его
// колонкой — конвейер «прогон колорвея → мультивью колорвея → сплит на стороны колорвея» целиком.
func TestDesignDBRunRecordsTheColorwayAndItsOutputsInheritIt(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	resetBudget(t, raw, "10.00")

	_, err := rep.Design().StartRun(context.Background(), entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindFlat, RequestedOutputs: 1,
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Author:        "probe", ColorwayId: cw,
	})
	require.ErrorIs(t, err, entity.ErrDesignColorwayForbidden,
		"флэт — одна разметка на карточку; флэт-прогон колорвея не имеет")

	started, err := rep.Design().StartRun(context.Background(), entity.DesignRunStart{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		Kind: entity.DesignRunKindRender, RequestedOutputs: 1,
		PriceEstimate: decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
		Author:        "probe", ColorwayId: cw,
	})
	require.NoError(t, err)
	require.Equal(t, cw, entity.DesignColorwayOrNone(started.Run.ColorwayId),
		"строка прогона несёт колорвей колонкой")

	// Кадры прилетевшего результата наследуют колорвей СТРОКИ — воркер его не называет вовсе.
	claimed, err := rep.Design().ClaimRuns(context.Background(), 1, 60_000_000_000, uuid.NewString())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, started.Run.Id, claimed[0].Id)
	outMedia := probeMedia(t, raw)
	done, err := rep.Design().CompleteRun(context.Background(), entity.DesignRunComplete{
		RunId: claimed[0].Id, ClaimToken: claimed[0].ClaimToken.String,
		Outputs: []entity.DesignPictureInsert{{MediaId: outMedia, Ordinal: 0}},
	})
	require.NoError(t, err)
	require.Len(t, done.Pictures, 1)
	require.Equal(t, cw, entity.DesignColorwayOrNone(done.Pictures[0].ColorwayId),
		"кадр рендера рождается кадром СВОЕГО колорвея")

	// И РАЗРЕЗ НАСЛЕДУЕТ: кроп мультивью колорвея — сторона того же колорвея.
	cropMedia := probeMedia(t, raw)
	crops, err := rep.Design().SplitPicture(context.Background(), entity.DesignSplitRequest{
		PictureId: done.Pictures[0].Id, ClientRequestId: uuid.NewString(), Actor: "probe",
		Frames: []entity.DesignSplitFrame{{MediaId: cropMedia, ViewKey: entity.DesignViewFront}},
	})
	require.NoError(t, err)
	require.Len(t, crops, 1)
	require.Equal(t, cw, entity.DesignColorwayOrNone(crops[0].ColorwayId),
		"сплит мультивью не теряет колорвей — иначе кроп не встал бы в колорвейный верстак")
}

// ЧТЕНИЕ ПОЛОСЫ ОТДАЁТ ПЕР-КОЛОРВЕЙНОЕ МНОЖЕСТВО ЗАНЯТЫХ ВЕРСТАКОВ и колорвей на каждом слоте —
// то, из чего клиент рисует дверь 3D и группирует верстаки.
//
// ⚠ ЭТА ПРОБА — ЖИВОЙ ЗАМЕР D5: она держит РАЗНИЦУ между «на карточке есть такой файл»
// (HasFabricRender) и «верстак этого колорвея занят» (RenderBenchColorways). Легаси-рендер здесь
// ЗАГРУЖЕН, но НЕ ПОСТАВЛЕН, и дверь безколорвейному 3D он открывать не должен: отбор входов
// читает слоты, и такой прогон ушёл бы в работу с пустым набором плит, заплатив за это.
//
// МУТАЦИЯ, КОТОРУЮ ЛОВИТ: вернуть designRenderBenchColorways к счёту по design_picture — список
// станет {0, cw} на первой половине пробы.
func TestDesignDBBandServesTheColorwayAxis(t *testing.T) {
	rep, raw := probeRepository(t)
	card, _, _ := designProbeCard(t, rep, raw)
	cw := probeColorway(t, raw, card, "BLK")
	picCw := uploadRenderPlate(t, rep, raw, card, cw)
	picLegacy := uploadRenderPlate(t, rep, raw, card, 0) // легаси-рендер, пока НЕ поставленный

	place := func(view string, colorway entity.DesignColorwayRef, pic int) error {
		_, err := rep.Design().SetBenchSlot(context.Background(), entity.DesignBenchSlotSet{
			TechCardId: card,
			Slot: entity.DesignSlotRef{
				ViewKey: view, Kind: entity.DesignPictureKindRender, ColorwayId: colorway,
			},
			PictureId: pic, Actor: "probe",
		})
		return err
	}
	require.NoError(t, place(entity.DesignViewFront, entity.DesignColorwayRef(cw), picCw))

	band, err := rep.Design().GetBand(context.Background(), card, 5)
	require.NoError(t, err)
	require.True(t, band.HasFabricRender,
		"файл на карточке есть — старый флаг это и говорит")
	require.ElementsMatch(t, []int{cw}, band.RenderBenchColorways,
		"ЗАНЯТ только верстак cw; загруженный, но не поставленный легаси-рендер двери не открывает")

	var found bool
	for _, s := range band.Bench {
		if entity.DesignColorwayOrNone(s.ColorwayId) == cw {
			found = true
			require.Equal(t, entity.DesignPictureKindRender, entity.DesignKindOrFlat(s.Kind))
		}
	}
	require.True(t, found, "верстак колорвея читается из полосы вместе со своим колорвеем")

	// А ПОСТАВЛЕННЫЙ — ОТКРЫВАЕТ. Вторая половина замера: дверь заводится именно постановкой, а
	// не загрузкой, и безколорвейный верстак в этом равноправен именованному.
	require.NoError(t, place(entity.DesignViewBack, 0, picLegacy))
	band, err = rep.Design().GetBand(context.Background(), card, 5)
	require.NoError(t, err)
	require.ElementsMatch(t, []int{0, cw}, band.RenderBenchColorways,
		"поставленный легаси-рендер открывает дверь безколорвейному 3D")
}
