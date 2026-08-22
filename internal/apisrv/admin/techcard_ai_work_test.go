package admin

import (
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ЦИТАТА + МУТАЦИЯ ДЛЯ ОСИ «РАБОТА» В ЧЕРНОВИКЕ ИИ.
//
// Каталог здесь СОБРАН СТРУКТУРОЙ, а не прочитан запросом: правила, которые проверяются, — чистые,
// и база в них не участвует ни одной строкой. Это же и делает пробу быстрой настолько, чтобы её
// гоняли мутациями.
//
// МУТАЦИИ, ПРОГНАННЫЕ ПО ЭТОМУ ФАЙЛУ (каждая ставилась одна, прогонялась и откатывалась):
//
//  1. В aiResolveWork снята проверка принадлежности каталогу (`if !ok { return "" }`) —
//     ПОКРАСНЕЛА строка «выдуманный токен и больше ничего» этого теста: работа приехала в шаг
//     словом, которого в каталоге нет, то есть ровно тем, что нельзя ни сохранить, ни найти в
//     пикере. Именно эта строка и держит проверку: у ненайденной работы пустые и глагол, и режим
//     машинки, поэтому на шаге, назвавшем глагол, промах поймало бы уже правило 3 — и проба
//     доказывала бы не то, о чём написана.
//  2. Из промпта убраны синонимы — покраснела проба в internal/openrouter (см. prompt_work_test.go).
//
// ЧТО ЭТИ ПРОБЫ НЕ ДОКАЗЫВАЮТ: что модель ответит токеном. Это свойство промпта и модели, и меряется
// оно живым вызовом, а не тестом. Здесь доказано другое — что ответ токеном ДОЕЗЖАЕТ до шага, а
// ответ выдумкой шаг не портит.

// aiWorkTestCatalog — четыре работы, покрывающие все три режима вопроса «на чём» и снятие.
// Значения дословно те, что сеют миграции 0329/0331: проба, разошедшаяся с сидом, доказывала бы
// свойства выдуманного каталога.
func aiWorkTestCatalog() *entity.OperationWorkCatalog {
	return entity.NewOperationWorkCatalog(aiWorkTestRows())
}

func aiWorkTestRows() []entity.OperationWork {
	return []entity.OperationWork{
		{
			Token: "moscow_hem", Verb: "machine", Stage: "edges_hems", Label: "Hem — rolled (Moscow)",
			MachineMode:    entity.OperationWorkMachineModeFixed,
			DefaultMachine: sql.NullString{String: "lockstitch", Valid: true},
			Machines:       []string{"lockstitch"},
			Syn:            []string{"московский", "московский шов", "узкая подгибка", "moscow hem"},
		},
		{
			Token: "slit_overcast", Verb: "machine", Stage: "closures", Label: "Slit — overcast",
			MachineMode:    entity.OperationWorkMachineModeAsk,
			DefaultMachine: sql.NullString{String: "zigzag", Valid: true},
			Machines:       []string{"zigzag", "buttonhole"},
			Syn:            []string{"прорезь", "обметать прорезь", "slit overcast"},
		},
		{
			Token: "press_flat", Verb: "press", Stage: "pressing", Label: "Press flat",
			MachineMode: entity.OperationWorkMachineModeNone,
			Syn:         []string{"приутюжить", "press flat"},
		},
		{
			// Снятая 0331: расщеплена на `gather` и `ease_in`. Читается, но не предлагается.
			Token: "gather_ease", Verb: "machine", Stage: "join_seam", Label: "Gather / ease",
			MachineMode:    entity.OperationWorkMachineModeFixed,
			DefaultMachine: sql.NullString{String: "gathering", Valid: true},
			Machines:       []string{"gathering"},
			Syn:            []string{"сборка", "gather"},
			RetiredAt:      sql.NullTime{Time: time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC), Valid: true},
		},
	}
}

// TestAIDraftOperationNamesTheWorkOrStaysSilent — ЦИТАТА.
//
// Одна таблица на все исходы, потому что вопрос у них один: что доезжает до шага, когда модель
// называет работу. Каждая строка держит и ОБРАТНОЕ утверждение — что стало с остальными полями
// шага, — потому что «работа не записалась» и «шаг испортился» это разные беды, и первая допустима,
// а вторая нет.
func TestAIDraftOperationNamesTheWorkOrStaysSilent(t *testing.T) {
	catalog := aiWorkTestCatalog()

	const (
		machineVerb = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE
		pressVerb   = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_PRESS
		noVerb      = pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_UNKNOWN
		lockstitch  = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH
		overlock    = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_OVERLOCK
		zigzag      = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_ZIGZAG
		buttonhole  = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_BUTTONHOLE
		gathering   = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_GATHERING
		noMachine   = pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_UNKNOWN
	)

	for _, tc := range []struct {
		name        string
		draft       openrouter.Operation
		wantWork    string
		wantVerb    pb_common.TechCardOperationType
		wantMachine pb_common.TechCardMachineType
		also        func(t *testing.T, op *pb_common.TechCardOperation)
	}{{
		// (а) ГЛАВНЫЙ СЛУЧАЙ ФАЗЫ: технолог сказал «подогнуть низ московским», модель ответила
		// токеном и больше ничем. Работа НЕСЁТ и глагол, и машинку — их не нужно ни спрашивать, ни
		// угадывать, и шаг приходит собранным, а не с двумя пустыми полями.
		name:     "работа названа одним токеном — глагол и машинка приходят от неё",
		draft:    openrouter.Operation{Work: "moscow_hem", Zone: "hem"},
		wantWork: "moscow_hem", wantVerb: machineVerb, wantMachine: lockstitch,
	}, {
		// Модель пишет прозой, а не токенами: тот же нормализатор, что у остальных словарей.
		name:     "работа названа с заглавной и пробелом",
		draft:    openrouter.Operation{Work: "Moscow Hem", OperationType: "machine", MachineType: "lockstitch"},
		wantWork: "moscow_hem", wantVerb: machineVerb, wantMachine: lockstitch,
	}, {
		// (б) ВЫДУМАННЫЙ ТОКЕН. Именно эта строка держит проверку принадлежности каталогу: шаг не
		// назвал ни глагола, ни машинки, поэтому ни одно следующее правило про промах не знает.
		name: "выдуманный токен и больше ничего — работа пуста, шаг цел",
		draft: openrouter.Operation{
			Work: "bogus_work", Zone: "hem", SmvMinutes: "0.5", CalloutNumber: "3",
			Note: "подогнуть низ",
		},
		wantWork: "", wantVerb: noVerb, wantMachine: noMachine,
		also: func(t *testing.T, op *pb_common.TechCardOperation) {
			require.Equal(t, pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_HEM, op.Zone)
			require.Equal(t, "0.5", op.Smv.GetValue())
			require.EqualValues(t, 3, op.CalloutNumber)
			require.Equal(t, "подогнуть низ", op.Note)
		},
	}, {
		// (б) То же на полностью заполненном машинном шаге: одно выдуманное слово не имеет права
		// стоить шагу ни одного другого поля.
		name: "выдуманный токен на собранном шаге — остальные девять полей целы",
		draft: openrouter.Operation{
			Work: "bogus_work", OperationType: "machine", MachineType: "overlock", Zone: "side",
			ThreadCount: "4", NeedleType: "ballpoint", NeedleSizeNm: "90", StitchesPerCm: "4",
		},
		wantWork: "", wantVerb: machineVerb, wantMachine: overlock,
		also: func(t *testing.T, op *pb_common.TechCardOperation) {
			require.EqualValues(t, 4, op.ThreadCount)
			require.EqualValues(t, 90, op.NeedleSizeNm)
			require.Equal(t, pb_common.TechCardNeedleType_TECH_CARD_NEEDLE_TYPE_BALLPOINT, op.NeedleType)
			require.Equal(t, "4", op.StitchesPerCm.GetValue())
		},
	}, {
		// (в) РЕЖИМ ask, МАШИНКА ВНЕ СПИСКА РАБОТЫ. ВЫБРАННЫЙ ИСХОД — СНЯТЬ РАБОТУ, А НЕ ЧИНИТЬ
		// МАШИНКУ. Правило 4 сохранения отвергло бы такой шаг целиком, значит выбирать приходится;
		// снятая работа это МОЛЧАНИЕ (пустое поле, сегодняшнее состояние каждой строки обеих баз, и
		// технолог заполняет его одним кликом), а переписанная машинка — ВЫДУМКА: сервер стёр бы
		// ответ, который модель дала явно, и сказал бы за неё «на самом деле зигзагом».
		name: "прорезь названа с чужой машинкой — снимается работа, машинка модели остаётся",
		draft: openrouter.Operation{
			Work: "slit_overcast", OperationType: "machine", MachineType: "overlock", Zone: "front",
		},
		wantWork: "", wantVerb: machineVerb, wantMachine: overlock,
	}, {
		// Тот же режим ask, но машинку шаг не назвал: вопрос «на чём» у работы есть, и ответ на него
		// у неё тоже есть — дефолт пункта. Оставить пусто нельзя: правило 4 отвергает шаг без машинки.
		name:     "прорезь без машинки — берётся дефолт работы",
		draft:    openrouter.Operation{Work: "slit_overcast", OperationType: "machine"},
		wantWork: "slit_overcast", wantVerb: machineVerb, wantMachine: zigzag,
	}, {
		name:     "прорезь на законной второй машинке списка",
		draft:    openrouter.Operation{Work: "slit_overcast", OperationType: "machine", MachineType: "buttonhole"},
		wantWork: "slit_overcast", wantVerb: machineVerb, wantMachine: buttonhole,
	}, {
		// СНЯТАЯ РАБОТА ШАГ НЕ УБИВАЕТ. Её нет в промпте, но модель отвечает и своей памятью о цехе.
		name: "снятая работа — не предлагается заново, но шаг живёт",
		draft: openrouter.Operation{
			Work: "gather_ease", OperationType: "machine", MachineType: "gathering", Zone: "waist",
		},
		wantWork: "", wantVerb: machineVerb, wantMachine: gathering,
	}, {
		// Правило 3: работа НЕСЁТ глагол, и два ответа на один вопрос на одной строке означают, что
		// печатный лист и рельс сборки скажут разное. Спорит — снимается работа, глагол модели цел
		// вместе со всем своим блоком настроек.
		name: "глагол шага спорит с глаголом работы — снимается работа",
		draft: openrouter.Operation{
			Work: "press_flat", OperationType: "machine", MachineType: "lockstitch",
		},
		wantWork: "", wantVerb: machineVerb, wantMachine: lockstitch,
	}, {
		// Работа режима none: ось «на чём» у неё не машинная вовсе, и глагол она приносит тот же
		// самый — ВТО. Блок ВТО обязан заполниться, то есть работа решается ДО раскладки полей.
		name: "работа ВТО без глагола — глагол приходит от неё вместе со своим блоком",
		draft: openrouter.Operation{
			Work: "press_flat", PressEquipment: "iron", PressTemperatureC: "150", Zone: "front",
		},
		wantWork: "press_flat", wantVerb: pressVerb, wantMachine: noMachine,
		also: func(t *testing.T, op *pb_common.TechCardOperation) {
			require.Equal(t, pb_common.TechCardPressEquipment_TECH_CARD_PRESS_EQUIPMENT_IRON, op.PressEquipment)
			require.EqualValues(t, 150, op.PressTemperatureC)
		},
	}, {
		// Легаси-слово («overlock» БЫЛО глаголом до этой фазы) канонизируется ДО сверки с работой:
		// иначе совпадение объявлялось бы спором, и работа снималась бы на самом обычном ответе.
		name: "легаси-слово вместо глагола — не спор, а то же самое",
		draft: openrouter.Operation{
			Work: "moscow_hem", OperationType: "lockstitch", Zone: "hem",
		},
		wantWork: "moscow_hem", wantVerb: machineVerb, wantMachine: lockstitch,
	}, {
		name:     "работа не названа вовсе — сегодняшнее состояние, и оно стоит ноль",
		draft:    openrouter.Operation{OperationType: "machine", MachineType: "overlock"},
		wantWork: "", wantVerb: machineVerb, wantMachine: overlock,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			op := aiDraftOperation(tc.draft, catalog)
			require.NotNil(t, op, "ни один исход не имеет права выбросить шаг")
			require.Equal(t, tc.wantWork, op.Work, "work")
			require.Equal(t, tc.wantVerb, op.OperationType, "operation_type")
			require.Equal(t, tc.wantMachine, op.MachineType, "machine_type")
			if tc.also != nil {
				tc.also(t, op)
			}
		})
	}
}

// Сервер, не загрузивший каталог, не имеет права проставить работу: разбор payload'а при пустом
// снимке отказывает непустой работе поимённо (`catalog_unavailable`), то есть такой черновик был бы
// черновиком, который нельзя сохранить. Шаг при этом целиком цел.
func TestAIDraftOperationWithoutACatalogNamesNoWork(t *testing.T) {
	op := aiDraftOperation(openrouter.Operation{
		Work: "moscow_hem", OperationType: "machine", MachineType: "lockstitch", Zone: "hem",
	}, nil)
	require.Empty(t, op.Work)
	require.Equal(t, pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE, op.OperationType)
	require.Equal(t, pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH, op.MachineType)
	require.Equal(t, pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_HEM, op.Zone)
}

// Снятая работа не попадает в промпт — но остаётся читаемой на приёме (это доказывает строка
// «снятая работа» таблицы выше). Две половины одного правила, и ни одна не заменяет другую.
func TestAIWorkContextsHidesRetiredWorksFromThePrompt(t *testing.T) {
	got := aiWorkContexts(aiWorkTestRows())
	require.Len(t, got, 3)
	for _, w := range got {
		require.NotEqual(t, "gather_ease", w.Token, "снятая работа предложена модели")
	}
	require.Equal(t, "moscow_hem", got[0].Token)
	require.Equal(t, "machine", got[0].Verb)
	require.Equal(t, []string{"lockstitch"}, got[0].Machines)
	require.Contains(t, got[0].Syn, "московский")

	// Пустой каталог — не пустой список, а «этот сервер каталога не знает»: промпт тогда о работах
	// не говорит вовсе, вместо того чтобы просить токен из ниоткуда.
	require.Nil(t, aiWorkContexts(nil))
}
