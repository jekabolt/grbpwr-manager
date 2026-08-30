package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func ref(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }

func pt(x, y string) entity.TechCardAnnotationPoint {
	return entity.TechCardAnnotationPoint{X: decimal.RequireFromString(x), Y: decimal.RequireFromString(y)}
}

// ЛЕГАСИ-НОЛЬ БЕЗ КЛЮЧА НЕ ТРОГАЮТ НИКОГДА. Это не «краевой случай», а весь прод: в
// tech_card_callout лежит callout_number NOT NULL DEFAULT 0 без UNIQUE, дублирующиеся нули там
// законны, и правило «ноль означает сминти» перенумеровало бы их первым же сохранением с любого
// клиента — то есть сдвинуло бы подпись DESIGN массово, в момент выката.
func TestMintLeavesLegacyZeroesAlone(t *testing.T) {
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, Part: ref("полочка")},
		{Number: 0, Part: ref("спинка")},
	}}
	MintCalloutNumbers(&entity.TechCard{}, tc)

	require.Equal(t, 0, tc.Callouts[0].Number, "легаси-ноль без ключа строки обязан остаться нулём")
	require.Equal(t, 0, tc.Callouts[1].Number, "второй легаси-ноль — тоже, дубли нулей законны")
	require.Equal(t, 0, tc.CalloutSeq, "счётчик не двигается там, где ничего не присвоено")
}

// КЛЮЧ СТРОКИ ПРИСВОИЛ НОМЕР, и сам ключ уехал в сущность целым — иначе форма после сохранения не
// поймёт, какой её строке достался какой номер.
func TestMintAssignsNumberToClientRefRow(t *testing.T) {
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, ClientRef: ref("abc")},
	}}
	MintCalloutNumbers(&entity.TechCard{}, tc)

	require.GreaterOrEqual(t, tc.Callouts[0].Number, 1, "строка с ключом обязана получить номер")
	require.Equal(t, "abc", tc.Callouts[0].ClientRef.String, "ключ строки обязан пережить минт")
	require.Equal(t, tc.Callouts[0].Number, tc.CalloutSeq, "счётчик обязан догнать присвоенное")
}

// КРУГЛЫЙ РЕЙС КЛЮЧА: то, что клиент прислал, он обязан прочитать обратно — в этом весь смысл
// хранения ключа, и без обратной половины провод молча теряет его.
func TestClientRefRoundTripsThroughTheWire(t *testing.T) {
	parsed, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name:            "Field Jacket",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		Callouts: []*pb_common.TechCardCallout{
			{Number: 0, Part: "collar", ClientRef: "row-uuid-1"},
		},
	})
	require.NoError(t, err)
	require.Equal(t, "row-uuid-1", parsed.Callouts[0].ClientRef.String, "ключ обязан доехать до сущности")

	back := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *parsed}, CostingFx{Base: "EUR"})
	require.Equal(t, "row-uuid-1", back.GetTechCard().GetCallouts()[0].GetClientRef(),
		"ключ обязан вернуться круглым рейсом")
}

// СЧЁТЧИК МОНОТОНЕН: GREATEST(хранимый, MAX(номеров payload'а), присвоенные). Без максимума по
// payload'у сервер выдал бы номер, который клиент старой схемы («максимум по своему экрану») уже
// нарисовал на эскизе.
func TestMintSeqIsGreatestOfStoredIncomingAndAssigned(t *testing.T) {
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 9},
		{Number: 0, ClientRef: ref("a")},
		{Number: 0, ClientRef: ref("b")},
	}}
	MintCalloutNumbers(&entity.TechCard{TechCardInsert: entity.TechCardInsert{CalloutSeq: 4}}, tc)

	require.Equal(t, 10, tc.Callouts[1].Number)
	require.Equal(t, 11, tc.Callouts[2].Number)
	require.Equal(t, 11, tc.CalloutSeq)

	// Хранимый счётчик выше всего, что видно в payload'е: карточка, у которой выноски удалили, не
	// имеет права раздать их номера заново.
	tc2 := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{{Number: 0, ClientRef: ref("c")}}}
	MintCalloutNumbers(&entity.TechCard{TechCardInsert: entity.TechCardInsert{CalloutSeq: 40}}, tc2)
	require.Equal(t, 41, tc2.Callouts[0].Number, "номер, уже отданный клиенту, не достаётся второй выноске")
}

// НОМЕР СМИНЧЕН ДО ДАЙДЖЕСТА — самая важная проба куска.
//
// Отпечаток DESIGN хеширует c.Number ЯВНО. Если подпись поставить раньше минта, она посчитается по
// нулю, а в колонку уедет единица: свежая подпись РОЖДАЕТСЯ ПРОТУХШЕЙ и не лечится переутверждением,
// потому что повторный штамп берёт то же расхождение. Проба сравнивает поставленный отпечаток с
// пересчётом по СОХРАНЁННЫМ номерам — то есть по тем, что реально уедут в базу.
func TestCalloutNumberIsMintedBeforeTheDesignDigest(t *testing.T) {
	payload := func() *entity.TechCardInsert {
		return &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
			{Number: 0, ClientRef: ref("abc"), Part: ref("полочка")},
		}}
	}

	// Порядок хендлера: минт → штамп.
	tc := payload()
	MintCalloutNumbers(&entity.TechCard{}, tc)
	stamped := TechCardSectionDigestsAsRead(tc, nil)[entity.SignoffDesign]

	// Пересчёт по СОХРАНЁННЫМ номерам — то, что увидит следующее чтение карточки.
	asSaved := TechCardSectionDigests(tc)[entity.SignoffDesign]
	require.Equal(t, asSaved, stamped,
		"подпись обязана совпадать с пересчётом по сохранённым номерам, иначе она рождается протухшей")

	// А теперь ровно та перестановка, которую проба обязана различать: штамп раньше минта.
	wrong := payload()
	stampedTooEarly := TechCardSectionDigestsAsRead(wrong, nil)[entity.SignoffDesign]
	MintCalloutNumbers(&entity.TechCard{}, wrong)
	require.NotEqual(t, TechCardSectionDigests(wrong)[entity.SignoffDesign], stampedTooEarly,
		"если отпечаток по нулю совпадает с отпечатком по сминченному номеру, проба сторожит мёртвый код")

	// И ГРАНИЦА, из-за которой эта перестановка невозможна молча: несминченный payload объявляет
	// себя таковым, и хендлер отказывается штамповать по нему.
	require.True(t, CalloutsAwaitingNumber(payload()), "несминченная выноска обязана быть видна границе")
	require.False(t, CalloutsAwaitingNumber(tc), "после минта ждать нечего")
	require.False(t, CalloutsAwaitingNumber(&entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{{Number: 0}},
	}), "легаси-ноль без ключа границу не трогает — он не ждёт номера, он его не имеет")
}

// МИНТ РАНЬШЕ carryOmitted*. CarryOmittedCalloutGeometry сопоставляет выноски ПО НОМЕРУ, и новая
// строка с нулём подцепила бы геометрию ЛЕГАСИ-НУЛЯ — чужую мерку с чужой картинки, молча.
func TestMintRunsBeforeCalloutGeometryCarry(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{{
			Number: 0, // легаси-ноль, у которого нарисована мерка
			Kind:   entity.AnnotationKindDim,
			Points: []entity.TechCardAnnotationPoint{pt("0.1", "0.1"), pt("0.4", "0.4")},
			Color:  entity.AnnotationColorRed,
		}},
	}}
	// Вкладка со старым бандлом про геометрию молчит (KindOmitted), но строка — НОВАЯ, со своим
	// ключом.
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, ClientRef: ref("abc"), KindOmitted: true},
	}}

	// Порядок хендлера.
	MintCalloutNumbers(stored, tc)
	CarryOmittedCalloutGeometry(stored, tc)

	require.Equal(t, 1, tc.Callouts[0].Number)
	require.Empty(t, tc.Callouts[0].Points, "геометрия легаси-нуля не имеет права переехать на новую выноску")
	require.NotEqual(t, entity.AnnotationColorRed, tc.Callouts[0].Color)

	// Обратный порядок — то, что проба обязана различать.
	swapped := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, ClientRef: ref("abc"), KindOmitted: true},
	}}
	CarryOmittedCalloutGeometry(stored, swapped)
	MintCalloutNumbers(stored, swapped)
	require.Len(t, swapped.Callouts[0].Points, 2,
		"при обратном порядке геометрия переезжает — если нет, проба сторожит мёртвый код")
}

// SIDE_L С ПРОВОДА ДОЕЗЖАЕТ ДО СУЩНОСТИ И ОБРАТНО. Без строки в dto-карте энумов новый вид
// отвергался бы словами «unknown tech card media kind: 9», которые не называют ни картинку, ни то,
// что с ней делать.
func TestSideViewKindsRoundTripOnceTheDictionaryIsWide(t *testing.T) {
	prev := entity.TechCardMediaKindDictExtended
	entity.TechCardMediaKindDictExtended = true
	defer func() { entity.TechCardMediaKindDictExtended = prev }()

	parsed, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name:            "Field Jacket",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		TechnicalMedia: []*pb_common.TechCardMediaItem{
			{MediaId: 11, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SIDE_L},
			{MediaId: 12, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SIDE_R},
			{MediaId: 13, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_RENDER},
		},
	})
	require.NoError(t, err)
	require.Equal(t, entity.TechCardMediaSideL, parsed.Media[0].Kind)
	require.Equal(t, entity.TechCardMediaSideR, parsed.Media[1].Kind)
	require.Equal(t, entity.TechCardMediaRender, parsed.Media[2].Kind)

	require.Equal(t, pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SIDE_L, pbTechCardMediaKind(entity.TechCardMediaSideL))
	require.Equal(t, pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SIDE_R, pbTechCardMediaKind(entity.TechCardMediaSideR))
	require.Equal(t, pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_RENDER, pbTechCardMediaKind(entity.TechCardMediaRender))
}

// ПОКА СЛОВАРЬ КОЛОНКИ УЖЕ ПРОВОДА — ВНЯТНЫЙ ОТКАЗ, а не паника, не тишина и не сырой 3819 от
// chk_tech_card_media_kind, который называет constraint и не называет картинку. Гейт снимается одной
// строкой (entity.TechCardMediaKindDictExtended), потому что 0346 — отделяемый файл.
func TestPendingDictionaryKindIsRefusedInWords(t *testing.T) {
	require.False(t, entity.TechCardMediaKindDictExtended,
		"гейт снимают ВМЕСТЕ с выкаткой 0346; если он снят, а миграция не уехала, отказ приедет из MySQL")

	_, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		Name:            "Field Jacket",
		Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
		MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
		TechnicalMedia: []*pb_common.TechCardMediaItem{
			{MediaId: 11, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_FRONT},
			{MediaId: 12, Kind: pb_common.TechCardMediaKind_TECH_CARD_MEDIA_KIND_SIDE_L},
		},
	})
	require.Error(t, err)
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve)
	require.Equal(t, "technical_media[1].kind", ve.Field, "отказ обязан вести к КОНКРЕТНОЙ картинке")
	require.Contains(t, ve.Message, "side_l")
	require.True(t, strings.Contains(ve.HowToFix, "not enabled on the server yet"),
		"человек обязан узнать, что дело в сервере, а не в его файле")
}

// ЗАМЕТКА МУДБОРДА — VERBATIM-ПРОТОКОЛ «ОТСУТСТВУЕТ = СОХРАНИТЬ», три состояния. Без флага
// присутствия голая proto3-строка от старого бандла приехала бы как "" и стёрла бы заметку молча.
func TestMoodNoteCarriesPresenceNotValue(t *testing.T) {
	base := func(note *string) *pb_common.TechCardInsert {
		return &pb_common.TechCardInsert{
			Name:            "Field Jacket",
			Stage:           pb_common.TechCardStage_TECH_CARD_STAGE_IDEA,
			MeasurementUnit: pb_common.TechCardMeasurementUnit_TECH_CARD_MEASUREMENT_UNIT_MM,
			MoodNote:        note,
		}
	}

	absent, err := ConvertPbTechCardInsertToEntity(base(nil))
	require.NoError(t, err)
	require.True(t, absent.MoodNoteOmitted, "поля не было — колонку трогать нельзя")

	empty := ""
	cleared, err := ConvertPbTechCardInsertToEntity(base(&empty))
	require.NoError(t, err)
	require.False(t, cleared.MoodNoteOmitted, "явная пустая строка — это ОЧИСТИТЬ, а не «молчание»")
	require.False(t, cleared.MoodNote.Valid)

	text := "рабочая одежда 70-х, выбеленный хлопок"
	set, err := ConvertPbTechCardInsertToEntity(base(&text))
	require.NoError(t, err)
	require.False(t, set.MoodNoteOmitted)
	require.Equal(t, text, set.MoodNote.String)

	// Круглый рейс: на чтении заметка ПРИСУТСТВУЕТ всегда — иначе новый клиент, вернувший
	// прочитанное, молчал бы про неё по ошибке.
	back := ConvertEntityTechCardToPb(&entity.TechCard{TechCardInsert: *set}, CostingFx{Base: "EUR"})
	require.NotNil(t, back.GetTechCard().MoodNote, "на чтении поле обязано присутствовать")
	require.Equal(t, text, back.GetTechCard().GetMoodNote())

	blank := ConvertEntityTechCardToPb(&entity.TechCard{}, CostingFx{Base: "EUR"})
	require.NotNil(t, blank.GetTechCard().MoodNote, "пустая колонка читается как присутствующее \"\"")
	require.Equal(t, "", blank.GetTechCard().GetMoodNote())
}

// КЛЮЧ СТРОКИ НЕ ВХОДИТ В ОТПЕЧАТОК DESIGN. Это АДРЕС, а не содержание, ровно как цвет выноски:
// захешируй его — и подпись протухала бы от того, что клиент перевыдал себе ключ.
func TestClientRefIsNotHashed(t *testing.T) {
	with := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{{Number: 1, ClientRef: ref("abc")}}}
	without := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{{Number: 1}}}
	require.Equal(t,
		TechCardSectionDigests(without)[entity.SignoffDesign],
		TechCardSectionDigests(with)[entity.SignoffDesign])
}

// ЗАМЕТКА МУДБОРДА ТОЖЕ НЕ ВХОДИТ В ОТПЕЧАТОК (Д4 отозвано — проекция DESIGN не менялась вовсе).
// Добавь её туда — и КАЖДАЯ подписанная секция DESIGN объявилась бы отредактированной после
// подписания, на всех карточках разом и в момент деплоя.
func TestMoodNoteIsNotHashed(t *testing.T) {
	with := &entity.TechCardInsert{MoodNote: ref("выбеленный хлопок")}
	require.Equal(t,
		TechCardSectionDigests(&entity.TechCardInsert{})[entity.SignoffDesign],
		TechCardSectionDigests(with)[entity.SignoffDesign])
}

func mid(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

// ПЕРЕНОС СОСТОЯЛСЯ. Вкладка со старым бандлом пересылает выноски без ключа; без переноса полная
// замена обнулила бы колонку, и адреса, которые новый клиент уже держит, исчезли бы молча — ключ
// вне дайджеста, ни одна подпись про это не скажет.
func TestClientRefIsCarriedOntoASilentPayload(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{{Number: 7, MediaId: mid(11), ClientRef: ref("abc")}},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 7, MediaId: mid(11)}, // старый бандл: поля нет вовсе
	}}

	MintCalloutNumbers(stored, tc)
	CarryOmittedCalloutClientRef(stored, tc)

	require.Equal(t, "abc", tc.Callouts[0].ClientRef.String, "ключ обязан пережить сейв со старого бандла")
	require.Equal(t, 7, tc.Callouts[0].Number, "перенос не имеет права трогать номер")
}

// ЗАМЕЩЕНИЕ РАБОТАЕТ. Явно присланное НЕПУСТОЕ значение — настоящая пере-адресация, а не умолчание.
// Съешь её «переносом» — и ключ станет вечным и неисправимым, притом молча.
func TestExplicitClientRefReplacesTheStoredOne(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{{Number: 7, MediaId: mid(11), ClientRef: ref("abc")}},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 7, MediaId: mid(11), ClientRef: ref("xyz")},
	}}

	CarryOmittedCalloutClientRef(stored, tc)
	require.Equal(t, "xyz", tc.Callouts[0].ClientRef.String, "клиент обязан мочь перевыдать строке ключ")
}

// ПЕРЕНОС НЕ ПЕРЕСЕКАЕТ ЭСКИЗЫ — ради этого всё остальное и написано.
//
// Номер выноски НЕ УНИКАЛЕН по карточке: эскиз и мудборд нумеруются независимо, схема дубли не
// запрещает. Перенос по ГОЛОМУ НОМЕРУ не потерял бы ключ, а ПОДМЕНИЛ его: мудбордная записка
// унаследовала бы адрес технической выноски, и форма нового клиента после сейва подсвечивала бы не
// ту строку — ровно тот дефект, ради которого ключ и заведён.
func TestClientRefCarryDoesNotCrossSketches(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{
			{Number: 7, MediaId: mid(11), ClientRef: ref("technical-abc")}, // технический эскиз
		},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 7, MediaId: mid(20)}, // ТОТ ЖЕ номер, но на мудбордной картинке
	}}

	CarryOmittedCalloutClientRef(stored, tc)

	require.False(t, tc.Callouts[0].ClientRef.Valid && tc.Callouts[0].ClientRef.String != "",
		"адрес технической выноски не имеет права переехать на мудбордную с тем же номером")

	// Контроль: на СВОЁЙ картинке тот же номер переносится — значит проба ловит границу эскиза, а не
	// отсутствие переноса вообще.
	same := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{{Number: 7, MediaId: mid(11)}}}
	CarryOmittedCalloutClientRef(stored, same)
	require.Equal(t, "technical-abc", same.Callouts[0].ClientRef.String)
}

// МИНТ И ПЕРЕНОС НЕ СПОРЯТ. Три строки в одном payload'е, три разных исхода, и ни одна не задевает
// соседнюю: минт — `number == 0` при непустом ключе, перенос — `number != 0` при пустом, легаси-ноль
// не подходит ни под одно правило.
func TestMintAndClientRefCarryDoNotCollide(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		CalloutSeq: 7,
		Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: mid(11)},                          // легаси-ноль, ключа нет и не было
			{Number: 7, MediaId: mid(11), ClientRef: ref("old-7")}, // старая выноска с ключом
			// Хранимый двойник ТОГО НОМЕРА, который счётчик выдаст новой строке. Он здесь затем,
			// чтобы «минт и перенос не спорят» проверялось, а не подразумевалось: перенос, съедающий
			// явно присланное значение, отдал бы новой строке ЭТОТ адрес.
			{Number: 8, MediaId: mid(11), ClientRef: ref("stale-8")},
		},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, MediaId: mid(11)},                        // легаси-ноль: не трогать
		{Number: 0, MediaId: mid(11), ClientRef: ref("new")}, // новая строка: сминтить номер
		{Number: 7, MediaId: mid(11)},                        // старый бандл молчит: перенести ключ
	}}

	MintCalloutNumbers(stored, tc)
	CarryOmittedCalloutClientRef(stored, tc)

	require.Equal(t, 0, tc.Callouts[0].Number, "легаси-ноль не трогают ни минтом, ни переносом")
	require.False(t, tc.Callouts[0].ClientRef.Valid && tc.Callouts[0].ClientRef.String != "",
		"легаси-нолю переносить нечего и неоткуда")

	require.Equal(t, 8, tc.Callouts[1].Number, "новая строка получает номер от счётчика")
	require.Equal(t, "new", tc.Callouts[1].ClientRef.String, "сминченной строке чужой ключ не переезжает")

	require.Equal(t, 7, tc.Callouts[2].Number)
	require.Equal(t, "old-7", tc.Callouts[2].ClientRef.String, "молчащая старая строка получает свой ключ")
}

// НОЛЬ ПОЛУЧАЕТ АДРЕС, ТОЛЬКО ЕСЛИ ОН ОДИН. Прежняя проба здесь запрещала это вовсе, и её довод
// был верен ДОСЛОВНО: «пара (номер 0, непустой ключ) этим бинарём не создаётся — минт присваивает
// номер всему, у чего ключ есть». Волна сделала премису ложной: минт больше НЕ присваивает номер
// заметке мудборда (entity.CalloutTakesSheetNumber), то есть пара создаётся этим же бинарём и
// является нормой, а не порчей.
//
// Опасение, стоявшее за прежним запретом, никуда не делось и обслуживается точнее: «раздать адреса
// нулям значило бы объявить неразличимые строки одной и той же» верно ровно тогда, когда нулей на
// одной картинке НЕСКОЛЬКО. Один ноль на картинке различим — это та же строка при полной замене, и
// вернуть ей её собственный адрес не значит ничего объявить.
func TestZeroCalloutTakesItsAddressOnlyWhenItIsTheOnlyOne(t *testing.T) {
	t.Run("один ноль — свой адрес возвращается", func(t *testing.T) {
		stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
			Callouts: []entity.TechCardCallout{{Number: 0, MediaId: mid(11), ClientRef: ref("only")}},
		}}
		tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{{Number: 0, MediaId: mid(11)}}}

		CarryOmittedCalloutClientRef(stored, tc)

		require.Equal(t, "only", tc.Callouts[0].ClientRef.String,
			"это ТА ЖЕ строка при полной замене; отнять у неё адрес — потерять личность заметки мудборда")
	})

	t.Run("несколько нулей — адресов не даёт никому", func(t *testing.T) {
		stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
			Callouts: []entity.TechCardCallout{
				{Number: 0, MediaId: mid(11), ClientRef: ref("a")},
				{Number: 0, MediaId: mid(11), ClientRef: ref("b")},
			},
		}}
		tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: mid(11)},
			{Number: 0, MediaId: mid(11)},
		}}

		CarryOmittedCalloutClientRef(stored, tc)

		require.Empty(t, tc.Callouts[0].ClientRef.String,
			"нули на одной картинке неразличимы: выданный адрес подсветит человеку чужую строку")
		require.Empty(t, tc.Callouts[1].ClientRef.String)
	})
}

// ЗАМЕТКА МУДБОРДА ТОЖЕ НЕ ТЕРЯЕТ КЛЮЧ. Она навсегда `Number == 0`, потому что номер листа ей не
// положен; прежний guard `c.Number == 0` отказывался её трогать, и любое сохранение из вкладки со
// старым бандлом стирало её идентичность — молча и без сдвига подписи, потому что ключ в дайджест
// не входит.
func TestMoodboardNoteKeepsItsClientRefThroughAnOldBundle(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{{Number: 0, MediaId: mid(20), ClientRef: ref("mood-1")}},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, MediaId: mid(20)}, // старый бандл: поля нет вовсе
	}}

	CarryOmittedCalloutClientRef(stored, tc)

	require.Equal(t, "mood-1", tc.Callouts[0].ClientRef.String,
		"без переноса заметка теряет личность при каждом сейве со старого бандла")
	require.Equal(t, 0, tc.Callouts[0].Number, "номер листа заметке мудборда не положен")
}

// ДВА НУЛЯ НА ОДНОЙ КАРТИНКЕ АДРЕСОВ НЕ ПОЛУЧАЮТ — И ЭТО НЕ ТА ЖЕ РАЗВЯЗКА, ЧТО У ГЕОМЕТРИИ.
//
// Позиционного сопоставления хватает, когда цена ошибки — потерянная фигура: она ВИДНА. Для адреса
// строки цена другая: ошибиться им значит подсветить человеку ЧУЖУЮ строку, то есть воспроизвести
// ровно тот дефект, ради которого адрес заведён. Позиция держится на порядке, порядок — на
// честности клиента, поэтому при неоднозначном ключе адрес не выдаётся вовсе.
//
// Заметка мудборда от этого не страдает: одна на картинке — обычный случай, и её ключ однозначен
// (см. TestMoodboardNoteKeepsItsClientRefThroughAnOldBundle).
func TestTwoZeroCalloutsSharingOneKeyGetNoAddressAtAll(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: mid(20), ClientRef: ref("first")},
			{Number: 0, MediaId: mid(20), ClientRef: ref("second")},
		},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, MediaId: mid(20)},
		{Number: 0, MediaId: mid(20)},
	}}

	CarryOmittedCalloutClientRef(stored, tc)

	require.Empty(t, tc.Callouts[0].ClientRef.String, "неоднозначный ключ адреса не даёт")
	require.Empty(t, tc.Callouts[1].ClientRef.String,
		"и второй тоже: выдать здесь адрес значит рискнуть подсветить человеку чужую строку")
}

// А ВОТ ГЕОМЕТРИЯ ПРИ ТОМ ЖЕ ВХОДЕ РАЗВЯЗЫВАЕТСЯ ПОЗИЦИОННО, И КАЖДЫЙ ПОЛУЧАЕТ СВОЮ.
// Расхождение с правилом выше намеренное и держится на цене ошибки: потерянная фигура видна,
// подменённая — нет; для адреса ровно наоборот.
func TestTwoZeroCalloutsKeepTheirOwnGeometry(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Callouts: []entity.TechCardCallout{
			{Number: 0, MediaId: mid(20), Kind: entity.AnnotationKindDim, Dashed: true},
			{Number: 0, MediaId: mid(20), Kind: entity.AnnotationKindArc, Filled: true},
		},
	}}
	tc := &entity.TechCardInsert{Callouts: []entity.TechCardCallout{
		{Number: 0, MediaId: mid(20), KindOmitted: true},
		{Number: 0, MediaId: mid(20), KindOmitted: true},
	}}

	CarryOmittedCalloutGeometry(stored, tc)

	require.Equal(t, entity.AnnotationKindDim, tc.Callouts[0].Kind)
	require.True(t, tc.Callouts[0].Dashed, "первая обязана сохранить СВОЮ фигуру, а не потерять её")
	require.Equal(t, entity.AnnotationKindArc, tc.Callouts[1].Kind,
		"вторая обязана получить свою, а не первую")
	require.True(t, tc.Callouts[1].Filled)
}
