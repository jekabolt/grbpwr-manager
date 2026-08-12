package entity

import (
	"strings"
	"testing"
)

// Тесты классификации удаления колорвея. Стор в этом репозитории тестируется только интеграционно
// (и его тесты бьют по продовой базе), поэтому решаемая часть — предикат и три категории — вынесена
// в entity именно ради этих тестов.

func factsOf(mutate func(*ColorwayDeletionFacts)) ColorwayDeletionFacts {
	f := ColorwayDeletionFacts{ColorwayID: 7, Label: "SS26-00021-BLK"}
	if mutate != nil {
		mutate(&f)
	}
	return f
}

func reasons(entries []ColorwayDeletionEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Reason)
	}
	return out
}

func findEntry(t *testing.T, entries []ColorwayDeletionEntry, reason string) ColorwayDeletionEntry {
	t.Helper()
	for _, e := range entries {
		if e.Reason == reason {
			return e
		}
	}
	t.Fatalf("entry %q not found in %v", reason, reasons(entries))
	return ColorwayDeletionEntry{}
}

// Чистый колорвей — тот, ради которого фича написана: ничего не продано, не произведено, не
// настелено, остатка нет.
func TestClassifyColorwayDeletion_CleanColorwayIsDeletable(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(nil))
	if !v.Deletable {
		t.Fatalf("clean colourway must be deletable, blockers = %v", reasons(v.Blockers))
	}
	if len(v.Blockers) != 0 {
		t.Fatalf("clean colourway must have no blockers, got %v", reasons(v.Blockers))
	}
	if v.BlockerSummary() != "" {
		t.Errorf("clean colourway summary must be empty, got %q", v.BlockerSummary())
	}
	if len(v.FieldViolations()) != 0 {
		t.Errorf("clean colourway must produce no field violations")
	}
}

// Каждый из четырёх фактов границы владельца блокирует ПООДИНОЧКЕ.
func TestClassifyColorwayDeletion_EachOwnerBoundaryFactBlocksAlone(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ColorwayDeletionFacts)
		reason string
	}{
		{"sold", func(f *ColorwayDeletionFacts) { f.Orders = 3; f.OrderLines = 4 }, ColorwayBlockerSold},
		{"draft run", func(f *ColorwayDeletionFacts) {
			f.Runs = []ColorwayRunRef{{ID: 12, Status: "draft"}}
		}, ColorwayBlockerProductionRun},
		{"lay", func(f *ColorwayDeletionFacts) {
			f.Lays = []ColorwayLayRef{{ID: 3, RunID: 12, Name: "основной"}}
		}, ColorwayBlockerLay},
		{"stock", func(f *ColorwayDeletionFacts) { f.StockUnits = 2 }, ColorwayBlockerStock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := ClassifyColorwayDeletion(factsOf(tc.mutate))
			if v.Deletable {
				t.Fatalf("%s must block deletion", tc.name)
			}
			if len(v.Blockers) != 1 || v.Blockers[0].Reason != tc.reason {
				t.Fatalf("blockers = %v, want exactly [%s]", reasons(v.Blockers), tc.reason)
			}
		})
	}
}

// ЧЕРНОВАЯ партия держит удаление — это решение владельца, а не побочный эффект фильтра по
// статусу. Тест пиннит именно её, потому что «черновик = ещё не производство» — самый естественный
// повод молча её пропустить.
func TestClassifyColorwayDeletion_DraftRunBlocksAndIsNamedAsDraft(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Runs = []ColorwayRunRef{{ID: 12, Status: "draft"}}
	}))
	if v.Deletable {
		t.Fatal("a draft production run must block deletion")
	}
	e := findEntry(t, v.Blockers, ColorwayBlockerProductionRun)
	if !strings.Contains(e.Text, "#12") {
		t.Errorf("blocker must name the run number, got %q", e.Text)
	}
	if !strings.Contains(e.Text, "черновик") {
		t.Errorf("blocker must say the run is a draft, got %q", e.Text)
	}
}

// Отказ называет ОБЪЕКТ, а не таблицу, и делает это для каждого статуса словаря.
func TestClassifyColorwayDeletion_RunStatusesAreNamedInRussian(t *testing.T) {
	want := map[string]string{
		"draft":              "черновик",
		"planned":            "запланирована",
		"in_progress":        "в производстве",
		"partially_received": "частично принята",
		"received":           "принята",
		"closed":             "закрыта",
		"cancelled":          "отменена",
	}
	for status, label := range want {
		v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
			f.Runs = []ColorwayRunRef{{ID: 5, Status: status}}
		}))
		e := findEntry(t, v.Blockers, ColorwayBlockerProductionRun)
		if !strings.Contains(e.Text, label) {
			t.Errorf("status %q must render as %q, got %q", status, label, e.Text)
		}
	}
	// Незнакомый статус отдаётся как есть: соврать про статус партии, которая держит удаление,
	// хуже, чем показать сырой код.
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Runs = []ColorwayRunRef{{ID: 5, Status: "teleported"}}
	}))
	if e := findEntry(t, v.Blockers, ColorwayBlockerProductionRun); !strings.Contains(e.Text, "teleported") {
		t.Errorf("unknown status must survive verbatim, got %q", e.Text)
	}
}

// Настил называется своим именем, а безымянный — партией, в которой лежит.
func TestClassifyColorwayDeletion_LaysAreNamed(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Lays = []ColorwayLayRef{
			{ID: 1, RunID: 12, Name: "основной"},
			{ID: 2, RunID: 12},
		}
	}))
	e := findEntry(t, v.Blockers, ColorwayBlockerLay)
	if e.Count != 2 {
		t.Errorf("count = %d, want 2", e.Count)
	}
	if !strings.Contains(e.Text, "«основной»") {
		t.Errorf("named lay must be quoted by name, got %q", e.Text)
	}
	if !strings.Contains(e.Text, "безымянном настиле партии #12") {
		t.Errorf("unnamed lay must fall back to its run, got %q", e.Text)
	}
}

// Длинный список сворачивается, а не вываливается дампом.
func TestClassifyColorwayDeletion_LongObjectListIsTruncated(t *testing.T) {
	runs := make([]ColorwayRunRef, 0, 9)
	for i := 1; i <= 9; i++ {
		runs = append(runs, ColorwayRunRef{ID: i, Status: "planned"})
	}
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) { f.Runs = runs }))
	e := findEntry(t, v.Blockers, ColorwayBlockerProductionRun)
	if e.Count != 9 {
		t.Errorf("count must stay honest at 9, got %d", e.Count)
	}
	if !strings.Contains(e.Text, "и ещё 4") {
		t.Errorf("tail must be folded into «и ещё 4», got %q", e.Text)
	}
	if strings.Contains(e.Text, "#9") {
		t.Errorf("truncated tail must not be printed, got %q", e.Text)
	}
}

// Остаточные RESTRICT — тоже блокеры. Без них ровно эти строки дали бы MySQL 1451 в лицо
// оператору, то есть ровно тот провал, ради устранения которого фича написана.
func TestClassifyColorwayDeletion_ResidualRestrictsBlock(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.InventoryTargets = 1
		f.Fittings = 2
	}))
	if v.Deletable {
		t.Fatal("inventory target / fitting RESTRICT must block, not 1451 later")
	}
	if got := reasons(v.Blockers); len(got) != 2 {
		t.Fatalf("blockers = %v, want inventory_target + fitting", got)
	}
	findEntry(t, v.Blockers, ColorwayBlockerInventoryTarget)
	findEntry(t, v.Blockers, ColorwayBlockerFitting)
}

// Несколько причин приезжают ВСЕ и по одной на категорию: оператор не должен снимать их по кругу.
func TestClassifyColorwayDeletion_AllBlockersReportedAtOnce(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Orders = 1
		f.OrderLines = 2
		f.Runs = []ColorwayRunRef{{ID: 12, Status: "draft"}}
		f.StockUnits = 5
	}))
	got := reasons(v.Blockers)
	if len(got) != 3 {
		t.Fatalf("blockers = %v, want sold + production_run + stock", got)
	}
	fvs := v.FieldViolations()
	if len(fvs) != 3 {
		t.Fatalf("field violations = %d, want 3", len(fvs))
	}
	for i, fv := range fvs {
		if fv.Field != "colorway_id" {
			t.Errorf("violation %d field = %q, want colorway_id", i, fv.Field)
		}
		if fv.Reason != v.Blockers[i].Reason {
			t.Errorf("violation %d reason = %q, want %q", i, fv.Reason, v.Blockers[i].Reason)
		}
		if fv.Conflicting != v.Blockers[i].Text {
			t.Errorf("violation %d must carry the human sentence, got %q", i, fv.Conflicting)
		}
		if !strings.Contains(fv.HowToFix, "архивируйте") {
			t.Errorf("violation %d must point at the archive, got %q", i, fv.HowToFix)
		}
	}
	if s := v.BlockerSummary(); !strings.Contains(s, "продан") || !strings.Contains(s, "#12") || !strings.Contains(s, "5 шт") {
		t.Errorf("summary must carry every blocker, got %q", s)
	}
}

// Каскад и сироты — РАЗНЫЕ категории. Раскладка переживает удаление и теряет колорвей; ни блокером,
// ни каскадом она не является, и подмена одной категории другой соврала бы оператору в обе стороны.
func TestClassifyColorwayDeletion_OrphansAreNeitherBlockerNorCascade(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Cascade.RecipeUsages = 3
		f.Cascade.Variants = 4
		f.Orphans.Markers = 2
		f.Orphans.Samples = 1
	}))
	if !v.Deletable {
		t.Fatalf("orphans must not block deletion, blockers = %v", reasons(v.Blockers))
	}
	if got := reasons(v.Cascade); len(got) != 2 {
		t.Fatalf("cascade = %v, want variant + recipe_usage", got)
	}
	findEntry(t, v.Cascade, ColorwayCascadeVariant)
	findEntry(t, v.Cascade, ColorwayCascadeRecipeUsage)

	if got := reasons(v.Orphans); len(got) != 2 {
		t.Fatalf("orphans = %v, want orphan_marker + orphan_sample", got)
	}
	m := findEntry(t, v.Orphans, ColorwayOrphanMarker)
	if m.Count != 2 {
		t.Errorf("marker orphan count = %d, want 2", m.Count)
	}
	if !strings.Contains(m.Text, "потеряют колорвей") {
		t.Errorf("marker orphan must say what is lost, got %q", m.Text)
	}
	for _, e := range v.Cascade {
		if strings.HasPrefix(e.Reason, "orphan_") {
			t.Errorf("orphan %q leaked into the cascade list", e.Reason)
		}
	}
	for _, e := range v.Orphans {
		if !strings.HasPrefix(e.Reason, "orphan_") {
			t.Errorf("cascade entry %q leaked into the orphan list", e.Reason)
		}
	}
}

// Нулевые категории в списки не попадают: «0 раскладок осиротеет» — шум, который оператор
// научится пролистывать вместе с настоящими строками.
func TestClassifyColorwayDeletion_ZeroCountsAreOmitted(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(nil))
	if len(v.Cascade) != 0 || len(v.Orphans) != 0 {
		t.Fatalf("empty facts must yield empty lists, got cascade %v orphans %v",
			reasons(v.Cascade), reasons(v.Orphans))
	}
}

// Каждое поле каскада и сирот доезжает до вердикта СВОИМ кодом и СВОИМ счётчиком — иначе строка
// молча исчезает из диалога (или подменяется соседней), и оператор подтверждает удаление того, о
// чём ему не сказали. Счётчики попарно различны нарочно: одинаковые числа пропустили бы поле,
// разложенное не в тот код.
func TestClassifyColorwayDeletion_EveryCascadeAndOrphanFieldSurfaces(t *testing.T) {
	wantCascade := map[string]int{
		ColorwayCascadeVariant: 1, ColorwayCascadeVariantPrice: 2, ColorwayCascadePrice: 3,
		ColorwayCascadeMedia: 4, ColorwayCascadeTag: 5, ColorwayCascadeTranslation: 6,
		ColorwayCascadeRecipeUsage: 7, ColorwayCascadeSizeConsumption: 8,
		ColorwayCascadePieceMaterial: 9, ColorwayCascadePackagingRecipe: 10,
		ColorwayCascadeLabDipRound: 11, ColorwayCascadeCostEvent: 12,
		ColorwayCascadeWaitlist: 13, ColorwayCascadeStockHistory: 14, ColorwayCascadeStyleLink: 15,
	}
	wantOrphans := map[string]int{
		ColorwayOrphanMarker: 21, ColorwayOrphanMaterialMovement: 22,
		ColorwayOrphanSample: 23, ColorwayOrphanTask: 24,
	}
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Cascade = ColorwayCascadeCounts{
			Variants: 1, VariantPrices: 2, Prices: 3, Media: 4, Tags: 5, Translations: 6,
			RecipeUsages: 7, SizeConsumptions: 8, PieceMaterials: 9, PackagingRecipes: 10,
			LabDipRounds: 11, CostEvents: 12, Waitlist: 13, StockHistory: 14, StyleLinks: 15,
		}
		f.Orphans = ColorwayOrphanCounts{Markers: 21, MaterialMovements: 22, Samples: 23, Tasks: 24}
	}))

	assertEntrySet := func(name string, got []ColorwayDeletionEntry, want map[string]int) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s entries = %d (%v), want %d — a counted table lost its own entry",
				name, len(got), reasons(got), len(want))
		}
		seen := map[string]bool{}
		for _, e := range got {
			wantCount, ok := want[e.Reason]
			if !ok {
				t.Errorf("%s carries unexpected reason %q", name, e.Reason)
				continue
			}
			if seen[e.Reason] {
				t.Errorf("%s repeats reason %q — two fields collapsed into one code", name, e.Reason)
			}
			seen[e.Reason] = true
			if e.Count != wantCount {
				t.Errorf("%s[%s].Count = %d, want %d — the field is wired to the wrong counter",
					name, e.Reason, e.Count, wantCount)
			}
			if e.Text == "" {
				t.Errorf("%s[%s] has no human sentence", name, e.Reason)
			}
		}
	}
	assertEntrySet("cascade", v.Cascade, wantCascade)
	assertEntrySet("orphans", v.Orphans, wantOrphans)
}

// Русский счёт: 1 заказ / 2 заказа / 5 заказов / 11 заказов / 21 заказ.
func TestPluralRU(t *testing.T) {
	cases := map[int]string{
		1: "1 заказ", 2: "2 заказа", 4: "4 заказа", 5: "5 заказов",
		11: "11 заказов", 12: "12 заказов", 14: "14 заказов",
		21: "21 заказ", 22: "22 заказа", 25: "25 заказов",
		101: "101 заказ", 111: "111 заказов", 0: "0 заказов",
	}
	for n, want := range cases {
		if got := pluralRU(n, "%d заказ", "%d заказа", "%d заказов"); got != want {
			t.Errorf("pluralRU(%d) = %q, want %q", n, got, want)
		}
	}
}

// «продан: 0 заказов» был бы отказом, отрицающим сам себя: если строка заказа есть, счёт обязан
// быть положительным даже когда заказы почему-то не сосчитались.
func TestClassifyColorwayDeletion_SoldFallsBackToLinesWhenOrdersAreZero(t *testing.T) {
	v := ClassifyColorwayDeletion(factsOf(func(f *ColorwayDeletionFacts) {
		f.Orders = 0
		f.OrderLines = 2
	}))
	e := findEntry(t, v.Blockers, ColorwayBlockerSold)
	if e.Count != 2 {
		t.Errorf("count = %d, want the line count 2", e.Count)
	}
	if strings.Contains(e.Text, "0 ") {
		t.Errorf("blocker must never say zero, got %q", e.Text)
	}
}
