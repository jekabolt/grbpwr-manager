package design_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/design"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ РУЧНОГО ПУТИ ПОЛОСЫ, ТРЕБУЮЩИЕ СТРОК: верстак, пачка загрузки, шапка, удаление слота.
//
// ⚠ ЭТИ ДЕВЯТЬ ПРОБ БЫЛИ НАПИСАНЫ И НЕ ИСПОЛНЯЛИСЬ НИ РАЗУ. Файл стоял под безусловным t.Skip с
// доводом «обвязки ещё нет»: обвязка требовала построить dependency.Repository поверх пробного
// соединения, и это было отложено. Довод УМЕР — store.NewForTest существует, и соседние файлы
// пакета (wave2_db_test.go, fixes_db_test.go) уже ходят через него в настоящую базу. Скип пережил
// свою причину и держал девять сценариев мёртвыми: пропущенная проба неотличима от отсутствующей,
// но выглядит как покрытие.
//
// ОБВЯЗКА ОБЩАЯ С wave2_db_test.go — probeRepository / probeCard / probeMedia. Второй, свой
// харнесс здесь означал бы второй источник правды о том, какой стор проверяется; см. шапку
// wave2_db_test.go: без CI=1 всё пропускается ДО открытия соединения, а имя базы, не похожее на
// пробное, отвергается отдельно.
//
// ПАКЕТ ВНЕШНИЙ (design_test), потому что репозиторий строит internal/store, который импортирует
// этот пакет: изнутри package design его не собрать без цикла. Плата — доступ только к
// экспортированному, и она уплачена сознательно: пробы ходят ровно тем путём, каким ходит
// хендлер.

// ─────────────────────── фикстуры ───────────────────────

// designProbeCard — карточка и ДВЕ её плиты. Плиты заводятся настоящей регистрацией пачки, а не
// прямым INSERT: слот сверяет род кадра и его карточку, и картинка, вставленная в обход
// RegisterBatch, отличалась бы от живой ровно теми полями, которые сторожа и читают.
func designProbeCard(t *testing.T, rep dependency.Repository, raw *sql.DB) (card, picA, picB int) {
	t.Helper()
	card = probeCard(t, raw)
	mediaA, mediaB := probeMedia(t, raw), probeMedia(t, raw)
	batch, err := rep.Design().RegisterBatch(context.Background(), entity.DesignBatchRegister{
		TechCardId: card, ClientRequestId: uuid.NewString(), Actor: "probe",
		Items: []entity.DesignUploadItem{
			{MediaId: mediaA, Kind: entity.DesignPictureKindFlat},
			{MediaId: mediaB, Kind: entity.DesignPictureKindFlat},
		},
	})
	require.NoError(t, err)
	require.Len(t, batch.Pictures, 2)
	return card, batch.Pictures[0].Id, batch.Pictures[1].Id
}

// designProbeRun кладёт ЗАКРЫТУЮ строку истории прямым INSERT. Прямым — потому что нужна не
// машина прогонов, а её след: девятнадцать строк ради одного счётчика шапки, и StartRun на каждую
// потратил бы ещё и бюджет. Статус `done` держит их вне досягаемости захвата, чтобы эти строки не
// участвовали в чужих пробах.
func designProbeRun(t *testing.T, raw *sql.DB, cardID int, archived bool) int {
	t.Helper()
	arch := "NULL"
	if archived {
		arch = "UTC_TIMESTAMP(6)"
	}
	res, err := raw.Exec(fmt.Sprintf(`
		INSERT INTO design_run
			(tech_card_id, kind, status, client_request_id, provider_idempotency_key, archived_at)
		VALUES (?, 'flat', 'done', ?, ?, %s)`, arch),
		cardID, uuid.NewString(), uuid.NewString())
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	return int(id)
}

// designProbeVersionQuotingSlot морозит лист, чья плита ЦИТИРУЕТ слот. Ровно эта ссылка и делает
// слот неудаляемым.
func designProbeVersionQuotingSlot(t *testing.T, raw *sql.DB, cardID, slotID int) {
	t.Helper()
	media := probeMedia(t, raw)
	res, err := raw.Exec(`
		INSERT INTO design_sheet_version
			(tech_card_id, version_number, client_request_id, minted_via, minted_by)
		VALUES (?, 1, ?, 'probe', 'probe')`, cardID, uuid.NewString())
	require.NoError(t, err)
	versionID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = raw.Exec(`
		INSERT INTO design_sheet_version_plate
			(version_id, ordinal, view_key, slot_id, media_id, source_class)
		VALUES (?, 0, 'detail', ?, ?, 'uploaded')`, versionID, slotID, media)
	require.NoError(t, err)
}

// ─────────────────────── верстак ───────────────────────

// РОЖДЕНИЕ СЛОТА ПОД ГОНКОЙ ИМЕЕТ РОВНО ОДНОГО ПОБЕДИТЕЛЯ, А ПРОИГРАВШИЙ ПОЛУЧАЕТ ОТКАЗ, КОТОРЫЙ
// КЛИЕНТ УМЕЕТ ОТКАТИТЬ.
//
// Четыре стороны силуэта не заводятся заранее: слот рождается первой постановкой. Проба ставит
// плиту на пустой `front` ДВУМЯ одновременными вызовами и требует ровно одного победителя, а
// проигравшему — slot_rev_mismatch с текущим состоянием слота, потому что откатывать клиент умеет
// именно его.
//
// ⚠ ЧЕГО ЭТА ПРОБА НЕ ДОКАЗЫВАЕТ, И ЭТО ИЗМЕРЕНО, А НЕ ПРЕДПОЛОЖЕНО. Она НЕ различает upsert и
// наивное «SELECT, строки нет, INSERT»: обезврежен разбор 1062 в upsertSilhouetteSlot — проба
// осталась ЗЕЛЁНОЙ. Причина та же, что у токена захвата (см. шапку раздела в wave2_db_test.go):
// пишущая транзакция идёт SERIALIZABLE, `slotByKey` по пустому диапазону берёт gap-lock, и вторая
// горутина ЖДЁТ снаружи, а не видит «строки нет». Столкновения на INSERT не происходит вовсе, и
// сырой 1062 взяться неоткуда. Требование NotContains("1062") здесь — страховка на день, когда
// изоляция ослабнет, а не действующий сторож.
//
// ЧТО ПРОБА ДОКАЗЫВАЕТ И ЧТО СЛОМАЕТСЯ БЕЗ ЭТОГО: CAS по slot_rev действительно исключающий.
// Убери вердикт ревизии из upsertSilhouetteSlot — победителями станут ОБА, и второй человек
// увидит «сохранено» на плите, которой в слоте нет.
func TestDesignDBLazySlotBirthRaceHasExactlyOneWinner(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, picA, picB := designProbeCard(t, rep, raw)

	var wg sync.WaitGroup
	results := make([]error, 2)
	slots := make([]*entity.DesignBenchSlot, 2)
	pics := []int{picA, picB}
	// СТАРТОВЫЙ БАРЬЕР. Без него первая горутина успевает закрыть транзакцию до того, как вторая
	// начнёт, «ровно один победитель» становится истинным по причине «одновременности не было», и
	// проба зеленеет, не измерив ничего.
	gate := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-gate
			slots[i], results[i] = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
				TechCardId:      card,
				Slot:            entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
				PictureId:       pics[i],
				ExpectedSlotRev: 0,
				Actor:           fmt.Sprintf("probe-%d", i),
			})
		}(i)
	}
	close(gate)
	wg.Wait()

	won := 0
	for i, err := range results {
		if err == nil {
			won++
			require.Equal(t, 1, slots[i].SlotRev, "слот победителя рождается на ревизии 1")
			continue
		}
		require.ErrorIs(t, err, entity.ErrDesignSlotRevMismatch,
			"проигравшему обязан достаться slot_rev_mismatch, а не голая 1062, которую клиенту нечем откатить")
		require.NotContains(t, err.Error(), "1062")
		require.NotNil(t, slots[i], "отказ несёт текущее состояние слота, иначе клиенту нечего показать")
	}
	require.Equal(t, 1, won, "постановка выигрывает ровно одна")
}

// УСТАРЕВШИЙ CAS ОТВЕРГАЕТСЯ И НЕ МЕНЯЕТ НИЧЕГО — ВКЛЮЧАЯ ПОДПИСЬ.
//
// SERIALIZABLE закрывает гонку записи, но не знает, что человек смотрел на экран четырёхминутной
// давности; это и есть работа slot_rev. Проба требует не только отказа, но и НЕПОДВИЖНОСТИ трёх
// вещей: ревизии, плиты и БАЙЛАЙНА.
//
// ПОДПИСЬ ЗДЕСЬ — НЕ ПРИДИРКА. Присваивания в ON DUPLICATE KEY UPDATE MySQL считает слева направо,
// и каждое следующее видит то, что записало предыдущее. Стоит slot_rev уехать вверх по списку —
// сторож `slot_rev = :expected_rev` в set_by и set_at сравнивается с УЖЕ УВЕЛИЧЕННОЙ ревизией,
// оказывается ложным, и два штампа молча остаются от прошлого автора на CAS, который УДАЛСЯ.
// Круговой рейс этого не покажет: картинка в слот встаёт, врёт только подпись.
func TestDesignDBStaleCASIsRefusedAndChangesNothing(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, picA, picB := designProbeCard(t, rep, raw)

	first, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picA, ExpectedSlotRev: 0, Actor: "first",
	})
	require.NoError(t, err)
	require.Equal(t, 1, first.SlotRev)

	// Тот, кто последний раз читал ревизию 0, пытается переписать то, что уже на ревизии 1.
	stale, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picB, ExpectedSlotRev: 0, Actor: "stale",
	})
	require.ErrorIs(t, err, entity.ErrDesignSlotRevMismatch)
	require.NotNil(t, stale, "отказ несёт слот таким, каков он есть")
	require.Equal(t, 1, stale.SlotRev, "ревизия не сдвинулась")
	require.Equal(t, int32(picA), stale.PictureId.Int32, "плита не сдвинулась")
	require.NotNil(t, stale.Picture, "и отказ показывает, КАКАЯ плита, а не только её id")
	require.Equal(t, "first", stale.SetBy, "подпись принадлежит тому, чья запись действительно прошла")

	// ⚠ УДАВШИЙСЯ CAS ПОВЕРХ СУЩЕСТВУЮЩЕЙ СТРОКИ — БЕЗ НЕГО ПРОБА НЕ ВИДИТ ПОРЯДОК ПРИСВАИВАНИЙ
	// ВОВСЕ. В отказе выше НИ ОДНО присваивание не срабатывает (все пять сторожей ложны), поэтому
	// «подпись осталась от first» истинна при любом порядке — измерено: перестановка slot_rev в
	// начало списка эту пробу зелёной и оставляла. Дефект живёт только на УСПЕХЕ: slot_rev,
	// присвоенный раньше подписи, увеличивается, и `slot_rev = :expected_rev` в set_by / set_at
	// сравнивается уже с НОВОЙ ревизией, оказывается ложным — плита переезжает, а штамп автора и
	// времени молча остаются от прежнего. Круговой рейс этого не покажет.
	second, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: picB, ExpectedSlotRev: 1, Actor: "second",
	})
	require.NoError(t, err)
	require.Equal(t, 2, second.SlotRev)
	require.Equal(t, int32(picB), second.PictureId.Int32, "плита переехала")
	require.Equal(t, "second", second.SetBy,
		"подпись обязана переехать ВМЕСТЕ с плитой: разъехавшись, они делают историю слота ложью")
	require.True(t, second.SetAt.Valid)
}

// ПЛИТА ЧУЖОЙ КАРТОЧКИ В СЛОТ НЕ ВСТАЁТ.
//
// СХЕМА ЭТОГО ВЫРАЗИТЬ НЕ МОЖЕТ, и потому проверка живёт в Go — в той же транзакции, что и
// запись. Составной FK (tech_card_id, picture_id) обязан был бы каскадировать: обе колонки NOT
// NULL, а слот детали обязан пережить исчезновение своей плиты. Без этой проверки лист карточки А
// молча собрался бы из кадров карточки Б.
func TestDesignDBForeignCardPlateIsRefused(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	cardA, _, _ := designProbeCard(t, rep, raw)
	_, foreignPic, _ := designProbeCard(t, rep, raw)

	_, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: cardA, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: foreignPic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignForeignCardPlate)
}

// ПОВТОР ЗАГРУЗКИ ПОСЛЕ СЕТЕВОГО ТАЙМАУТА — ТА ЖЕ ПАЧКА, А НЕ ВТОРАЯ.
//
// Что сломается без идемпотентности по client_request_id: повтор упрётся в UNIQUE и вернётся
// человеку ошибкой на жест, который УЖЕ прошёл, — то есть «мои файлы пропали» при том, что файлы
// на месте. Проба требует не только отсутствия ошибки, но и ТЕХ ЖЕ id картинок: повтор, заведший
// вторую пачку с новыми строками, тоже был бы «без ошибки».
func TestDesignDBBatchIsIdempotentOnClientRequestId(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	// Карточка БЕЗ заготовленных плит: designProbeCard заводит их пачкой, и счёт пачек в конце
	// пробы считал бы её тоже.
	card := probeCard(t, raw)
	mediaA, mediaB := probeMedia(t, raw), probeMedia(t, raw)

	req := entity.DesignBatchRegister{
		TechCardId:      card,
		ClientRequestId: uuid.NewString(),
		Items: []entity.DesignUploadItem{
			{MediaId: mediaA, GhostView: entity.DesignViewFront},
			{MediaId: mediaB, GhostView: entity.DesignViewBack},
		},
		Actor: "probe",
	}
	first, err := rep.Design().RegisterBatch(ctx, req)
	require.NoError(t, err)
	require.False(t, first.Idempotent)
	require.Len(t, first.Pictures, 2)

	second, err := rep.Design().RegisterBatch(ctx, req)
	require.NoError(t, err, "повтор после сетевого таймаута не может быть ошибкой")
	require.True(t, second.Idempotent)
	require.Equal(t, first.Batch.Id, second.Batch.Id, "ТА ЖЕ пачка, а не вторая")
	require.Len(t, second.Pictures, 2, "и те же картинки, а не четыре")
	for i := range first.Pictures {
		require.Equal(t, first.Pictures[i].Id, second.Pictures[i].Id)
	}

	var batches int
	require.NoError(t, raw.QueryRow(
		`SELECT COUNT(*) FROM design_batch WHERE tech_card_id = ?`, card).Scan(&batches))
	require.Equal(t, 1, batches)
}

// ─────────────────────── шапка полосы ───────────────────────

// СЧЁТЧИКИ ШАПКИ СЧИТАЮТ КАРТОЧКУ, А НЕ ЭКРАН.
//
// У карточки строк больше, чем влезает на страницу. Посчитанный по загруженной странице
// total_runs равнялся бы РАЗМЕРУ СТРАНИЦЫ — и это единственный способ увидеть подмену: число
// совпало бы с потолком, а не с правдой. Тот же довод у archived_runs: свёрнутые строки живут
// в основном за пределами первой страницы, и счётчик, считающий видимое, показывал бы ноль
// ровно тогда, когда сворачиванием пользуются.
func TestDesignDBHeaderAggregatesSurvivePagination(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card := probeCard(t, raw)

	const total = design.DefaultRunPageLimit + 7
	const archived = 3
	for i := 0; i < total; i++ {
		designProbeRun(t, raw, card, i < archived)
	}

	band, err := rep.Design().GetBand(ctx, card, design.DefaultRunPageLimit)
	require.NoError(t, err)
	require.Len(t, band.Runs, design.DefaultRunPageLimit, "СТРАНИЦА — это одна страница")
	require.Equal(t, total, band.TotalRuns, "ШАПКА считает карточку целиком")
	require.Equal(t, archived, band.ArchivedRuns)
	require.NotZero(t, band.NextCursor, "и страница честно говорит, что есть продолжение")
}

// ─────────────────────── невидимость против слота ───────────────────────

// ПЛИТУ, СТОЯЩУЮ В СЛОТЕ, СПРЯТАТЬ НЕЛЬЗЯ — И СТОРОЖ ЧИТАЕТ В ТОЙ ЖЕ ТРАНЗАКЦИИ, ЧТО ПИШЕТ.
//
// Спрятанная плита осталась бы в слоте и уехала бы на технический лист, которого никто больше не
// видит в полосе. Проверка «прочитать, потом записать» двумя транзакциями — это TOCTOU под
// вежливым именем: между чтением и записью плиту успевают поставить.
//
// Обратный жест не сторожится вовсе, и снятие плиты снова делает сокрытие законным — обе половины
// проверены ниже, иначе «отказ» доказывал бы лишь то, что сокрытие не работает никогда.
func TestDesignDBHidePictureRefusesAPlateInASlot(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, rep, raw)

	_, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().HidePicture(ctx, pic, true, "probe")
	require.ErrorIs(t, err, entity.ErrDesignInSlot)

	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: 0, ExpectedSlotRev: 1, Actor: "probe",
	})
	require.NoError(t, err)
	hidden, err := rep.Design().HidePicture(ctx, pic, true, "probe")
	require.NoError(t, err)
	require.True(t, hidden.HiddenAt.Valid)
}

// ─────────────────────── удаление слота детали ───────────────────────

// СЛОТ, ПРОЦИТИРОВАННЫЙ ЗАМОРОЖЕННОЙ ВЕРСИЕЙ, НЕ УДАЛЯЕТСЯ — И ЭТО ОТКАЗ, КОТОРЫЙ FK ВЫРАЗИТЬ НЕ
// МОЖЕТ.
//
// Слот и версия оба каскадируют от tech_card, поэтому RESTRICT на design_sheet_version_plate.slot_id
// убил бы DeleteTechCard ошибкой 1451, которую вызывающему нечем починить. Значит правило живёт в
// Go, и здесь у него ДВЕ половины: слот с плитой (slot_filled) и слот, на который ссылается
// бумага (slot_in_version). Проверять надо обе — сторож, снятый с одной, оставил бы другую
// зелёной.
func TestDesignDBDetailSlotQuotedByAVersionCannotBeDeleted(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, rep, raw)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewDetail},
		PictureId: pic, ExpectedSlotRev: 0, NewDetailName: "pocket flap", Actor: "probe",
	})
	require.NoError(t, err)

	// Пока держит плиту.
	require.ErrorIs(t, rep.Design().DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignSlotFilled)

	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{SlotId: slot.Id},
		PictureId: 0, ExpectedSlotRev: slot.SlotRev, Actor: "probe",
	})
	require.NoError(t, err)
	designProbeVersionQuotingSlot(t, raw, card, slot.Id)
	require.ErrorIs(t, rep.Design().DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignSlotInVersion)
}

// СТОРОНУ СИЛУЭТА УДАЛИТЬ НЕЛЬЗЯ, ДАЖЕ ПУСТУЮ.
//
// Четыре стороны — это адреса изделия, а не вещи, которые человек завёл. Удалённая сторона
// молча родилась бы заново на ревизии 1 при следующей постановке, и CAS-токен КАЖДОГО открытого
// клиента стал бы неверным разом. Отказ добавлен здесь, а не оставлен тихим успехом.
func TestDesignDBSilhouetteSideIsNotADetailSlot(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, rep, raw)

	slot, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)
	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{SlotId: slot.Id},
		PictureId: 0, ExpectedSlotRev: slot.SlotRev, Actor: "probe",
	})
	require.NoError(t, err)
	require.ErrorIs(t, rep.Design().DeleteDetailSlot(ctx, slot.Id), entity.ErrDesignNotADetailSlot)
}

// ОДНА ПЛИТА НЕ МОЖЕТ СТОЯТЬ В ДВУХ СЛОТАХ, И ПРОВЕРКА В Go ЗДЕСЬ НУЖНА ДЛЯ ПРАВИЛЬНОСТИ, А НЕ
// ДЛЯ КРАСИВОГО ОТКАЗА.
//
// У design_bench_slot ДВА уникальных ключа, а INSERT … ON DUPLICATE KEY UPDATE правит ТУ строку,
// на которой столкнулся. Плита, уже стоящая в `back`, при постановке в `front` сталкивается на
// uq_design_bench_picture — то есть НА СТРОКЕ `back`, — и ветка ON DUPLICATE поехала бы менять
// `back` вместо `front`. Отказ в Go оставляет upsert достижимым только через uq_design_bench_view.
//
// ЧТО СЛОМАЕТСЯ, ЕСЛИ ПРОВЕРКА ИСЧЕЗНЕТ: отказа не будет вовсе, а изменится ЧУЖОЙ слот — поэтому
// проба смотрит не только на ошибку, но и на неподвижность `back`.
func TestDesignDBPlateCannotStandInTwoSlots(t *testing.T) {
	rep, raw := probeRepository(t)
	ctx := context.Background()
	card, pic, _ := designProbeCard(t, rep, raw)

	back, err := rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewBack},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.NoError(t, err)

	_, err = rep.Design().SetBenchSlot(ctx, entity.DesignBenchSlotSet{
		TechCardId: card, Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront},
		PictureId: pic, ExpectedSlotRev: 0, Actor: "probe",
	})
	require.ErrorIs(t, err, entity.ErrDesignPictureAlreadyInSlot)

	var rev int
	var holder sql.NullInt32
	require.NoError(t, raw.QueryRow(
		`SELECT slot_rev, picture_id FROM design_bench_slot WHERE id = ?`, back.Id).Scan(&rev, &holder))
	require.Equal(t, back.SlotRev, rev, "отвергнутая постановка не тронула ЧУЖОЙ слот")
	require.Equal(t, int32(pic), holder.Int32)
}
