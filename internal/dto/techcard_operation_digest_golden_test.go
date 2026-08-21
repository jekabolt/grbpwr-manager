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
			// набор пар пуст, когда все ВТО-колонки NULL, а пустой набор хвоста не рождает — значит
			// «никто не отвечал» хешируется как до появления блока ВТО.
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
			// МАШИННЫЙ ХВОСТ ПРИ LEGACY-МАШИНКЕ: пары "machine_type" в хвосте НЕТ ВОВСЕ, потому
			// что машинка уже отхеширована компат-позицией. Дублировать её значило бы записать один
			// факт дважды. В позиционной форме её место занимала пустая строка; в парной место
			// незаполненного поля не занимает ничто — в этом и смысл формы.
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
				[]any{"machine", []any{
					[]any{"needle_size_nm", 90},
					[]any{"needle_type", "ballpoint"},
					[]any{"thread_count", 5},
				}},
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
				[]any{"machine", []any{
					[]any{"machine_type", "zigzag"},
					[]any{"stitch_width_mm", "4"},
				}},
			},
		},
		{
			// ВТО-ХВОСТ. Пар трёхзначен, и форма пар выражает это сама: пары нет — «никто не
			// отвечал», ["press_steam", false] — явное «без пара», ["press_steam", true] — «с
			// паром». Обёртка {Valid, Bool} позиционной формы больше не нужна.
			//
			// ЭТОТ ЖЕ ХВОСТ НЕСЁТ press_action / press_toward (0325): ВТО-факты живут в ОДНОМ
			// хвосте под ОДНИМ тегом. Здесь они не заполнены — значит пар не дают.
			name: "блок ВТО: хвост press парами, явное «без пара» — пара press_steam=false",
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
				[]any{"press", []any{
					[]any{"press_dwell_sec", 12},
					[]any{"press_pressure_n_cm2", "3.5"},
					[]any{"press_steam", false},
					[]any{"press_temperature_c", 150},
				}},
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
		{
			// ОСЬ «РАБОТА» (0330) — ДВЕНАДЦАТЫЙ ХВОСТ, И ОН ПОСЛЕДНИЙ ИЗ СУЩЕСТВУЮЩИХ.
			//
			// Шаг взят тот же, что и «машинный шаг с legacy-машинкой» выше, с одной-единственной
			// разницей — названа работа. Так эталон читается как ДЕЛЬТА: всё, что появилось в
			// кортеже, появилось из-за work, и ни одна позиция головы не сдвинулась. Это и есть
			// доказательство нулевой волны в кортежной форме; hex-эталоны рядом доказывают её же в
			// байтах.
			//
			// В ХВОСТЕ ТОЛЬКО ТОКЕН. Ни ярлыка («Topstitch»), ни глагола, ни стадии, ни версии
			// каталога: они представление, правятся UPDATE-миграцией, и правка ярлыка не смеет
			// объявлять подписанную карточку изменённой (прецедент: цвет выноски не хешируется).
			name: "ось работа: хвост work поверх головы, ничего больше",
			op: entity.TechCardOperation{
				OperationNumber: opGoldI32(20),
				OperationType:   entity.OpTypeMachine,
				MachineType:     opGoldStr("lockstitch"),
				Zone:            entity.ZoneOuter,
				PieceLineKeys:   []string{"P1"},
				SMV:             opGoldDec("1.5"),
				Work:            opGoldStr("topstitch"),
			},
			want: []any{
				20, "lockstitch", "outer", []string{"P1"}, nil, "1.5", 0,
				"", "", "", "", "", 0, "", "", "",
				[]any{"work", []any{
					[]any{"work", "topstitch"},
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

// opGoldCaseByName достаёт кейс по имени — карточка hex-эталона обязана быть собрана ИЗ ТЕХ ЖЕ
// данных, что кортежный голден, а не из своей копии: две копии одних и тех же операций разъехались
// бы при первой правке, и замороженный hex начал бы падать сам по себе, ничего не сообщая.
func opGoldCaseByName(t *testing.T, name string) entity.TechCardOperation {
	t.Helper()
	for _, c := range opGoldCases() {
		if c.name == name {
			return c.op
		}
	}
	t.Fatalf("кейс %q исчез из opGoldCases — карточка hex-эталона собрана не из тех данных", name)
	return entity.TechCardOperation{}
}

// ЗАМОРОЖЕННЫЙ HEX ОТПЕЧАТКА CONSTRUCTION — второй эталон, поверх кортежного.
//
// ЧТО ОН ЛОВИТ СВЕРХ ГОЛДЕНА ВЫШЕ. Кортежный эталон замораживает ПРОЕКЦИЮ — то, что подаётся на
// вход digestOf. Всё, что происходит ПОСЛЕ, он не видит вовсе: digestOf кодирует проекцию
// json.Marshal (без отступов, с запятыми без пробелов) и берёт sha256 от полученных байт, и любая
// правка этого слоя — MarshalIndent вместо Marshal, другие разделители, дописанная соль или
// версионный префикс, иная функция хеша, другой порядок склейки элементов перед хешированием —
// оставит проекцию БАЙТ В БАЙТ прежней. Кортежный голден останется зелёным, а подписи КАЖДОЙ
// карточки в базе протухнут молча в момент выкатки: сравнивается-то hex, а он поехал у всех сразу.
// Именно эту дыру закрывает литерал ниже.
//
// ПОЧЕМУ КАРТОЧКА, А НЕ ОДИН ШАГ: несколько операций в одном списке проверяют и порядок склейки —
// перестановка или иная сборка списка шагов сдвинет hex, а каждый отдельный кортеж останется тем же.
//
// КОГДА ПРАВКА ЛИТЕРАЛА ЗАКОННА. Ровно тогда же, когда законна правка кортежного эталона, и ни на
// шаг раньше: смена формата отпечатка ОСОЗНАННА, описана комментарием у места правки в
// techcard_section_digest.go, и волна пере-утверждения подписей CONSTRUCTION посчитана — здесь она
// равна ВСЕЙ базе подписанных карточек, потому что слой сборки общий для всех. Переписать hex,
// чтобы «тест позеленел», значит выкатить эту волну молча.
//
// ─────────────────────────────────────────────────────────────────────────────────────────────────
// ЛИТЕРАЛ БЫЛ ПЕРЕПИСАН ОДИН РАЗ — 0325-b, ВЫРАВНИВАНИЕ ФОРМЫ ХВОСТОВ. Прежнее значение:
// b1316bb0c183cce498bb8e9f388dfa799b385cb84cde78b549b3bb0434eca051. Что двинулось: машинный и
// ВТО-хвосты шага перестали быть ПОЗИЦИОННЫМИ и записываются парами «имя колонки, значение», а два
// ВТО-хвоста, жившие под одним тегом "press" (позиционный 0306 и парный 0325), слились в один.
//
// ПОЧЕМУ ЭТО БЫЛО ЗАКОННО ИМЕННО ТОГДА И БОЛЬШЕ НЕ БУДЕТ — НЕ ПРЕЦЕДЕНТ, А ЗАКРЫВШЕЕСЯ ОКНО.
// Разведка обеих баз (прод и бета) на день правки: tech_card_signoff — 0 строк, tech_card_release —
// 0 строк. signed_digest не хранится больше нигде в схеме. Значит подписей не существовало НИ
// ОДНОЙ, протухать было нечему, и посчитанная волна пере-утверждения равнялась НУЛЮ — не «мало», а
// ровно ноль, и это проверяемый факт, а не оценка. Это единственное состояние базы, в котором
// форму проекции можно переписать бесплатно.
//
// С ПЕРВОЙ ЖЕ УТВЕРЖДЁННОЙ СЕКЦИЕЙ окно закрывается навсегда, и правило «отпечаток существующей
// строки обязан остаться байт в байт» снова становится безусловным. Читателю, который наткнётся на
// эту правку как на разрешение: разрешение было выдано состоянием базы, а не соображениями вкуса.
// Прежде чем повторять — сходи и посчитай строки в tech_card_signoff. Если их не ноль, ответ «нет»,
// и новое поле идёт условным парным хвостом, ради чего форма и выравнивалась.
// ─────────────────────────────────────────────────────────────────────────────────────────────────
const opGoldConstructionDigestHex = "caeb665c60a472ebf38f183a7ae698dba29660eab13a048ffbcf94d281b52e3c"

// TestTechCardConstructionDigestHexFrozen — сам hex-эталон.
func TestTechCardConstructionDigestHexFrozen(t *testing.T) {
	tc := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		opGoldCaseByName(t, "только обязательные поля: тип и зона, без хвостов"),
		opGoldCaseByName(t, "машинный блок: хвост есть, machine_type в нём пуст (уехал в компат-позицию)"),
		opGoldCaseByName(t, "сборка и медиа: хвосты assembly и media поверх головы"),
	}}
	got := digestOf(constructionProjection(tc))
	if got != opGoldConstructionDigestHex {
		t.Errorf("отпечаток CONSTRUCTION поехал при неизменной проекции — значит поехал слой сборки "+
			"(кодирование, отступы, порядок склейки), и подписи ВСЕХ карточек в базе протухли разом."+
			"\n--- эталон ---\n%s\n--- сейчас ---\n%s", opGoldConstructionDigestHex, got)
	}
}

// ЗАМОРОЖЕННЫЙ HEX ШАГА С РАБОТОЙ (0330) — ТРЕТИЙ ЭТАЛОН, И ОН НОВЫЙ, А НЕ ПЕРЕПИСАННЫЙ.
//
// ПОЧЕМУ ОТДЕЛЬНЫМ ЛИТЕРАЛОМ, А НЕ ДОПИСКОЙ ШАГА В КАРТОЧКУ ВЫШЕ. Тот hex морозит СУЩЕСТВУЮЩИЕ
// строки — те самые 126, у которых work = NULL, — и его неподвижность и есть цитата «волна нулевая».
// Допиши в ту карточку work-шаг, и литерал пришлось бы переписать; переписанный эталон нулевой волны
// не доказывает ничего. Поэтому work морозится СВОЕЙ карточкой: два литерала отвечают на два разных
// вопроса — «старое не двинулось» и «новое зафиксировано».
//
// ЧТО ЛОВИТ ИМЕННО ЭТОТ ЛИТЕРАЛ. Всё, что кортежный голден не видит, потому что происходит ПОСЛЕ
// проекции (кодировка, разделители, функция хеша), — и вдобавок то, что видит, но на другом уровне:
// перестановку хвоста "work" относительно соседей. Хвост стоит ДВЕНАДЦАТЫМ, сразу после "fastening",
// и место это заморожено здесь.
//
// КОГДА ПРАВКА ЛИТЕРАЛА ЗАКОННА: ровно тогда же, когда правка соседнего, и ни на шаг раньше — то
// есть после подсчёта волны пере-утверждения по tech_card_signoff. «Позеленить тест» законным
// поводом не является ни в одном из двух случаев.
// ЛИТЕРАЛ ПОСЧИТАН НЕЗАВИСИМО ОТ КОДА, А НЕ СПИСАН С ЕГО ВЫВОДА. Кортеж выше выписан руками и
// сверен тестом проекции; байты — sha256 от [null,[<кортеж>],[]] в компактном JSON, посчитанные
// сторонним скриптом по ОБОИМ кортежам, и тем же скриптом на тех же правилах воспроизведён соседний, давно
// замороженный opGoldConstructionDigestHex. Списанный с падения эталон морозит не форму, а ошибку.
const opGoldWorkDigestHex = "1b3fda5ff5df5958e001297a6f5de2c4ba894cd32b4bec5c0f22a48200b5b790"

// opWorkNextToFastening — шаг с ДВУМЯ хвостами, "fastening" и "work".
//
// ⚠️ ПОЧЕМУ ОН ЖИВЁТ ЗДЕСЬ, А НЕ СРЕДИ opGoldCases. Тот набор — КОРПУС ДО ВОЛНЫ 0324: ни один его
// кейс не заполняет ни одной колонки волны, и на этом стоит отдельное правило
// (TestOperationKindTailsAbsentWhenAllNull). Шаг, который НАМЕРЕННО заполняет zipper_application,
// сломал бы смысл корпуса, а не проверил бы новое свойство. Определение всё равно ОДНО — и кортеж,
// и hex ниже читают его, поэтому разъехаться им негде.
//
// ЗАЧЕМ ОН НУЖЕН. Кейс «хвост work поверх головы» несёт ОДИН хвост и потому слеп к перестановке:
// переставь "work" куда угодно — единственный хвост останется единственным, и hex не двинется. Это
// выяснилось МУТАЦИЕЙ, а не рассуждением: «переставить work перед fastening» оставило односоставный
// эталон зелёным. Здесь хвостов два, и порядок между ними заморожен байтами.
func opWorkNextToFastening() entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationNumber:   opGoldI32(30),
		OperationType:     entity.OpTypeMachine,
		MachineType:       opGoldStr("lockstitch"),
		Zone:              entity.ZoneOuter,
		ZipperApplication: opGoldStr("invisible"),
		Work:              opGoldStr("topstitch"),
	}
}

// TestWorkTailSitsRightAfterFasteningInTheTuple — кортежная половина той же заморозки: она говорит,
// ЧТО поехало, тогда как hex ниже говорит только «поехало».
func TestWorkTailSitsRightAfterFasteningInTheTuple(t *testing.T) {
	want := []any{
		30, "lockstitch", "outer", nil, nil, "", 0,
		"", "", "", "", "", 0, "", "", "",
		[]any{"fastening", []any{
			[]any{"zipper_application", "invisible"},
		}},
		[]any{"work", []any{
			[]any{"work", "topstitch"},
		}},
	}
	got := opGoldJSON(t, opGoldProject(t, opWorkNextToFastening()))
	if got != opGoldJSON(t, want) {
		t.Errorf("кортеж шага с fastening и work сдвинулся:\n--- эталон ---\n%s\n--- сейчас ---\n%s",
			opGoldJSON(t, want), got)
	}
}

// TestTechCardConstructionWorkDigestHexFrozen — hex-эталон шага с названной работой.
//
// ДВА ШАГА, А НЕ ОДИН, И ЭТО НЕ УКРАШЕНИЕ. Первый морозит ФОРМУ хвоста (пара «work → токен» поверх
// нетронутой головы), второй — его МЕСТО (после "fastening"); почему второй не лежит в opGoldCases,
// сказано у opWorkNextToFastening.
func TestTechCardConstructionWorkDigestHexFrozen(t *testing.T) {
	tc := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		opGoldCaseByName(t, "ось работа: хвост work поверх головы, ничего больше"),
		opWorkNextToFastening(),
	}}
	got := digestOf(constructionProjection(tc))
	if got != opGoldWorkDigestHex {
		t.Errorf("отпечаток шага с работой поехал: либо изменилась форма хвоста \"work\", либо его "+
			"место среди хвостов, либо слой сборки отпечатка."+
			"\n--- эталон ---\n%s\n--- сейчас ---\n%s", opGoldWorkDigestHex, got)
	}
}

// TestWorkTailStandsTwelfthAfterFastening — МЕСТО хвоста, названное вслух.
//
// Hex выше упадёт и от перестановки, но скажет об этом одной строкой мусора. Этот тест говорит, ЧТО
// именно поехало: у шага, заполненного и по семействам волны, и по работе, "work" обязан стоять
// последним и ровно за "fastening". Обещанное "handwork" ПОСЛЕДНЕЕ место цело — он ещё не рождён и
// встанет ПОСЛЕ "work", тринадцатым.
func TestWorkTailStandsTwelfthAfterFastening(t *testing.T) {
	op := entity.TechCardOperation{
		OperationType:     entity.OpTypeMachine,
		MachineType:       opGoldStr("lockstitch"),
		ZipperApplication: opGoldStr("invisible"), // хвост "fastening"
		Work:              opGoldStr("topstitch"),
	}
	tags := opKindTagsOf(t, op)
	if len(tags) < 2 {
		t.Fatalf("ожидались хвосты fastening и work, получено: %v", tags)
	}
	last, prev := tags[len(tags)-1], tags[len(tags)-2]
	if last != "work" || prev != "fastening" {
		t.Fatalf("порядок хвостов %v: ожидалось …, fastening, work — место \"work\" заморожено "+
			"двенадцатым, сразу после \"fastening\"", tags)
	}
}

// TestWorkTailAbsentWhenWorkIsNull — вторая половина нулевой волны, названная точечно.
//
// Hex существующих строк доказывает её в байтах на трёх шагах; здесь то же свойство проверяется как
// ПРАВИЛО: NULL-работа не рождает хвоста вовсе, а не рождает пустой. Пустой хвост ["work", []] —
// именно та ошибка, которую легко не заметить: кортежный голден на кейсах без work остался бы
// зелёным только потому, что этих кейсов три, а строк в проде сто двадцать шесть.
func TestWorkTailAbsentWhenWorkIsNull(t *testing.T) {
	for _, tag := range opKindTagsOf(t, entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		MachineType:   opGoldStr("lockstitch"),
		ThreadCount:   opGoldI32(5),
	}) {
		if tag == "work" {
			t.Fatal("шаг без названной работы отрастил хвост \"work\" — отпечаток каждой из 126 " +
				"существующих строк прода сдвинулся бы в момент выкатки")
		}
	}
}
