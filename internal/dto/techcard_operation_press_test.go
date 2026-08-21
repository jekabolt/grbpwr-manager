package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ВТО: ПОД-ГЛАГОЛ И НАПРАВЛЕНИЕ ПРИПУСКА (0325) — тесты правил.
//
// Круг pb → entity → pb покрыт TestOperationKindsRoundTrip (шаги 15 и 16). Здесь — четыре правила,
// каждое из которых ломается по-своему:
//
//  1. блок законен только на PRESS и PRESS_OPEN; FUSING и PRINT отвергаются НАРЯДУ со всеми
//     прочими, хотя ВТО-блок 39..45 при них законен, — это разные вопросы;
//  2. на PRESS_OPEN под-глагол не законен ВОВСЕ: глагол уже сам себе под-глагол, и с 0327 это его
//     единственное написание — член `open` снят из контракта;
//  3. `press_toward` — только при `press_action = to_one_side`;
//  4. при `to_one_side` он ОБЯЗАТЕЛЕН — и это единственная обязательность, которая не может стать
//     ретроактивной, потому что значения to_one_side ни одна сохранённая строка иметь не может.

func pressOp(action pb_common.TechCardPressAction, toward pb_common.TechCardPressToward) *pb_common.TechCardOperationPress {
	return &pb_common.TechCardOperationPress{Action: action, Toward: toward}
}

const (
	paUnknown   = pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_UNKNOWN
	paFlat      = pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_PRESS_FLAT
	paToOneSide = pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_TO_ONE_SIDE
	// РОВНО ТОТ САМЫЙ СНЯТЫЙ НОМЕР, ВЗЯТЫЙ СЫРЫМ ЧИСЛОМ. Именованного члена больше нет — 0327
	// объявил 3 reserved, — но провод несёт числа, и старый бандл продолжит слать именно это.
	// Написать здесь `3` вместо имени и есть тот единственный способ проверить, что сервер отвечает
	// на него ШУМНО и с именем поля, а не молча кладёт в колонку значение, которого нет в словаре.
	paRetiredOpen = pb_common.TechCardPressAction(3)
	paSteam       = pb_common.TechCardPressAction_TECH_CARD_PRESS_ACTION_STEAM

	ptUnknown = pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_UNKNOWN
	ptFront   = pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_FRONT
	ptSleeve  = pb_common.TechCardPressToward_TECH_CARD_PRESS_TOWARD_SLEEVE

	pressIron = pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON
)

// ── 1. БЛОК — ТОЛЬКО НА ВТО-ГЛАГОЛАХ ────────────────────────────────────────────────────────────

// TestPressBlockNotApplicableByVerb — FUSING и PRINT здесь НЕ исключение и стоят в таблице
// намеренно: у дублирования и у печати ВТО-блок 39..45 законен (термопресс — то же оборудование),
// но припуска у них нет вовсе, и «заутюжить на полочку» на таком шаге описывает ничто.
func TestPressBlockNotApplicableByVerb(t *testing.T) {
	cases := []struct {
		name  string
		op    *pb_common.TechCardOperation
		field string
	}{
		{"под-глагол на дублировании", &pb_common.TechCardOperation{
			OperationType: opTypeFusing, Zone: zoneOuter,
			PressEquipment: pressIron,
			Press:          pressOp(paFlat, ptUnknown),
		}, "operations[0].press_action"},
		{"под-глагол на печати", &pb_common.TechCardOperation{
			OperationType: opTypePrint, Zone: zoneOuter,
			PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_SCREEN,
			Press:       pressOp(paSteam, ptUnknown),
		}, "operations[0].press_action"},
		{"под-глагол на машинной строчке", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock,
			Press: pressOp(paFlat, ptUnknown),
		}, "operations[0].press_action"},
		{"направление на подрезке", &pb_common.TechCardOperation{
			OperationType: opTypeTrimNew, Zone: zoneCollar,
			Trim:  &pb_common.TechCardOperationTrim{Action: pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_TRIM_EVEN},
			Press: pressOp(paUnknown, ptFront),
		}, "operations[0].press_toward"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.op)
			if ve.Field != tt.field || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/not_applicable", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// ── 2. PRESS_OPEN — САМ СЕБЕ ПОД-ГЛАГОЛ ─────────────────────────────────────────────────────────

// TestPressOpenTakesNoSubVerbAtAll. Пустой press_action на этом глаголе — КАНОН И ЕДИНСТВЕННОЕ
// ЗАКОННОЕ СОСТОЯНИЕ. До 0327 у разутюжки было два написания — глагол и член `open`, — и они давали
// два разных кортежа в проекции дайджеста CONSTRUCTION: одна и та же работа на двух карточках
// получала разные отпечатки. Член снят, значит на этом глаголе отвергается ЛЮБОЙ под-глагол, а не
// только чужой.
func TestPressOpenTakesNoSubVerbAtAll(t *testing.T) {
	base := func(p *pb_common.TechCardOperationPress) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypePressOpen, Zone: zoneOuter,
			PressEquipment: pressIron, Press: p,
		}
	}
	// Канон: глагол без блока.
	if got := kindFacts(kindParse(t, base(nil)).Operations[0])["press_action"]; got != "" {
		t.Errorf("канонический press_open обязан хранить press_action пустым, получено %q", got)
	}
	for _, tt := range []struct {
		name   string
		action pb_common.TechCardPressAction
	}{
		{"приутюжить", paFlat},
		{"заутюжить", paToOneSide},
		{"отпарить", paSteam},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, base(pressOp(tt.action, ptUnknown)))
			if ve.Field != "operations[0].press_action" || ve.Reason != "not_applicable" {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].press_action/not_applicable", ve.Field, ve.Reason)
			}
		})
	}
}

// TestRetiredPressActionOpenIsRefusedByField — ГЛАВНОЕ УТВЕРЖДЕНИЕ F1, и проверяется оно СЫРЫМ
// НОМЕРОМ, а не именем.
//
// На месте этого теста стоял TestPressOpenVerbIsNotRewritten — он ЗАКРЕПЛЯЛ оба написания
// разутюжки как законные и запрещал сводить их друг к другу, «принимая цену осознанно». Цена
// оказалась не той, что он охранял: пикер предлагал `press` и `press open` соседними строками, а
// при глаголе `press` открывал второй селект, где строка `press open` стояла снова. Технолог
// выбирал не приём, а строку в списке, и одна и та же разутюжка на двух карточках получала разные
// отпечатки CONSTRUCTION. 0327 снял член; тест, закреплявший его, снят вместе с ним.
//
// ЧТО ДОКАЗЫВАЕТСЯ ЗДЕСЬ. Бэкенд едет РАНЬШЕ клиента, и до его выкатки старый бандл продолжит
// слать номер 3 — сырым числом на проводе, без всякого имени. Такое сохранение обязано ОТВЕРГАТЬСЯ
// ШУМНО И С ИМЕНЕМ ПОЛЯ: «вернулась ошибка» тут не годится, потому что поле-адресат — это то, за
// что цепляется форма, чтобы подсветить контрол (см. field-errors.ts). Молчаливый приём был бы
// хуже отказа вдвойне — в колонку легло бы значение, которого нет ни в одном словаре, и CHECK
// отбил бы всю карточку голым 3819.
func TestRetiredPressActionOpenIsRefusedByField(t *testing.T) {
	if _, ok := pb_common.TechCardPressAction_name[3]; ok {
		t.Fatal("номер 3 снова занят членом enum'а — он объявлен reserved 0327 и закрыт навсегда")
	}
	for _, tt := range []struct {
		name string
		op   *pb_common.TechCardOperation
	}{
		{"на глаголе press", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter, PressEquipment: pressIron,
			Press: pressOp(paRetiredOpen, ptUnknown)}},
		{"на глаголе press_open", &pb_common.TechCardOperation{
			OperationType: opTypePressOpen, Zone: zoneOuter, PressEquipment: pressIron,
			Press: pressOp(paRetiredOpen, ptUnknown)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, tt.op)
			if ve.Field != "operations[0].press_action" || ve.Reason != "unknown_value" {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].press_action/unknown_value", ve.Field, ve.Reason)
			}
		})
	}
}

// TestPressOpenVerbSurvivesAsTheOnlySpelling — вторая половина F1, и без неё первая ничего не
// стоит. Снять член можно было двумя способами: убрать второе написание (то, что сделано) или
// начать переписывать одно в другое. Второе пометило бы подписанную карточку как «изменена после
// подписи» без единой человеческой правки, поэтому глагол обязан оставаться глаголом и НЕ
// обзаводиться дописанным под-глаголом.
func TestPressOpenVerbSurvivesAsTheOnlySpelling(t *testing.T) {
	viaVerb := kindParse(t, &pb_common.TechCardOperation{
		OperationType: opTypePressOpen, Zone: zoneOuter, PressEquipment: pressIron,
	}).Operations[0]
	if viaVerb.OperationType != entity.OpTypePressOpen {
		t.Errorf("глагол press_open переписан в %q — он в проде и в подписанных карточках", viaVerb.OperationType)
	}
	if viaVerb.PressAction.Valid {
		t.Errorf("канонической записи дописали press_action = %q — это изобретённый за технолога ответ", viaVerb.PressAction.String)
	}
	// И ОТПЕЧАТОК ЭТОЙ СТРОКИ НЕ СДВИНУЛСЯ. Пара «press_action, значение» рождается только у
	// ЗАПОЛНЕННОГО поля, поэтому шаг без под-глагола хешируется ровно так же, как хешировался до
	// снятия члена. НЕГАТИВНЫЙ КОНТРОЛЬ обязателен: без него тест был бы зелён и на константе.
	blank := digestOf(constructionProjection(&entity.TechCardInsert{Operations: []entity.TechCardOperation{viaVerb}}))
	filled := viaVerb
	filled.PressAction = sql.NullString{String: "press_flat", Valid: true}
	if blank == digestOf(constructionProjection(&entity.TechCardInsert{Operations: []entity.TechCardOperation{filled}})) {
		t.Error("заполненный press_action дал ТОТ ЖЕ отпечаток, что пустой — значит хвостовая пара не эмитится вовсе и предыдущая проверка ничего не проверяет")
	}
}

// ── 3-4. НАПРАВЛЕНИЕ — ТОЛЬКО ПРИ «ЗАУТЮЖИТЬ», И ТАМ ОБЯЗАТЕЛЬНО ────────────────────────────────

func TestPressTowardOnlyWithToOneSide(t *testing.T) {
	press := func(p *pb_common.TechCardOperationPress) *pb_common.TechCardOperation {
		return &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter, PressEquipment: pressIron, Press: p,
		}
	}
	t.Run("законная пара сохраняется", func(t *testing.T) {
		f := kindFacts(kindParse(t, press(pressOp(paToOneSide, ptSleeve))).Operations[0])
		if f["press_action"] != "to_one_side" || f["press_toward"] != "sleeve" {
			t.Errorf("пара не доехала: %q / %q", f["press_action"], f["press_toward"])
		}
	})
	for _, tt := range []struct {
		name   string
		action pb_common.TechCardPressAction
	}{
		{"направление при «приутюжить»", paFlat},
		{"направление при «отпарить»", paSteam},
		{"направление без под-глагола вовсе", paUnknown},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ve := kindRefusal(t, press(pressOp(tt.action, ptFront)))
			if ve.Field != "operations[0].press_toward" || ve.Reason != "needs_press_action" {
				t.Errorf("отказ назвал %q/%q, ожидалось operations[0].press_toward/needs_press_action", ve.Field, ve.Reason)
			}
		})
	}
	t.Run("«заутюжить» без стороны отвергается", func(t *testing.T) {
		ve := kindRefusal(t, press(pressOp(paToOneSide, ptUnknown)))
		if ve.Field != "operations[0].press_toward" || ve.Reason != "required" {
			t.Errorf("отказ назвал %q/%q, ожидалось operations[0].press_toward/required", ve.Field, ve.Reason)
		}
	})
}

// ── 5. РЕТРОАКТИВНОЙ ОБЯЗАТЕЛЬНОСТИ НЕТ НИГДЕ ───────────────────────────────────────────────────

// TestPressFieldsAreNeverRequiredOnTheirOwn — САМОЕ ВАЖНОЕ УТВЕРЖДЕНИЕ ФАЗЫ, проверяемое, а не
// принимаемое на веру.
//
// Обе колонки рождаются NULL на КАЖДОЙ существующей строке. Значит любой сегодняшний шаг обязан
// пройти разбор без единого отказа — и ВТО-шаги в первую очередь, потому что именно у них поля
// теперь есть. Единственная обязательность в этой фазе условная (toward при to_one_side), и
// включить её на сохранённой строке физически нечем: значения to_one_side в базе не существует,
// его завела эта же миграция.
func TestPressFieldsAreNeverRequiredOnTheirOwn(t *testing.T) {
	// Все глаголы, какие бывают у сегодняшних строк, БЕЗ блока press вовсе.
	for _, tt := range []struct {
		name string
		op   *pb_common.TechCardOperation
	}{
		{"press без под-глагола", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter, PressEquipment: pressIron}},
		{"press_open без под-глагола", &pb_common.TechCardOperation{
			OperationType: opTypePressOpen, Zone: zoneOuter, PressEquipment: pressIron}},
		{"fusing без под-глагола", &pb_common.TechCardOperation{
			OperationType: opTypeFusing, Zone: zoneOuter, PressEquipment: pressIron}},
		{"machine без под-глагола", &pb_common.TechCardOperation{
			OperationType: opTypeMachineNew, Zone: zoneOuter, MachineType: mtOverlock}},
		{"press с ПУСТЫМ блоком — бандл говорит «полей нет»", &pb_common.TechCardOperation{
			OperationType: opTypePress, Zone: zoneOuter, PressEquipment: pressIron,
			Press: &pb_common.TechCardOperationPress{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ins := kindParse(t, tt.op)
			f := kindFacts(ins.Operations[0])
			if f["press_action"] != "" || f["press_toward"] != "" {
				t.Errorf("шаг без под-глагола получил значения %q / %q — стор изобрёл ответ за технолога",
					f["press_action"], f["press_toward"])
			}
		})
	}
	// И то же самое кругом: пустое остаётся ОТСУТСТВУЮЩИМ блоком, а не пустой обёрткой.
	ins := kindParse(t, &pb_common.TechCardOperation{
		OperationType: opTypePress, Zone: zoneOuter, PressEquipment: pressIron,
	})
	emitted := kindEmit(t, ins)
	if p := emitted.Operations[0].GetPress(); p != nil {
		t.Errorf("у шага без под-глагола эмитился блок press: %+v — клиент прочтёт это как «про ВТО тут думали»", p)
	}
}

// ── 6. ВЫХОД «ПРОЧЕЕ» У ШЕСТИ ОБЯЗАТЕЛЬНЫХ ДИСКРИМИНАТОРОВ ─────────────────────────────────────

// TestRequiredDiscriminatorsAcceptOtherRejectUnknown — «прочее» ЕСТЬ ОТВЕТ, «не выбрано» ответом НЕ
// является, и это разные вещи.
//
// Пока выхода не было, отсутствие своего приёма не оставляло поле пустым — оно ЗАСТАВЛЯЛО выбрать
// чужой, и дальше это значение уходило в подписанный хвост дайджеста, в релизный снапшот и на
// печатный лист. Оба утверждения проверяются на КАЖДОМ из шести: other проходит и переживает круг,
// UNKNOWN по-прежнему отвергается с тем же путём поля.
func TestRequiredDiscriminatorsAcceptOtherRejectUnknown(t *testing.T) {
	cases := []struct {
		name   string
		column string
		field  string
		other  *pb_common.TechCardOperation
		absent *pb_common.TechCardOperation
	}{
		{
			name: "attach_method", column: "attach_method", field: "operations[0].attach_method",
			other: &pb_common.TechCardOperation{
				OperationType: opTypeHardware, Zone: zoneClosure,
				Hardware: &pb_common.TechCardOperationHardware{
					AttachMethod: pb_common.TechCardHardwareAttachMethod_TECH_CARD_HARDWARE_ATTACH_METHOD_OTHER,
				},
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypeHardware, Zone: zoneClosure},
		},
		{
			name: "print_method", column: "print_method", field: "operations[0].print_method",
			other: &pb_common.TechCardOperation{
				OperationType: opTypePrint, Zone: zoneOuter,
				PrintMethod: pb_common.TechCardPrintMethod_TECH_CARD_PRINT_METHOD_OTHER,
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypePrint, Zone: zoneOuter},
		},
		{
			name: "trim_action", column: "trim_action", field: "operations[0].trim_action",
			other: &pb_common.TechCardOperation{
				OperationType: opTypeTrimNew, Zone: zoneCollar,
				Trim: &pb_common.TechCardOperationTrim{
					Action: pb_common.TechCardTrimAction_TECH_CARD_TRIM_ACTION_OTHER,
				},
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypeTrimNew, Zone: zoneCollar},
		},
		{
			name: "cleaning_kind", column: "cleaning_kind", field: "operations[0].cleaning_kind",
			other: &pb_common.TechCardOperation{
				OperationType: opTypeClean, Zone: zoneOther,
				Clean: &pb_common.TechCardOperationClean{
					Kind: pb_common.TechCardCleaningKind_TECH_CARD_CLEANING_KIND_OTHER,
				},
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypeClean, Zone: zoneOther},
		},
		{
			name: "coverage_mode", column: "coverage_mode", field: "operations[0].coverage_mode",
			other: &pb_common.TechCardOperation{
				OperationType: opTypeInspect, Zone: zoneOther,
				Inspect: &pb_common.TechCardOperationInspect{
					CoverageMode: pb_common.TechCardInspectCoverage_TECH_CARD_INSPECT_COVERAGE_OTHER,
				},
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypeInspect, Zone: zoneOther},
		},
		{
			name: "wet_process_kind", column: "wet_process_kind", field: "operations[0].wet_process_kind",
			other: &pb_common.TechCardOperation{
				OperationType: opTypeWet, Zone: zoneOther,
				WetProcessKind: pb_common.TechCardWetProcessKind_TECH_CARD_WET_PROCESS_KIND_OTHER,
			},
			absent: &pb_common.TechCardOperation{OperationType: opTypeWet, Zone: zoneOther},
		},
	}
	if len(cases) != 6 {
		t.Fatalf("обязательных дискриминаторов шесть, в таблице %d", len(cases))
	}
	for _, tt := range cases {
		t.Run(tt.name+": other — законный ответ", func(t *testing.T) {
			ins := kindParse(t, tt.other)
			if got := kindFacts(ins.Operations[0])[tt.column]; got != "other" {
				t.Fatalf("%s = %q, ожидалось other", tt.column, got)
			}
			// И круг: эмиссия обязана вернуть тот же член, иначе клон сезона превратит «прочее»
			// в «не выбрано» и упрётся в required на ровном месте.
			back, err := ConvertPbTechCardInsertToEntity(kindEmit(t, ins))
			if err != nil {
				t.Fatalf("повторный разбор эмитированного payload'а: %v", err)
			}
			if got := kindFacts(back.Operations[0])[tt.column]; got != "other" {
				t.Errorf("%s не пережил круг: %q", tt.column, got)
			}
		})
		t.Run(tt.name+": UNKNOWN по-прежнему отвергается", func(t *testing.T) {
			ve := kindRefusal(t, tt.absent)
			if ve.Field != tt.field || ve.Reason != "required" {
				t.Errorf("отказ назвал %q/%q, ожидалось %q/required", ve.Field, ve.Reason, tt.field)
			}
		})
	}
}

// TestOtherIsAnAnswerNotSilence — «прочее» и «не выбрано» обязаны РАЗЛИЧАТЬСЯ в отпечатке.
//
// Если бы они хешировались одинаково, подпись под шагом, где технолог честно сказал «прочее»,
// читалась бы как действительная под шагом, где он не сказал ничего, — а это ровно та потеря
// различения, ради устранения которой член и заводится.
func TestOtherIsAnAnswerNotSilence(t *testing.T) {
	silent := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{OperationType: entity.OpTypeClean},
	}}
	answered := &entity.TechCardInsert{Operations: []entity.TechCardOperation{
		{OperationType: entity.OpTypeClean, CleaningKind: opGoldStr("other")},
	}}
	if digestOf(constructionProjection(silent)) == digestOf(constructionProjection(answered)) {
		t.Error("«прочее» и молчание дали один отпечаток — ответ технолога не попал в подпись")
	}
}
