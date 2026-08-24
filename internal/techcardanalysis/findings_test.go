package techcardanalysis

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// miss builds one coverage gap anchored on an operation.
func miss(op int32, title string) CoverageMiss {
	return CoverageMiss{
		Refs: []string{RefOp(op)},
		Finding: Finding{
			Source: SourceMachine, Category: CategoryParameter, Severity: SeverityWarning,
			Title: title, Refs: []string{RefOp(op)},
		},
	}
}

// aggFinding is the aggregate template the law is handed.
func aggFinding(missing, applicable int, refs []string) Finding {
	return Finding{
		Source: SourceMachine, Category: CategoryParameter, Severity: SeverityWarning,
		Title: "press parameters missing",
		Detail: "press parameters absent on " + itoa32(int32(missing)) + " of " +
			itoa32(int32(applicable)) + " pressing operations",
		Refs: refs,
	}
}

// TestAggregateLaw walks all three branches of §3.0. Граница «три против четырёх» — не вкусовая:
// на ней стоит обещание «никогда 48 находок», и сдвиг её на единицу невидим без этого теста.
func TestAggregateLaw(t *testing.T) {
	if got := Aggregate(4, nil, aggFinding); got != nil {
		t.Errorf("|M| = 0 обязано молчать, а вернулось %d находок", len(got))
	}

	for n := 1; n <= 3; n++ {
		var m []CoverageMiss
		for i := 0; i < n; i++ {
			m = append(m, miss(int32((i+1)*10), "no press parameters"))
		}
		got := Aggregate(4, m, aggFinding)
		if len(got) != n {
			t.Fatalf("|M| = %d: находок %d, ожидается пер-операционные (%d)", n, len(got), n)
		}
		for i := range got {
			if len(got[i].Refs) != 1 || !strings.HasPrefix(got[i].Refs[0], "op:") {
				t.Errorf("пер-операционная находка потеряла свой якорь: %v", got[i].Refs)
			}
		}
	}

	// Четыре — уже одна агрегированная, с дробью и ТРЕМЯ якорями-образцами (§3.0 и §7.2: «4 of 4»,
	// якоря 50/70/100).
	four := []CoverageMiss{miss(50, "a"), miss(70, "b"), miss(100, "c"), miss(160, "d")}
	got := Aggregate(4, four, aggFinding)
	if len(got) != 1 {
		t.Fatalf("|M| = 4: находок %d, ожидается ровно одна", len(got))
	}
	if !strings.Contains(got[0].Detail, "4 of 4") {
		t.Errorf("агрегированная находка обязана цитировать дробь, а её текст: %q", got[0].Detail)
	}
	if strings.Join(got[0].Refs, ",") != "op:50,op:70,op:100" {
		t.Errorf("якоря-образцы %v, ожидаются первые три (op:50, op:70, op:100)", got[0].Refs)
	}

	// Сорок восемь пропусков — по-прежнему ОДНА находка и по-прежнему три якоря.
	var many []CoverageMiss
	for i := 0; i < 48; i++ {
		many = append(many, miss(int32((i+1)*10), "x"))
	}
	if got := Aggregate(48, many, aggFinding); len(got) != 1 || len(got[0].Refs) != 3 {
		t.Errorf("48 пропусков дали %d находок с %d якорями — обещание «никогда 48 находок» нарушено",
			len(got), len(got[0].Refs))
	}
}

// TestAggregateSampleRefsAreDeduplicated: два пропуска могут делить якорь (одна операция с двумя
// дырами). Три ОДИНАКОВЫХ якоря в агрегированной находке — это один якорь, напечатанный трижды.
func TestAggregateSampleRefsAreDeduplicated(t *testing.T) {
	m := []CoverageMiss{miss(50, "a"), miss(50, "b"), miss(70, "c"), miss(100, "d")}
	got := Aggregate(4, m, aggFinding)
	if len(got) != 1 {
		t.Fatalf("находок %d, ожидается одна", len(got))
	}
	if strings.Join(got[0].Refs, ",") != "op:50,op:70,op:100" {
		t.Errorf("якоря %v: повтор якоря обязан схлопнуться", got[0].Refs)
	}
}

func readiness(title, clause, severity string) Finding {
	return Finding{
		Source: SourceMachine, Category: CategoryReadiness, Severity: severity,
		Title: title, Clause: clause, Refs: []string{RefCard},
	}
}

func TestCollapseReadinessOnDraft(t *testing.T) {
	in := []Finding{
		{Source: SourceMachine, Category: CategoryNaming, Severity: SeverityWarning,
			Title: "Base/base", Refs: []string{RefOp(270)}},
		readiness("SMV missing", "SMV 0/48", SeverityWarning),
		readiness("no equipment profiles", "no equipment profiles", SeverityWarning),
		readiness("nothing to sew", "no operations", SeverityError),
	}

	out := CollapseReadiness(in, true)
	if len(out) != 2 {
		t.Fatalf("после схлопывания находок %d, ожидается 2 (одна readiness + одна чужая)", len(out))
	}
	var collapsed *Finding
	for i := range out {
		if out[i].Category == CategoryReadiness {
			collapsed = &out[i]
		}
	}
	if collapsed == nil {
		t.Fatal("схлопнутая readiness-находка исчезла")
	}
	if len(collapsed.Refs) != 1 || collapsed.Refs[0] != RefCard {
		t.Errorf("якорь схлопнутой %v, ожидается ровно [card]", collapsed.Refs)
	}
	want := "Not yet ready for release: SMV 0/48 · no equipment profiles · no operations"
	if collapsed.Detail != want {
		t.Errorf("текст схлопнутой:\n  got  %q\n  want %q", collapsed.Detail, want)
	}
	// Severity — МАКСИМУМ из схлопнутых: занизить его значило бы спрятать «операций ноль» за
	// словом warning.
	if collapsed.Severity != SeverityError {
		t.Errorf("severity схлопнутой %q, ожидается error (максимум из схлопнутых)", collapsed.Severity)
	}
	// Позиция первой readiness сохранена: порядок остальных не поехал.
	if out[0].Category != CategoryNaming {
		t.Errorf("порядок нечитаемых находок поехал: первой стала %q", out[0].Category)
	}
}

func TestCollapseReadinessLeavesNonDraftAlone(t *testing.T) {
	in := []Finding{
		readiness("SMV missing", "SMV 0/48", SeverityWarning),
		readiness("no labels", "no labels", SeverityWarning),
		{Source: SourceMachine, Category: CategoryIntegrity, Severity: SeverityError, Title: "dangling profile key"},
	}
	out := CollapseReadiness(in, false)
	if len(out) != 3 {
		t.Fatalf("на не-draft карточке список разворачивается целиком, а находок %d", len(out))
	}

	// И на черновике класс integrity схлопыванию НЕ подлежит: битая ссылка на черновике — такой же
	// дефект, как на релизе.
	drafted := CollapseReadiness(in, true)
	kept := 0
	for _, f := range drafted {
		if f.Category == CategoryIntegrity {
			kept++
		}
	}
	if kept != 1 {
		t.Errorf("находка класса integrity схлопнулась вместе с readiness — она не про готовность")
	}
}

func TestCollapseReadinessWithoutReadinessFindings(t *testing.T) {
	in := []Finding{{Source: SourceMachine, Category: CategoryNaming, Title: "x"}}
	if out := CollapseReadiness(in, true); len(out) != 1 || out[0].Title != "x" {
		t.Errorf("схлопывать нечего — список обязан пройти как есть, а стал %+v", out)
	}
}

// TestSortFindingsIsDeterministicAndReadable pins the order the always-on section renders in: слой
// пересчитывается при каждом открытии вкладки, и список, тасующийся на одних данных, читается как
// «что-то изменилось».
func TestSortFindingsOrder(t *testing.T) {
	in := []Finding{
		{Category: CategoryNaming, Severity: SeverityWarning, Title: "w-450", Refs: []string{RefOp(450)}},
		{Category: CategoryReadiness, Severity: SeverityWarning, Title: "card-level", Refs: []string{RefCard}},
		{Category: CategoryParameter, Severity: SeverityError, Title: "e-470", Refs: []string{RefOp(470)}},
		{Category: CategoryParameter, Severity: SeverityWarning, Title: "w-50", Refs: []string{RefOp(50)}},
		{Category: CategoryBomMismatch, Severity: SeverityBlocker, Title: "b-460", Refs: []string{RefOp(460)}},
	}
	sortFindings(in)

	var got []string
	for _, f := range in {
		got = append(got, f.Title)
	}
	want := "b-460,e-470,w-50,w-450,card-level"
	if strings.Join(got, ",") != want {
		t.Errorf("порядок %v, ожидается %s (severity, затем по маршруту, карточные — последними)",
			got, want)
	}
}

// TestRunAuditSkeleton: реестр проверок после T2 пуст, и это ВЕРНОЕ состояние — находки приносят
// T3 и T4. Проверяется то, что оркестратор обязан отдать в любом случае.
func TestRunAuditSkeleton(t *testing.T) {
	res := RunAudit(card8(), Fx{Base: "EUR", ToBase: map[string]decimal.Decimal{}})

	if len(res.Fingerprints) != 48 {
		t.Errorf("отпечатков %d, ожидается 48", len(res.Fingerprints))
	}
	// Две строки, которые машинный слой не проверяет НИКОГДА: путь текстовый, контуров деталей в
	// нём нет. Молчание проверки не должно читаться как «проверено и чисто».
	if len(res.NotChecked) < 2 {
		t.Errorf("not_checked = %v, ожидаются хотя бы эскиз и геометрия деталей", res.NotChecked)
	}
	joined := strings.Join(res.NotChecked, " | ")
	for _, want := range []string{"sketch", "geometry"} {
		if !strings.Contains(joined, want) {
			t.Errorf("not_checked не называет %q: %v", want, res.NotChecked)
		}
	}

	for _, f := range res.Findings {
		if f.Source != SourceMachine {
			t.Errorf("машинный слой выпустил находку с источником %q", f.Source)
		}
	}
}

func TestRunAuditOnNilCard(t *testing.T) {
	res := RunAudit(nil, Fx{})
	if res.Fingerprints == nil {
		t.Error("карта отпечатков обязана быть непустым значением даже на nil-карточке")
	}
	if len(res.Findings) != 0 {
		t.Errorf("на nil-карточке находок быть не может, а их %d", len(res.Findings))
	}
}

// TestFxRate pins the two answers the money checks depend on, and the one they must never get.
func TestFxRate(t *testing.T) {
	fx := Fx{Base: "EUR", ToBase: map[string]decimal.Decimal{"PLN": decimal.RequireFromString("0.23")}}

	if r, ok := fx.Rate("PLN"); !ok || !r.Equal(decimal.RequireFromString("0.23")) {
		t.Errorf("курс PLN = %v (ok=%v), ожидается 0.23", r, ok)
	}
	// Валюта, равная базе, известна всегда — иначе каждая проверка повторяла бы это условие сама.
	if r, ok := fx.Rate("eur"); !ok || !r.Equal(decimal.NewFromInt(1)) {
		t.Errorf("курс базы = %v (ok=%v), ожидается 1", r, ok)
	}
	// Неизвестная валюта — именно «неизвестна». Подставить единицу значило бы посчитать 60 GBP как
	// 60 EUR и объявить это фактом.
	if _, ok := fx.Rate("GBP"); ok {
		t.Error("неизвестная валюта отдала курс — единица здесь была бы ложным фактом")
	}
	if _, ok := fx.Rate(""); ok {
		t.Error("пустая валюта отдала курс")
	}
}

// TestRegisterIsUsableFromAnotherFile guards the mechanism T3/T4 depend on: реестр наполняется
// вызовом, а не правкой общего списка в analysis.go, — у «параллельных» задач общего файла нет.
func TestRegisterAppendsToTheRegistry(t *testing.T) {
	before := len(checks)
	marker := Finding{Source: SourceMachine, Category: CategoryQuestion, Severity: SeverityWarning,
		Title: "registry probe", Refs: []string{RefCard}}
	_ = register(func(v *cardView) []Finding {
		if v.card == nil || v.card.Id != 8 {
			return nil
		}
		return []Finding{marker}
	})
	t.Cleanup(func() { checks = checks[:before] })

	res := RunAudit(card8(), Fx{Base: "EUR"})
	found := false
	for _, f := range res.Findings {
		if f.Title == marker.Title {
			found = true
		}
	}
	if !found {
		t.Fatal("зарегистрированная проверка не была вызвана RunAudit")
	}
}

// TestCardViewIsParsedOnce pins the accessors T3/T4 read the card through — including the two that
// must never return nil (construction и парк оборудования): проверка, которая обязана сперва
// сравнить с дефолтом карточки, не должна для этого писать nil-guard.
func TestCardViewAccessors(t *testing.T) {
	v := newCardView(card8(), Fx{Base: "EUR"})

	if !v.draft {
		t.Error("карточка 8 в состоянии draft — cardView обязан это знать (§3.0 схлопывание)")
	}
	if len(v.ops) != 48 {
		t.Errorf("операций в каноническом порядке %d, ожидается 48", len(v.ops))
	}
	if v.ops[0].OperationNumber.Int32 != 10 || v.ops[47].OperationNumber.Int32 != 480 {
		t.Errorf("канонический порядок сбит: первая #%d, последняя #%d",
			v.ops[0].OperationNumber.Int32, v.ops[47].OperationNumber.Int32)
	}
	if len(v.pieceByKey) != 48 || len(v.bomByKey) != 4 {
		t.Errorf("индексы: деталей %d, строк BOM %d", len(v.pieceByKey), len(v.bomByKey))
	}
	if v.pieceName(card8PieceKey(36)) != "BP_L" {
		t.Errorf("pieceName(36) = %q, ожидается BP_L", v.pieceName(card8PieceKey(36)))
	}
	if v.pieceName("нет такого ключа") != "нет такого ключа" {
		t.Error("pieceName обязан вернуть сам ключ, а не пустую строку: якорь не бывает пустым")
	}
	if v.construction().DefaultSeamClass.String != "ss_plain" {
		t.Error("cardView не видит дефолтов конструкции")
	}
	// Профилей на карточке 0, и это ЗНАЧЕНИЕ: 0 профилей при 4 типах машин — находка C4.
	if eq := v.equipment(); eq == nil || len(eq.Machines) != 0 || len(eq.Presses) != 0 {
		t.Errorf("парк оборудования = %+v, ожидается пустой и НЕ nil", eq)
	}
	if v.gt.TerminalCount() != 1 {
		t.Errorf("cardView несёт пересчитанный ground truth: терминалов %d", v.gt.TerminalCount())
	}

	// Карточка без секции construction не должна ронять читателя.
	bare := newCardView(&entity.TechCard{}, Fx{})
	if bare.construction() == nil || bare.equipment() == nil {
		t.Error("на карточке без секции construction аксессоры обязаны отдать пустые значения")
	}
}

func TestNotCheckAndObserveAreCollected(t *testing.T) {
	before := len(checks)
	_ = register(func(v *cardView) []Finding {
		v.notCheck("probe: nothing")
		v.notCheck("   ") // пустое не пишется
		v.observe("probe: observation")
		v.observe("")
		return nil
	})
	t.Cleanup(func() { checks = checks[:before] })

	res := RunAudit(card8(), Fx{Base: "EUR"})
	if !strings.Contains(strings.Join(res.NotChecked, "|"), "probe: nothing") {
		t.Errorf("строка not_checked от проверки потерялась: %v", res.NotChecked)
	}
	for _, s := range res.NotChecked {
		if strings.TrimSpace(s) == "" {
			t.Error("пустая строка попала в not_checked")
		}
	}
	if len(res.Observations) != 1 || res.Observations[0] != "probe: observation" {
		t.Errorf("MACHINE OBSERVATIONS = %v, ожидается одна строка", res.Observations)
	}
}

func TestAiBoundedText(t *testing.T) {
	if got := aiBoundedText("  hello  ", 10); got != "hello" {
		t.Errorf("aiBoundedText не подрезал пробелы: %q", got)
	}
	// Режет по РУНАМ, не по байтам: кириллическая нота иначе обрывалась бы посреди символа.
	got := aiBoundedText("плечевая накладка", 5)
	if got != "плеч…" {
		t.Errorf("aiBoundedText = %q, ожидается «плеч…» (4 руны + многоточие)", got)
	}
	if []rune(got)[0] != 'п' {
		t.Error("обрезка поехала по байтам")
	}
}
