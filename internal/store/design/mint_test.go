package design

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// ПРОБЫ МИНТА, СТОРОВАЯ ПОЛОВИНА — БЕЗ БАЗЫ.
//
// ЧТО ЗДЕСЬ ПРОВЕРЯЕТСЯ: решения минта, которые целиком живут в Go, — CAS по верстаку, четверо
// ворот и состав заморозки выносок. Именно они и есть содержание отказов, которые человек будет
// читать на экране.
//
// ЧЕГО ЗДЕСЬ НЕТ И ПОЧЕМУ: SQL-половина (UNIQUE на client_request_id, MAX+1 на номер версии, откат
// транзакции) требует живой базы, а прогон тестов internal/store здесь запрещён — его TestMain вне
// CI читает продовый DSN и на очистке ДРОПАЕТ таблицы. Названо вслух, чтобы зелень этого файла не
// читалась как «минт проверен целиком».

func benchSlot(id int, view string, rev int, picture int) entity.DesignBenchSlot {
	s := entity.DesignBenchSlot{Id: id, ViewKey: view, SlotRev: rev}
	if picture > 0 {
		s.PictureId = sql.NullInt32{Int32: int32(picture), Valid: true}
	}
	return s
}

func silhouettePlate(view string, media int) mintPlate {
	return mintPlate{slot: benchSlot(1, view, 1, 900), MediaId: media, SourceClass: entity.DesignSourceUploaded}
}

// ─────────────────────── CAS ПО ВЕРСТАКУ ───────────────────────

// ПУСТОЙ СПИСОК ЗНАЧИТ «НЕ ПРОВЕРЯТЬ», и это контракт, а не забывчивость. Серверный вызов вправе
// минтить без снимка верстака; UI всегда шлёт полный набор.
func TestExpectedPlatesEmptyMeansNoCheck(t *testing.T) {
	slots := []entity.DesignBenchSlot{benchSlot(1, entity.DesignViewFront, 9, 900)}
	require.NoError(t, casExpectedPlates(slots, nil))
}

// СОШЁЛСЯ — ПРОПУСКАЕТ. Положительный контроль: без него проба ниже зеленела бы на CAS, который
// отказывает ВСЕГДА.
func TestExpectedPlatesMatchingRevPasses(t *testing.T) {
	slots := []entity.DesignBenchSlot{benchSlot(1, entity.DesignViewFront, 3, 900)}
	err := casExpectedPlates(slots, []entity.DesignExpectedPlate{
		{Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront}, SlotRev: 3},
	})
	require.NoError(t, err)
}

// РЕВИЗИЯ УЕХАЛА — bench_moved С ИМЕНЕМ СЛОТА И ОБЕИМИ РЕВИЗИЯМИ.
func TestExpectedPlatesMovedRevIsBenchMoved(t *testing.T) {
	slots := []entity.DesignBenchSlot{benchSlot(11, entity.DesignViewFront, 4, 900)}
	err := casExpectedPlates(slots, []entity.DesignExpectedPlate{
		{Slot: entity.DesignSlotRef{ViewKey: entity.DesignViewFront}, SlotRev: 3},
	})
	require.ErrorIs(t, err, entity.ErrDesignBenchMoved)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal, "отказ обязан нести подробности, иначе клиенту нечего показать")
	require.Equal(t, "front", refusal.Metadata["slot"])
	require.Equal(t, "4", refusal.Metadata["slot_rev"])
	require.Equal(t, "3", refusal.Metadata["expected_slot_rev"])
}

// СЛОТА БОЛЬШЕ НЕТ — ТОЖЕ bench_moved, А НЕ «НЕЧЕГО СВЕРЯТЬ». Слот детали могли удалить ровно
// между экраном и минтом, и заморозить состав, которого уже нет, нельзя.
func TestExpectedPlatesVanishedSlotIsBenchMoved(t *testing.T) {
	err := casExpectedPlates(nil, []entity.DesignExpectedPlate{
		{Slot: entity.DesignSlotRef{SlotId: 91}, SlotRev: 2},
	})
	require.ErrorIs(t, err, entity.ErrDesignBenchMoved)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "true", refusal.Metadata["slot_gone"])
}

// АДРЕС СЛОТА ОБЯЗАН БЫТЬ НАЗВАН. Пустая ссылка — это не «любой слот», а испорченный запрос.
func TestExpectedPlatesWithoutAnAddressIsInvalid(t *testing.T) {
	err := casExpectedPlates(nil, []entity.DesignExpectedPlate{{SlotRev: 1}})
	require.ErrorIs(t, err, entity.ErrDesignInvalidArgument)
}

// ─────────────────────── ЧЕТВЕРО ВОРОТ ───────────────────────

func fullBench() []mintPlate {
	return []mintPlate{
		silhouettePlate(entity.DesignViewFront, 55),
		silhouettePlate(entity.DesignViewBack, 56),
	}
}

// ПОЛНЫЙ СОСТАВ БЕЗ ОСОБЕННОСТЕЙ ПРОХОДИТ. Положительный контроль ко всем воротам сразу.
func TestMintGatesPassOnAPlainComposition(t *testing.T) {
	require.NoError(t, mintGates(fullBench(), "oversized", entity.DesignSheetMint{}))
}

// МИНИМУМ ЛИСТА ПРОВЕРЯЕТСЯ В МИНТЕ, А НЕ НА ПРОГОНЕ: пустой обязательный слот запирает и v2+.
func TestMintGatesRefuseAMissingRequiredView(t *testing.T) {
	err := mintGates([]mintPlate{silhouettePlate(entity.DesignViewFront, 55)}, "oversized", entity.DesignSheetMint{})
	require.ErrorIs(t, err, entity.ErrDesignSheetMinUnmet)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "back", refusal.Metadata["missing"])
}

// ПОСАДКА СОГЛАСИЕМ НЕ СНИМАЕТСЯ, и отказ называет обе стороны спора: плита нарисована под одну,
// карточка утверждает другую, и одно из двух неверно.
func TestMintGatesRefuseAFitMismatchAndNameBothSides(t *testing.T) {
	plates := fullBench()
	plates[0].FitAtLaunch = sql.NullString{String: "slim", Valid: true}
	err := mintGates(plates, "oversized", entity.DesignSheetMint{MixedConsent: true, UploadedFitConfirm: true})
	require.ErrorIs(t, err, entity.ErrDesignFitMismatch)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "front", refusal.Metadata["view"])
	require.Equal(t, "slim", refusal.Metadata["fit"])
	require.Equal(t, "oversized", refusal.Metadata["card_fit"])
}

// ТА ЖЕ ПОСАДКА — НЕ РАСХОЖДЕНИЕ. Без этой половины ворота отказывали бы каждой карточке подряд.
func TestMintGatesAcceptAMatchingFit(t *testing.T) {
	plates := fullBench()
	plates[0].FitAtLaunch = sql.NullString{String: "oversized", Valid: true}
	require.NoError(t, mintGates(plates, "oversized", entity.DesignSheetMint{}))
}

// РУКИ ПОСАДКИ НЕ ЗАЯВЛЯЮТ ВОВСЕ — значит за них отвечает человек, а не сервер молча подставляет
// карточкину.
func TestMintGatesAskAboutUploadedPlates(t *testing.T) {
	plates := fullBench()
	plates[0].BatchId = sql.NullInt32{Int32: 5, Valid: true}
	require.ErrorIs(t, mintGates(plates, "oversized", entity.DesignSheetMint{}),
		entity.ErrDesignUploadedFitUnconfirmed)
	require.NoError(t, mintGates(plates, "oversized", entity.DesignSheetMint{UploadedFitConfirm: true}),
		"подтверждение обязано снимать вопрос — иначе минт руками невозможен вовсе")
}

// ДВЕ РАЗНЫЕ ГЕНЕРАЦИИ В СИЛУЭТЕ — КРАСНОЕ, и человек принимает это явно.
func TestMintGatesRequireConsentForTwoGenerations(t *testing.T) {
	plates := fullBench()
	plates[0].RunId = sql.NullInt32{Int32: 10, Valid: true}
	plates[1].RunId = sql.NullInt32{Int32: 11, Valid: true}
	require.ErrorIs(t, mintGates(plates, "oversized", entity.DesignSheetMint{}),
		entity.ErrDesignMixedNeedsConsent)
	require.NoError(t, mintGates(plates, "oversized", entity.DesignSheetMint{MixedConsent: true}))
}

// ОДНА ГЕНЕРАЦИЯ НА ДВУХ СТОРОНАХ — НЕ СМЕСЬ. Иначе «согласие» спрашивали бы у каждого минта, и
// оно перестало бы что-либо означать.
func TestMintGatesDoNotCallOneGenerationAMix(t *testing.T) {
	plates := fullBench()
	plates[0].RunId = sql.NullInt32{Int32: 10, Valid: true}
	plates[1].RunId = sql.NullInt32{Int32: 10, Valid: true}
	require.NoError(t, mintGates(plates, "oversized", entity.DesignSheetMint{}))
}

// ─────────────────────── ПОЯС П-А ───────────────────────

// ПЛИТА, КОТОРОЙ НЕТ В ТЕХНИЧЕСКИХ МЕДИА ДОКУМЕНТА, ОСТАНАВЛИВАЕТ МИНТ.
//
// Иначе она молча делает КАЖДУЮ деталь кроя на листовой выноске оторванной, и узнают об этом с
// напечатанного пустого эскиза.
func TestPlatesMustBeListedAsTechnicalMedia(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	empty := &entity.TechCardInsert{}
	require.ErrorIs(t, requirePlatesInDocument(plates, empty), entity.ErrDesignPlatesNotInDocument)

	// Мудборд не считается: связь детали с выноской смотрит ровно на category='technical'.
	mood := &entity.TechCardInsert{Media: []entity.TechCardMediaItem{
		{MediaId: 55, Category: entity.TechCardMediaCategoryMoodboard},
	}}
	require.ErrorIs(t, requirePlatesInDocument(plates, mood), entity.ErrDesignPlatesNotInDocument)

	technical := &entity.TechCardInsert{Media: []entity.TechCardMediaItem{
		{MediaId: 55, Category: entity.TechCardMediaCategoryTechnical},
	}}
	require.NoError(t, requirePlatesInDocument(plates, technical))
}

// ─────────────────────── СОСТАВ ЗАМОРОЗКИ (П-Е) ───────────────────────

func sheetCallout(number, media int) entity.TechCardCallout {
	return entity.TechCardCallout{
		Number:  number,
		MediaId: sql.NullInt32{Int32: int32(media), Valid: true},
		Kind:    entity.AnnotationKindDim,
		Color:   entity.AnnotationColorRed,
		PosX:    decimal.NewNullDecimal(decimal.RequireFromString("0.25")),
		PosY:    decimal.NewNullDecimal(decimal.RequireFromString("0.50")),
	}
}

// ТЕКСТ ЗАМОРОЗКИ — СОСТАВНОЙ, И ЭТА ПРОБА СУЩЕСТВУЕТ ПОТОМУ, ЧТО ЕЁ НЕ БЫЛО.
//
// Все фикстуры заморозки выше держат текстовые поля пустыми, поэтому первая реализация,
// замораживавшая одно `description`, была зелёной у всех шести проб. Указание-мерка — `part`
// заполнен, `description` ПУСТ, `dimensions` = «6 мм» — уезжало в НЕОБРАТИМУЮ версию без единого
// символа: номерная плашка, под которой ничего не написано.
func dimensionCallout(number, media int) entity.TechCardCallout {
	c := sheetCallout(number, media)
	c.Part = sql.NullString{String: "полочка", Valid: true}
	c.Dimensions = sql.NullString{String: "6 мм", Valid: true}
	return c
}

func TestFrozenCalloutCarriesTheComposedLineNotJustTheDescription(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}

	frozen, err := freezeCallouts([]entity.TechCardCallout{dimensionCallout(1, 55)}, plates, nil)
	require.NoError(t, err)
	require.Len(t, frozen, 1)
	require.Equal(t, "полочка (6 мм)", frozen[0].Text.String,
		"указание-мерка обязано замёрзнуть с текстом; пустая строка тут — плашка без слов на бумаге")

	// НЕСКОЛЬКО ДЕТАЛЕЙ — через запятую, как их печатает клиент. Совпадение посимвольное, потому
	// что экран и бумага под одной подписью обязаны читаться одинаково.
	many := dimensionCallout(3, 55)
	many.Parts = []string{"полочка", "спинка"}
	frozen, err = freezeCallouts([]entity.TechCardCallout{many}, plates, nil)
	require.NoError(t, err)
	require.Equal(t, "полочка, спинка (6 мм)", frozen[0].Text.String)

	// И полная форма: деталь, что с ней, и мерка — все три в одной строке, мерка в конце.
	full := dimensionCallout(2, 55)
	full.Description = sql.NullString{String: "обтачка, разрез", Valid: true}
	frozen, err = freezeCallouts([]entity.TechCardCallout{full}, plates, nil)
	require.NoError(t, err)
	require.Len(t, frozen, 1)
	require.Equal(t, "полочка: обтачка, разрез (6 мм)", frozen[0].Text.String)
}

// СОБСТВЕННЫЙ `text` АННОТАЦИИ ОБЯЗАН БЫТЬ ПУСТ — так велит контракт, и иначе заметка печатается
// дважды: у фигуры и в списке.
func TestFrozenAnnotationLeavesItsOwnTextEmpty(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	c := dimensionCallout(1, 55)
	c.Description = sql.NullString{String: "обтачка", Valid: true}

	frozen, err := freezeCallouts([]entity.TechCardCallout{c}, plates, nil)
	require.NoError(t, err)
	require.Len(t, frozen, 1)
	require.NotContains(t, string(frozen[0].Annotation), "обтачка",
		"аннотация не должна нести печатную заметку: контракт держит её в DesignSheetCallout.text")
}

// МОРОЗЯТСЯ ВЫНОСКИ НА МЕДИА ПЛИТ — И ТОЛЬКО ОНИ.
func TestFreezeTakesCalloutsOnPlateMedia(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	frozen, err := freezeCallouts([]entity.TechCardCallout{sheetCallout(1, 55)}, plates, nil)
	require.NoError(t, err)
	require.Len(t, frozen, 1)
	require.Equal(t, 1, frozen[0].Number)
	require.Equal(t, 55, frozen[0].MediaId)
}

// МУДБОРДНАЯ ВЫНОСКА НЕ ЕДЕТ НА БУМАГУ И НЕ ЗАПИРАЕТ МИНТ.
//
// Это и есть граница П-Е. Отказывать по всем выноскам вне плит значило бы сделать минт
// недостижимым на КАЖДОЙ карточке с мудбордом — то есть на всех.
func TestFreezeIgnoresCalloutsThatWereNeverPlates(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	frozen, err := freezeCallouts([]entity.TechCardCallout{sheetCallout(1, 55), sheetCallout(2, 77)}, plates, nil)
	require.NoError(t, err, "выноска на медиа, которое никогда не было плитой, минт не запирает")
	require.Len(t, frozen, 1)
	require.Equal(t, 55, frozen[0].MediaId)
}

// НЕЗАПИНЕННАЯ ВЫНОСКА НА БУМАГУ НЕ ЕДЕТ: она не показывает НА ЧТО.
func TestFreezeSkipsUnanchoredCallouts(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	frozen, err := freezeCallouts([]entity.TechCardCallout{{Number: 3}}, plates, nil)
	require.NoError(t, err)
	require.Empty(t, frozen)
}

// ВЫНОСКА НА ЗАМЕНЁННОМ МЕДИА ОСТАНАВЛИВАЕТ МИНТ И НАЗЫВАЕТ НОМЕРА.
//
// Ровно у неё есть потерянный адрес: она указывала на плиту ПРОШЛОЙ версии, а этот состав её
// вытеснил. «Часть выносок потеряна» без номеров человеку нечем закрыть.
func TestFreezeRefusesCalloutsOnAReplacedPlate(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 56)}
	prev := map[int]bool{55: true}
	_, err := freezeCallouts([]entity.TechCardCallout{sheetCallout(3, 55), sheetCallout(5, 55)}, plates, prev)
	require.ErrorIs(t, err, entity.ErrDesignUnrepinnedCallouts)
	var refusal *entity.DesignMintRefusal
	require.ErrorAs(t, err, &refusal)
	require.Equal(t, "3,5", refusal.Metadata["numbers"])
}

// ТА ЖЕ ПЛИТА В НОВОЙ ВЕРСИИ — НИЧЕГО НЕ ЗАМЕНЕНО, ВЫНОСКА ПЕРЕЕЗЖАЕТ МОЛЧА.
// Без этой половины повторный минт неизменного состава отказывал бы всегда.
func TestFreezeCarriesCalloutsWhenThePlateDidNotChange(t *testing.T) {
	plates := []mintPlate{silhouettePlate(entity.DesignViewFront, 55)}
	frozen, err := freezeCallouts([]entity.TechCardCallout{sheetCallout(3, 55)}, plates, map[int]bool{55: true})
	require.NoError(t, err)
	require.Len(t, frozen, 1)
}

// ─────────────────────── ГЕОМЕТРИЯ НА БУМАГЕ ───────────────────────

// ЗАМОРОЖЕННАЯ ГЕОМЕТРИЯ ГОВОРИТ ЯЗЫКОМ ПРОВОДА, А НЕ ЯЗЫКОМ КОЛОНОК.
//
// Читатель версии разбирает эту колонку в common.TechCardAnnotation с DiscardUnknown — то есть
// самодельный объект с ХРАНИМЫМИ строками («dim») разбирается БЕЗ ОШИБКИ в ПУСТОЕ сообщение, и
// бумага теряет каждую мерку и каждую скобку молча. Проба смотрит на ИМЯ ЭНУМА, потому что именно
// оно и отличает эти два исхода; на «валидный JSON» она зеленела бы в обоих.
func TestFrozenAnnotationSpeaksTheWireDialect(t *testing.T) {
	frozen, err := freezeCallouts([]entity.TechCardCallout{sheetCallout(1, 55)},
		[]mintPlate{silhouettePlate(entity.DesignViewFront, 55)}, nil)
	require.NoError(t, err)
	require.Len(t, frozen, 1)

	var probe map[string]any
	require.NoError(t, json.Unmarshal(frozen[0].Annotation, &probe),
		"геометрия уедет в JSON-колонку: %s", frozen[0].Annotation)
	require.Equal(t, "TECH_CARD_ANNOTATION_KIND_DIM", probe["kind"],
		"вид записан хранимой строкой, а не именем энума: читатель разберёт это в ПУСТУЮ выноску, "+
			"без единой ошибки, и лист напечатается без мерок")
	require.NotEmpty(t, probe["labelX"], "положение плашки обязано доехать")
}

// ─────────────────────── ВХОДНЫЕ ОТКАЗЫ ───────────────────────

// НЕЗАКОННЫЙ АКТ МИНТА ОТВЕРГАЕТСЯ ДО ЛЮБОЙ РАБОТЫ: словарь minted_via закрыт, потому что
// журнальная строка `minted` обязана быть достижима ТОЛЬКО минтом.
func TestMintedViaDictionaryIsClosed(t *testing.T) {
	require.True(t, entity.IsDesignMintedVia(entity.DesignMintedViaPrint))
	require.False(t, entity.IsDesignMintedVia("whatever"))
	require.False(t, entity.IsDesignMintedVia(entity.DesignIssueMinted),
		"`minted` это ДЕЙСТВИЕ ЖУРНАЛА, а не акт минта — путать их значит дать журналу второй способ соврать")
}

// Сторовые входные проверки отказывают ДО открытия транзакции, поэтому исполняются без базы.
func TestMintSheetVersionRefusesBadInputWithoutTouchingTheDatabase(t *testing.T) {
	s := &Store{}
	for name, req := range map[string]entity.DesignSheetMint{
		"no card":    {ClientRequestId: "r", TechCard: &entity.TechCardInsert{}, MintedVia: entity.DesignMintedViaCallout},
		"no request": {TechCardId: 1, TechCard: &entity.TechCardInsert{}, MintedVia: entity.DesignMintedViaCallout},
		"no document": {TechCardId: 1, ClientRequestId: "r",
			MintedVia: entity.DesignMintedViaCallout},
		"bad act": {TechCardId: 1, ClientRequestId: "r", TechCard: &entity.TechCardInsert{}, MintedVia: "печать"},
	} {
		_, err := s.MintSheetVersion(t.Context(), req)
		require.Error(t, err, name)
		require.True(t,
			errors.Is(err, entity.ErrDesignInvalidArgument),
			"%s: отказ обязан быть InvalidArgument, а не паника на nil-полях стора: %v", name, err)
	}
}

// ─────────────────────── ВОРОТА, КОТОРЫХ НИКТО НЕ ЗОВЁТ, ЗЕЛЕНЕЮТ ВЕЧНО ───────────────────────

// ВСЕ ПРОБЫ ВЫШЕ ЗОВУТ ВОРОТА НАПРЯМУЮ, поэтому ни одна из них не заметит, если вызов исчезнет из
// MintSheetVersion: функции останутся правильными, а защиты не будет. Исполнением это здесь не
// покупается — тело минта требует живой базы, — поэтому проверяется чтением исходника, ровно тем
// же приёмом, каким в этом репозитории проверяется, что щиты количеств вообще зовутся.
//
// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ ОБЯЗАТЕЛЕН: если извлекатель смотрит не туда, «вызова нет» становится
// неотличимо от «файла нет», и проверка тихо перестаёт что-либо измерять.
func TestMintActuallyCallsItsGates(t *testing.T) {
	body, err := os.ReadFile("mint.go")
	require.NoError(t, err, "не читается mint.go — проба обязана упасть, а не «не найти вызовов»")
	src := string(body)
	require.Contains(t, src, "func (s *Store) MintSheetVersion(",
		"извлекатель смотрит не в тот файл — всё ниже зазеленело бы на любой ошибке")

	for _, call := range []string{
		// Ключ ЗАПРОСА, а не что попало: поиск по пустой строке нашёл бы чужую версию или ничего,
		// и идемпотентность исчезла бы, не изменив ни одной строки, которую видно глазом.
		"versionByRequestID(ctx, db, req.ClientRequestId)",
		"casExpectedPlates(slots, req.ExpectedPlates)",
		"mintGates(plates, cardFit.Fit.String, req)",
		"requirePlatesInDocument(plates, req.TechCard)",
		"freezeCallouts(stored, plates, prevPlateMedia)",
		"s.cards.UpdateTechCardTx(ctx, rep, req.TechCardId, req.TechCard, req.ExpectedLockVersion)",
	} {
		require.Equal(t, 1, strings.Count(src, call),
			"вызов %q не найден ровно один раз: ворота останутся правильными, а защиты не будет", call)
	}
}
