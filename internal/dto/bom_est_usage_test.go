package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ОЦЕНКА РАСХОДА НА ИЗДЕЛИЕ НА ПРОВОДЕ (0365, B-16).
//
// Здесь проверяются ровно четыре вещи, и каждая — граница, а не «ещё немного покрытия»:
// контракт присутствия (очистить ≠ не прислать), отказ на отрицательном и на лишнем знаке,
// круговой рейс entity → pb → entity (иначе клон и сезонная копия теряют колонку молча) и —
// главное — ПОДПИСЬ MATERIALS, которая от новой колонки НЕ ДВИГАЕТСЯ.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (каждая прогнана и откачена; ни одна не осталась зелёной):
//  1. `estUsageOmitted := !estUsage.Valid` (наивный флаг ПО ЗНАЧЕНИЮ вместо указателя) —
//     TestEstUsageWirePresenceContract краснеет на очистке: поле становится неочищаемым;
//  2. снять отказ `validateDecimalFits(... , false)` (передать signed=true) —
//     TestEstUsageWirePresenceContract краснеет на «-1»;
//  3. убрать `EstUsage: pbDecimalFromNull(b.EstUsage)` из techCardBomItemsToPb —
//     TestEstUsageSurvivesTheEntityWireRoundTrip краснеет: число не возвращается чтением;
//  4. дописать est_usage хвостом в materialsRow — TestEstUsageDoesNotStaleTheMaterialsSignature
//     краснеет: подпись каждой карточки в базе сдвинулась бы от совещательного числа.

func euPbDec(v string) *pb_decimal.Decimal { return &pb_decimal.Decimal{Value: v} }

func euNullDec(v string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
}

// euParse — одна дверь: строка спеки минимальной законной формы плюс то, что проверяется.
func euParse(t *testing.T, b *pb_common.TechCardBomItem) entity.TechCardBomItem {
	t.Helper()
	b.Section = pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC
	b.Name = "main fabric"
	out, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{b})
	require.NoError(t, err)
	require.Len(t, out, 1)
	return out[0]
}

// TestEstUsageWirePresenceContract — ТА ЖЕ ЛОВУШКА ПРОВОДА, ЧТО У СЧЁТНОЙ ПАРЫ, только присутствие
// здесь одиночное. У google.type.Decimal нет `optional`, nullDecimalFromPb считает пустым И nil, И
// Decimal{Value:""}, поэтому «очистить» и «не прислали» различаются ТОЛЬКО указателем. Флаг по
// значению сделал бы колонку неочищаемой навсегда.
func TestEstUsageWirePresenceContract(t *testing.T) {
	// Поля нет на проводе — вкладка со старым бандлом. «Не трогай»: store оставит колонку как лежит.
	got := euParse(t, &pb_common.TechCardBomItem{})
	require.True(t, got.EstUsageOmitted, "отсутствие поля означает «не трогай сохранённое»")
	require.False(t, got.EstUsage.Valid)

	// Значение пришло — пишем.
	got = euParse(t, &pb_common.TechCardBomItem{EstUsage: euPbDec("1.6")})
	require.False(t, got.EstUsageOmitted)
	require.Equal(t, "1.6", got.EstUsage.Decimal.String())

	// ЯВНАЯ ПУСТОТА = ОЧИСТИТЬ, и это единственная дверь, через которую оценка снимается со строки.
	got = euParse(t, &pb_common.TechCardBomItem{EstUsage: euPbDec("")})
	require.False(t, got.EstUsageOmitted, "явная пустота — распоряжение, а не молчание")
	require.False(t, got.EstUsage.Valid)

	// Ноль — законное утверждение «нисколько», а не отсутствие.
	got = euParse(t, &pb_common.TechCardBomItem{EstUsage: euPbDec("0")})
	require.False(t, got.EstUsageOmitted)
	require.True(t, got.EstUsage.Valid)
	require.Equal(t, "0", got.EstUsage.Decimal.String())

	// Отрицательного расхода не бывает: отказ ПОЛЕМ, а не сырым MySQL — иначе оператор увидит
	// «ошибка сохранения карточки» без адреса.
	_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{{
		Section:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		Name:     "main fabric",
		EstUsage: euPbDec("-1"),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bom_items[0].est_usage")

	// Четвёртый знак после точки колонка не хранит — молча пропасть он не имеет права.
	_, err = parseTechCardBomItems([]*pb_common.TechCardBomItem{{
		Section:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		Name:     "main fabric",
		EstUsage: euPbDec("1.6001"),
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bom_items[0].est_usage")
}

// TestEstUsageIsLegalOnEverySection — оценка НЕ счётная норма: у неё нет семьи. Вопрос «сколько на
// изделие, примерно» одинаково законен на ткани (метры), на нитке (метры) и на молнии (штуки), и
// секционного сторожа у него быть не должно — иначе мы получили бы ложное расщепление одного поля
// на «оценку тканей» и «оценку фурнитуры».
func TestEstUsageIsLegalOnEverySection(t *testing.T) {
	for _, sec := range []pb_common.TechCardBomSection{
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_THREAD,
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
		pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_LABEL,
	} {
		out, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{{
			Section: sec, Name: "slot", EstUsage: euPbDec("2.5"),
		}})
		require.NoError(t, err, "секция %s", sec)
		require.Equal(t, "2.5", out[0].EstUsage.Decimal.String(), "секция %s", sec)
	}
}

// TestEstUsageSurvivesTheEntityWireRoundTrip — КЛОН СТРОИТ PAYLOAD САМ, теми же конвертерами
// (ConvertEntityTechCardToPb → ConvertPbTechCardInsertToEntity) и транспортных флагов не эмитит.
// Колонка, забытая в читателе, уехала бы в клон пустой, а на сохранении круговым рейсом ещё и
// стёрла бы себя на исходной карточке.
func TestEstUsageSurvivesTheEntityWireRoundTrip(t *testing.T) {
	card := &entity.TechCard{Id: 7}
	card.Name = "Shirt"
	card.StyleNumber = sql.NullString{String: "S-7", Valid: true}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 101, Name: "main fabric", Section: entity.BomSectionFabric,
			Unit: sql.NullString{String: "m", Valid: true}, EstUsage: euNullDec("1.6")},
		{Id: 102, Name: "care label", Section: entity.BomSectionLabel},
	}

	full := ConvertEntityTechCardToPb(card, CostingFx{Base: "EUR"})
	require.NotNil(t, full.GetTechCard())
	wire := full.GetTechCard().BomItems
	require.Len(t, wire, 2)
	require.NotNil(t, wire[0].EstUsage, "заполненная оценка обязана вернуться чтением")
	require.Equal(t, "1.6", wire[0].EstUsage.Value)
	require.Nil(t, wire[1].EstUsage, "незаполненная едет nil'ом, а не пустой обёрткой")

	back, err := parseTechCardBomItems(wire)
	require.NoError(t, err)
	require.Equal(t, "1.6", back[0].EstUsage.Decimal.String())
	require.False(t, back[0].EstUsageOmitted, "прочитанное значение приезжает присутствующим")
	require.False(t, back[1].EstUsage.Valid)
	require.True(t, back[1].EstUsageOmitted,
		"строка без оценки на круговом рейсе говорит «не трогай», а не «очисти»")
}

// TestEstUsageDoesNotStaleTheMaterialsSignature — ГЛАВНАЯ ГРАНИЦА ВСЕЙ ФАЗЫ.
//
// Подпись секции MATERIALS удостоверяет то, что карточка ПОКУПАЕТ. Оценка ничего не покупает: она
// совещательная, её не читают ни костинг, ни план материалов, ни кат-лист. Попади она в проекцию —
// и отпечаток КАЖДОЙ карточки в базе сдвинулся бы при первом же заполнении графы, то есть
// утверждённые подписи протухли бы от числа, которое никто не обещал.
func TestEstUsageDoesNotStaleTheMaterialsSignature(t *testing.T) {
	base := entity.TechCardInsert{
		Name: "Shirt",
		BomItems: []entity.TechCardBomItem{{
			LineKey: "K0000000000000000000000001",
			Name:    "main fabric", Section: entity.BomSectionFabric,
			Unit: sql.NullString{String: "m", Valid: true},
		}},
	}
	withEstimate := base
	withEstimate.BomItems = []entity.TechCardBomItem{base.BomItems[0]}
	withEstimate.BomItems[0].EstUsage = euNullDec("1.6")

	before := TechCardSectionDigests(&base)[entity.SignoffMaterials]
	after := TechCardSectionDigests(&withEstimate)[entity.SignoffMaterials]
	require.NotEmpty(t, before)
	require.Equal(t, before, after,
		"совещательное число не входит в подпись: иначе правка EST USAGE протухала бы утверждённую спецификацию")

	// Контроль дееспособности пробы: колонка, КОТОРАЯ покупает, отпечаток обязана двигать —
	// иначе равенство выше доказывало бы лишь то, что дайджест ничего не считает.
	moved := base
	moved.BomItems = []entity.TechCardBomItem{base.BomItems[0]}
	moved.BomItems[0].Unit = sql.NullString{String: "kg", Valid: true}
	require.NotEqual(t, before, TechCardSectionDigests(&moved)[entity.SignoffMaterials],
		"единица закупки в подписи есть — если и она не двигает отпечаток, проба смотрит в мёртвый код")
}
