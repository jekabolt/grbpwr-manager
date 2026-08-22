package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ДЛИНА ПРОРЕЗИ: ВТОРОЙ ЗАКОННЫЙ ВХОД (0331) — КЛЕТКА ЗА КЛЕТКОЙ.
//
// ЧТО ДОКАЗЫВАЕТСЯ. Правило РАСШИРЕНО, а не сужено и не подменено: старый вход (петельный автомат)
// работает ровно как работал — в том числе на строке БЕЗ всякой работы, то есть на любой из 126
// строк прода, — а рядом с ним появился второй, работа `slit_overcast`. Обе половины проверяются
// ОДНОЙ таблицей, потому что «не сузили» и «расширили» — два разных утверждения, и таблица, в
// которой есть только второе, доказывает половину.
//
// ⚠️ ПОЧЕМУ ЗДЕСЬ ЖЕ ПРОВЕРЯЕТСЯ REQUIRED, КОТОРОГО В ЭТОМ СЕМЕЙСТВЕ НЕ БЫЛО НИ ОДНОГО. Он висит
// на НЕПУСТОЙ работе, а непустую работу не может прислать ни один сегодняшний бандл (щит
// operation_work_aware отвергает payload, который везёт работу, не объявив поддержки). Клетка
// «прорезь без длины» достижима ровно одним новым жестом, и ни одна сохранённая строка её не
// занимает — это и есть разница между «сломаться можно» и ретроактивным отказом.
//
// ТЕСТИРУЕТСЯ parseOperationKindFields НАПРЯМУЮ, а не путь с провода целиком: соседние правила шага
// (парк оборудования, дискриминаторы глаголов) отказали бы раньше и по своим причинам, и тест про
// длину прорези зеленел бы или краснел от чужого правила.
//
// МУТАЦИИ, ПРОГНАННЫЕ ПО ЭТОМУ ПРАВИЛУ (2026-08-22, откачены):
//
//	(1) `!workAcceptsCutLength(work)` убрано из условия (правило ВЕРНУЛОСЬ к «только петельный
//	    автомат») → красная клетка «зигзаг + прорезь: длина законна», отказ not_applicable.
//	(2) `machineIsOneOf(machineType, machineButtonhole)` убрано (правило СУЖЕНО до одной только
//	    работы) → красная клетка «петельный автомат без работы: длина законна, как и была» — то
//	    есть ровно та, которая описывает все 126 сегодняшних строк прода.
//	(3) REQUIRED снят → красные обе клетки «прорезь без длины» (на зигзаге и на петельном).
//	Постоянная половина мутации — TestCutLengthRuleIsNotFalseGreen ниже: он держит обе
//	неправильные версии предиката рядом с правильной и требует, чтобы каждая на чём-нибудь
//	разошлась с таблицей.

// cutLengthCell — одна клетка таблицы: машинка шага, работа шага, прислана ли длина.
type cutLengthCell struct {
	name    string
	machine string // "" — машинка не названа
	work    string // "" — вид не назначен (состояние каждой строки обеих баз)
	length  string // "" — длина не прислана
	// wantReason: "" — разбор обязан пройти; иначе — причина именованного отказа.
	wantReason string
}

var cutLengthCells = []cutLengthCell{
	{
		// СТАРЫЙ ВХОД, И ОН ПЕРВЫЙ В ТАБЛИЦЕ НАМЕРЕННО: это состояние каждой сегодняшней строки —
		// петельный автомат, работа не назначена. Сужение правила убило бы ровно её.
		name:    "петельный автомат без работы: длина законна, как и была",
		machine: machineButtonhole, length: "18",
	},
	{
		name:    "петельный автомат без работы и без длины: REQUIRED не появился",
		machine: machineButtonhole,
	},
	{
		name:    "зигзаг без работы: длина по-прежнему не про эту машинку",
		machine: "zigzag", length: "18", wantReason: "not_applicable",
	},
	{
		// РАСШИРЕНИЕ. Ровно та запись, которой в цехе не было чем описать.
		name:    "зигзаг + прорезь: длина законна",
		machine: "zigzag", work: workSlitOvercast, length: "18",
	},
	{
		name:    "петельный автомат + прорезь: оба входа сразу",
		machine: machineButtonhole, work: workSlitOvercast, length: "18",
	},
	{
		name:    "прорезь без длины: REQUIRED по имени поля",
		machine: "zigzag", work: workSlitOvercast, wantReason: "required",
	},
	{
		name:    "прорезь без длины на петельном автомате: тот же REQUIRED",
		machine: machineButtonhole, work: workSlitOvercast, wantReason: "required",
	},
	{
		// РАСШИРЕНИЕ УЗКОЕ: открывает поле именно прорезь, а не «любая назначенная работа».
		name:    "чужая работа на зигзаге длину не открывает",
		machine: "zigzag", work: "topstitch", length: "18", wantReason: "not_applicable",
	},
	{
		name:    "чужая работа без длины REQUIRED не рождает",
		machine: "lockstitch", work: "topstitch",
	},
}

func cutLengthOperation(c cutLengthCell) *pb_common.TechCardOperation {
	op := &pb_common.TechCardOperation{}
	if c.length != "" {
		op.Fastening = &pb_common.TechCardOperationFastening{CutLengthMm: dec(c.length)}
	}
	return op
}

// TestCutLengthAcceptsButtonholeMachineAndSlitWork — ЦИТАТА: девять клеток, обе половины
// утверждения.
func TestCutLengthAcceptsButtonholeMachineAndSlitWork(t *testing.T) {
	for _, c := range cutLengthCells {
		t.Run(c.name, func(t *testing.T) {
			machine := sql.NullString{String: c.machine, Valid: c.machine != ""}
			got, err := parseOperationKindFields(cutLengthOperation(c), entity.OpTypeMachine,
				machine, sql.NullString{}, c.work, "operations[0]")
			if c.wantReason == "" {
				if err != nil {
					t.Fatalf("разбор отказал там, где обязан пройти: %v", err)
				}
				if (c.length != "") != got.cutLengthMm.Valid {
					t.Fatalf("длина прорези доехала не так: прислано %q, разобрано valid=%v",
						c.length, got.cutLengthMm.Valid)
				}
				return
			}
			ve := workFieldViolation(t, err)
			if ve.Field != "operations[0].cut_length_mm" {
				t.Fatalf("отказ назвал поле %q, а обязан назвать длину прорези — клиент показывает "+
					"ошибку у строки по имени поля", ve.Field)
			}
			if ve.Reason != c.wantReason {
				t.Fatalf("причина отказа %q, ожидалась %q", ve.Reason, c.wantReason)
			}
			if c.wantReason == "not_applicable" && !strings.Contains(ve.HowToFix, workSlitOvercast) {
				t.Errorf("отказ не называет ВТОРОЙ законный вход (%s) — человек не узнает, чем "+
					"починить: %s", workSlitOvercast, ve.HowToFix)
			}
		})
	}
}

// TestCutLengthRuleIsNotFalseGreen — МУТАЦИЯ, ПОСТОЯННАЯ. Две неправильные версии правила живут
// рядом с правильной, и каждая обязана разойтись с таблицей хотя бы на одной клетке. Без этого
// зелень теста выше доказывала бы только то, что таблица согласна сама с собой.
func TestCutLengthRuleIsNotFalseGreen(t *testing.T) {
	// accepted — ответ ПРАВИЛЬНОГО правила на клетку: законна ли длина.
	accepted := func(c cutLengthCell) bool {
		machine := sql.NullString{String: c.machine, Valid: c.machine != ""}
		return machineIsOneOf(machine, machineButtonhole) || workAcceptsCutLength(c.work)
	}
	variants := []struct {
		name string
		rule func(c cutLengthCell) bool
	}{
		{
			// ПРАВИЛО ДО 0331: только петельный автомат. Обязано разойтись на расширении.
			name: "правило не расширено: только машинка",
			rule: func(c cutLengthCell) bool {
				return machineIsOneOf(sql.NullString{String: c.machine, Valid: c.machine != ""}, machineButtonhole)
			},
		},
		{
			// СУЖЕНИЕ, КОТОРОГО НЕЛЬЗЯ ДОПУСТИТЬ: только работа. Обязано разойтись на строках,
			// которых сегодня 126 из 126 — с машинкой и без работы.
			name: "правило сужено: только работа",
			rule: func(c cutLengthCell) bool { return workAcceptsCutLength(c.work) },
		},
		{
			// «Любая назначенная работа открывает поле» — расширение, которого никто не просил.
			name: "правило расширено до любой работы",
			rule: func(c cutLengthCell) bool {
				return machineIsOneOf(sql.NullString{String: c.machine, Valid: c.machine != ""}, machineButtonhole) ||
					c.work != ""
			},
		},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			for _, c := range cutLengthCells {
				if v.rule(c) != accepted(c) {
					t.Logf("разошлось, как и требуется, на клетке %q", c.name)
					return
				}
			}
			t.Fatalf("неправильная версия правила %q согласилась с таблицей НА ВСЕХ клетках — "+
				"таблица не различает правильное правило и это", v.name)
		})
	}
	// И отдельно: REQUIRED обязан быть достижим только через работу. Клетка «машинка есть, работы
	// нет, длины нет» обязана проходить — иначе REQUIRED стал бы ретроактивным.
	if workRequiresCutLength("") {
		t.Error("REQUIRED сработал на строке БЕЗ работы — это ретроактивный отказ каждой " +
			"сегодняшней строке обеих баз")
	}
}

// TestBackfilledPairsStaySavable — ЭКВИВАЛЕНТНОСТЬ БЭКФИЛЛА И ЖИВЫХ ПРАВИЛ.
//
// ЗАЧЕМ. Миграция 0331 проставляет работу пятнадцати строкам НАПРЯМУЮ В БАЗЕ, минуя всякую
// валидацию. Если хоть одна такая пара нарушает правила когерентности 0330, владелец получит
// карточку, которую можно открыть и НЕЛЬЗЯ сохранить, — а починить её будет нечем: жеста «снять
// вид» в проде ещё нет. Здесь каждая пара вайтлиста проходит ровно тот разбор, через который пойдёт
// первое же сохранение размеченной карточки.
//
// Таблица пар продублирована с guard-тестом миграции (internal/store/migrationlint) СОЗНАТЕЛЬНО:
// там она сверяется с ТЕКСТОМ SQL и заморожена, здесь — с ПРАВИЛАМИ Go. Разъехаться незаметно они
// не могут: правка вайтлиста в SQL красит замороженную таблицу там, и человек приходит сюда.
func TestBackfilledPairsStaySavable(t *testing.T) {
	restore := withBackfillWorkCatalog(t)
	defer restore()

	pairs := []struct {
		token   string
		opType  entity.TechCardOperationType
		machine string
	}{
		{"buttonhole", entity.OpTypeMachine, "buttonhole"},
		{"button_attach", entity.OpTypeMachine, "button_attach"},
		{"embroidery", entity.OpTypeMachine, "embroidery"},
		{"press_open", entity.OpTypePressOpen, ""},
		{"press_flat", entity.OpTypePress, ""},
	}
	for _, p := range pairs {
		t.Run(p.token, func(t *testing.T) {
			machine := sql.NullString{String: p.machine, Valid: p.machine != ""}
			got, err := parseOperationWork(&pb_common.TechCardOperation{Work: p.token},
				p.opType, machine, "operations[0]")
			if err != nil {
				t.Fatalf("строка, которую проставит бэкфилл, НЕ СОХРАНЯЕТСЯ: %v", err)
			}
			if !got.Valid || got.String != p.token {
				t.Fatalf("работа доехала как %+v, ожидался токен %q", got, p.token)
			}
		})
	}
	t.Logf("пять пар вайтлиста (15 строк прода) проходят правила когерентности 0330")
}

// withBackfillWorkCatalog публикует ПЯТЬ работ вайтлиста, снятых с сида 0329 ДОСЛОВНО (токен,
// глагол, режим, машинки). Настоящие, а не выдуманные: правила когерентности сравнивают глагол шага
// с глаголом каталога, и выдуманная пара проверяла бы согласие теста с самим собой.
func withBackfillWorkCatalog(t *testing.T) func() {
	t.Helper()
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога уже опубликован до начала теста — предыдущий тест его не вернул")
	}
	entity.SetOperationWorkCatalog([]entity.OperationWork{
		{
			Token: "buttonhole", Verb: "machine", Stage: "closures", Label: "Buttonhole",
			MachineMode: entity.OperationWorkMachineModeFixed, Machines: []string{"buttonhole"},
		},
		{
			Token: "button_attach", Verb: "machine", Stage: "closures", Label: "Button attach",
			MachineMode: entity.OperationWorkMachineModeFixed, Machines: []string{"button_attach"},
		},
		{
			Token: "embroidery", Verb: "machine", Stage: "print_decorate", Label: "Embroidery",
			MachineMode: entity.OperationWorkMachineModeFixed, Machines: []string{"embroidery"},
		},
		{
			Token: "press_open", Verb: "press_open", Stage: "pressing", Label: "Press open",
			MachineMode: entity.OperationWorkMachineModeNone,
		},
		{
			Token: "press_flat", Verb: "press", Stage: "pressing", Label: "Press flat",
			MachineMode: entity.OperationWorkMachineModeNone,
		},
	})
	return func() { entity.SetOperationWorkCatalog(nil) }
}
