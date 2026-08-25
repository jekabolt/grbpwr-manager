package techcardanalysis

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// ── ФИДЕЛЬНОСТЬ ФИКСТУРЫ ────────────────────────────────────────────────────────────────────────
//
// Сначала — что фикстура ЕСТЬ ТА КАРТОЧКА. Весь остальной пакет измеряется на ней, поэтому ошибка
// здесь не «один красный тест», а тихая переоценка всех двадцати четырёх проверок.

func TestCard8MatchesTheDumpShape(t *testing.T) {
	c := card8()

	if c.Id != 8 || c.StyleNumber.String != "SS26-008" || c.Name != "Blazer" {
		t.Errorf("шапка: id=%d style=%q name=%q", c.Id, c.StyleNumber.String, c.Name)
	}
	if c.Stage != entity.TechCardStageProto || c.ApprovalState != entity.TechCardApprovalDraft {
		t.Errorf("stage=%q approval=%q, ожидается proto/draft", c.Stage, c.ApprovalState)
	}
	if c.Purpose != entity.TechCardPurposeSellable || c.TargetGender.String != "male" {
		t.Errorf("purpose=%q gender=%q", c.Purpose, c.TargetGender.String)
	}
	if !c.RequiredSeamAllowanceMm.Valid || !c.RequiredSeamAllowanceMm.Decimal.Equal(decimal.RequireFromString("10.0")) {
		t.Errorf("required_seam_allowance_mm = %v, ожидается 10.0", c.RequiredSeamAllowanceMm)
	}

	if len(c.Pieces) != 48 {
		t.Fatalf("деталей %d, в дампе 48", len(c.Pieces))
	}
	if len(c.Operations) != 48 {
		t.Fatalf("операций %d, в дампе 48", len(c.Operations))
	}
	if len(c.BomItems) != 4 {
		t.Fatalf("строк BOM %d, в дампе 4", len(c.BomItems))
	}

	// РАЗМЕРНЫЙ РЯД — с прода, не с дампа (дамп секции размеров не печатает). nil здесь был бы
	// УТВЕРЖДЕНИЕМ «ряда нет», которого никто не измерял: C1 стоит ровно на этой тройке
	// (операции / детали / размерный ряд), и вакуумно пустой ряд перевернул бы её.
	if fmt.Sprint(c.SizeIds) != "[3 4 5 6]" {
		t.Errorf("размерный ряд %v, на проде size_id 3, 4, 5, 6 (s, m, l, xl)", c.SizeIds)
	}
	// А базовый размер на проде NULL — и это ПЯТАЯ пустота печатного пакета для C2, которую
	// пример §3.3 забывает.
	if c.BaseSampleSizeId.Valid {
		t.Errorf("base_sample_size_id = %v, на проде NULL", c.BaseSampleSizeId)
	}

	// Дефолты конструкции: наследуют ВСЕ 48 шагов, и это одна из несущих находок золотого ревью.
	cons := c.Construction
	if cons == nil || cons.DefaultSeamClass.String != "ss_plain" ||
		!cons.DefaultStitchesPerCm.Decimal.Equal(decimal.RequireFromString("3.00")) {
		t.Errorf("дефолты конструкции: %+v", cons)
	}
	if cons.HemFinish.Valid || cons.Notes.Valid {
		t.Errorf("hem_finish и notes в дампе NULL, а в фикстуре %v / %v", cons.HemFinish, cons.Notes)
	}
	if cons.EquipmentDefaults != nil {
		t.Errorf("профилей оборудования на карточке 0 — чтение стора оставляет здесь nil, а не пустую структуру")
	}

	// Пустоты, на которых стоят приёмки T4: их нельзя «дозаполнить для полноты».
	if len(c.Labels) != 0 || c.Packaging != nil || c.Costing != nil || len(c.Issues) != 0 || len(c.Media) != 0 {
		t.Errorf("карточка 8 не несёт ни лейблов, ни упаковки, ни костинга, ни issues, ни медиа")
	}
}

func TestCard8PiecesMatchTheDump(t *testing.T) {
	c := card8()

	ungraded, scopes := 0, PieceScopeKeys(c)
	for i := range c.Pieces {
		p := &c.Pieces[i]
		if len(p.LineKey) != 26 {
			t.Errorf("деталь %q: line_key %q длиной %d, ULID — 26 символов", p.Name, p.LineKey, len(p.LineKey))
		}
		if p.PiecesPerGarment != 1 {
			t.Errorf("деталь %q: ppg=%d, в дампе 1", p.Name, p.PiecesPerGarment)
		}
		if p.CutSymmetry.String != string(entity.PieceCutSymmetryIdentical) {
			t.Errorf("деталь %q: cut_symmetry=%q, в дампе identical", p.Name, p.CutSymmetry.String)
		}
		if p.Grainline != "lengthwise" {
			t.Errorf("деталь %q: grainline=%q, в дампе lengthwise", p.Name, p.Grainline)
		}
		if p.Fused {
			t.Errorf("деталь %q помечена клеевой — в дампе fused=0 у всех 48", p.Name)
		}
		if p.Ungraded {
			ungraded++
		}
	}
	// Ungraded ровно у семи pocketing-деталей: §7.2 печатает «| pocketing | ungraded» именно у них.
	if ungraded != 7 {
		t.Errorf("ungraded деталей %d, в дампе 7 (все pocketing)", ungraded)
	}

	// Ведро ткани резолвится через рецепт и совпадает с колонкой scope дампа у ВСЕХ 48 деталей.
	if len(scopes) != 48 {
		t.Fatalf("ведро ткани разрешилось у %d деталей из 48", len(scopes))
	}
	for _, want := range card8Pieces {
		got := scopes[card8PieceKey(want.id)]
		if got != want.scope {
			t.Errorf("деталь %q: ведро %q, в дампе %q", want.name, got, want.scope)
		}
	}

	// ↑ этот цикл сверяет фикстуру С САМОЙ СОБОЙ: таблица card8Pieces и строит карточку, и задаёт
	// ожидание. Он ловит поломку РЕЗОЛВЕРА и не ловит опечатку В ТАБЛИЦЕ. Ниже — ожидания,
	// СНЯТЫЕ С ДАМПА НЕЗАВИСИМО: раскладка 48 деталей по четырём ведрам и несколько поимённых
	// точек, включая те, где имя детали спорит с её тканью.
	byScope := map[string]int{}
	for _, s := range scopes {
		byScope[s]++
	}
	wantScopes := map[string]int{"main": 24, "lining": 13, "pocketing": 7, "other": 4}
	for scope, want := range wantScopes {
		if byScope[scope] != want {
			t.Errorf("в ведре %q деталей %d, в дампе %d", scope, byScope[scope], want)
		}
	}
	if len(byScope) != len(wantScopes) {
		t.Errorf("вёдер %d (%v), в дампе 4", len(byScope), byScope)
	}
	named := map[string]string{
		// «LIN» в имени — про подкладочную ДЕТАЛЬ, а не про подкладочную ткань: обе кроятся из main.
		"P_LIN_L_2": "main",
		"SL_LIN_R":  "main",
		// А эта — действительно из подкладки.
		"PCK_LOCKER": "lining",
		"BP_LIN_L_2": "lining",
		// Карманка.
		"PCK_MAIN_INS_S": "pocketing",
		"PCK_UP_INS_S":   "pocketing",
		// Плечевые накладки: строка «Плечевая», назначение other.
		"SHLD_L":   "other",
		"SHLD_1_R": "other",
		"BP_L":     "main",
	}
	for name, want := range named {
		p := card8PieceByName(c, name)
		if got := scopes[p.LineKey]; got != want {
			t.Errorf("деталь %q: ведро %q, в дампе %q", name, got, want)
		}
	}
	// Ungraded и pocketing на этой карточке — одно и то же множество: семь деталей карманки.
	for i := range c.Pieces {
		p := &c.Pieces[i]
		if p.Ungraded != (scopes[p.LineKey] == "pocketing") {
			t.Errorf("деталь %q: ungraded=%v при ведре %q — в дампе ungraded ровно у семи pocketing",
				p.Name, p.Ungraded, scopes[p.LineKey])
		}
	}
}

func TestCard8OperationsMatchTheDump(t *testing.T) {
	c := card8()

	works, smv, seamClass, machines := 0, 0, 0, map[string]bool{}
	for i := range c.Operations {
		op := &c.Operations[i]
		if want := int32((i + 1) * 10); op.OperationNumber.Int32 != want {
			t.Fatalf("операция на позиции %d носит номер %d, ожидается %d",
				i, op.OperationNumber.Int32, want)
		}
		if op.Work.Valid && op.Work.String != "" {
			works++
		}
		if op.SMV.Valid {
			smv++
		}
		if op.SeamClass.Valid {
			seamClass++
		}
		if op.MachineType.Valid {
			machines[op.MachineType.String] = true
		}
	}

	// VERIFIED FACTS §7.2: «Works assigned: 5 of 48. SMV: 0 of 48.»
	//
	// СЧЁТА МАЛО. Перенос press_open с оп 50 на оп 40 оставляет пятёрку нетронутой, а «работы
	// назначены пяти шагам» превращается в утверждение о ДРУГИХ пяти — и приёмка A8 (машина
	// назначенной работы против каталога 0329/0330) поедет вместе с ним. Карта поимённая.
	if works != 5 {
		t.Errorf("назначенных работ %d, в дампе 5", works)
	}
	wantWorks := map[int32]string{
		50: "press_open", 70: "press_open", 160: "press_open",
		470: "buttonhole", 480: "button_attach",
	}
	for i := range c.Operations {
		op := &c.Operations[i]
		num := op.OperationNumber.Int32
		if got := op.Work.String; got != wantWorks[num] {
			t.Errorf("работа операции #%d = %q, в дампе %q", num, got, wantWorks[num])
		}
	}
	if smv != 0 {
		t.Errorf("шагов с SMV %d, в дампе 0 из 48", smv)
	}
	// «On this card ALL 48 operations inherit all three» — ни один шаг не переопределяет класс шва.
	if seamClass != 0 {
		t.Errorf("шагов со своим классом шва %d, в дампе 0: все 48 наследуют дефолт карточки", seamClass)
	}
	// Четыре типа машин — на них стоит приёмка C4 «0 профилей / 4 типа». И снова счёта мало:
	// overlock оп 220, переставленный в lockstitch, оставляет множество четырёхэлементным, а
	// приёмка A4 («210/220 наследуют ss_plain — не шьётся на оверлоке») повисает в воздухе.
	if len(machines) != 4 {
		t.Errorf("типов машин %d, в дампе 4 (%v)", len(machines), sortedKeys(machines))
	}
	// ТРИ ОСИ ШАГА ПОИМЁННО: глагол, машина и зона. Дамп задаёт их построчно, и ни одна проверка
	// §3.1 не устоит, если ось поедет на соседний шаг: A2 стоит на 470/480, A3 — на четырёх ВТО,
	// A4 — на двух оверлоках, A6 — на зонах финиша.
	wantAxes := map[int32]struct{ otype, machine, zone string }{
		10:  {"machine", "lockstitch", "back"},
		20:  {"machine", "lockstitch", "back"},
		30:  {"machine", "lockstitch", "back"},
		40:  {"machine", "lockstitch", "back"},
		50:  {"press_open", "", "back"},
		60:  {"machine", "lockstitch", "pocket"},
		70:  {"press_open", "", "pocket"},
		80:  {"machine", "lockstitch", "front"},
		90:  {"machine", "lockstitch", "pocket"},
		100: {"press", "", "pocket"},
		110: {"machine", "lockstitch", "sleeve"},
		120: {"machine", "lockstitch", "sleeve"},
		130: {"machine", "lockstitch", "collar"},
		140: {"machine", "lockstitch", "collar"},
		150: {"machine", "lockstitch", "front"},
		160: {"press_open", "", "collar"},
		170: {"machine", "lockstitch", "outer"},
		180: {"machine", "lockstitch", "outer"},
		190: {"machine", "lockstitch", "front"},
		200: {"machine", "lockstitch", "front"},
		210: {"machine", "overlock", "pocket"},
		220: {"machine", "overlock", "pocket"},
		230: {"machine", "lockstitch", "pocket"},
		240: {"machine", "lockstitch", "pocket"},
		250: {"machine", "lockstitch", "pocket"},
		260: {"machine", "lockstitch", "pocket"},
		270: {"machine", "lockstitch", "outer"},
		280: {"machine", "lockstitch", "outer"},
		290: {"machine", "lockstitch", "pocket"},
		300: {"machine", "lockstitch", "lining"},
		310: {"machine", "lockstitch", "lining"},
		320: {"machine", "lockstitch", "lining"},
		330: {"machine", "lockstitch", "lining"},
		340: {"machine", "lockstitch", "lining"},
		350: {"machine", "lockstitch", "lining"},
		360: {"machine", "lockstitch", "lining"},
		370: {"machine", "lockstitch", "lining"},
		380: {"machine", "lockstitch", "lining"},
		390: {"machine", "lockstitch", "lining"},
		400: {"machine", "lockstitch", "lining"},
		410: {"machine", "lockstitch", "lining"},
		420: {"machine", "lockstitch", "interlining"},
		430: {"machine", "lockstitch", "interlining"},
		440: {"machine", "lockstitch", "outer"},
		450: {"machine", "lockstitch", "outer"},
		460: {"machine", "lockstitch", "outer"},
		470: {"machine", "buttonhole", "front"},
		480: {"machine", "button_attach", "front"},
	}
	if len(wantAxes) != 48 {
		t.Fatalf("таблица ожиданий покрывает %d шагов из 48", len(wantAxes))
	}
	for i := range c.Operations {
		op := &c.Operations[i]
		num := op.OperationNumber.Int32
		want, known := wantAxes[num]
		if !known {
			t.Errorf("шаг #%d в дампе отсутствует", num)
			continue
		}
		if string(op.OperationType) != want.otype {
			t.Errorf("operation_type шага #%d = %q, в дампе %q", num, op.OperationType, want.otype)
		}
		if op.MachineType.String != want.machine {
			t.Errorf("machine_type шага #%d = %q, в дампе %q", num, op.MachineType.String, want.machine)
		}
		if string(op.Zone) != want.zone {
			t.Errorf("zone шага #%d = %q, в дампе %q", num, op.Zone, want.zone)
		}
		// NULL и пустая строка у machine_type — одно и то же «нет машины», но хранится это NULL'ом:
		// ВТО-шаг с machine_type='' прошёл бы в предикат «машина задана» у неаккуратной проверки.
		if want.machine == "" && op.MachineType.Valid {
			t.Errorf("machine_type шага #%d — пустая строка, а в дампе NULL", num)
		}
	}

	// Ноты: пять непустых, остальные — ПУСТЫЕ СТРОКИ, а не NULL (так их печатает дамп).
	notes := map[int32]string{40: "Low thread tension stich", 110: "front seam", 120: "front seam",
		210: "join pockets", 220: "join pockets"}
	for i := range c.Operations {
		op := &c.Operations[i]
		want := notes[op.OperationNumber.Int32]
		if op.Note.String != want {
			t.Errorf("нота операции #%d = %q, ожидается %q", op.OperationNumber.Int32, op.Note.String, want)
		}
	}

	// Материалы на шаге: ровно две операции подшивают строку BOM, и обе — «Плечевая».
	withBom := []int32{}
	for i := range c.Operations {
		if len(c.Operations[i].BomIds) > 0 {
			withBom = append(withBom, c.Operations[i].OperationNumber.Int32)
		}
	}
	if fmt.Sprint(withBom) != "[420 430]" {
		t.Errorf("строки BOM подшиты к операциям %v, в дампе к 420 и 430", withBom)
	}
}

// ── GROUND TRUTH: приёмка T2 ────────────────────────────────────────────────────────────────────

func TestGroundTruthCard8(t *testing.T) {
	c := card8()
	gt := ComputeGroundTruth(c)

	// §1: сохранённая карточка нарушений не несёт — и если понесла, промпту нельзя утверждать
	// «граф ацикличен».
	if len(gt.Violations) != 0 {
		t.Fatalf("проход нашёл %d нарушений на карточке, которая прошла запись: %+v",
			len(gt.Violations), gt.Violations)
	}
	if !gt.Marked {
		t.Fatal("карточка несёт производящие шаги — Marked обязан быть true (условие включения правила 4)")
	}

	// ТЕРМИНАЛ РОВНО ОДИН, И ЭТО «blazer».
	if len(gt.Terminals) != 1 || gt.Terminals[0] != "blazer" {
		t.Fatalf("терминалы %v, ожидается ровно один «blazer»", gt.Terminals)
	}
	if at := gt.ProducerOf["blazer"]; gt.Steps[at].OperationNumber != 460 {
		t.Errorf("«blazer» произведён шагом #%d, ожидается 460", gt.Steps[at].OperationNumber)
	}

	// ДЕВЯТЬ ОБРАБОТОК ИЗ СОРОКА ВОСЬМИ — поимённо (§1 и VERIFIED FACTS §7.2).
	wantProcessing := []int32{40, 50, 70, 100, 160, 210, 220, 470, 480}
	var gotProcessing []int32
	for _, s := range gt.Steps {
		if s.Kind == StepProcessing {
			gotProcessing = append(gotProcessing, s.OperationNumber)
		}
	}
	if fmt.Sprint(gotProcessing) != fmt.Sprint(wantProcessing) {
		t.Errorf("обработки %v, ожидаются %v", gotProcessing, wantProcessing)
	}
	if gt.ProcessingCount != 9 {
		t.Errorf("ProcessingCount = %d, ожидается 9", gt.ProcessingCount)
	}

	// ПОГЛОЩЕНИЕ — ровно оп 260, и оно ОДНО. 250 — обычный джойн: оба производят «pocket base», и
	// спутать их значило бы объявить легальную цепочку дублем-производителем.
	var absorbing []int32
	for _, s := range gt.Steps {
		if s.Kind == StepAbsorbing {
			absorbing = append(absorbing, s.OperationNumber)
		}
	}
	if fmt.Sprint(absorbing) != "[260]" {
		t.Errorf("поглощающие шаги %v, ожидается ровно [260]", absorbing)
	}
	if s, _ := gt.StepByNumber(250); s.Kind != StepJoin {
		t.Errorf("оп 250 классифицирована как %s, ожидается join", s.Kind)
	}
	if s, _ := gt.StepByNumber(260); s.Kind != StepAbsorbing {
		t.Errorf("оп 260 классифицирована как %s, ожидается absorbing", s.Kind)
	}
	// Узел «pocket base» остался ОДНИМ узлом с одним первым производителем.
	pocketBase, ok := gt.Units["pocket base"]
	if !ok {
		t.Fatal("узла «pocket base» нет вовсе")
	}
	if gt.Steps[pocketBase.ProducedAt].OperationNumber != 250 {
		t.Errorf("первый производитель «pocket base» — шаг #%d, ожидается 250",
			gt.Steps[pocketBase.ProducedAt].OperationNumber)
	}
	if len(pocketBase.AbsorbedAt) != 1 || gt.Steps[pocketBase.AbsorbedAt[0]].OperationNumber != 260 {
		t.Errorf("поглощения узла «pocket base»: %v, ожидается одно на шаге 260", pocketBase.AbsorbedAt)
	}

	if gt.JoinCount+gt.AbsorbingCount+gt.ProcessingCount != 48 {
		t.Errorf("классификация покрыла %d шагов из 48",
			gt.JoinCount+gt.AbsorbingCount+gt.ProcessingCount)
	}

	// ВСЕ 48 ДЕТАЛЕЙ ПОТРЕБЛЕНЫ РОВНО ПО РАЗУ.
	if len(gt.PieceConsumedBy) != 48 {
		t.Errorf("потреблённых деталей %d из 48", len(gt.PieceConsumedBy))
	}
	if len(gt.UnconsumedPieces) != 0 {
		t.Errorf("не потреблены детали: %s", namesOf(c, gt.UnconsumedPieces))
	}
	if len(gt.UnreachedPieces) != 0 {
		t.Errorf("не дошли до терминала: %s", namesOf(c, gt.UnreachedPieces))
	}
	// «Ровно по разу» — не только «потреблены»: каждая деталь обязана уйти к ОДНОМУ шагу, и
	// суммарно шаги обязаны съесть ровно 48 деталей, ни одной дважды.
	eaten := 0
	for i := range c.Operations {
		for _, in := range c.Operations[i].AssemblyInputs {
			if in.Kind != entity.AssemblyInputPiece {
				continue
			}
			num := c.Operations[i].OperationNumber.Int32
			if gt.PieceConsumedBy[in.Key] != indexOfStep(gt, num) {
				t.Errorf("деталь %q — вход шага #%d, а числится за шагом #%d",
					namesOf(c, []string{in.Key}), num,
					gt.Steps[gt.PieceConsumedBy[in.Key]].OperationNumber)
			}
			eaten++
		}
	}
	if eaten != 48 {
		t.Errorf("шаги берут детали %d раз, а деталей 48 — значит какая-то взята дважды или ни разу", eaten)
	}

	// Фронтир: единственный терминал плюс ничего больше — узлов на столе один, деталей ноль.
	if len(gt.Frontier) != 1 || gt.Frontier[0] != "blazer" {
		t.Errorf("фронтир %v, ожидается ровно [blazer]", gt.Frontier)
	}
}

// TestGroundTruthDetectsATornGraph is the fire side: ground truth must MOVE when the card does.
// Без этой половины «терминал ровно один» одинаково зелен и на сторожевой проверке у мёртвого кода.
func TestGroundTruthDetectsATornGraph(t *testing.T) {
	// Убираем финальный джойн — тот самый случай, который предсказывает отказ релиза (C6).
	c := card8()
	card8DropOperation(c, 460)
	gt := ComputeGroundTruth(c)

	if len(gt.Terminals) != 2 {
		t.Fatalf("без оп 460 терминалов %v, ожидается два («lining» и «base»)", gt.Terminals)
	}
	sorted := append([]string(nil), gt.Terminals...)
	sort.Strings(sorted)
	if strings.Join(sorted, ",") != "base,lining" {
		t.Errorf("терминалы %v, ожидаются base и lining", sorted)
	}
	// При двух терминалах список сирот НЕ выдаётся: формально ни одна деталь не достигает изделия,
	// и сорок восемь имён утопили бы настоящую причину.
	if gt.UnreachedPieces != nil {
		t.Errorf("при двух терминалах UnreachedPieces обязан быть nil, а там %d имён", len(gt.UnreachedPieces))
	}

	// Деталь, которую трогает только ОБРАБОТКА, НЕ ПОТРЕБЛЕНА: она упомянута, но ни в один узел не
	// попадает. Ровно то различие, из-за которого покрытие считается по джойнам, а не по
	// «ключ где-то встретился» — прототип проверял упоминание и такую деталь пропускал.
	c2 := card8()
	orphan := entity.TechCardPiece{
		Id: 84, Name: "SHLD_SPARE", LineKey: card8PieceKey(84), PiecesPerGarment: 1,
		CutSymmetry: text(string(entity.PieceCutSymmetryIdentical)), Grainline: "lengthwise",
	}
	c2.Pieces = append(c2.Pieces, orphan)
	c2.Operations = append(c2.Operations, entity.TechCardOperation{
		OperationNumber: sql.NullInt32{Int32: 490, Valid: true},
		OperationType:   "press", Zone: "interlining", Note: text(""),
		AssemblyInputs: []entity.OperationInput{{Kind: entity.AssemblyInputPiece, Key: orphan.LineKey}},
		InputKeys:      []string{orphan.LineKey},
		PieceIds:       []int{orphan.Id},
		PieceLineKeys:  []string{orphan.LineKey},
	})

	gt2 := ComputeGroundTruth(c2)
	if len(gt2.Violations) != 0 {
		t.Fatalf("мутация родила нарушения %+v — стенд обязан оставаться карточкой, которую "+
			"запись бы приняла", gt2.Violations)
	}
	if len(gt2.UnconsumedPieces) != 1 || gt2.UnconsumedPieces[0] != orphan.LineKey {
		t.Fatalf("непотреблённые детали %s, ожидается ровно SHLD_SPARE",
			namesOf(c2, gt2.UnconsumedPieces))
	}
	if len(gt2.UnreachedPieces) != 1 || gt2.UnreachedPieces[0] != orphan.LineKey {
		t.Errorf("не дошли до терминала %s, ожидается ровно SHLD_SPARE", namesOf(c2, gt2.UnreachedPieces))
	}
	// Терминал при этом по-прежнему один: дыра именно в покрытии деталей, и C6 обязана уметь
	// говорить о ней отдельно от «терминалов не один».
	if len(gt2.Terminals) != 1 || gt2.Terminals[0] != "blazer" {
		t.Errorf("терминалы %v, ожидается ровно [blazer]", gt2.Terminals)
	}
	// А новый шаг — обработка: его вход остался на столе, поэтому деталь и не съедена.
	if s, _ := gt2.StepByNumber(490); s.Kind != StepProcessing {
		t.Errorf("шаг 490 классифицирован как %s, ожидается processing", s.Kind)
	}
}

// TestGroundTruthClassifiesAbsorptionByTheEngine pins that ПОГЛОЩЕНИЕ is read off the sweep, not
// re-derived by a second formula. Шаг, чей выход совпадает с ключом узла, КОТОРОГО ЕЩЁ НЕТ, — не
// поглощение, а обычный первый производитель, и вторая формула («выход есть среди входов») этого
// не различает.
func TestGroundTruthClassifiesAbsorptionByTheEngine(t *testing.T) {
	c := card8()
	gt := ComputeGroundTruth(c)
	for _, s := range gt.Steps {
		if s.Kind != StepAbsorbing {
			continue
		}
		u := gt.Units[s.OutputUnitKey]
		if gt.Steps[u.ProducedAt].Index == s.Index {
			t.Errorf("шаг #%d назван поглощением, но он же первый производитель узла %q",
				s.OperationNumber, s.OutputUnitKey)
		}
	}
}

func TestGroundTruthOfEmptyCardIsUsable(t *testing.T) {
	// Карточка без единого сборочного факта проходит вакуумно (§1, ранний выход `marked`) — и это
	// значение, а не паника: аудит зовут на любой карточке, включая пустую.
	gt := ComputeGroundTruth(nil)
	if gt.Marked || len(gt.Steps) != 0 || gt.Units == nil || gt.ConsumedBy == nil || gt.PieceConsumedBy == nil {
		t.Fatalf("пустая карточка дала %+v — карты обязаны быть непустыми значениями", gt)
	}
	if _, ok := gt.StepByNumber(10); ok {
		t.Error("на пустой карточке нашёлся шаг #10")
	}

	empty := &entity.TechCard{}
	if gt2 := ComputeGroundTruth(empty); gt2.Marked {
		t.Error("карточка без операций помечена как несущая производящий шаг")
	}
}

// ── ХЕЛПЕРЫ ─────────────────────────────────────────────────────────────────────────────────────

func namesOf(c *entity.TechCard, lineKeys []string) string {
	if len(lineKeys) == 0 {
		return "(нет)"
	}
	out := make([]string, 0, len(lineKeys))
	for _, k := range lineKeys {
		name := k
		for i := range c.Pieces {
			if c.Pieces[i].LineKey == k {
				name = c.Pieces[i].Name
				break
			}
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

func indexOfStep(gt GroundTruth, number int32) int {
	s, _ := gt.StepByNumber(number)
	return s.Index
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
