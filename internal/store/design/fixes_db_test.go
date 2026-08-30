package design_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ ЧЕТЫРЁХ ПОДТВЕРЖДЁННЫХ ДЕФЕКТОВ, ТРЕБУЮЩИЕ СТРОК.
//
// Обвязка та же, что у wave2_db_test.go (см. её шапку: без CI=1 всё здесь пропускается ДО
// открытия соединения, а имя базы, не похожее на пробное, отвергается отдельно).

// ─────────────────────── H: род при адресации по slot_id ───────────────────────

// АДРЕС ПО slot_id БЕРЁТ РОД У САМОЙ СТРОКИ СЛОТА, А НЕ У ЗАПРОСА.
//
// Контракт говорит это прямо: «IGNORED when the ref addresses an existing slot by slot_id — a
// minted id already names its bench». Пока род брался из запроса (а по проводу он в этой форме и
// не передаётся, то есть всегда пуст → flat), появлялись ДВА молчаливых исхода, и оба проверены
// ниже:
//
//  1. ЛОЖНЫЙ ОТКАЗ: замена плиты в рендер-слоте по id — кадр `render` против подставленного
//     `flat`, ErrDesignWrongKind на совершенно законный жест;
//  2. ФЛЭТ, ПРИНЯТЫЙ В РЕНДЕР-СЛОТ: род запроса (flat) совпадал с родом кадра (flat), а
//     casExistingSlot род не проверяет вовсе — верстак, с которого строится 3D, портился молча.
//
// МУТАЦИЯ: вернуть `kind := entity.DesignKindOrFlat(req.Slot.Kind)` действующим на ветке byID.
func TestDesignDBSlotAddressedByIdKeepsItsOwnKind(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	renderA, renderB, flatMedia := probeMedia(t, raw), probeMedia(t, raw), probeMedia(t, raw)
	ctx := context.Background()

	batch, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: renderA, Kind: entity.DesignPictureKindRender},
			{MediaId: renderB, Kind: entity.DesignPictureKindRender},
			{MediaId: flatMedia, Kind: entity.DesignPictureKindFlat},
		},
	})
	require.NoError(t, err)
	picRenderA, picRenderB, picFlat := batch.Pictures[0].Id, batch.Pictures[1].Id, batch.Pictures[2].Id

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card,
		Slot: entity.DesignSlotRef{
			ViewKey: entity.DesignViewFront, Kind: entity.DesignPictureKindRender,
		},
		PictureId: picRenderA, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignPictureKindRender, slot.Kind)

	// ① ЗАМЕНА ПО id БЕЗ РОДА — ЗАКОННА. Это единственная форма, которую клиент вообще может
	// послать: род при адресации по id на проводе не переносится.
	after, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:      card,
		Slot:            entity.DesignSlotRef{SlotId: slot.Id},
		PictureId:       picRenderB,
		ExpectedSlotRev: slot.SlotRev,
		Actor:           "probe",
	})
	require.NoError(t, err, "замена рендера в рендер-слоте по id — законный жест, а не wrong_kind")
	require.Equal(t, int32(picRenderB), after.PictureId.Int32)
	require.Equal(t, entity.DesignPictureKindRender, after.Kind, "слот не сменил верстак")

	// ② ФЛЭТ В РЕНДЕР-СЛОТ ПО id — ОТКАЗ. Прежде проходил: род запроса читался как flat и совпадал
	// с родом кадра, а сам CAS род не сверяет.
	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId:      card,
		Slot:            entity.DesignSlotRef{SlotId: slot.Id},
		PictureId:       picFlat,
		ExpectedSlotRev: after.SlotRev,
		Actor:           "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignWrongKind,
		"флэт в рендер-слоте — это лист, собранный не из того, и увидеть это можно только здесь")
}

// ─────────────────────── G: провенанс флэттена читает origin ───────────────────────

// ФЛЭТТЕН ИМПОРТИРОВАННОГО ФАЙЛА НЕ СТАНОВИТСЯ «ПРАВКОЙ ИИ», А МАШИННЫЙ ВЕКТОР — «РИСУНКОМ».
//
// МУТАЦИЯ: вернуть прежнее правило (есть база → ai_edits, нет → drawn) — оба утверждения краснеют.
func TestDesignDBFlattenCarriesTheLayerOrigin(t *testing.T) {
	rep, raw := probeRepository(t)
	card := probeCard(t, raw)
	base, svg, raster := probeMedia(t, raw), probeMedia(t, raw), probeMedia(t, raw)
	ctx := context.Background()

	reg, err := rep.Design().RegisterBatch(ctx, entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{{MediaId: base, Kind: entity.DesignPictureKindFlat}},
	})
	require.NoError(t, err)

	// ЧУЖОЙ ФАЙЛ ПОВЕРХ НАШЕГО РАСТРА. База есть, значит прежнее правило сказало бы `ai_edits` —
	// то есть «правка нашего кадра», хотя вектор пришёл извне целиком.
	imported, err := rep.Design().ImportVector(ctx, entity.DesignVectorImport{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		SourceMediaId: svg, SourcePictureId: reg.Pictures[0].Id,
		Origin: entity.DesignLayerOriginImported, BaseMediaId: base,
		Strokes: []byte(`[{"d":"M0 0 L1 1"}]`), Actor: "probe",
	})
	require.NoError(t, err)

	pic, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: imported.Id, ExpectedRev: imported.Rev,
		MediaId: raster, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignSourceImportedSVG, pic.SourceClass,
		"импортированный SVG остаётся чужим файлом и лёжа поверх нашего растра")

	// МАШИННЫЙ ВЕКТОР БЕЗ БАЗЫ. Прежнее правило сказало бы `drawn` — то есть отмыло бы платную
	// перерисовку моделью в работу руки.
	svg2, raster2 := probeMedia(t, raw), probeMedia(t, raw)
	vectorised, err := rep.Design().ImportVector(ctx, entity.DesignVectorImport{
		TechCardId: card, ClientRequestId: uuid.NewString(),
		SourceMediaId: svg2, Origin: entity.DesignLayerOriginVectorised,
		Strokes: []byte(`[{"d":"M0 0 L2 2"}]`), Actor: "probe",
	})
	require.NoError(t, err)

	pic2, err := rep.Design().FlattenEditLayer(ctx, entity.DesignEditLayerFlatten{
		TechCardId: card, LayerId: vectorised.Id, ExpectedRev: vectorised.Rev,
		MediaId: raster2, Actor: "probe",
	})
	require.NoError(t, err)
	require.Equal(t, entity.DesignSourceAI, pic2.SourceClass,
		"машинная перерисовка не становится рисунком от руки оттого, что у слоя нет базы")
}

// ─────────────────────── A: перехват брошенного хендлера ───────────────────────

// ДВА ОДНОВРЕМЕННЫХ ПОВТОРА ОДНОГО client_request_id — ОДИН ПЕРЕХВАТ.
//
// ЧТО БЫЛО. Перехват сводился к чтению строки: оба повтора видели истёкшую лизу, оба брали ЕЁ ЖЕ
// claim_token, оба открывали попытку (MAX(attempt_no)+1 их лишь нумерует) и ОБА ПЛАТИЛИ МОДЕЛИ.
// ReviveExpiredRuns тут не спасает — он трогает только `running`, а брошенная строка синхронного
// хендлера лежит `pending`. Обещание «повтор = один платёж» в окне резюма не выполнялось.
//
// ЧТО ПРОВЕРЯЕТСЯ ИМЕННО ЗДЕСЬ, А НЕ МОКОМ. Исключение живёт в WHERE записи, и доказать его может
// только база: два соединения, одна строка, счёт победителей.
//
// МУТАЦИЯ: убрать из designRunResumableSQL условие живости лизы (`claim_expires_at <
// UTC_TIMESTAMP(6)`) — перехват перестаёт быть исключающим и побеждают оба.
func TestDesignDBResumeOfAnAbandonedHandlerHasExactlyOneWinner(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw, "5.00")
	card := probeCard(t, raw)
	ctx := context.Background()

	reqID := uuid.NewString()
	start := func() (*entity.DesignRunStarted, error) {
		return rep.Design().StartRun(ctx, entity.DesignRunStart{
			TechCardId:      card,
			ClientRequestId: reqID,
			Kind:            entity.DesignRunKindDraftIdea,
			PriceEstimate:   decimal.NullDecimal{Decimal: decimal.RequireFromString("0.05"), Valid: true},
			Author:          "probe",
		})
	}
	first, err := start()
	require.NoError(t, err)
	require.False(t, first.Idempotent)
	require.False(t, first.Resumed, "первый запуск ничего не перехватывает — он рождает строку")
	require.True(t, first.Run.ClaimToken.Valid)
	original := first.Run.ClaimToken.String

	// ЖИВАЯ ЛИЗА — ПЕРЕХВАТА НЕТ. Это «вызов идёт прямо сейчас, в соседнем запросе».
	live, err := start()
	require.NoError(t, err)
	require.True(t, live.Idempotent)
	require.False(t, live.Resumed, "живую лизу не перехватывают: второй звонок оплатил бы ту же модель")
	require.Equal(t, original, live.Run.ClaimToken.String, "токен живой строки не ротируется")

	// ХЕНДЛЕР УМЕР: лиза истекла.
	expireClaim(t, raw, first.Run.Id)

	const racers = 6
	type result struct {
		started *entity.DesignRunStarted
		err     error
	}
	out := make(chan result, racers)
	var gate sync.WaitGroup
	gate.Add(1)
	for i := 0; i < racers; i++ {
		go func() {
			gate.Wait()
			s, err := start()
			out <- result{s, err}
		}()
	}
	gate.Done()

	winners, tokens := 0, map[string]struct{}{}
	for i := 0; i < racers; i++ {
		r := <-out
		require.NoError(t, r.err)
		require.True(t, r.started.Idempotent, "второй прогон на тот же ключ не заводится")
		if r.started.Resumed {
			winners++
			require.True(t, r.started.Run.ClaimToken.Valid)
			tokens[r.started.Run.ClaimToken.String] = struct{}{}
		}
	}
	require.Equal(t, 1, winners,
		"перехват брошенного хендлера обязан быть ИСКЛЮЧАЮЩИМ: каждый лишний победитель — второй платёж")
	require.Len(t, tokens, 1)
	for tok := range tokens {
		require.NotEqual(t, original, tok,
			"перехват РОТИРУЕТ токен: иначе опоздавший закроет прогон, который ведёт победитель")
	}

	// ЛИЗА ПРОДЛЕНА, значит третий повтор секундой позже снова упирается в живой захват.
	after, err := start()
	require.NoError(t, err)
	require.False(t, after.Resumed, "перехват без продления лизы действовал бы ровно один раз")
}

// ПЕРЕХВАТ НЕ ТРОГАЕТ СТРОКИ ВОРКЕРА, И ЭТО ВТОРАЯ ПОЛОВИНА ПРАВИЛА.
//
// Предикат резюма — ТОЧНОЕ ДОПОЛНЕНИЕ предиката захвата по роду (`kind = 'draft_idea'` против
// `kind <> 'draft_idea'`), и не по вкусу: у прогона воркера истёкшая лиза значит «подмети меня»,
// а подметает ReviveExpiredRuns — он возвращает строку в pending и СТИРАЕТ токен. Ротировать его
// здесь значило бы оставить строку `running` со свежей лизой, которую не держит никто: воркер её
// не возьмёт (предикат требует pending), подметальщик не тронет (лиза жива), и задание зависло бы
// с зарезервированными деньгами до полуночи.
//
// МУТАЦИЯ: убрать `kind = 'draft_idea'` из designRunResumableSQL.
func TestDesignDBResumeLeavesWorkerRunsToTheSweeper(t *testing.T) {
	rep, raw := probeRepository(t)
	resetBudget(t, raw, "5.00")
	card := probeCard(t, raw)
	ctx := context.Background()

	reqID := uuid.NewString()
	start := func() (*entity.DesignRunStarted, error) {
		return rep.Design().StartRun(ctx, entity.DesignRunStart{
			TechCardId:       card,
			ClientRequestId:  reqID,
			Kind:             entity.DesignRunKindFlat,
			RequestedOutputs: 1,
			PriceEstimate:    decimal.NullDecimal{Decimal: decimal.RequireFromString("0.10"), Valid: true},
			Author:           "probe",
		})
	}
	first, err := start()
	require.NoError(t, err)

	// Строку берёт ВОРКЕР и его лиза истекает.
	claimed, err := rep.Design().ClaimRuns(ctx, 1, time.Minute, uuid.NewString())
	require.NoError(t, err)
	require.Len(t, claimed, 1)
	require.Equal(t, first.Run.Id, claimed[0].Id)
	expireClaim(t, raw, first.Run.Id)

	again, err := start()
	require.NoError(t, err)
	require.True(t, again.Idempotent)
	require.False(t, again.Resumed,
		"строка воркера принадлежит подметальщику: ротация токена оставила бы её running и ничьей")
	require.Equal(t, claimed[0].ClaimToken.String, again.Run.ClaimToken.String,
		"токен воркера не трогается — иначе его собственный CompleteRun получит claim_lost")

	// А подметальщик её действительно подбирает — иначе проба доказывала бы «никто не подберёт».
	n, err := rep.Design().ReviveExpiredRuns(ctx)
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 1)
	status, token := runStatus(t, raw, first.Run.Id)
	require.Equal(t, entity.DesignRunPending, status)
	require.False(t, token.Valid, "подметание СТИРАЕТ токен — это и есть возврат строки в очередь")
}
