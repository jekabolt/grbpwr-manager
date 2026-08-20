package dto

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ГОЛДЕН-ЭТАЛОН ПРОЕКЦИИ ОПЕРАЦИИ — заморозка кортежа ПЕРЕД волной новых видов операций.
//
// Зачем он существует. constructionProjection кодирует шаг ПОЗИЦИОННО (json.Marshal пишет []any по
// порядку), поэтому любой безусловный элемент, дописанный в голову или вставленный в середину,
// сдвигает отпечаток КАЖДОГО шага в базе и объявляет все утверждённые подписи CONSTRUCTION
// устаревшими в момент выкатки. Комментарии в techcard_section_digest.go предписывают дописывать
// новое ТОЛЬКО условным тегированным хвостом; этот тест — исполнительный механизм под то
// предписание.
//
// ЭТАЛОН ЗАПИСАН КОРТЕЖЕМ, А НЕ HEX-ХЕШЕМ, НАМЕРЕННО. Хеш сказал бы «отпечаток поехал» и замолчал
// бы о том, ГДЕ именно: упавший diff обязан показывать позицию и значение, иначе чинить его будут
// переписыванием эталона, а не разбором причины. Сравнение идёт через JSON ровно потому, что
// отпечаток берётся с JSON: тест видит то же самое, что увидит sha256, и не спотыкается о разницу
// Go-типов, которая на кодировку не влияет (int32(0) и 0 — один байт `0`).
//
// КАК ЧИТАТЬ ПАДЕНИЕ. Если поехал существующий кейс — проекция уже подписанных карточек сдвинулась,
// и это регресс, а не «эталон устарел». Правка эталона законна ровно в одном случае: когда сдвиг
// осознан, описан в комментарии у места правки в techcard_section_digest.go и волна
// пере-утверждения посчитана. Новый вид операции обязан приходить НОВЫМ кейсом с новым хвостом, а
// все кейсы ниже — остаться байт в байт.

func opGoldStr(s string) sql.NullString { return sql.NullString{String: s, Valid: true} }
func opGoldI32(i int32) sql.NullInt32   { return sql.NullInt32{Int32: i, Valid: true} }
func opGoldBool(b bool) sql.NullBool    { return sql.NullBool{Bool: b, Valid: true} }
func opGoldDec(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}
func opGoldPoint(x, y string) entity.TechCardAnnotationPoint {
	return entity.TechCardAnnotationPoint{
		X: decimal.RequireFromString(x), Y: decimal.RequireFromString(y),
	}
}

// opGoldJSON кодирует проекцию ровно так, как её увидит digestOf.
func opGoldJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("проекция не маршалится, значит и отпечаток был бы мусорным: %v", err)
	}
	return string(b)
}

// opGoldProject прогоняет ОДИН шаг через настоящую секционную проекцию и достаёт его кортеж.
//
// Через constructionProjection, а не мимо неё: тест обязан ловить и изменение самого кортежа шага,
// и изменение места, где список шагов лежит во внешнем кортеже секции.
func opGoldProject(t *testing.T, op entity.TechCardOperation) any {
	t.Helper()
	tc := &entity.TechCardInsert{Operations: []entity.TechCardOperation{op}}
	outer, ok := constructionProjection(tc).([]any)
	if !ok {
		t.Fatalf("внешний кортеж CONSTRUCTION перестал быть []any")
	}
	// Карточка без Construction и без деталей: внешний кортеж обязан остаться трёхэлементным —
	// [construction, ops, pieces] — и хвост парка не появиться.
	if len(outer) != 3 {
		t.Fatalf("внешний кортеж CONSTRUCTION стал длины %d, ожидалось 3 "+
			"(появился безусловный элемент — он сдвинет отпечаток каждой карточки)", len(outer))
	}
	ops, ok := outer[1].([]any)
	if !ok || len(ops) != 1 {
		t.Fatalf("список операций не на позиции 1 внешнего кортежа или не единичный: %#v", outer[1])
	}
	return ops[0]
}

// opGoldCases — карточки разной наполненности. Порядок кейсов значения не имеет, каждый независим.
func opGoldCases() []struct {
	name string
	op   entity.TechCardOperation
	want []any
} {
	return []struct {
		name string
		op   entity.TechCardOperation
		want []any
	}{
		{
			// ПУСТОЙ ШАГ. Голова из 16 позиций и ни одного хвоста — это тот самый байт-в-байт
			// эталон, относительно которого меряются все будущие дописки.
			name: "пустой шаг: голая 16-позиционная голова",
			op:   entity.TechCardOperation{},
			want: []any{
				0,   // 1 operation_number
				"",  // 2 тип шага (компат-проекция)
				"",  // 3 zone
				nil, // 4 piece_line_keys (nil-срез, а НЕ [] — json их различает)
				nil, // 5 bom_line_keys
				"",  // 6 smv
				0,   // 7 callout_number
				"",  // 8 stitches_per_cm
				"",  // 9 seam_class
				"",  // 10 seam_allowance_mm
				"",  // 11 topstitch_mode
				"",  // 12 topstitch_width_mm
				0,   // 13 topstitch_rows
				"",  // 14 attachment_kind
				"",  // 15 attachment_size_mm
				"",  // 16 note
			},
		},
		{
			// ТОЛЬКО ОБЯЗАТЕЛЬНЫЕ ПОЛЯ. У шага ВТО без единого заполненного факта ВТО хвоста нет:
			// operationHasPressTail считает по ВАЛИДНОСТИ, и «никто не отвечал» обязано хешироваться
			// как до появления блока ВТО.
			name: "только обязательные поля: тип и зона, без хвостов",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(10),
				OperationType:   entity.OpTypePress,
				Zone:            entity.ZoneOuter,
			},
			want: []any{
				10, "press", "outer", nil, nil, "", 0, "", "", "", "", "", 0, "", "", "",
			},
		},
		{
			// LEGACY-МАШИНКА СХЛОПЫВАЕТСЯ И ХВОСТА НЕ РОЖДАЕТ. Ровно это свойство удержало подписи
			// при переезде 0306: строка (machine, lockstitch) обязана дать в позиции 2 голый
			// "lockstitch" и НИ ОДНОГО дописанного элемента.
			name: "машинный шаг с legacy-машинкой: компат-позиция, хвоста нет",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(20),
				OperationType:   entity.OpTypeMachine,
				MachineType:     opGoldStr("lockstitch"),
				Zone:            entity.ZoneOuter,
				PieceLineKeys:   []string{"P1"},
				SMV:             opGoldDec("1.5"),
			},
			want: []any{
				20, "lockstitch", "outer", []string{"P1"}, nil, "1.5", 0,
				"", "", "", "", "", 0, "", "", "",
			},
		},
		{
			// МАШИННЫЙ ХВОСТ ПРИ LEGACY-МАШИНКЕ: сама машинка в хвосте ПУСТА, потому что уже
			// отхеширована компат-позицией. Дублировать её значило бы записать один факт дважды.
			name: "машинный блок: хвост есть, machine_type в нём пуст (уехал в компат-позицию)",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(30),
				OperationType:   entity.OpTypeMachine,
				MachineType:     opGoldStr("overlock"),
				Zone:            entity.ZoneOuter,
				ThreadCount:     opGoldI32(5),
				NeedleType:      opGoldStr("ballpoint"),
				NeedleSizeNm:    opGoldI32(90),
			},
			want: []any{
				30, "overlock", "outer", nil, nil, "", 0, "", "", "", "", "", 0, "", "", "",
				[]any{"machine", "", "", 5, "ballpoint", 90, "", "", ""},
			},
		},
		{
			// МАШИНКА БЕЗ LEGACY-БЛИЗНЕЦА не схлопывается: позиция 2 несёт "machine", а сама машинка
			// уезжает в хвост. Такого шага до 0306 существовать не могло, протухать нечему.
			name: "машинный блок: машинка вне legacy-словаря остаётся в хвосте",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(35),
				OperationType:   entity.OpTypeMachine,
				MachineType:     opGoldStr("zigzag"),
				Zone:            entity.ZoneOuter,
				StitchWidthMm:   opGoldDec("4"),
			},
			want: []any{
				35, "machine", "outer", nil, nil, "", 0, "", "", "", "", "", 0, "", "", "",
				[]any{"machine", "zigzag", "", 0, "", 0, "", "", "4"},
			},
		},
		{
			// ВТО-ХВОСТ. Пар — ПАРОЙ {Valid, Bool}: явное «без пара» обязано отличаться от «никто не
			// отвечал», иначе подпись под указанием читалась бы как действительная под умолчанием.
			name: "блок ВТО: хвост press, пар парой {valid,bool} = явное «без пара»",
			op: entity.TechCardOperation{
				OperationNumber:   opGoldI32(40),
				OperationType:     entity.OpTypeFusing,
				Zone:              entity.ZoneInterlining,
				PressTemperatureC: opGoldI32(150),
				PressDwellSec:     opGoldI32(12),
				PressPressureNCm2: opGoldDec("3.5"),
				PressSteam:        opGoldBool(false),
			},
			want: []any{
				40, "fusing", "interlining", nil, nil, "", 0, "", "", "", "", "", 0, "", "", "",
				[]any{"press", "", "", 150, 12, "3.5", []any{true, false}, ""},
			},
		},
		{
			// СБОРКА И МЕДИА. Хвост сборки несёт ВЕСЬ упорядоченный union тегами piece/unit; имя
			// узла в проекцию не входит. Хвост медиа несёт снимки по порядку с выносками, а цвет
			// выноски — нет. Две выноски здесь разной формы намеренно: короткая (5 элементов, без
			// детали и без стиля) и полная (деталь + стилевой хвост).
			name: "сборка и медиа: хвосты assembly и media поверх головы",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(50),
				OperationType:   entity.OpTypeMachine,
				MachineType:     opGoldStr("lockstitch"),
				Zone:            entity.ZoneOuter,
				PieceLineKeys:   []string{"P1"},
				OutputUnitKey:   opGoldStr("COLLAR"),
				OutputUnitName:  opGoldStr("воротник"), // в отпечаток НЕ входит
				AssemblyInputs: []entity.OperationInput{
					{Kind: entity.AssemblyInputPiece, Key: "P1"},
					{Kind: entity.AssemblyInputUnit, Key: "STAND"},
				},
				Media: []entity.TechCardOperationMedia{{
					MediaId: 7,
					Caption: opGoldStr("узел воротника"),
					Annotations: []entity.TechCardAnnotation{
						{
							Kind:   entity.AnnotationKindPin,
							Points: []entity.TechCardAnnotationPoint{opGoldPoint("0.25", "0.5")},
							Text:   "припосадить 6 мм",
							LabelX: decimal.RequireFromString("0.3"),
							LabelY: decimal.RequireFromString("0.4"),
							Color:  entity.TechCardAnnotationColor("red"), // в отпечаток НЕ входит
						},
						{
							Kind: entity.AnnotationKindDim,
							Points: []entity.TechCardAnnotationPoint{
								opGoldPoint("0.1", "0.1"), opGoldPoint("0.9", "0.9"),
							},
							Text:          "12",
							LabelX:        decimal.RequireFromString("0.5"),
							LabelY:        decimal.RequireFromString("0.5"),
							PieceLineKey:  "P1",
							PieceLineKeys: []string{"P1", "P2"},
							Dashed:        true,
						},
					},
				}},
			},
			want: []any{
				50, "lockstitch", "outer", []string{"P1"}, nil, "", 0, "", "", "", "", "", 0, "", "", "",
				[]any{"assembly", "COLLAR", []any{
					[]any{"piece", "P1"},
					[]any{"unit", "STAND"},
				}},
				[]any{"media", []any{
					[]any{7, "узел воротника", []any{
						[]any{"pin", []any{[]any{"0.25", "0.5"}}, "припосадить 6 мм", "0.3", "0.4"},
						[]any{"dim", []any{[]any{"0.1", "0.1"}, []any{"0.9", "0.9"}}, "12", "0.5", "0.5",
							"P1", []any{true, false, []string{"P1", "P2"}}},
					}},
				}},
			},
		},
	}
}

// TestTechCardOperationDigestProjectionGolden — сам эталон.
func TestTechCardOperationDigestProjectionGolden(t *testing.T) {
	for _, tt := range opGoldCases() {
		t.Run(tt.name, func(t *testing.T) {
			got := opGoldJSON(t, opGoldProject(t, tt.op))
			want := opGoldJSON(t, tt.want)
			if got != want {
				t.Errorf("проекция операции сдвинулась — отпечаток CONSTRUCTION уже подписанных "+
					"карточек изменится.\n--- эталон ---\n%s\n--- сейчас ---\n%s", want, got)
			}
		})
	}
}

// TestTechCardOperationDigestHeadIsSixteen фиксирует САМО ПРАВИЛО, а не отдельные значения:
// голова ровно 16 позиций, всё сверх неё — тегированный хвост «[имя, …]».
//
// Этот тест — тот, который обязан упасть, когда новый вид операции дописывает поле БЕЗУСЛОВНО.
// Голден выше покажет, ЧТО поехало; этот скажет, ПОЧЕМУ так делать нельзя.
func TestTechCardOperationDigestHeadIsSixteen(t *testing.T) {
	const head = 16
	for _, tt := range opGoldCases() {
		t.Run(tt.name, func(t *testing.T) {
			row, ok := opGoldProject(t, tt.op).([]any)
			if !ok {
				t.Fatalf("кортеж операции перестал быть []any")
			}
			if len(row) < head {
				t.Fatalf("голова кортежа операции короче %d позиций (%d) — из неё удалили элемент, "+
					"а это тот же безусловный сдвиг, что и дописка", head, len(row))
			}
			for i, tail := range row[head:] {
				tagged, ok := tail.([]any)
				if !ok || len(tagged) == 0 {
					t.Errorf("хвост %d не тегированный кортеж: %#v", i, tail)
					continue
				}
				tag, ok := tagged[0].(string)
				if !ok || tag == "" {
					t.Errorf("хвост %d начинается не с имени-тега: %#v — голый хвост запрещён, "+
						"типы хвостов пересекаются и прочесть кортеж станет нельзя", i, tagged[0])
				}
			}
		})
	}
}

// TestTechCardOperationDigestStableAcrossRuns проверяет то единственное, ради чего проекция вообще
// существует: одна и та же карточка обязана дать один и тот же hex — иначе подпись протухала бы
// сама по себе, без единой правки.
func TestTechCardOperationDigestStableAcrossRuns(t *testing.T) {
	for _, tt := range opGoldCases() {
		t.Run(tt.name, func(t *testing.T) {
			tc := &entity.TechCardInsert{Operations: []entity.TechCardOperation{tt.op}}
			first := digestOf(constructionProjection(tc))
			second := digestOf(constructionProjection(tc))
			if first != second {
				t.Errorf("отпечаток неустойчив между прогонами: %s != %s", first, second)
			}
			if len(first) != 64 {
				t.Errorf("отпечаток не sha256-hex: %q", first)
			}
		})
	}
}
