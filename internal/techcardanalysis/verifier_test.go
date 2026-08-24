package techcardanalysis

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

// ── ВЕРИФИКАТОР §8: ТЕСТЫ ───────────────────────────────────────────────────────────────────────
//
// Проверки идут по фикстуре карточки 8, а не по синтетической карточке из трёх операций, ровно по
// одной причине: коллизия узлов «Base» (оп 270) и «base» (оп 450) — НАСТОЯЩАЯ, и case-insensitive
// разрешение «BASE» обязано на ней сломаться. Синтетическая карточка позволила бы написать эту
// коллизию удобной, а удобная коллизия проверяет реализацию против самой себя.

// verifierCard builds the card view every case reads.
func verifierCard(t *testing.T) *cardView {
	t.Helper()
	return newCardView(card8(), Fx{Base: "EUR"})
}

// rawFinding is the JSON shape §7.1 asks the model for. Тесты строят ответ ИМЕННО через json.Marshal
// этой структуры, а не склейкой строк: экранирование кавычек в «detail» руками — источник тестов,
// которые падают из-за собственной опечатки и чинятся правкой ожидания.
type rawFinding struct {
	Category    string   `json:"category"`
	Severity    string   `json:"severity"`
	Title       string   `json:"title"`
	Detail      string   `json:"detail"`
	Evidence    []string `json:"evidence"`
	Refs        []string `json:"refs"`
	InsertAfter string   `json:"insert_after"`
	Suggestion  string   `json:"suggestion"`
	Confidence  string   `json:"confidence"`
}

// okFinding is a well-formed finding with one live anchor; cases mutate the field they are about.
func okFinding(refs ...string) rawFinding {
	return rawFinding{
		Category: CategoryMethod, Severity: SeverityWarning,
		Title:  "Method question on the shell join",
		Detail: "The step joins shell and lining in one pass.",
		// Evidence НАМЕРЕННО цитирует то, чего в контексте нет: §8 п.3 — evidence не
		// верифицируется и не является основанием дропа. Если однажды станет — упадёт всё.
		Evidence:   []string{"op 9999 | consumes: nothing that exists"},
		Refs:       refs,
		Confidence: ConfidenceLikely,
	}
}

// rawResponse renders a model answer.
func rawResponse(t *testing.T, findings []rawFinding, notChecked []string, summary string) string {
	t.Helper()
	body := map[string]any{"findings": findings, "not_checked": notChecked, "summary": summary}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal fixture response: %v", err)
	}
	return string(b)
}

// oneFinding is the common shape: one finding, nothing else.
func oneFinding(t *testing.T, f rawFinding) string {
	t.Helper()
	return rawResponse(t, []rawFinding{f}, nil, "one finding")
}

// ── §8 п.2: ГРАММАТИКА REFS И ТАБЛИЦА КОЭРЦИЙ ──────────────────────────────────────────────────

func TestVerifyModelOutputRefCoercionTable(t *testing.T) {
	// Каждый кейс подаёт ОДНУ спорную ссылку рядом с живым якорем `card`, поэтому находка выживает
	// всегда, и тест меряет судьбу ССЫЛКИ, а не находки. Разделение дроп-ссылки / дроп-находки
	// проверяется отдельными тестами ниже.
	cases := []struct {
		name string
		ref  string
		want string // "" — ссылка дропнута
	}{
		{"canonical op", "op:460", "op:460"},
		{"op with a space", "op 460", "op:460"},
		{"operation spelled out", "operation:460", "op:460"},
		{"operation spelled out with a space", "operation 460", "op:460"},
		{"plural sigil", "operations:460", "op:460"},
		{"hash the way a human writes it", "op #460", "op:460"},
		{"bare operation number", "460", "op:460"},
		{"bare number that is not an operation", "465", ""},
		{"op that is not on the card", "op:9999", ""},
		{"op sigil with a non-number", "op:blazer", ""},

		{"canonical unit", "unit:blazer", "unit:blazer"},
		{"space after the unit sigil", "unit: blazer", "unit:blazer"},
		{"unit key that contains spaces", "unit:left lining with pocket", "unit:left lining with pocket"},
		{"unit resolved case-insensitively", "unit:BLAZER", "unit:blazer"},
		// Обе половины коллизии разрешаются БАЙТОВО и обязаны остаться разными.
		{"byte-exact upper half of the collision", "unit:Base", "unit:Base"},
		{"byte-exact lower half of the collision", "unit:base", "unit:base"},
		{"unit that is not on the card", "unit:nonesuch", ""},

		{"canonical piece", "piece:SL_INS_L", "piece:SL_INS_L"},
		{"piece resolved case-insensitively", "piece:sl_ins_l", "piece:SL_INS_L"},
		{"piece that is not on the card", "piece:NOPE", ""},

		{"canonical bom line", "bom:Плечевая", "bom:Плечевая"},
		{"bom line resolved case-insensitively", "bom:плечевая", "bom:Плечевая"},
		{"bom line that is not on the card", "bom:nonesuch", ""},

		{"card", "card", "card"},
		{"card in caps", "CARD", "card"},

		{"a bare word is not a ref", "blazer", ""},
		{"an unknown sigil is not a ref", "zone:sleeve", ""},
		{"empty", "   ", ""},
	}

	card := verifierCard(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, stats, err := VerifyModelOutput(
				oneFinding(t, okFinding(tc.ref, RefCard)), card, nil)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding count = %d, want 1 (the `card` anchor keeps it alive); stats %+v", len(got), stats)
			}
			want := []string{RefCard}
			if tc.want != "" && tc.want != RefCard {
				want = []string{tc.want, RefCard}
			}
			if !equalStrings(got[0].Refs, want) {
				t.Fatalf("refs for %q = %v, want %v", tc.ref, got[0].Refs, want)
			}
		})
	}
}

// TestVerifyModelOutputCaseInsensitiveRefIsNotSilent pins §8 п.2's «коэрция С ЛОГОМ»: a key resolved
// by folding case, and a key dropped for ambiguity, both leave a record — otherwise the anchor a
// human needed disappears with nothing to read about it.
func TestVerifyModelOutputCaseInsensitiveRefIsNotSilent(t *testing.T) {
	card := verifierCard(t)

	_, _, _, stats, err := VerifyModelOutput(oneFinding(t, okFinding("unit:BLAZER", RefCard)), card, nil)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if !containsSubstring(stats.Coercions, `"unit:BLAZER"`) ||
		!containsSubstring(stats.Coercions, "case-insensitively") {
		t.Fatalf("case-insensitive coercion was silent; coercions = %#v", stats.Coercions)
	}
}

// TestVerifyModelOutputBaseCollisionDropsTheRef is the case the plan names outright: card 8 carries
// units «Base» (op 270) AND «base» (op 450), so a model ref of "BASE" folds onto TWO keys, is
// therefore not unique, and must be DROPPED — anchoring it on either half would erase the very
// finding (A1) that the collision exists to raise.
func TestVerifyModelOutputBaseCollisionDropsTheRef(t *testing.T) {
	card := verifierCard(t)

	// Сначала — доказательство, что коллизия в фикстуре ЕСТЬ. Без него тест зелен и на карточке,
	// где «Base» просто нет: дроп по причине «ключа не существует» неотличим от дропа по
	// неуникальности, и проверка выродилась бы в сторожа у мёртвой ветки.
	if _, ok := card.gt.Units["Base"]; !ok {
		t.Fatal("fixture lost unit \"Base\": the ambiguity this test is about cannot arise")
	}
	if _, ok := card.gt.Units["base"]; !ok {
		t.Fatal("fixture lost unit \"base\": the ambiguity this test is about cannot arise")
	}

	got, _, _, stats, err := VerifyModelOutput(
		oneFinding(t, okFinding("unit:BASE", RefCard)), card, nil)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("finding count = %d, want 1: the `card` anchor survives", len(got))
	}
	if !equalStrings(got[0].Refs, []string{RefCard}) {
		t.Fatalf("refs = %v, want only [card]: \"BASE\" matches both \"Base\" and \"base\" and must be dropped",
			got[0].Refs)
	}
	if stats.DroppedBadRef != 0 {
		t.Fatalf("DroppedBadRef = %d, want 0: a dropped REF is not a dropped FINDING", stats.DroppedBadRef)
	}
	if !containsSubstring(stats.Coercions, "not resolvable") ||
		!containsSubstring(stats.Coercions, `"Base"`) || !containsSubstring(stats.Coercions, `"base"`) {
		t.Fatalf("the ambiguous ref was dropped silently; coercions = %#v", stats.Coercions)
	}
}

// ── §8 п.2: ДРОП ССЫЛКИ vs ДРОП НАХОДКИ ────────────────────────────────────────────────────────

func TestVerifyModelOutputDropRefVersusDropFinding(t *testing.T) {
	card := verifierCard(t)

	t.Run("one live ref out of four keeps the finding", func(t *testing.T) {
		got, _, _, stats, err := VerifyModelOutput(oneFinding(t, okFinding(
			"op:9999", "unit:nonesuch", "piece:NOPE", "op:460")), card, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("finding count = %d, want 1: one resolving ref is enough", len(got))
		}
		if !equalStrings(got[0].Refs, []string{"op:460"}) {
			t.Fatalf("refs = %v, want [op:460]", got[0].Refs)
		}
		if stats.DroppedBadRef != 0 {
			t.Fatalf("DroppedBadRef = %d, want 0", stats.DroppedBadRef)
		}
	})

	t.Run("no ref resolves drops the finding and counts it", func(t *testing.T) {
		got, _, _, stats, err := VerifyModelOutput(oneFinding(t, okFinding("op:9999")), card, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("finding count = %d, want 0", len(got))
		}
		if stats.DroppedBadRef != 1 || stats.DroppedContradiction != 0 {
			t.Fatalf("counters = %d bad ref / %d contradiction, want 1 / 0",
				stats.DroppedBadRef, stats.DroppedContradiction)
		}
		if len(stats.Drops) != 1 || stats.Drops[0].Reason != DropBadRef {
			t.Fatalf("drop records = %#v, want one %s record", stats.Drops, DropBadRef)
		}
		if stats.Drops[0].Title != "Method question on the shell join" {
			t.Fatalf("drop record lost the model's own text: %q", stats.Drops[0].Title)
		}
	})

	t.Run("an empty ref list drops the finding", func(t *testing.T) {
		got, _, _, stats, err := VerifyModelOutput(oneFinding(t, okFinding()), card, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 0 || stats.DroppedBadRef != 1 {
			t.Fatalf("findings = %d, DroppedBadRef = %d, want 0 / 1", len(got), stats.DroppedBadRef)
		}
	})
}

// ── §8 п.1: КОЭРЦИЯ ENUM'ОВ ────────────────────────────────────────────────────────────────────

func TestVerifyModelOutputEnumCoercion(t *testing.T) {
	cases := []struct {
		name                                       string
		category, severity, confidence             string
		wantCategory, wantSeverity, wantConfidence string
	}{
		{"valid values pass through", CategoryCoarseStep, SeverityBlocker, ConfidenceCertain,
			CategoryCoarseStep, SeverityBlocker, ConfidenceCertain},
		{"case drifts back", "Coarse_Step", "BLOCKER", "Certain",
			CategoryCoarseStep, SeverityBlocker, ConfidenceCertain},
		{"unknown category becomes question", "topology", SeverityError, ConfidenceCertain,
			CategoryQuestion, SeverityError, ConfidenceCertain},
		// Машинная категория от МОДЕЛИ — не её работа (§8 п.1, ValidModelCategories).
		{"machine category is not the model's to file", CategoryReadiness, SeverityWarning, ConfidenceLikely,
			CategoryQuestion, SeverityWarning, ConfidenceLikely},
		{"empty category becomes question", "", SeverityWarning, ConfidenceLikely,
			CategoryQuestion, SeverityWarning, ConfidenceLikely},
		{"unknown severity becomes warning", CategoryMethod, "critical", ConfidenceLikely,
			CategoryMethod, SeverityWarning, ConfidenceLikely},
		{"empty severity becomes warning", CategoryMethod, "", ConfidenceLikely,
			CategoryMethod, SeverityWarning, ConfidenceLikely},
		{"unknown confidence becomes likely", CategoryMethod, SeverityWarning, "high",
			CategoryMethod, SeverityWarning, ConfidenceLikely},
		{"empty confidence becomes likely", CategoryMethod, SeverityWarning, "",
			CategoryMethod, SeverityWarning, ConfidenceLikely},
		// §8 п.1 дословно: question в severity — это КАТЕГОРИЯ, поставленная не в то поле.
		{"question in severity moves to category", CategoryMethod, CategoryQuestion, ConfidenceCertain,
			CategoryQuestion, SeverityWarning, ConfidenceCertain},
		{"question in severity, whatever the category said", "", "Question", "",
			CategoryQuestion, SeverityWarning, ConfidenceLikely},
	}

	card := verifierCard(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := okFinding("op:460")
			f.Category, f.Severity, f.Confidence = tc.category, tc.severity, tc.confidence
			got, _, _, _, err := VerifyModelOutput(oneFinding(t, f), card, nil)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding count = %d, want 1: a bad enum is a defect of FORM, never a drop", len(got))
			}
			if got[0].Category != tc.wantCategory {
				t.Errorf("category = %q, want %q", got[0].Category, tc.wantCategory)
			}
			if got[0].Severity != tc.wantSeverity {
				t.Errorf("severity = %q, want %q", got[0].Severity, tc.wantSeverity)
			}
			if got[0].Confidence != tc.wantConfidence {
				t.Errorf("confidence = %q, want %q", got[0].Confidence, tc.wantConfidence)
			}
			if got[0].Source != SourceModel {
				t.Errorf("source = %q, want %q", got[0].Source, SourceModel)
			}
		})
	}
}

// ── §8 п.8: РАЗБОР — ФЕНСЫ, ПРОЗА, БИТЫЙ JSON ──────────────────────────────────────────────────

func TestVerifyModelOutputJSONExtraction(t *testing.T) {
	body := `{"findings":[{"category":"method","severity":"warning","title":"T","detail":"D",` +
		`"evidence":["e"],"refs":["op:460"],"insert_after":"","suggestion":"S","confidence":"likely"}],` +
		`"not_checked":["sketch"],"summary":"one"}`

	cases := []struct {
		name        string
		raw         string
		wantErr     bool
		wantCount   int
		wantSummary string
	}{
		{"bare object", body, false, 1, "one"},
		{"json fence", "```json\n" + body + "\n```", false, 1, "one"},
		{"bare fence", "```\n" + body + "\n```", false, 1, "one"},
		{"prose before and after", "Here is my review:\n" + body + "\nHope this helps.", false, 1, "one"},
		{"prose around a fence", "Sure!\n```json\n" + body + "\n```\nLet me know.", false, 1, "one"},
		// Единственный случай, в котором СНЯТИЕ ФЕНСА несёт нагрузку, а не дублирует поиск скобок:
		// хвостовая проза с фигурной скобкой. Без снятия фенса LastIndexByte('}') уедет в «{anything}»
		// и JSON оборвётся на полуслове.
		{"fence with braces in the trailing prose",
			"```json\n" + body + "\n```\nTell me if {anything} is unclear.", false, 1, "one"},
		{"an empty findings list is a complete answer",
			`{"findings":[],"summary":"nothing to report"}`, false, 0, "nothing to report"},

		{"broken json", `{"findings":[{"category":`, true, 0, ""},
		{"no object at all", "I could not review this card.", true, 0, ""},
		{"empty output", "   ", true, 0, ""},
		// Объект без ключа findings — ответ НЕ ПО СХЕМЕ. Принять его за «модель ничего не нашла»
		// значило бы нарисовать человеку чистую карточку по ответу, которого он не получал.
		{"object without a findings key", `{"summary":"all good"}`, true, 0, ""},
		{"findings is null", `{"findings":null,"summary":"x"}`, true, 0, ""},
	}

	card := verifierCard(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, summary, stats, err := VerifyModelOutput(tc.raw, card, nil)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v, want ErrInvalidOutput", err)
				}
				if !stats.InvalidOutput {
					t.Fatal("stats.InvalidOutput = false on a rejected run")
				}
				if len(got) != 0 {
					t.Fatalf("findings = %d on a rejected run, want 0: the model half is discarded whole", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("finding count = %d, want %d", len(got), tc.wantCount)
			}
			if summary != tc.wantSummary {
				t.Fatalf("summary = %q, want %q", summary, tc.wantSummary)
			}
		})
	}
}

// TestVerifyModelOutputTolerantFieldShapes covers the two list shapes a model writes where the
// schema says «array of strings». Ронять из-за них ВЕСЬ прогон значило бы применить к дефекту формы
// наказание, придуманное для лжи о карточке.
func TestVerifyModelOutputTolerantFieldShapes(t *testing.T) {
	card := verifierCard(t)
	cases := []struct {
		name string
		refs string
		want []string
	}{
		{"refs as a bare string", `"op:460"`, []string{"op:460"}},
		{"refs as an array with a number in it", `[460, "card"]`, []string{"op:460", RefCard}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{"findings":[{"category":"method","severity":"warning","title":"T","detail":"D",` +
				`"refs":` + tc.refs + `,"confidence":"likely"}],"summary":"s"}`
			got, _, _, _, err := VerifyModelOutput(raw, card, nil)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding count = %d, want 1", len(got))
			}
			if !equalStrings(got[0].Refs, tc.want) {
				t.Fatalf("refs = %v, want %v", got[0].Refs, tc.want)
			}
		})
	}
}

// ── §8 п.5: ПОРОГ СМЕРТИ max(4, 30%) ───────────────────────────────────────────────────────────

func TestVerifyModelOutputDeathThreshold(t *testing.T) {
	// Обе стороны порога, и с обеих сторон каждого слагаемого: пол ловит лаконичный прогон, доля —
	// длинный. Реализация, потерявшая любое из двух, обязана здесь покраснеть.
	cases := []struct {
		name        string
		total, dead int
		wantInvalid bool
	}{
		{"floor holds: 4 of 6 dropped is not death", 6, 4, false},
		{"floor crossed: 5 of 6 dropped is death", 6, 5, true},
		{"share holds exactly: 6 of 20 is 30%, not over it", 20, 6, false},
		{"share crossed: 7 of 20 is over 30%", 20, 7, true},
		{"share governs the long run: 5 of 15 is over 30%", 15, 5, true},
		{"share holds on the long run: 4 of 15 is under both", 15, 4, false},
		{"nothing dropped is never death", 15, 0, false},
		{"every finding dropped on a long run is death", 20, 20, true},
	}

	card := verifierCard(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := make([]rawFinding, 0, tc.total)
			for i := 0; i < tc.total; i++ {
				f := okFinding("op:460")
				if i < tc.dead {
					f = okFinding("op:9999") // не разрешится ни одна ссылка
				}
				f.Title = fmt.Sprintf("finding %d", i)
				findings = append(findings, f)
			}
			got, _, _, stats, err := VerifyModelOutput(rawResponse(t, findings, nil, "s"), card, nil)

			if stats.Emitted != tc.total {
				t.Fatalf("Emitted = %d, want %d: the denominator is what the MODEL emitted", stats.Emitted, tc.total)
			}
			if stats.DroppedBadRef != tc.dead {
				t.Fatalf("DroppedBadRef = %d, want %d", stats.DroppedBadRef, tc.dead)
			}
			if tc.wantInvalid {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v, want ErrInvalidOutput (%d of %d dropped)", err, tc.dead, tc.total)
				}
				if len(got) != 0 {
					t.Fatalf("findings = %d, want 0: §8 п.5 discards the model half whole", len(got))
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil (%d of %d dropped is under the threshold)", err, tc.dead, tc.total)
			}
			if want := minInt(tc.total-tc.dead, MaxModelFindings); len(got) != want {
				t.Fatalf("findings = %d, want %d", len(got), want)
			}
		})
	}
}

// ── §8 п.4: ПРОТИВОРЕЧИЯ И ДЕДУП ПО ПЕРЕСЕЧЕНИЮ ЯКОРЕЙ ─────────────────────────────────────────

func TestVerifyModelOutputDedupByAnchorIntersection(t *testing.T) {
	card := verifierCard(t)
	// Машинная пара 470/480 — реальная находка A2 карточки 8.
	machine := []Finding{{
		Source: SourceMachine, Category: CategoryNaming, Severity: SeverityWarning,
		Title: "Buttonholes and buttons are separate operations",
		Refs:  []string{"op:470", "op:480"},
	}}

	cases := []struct {
		name        string
		modelRefs   []string
		modelTitle  string
		wantDropped bool
	}{
		// «Машинная пара 70/100 и модельный method-вердикт о ней — один факт» (§8 п.4): заголовки
		// разные, якорь общий — дубль.
		{"shared anchor, different words", []string{"op:480", "unit:blazer"},
			"Button attaching should use the same machine as the buttonholes", true},
		{"shared anchor is enough even when it is the only one", []string{"op:470"},
			"Something else entirely", true},
		{"disjoint anchors survive", []string{"op:460", "unit:blazer"},
			"Buttonholes and buttons are separate operations", false},
		// Дедуп НЕ по равенству заголовков: тот же текст на другом якоре — другая находка.
		{"identical title, disjoint anchors", []string{"op:110"},
			"Buttonholes and buttons are separate operations", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := okFinding(tc.modelRefs...)
			f.Title = tc.modelTitle
			got, _, _, stats, err := VerifyModelOutput(oneFinding(t, f), card, machine)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if tc.wantDropped {
				if len(got) != 0 || stats.DroppedContradiction != 1 {
					t.Fatalf("findings = %d, DroppedContradiction = %d, want 0 / 1",
						len(got), stats.DroppedContradiction)
				}
				if stats.DroppedBadRef != 0 {
					t.Fatalf("DroppedBadRef = %d, want 0: a duplicate is not a bad ref", stats.DroppedBadRef)
				}
				if len(stats.Drops) != 1 || stats.Drops[0].Reason != DropContradiction {
					t.Fatalf("drop records = %#v, want one %s record", stats.Drops, DropContradiction)
				}
				return
			}
			if len(got) != 1 || stats.DroppedContradiction != 0 {
				t.Fatalf("findings = %d, DroppedContradiction = %d, want 1 / 0",
					len(got), stats.DroppedContradiction)
			}
		})
	}
}

// TestVerifyModelOutputCardAnchorIsNotADuplicate pins the one exclusion the dedup rule needs to stay
// usable: `card` means «эта находка не про конкретный шаг», which is the ABSENCE of an address, not
// an address. The machine layer stamps it on the collapsed readiness finding of every draft (§3.0),
// so counting it would make every whole-card model finding a duplicate — including the fusing
// blocker §14 requires from EVERY acceptance run, anchored on exactly `card`.
func TestVerifyModelOutputCardAnchorIsNotADuplicate(t *testing.T) {
	card := verifierCard(t)
	machine := []Finding{{
		Source: SourceMachine, Category: CategoryReadiness, Severity: SeverityBlocker,
		Title: collapsedReadinessTitle, Refs: []string{RefCard},
	}}

	f := okFinding(RefCard, "bom:Плечевая")
	f.Category, f.Severity = CategoryMissingStep, SeverityBlocker
	f.Title = "No fusing block anywhere in the route"

	got, _, _, stats, err := VerifyModelOutput(oneFinding(t, f), card, machine)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1: `card` is not a discriminating anchor; dropped = %#v",
			len(got), stats.Drops)
	}
}

func TestVerifyModelOutputTopologicalContradiction(t *testing.T) {
	card := verifierCard(t)

	t.Run("a claim the write path makes unrepresentable is dropped", func(t *testing.T) {
		f := okFinding("op:460")
		f.Detail = "Operation 460 creates a circular dependency: the unit it produces is already " +
			"consumed by an earlier step."
		got, _, _, stats, err := VerifyModelOutput(oneFinding(t, f), card, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 0 || stats.DroppedContradiction != 1 {
			t.Fatalf("findings = %d, DroppedContradiction = %d, want 0 / 1",
				len(got), stats.DroppedContradiction)
		}
	})

	t.Run("an ordinary word that merely contains a topology term is not a claim", func(t *testing.T) {
		f := okFinding("op:460")
		f.Detail = "Loop the thread twice before closing the seam; the sleeve head is not cycled."
		got, _, _, _, err := VerifyModelOutput(oneFinding(t, f), card, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 1 {
			t.Fatal("a sewing instruction was mistaken for a topological claim")
		}
	})

	t.Run("when the recomputation itself found violations, nothing is contradicted", func(t *testing.T) {
		// Карточка, записанная МИМО конвертера: имя узла есть, ключа нет — гигиена AssemblySweep.
		// В этот момент VERIFIED FACTS чистоту НЕ утверждают, и модель, заметившая беду, скорее
		// права; молча стереть её находку было бы худшим из исходов.
		mutated := card8()
		card8OpByNumber(mutated, 40).OutputUnitName = text("ghost unit")
		dirty := newCardView(mutated, Fx{Base: "EUR"})
		if len(dirty.gt.Violations) == 0 {
			t.Fatal("the mutation produced no violation: this test would be a guard on dead code")
		}

		f := okFinding("op:460")
		f.Detail = "This looks like a circular dependency in the assembly graph."
		got, _, _, stats, err := VerifyModelOutput(oneFinding(t, f), dirty, nil)
		if err != nil {
			t.Fatalf("VerifyModelOutput: %v", err)
		}
		if len(got) != 1 || stats.DroppedContradiction != 0 {
			t.Fatalf("findings = %d, DroppedContradiction = %d, want 1 / 0",
				len(got), stats.DroppedContradiction)
		}
	})
}

// ── §8 п.3: EVIDENCE НЕ ВЕРИФИЦИРУЕТСЯ И НИЧЕГО НЕ ДРОПАЕТ ─────────────────────────────────────

func TestVerifyModelOutputEvidenceIsNeverGroundsForDropping(t *testing.T) {
	card := verifierCard(t)
	cases := []struct {
		name     string
		evidence []string
	}{
		{"evidence quoting an operation that does not exist", []string{"op 9999 | produces: nothing"}},
		{"evidence quoting a price rather than a step", []string{"BOM: 60 PLN/m"}},
		{"evidence quoting an absence, which has no verbatim line", []string{"VERIFIED FACTS: fusing operations: 0"}},
		{"no evidence at all", nil},
		{"evidence that is pure whitespace", []string{"   ", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := okFinding("op:460")
			f.Evidence = tc.evidence
			got, _, _, stats, err := VerifyModelOutput(oneFinding(t, f), card, nil)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding dropped over evidence, which §8 п.3 forbids; drops = %#v", stats.Drops)
			}
		})
	}
}

// ── §8 п.6: insert_after ───────────────────────────────────────────────────────────────────────

func TestVerifyModelOutputInsertAfterCoercion(t *testing.T) {
	cases := []struct {
		name             string
		category, insert string
		want             string
	}{
		{"canonical", CategoryMissingStep, "op:120", "op:120"},
		{"coerced from prose", CategoryMissingStep, "op 120", "op:120"},
		{"coerced from a bare number", CategoryMissingStep, "120", "op:120"},
		{"start", CategoryMissingStep, "start", "start"},
		{"start in caps", CategoryMissingStep, "START", "start"},
		{"empty stays empty", CategoryMissingStep, "", ""},
		{"an operation that is not on the card is cleared", CategoryMissingStep, "op:9999", ""},
		{"a unit is not an insertion point", CategoryMissingStep, "unit:blazer", ""},
		{"card is not an insertion point", CategoryMissingStep, RefCard, ""},
		{"prose is not an insertion point", CategoryMissingStep, "after the sleeves", ""},
		// Поле существует только у missing_step (§7.1 п.7): стрелка вставки на находке о названии
		// узла предлагает то, чего находка не предлагает.
		{"cleared on a category that has no insertion point", CategoryMethod, "op:120", ""},
		{"cleared on a category coerced into question", "topology", "op:120", ""},
	}

	card := verifierCard(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := okFinding("op:460")
			f.Category, f.InsertAfter = tc.category, tc.insert
			got, _, _, _, err := VerifyModelOutput(oneFinding(t, f), card, nil)
			if err != nil {
				t.Fatalf("VerifyModelOutput: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding count = %d, want 1: a bad insert_after never kills the finding", len(got))
			}
			if got[0].InsertAfter != tc.want {
				t.Fatalf("insert_after = %q, want %q", got[0].InsertAfter, tc.want)
			}
		})
	}
}

// ── §8 п.7: ОБРЕЗКА 15 И КАПЫ ТЕКСТОВ ──────────────────────────────────────────────────────────

func TestVerifyModelOutputTruncatesToFifteen(t *testing.T) {
	card := verifierCard(t)
	findings := make([]rawFinding, 0, 20)
	for i := 0; i < 20; i++ {
		f := okFinding("op:460")
		f.Title = fmt.Sprintf("finding %02d", i)
		findings = append(findings, f)
	}
	got, _, _, stats, err := VerifyModelOutput(rawResponse(t, findings, nil, "s"), card, nil)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if len(got) != MaxModelFindings {
		t.Fatalf("finding count = %d, want %d", len(got), MaxModelFindings)
	}
	if stats.Truncated != 5 {
		t.Fatalf("Truncated = %d, want 5", stats.Truncated)
	}
	if stats.Dropped() != 0 {
		t.Fatalf("Dropped() = %d, want 0: truncation is not a drop and must not feed the death threshold",
			stats.Dropped())
	}
	// Обрезается ХВОСТ: правило 9 §7.1 велит модели ставить важное первым.
	if got[0].Title != "finding 00" || got[MaxModelFindings-1].Title != "finding 14" {
		t.Fatalf("kept the wrong end: first %q, last %q", got[0].Title, got[MaxModelFindings-1].Title)
	}
}

func TestVerifyModelOutputBoundsTexts(t *testing.T) {
	card := verifierCard(t)
	long := strings.Repeat("я", 4000)

	f := okFinding("op:460")
	f.Title, f.Detail, f.Suggestion = long, long, long
	f.Evidence = []string{long}
	notChecked := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		notChecked = append(notChecked, fmt.Sprintf("line %02d ", i)+long)
	}

	got, gotNotChecked, summary, _, err := VerifyModelOutput(
		rawResponse(t, []rawFinding{f}, notChecked, long), card, nil)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("finding count = %d, want 1", len(got))
	}
	// Числа ЛИТЕРАЛАМИ, а не константами пакета. Кап, сверенный с собственной константой, — это
	// тавтология: подняв константу до 900, реализация подняла бы вместе с ней и ожидание, и тест
	// остался бы зелёным ровно в том случае, ради которого написан. 90 — не наше число вовсе:
	// это контракт провода (§4, «title <= 90 chars»).
	checkBounded(t, "title", got[0].Title, 90)
	checkBounded(t, "detail", got[0].Detail, 1200)
	checkBounded(t, "suggestion", got[0].Suggestion, 600)
	if len(got[0].Evidence) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(got[0].Evidence))
	}
	checkBounded(t, "evidence", got[0].Evidence[0], 300)
	checkBounded(t, "summary", summary, 600)
	if len(gotNotChecked) != 10 {
		t.Fatalf("not_checked count = %d, want 10", len(gotNotChecked))
	}
	for i, line := range gotNotChecked {
		checkBounded(t, fmt.Sprintf("not_checked[%d]", i), line, 200)
	}
}

// checkBounded fails when a field was NOT cut, and fails just as loudly when it was cut without the
// ellipsis: a truncated sentence read as if the model had ended it there is a different instruction
// from the one it wrote.
func checkBounded(t *testing.T, field, got string, max int) {
	t.Helper()
	n := utf8.RuneCountInString(got)
	if n != max {
		t.Errorf("%s is %d runes, want exactly %d (the cap)", field, n, max)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("%s was cut without the ellipsis that marks the cut: %q", field, lastRunes(got, 12))
	}
}

// ── §8 п.8: finish_reason == "length" ──────────────────────────────────────────────────────────

func TestVerifyModelRunFinishReason(t *testing.T) {
	// Ответ ЦЕЛЫЙ и разбираемый: length здесь режет не JSON, а ревью, и признак обязан быть
	// сильнее парсера. Тест, подающий сюда обрезанный JSON, доказал бы только работу парсера.
	raw := oneFinding(t, okFinding("op:460"))
	card := card8()

	cases := []struct {
		name         string
		finishReason string
		wantErr      bool
	}{
		{"length kills the run", FinishReasonLength, true},
		{"length in caps kills it too", "LENGTH", true},
		{"stop is a complete answer", "stop", false},
		{"an empty finish reason is not a truncation", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _, stats, err := VerifyModelRun(raw, tc.finishReason, card, Fx{Base: "EUR"}, nil)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidOutput) {
					t.Fatalf("err = %v, want ErrInvalidOutput", err)
				}
				if len(got) != 0 || !stats.InvalidOutput {
					t.Fatalf("findings = %d, InvalidOutput = %v, want 0 / true", len(got), stats.InvalidOutput)
				}
				return
			}
			if err != nil {
				t.Fatalf("VerifyModelRun: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("finding count = %d, want 1", len(got))
			}
		})
	}
}

// TestVerifyModelOutputNilCardResolvesOnlyCard proves the verifier survives the degenerate input
// without a panic and without inventing anchors.
func TestVerifyModelOutputNilCardResolvesOnlyCard(t *testing.T) {
	got, _, _, stats, err := VerifyModelOutput(
		rawResponse(t, []rawFinding{okFinding("op:460", RefCard), okFinding("op:460")}, nil, "s"), nil, nil)
	if err != nil {
		t.Fatalf("VerifyModelOutput: %v", err)
	}
	if len(got) != 1 || !equalStrings(got[0].Refs, []string{RefCard}) {
		t.Fatalf("findings = %#v, want exactly the `card`-anchored one", got)
	}
	if stats.DroppedBadRef != 1 {
		t.Fatalf("DroppedBadRef = %d, want 1", stats.DroppedBadRef)
	}
}

// ── общие помощники ────────────────────────────────────────────────────────────────────────────

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsSubstring(lines []string, want string) bool {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func lastRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
