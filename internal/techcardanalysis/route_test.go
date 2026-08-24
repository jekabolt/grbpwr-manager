package techcardanalysis

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── ПРИЁМКА A1–A10 (design §3.1) ────────────────────────────────────────────────────────────────
//
// У КАЖДОЙ проверки здесь ДВА направления, и это не формальность. Тест, который лишь исполняет код
// проверки, зелен и тогда, когда проверка сторожит мёртвую ветку: он доказывает, что функция
// вызвалась, а не что она что-то ловит. Поэтому у всякой проверки есть мутация, на которой она
// ОБЯЗАНА заговорить, и состояние, на котором она ОБЯЗАНА молчать, — включая пять проверок,
// молчащих на карточке 8 (A6–A10): у них fire-направление построено мутацией фикстуры.
//
// t.Parallel НЕ ИСПОЛЬЗУЕТСЯ НИГДЕ В ПАКЕТЕ: реестр проверок — пакетный слайс, а каталог работ
// (A8) — пакетный снимок процесса.

// ── ХЕЛПЕРЫ ─────────────────────────────────────────────────────────────────────────────────────

// rtFx is the fixed currency channel of every route test: base EUR, no rates. Проверки маршрута
// денег не читают вовсе, но RunAudit гоняет ВЕСЬ реестр, и фиксированный fx держит соседей
// детерминированными.
var rtFx = Fx{Base: "EUR"}

func rtFindings(c *entity.TechCard) []Finding { return RunAudit(c, rtFx).Findings }

// rtWithTitle returns the findings whose title contains substr.
func rtWithTitle(fs []Finding, substr string) []Finding {
	var out []Finding
	for _, f := range fs {
		if strings.Contains(f.Title, substr) {
			out = append(out, f)
		}
	}
	return out
}

// rtOne requires exactly one finding with that title substring and returns it.
func rtOne(t *testing.T, fs []Finding, substr string) Finding {
	t.Helper()
	got := rtWithTitle(fs, substr)
	if len(got) != 1 {
		t.Fatalf("want exactly one finding titled %q, got %d:\n%s", substr, len(got), rtDump(fs))
	}
	return got[0]
}

// rtNone requires that nothing carries that title substring.
func rtNone(t *testing.T, fs []Finding, substr string) {
	t.Helper()
	if got := rtWithTitle(fs, substr); len(got) != 0 {
		t.Fatalf("want silence on %q, got %d finding(s):\n%s", substr, len(got), rtDump(got))
	}
}

func rtDump(fs []Finding) string {
	var b strings.Builder
	for _, f := range fs {
		b.WriteString("  [" + f.Category + "/" + f.Severity + "] " + f.Title + " refs=" +
			strings.Join(f.Refs, ",") + "\n")
	}
	if b.Len() == 0 {
		return "  (no findings)\n"
	}
	return b.String()
}

func rtHasRef(f Finding, ref string) bool {
	for _, r := range f.Refs {
		if r == ref {
			return true
		}
	}
	return false
}

func rtWantRefs(t *testing.T, f Finding, refs ...string) {
	t.Helper()
	for _, r := range refs {
		if !rtHasRef(f, r) {
			t.Errorf("finding %q: want anchor %q among %v", f.Title, r, f.Refs)
		}
	}
}

// rtAppendOp appends one operation and stamps its number the way the write path does — (i+1)*10 —
// so the mutated card is one the server could actually have saved. Возвращает номер: указатель в
// срез пережил бы следующий append только по счастливой случайности.
func rtAppendOp(c *entity.TechCard, op entity.TechCardOperation) int32 {
	num := int32((len(c.Operations) + 1) * 10)
	op.OperationNumber = sql.NullInt32{Int32: num, Valid: true}
	if op.Note.String == "" && !op.Note.Valid {
		op.Note = text("") // 43 ноты карточки 8 — пустые строки, а не NULL; новая ведёт себя так же
	}
	c.Operations = append(c.Operations, op)
	return num
}

// rtUnitInput / rtPieceInput spell one input of an appended operation.
func rtUnitInput(key string) entity.OperationInput {
	return entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: key}
}

func rtPieceInput(c *entity.TechCard, pieceName string) entity.OperationInput {
	p := card8PieceByName(c, pieceName)
	return entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: p.LineKey}
}

// rtAppendBom appends one BOM line and returns it; the line_key is synthetic and stable.
func rtAppendBom(c *entity.TechCard, name string, section entity.TechCardBomSection, kind sql.NullString) *entity.TechCardBomItem {
	id := 100 + len(c.BomItems)
	c.BomItems = append(c.BomItems, entity.TechCardBomItem{
		Id:      id,
		LineKey: "01M0TCBOM000000000000" + string(rune('A'+len(c.BomItems)%26)) + "0000",
		Section: section,
		Name:    name,
		Kind:    kind,
		Unit:    text("pcs"),
	})
	return &c.BomItems[len(c.BomItems)-1]
}

// rtLinkBom links an operation to a BOM line by both halves of the link, exactly as the store reads
// it (0200: ids and line keys travel together).
func rtLinkBom(op *entity.TechCardOperation, b *entity.TechCardBomItem) {
	op.BomLineKeys = append(op.BomLineKeys, b.LineKey)
	op.BomIds = append(op.BomIds, b.Id)
}

// ── A1. РЕГИСТРОВАЯ КОЛЛИЗИЯ КЛЮЧЕЙ УЗЛОВ ───────────────────────────────────────────────────────

func TestA1FiresOnBaseAndBaseOfCard8(t *testing.T) {
	fs := rtFindings(card8())
	f := rtOne(t, fs, "differ only in case")

	if f.Category != CategoryNaming || f.Severity != SeverityWarning {
		t.Errorf("want naming/warning, got %s/%s", f.Category, f.Severity)
	}
	if f.Confidence != "" {
		t.Errorf("A1 is a byte fact, not a guess: confidence must be empty, got %q", f.Confidence)
	}
	// Формы — БАЙТ-В-БАЙТ в тексте; lower-fold только группирует.
	if !strings.Contains(f.Title, `"Base"`) || !strings.Contains(f.Title, `"base"`) {
		t.Errorf("title must quote both byte forms, got %q", f.Title)
	}
	rtWantRefs(t, f, RefUnit("Base"), RefUnit("base"), RefOp(270), RefOp(450))
	if !strings.Contains(f.Detail, "op 270") || !strings.Contains(f.Detail, "op 450") {
		t.Errorf("detail must name both producers, got %q", f.Detail)
	}

	// ТРЕТИЙ СМЫСЛ ТОГО ЖЕ СЛОВА В НАХОДКУ НЕ ПОПАДАЕТ: «pocket base» (250/260) и «lining base»
	// (350) — не коллизия, а другие узлы, и назвать их здесь значило бы отправить технолога
	// переименовывать верное.
	for _, other := range []string{"pocket base", "lining base"} {
		if strings.Contains(f.Detail, other) || strings.Contains(f.Title, other) {
			t.Errorf("A1 must not drag %q into the collision: %q / %q", other, f.Title, f.Detail)
		}
	}
}

func TestA1IsSilentWhenTheCollisionIsGone(t *testing.T) {
	c := card8()
	// Переименование в РАЗЛИЧНЫЙ ключ, а не «base» → «Base»: второе сделало бы 450 вторым
	// производителем узла «Base», а дубль-производитель — состояние, которое запись отвергает
	// (§1). Мутация обязана оставлять карточку такой, какую сервер мог сохранить.
	card8OpByNumber(c, 450).OutputUnitKey = text("shell")
	card8OpByNumber(c, 450).OutputUnitName = text("shell")
	in := card8OpByNumber(c, 460).AssemblyInputs
	for i := range in {
		if in[i].Key == "base" {
			in[i].Key = "shell"
		}
	}
	card8OpByNumber(c, 460).InputKeys = []string{"lining", "shell"}

	rtNone(t, rtFindings(c), "differ only in case")
}

func TestA1FindsEveryColludingForm(t *testing.T) {
	c := card8()
	// Третья форма того же слова — та же группа, одна находка, три формы.
	card8OpByNumber(c, 340).OutputUnitKey = text("BASE")
	card8OpByNumber(c, 340).OutputUnitName = text("BASE")
	in := card8OpByNumber(c, 350).AssemblyInputs
	for i := range in {
		if in[i].Key == "lining back" {
			in[i].Key = "BASE"
		}
	}

	f := rtOne(t, rtFindings(c), "differ only in case")
	for _, form := range []string{`"Base"`, `"base"`, `"BASE"`} {
		if !strings.Contains(f.Title, form) {
			t.Errorf("want form %s in title %q", form, f.Title)
		}
	}
}

// ── A2. ДИСКРИМИНАТОРЫ ВИДОВ ────────────────────────────────────────────────────────────────────

func TestA2FiresOn470And480OfCard8(t *testing.T) {
	fs := rtFindings(card8())

	hole := rtOne(t, fs, "Buttonhole unspecified")
	rtWantRefs(t, hole, RefOp(470))
	if hole.Category != CategoryParameter || hole.Severity != SeverityWarning {
		t.Errorf("want parameter/warning, got %s/%s", hole.Category, hole.Severity)
	}

	counts := rtWithTitle(fs, "one-button garment")
	if len(counts) != 2 {
		t.Fatalf("want the placement-count finding on 470 AND 480, got %d:\n%s", len(counts), rtDump(counts))
	}
	if !rtHasRef(counts[0], RefOp(470)) || !rtHasRef(counts[1], RefOp(480)) {
		t.Errorf("want anchors op:470 then op:480, got %v and %v", counts[0].Refs, counts[1].Refs)
	}
}

func TestA2IsSilentOnceTheDiscriminatorsAreFilled(t *testing.T) {
	c := card8()
	op470 := card8OpByNumber(c, 470)
	op470.ButtonholeStyle = text("straight")
	op470.CutLengthMm = dec("18")
	op470.PlacementCount = sql.NullInt32{Int32: 5, Valid: true}
	card8OpByNumber(c, 480).PlacementCount = sql.NullInt32{Int32: 5, Valid: true}

	fs := rtFindings(c)
	rtNone(t, fs, "Buttonhole unspecified")
	rtNone(t, fs, "one-button garment")
}

func TestA2ButtonholeIsSuppressedByEitherHalf(t *testing.T) {
	// Текст находки утверждает «no style, no cut length» — заполненная половина делает его ложью,
	// поэтому подавляет её любая из двух.
	for _, tc := range []struct {
		name string
		fill func(*entity.TechCardOperation)
	}{
		{"style only", func(op *entity.TechCardOperation) { op.ButtonholeStyle = text("eyelet") }},
		{"cut length only", func(op *entity.TechCardOperation) { op.CutLengthMm = dec("22") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := card8()
			tc.fill(card8OpByNumber(c, 470))
			rtNone(t, rtFindings(c), "Buttonhole unspecified")
		})
	}
}

func TestA2ObeysTheAggregationLaw(t *testing.T) {
	c := card8()
	// Шесть шагов на пуговичном автомате и ни одного счёта — закон §3.0 требует ОДНУ находку с
	// дробью, а не шесть.
	for _, n := range []int32{10, 20, 30, 40} {
		card8OpByNumber(c, n).MachineType = text("button_attach")
	}

	f := rtOne(t, rtFindings(c), "No placement count on")
	if !strings.Contains(f.Title, "6 of 6") {
		t.Errorf("aggregated title must carry the fraction, got %q", f.Title)
	}
	if len(f.Refs) != 3 {
		t.Errorf("aggregated finding carries THREE sample anchors, got %v", f.Refs)
	}
	rtNone(t, rtFindings(c), "one-button garment") // пер-операционная ветка обязана исчезнуть
}

func TestA2FiresOnTrimAndHardwareDiscriminators(t *testing.T) {
	t.Run("trim without action", func(t *testing.T) {
		c := card8()
		op := card8OpByNumber(c, 40)
		op.OperationType = entity.OpTypeTrim
		op.MachineType = sql.NullString{}

		f := rtOne(t, rtFindings(c), "does not say what is trimmed")
		rtWantRefs(t, f, RefOp(40))

		c2 := card8()
		op2 := card8OpByNumber(c2, 40)
		op2.OperationType = entity.OpTypeTrim
		op2.MachineType = sql.NullString{}
		op2.TrimAction = text("grade_layers")
		rtNone(t, rtFindings(c2), "does not say what is trimmed")
	})

	t.Run("hardware without attach method", func(t *testing.T) {
		c := card8()
		op := card8OpByNumber(c, 40)
		op.OperationType = entity.OpTypeHardwareSet
		op.MachineType = sql.NullString{}

		f := rtOne(t, rtFindings(c), "how the part is attached")
		rtWantRefs(t, f, RefOp(40))

		c2 := card8()
		op2 := card8OpByNumber(c2, 40)
		op2.OperationType = entity.OpTypeHardwareSet
		op2.MachineType = sql.NullString{}
		op2.AttachMethod = text("prong_clinch")
		rtNone(t, rtFindings(c2), "how the part is attached")
	})
}

// ── A3. ТЕРМОПАРАМЕТРЫ ──────────────────────────────────────────────────────────────────────────

func TestA3FiresAsOneAggregateOnCard8(t *testing.T) {
	fs := rtFindings(card8())
	f := rtOne(t, fs, "Pressing parameters missing")

	if !strings.Contains(f.Title, "4 of 4") {
		t.Errorf("want the fraction «4 of 4» in the title, got %q", f.Title)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("pressing is a warning, got %s", f.Severity)
	}
	// Якоря-образцы — ПЕРВЫЕ ТРИ пропуска в порядке маршрута: 50, 70, 100 (160 в образцы не едет).
	if len(f.Refs) != 3 {
		t.Fatalf("want three sample anchors, got %v", f.Refs)
	}
	rtWantRefs(t, f, RefOp(50), RefOp(70), RefOp(100))
}

func TestA3AnySettingSuppressesItsOwnStep(t *testing.T) {
	c := card8()
	card8OpByNumber(c, 50).PressTemperatureC = sql.NullInt32{Int32: 150, Valid: true}

	fs := rtFindings(c)
	rtNone(t, fs, "Pressing parameters missing") // агрегата больше нет: пропусков три
	per := rtWithTitle(fs, "Pressing parameters are not specified")
	if len(per) != 3 {
		t.Fatalf("want three per-operation findings (70/100/160), got %d:\n%s", len(per), rtDump(per))
	}
	for _, f := range per {
		if rtHasRef(f, RefOp(50)) {
			t.Errorf("op 50 names a temperature and must be silent: %v", f.Refs)
		}
	}
}

func TestA3IsSilencedByAnApplicablePressProfile(t *testing.T) {
	t.Run("universal profile silences every pressing step", func(t *testing.T) {
		c := card8()
		c.Construction.EquipmentDefaults = &entity.TechCardEquipmentDefaults{
			Presses: []entity.TechCardPressProfile{{
				ProfileKey: "01M0PRESS0000000000000001", PressEquipment: "iron",
				// press_operation_type NULL = УНИВЕРСАЛЬНЫЙ профиль (0306).
				PressTemperatureC: sql.NullInt32{Int32: 150, Valid: true},
			}},
		}
		fs := rtFindings(c)
		rtNone(t, fs, "Pressing parameters missing")
		rtNone(t, fs, "Pressing parameters are not specified")
	})

	t.Run("a profile for another verb silences nothing", func(t *testing.T) {
		c := card8()
		c.Construction.EquipmentDefaults = &entity.TechCardEquipmentDefaults{
			Presses: []entity.TechCardPressProfile{{
				ProfileKey: "01M0PRESS0000000000000002", PressEquipment: "fusing_press",
				PressOperationType: text("fusing"),
				PressTemperatureC:  sql.NullInt32{Int32: 150, Valid: true},
			}},
		}
		f := rtOne(t, rtFindings(c), "Pressing parameters missing")
		if !strings.Contains(f.Title, "4 of 4") {
			t.Errorf("a fusing-only profile must not cover pressing steps, got %q", f.Title)
		}
	})
}

func TestA3RaisesFusingToError(t *testing.T) {
	c := card8()
	num := rtAppendOp(c, entity.TechCardOperation{
		OperationType:  entity.OpTypeFusing,
		Zone:           "front",
		AssemblyInputs: []entity.OperationInput{rtPieceInput(c, "SHLD_L")},
	})

	f := rtOne(t, rtFindings(c), "Fusing parameters are not specified")
	if f.Severity != SeverityError {
		t.Errorf("fusing without temperature is an error (the interlining either will not stick or "+
			"will burn), got %s", f.Severity)
	}
	rtWantRefs(t, f, RefOp(num))

	c2 := card8()
	num2 := rtAppendOp(c2, entity.TechCardOperation{
		OperationType:     entity.OpTypeFusing,
		Zone:              "front",
		AssemblyInputs:    []entity.OperationInput{rtPieceInput(c2, "SHLD_L")},
		PressTemperatureC: sql.NullInt32{Int32: 140, Valid: true},
		PressDwellSec:     sql.NullInt32{Int32: 12, Valid: true},
	})
	_ = num2
	rtNone(t, rtFindings(c2), "Fusing parameters are not specified")
}

// ── A4. КЛАСС ШВА × МАШИНА ──────────────────────────────────────────────────────────────────────

func TestA4FiresOnTheInheritedOverlockPairOfCard8(t *testing.T) {
	fs := rtFindings(card8())
	got := rtWithTitle(fs, "is not producible on")
	if len(got) != 2 {
		t.Fatalf("want the pair 210/220, got %d:\n%s", len(got), rtDump(got))
	}
	for i, want := range []int32{210, 220} {
		f := got[i]
		if f.Severity != SeverityError || f.Category != CategoryParameter {
			t.Errorf("want parameter/error, got %s/%s", f.Category, f.Severity)
		}
		rtWantRefs(t, f, RefOp(want))
		// УНАСЛЕДОВАННЫЙ класс называется в тексте явно: без этого технолог пойдёт искать
		// override в строке шага, где его нет и не будет.
		if !strings.Contains(f.Detail, "inherited from the card default") {
			t.Errorf("op %d: detail must say the class is inherited, got %q", want, f.Detail)
		}
		if !strings.Contains(f.Detail, "ss_plain") || !strings.Contains(f.Detail, "overlock") {
			t.Errorf("op %d: detail must name both halves of the pair, got %q", want, f.Detail)
		}
	}
}

func TestA4SuppressorsAreImplemented(t *testing.T) {
	for _, tc := range []struct {
		name  string
		apply func(*entity.TechCard)
	}{
		{"override to a producible class", func(c *entity.TechCard) {
			card8OpByNumber(c, 210).SeamClass = text("ef_hem_raw")
			card8OpByNumber(c, 220).SeamClass = text("ef_hem_raw")
		}},
		{"no machine named", func(c *entity.TechCard) {
			card8OpByNumber(c, 210).MachineType = sql.NullString{}
			card8OpByNumber(c, 220).MachineType = sql.NullString{}
		}},
		{"no effective class at all", func(c *entity.TechCard) {
			c.Construction.DefaultSeamClass = sql.NullString{}
		}},
		{"pair is not in the curated table", func(c *entity.TechCard) {
			c.Construction.DefaultSeamClass = text("bs_bound")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := card8()
			tc.apply(c)
			rtNone(t, rtFindings(c), "is not producible on")
		})
	}
}

func TestA4NamesAnOverrideAsAnOverride(t *testing.T) {
	c := card8()
	card8OpByNumber(c, 210).SeamClass = text("ls_flat_felled")

	f := rtWithTitle(rtFindings(c), "ls_flat_felled is not producible")
	if len(f) != 1 {
		t.Fatalf("want one finding for the overridden step, got %d", len(f))
	}
	if strings.Contains(f[0].Detail, "inherited") {
		t.Errorf("a class set on the step must not be called inherited: %q", f[0].Detail)
	}
	if !strings.Contains(f[0].Detail, "set on the step") {
		t.Errorf("want «set on the step» in %q", f[0].Detail)
	}
}

// ── A5. НОТЫ-НОСИТЕЛИ ───────────────────────────────────────────────────────────────────────────

func TestA5FiresOnOp40OfCard8(t *testing.T) {
	fs := rtFindings(card8())
	f := rtOne(t, fs, "Instruction lives only in a note")

	rtWantRefs(t, f, RefOp(40))
	if !strings.Contains(f.Title, "thread tension") {
		t.Errorf("title must name the twin field, got %q", f.Title)
	}
	if !strings.Contains(f.Detail, "Low thread tension stich") {
		t.Errorf("detail must quote the note itself, got %q", f.Detail)
	}
	if !strings.Contains(f.Detail, "thread_tension") {
		t.Errorf("detail must name the empty column, got %q", f.Detail)
	}
}

func TestA5IsSilencedByTheTwinBeingFilled(t *testing.T) {
	c := card8()
	card8OpByNumber(c, 40).ThreadTension = text("lower")

	rtNone(t, rtFindings(c), "Instruction lives only in a note")
}

func TestA5IgnoresNotesThatNameNoField(t *testing.T) {
	// «front seam» (110/120) и «join pockets» (210/220) — ноты карточки 8, которые полем не
	// ловятся. Они едут в промпт фактами, а находкой не становятся никогда.
	c := card8()
	card8OpByNumber(c, 40).Note = text("front seam")

	rtNone(t, rtFindings(c), "Instruction lives only in a note")
}

func TestA5DoesNotReadCentimetresAsDegrees(t *testing.T) {
	// РЕГИСТР ВЫБРАН ПО АЛЬТЕРНАТИВАМ: `(?i)` на всём выражении температуры превратил бы «10 cm»
	// в градусы Цельсия и потребовал press_temperature_c у машинного шага. Ложная находка дороже
	// отсутствующей.
	c := card8()
	card8OpByNumber(c, 40).Note = text("topstitch 10 cm from the edge")

	rtNone(t, rtFindings(c), "Instruction lives only in a note")

	c2 := card8()
	card8OpByNumber(c2, 50).Note = text("press at 150 C")
	f := rtOne(t, rtFindings(c2), "note: a pressing temperature")
	rtWantRefs(t, f, RefOp(50))
}

func TestA5ReadsSteamAsThreeValued(t *testing.T) {
	// press_steam трёхзначен: Valid+false — это «БЕЗ ПАРА», настоящая инструкция, и она обязана
	// подавлять находку ровно как «с паром».
	c := card8()
	card8OpByNumber(c, 50).Note = text("press with steam")
	rtWantRefs(t, rtOne(t, rtFindings(c), "note: steam"), RefOp(50))

	c2 := card8()
	card8OpByNumber(c2, 50).Note = text("press with steam")
	card8OpByNumber(c2, 50).PressSteam = sql.NullBool{Bool: false, Valid: true}
	rtNone(t, rtFindings(c2), "note: steam")
}

// ── A6. ПОРЯДОК ФИНИШНЫХ ГЛАГОЛОВ ───────────────────────────────────────────────────────────────

func TestA6IsSilentOnCard8(t *testing.T) {
	// Подавитель полный: финишных глаголов на карточке ноль, `pack` в маршруте нет.
	rtNone(t, rtFindings(card8()), "after packing")
}

func TestA6WakesUpWhenPackingIsNotLast(t *testing.T) {
	c := card8()
	packNum := rtAppendOp(c, entity.TechCardOperation{OperationType: entity.OpTypePack, Zone: "other"})
	sewNum := rtAppendOp(c, entity.TechCardOperation{
		OperationType: entity.OpTypeMachine, Zone: "front", MachineType: text("lockstitch"),
	})

	f := rtOne(t, rtFindings(c), "Assembly or wet work after packing")
	if f.Severity != SeverityError || f.Category != CategorySequence {
		t.Errorf("want sequence/error, got %s/%s", f.Category, f.Severity)
	}
	rtWantRefs(t, f, RefOp(sewNum))
	if !strings.Contains(f.Detail, "Operation "+itoa32(packNum)) {
		t.Errorf("detail must name the packing step, got %q", f.Detail)
	}
}

func TestA6FoldAfterPackIsOnlyAWarning(t *testing.T) {
	c := card8()
	rtAppendOp(c, entity.TechCardOperation{OperationType: entity.OpTypePack, Zone: "other"})
	rtAppendOp(c, entity.TechCardOperation{OperationType: entity.OpTypeFold, Zone: "other"})

	f := rtOne(t, rtFindings(c), "Folding after packing")
	if f.Severity != SeverityWarning {
		t.Errorf("folding a packed garment is a warning, not an error: %s", f.Severity)
	}
	rtNone(t, rtFindings(c), "Assembly or wet work after packing")
}

func TestA6IsSilentWhenPackingIsLast(t *testing.T) {
	c := card8()
	rtAppendOp(c, entity.TechCardOperation{OperationType: entity.OpTypeInspect, Zone: "other",
		CoverageMode: text("each_unit")})
	rtAppendOp(c, entity.TechCardOperation{OperationType: entity.OpTypePack, Zone: "other"})

	rtNone(t, rtFindings(c), "after packing")
}

// ── A7. FUSING ПО СОБРАННЫМ УЗЛАМ ───────────────────────────────────────────────────────────────

func TestA7IsSilentOnCard8(t *testing.T) {
	rtNone(t, rtFindings(card8()), "Fusing applied to an assembled unit")
}

func TestA7FiresOnFusingWithAUnitInput(t *testing.T) {
	c := card8()
	num := rtAppendOp(c, entity.TechCardOperation{
		OperationType:     entity.OpTypeFusing,
		Zone:              "front",
		PressTemperatureC: sql.NullInt32{Int32: 140, Valid: true}, // A3 приглушена: мерим A7
		AssemblyInputs: []entity.OperationInput{
			rtPieceInput(c, "SHLD_L"),
			rtUnitInput("blazer"),
		},
	})

	f := rtOne(t, rtFindings(c), "Fusing applied to an assembled unit")
	if f.Severity != SeverityError || f.Category != CategorySequence {
		t.Errorf("want sequence/error, got %s/%s", f.Category, f.Severity)
	}
	rtWantRefs(t, f, RefOp(num), RefUnit("blazer"))
	if !strings.Contains(f.Detail, "flat pieces before the first seam") {
		t.Errorf("detail must say WHY, got %q", f.Detail)
	}
}

func TestA7IsSuppressedWhenEveryInputIsAPiece(t *testing.T) {
	c := card8()
	rtAppendOp(c, entity.TechCardOperation{
		OperationType:     entity.OpTypeFusing,
		Zone:              "front",
		PressTemperatureC: sql.NullInt32{Int32: 140, Valid: true},
		AssemblyInputs: []entity.OperationInput{
			rtPieceInput(c, "SHLD_L"),
			rtPieceInput(c, "SHLD_R"),
		},
	})

	rtNone(t, rtFindings(c), "Fusing applied to an assembled unit")
}

// ── A8. WORK ↔ MACHINE ──────────────────────────────────────────────────────────────────────────

// rtCard8Works mirrors the three catalog rows card 8 actually uses (migration 0329 seed):
// press_open — machine_mode 'none'; buttonhole and button_attach — 'fixed', one machine each.
var rtCard8Works = []entity.OperationWork{
	{Token: "press_open", Verb: "press_open", Stage: "pressing", Label: "Press open",
		MachineMode: entity.OperationWorkMachineModeNone},
	{Token: "buttonhole", Verb: "machine", Stage: "closures", Label: "Buttonhole",
		MachineMode: entity.OperationWorkMachineModeFixed, DefaultMachine: text("buttonhole"),
		Machines: []string{"buttonhole"}},
	{Token: "button_attach", Verb: "machine", Stage: "closures", Label: "Button attach",
		MachineMode: entity.OperationWorkMachineModeFixed, DefaultMachine: text("button_attach"),
		Machines: []string{"button_attach"}},
}

// rtPublishWorks publishes the process-wide catalog snapshot for one test and takes it down after.
func rtPublishWorks(t *testing.T, works []entity.OperationWork) {
	t.Helper()
	entity.SetOperationWorkCatalog(works)
	t.Cleanup(func() { entity.SetOperationWorkCatalog(nil) })
}

func TestA8IsSilentOnTheFiveLegalPairsOfCard8(t *testing.T) {
	rtPublishWorks(t, rtCard8Works)

	res := RunAudit(card8(), rtFx)
	rtNone(t, res.Findings, "Work ")
	for _, line := range res.NotChecked {
		if strings.Contains(line, "work catalog") {
			t.Errorf("with a loaded catalog the check must not report itself unchecked: %q", line)
		}
	}
}

func TestA8FiresOnAMachineOutsideTheWorksSet(t *testing.T) {
	rtPublishWorks(t, rtCard8Works)

	c := card8()
	card8OpByNumber(c, 470).MachineType = text("zigzag") // работа buttonhole живёт только на петельной

	f := rtOne(t, rtFindings(c), "does not run on a zigzag")
	if f.Category != CategoryIntegrity || f.Severity != SeverityWarning {
		t.Errorf("want integrity/warning, got %s/%s", f.Category, f.Severity)
	}
	rtWantRefs(t, f, RefOp(470))
	if !strings.Contains(f.Detail, "buttonhole") {
		t.Errorf("detail must name the work's own machine list, got %q", f.Detail)
	}
}

func TestA8FiresOnAVerbThatIsNotTheWorksVerb(t *testing.T) {
	rtPublishWorks(t, rtCard8Works)

	c := card8()
	op := card8OpByNumber(c, 470)
	op.OperationType = entity.OpTypeHandwork
	op.MachineType = sql.NullString{}

	f := rtOne(t, rtFindings(c), `does not belong to a handwork step`)
	rtWantRefs(t, f, RefOp(470))
	if !strings.Contains(f.Detail, `"machine"`) {
		t.Errorf("detail must name the verb the catalog declares, got %q", f.Detail)
	}
}

func TestA8SaysOutLoudThatItCouldNotCheck(t *testing.T) {
	// Снимок каталога не опубликован — состояние КАЖДОГО процесса, который не звал store.New.
	// Молчание, неотличимое от чистоты, здесь запрещено: проверка обязана назвать себя
	// непроверенной.
	entity.SetOperationWorkCatalog(nil)

	res := RunAudit(card8(), rtFx)
	rtNone(t, res.Findings, "Work ")
	found := false
	for _, line := range res.NotChecked {
		if strings.Contains(line, "work catalog") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a not_checked line about the missing catalog, got %v", res.NotChecked)
	}
}

func TestA8SuppressorsAreImplemented(t *testing.T) {
	t.Run("no work assigned anywhere", func(t *testing.T) {
		rtPublishWorks(t, rtCard8Works)
		c := card8()
		for i := range c.Operations {
			c.Operations[i].Work = sql.NullString{}
		}
		res := RunAudit(c, rtFx)
		rtNone(t, res.Findings, "Work ")
		for _, line := range res.NotChecked {
			if strings.Contains(line, "work catalog") {
				t.Errorf("a card with no works needs no catalog: %q", line)
			}
		}
	})

	t.Run("machine mode none does not judge the machine", func(t *testing.T) {
		rtPublishWorks(t, rtCard8Works)
		c := card8()
		// press_open объявлен с machine_mode = none: ось «на чём» у этого глагола не машинная,
		// и названная машинка не делает пару незаконной.
		card8OpByNumber(c, 50).MachineType = text("zigzag")
		rtNone(t, rtFindings(c), "does not run on")
	})

	t.Run("a step that names no machine", func(t *testing.T) {
		rtPublishWorks(t, rtCard8Works)
		c := card8()
		card8OpByNumber(c, 470).MachineType = sql.NullString{}
		rtNone(t, rtFindings(c), "does not run on")
	})

	t.Run("a token the catalog does not know", func(t *testing.T) {
		rtPublishWorks(t, rtCard8Works)
		c := card8()
		card8OpByNumber(c, 470).Work = text("slit_overcast")
		res := RunAudit(c, rtFx)
		rtNone(t, res.Findings, "Work ")
		named := false
		for _, line := range res.NotChecked {
			if strings.Contains(line, "slit_overcast") {
				named = true
			}
		}
		if !named {
			t.Errorf("an unknown work token is «not checked», not «legal»: %v", res.NotChecked)
		}
	})
}

// ── A9. ЛЕГАСИ-ДРЕЙФ OP-PIECE ───────────────────────────────────────────────────────────────────

func TestA9IsSilentOnTheLockstepOfCard8(t *testing.T) {
	rtNone(t, rtFindings(card8()), "Legacy piece links diverge")
}

func TestA9FiresOnceForAnySetThatDrifted(t *testing.T) {
	c := card8()
	// Одна легаси-связь потеряна на 60, одна лишняя на 90 — ДВА разошедшихся шага.
	op60 := card8OpByNumber(c, 60)
	op60.PieceIds = op60.PieceIds[:1]
	op90 := card8OpByNumber(c, 90)
	op90.PieceIds = append(op90.PieceIds, card8PieceByName(c, "BP_L").Id)

	fs := rtFindings(c)
	f := rtOne(t, fs, "Legacy piece links diverge")
	if f.Category != CategoryIntegrity || f.Severity != SeverityWarning {
		t.Errorf("want integrity/warning, got %s/%s", f.Category, f.Severity)
	}
	// ОДНА агрегированная находка при ЛЮБОМ расхождении — не по одной на шаг.
	if !strings.Contains(f.Detail, "2 operation(s)") {
		t.Errorf("detail must count the drifted steps, got %q", f.Detail)
	}
	rtWantRefs(t, f, RefOp(60))
	if len(f.Evidence) != 2 {
		t.Errorf("want one evidence line per drifted step (capped at three), got %v", f.Evidence)
	}
	if !strings.Contains(strings.Join(f.Evidence, " "), "PCK_MAIN_R_1") {
		t.Errorf("evidence must name the piece that fell out, got %v", f.Evidence)
	}
}

func TestA9DoesNotAccuseACardThatWasNeverStored(t *testing.T) {
	// Карточка, разобранная с провода и ещё не сохранённая, несёт только line_key: id детали
	// проставляет стор. Прочитать это как «легаси-таблица пуста» значило бы обвинить карточку,
	// которой ещё не было в базе.
	c := card8()
	for i := range c.Operations {
		c.Operations[i].PieceIds = nil
	}

	rtNone(t, rtFindings(c), "Legacy piece links diverge")
}

// ── A10. WET-PROCESS ────────────────────────────────────────────────────────────────────────────

func TestA10IsSilentOnCard8(t *testing.T) {
	rtNone(t, rtFindings(card8()), "Sensitive component set before wet processing")
}

// rtFoilBeforeWash builds the A10 shape: a hardware/print step carrying a BOM line of `kind`,
// followed by a wet process.
func rtFoilBeforeWash(kind sql.NullString) *entity.TechCard {
	c := card8()
	line := rtAppendBom(c, "gold foil", entity.BomSectionDecoration, kind)
	num := rtAppendOp(c, entity.TechCardOperation{
		OperationType: entity.OpTypePrint, Zone: "front", PrintMethod: text("foil"),
	})
	rtLinkBom(card8OpByNumber(c, num), line)
	rtAppendOp(c, entity.TechCardOperation{
		OperationType: entity.OpTypeWetProcess, Zone: "other", WetProcessKind: text("garment_dye"),
	})
	return c
}

func TestA10FiresOnFoilSetBeforeAWash(t *testing.T) {
	c := rtFoilBeforeWash(text(string(entity.BomKindFoil)))

	f := rtOne(t, rtFindings(c), "Sensitive component set before wet processing")
	if f.Category != CategorySequence || f.Severity != SeverityWarning {
		t.Errorf("want sequence/warning, got %s/%s", f.Category, f.Severity)
	}
	rtWantRefs(t, f, RefOp(490), RefBom("gold foil"), RefOp(500))
	if !strings.Contains(f.Detail, "foil") {
		t.Errorf("detail must name the kind, got %q", f.Detail)
	}
}

func TestA10SuppressorsAreImplemented(t *testing.T) {
	t.Run("kind NULL", func(t *testing.T) {
		rtNone(t, rtFindings(rtFoilBeforeWash(sql.NullString{})),
			"Sensitive component set before wet processing")
	})

	t.Run("kind outside the sensitive set", func(t *testing.T) {
		rtNone(t, rtFindings(rtFoilBeforeWash(text(string(entity.BomKindPatch)))),
			"Sensitive component set before wet processing")
	})

	t.Run("no BOM link at all", func(t *testing.T) {
		c := card8()
		rtAppendOp(c, entity.TechCardOperation{
			OperationType: entity.OpTypePrint, Zone: "front", PrintMethod: text("foil"),
		})
		rtAppendOp(c, entity.TechCardOperation{
			OperationType: entity.OpTypeWetProcess, Zone: "other", WetProcessKind: text("rinse"),
		})
		rtNone(t, rtFindings(c), "Sensitive component set before wet processing")
	})

	t.Run("the wet process runs first", func(t *testing.T) {
		c := card8()
		line := rtAppendBom(c, "gold foil", entity.BomSectionDecoration, text(string(entity.BomKindFoil)))
		rtAppendOp(c, entity.TechCardOperation{
			OperationType: entity.OpTypeWetProcess, Zone: "other", WetProcessKind: text("rinse"),
		})
		num := rtAppendOp(c, entity.TechCardOperation{
			OperationType: entity.OpTypePrint, Zone: "front", PrintMethod: text("foil"),
		})
		rtLinkBom(card8OpByNumber(c, num), line)

		rtNone(t, rtFindings(c), "Sensitive component set before wet processing")
	})
}

// ── ОБЩИЕ СВОЙСТВА БЛОКА ────────────────────────────────────────────────────────────────────────

func TestRouteFindingsCarryAnchorsAndASource(t *testing.T) {
	// Находка без единого якоря дропается верификатором (§8 п.2); машинная находка без источника —
	// забытое поле.
	for _, f := range rtFindings(card8()) {
		if len(f.Refs) == 0 {
			t.Errorf("finding %q has no anchor at all", f.Title)
		}
		if f.Source != SourceMachine {
			t.Errorf("finding %q: want source %q, got %q", f.Title, SourceMachine, f.Source)
		}
		if len([]rune(f.Title)) > 90 {
			t.Errorf("finding title is longer than 90 runes: %q", f.Title)
		}
	}
}

func TestRouteFilesNoTopologicalFinding(t *testing.T) {
	// §1/§2: циклы, ссылки вперёд, дубли-производители и двойное потребление НЕПРЕДСТАВИМЫ в
	// сохранённой карточке — запись их не принимает. Поглощение 250/260 (тот же ключ на входе и
	// на выходе) — ЛЕГАЛЬНАЯ цепочка, и находка о нём была бы предложением «починить»
	// единственно верную разметку.
	for _, f := range rtFindings(card8()) {
		low := strings.ToLower(f.Title + " " + f.Detail)
		for _, forbidden := range []string{"duplicate producer", "cycle", "forward reference",
			"consumed twice", "double-consumed"} {
			if strings.Contains(low, forbidden) {
				t.Errorf("topological finding leaked in: %q mentions %q", f.Title, forbidden)
			}
		}
		if rtHasRef(f, RefOp(260)) {
			t.Errorf("op 260 is an ABSORBING step, not a defect: %q", f.Title)
		}
	}
}

func TestRouteIsDeterministicAcrossRuns(t *testing.T) {
	// Слой always-on: список, который тасуется на одних и тех же данных, читается как «что-то
	// изменилось» там, где не изменилось ничего.
	first := rtDump(rtFindings(card8()))
	for i := 0; i < 5; i++ {
		if got := rtDump(rtFindings(card8())); got != first {
			t.Fatalf("run %d differs:\nfirst:\n%s\ngot:\n%s", i, first, got)
		}
	}
}

func TestRouteHandlesAnEmptyCard(t *testing.T) {
	// Карточка без единого сборочного факта проходит вакуумно (§1) — проверки маршрута обязаны
	// молчать, а не паниковать на пустых картах.
	res := RunAudit(&entity.TechCard{}, rtFx)
	if len(res.Findings) != 0 {
		t.Errorf("want no route findings on an empty card, got:\n%s", rtDump(res.Findings))
	}
	if res2 := RunAudit(nil, rtFx); len(res2.Findings) != 0 {
		t.Errorf("want no findings on a nil card, got:\n%s", rtDump(res2.Findings))
	}
}
