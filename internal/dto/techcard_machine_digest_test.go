package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// machineCompatConstructionDigest — ЭТАЛОННЫЙ ОТПЕЧАТОК КАРТОЧКИ «БЕЗ НОВЫХ ФАКТОВ», снятый на коде
// ДО правки проекции (коммит ff4adf4, T1-T4 строго аддитивны и проекции не касались) с фикстуры
// legacyShapeConstructionCard — то есть с карточки ровно той формы, в какой она лежит в базе сегодня:
// тип шага строкой "lockstitch", pressing/overlock_thread_count не заполнены, парка оборудования нет.
//
// ЧТО ИМЕННО ОН УДОСТОВЕРЯЕТ. Перекладка 0306 не меняет СОДЕРЖАНИЕ ни одного существующего шага —
// она разносит его по двум колонкам («что делают» и «на чём») и снимает с конструкции два поля,
// переехавших в профили. Отпечаток обязан следовать за содержанием, а не за раскладкой по колонкам,
// иначе в момент выкатки ВСЕ подписанные карточки разом объявляются «изменёнными после подписи» — и
// сигнал, ради которого весь механизм существует, обесценивается для всех сразу, ни за что.
//
// Поэтому ниже ДВЕ фикстуры одной карточки — старой формы и новой — и обе обязаны дать этот hex
// байт в байт. Он же караулит заморозку позиций 3 и 5 кортежа конструкции: удалить их — тот же
// позиционный сдвиг, что и дописать элемент, и этот тест упадёт первым.
const machineCompatConstructionDigest = "82b210427bc06d6c9e428cae7dff3f3a41c0cb07ba5777da5086b4cd84063d25"

// legacyShapeConstructionCard — карточка В СТАРОЙ ФОРМЕ: тип шага одним словом, машинки нет.
// Строки, а не константы entity.OpType*: те девять снимаются вместе с переездом, а фикстура обязана
// пережить их и продолжать описывать ровно то, что лежит в базе.
func legacyShapeConstructionCard() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{
			HemFinish: ns("подгибка 2 см"), Notes: ns("общие заметки"),
			DefaultSeamClass: ns("ss_plain"), DefaultStitchesPerCm: nd("4"),
		},
		Operations: []entity.TechCardOperation{
			{OperationNumber: ni32(10), OperationType: "lockstitch", Zone: "closure",
				SMV: nd("0.8"), CalloutNumber: ni32(1), Note: ns("притачать молнию")},
			{OperationNumber: ni32(20), OperationType: "fusing", Zone: "interlining"},
		},
	}
}

// machineShapeConstructionCard — ТА ЖЕ карточка после 0306: «прострочить» стало (machine,
// lockstitch), дублирование осталось fusing и никаких фактов ВТО не приобрело.
func machineShapeConstructionCard() *entity.TechCardInsert {
	return &entity.TechCardInsert{
		Construction: &entity.TechCardConstruction{
			HemFinish: ns("подгибка 2 см"), Notes: ns("общие заметки"),
			DefaultSeamClass: ns("ss_plain"), DefaultStitchesPerCm: nd("4"),
		},
		Operations: []entity.TechCardOperation{
			{OperationNumber: ni32(10), OperationType: entity.OpTypeMachine, MachineType: ns("lockstitch"),
				Zone: "closure", SMV: nd("0.8"), CalloutNumber: ni32(1), Note: ns("притачать молнию")},
			{OperationNumber: ni32(20), OperationType: entity.OpTypeFusing, Zone: "interlining"},
		},
	}
}

func constructionDigest(tc *entity.TechCardInsert) string {
	return TechCardSectionDigests(tc)[entity.SignoffConstruction]
}

// ГЛАВНЫЙ ТЕСТ ЗАДАЧИ. Обе формы одной карточки — до перекладки и после — обязаны дать эталон.
func TestMachineSplitDoesNotMoveTheConstructionDigest(t *testing.T) {
	require.Equal(t, machineCompatConstructionDigest, constructionDigest(legacyShapeConstructionCard()),
		"отпечаток карточки СТАРОЙ формы уехал: либо позиция кортежа сдвинулась, либо в него дописали "+
			"безусловный элемент — все подписанные карточки в базе только что стали «изменёнными после подписи»")
	require.Equal(t, machineCompatConstructionDigest, constructionDigest(machineShapeConstructionCard()),
		"та же карточка после перекладки 0306 хешируется иначе, чем до неё: компат-проекция типа шага "+
			"не работает, и день миграции устаревает каждую подпись CONSTRUCTION на карточках с операциями")
}

// Обратная половина утверждения, иначе тест выше удовлетворялся бы проекцией, которая тип шага не
// хеширует вовсе: биекция обязана быть ВЗАИМНО однозначной, и разные машинки — разные отпечатки.
func TestCompatOperationTypeIsNotACollapse(t *testing.T) {
	seen := map[string]string{"эталон (lockstitch)": machineCompatConstructionDigest}
	for _, machine := range []string{
		"overlock", "coverstitch", "chainstitch", "blindstitch", "bartack",
		"buttonhole", "button_attach", "lockstitch_double_needle",
		"coverlock", // машинки, которой в старом словаре не было: НЕ схлопывается, уезжает в хвост
		"zigzag",
	} {
		tc := machineShapeConstructionCard()
		tc.Operations[0].MachineType = ns(machine)
		d := constructionDigest(tc)
		for name, prev := range seen {
			require.NotEqual(t, prev, d, "машинки %q и %q дали ОДИН отпечаток CONSTRUCTION", name, machine)
		}
		seen[machine] = d
	}
}

// ЕДИНСТВЕННЫЙ ШАГ БЕЗ МАШИНКИ. У шага типа machine, у которого машинка не выбрана, компат-позиция
// несёт "machine" — токен нового словаря, которого среди девяти legacy нет. Это и есть довод, по
// которому позицию вообще можно читать однозначно: множества токенов не пересекаются.
func TestUnsetMachineTypeIsDistinctFromEveryLegacyToken(t *testing.T) {
	bare := machineShapeConstructionCard()
	bare.Operations[0].MachineType = sql.NullString{}
	require.NotEqual(t, machineCompatConstructionDigest, constructionDigest(bare))
}

// ДОВОД, НА КОТОРОМ ДЕРЖИТСЯ ВСЯ КОМПАТ-ПОЗИЦИЯ, — СТРУКТУРНЫЙ, А НЕ СЛОВЕСНЫЙ. В одну позицию
// кортежа пишутся значения из ДВУХ словарей: девять legacy-токенов (для шагов, которые 0306
// переложил) и семь токенов нового словаря (для всех прочих). Читается позиция однозначно ровно до
// тех пор, пока эти множества не пересекаются. Добавь кто-нибудь завтра тип шага "overlock" — и
// прежний оверлок стал бы неотличим от него, то есть подпись под одним шагом читалась бы как
// действительная под другим. Здесь это ловится в тесте, а не в цехе.
func TestLegacyAndModernOperationVocabulariesDoNotOverlap(t *testing.T) {
	modern := map[string]bool{}
	for _, tok := range entity.OperationTypeTokens {
		modern[tok] = true
	}
	for legacy := range entity.LegacyOperationMachineType {
		require.False(t, modern[string(legacy)],
			"токен %q есть и в legacy-словаре типов, и в новом: компат-позиция дайджеста перестала читаться однозначно", legacy)
	}
}

// --- мутационный набор: пятнадцать полей шага поимённо -------------------------------------------

type opMutation struct {
	name   string
	op     int
	mutate func(*entity.TechCardOperation)
}

// Каждое из пятнадцати новых полей операции обязано двигать отпечаток CONSTRUCTION, и двигать его
// СВОИМ образом: поле, попавшее в чужую позицию хвоста (или не попавшее никуда), проявится здесь
// совпадением двух отпечатков, а в цехе — подписью под настройкой, которой никто не видел.
func TestEveryOperationEquipmentFieldMovesTheDigest(t *testing.T) {
	mutations := []opMutation{
		// машинный блок (31-38)
		{"machine_type", 0, func(o *entity.TechCardOperation) { o.MachineType = ns("overlock") }},
		{"machine_type вне старого словаря", 0, func(o *entity.TechCardOperation) { o.MachineType = ns("coverlock") }},
		{"machine_profile_key", 0, func(o *entity.TechCardOperation) { o.MachineProfileKey = ns(machineKey) }},
		{"thread_count", 0, func(o *entity.TechCardOperation) { o.ThreadCount = ni32(5) }},
		{"needle_type", 0, func(o *entity.TechCardOperation) { o.NeedleType = ns("ballpoint") }},
		{"needle_size_nm", 0, func(o *entity.TechCardOperation) { o.NeedleSizeNm = ni32(90) }},
		{"thread_tension", 0, func(o *entity.TechCardOperation) { o.ThreadTension = ns("tighter") }},
		{"thread_tension_note", 0, func(o *entity.TechCardOperation) { o.ThreadTensionNote = ns("туже на 0.5") }},
		{"stitch_width_mm", 0, func(o *entity.TechCardOperation) { o.StitchWidthMm = nd("6.5") }},
		// блок ВТО (39-45) — на шаге дублирования
		{"press_equipment", 1, func(o *entity.TechCardOperation) { o.PressEquipment = ns("fusing_press") }},
		{"press_profile_key", 1, func(o *entity.TechCardOperation) { o.PressProfileKey = ns(pressKey) }},
		{"press_temperature_c", 1, func(o *entity.TechCardOperation) { o.PressTemperatureC = ni32(150) }},
		{"press_dwell_sec", 1, func(o *entity.TechCardOperation) { o.PressDwellSec = ni32(12) }},
		{"press_pressure_n_cm2", 1, func(o *entity.TechCardOperation) { o.PressPressureNCm2 = nd("3.5") }},
		{"press_steam", 1, func(o *entity.TechCardOperation) {
			o.PressSteam = sql.NullBool{Bool: true, Valid: true}
		}},
		{"press_cloth", 1, func(o *entity.TechCardOperation) { o.PressCloth = ns("teflon_sheet") }},
	}

	seen := map[string]string{machineCompatConstructionDigest: "карточка без новых фактов"}
	for _, m := range mutations {
		tc := machineShapeConstructionCard()
		m.mutate(&tc.Operations[m.op])
		d := constructionDigest(tc)
		if prev, dup := seen[d]; dup {
			t.Fatalf("%q и %q дают ОДИН отпечаток CONSTRUCTION: факт не хешируется или занял чужую позицию", prev, m.name)
		}
		seen[d] = m.name
	}
}

// ПОЛЕ ЖИВЁТ НА СВОЁМ ШАГЕ. Хвост дописывается в строку своей операции, поэтому две карточки,
// отличающиеся тем, КАКОЙ шаг несёт факт, обязаны различаться — иначе подпись покрывала бы «где-то
// на карточке стоит игла Nm 90» вместо «на этом шве».
func TestOperationEquipmentTailIsPerStep(t *testing.T) {
	first := machineShapeConstructionCard()
	first.Operations[0].ThreadCount = ni32(5)
	second := machineShapeConstructionCard()
	second.Operations[1].ThreadCount = ni32(5)
	require.NotEqual(t, constructionDigest(first), constructionDigest(second))
}

// ДВА ХВОСТА НА ОДНОЙ ПОЗИЦИИ — И ОБА ТЕГИРОВАНЫ. Это ровно та ловушка, о которой предупреждает
// комментарий к деталям: типы хвостов пересекаются (оба здесь — массивы), поэтому голый хвост
// перестал бы отвечать на вопрос, ЧЕЙ он, и шаг с одними машинными фактами столкнулся бы с шагом с
// одними фактами ВТО. Четыре состояния пары обязаны дать четыре РАЗНЫХ отпечатка.
func TestMachineAndPressTailsDoNotCollide(t *testing.T) {
	state := func(machine, press bool) string {
		tc := machineShapeConstructionCard()
		o := &tc.Operations[0]
		if machine {
			o.ThreadCount = ni32(5)
		}
		if press {
			o.PressDwellSec = ni32(5)
		}
		return constructionDigest(tc)
	}
	seen := map[string]string{}
	for _, c := range []struct {
		name           string
		machine, press bool
	}{
		{"ни одного", false, false},
		{"только машинный", true, false},
		{"только ВТО", false, true},
		{"оба", true, true},
	} {
		d := state(c.machine, c.press)
		if prev, dup := seen[d]; dup {
			t.Fatalf("состояния %q и %q дают ОДИН отпечаток: хвост потерял различитель", prev, c.name)
		}
		seen[d] = c.name
	}
}

// ПАР ТРЁХЗНАЧЕН. «Не задано» (наследовать профиль), «явное нет» (указание цеху: без пара) и «да» —
// три разных указания, и схлопывание первых двух дало бы подписи под «без пара» силу над «как
// получится». Четвёртое состояние — шаг, у которого блока ВТО нет вовсе.
func TestPressSteamIsThreeValuedOnTheStep(t *testing.T) {
	withSteam := func(s sql.NullBool) string {
		tc := machineShapeConstructionCard()
		tc.Operations[1].PressEquipment = ns("fusing_press")
		tc.Operations[1].PressSteam = s
		return constructionDigest(tc)
	}
	states := map[string]string{
		"блока ВТО нет":    machineCompatConstructionDigest,
		"не задано":        withSteam(sql.NullBool{}),
		"явное «без пара»": withSteam(sql.NullBool{Bool: false, Valid: true}),
		"с паром":          withSteam(sql.NullBool{Bool: true, Valid: true}),
	}
	seen := map[string]string{}
	for name, d := range states {
		if prev, dup := seen[d]; dup {
			t.Fatalf("состояния пара %q и %q дают ОДИН отпечаток", prev, name)
		}
		seen[d] = name
	}
}

// --- парк оборудования ---------------------------------------------------------------------------

func digestMachineProfile() entity.TechCardMachineProfile {
	return entity.TechCardMachineProfile{
		ProfileKey: machineKey, Label: ns("оверлок у окна"), MachineType: "overlock",
		ThreadCount: ni32(4), NeedleType: ns("ballpoint"), NeedleSizeNm: ni32(90),
		BedType: ns("cylinder_bed"), Automation: ns("semi_auto"), ThreadTension: ns("looser"),
		ThreadTensionNote: ns("на пол-оборота"), AttachmentKind: ns("binder"),
		StitchesPerCm: nd("4.5"), StitchWidthMm: nd("5.5"), Note: ns("Juki MO-6800"),
	}
}

func digestPressProfile() entity.TechCardPressProfile {
	return entity.TechCardPressProfile{
		ProfileKey: pressKey, Label: ns("дублирующий"), PressEquipment: "fusing_press",
		PressOperationType: ns("fusing"), PressTemperatureC: ni32(140), PressDwellSec: ni32(12),
		PressPressureNCm2: nd("3.5"), PressSteam: sql.NullBool{Bool: false, Valid: true},
		PressCloth: ns("teflon_sheet"), Note: ns("Veit 2000"),
	}
}

func cardWithPark(d *entity.TechCardEquipmentDefaults) *entity.TechCardInsert {
	tc := machineShapeConstructionCard()
	tc.Construction.EquipmentDefaults = d
	return tc
}

// ПРИСУТСТВИЕ ОБЁРТКИ В ПОДПИСЬ НЕ ВХОДИТ, только её содержание. Старый клиент не умеет говорить о
// парке и шлёт nil («не трогай хранимое»), новый может прислать пустую обёртку («парка нет»). Это
// одно и то же содержание — ноль профилей, — и если бы они хешировались по-разному, смена версии
// клиента сама по себе устаревала бы каждую подпись CONSTRUCTION.
func TestEmptyParkHashesAsNoPark(t *testing.T) {
	require.Equal(t, machineCompatConstructionDigest, constructionDigest(cardWithPark(nil)))
	require.Equal(t, machineCompatConstructionDigest,
		constructionDigest(cardWithPark(&entity.TechCardEquipmentDefaults{})),
		"пустая обёртка и отсутствующая обязаны дать один отпечаток")
	require.Equal(t, machineCompatConstructionDigest, constructionDigest(cardWithPark(
		&entity.TechCardEquipmentDefaults{Machines: []entity.TechCardMachineProfile{}, Presses: []entity.TechCardPressProfile{}})))
}

// ТРАНСПОРТНЫЙ ФЛАГ — О ОТПРАВИТЕЛЕ, А НЕ О КАРТОЧКЕ. Он говорит, знает ли клиент про машинные поля;
// содержание карточки от него не зависит, и хешировать его значило бы устаревать подпись при выкатке
// клиента.
func TestMachineFieldsAwareIsNotHashed(t *testing.T) {
	tc := machineShapeConstructionCard()
	tc.MachineFieldsAware = true
	require.Equal(t, machineCompatConstructionDigest, constructionDigest(tc))
}

// Профиль входит в подписанное содержание: шаг наследует от него живьём, поэтому «на чём и как»
// читается из профиля и меняется вместе с ним.
func TestAddingAProfileMovesTheDigest(t *testing.T) {
	onlyMachine := constructionDigest(cardWithPark(&entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{digestMachineProfile()}}))
	onlyPress := constructionDigest(cardWithPark(&entity.TechCardEquipmentDefaults{
		Presses: []entity.TechCardPressProfile{digestPressProfile()}}))
	both := constructionDigest(cardWithPark(&entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{digestMachineProfile()},
		Presses:  []entity.TechCardPressProfile{digestPressProfile()}}))

	seen := map[string]string{machineCompatConstructionDigest: "парка нет"}
	for name, d := range map[string]string{
		"только машинный профиль": onlyMachine, "только профиль ВТО": onlyPress, "оба": both,
	} {
		if prev, dup := seen[d]; dup {
			t.Fatalf("%q и %q дают ОДИН отпечаток CONSTRUCTION", prev, name)
		}
		seen[d] = name
	}
}

// Каждое поле профиля — своё содержание и своя позиция. Пропущенное поле означало бы подпись под
// настройкой пресса, которую подписавший не видел и которая всё равно уйдёт в цех через наследование.
func TestEveryProfileFieldMovesTheDigest(t *testing.T) {
	machineMutations := map[string]func(*entity.TechCardMachineProfile){
		"profile_key":         func(m *entity.TechCardMachineProfile) { m.ProfileKey = "01J0MACHINEKEY000000000002" },
		"label":               func(m *entity.TechCardMachineProfile) { m.Label = ns("оверлок у двери") },
		"machine_type":        func(m *entity.TechCardMachineProfile) { m.MachineType = "coverlock" },
		"thread_count":        func(m *entity.TechCardMachineProfile) { m.ThreadCount = ni32(5) },
		"needle_type":         func(m *entity.TechCardMachineProfile) { m.NeedleType = ns("stretch") },
		"needle_size_nm":      func(m *entity.TechCardMachineProfile) { m.NeedleSizeNm = ni32(80) },
		"bed_type":            func(m *entity.TechCardMachineProfile) { m.BedType = ns("flatbed") },
		"automation":          func(m *entity.TechCardMachineProfile) { m.Automation = ns("auto") },
		"thread_tension":      func(m *entity.TechCardMachineProfile) { m.ThreadTension = ns("tighter") },
		"thread_tension_note": func(m *entity.TechCardMachineProfile) { m.ThreadTensionNote = ns("на оборот") },
		"attachment_kind":     func(m *entity.TechCardMachineProfile) { m.AttachmentKind = ns("teflon_foot") },
		"stitches_per_cm":     func(m *entity.TechCardMachineProfile) { m.StitchesPerCm = nd("5.5") },
		"stitch_width_mm":     func(m *entity.TechCardMachineProfile) { m.StitchWidthMm = nd("6.5") },
		"note":                func(m *entity.TechCardMachineProfile) { m.Note = ns("Juki MO-6900") },
	}
	pressMutations := map[string]func(*entity.TechCardPressProfile){
		"profile_key":          func(p *entity.TechCardPressProfile) { p.ProfileKey = "01J0PRESSKEY0000000000002B" },
		"label":                func(p *entity.TechCardPressProfile) { p.Label = ns("отпариватель") },
		"press_equipment":      func(p *entity.TechCardPressProfile) { p.PressEquipment = "steamer" },
		"press_operation_type": func(p *entity.TechCardPressProfile) { p.PressOperationType = ns("press") },
		"press_temperature_c":  func(p *entity.TechCardPressProfile) { p.PressTemperatureC = ni32(150) },
		"press_dwell_sec":      func(p *entity.TechCardPressProfile) { p.PressDwellSec = ni32(15) },
		"press_pressure_n_cm2": func(p *entity.TechCardPressProfile) { p.PressPressureNCm2 = nd("4.5") },
		"press_steam=unset": func(p *entity.TechCardPressProfile) {
			p.PressSteam = sql.NullBool{}
		},
		"press_steam=true": func(p *entity.TechCardPressProfile) {
			p.PressSteam = sql.NullBool{Bool: true, Valid: true}
		},
		"press_cloth": func(p *entity.TechCardPressProfile) { p.PressCloth = ns("damp_press_cloth") },
		"note":        func(p *entity.TechCardPressProfile) { p.Note = ns("Veit 2001") },
	}

	park := func() *entity.TechCardEquipmentDefaults {
		return &entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{digestMachineProfile()},
			Presses:  []entity.TechCardPressProfile{digestPressProfile()},
		}
	}
	base := constructionDigest(cardWithPark(park()))
	seen := map[string]string{base: "профили как есть"}
	for name, mutate := range machineMutations {
		p := park()
		mutate(&p.Machines[0])
		d := constructionDigest(cardWithPark(p))
		if prev, dup := seen[d]; dup {
			t.Fatalf("машинный профиль: %q и %q дают ОДИН отпечаток", prev, name)
		}
		seen[d] = "машинный профиль: " + name
	}
	for name, mutate := range pressMutations {
		p := park()
		mutate(&p.Presses[0])
		d := constructionDigest(cardWithPark(p))
		if prev, dup := seen[d]; dup {
			t.Fatalf("профиль ВТО: %q и %q дают ОДИН отпечаток", prev, name)
		}
		seen[d] = "профиль ВТО: " + name
	}
}

// ID, ПРИСВОЕННЫЙ ХРАНИЛИЩЕМ, НЕ ХЕШИРУЕТСЯ. Тот же проекционный код считает отпечаток и на payload'е
// записи (где id нет), и на модели чтения (где он есть); хешировать id значило бы получить вечное
// «изменилось с момента утверждения», которое нечем погасить — ровно провал, описанный в шапке файла.
func TestProfileStorageIdsAreNotHashed(t *testing.T) {
	park := &entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{digestMachineProfile()},
		Presses:  []entity.TechCardPressProfile{digestPressProfile()},
	}
	before := constructionDigest(cardWithPark(park))
	park.Machines[0].Id, park.Machines[0].TechCardId = 17, 42
	park.Presses[0].Id, park.Presses[0].TechCardId = 18, 42
	require.Equal(t, before, constructionDigest(cardWithPark(park)))
}

// ПОРЯДОК ПРОФИЛЕЙ НЕ ЗНАЧИМ, поэтому проекция сортирует сама. Шаг ссылается на профиль по ключу, и
// перестановка списка не меняет ни одного указания цеху — но полагаться на ORDER BY чтения нельзя:
// payload записи через него не проходит, и два порядка одного парка дали бы два отпечатка, то есть
// пересохранение без единой правки читалось бы как «изменилось после подписи».
func TestProfileOrderDoesNotMoveTheDigest(t *testing.T) {
	second := digestMachineProfile()
	second.ProfileKey = "01J0MACHINEKEY000000000002"
	second.Label = ns("оверлок у двери")
	secondPress := digestPressProfile()
	secondPress.ProfileKey = "01J0PRESSKEY0000000000002B"
	secondPress.Label = ns("утюг")

	straight := &entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{digestMachineProfile(), second},
		Presses:  []entity.TechCardPressProfile{digestPressProfile(), secondPress},
	}
	reversed := &entity.TechCardEquipmentDefaults{
		Machines: []entity.TechCardMachineProfile{second, digestMachineProfile()},
		Presses:  []entity.TechCardPressProfile{secondPress, digestPressProfile()},
	}
	require.Equal(t, constructionDigest(cardWithPark(straight)), constructionDigest(cardWithPark(reversed)),
		"перестановка профилей сдвинула отпечаток — сортировка не в проекции")

	// И сортировка НЕ на месте: чужой слайс проекция трогать не смеет, иначе она молча меняла бы то,
	// что будет записано в базу.
	require.Equal(t, machineKey, reversed.Machines[1].ProfileKey,
		"проекция переупорядочила payload вызывающего")

	// Обратная половина: два РАЗНЫХ парка обязаны различаться, иначе тест выше удовлетворялся бы
	// проекцией, которая профили не читает.
	require.NotEqual(t, constructionDigest(cardWithPark(straight)),
		constructionDigest(cardWithPark(&entity.TechCardEquipmentDefaults{
			Machines: []entity.TechCardMachineProfile{digestMachineProfile()},
			Presses:  []entity.TechCardPressProfile{digestPressProfile(), secondPress}})))
}

// --- канонизация происходит ДО отпечатка ---------------------------------------------------------

// САМЫЙ КОВАРНЫЙ ИЗ ВОЗМОЖНЫХ ПРОВАЛОВ: если бы канонизация legacy-типа жила НЕ в конверсии
// proto→entity, а где-то после дайджеста, старый клиент штамповал бы подпись отпечатком одной формы,
// а следующее чтение сообщало бы отпечаток другой — и карточка навсегда осталась бы «изменённой
// после подписи», без единого способа это погасить.
//
// Оба payload'а описывают ОДИН шаг: старый бандл шлёт LOCKSTITCH и про машинки не знает, новый шлёт
// MACHINE + машинку. Отпечаток обязан совпасть.
func TestCanonicalisationHappensBeforeTheDigest(t *testing.T) {
	legacy, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "MD-1", Name: "Jacket",
		Operations: []*pb_common.TechCardOperation{{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_LOCKSTITCH,
			Zone:          zoneOuter,
		}},
	})
	require.NoError(t, err)

	modern, err := ConvertPbTechCardInsertToEntity(&pb_common.TechCardInsert{
		StyleNumber: "MD-1", Name: "Jacket", MachineFieldsAware: true,
		Operations: []*pb_common.TechCardOperation{{
			OperationType: pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
			Zone:          zoneOuter,
			MachineType:   pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		}},
	})
	require.NoError(t, err)

	require.Equal(t, constructionDigest(legacy), constructionDigest(modern),
		"legacy-payload и эквивалентный новый дали разные отпечатки: подпись старым клиентом рождала бы "+
			"вечное «изменилось после подписи»")

	// ...и совпали они потому, что канонизация действительно произошла в конверсии, а не потому, что
	// проекция типа шага не хеширует: entity старого бандла обязана уже нести новую пару.
	require.Equal(t, entity.OpTypeMachine, legacy.Operations[0].OperationType)
	require.Equal(t, "lockstitch", legacy.Operations[0].MachineType.String)
}
