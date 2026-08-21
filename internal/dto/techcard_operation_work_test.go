package dto

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ПРАВИЛА КОГЕРЕНТНОСТИ ОСИ «РАБОТА» (0330) — КЛЕТКА ЗА КЛЕТКОЙ.
//
// ТЕСТИРУЕТСЯ parseOperationWork НАПРЯМУЮ, А НЕ ЧЕРЕЗ ЦЕЛЫЙ PAYLOAD, И ЭТО НЕ ЛЕНЬ. Соседние
// правила шага (обязательные поля глагола, дискриминаторы блоков, парк оборудования) отказали бы
// раньше и по своим причинам, и тест про работу зеленел бы или краснел от чужого правила. Путь с
// провода целиком проверяет соседний файл — тест симметрии, — а здесь заморожены сами четыре
// правила.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены), — см. шапку
// techcard_operation_work_gate_test.go в internal/apisrv/admin: там же зафиксирован прогон по
// щиту осведомлённости.

func workFieldViolation(t *testing.T, err error) *entity.ValidationError {
	t.Helper()
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("ожидался именованный FieldViolation, получено %#v — голая ошибка не называет ни "+
			"поля, ни причины, и клиент не сможет показать её у нужной строки", err)
	}
	return ve
}

func TestParseOperationWorkRules(t *testing.T) {
	restore := withWorkCatalog(t)
	defer restore()

	cases := []struct {
		name       string
		token      string
		opType     entity.TechCardOperationType
		machine    sql.NullString
		wantReason string // "" — разбор обязан пройти
		wantToken  string
	}{
		{
			// РАБОТА НЕ НАЗВАНА — состояние КАЖДОЙ из 126 строк прода и каждой строки беты. Ни
			// одного правила, ни одного обращения к каталогу: нулевая цена, иначе выкатка объявила
			// бы невалидной всю базу разом.
			name:   "пустая работа: ноль правил, NULL в колонке",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "lockstitch", Valid: true},
		},
		{
			name:   "пробелы вместо работы читаются как «не названа»",
			token:  "   ",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "lockstitch", Valid: true},
		},
		{
			name:  "работа из каталога на своём глаголе и своей машинке",
			token: "topstitch", wantToken: "topstitch",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "lockstitch", Valid: true},
		},
		{
			name:  "токена нет в каталоге",
			token: "не_такой_работы", wantReason: "unknown_work",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "lockstitch", Valid: true},
		},
		{
			// ПРАВИЛО 3. Работа НЕСЁТ глагол: «отстрочка» это машинная строчка, и никакой другой.
			// Два ответа на один вопрос на одной строке означают, что печатный лист и рельс сборки
			// скажут разное.
			name:  "глагол шага не равен глаголу работы",
			token: "topstitch", wantReason: "work_verb_mismatch",
			opType: entity.OpTypePress,
		},
		{
			// ПРАВИЛО 4 — И ТОЛЬКО ПРИ ask. Отстрочка законно живёт на пяти машинках, и список
			// закрыт: оверлок в него не входит.
			name:  "машинка вне списка работы при режиме ask",
			token: "topstitch", wantReason: "work_machine_mismatch",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "overlock", Valid: true},
		},
		{
			name:  "машинка не названа вовсе при режиме ask",
			token: "topstitch", wantReason: "work_machine_mismatch",
			opType: entity.OpTypeMachine,
		},
		{
			// РЕЖИМ fixed ВОПРОСА НЕ ЗАДАЁТ, значит и ответа не проверяет. Машинка СЛЕДУЕТ из
			// работы; несогласие шага с ней — предмет других правил (машинный блок 0306), а не
			// этого. Проверять здесь значило бы завести второй ответ на один вопрос.
			name:  "режим fixed: машинка шага правилом 4 не проверяется",
			token: "join_lockstitch", wantToken: "join_lockstitch",
			opType: entity.OpTypeMachine, machine: sql.NullString{String: "overlock", Valid: true},
		},
		{
			// ⚠️ КЛЕТКА, РАДИ КОТОРОЙ ПРАВИЛО 4 СУЖЕНО ДО ask. У шести работ глагола hardware_set
			// режим none, хотя миграция 0328 сделала машинку на этом глаголе ЗАКОННОЙ. Проверяй
			// правило 4 при none — и каждая такая строка стала бы несохраняемой задним числом.
			name:  "hardware_set с режимом none: названная машинка не мешает",
			token: "set_hardware", wantToken: "set_hardware",
			opType: entity.OpTypeHardwareSet, machine: sql.NullString{String: "button_attach", Valid: true},
		},
		{
			name:  "press_open с режимом none",
			token: "press_open", wantToken: "press_open",
			opType: entity.OpTypePressOpen,
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseOperationWork(
				&pb_common.TechCardOperation{Work: tt.token}, tt.opType, tt.machine, "operations[0]")
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("разбор отказал там, где обязан пройти: %v", err)
				}
				if tt.wantToken == "" {
					if got.Valid {
						t.Fatalf("работа не названа, а в колонку уехало %q — «не назначено» обязано "+
							"быть NULL, а не пустой строкой", got.String)
					}
					return
				}
				if !got.Valid || got.String != tt.wantToken {
					t.Fatalf("в колонку уехало %#v, ожидалось %q", got, tt.wantToken)
				}
				return
			}
			ve := workFieldViolation(t, err)
			if ve.Reason != tt.wantReason {
				t.Errorf("причина отказа %q, ожидалась %q", ve.Reason, tt.wantReason)
			}
			if ve.Field != "operations[0].work" {
				t.Errorf("отказ повешен на поле %q, ожидалось operations[0].work — клиент показывает "+
					"его у названного поля, и чужое имя уводит человека не туда", ve.Field)
			}
			if got.Valid {
				t.Errorf("отказ вернул непустое значение %q — отказ и значение вместе означают, что "+
					"кто-то может записать одно, прочитав другое", got.String)
			}
		})
	}
}

// TestParseOperationWorkRefusesWhenCatalogUnloaded — незагруженный каталог ЗАПИРАЕТ запись работы,
// а не отключает правила.
//
// Пропустить работу мимо проверок нельзя: правило, которое «иногда не работает», не защищает
// ничего, а незнакомый токен всё равно упёрся бы во внешний ключ — но уже голым 1452 без имени поля
// и без единого слова человеку. Строка БЕЗ работы — сегодня каждая строка обеих баз — не затронута.
func TestParseOperationWorkRefusesWhenCatalogUnloaded(t *testing.T) {
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога опубликован — предыдущий тест его не вернул")
	}
	_, err := parseOperationWork(&pb_common.TechCardOperation{Work: "topstitch"},
		entity.OpTypeMachine, sql.NullString{String: "lockstitch", Valid: true}, "operations[0]")
	ve := workFieldViolation(t, err)
	if ve.Reason != "catalog_unavailable" {
		t.Errorf("причина отказа %q, ожидалась catalog_unavailable — «такой работы нет в каталоге» "+
			"увело бы человека искать опечатку в токене", ve.Reason)
	}

	// А вот шаг БЕЗ работы обязан пройти даже здесь: иначе незагруженный каталог сделал бы
	// нередактируемой всю базу.
	got, err := parseOperationWork(&pb_common.TechCardOperation{},
		entity.OpTypeMachine, sql.NullString{String: "lockstitch", Valid: true}, "operations[0]")
	if err != nil {
		t.Fatalf("шаг без работы отказал при незагруженном каталоге: %v — это заперло бы все 126 "+
			"строк прода", err)
	}
	if got.Valid {
		t.Fatalf("шаг без работы вернул %q", got.String)
	}
}

// TestParseOperationWorkDoesNotJudgeRetired — граница между разбором и щитом, названная вслух.
//
// Снятая (retired) работа отказывает НОВОЙ разметке и принимается там, где строка уже её несёт.
// Второе требует СОХРАНЁННОЙ карточки, которой разбор не видит, поэтому правило целиком живёт в
// apisrv. Здесь заморожено, что разбор его НЕ дублирует: два ответа на один вопрос разъезжаются, и
// разъедется тот, который никто не читает.
func TestParseOperationWorkDoesNotJudgeRetired(t *testing.T) {
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога опубликован — предыдущий тест его не вернул")
	}
	retired := testWorkCatalog()
	retired[1].RetiredAt = sql.NullTime{Time: time.Now().UTC(), Valid: true} // topstitch
	entity.SetOperationWorkCatalog(retired)
	defer entity.SetOperationWorkCatalog(nil)

	got, err := parseOperationWork(&pb_common.TechCardOperation{Work: "topstitch"},
		entity.OpTypeMachine, sql.NullString{String: "lockstitch", Valid: true}, "operations[0]")
	if err != nil {
		t.Fatalf("разбор отказал снятой работе: %v — правило о retired принадлежит щиту, где на "+
			"руках сохранённая карточка, иначе размеченная когда-то строка стала бы несохраняемой", err)
	}
	if !got.Valid || got.String != "topstitch" {
		t.Fatalf("снятая работа не доехала: %#v", got)
	}
}
