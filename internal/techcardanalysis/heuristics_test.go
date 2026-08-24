package techcardanalysis

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── ПРИЁМКА ЭВРИСТИК §3.4 ───────────────────────────────────────────────────────────────────────
//
// ЭТИ ТЕСТЫ ФИКСИРУЮТ НЕ ТОЛЬКО ТО, ЧТО ПАРОВАТЕЛЬ РАБОТАЕТ, НО И ТО, ЧТО ОН ОШИБАЕТСЯ ИМЕННО ТАК,
// КАК ОПИСАНО. Эталон «неправильности» — часть контракта промпта: модели сказано «наблюдения МОГУТ
// БЫТЬ НЕВЕРНЫ, опровергай их, цитируя операции», и стенд приёмки Ф1 меряет, опровергает ли она
// пару 310↔320. Если однажды парователь «починится» и перестанет её выдавать, сломаться обязан
// тест, а не стенд.

func htObservations(c *entity.TechCard) []string { return RunAudit(c, rtFx).Observations }

// htLine returns the single observation containing substr, failing loudly otherwise.
func htLine(t *testing.T, obs []string, substr string) string {
	t.Helper()
	var hit []string
	for _, o := range obs {
		if strings.Contains(o, substr) {
			hit = append(hit, o)
		}
	}
	if len(hit) != 1 {
		t.Fatalf("want exactly one observation containing %q, got %d:\n  %s",
			substr, len(hit), strings.Join(obs, "\n  "))
	}
	return hit[0]
}

func htNoLine(t *testing.T, obs []string, substr string) {
	t.Helper()
	for _, o := range obs {
		if strings.Contains(o, substr) {
			t.Fatalf("want no observation containing %q, got %q", substr, o)
		}
	}
}

// htCard builds a minimal card out of bare operations, numbered the way the write path numbers them.
func htCard(ops ...entity.TechCardOperation) *entity.TechCard {
	c := &entity.TechCard{}
	for i := range ops {
		ops[i].OperationNumber = sql.NullInt32{Int32: int32((i + 1) * 10), Valid: true}
		ops[i].Note = text("")
	}
	c.Operations = ops
	return c
}

func htJoin(out string, inputs ...entity.OperationInput) entity.TechCardOperation {
	return entity.TechCardOperation{
		OperationType: entity.OpTypeMachine, Zone: "front", MachineType: text("lockstitch"),
		OutputUnitKey: text(out), OutputUnitName: text(out), AssemblyInputs: inputs,
	}
}

func htPiece(lineKey string) entity.OperationInput {
	return entity.OperationInput{Kind: entity.AssemblyInputPiece, Key: lineKey}
}

// ── ГЛАВНОЕ РЕШЕНИЕ: ЭВРИСТИКА — НАБЛЮДЕНИЕ, А НЕ НАХОДКА ───────────────────────────────────────

func TestHeuristicsFileNoFindingAtAll(t *testing.T) {
	res := RunAudit(card8(), rtFx)
	if len(res.Observations) == 0 {
		t.Fatal("the heuristics produced nothing at all — the block is the whole point")
	}
	for _, f := range res.Findings {
		if f.Confidence == ConfidenceHeuristic {
			t.Errorf("a heuristic became a filed finding: %q", f.Title)
		}
		// Парователь ошибается на этой самой карточке (310↔320). Пустить его вывод в секцию
		// CONSTRUCTION значило бы отправить технолога править верную разметку и заплатить за это
		// доверием ко ВСЕЙ секции.
		low := strings.ToLower(f.Title + " " + f.Detail)
		for _, word := range []string{"twin", "mirror", "suspected typo"} {
			if strings.Contains(low, word) {
				t.Errorf("heuristic language leaked into a finding: %q mentions %q", f.Title, word)
			}
		}
	}
}

// ── ЛЕКСИЧЕСКИЙ ПАРОВАТЕЛЬ ──────────────────────────────────────────────────────────────────────

func TestMirrorPairingOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "Lexical mirror pairing over unit names")

	// Пары, которые парователь обязан выдать на карточке 8. 60↔90 и 70↔100 — ПОРЯДКОВЫЙ зип
	// внутри одного имени: шитьё с шитьём, утюжка с утюжкой.
	for _, want := range []string{
		"60<->90", "70<->100", "80<->150", "110<->120", "170<->180",
		"190<->200", "210<->220", "310<->320", "330<->?", "360<->370", "380<->390", "420<->430",
	} {
		if !strings.Contains(line, want) {
			t.Errorf("want pair %s in:\n  %s", want, line)
		}
	}
	// Контракт «может быть неверно» едет в самой строке: блок собирает Ф1, но строка обязана
	// сказать, чем она является, и без шапки.
	if !strings.Contains(line, "NAMES only") || !strings.Contains(line, "ground truth") {
		t.Errorf("the pairing line must carry its own contract, got:\n  %s", line)
	}
}

func TestMirrorPairerReproducesItsDocumentedError(t *testing.T) {
	// §3.4 называет ошибку поимённо: лексика спаривает 310 «right lining» с 320 «left lining»,
	// которые близнецами НЕ ЯВЛЯЮТСЯ — настоящие пары 300↔310 и 320↔330 (золотая ошибка 2).
	// Тест фиксирует именно ошибку: на ней стоит стенд приёмки Ф1, где модель обязана её
	// опровергнуть, цитируя входы.
	line := htLine(t, htObservations(card8()), "Lexical mirror pairing over unit names")

	if !strings.Contains(line, "310<->320") {
		t.Errorf("the pairer must still make its documented mistake, got:\n  %s", line)
	}
	for _, real := range []string{"300<->310", "320<->330"} {
		if strings.Contains(line, real) {
			t.Errorf("lexical pairing cannot know the real pair %s — it reads names, not inputs:\n  %s",
				real, line)
		}
	}
	// Непарные левые называются вслух, а не молча выбрасываются: «300<->?» и есть след настоящей
	// пары, которую лексика не увидела.
	for _, lonely := range []string{"290<->?", "300<->?"} {
		if !strings.Contains(line, lonely) {
			t.Errorf("want the unpaired left %s in:\n  %s", lonely, line)
		}
	}
}

func TestMirrorMethodDiscrepancyOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "Method differs inside suspected twins")
	for _, want := range []string{"op 70 is press_open", "op 100 is press"} {
		if !strings.Contains(line, want) {
			t.Errorf("want %q in:\n  %s", want, line)
		}
	}
}

func TestMirrorSplitAlongTheRouteOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "split along the route")
	if !strings.Contains(line, "ops 80 and 150 are separated by ops 90-140") {
		t.Errorf("want the 80/150 split, got:\n  %s", line)
	}
	// 60/90 и 70/100 разведены на два шага СОБСТВЕННОЙ чересполосицей одного блока карманов —
	// назвать это разрывом значило бы утопить единственный настоящий.
	for _, noise := range []string{"ops 60 and 90", "ops 70 and 100"} {
		if strings.Contains(line, noise) {
			t.Errorf("the gap threshold must not report %q:\n  %s", noise, line)
		}
	}
}

func TestMirrorCapitalisationDiscrepancyOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "Capitalisation differs inside suspected twins")
	if !strings.Contains(line, `op 360 "left sleeve lining" vs op 370 "Right sleeve lining"`) {
		t.Errorf("want the 360/370 case mismatch, got:\n  %s", line)
	}
	// 170/180 («Right side…» / «Left side…») набраны ОДИНАКОВО — внутри пары разнобоя нет.
	if strings.Contains(line, "op 170") {
		t.Errorf("170/180 are capitalised the same way and must not be reported:\n  %s", line)
	}
}

func TestMirrorPairerIsSilentWithoutSideNames(t *testing.T) {
	c := htCard(
		htJoin("front", htPiece("k1"), htPiece("k2")),
		htJoin("back", htPiece("k3"), htPiece("k4")),
		htJoin("shell", entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: "front"},
			entity.OperationInput{Kind: entity.AssemblyInputUnit, Key: "back"}),
	)
	obs := htObservations(c)
	htNoLine(t, obs, "Lexical mirror pairing over unit names")
	htNoLine(t, obs, "Suspected typo")
}

func TestMirrorPairerSkipsAStepWithTwoUnitInputs(t *testing.T) {
	// У шага-обработки с двумя узлами на входе нет ОДНОГО имени, и выбрать из двух — уже не
	// лексика, а догадка о догадке.
	c := htCard(
		htJoin("left panel", htPiece("k1")),
		htJoin("right panel", htPiece("k2")),
		entity.TechCardOperation{
			OperationType: entity.OpTypePress, Zone: "front",
			AssemblyInputs: []entity.OperationInput{
				{Kind: entity.AssemblyInputUnit, Key: "left panel"},
				{Kind: entity.AssemblyInputUnit, Key: "right panel"},
			},
		},
	)
	line := htLine(t, htObservations(c), "Lexical mirror pairing over unit names")
	if !strings.Contains(line, "10<->20") {
		t.Errorf("want the producing pair 10<->20, got:\n  %s", line)
	}
	if strings.Contains(line, "30") {
		t.Errorf("the two-unit press step has no single name and must not be paired:\n  %s", line)
	}
}

// ── ДЕТАЛИ КРОЯ ─────────────────────────────────────────────────────────────────────────────────

func TestPieceMirrorSummaryOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "Lexical mirror pairing over cut-piece names")

	if !strings.Contains(line, "matched 18 _L/_R pairs") {
		t.Errorf("want 18 matched piece pairs, got:\n  %s", line)
	}
	// НАСТОЯЩИЙ сигнал здесь — не список совпавших, а тот, кто не совпал: у BP_LIN_L_2 и
	// BP_LIN_R_1 разные числовые хвосты, и по именам они друг другу не близнецы.
	for _, want := range []string{"BP_LIN_L_2", "BP_LIN_R_1"} {
		if !strings.Contains(line, want) {
			t.Errorf("want the unpaired piece %s in:\n  %s", want, line)
		}
	}
	if strings.Contains(line, "SL_INS_L,") || strings.Contains(line, "SHLD_L,") {
		t.Errorf("a matched piece must not be listed as lonely:\n  %s", line)
	}
}

// ── ПОДОЗРЕНИЯ НА ОПЕЧАТКУ ──────────────────────────────────────────────────────────────────────

func TestTypoIrregularCapitalisationOnCard8(t *testing.T) {
	line := htLine(t, htObservations(card8()), "irregular capitalisation")

	if !strings.Contains(line, `"LEft"`) || !strings.Contains(line, `"LEft front panel with pockets"`) {
		t.Errorf("want the word and the key it sits in, got:\n  %s", line)
	}
	if !strings.Contains(line, "produced by op 200") || !strings.Contains(line, "consumed by ops 220, 270") {
		t.Errorf("want the anchors that let a human jump there, got:\n  %s", line)
	}
}

func TestTypoIgnoresRegularCapitalisation(t *testing.T) {
	// «Back Panels Upper» — Title case во всех словах, «PCK» — аббревиатура: ни то, ни другое не
	// опечатка, и «подозрение» на них было бы шумом на каждой карточке.
	obs := htObservations(card8())
	for _, ok := range []string{"Back Panels Upper", "Collar inner", "Pocket detail inside"} {
		for _, o := range obs {
			if strings.Contains(o, "irregular capitalisation") && strings.Contains(o, ok) {
				t.Errorf("%q is regular capitalisation: %q", ok, o)
			}
		}
	}
}

func TestTypoLevenshteinIsWrongOnCard8AndSaysSo(t *testing.T) {
	// ⚠️ ЗАФИКСИРОВАННАЯ ЛОЖНАЯ ДОГАДКА. «lining back» (340) и «lining base» (350) отстоят на две
	// правки и при этом являются двумя совершенно разными, законными узлами. Наблюдение —
	// единственная форма, в которой такую догадку можно показать, не соврав: находкой она была бы
	// требованием «починить» верное.
	obs := htObservations(card8())
	line := htLine(t, obs, `"lining back"`)
	if !strings.Contains(line, "Suspected typo") || !strings.Contains(line, `"lining base"`) {
		t.Errorf("want the (wrong) typo suspicion over lining back / lining base, got:\n  %s", line)
	}
	if !strings.Contains(line, "op 340") || !strings.Contains(line, "op 350") {
		t.Errorf("want both anchors, got:\n  %s", line)
	}

	// А вот пара, различающаяся ТОЛЬКО регистром, отсюда исключена: её уже подала A1 находкой, и
	// промпт прямо запрещает пересказывать поданное.
	for _, o := range obs {
		if strings.Contains(o, "Suspected typo") && strings.Contains(o, `"Base"`) &&
			strings.Contains(o, `"base"`) {
			t.Errorf("A1 already filed Base/base as a finding; the heuristic must not repeat it: %q", o)
		}
	}
}

func TestLevenshteinAtMostStopsAtTheThreshold(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		max  int
		want int
		ok   bool
	}{
		{"lining back", "lining base", 2, 2, true},
		{"Base", "base", 2, 1, true},
		{"left lining", "right lining", 2, 0, false}, // 4 правки
		{"lining", "lining base", 2, 0, false},       // длина отсекает без матрицы
		{"blazer", "blazer", 2, 0, true},             // ноль — тоже ответ
		{"pocket base", "pocket vase", 1, 1, true},
	} {
		got, ok := levenshteinAtMost(tc.a, tc.b, tc.max)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Errorf("levenshteinAtMost(%q, %q, %d) = %d, %v; want %d, %v",
				tc.a, tc.b, tc.max, got, ok, tc.want, tc.ok)
		}
	}
}

func TestCaseShapeClassifies(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"base", caseShapeLower},
		{"PCK", caseShapeUpper},
		{"Base", caseShapeTitle},
		{"LEft", caseShapeMixed},
		{"McQ", caseShapeMixed},
		{"2", caseShapeNone},
	} {
		if got := caseShape(tc.in); got != tc.want {
			t.Errorf("caseShape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitSideNeedsExactlyOneSideToken(t *testing.T) {
	for _, tc := range []struct {
		in       string
		wantSide string
		wantNorm string
		wantOK   bool
	}{
		{"left lining", "left", "lining", true},
		{"SL_OUT_R", "right", "sl out", true},
		{"underlining left", "left", "underlining", true},
		{"BP_LIN_L_2", "left", "bp lin 2", true},
		{"blazer", "", "", false},
		{"PCK_LOCKER", "", "", false}, // «LOCKER» — не сторона, а слово
		{"left to right", "", "", false},
	} {
		side, _, norm, ok := splitSide(tc.in)
		if ok != tc.wantOK || side != tc.wantSide || (ok && norm != tc.wantNorm) {
			t.Errorf("splitSide(%q) = %q, %q, %v; want %q, %q, %v",
				tc.in, side, norm, ok, tc.wantSide, tc.wantNorm, tc.wantOK)
		}
	}
}
