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
	prev := techCardMediaKindDictExtended
	techCardMediaKindDictExtended = true
	defer func() { techCardMediaKindDictExtended = prev }()

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
// строкой (techCardMediaKindDictExtended), потому что 0346 — отделяемый файл.
func TestPendingDictionaryKindIsRefusedInWords(t *testing.T) {
	require.False(t, techCardMediaKindDictExtended,
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
