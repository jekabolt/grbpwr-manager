package dto

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ЧАСТЬ СОСТАВНИКА (label_part) на строке BOM — протокол «нет на проводе = не трогай».
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены):
//  1. в bomLabelPartFromPb ветка `p == nil` возвращает omitted=false — краснеет
//     TestBomLabelPartAbsentMeansLeaveStoredValueAlone;
//  2. в techCardBomItemsToPb убрано поле LabelPart — краснеет TestBomLabelPartReadBack;
//  3. ветка UNSPECIFIED возвращает omitted=true — краснеет TestBomLabelPartExplicitUnspecifiedClears;
//  4. из entity.ValidTechCardBomLabelParts убран `trim` — краснеет TestBomLabelPartEnumNoDrift (и
//     TestBomLabelPartDBCheckNoDrift в migrationlint).
// Негативный контроль: TestBomKind* остаются зелёными под каждой мутацией.

// bomLabelPartLine builds one wire BOM line; a nil pointer means the field was NOT SENT, which is
// the subject of these tests and is not the same as sending UNSPECIFIED.
func bomLabelPartLine(p *pb_common.TechCardBomLabelPart) *pb_common.TechCardBomItem {
	return &pb_common.TechCardBomItem{
		LineKey:   "01DGSTBOMLABELPART0000SHL1",
		Section:   pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_FABRIC,
		Name:      "основная ткань",
		LabelPart: p,
	}
}

// Вкладка со старым бандлом поля не шлёт вовсе. Это «не трогай», а не «очисти» — иначе её сейв
// стёр бы выбор части у ВСЕХ строк карточки, бесследно (поле вне дайджеста подписи).
func TestBomLabelPartAbsentMeansLeaveStoredValueAlone(t *testing.T) {
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomLabelPartLine(nil)})
	require.NoError(t, err)
	require.True(t, got[0].LabelPartOmitted, "absent on the wire must mean «keep what is stored»")
	require.False(t, got[0].LabelPart.Valid)
}

// Явно присланный UNSPECIFIED — осознанное «авто»: пишется NULL, флаг «не трогай» снят.
func TestBomLabelPartExplicitUnspecifiedClears(t *testing.T) {
	unspecified := pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_UNSPECIFIED
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomLabelPartLine(&unspecified)})
	require.NoError(t, err)
	require.False(t, got[0].LabelPartOmitted, "an explicitly sent UNSPECIFIED must be written, not preserved")
	require.False(t, got[0].LabelPart.Valid, "UNSPECIFIED is «авто» and must be stored as NULL, never as a string")
}

func TestBomLabelPartPresentIsStoredAsItsEntityString(t *testing.T) {
	shell := pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SHELL
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomLabelPartLine(&shell)})
	require.NoError(t, err)
	require.False(t, got[0].LabelPartOmitted)
	require.Equal(t, sql.NullString{String: "shell", Valid: true}, got[0].LabelPart)

	// Любой раздел может нести часть явно — пары с секцией нет (в отличие от kind).
	notOn := pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_NOT_ON_LABEL
	zip := bomLabelPartLine(&notOn)
	zip.Section = pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE
	got, err = parseTechCardBomItems([]*pb_common.TechCardBomItem{zip})
	require.NoError(t, err)
	require.Equal(t, "not_on_label", got[0].LabelPart.String)
}

// Неизвестное значение (клиент говорит на более новом словаре) ОТВЕРГАЕТСЯ с адресом поля, а не
// деградирует в «авто»: запись не должна молча терять решение оператора.
func TestBomLabelPartUnknownIsRefused(t *testing.T) {
	unknown := pb_common.TechCardBomLabelPart(99)
	_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomLabelPartLine(&unknown)})
	require.Error(t, err)
	require.Contains(t, err.Error(), "bom_items[0].label_part")
}

// Чтение обратно: присутствие всегда явное, NULL → UNSPECIFIED, строка → свой enum, незнакомая
// строка (более новая схема) → UNSPECIFIED, а не какая-то другая часть. И круговой рейс
// parse(read(x)) возвращает x.
func TestBomLabelPartReadBack(t *testing.T) {
	items := []entity.TechCardBomItem{
		{Section: entity.BomSectionFabric, Name: "a", LineKey: "01DGSTBOMLABELPART0000RD01"},
		{Section: entity.BomSectionFabric, Name: "b", LineKey: "01DGSTBOMLABELPART0000RD02",
			LabelPart: sql.NullString{String: "sleeve_lining", Valid: true}},
		{Section: entity.BomSectionFabric, Name: "c", LineKey: "01DGSTBOMLABELPART0000RD03",
			LabelPart: sql.NullString{String: "armpit", Valid: true}},
	}
	pbs := techCardBomItemsToPb(items, nil)
	require.Len(t, pbs, 3)
	for i, pb := range pbs {
		require.NotNil(t, pb.LabelPart, "line %d: the read path must always send presence", i)
	}
	require.Equal(t, pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_UNSPECIFIED, pbs[0].GetLabelPart())
	require.Equal(t, pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_SLEEVE_LINING, pbs[1].GetLabelPart())
	require.Equal(t, pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_UNSPECIFIED, pbs[2].GetLabelPart())

	back, err := parseTechCardBomItems(pbs[:2])
	require.NoError(t, err)
	require.False(t, back[0].LabelPartOmitted)
	require.False(t, back[0].LabelPart.Valid)
	require.False(t, back[1].LabelPartOmitted)
	require.Equal(t, "sleeve_lining", back[1].LabelPart.String)
}

// Часть составника — свойство ленты, в дайджест подписи НЕ входит (как kind): выбор части не должен
// протухать ни одну подпись, и меньше всего MATERIALS.
func TestBomLabelPartIsOutsideEverySectionDigest(t *testing.T) {
	line := func(part string) *entity.TechCardInsert {
		b := entity.TechCardBomItem{LineKey: "01DGSTBOMLABELPART0000DG01", Section: entity.BomSectionFabric, Name: "ткань"}
		if part != "" {
			b.LabelPart = sql.NullString{String: part, Valid: true}
		}
		return &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{b}}
	}
	require.Equal(t, TechCardSectionDigests(line("")), TechCardSectionDigests(line("sleeve_lining")),
		"choosing a label part must not stale any sign-off")
}

// TestBomLabelPartEnumNoDrift — нога entity<->proto: каждое не-UNSPECIFIED значение proto
// отображается в валидную часть entity, три размера совпадают, обратная таблица — истинная
// инверсия. Нога entity<->DB — TestBomLabelPartDBCheckNoDrift в internal/store/migrationlint.
// UNSPECIFIED в таблицу не входит: это «авто», а не значение, и оно обязано становиться NULL.
func TestBomLabelPartEnumNoDrift(t *testing.T) {
	protoValues := 0
	for v, name := range pb_common.TechCardBomLabelPart_name {
		if pb_common.TechCardBomLabelPart(v) == pb_common.TechCardBomLabelPart_TECH_CARD_BOM_LABEL_PART_UNSPECIFIED {
			continue
		}
		protoValues++
		p, ok := techCardBomLabelPartPbToEntity[pb_common.TechCardBomLabelPart(v)]
		if !ok || !entity.ValidTechCardBomLabelParts[p] {
			t.Errorf("proto TechCardBomLabelPart %s maps to invalid entity part %q", name, p)
		}
	}
	require.Equal(t, protoValues, len(techCardBomLabelPartPbToEntity))
	require.Equal(t, protoValues, len(entity.ValidTechCardBomLabelParts))
	for pb, ent := range techCardBomLabelPartPbToEntity {
		require.Equal(t, pb, techCardBomLabelPartEntityToPb[ent], "entity %q must map back", ent)
	}
}
