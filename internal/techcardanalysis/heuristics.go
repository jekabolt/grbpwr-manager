package techcardanalysis

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ── ЭВРИСТИКИ §3.4 → MACHINE OBSERVATIONS ───────────────────────────────────────────────────────
//
// ЭТО НЕ НАХОДКИ, И ЭТО ГЛАВНОЕ РЕШЕНИЕ ФАЙЛА.
//
// Парователь ЛЕКСИЧЕСКИЙ: он читает имена и ничего кроме имён. Дизайн заранее знает, что он
// ошибётся, и называет ошибку поимённо: на карточке 8 лексика спаривает 310 «right lining» с 320
// «left lining», которые близнецами НЕ ЯВЛЯЮТСЯ (настоящие пары — 300↔310 и 320↔330, это золотая
// ошибка 2). Левенштейн на той же карточке «находит» пару «lining back» / «lining base» — два
// совершенно разных, полностью законных узла. Обе догадки полезны модели и обе НЕВЕРНЫ.
//
// Отсюда форма продукта. Находка в секции CONSTRUCTION — это утверждение машины, которое технолог
// обязан пойти и починить; выставить туда «310 и 320 — близнецы» значило бы отправить человека
// править то, что верно, и заплатить за это доверием ко ВСЕЙ секции (закон §3.0: проверка, которая
// врёт, дороже проверки, которой нет). Поэтому эвристики уезжают СТРОКАМИ в `Observations` — блок
// MACHINE OBSERVATIONS промпта, у которого в шапке стоит контракт «они МОГУТ БЫТЬ НЕВЕРНЫ;
// подтвердите, уточните или опровергните, цитируя операции» (§7.1, §7.2). Модель — единственный
// читатель, которому разрешено с ними спорить, и она единственная, кто видит входы, а не имена.
//
// ⚠️ РАСХОЖДЕНИЕ С ТЕКСТОМ ЗАДАЧИ, ЗАФИКСИРОВАННОЕ ЗДЕСЬ: T3 просит «каждая находка —
// Confidence: heuristic» И блок наблюдений. §3.4 называется «эвристики → MACHINE OBSERVATIONS (не
// находки без бейджа)», §2 отправляет их «в промпт — MACHINE OBSERVATIONS», а §14 в списке
// голден-теста ни одной эвристической находки не перечисляет. Выбрано: ТОЛЬКО наблюдения. Поле
// ConfidenceHeuristic объявлено T2 и остаётся для того дня, когда парователь научится читать входы
// и перестанет ошибаться на 310↔320 — тогда его вывод и станет находкой.
var _ = register(observeMirrorsAndTypos)

// maxObservationPairs bounds the pairing line. Карточка на 200 шагов (MaxAnalysisOperations) дала бы
// сотню пар, а блок наблюдений едет в промпт целиком: список, который длиннее самого маршрута, — это
// не наблюдение, а шум с бюджетом.
const maxObservationPairs = 24

// maxObservationTypos bounds the typo lines for the same reason.
const maxObservationTypos = 6

// mirrorGapThreshold — со скольких ЧУЖИХ шагов между близнецами разрыв становится наблюдением.
// Тройка выбрана по карточке 8: 60/90 и 70/100 разведены на два шага собственной чересполосицей
// одного и того же блока карманов (60,70,80,90,100), и назвать это разрывом значило бы утопить
// единственный настоящий — 80 и 150, между которыми лежат шесть посторонних шагов.
const mirrorGapThreshold = 3

// observeMirrorsAndTypos is the §3.4 producer: it files NO findings and returns nil, always. Весь
// его продукт — строки в v.observe().
func observeMirrorsAndTypos(v *cardView) []Finding {
	subjects := mirrorSubjects(v)
	pairs, singles := pairMirrors(subjects)

	observeMirrorPairs(v, pairs, singles)
	observeMirrorDiscrepancies(v, pairs)
	observePieceMirrors(v)
	observeTypos(v)
	return nil
}

// ── ЛЕКСИЧЕСКИЙ ПАРОВАТЕЛЬ ──────────────────────────────────────────────────────────────────────

// mirrorSubject is one step and the unit name the pairer judges it by.
type mirrorSubject struct {
	op       *entity.TechCardOperation
	num      int32
	hasNum   bool
	ord      int    // позиция в каноническом порядке
	key      string // ключ узла, о котором шаг
	side     string // "left" | "right"
	sideWord string // байтовая форма слова стороны — на ней стоит наблюдение о регистре
	norm     string // остаток имени без слова стороны, в нижнем регистре
}

// mirrorSubjects picks, for every step, the unit name it is ABOUT: what it produces, or — for a
// ОБРАБОТКА with exactly one unit input — what it works on.
//
// Обработка судится по входу намеренно: на карточке 8 близнецы 70↔100 (разутюжка правой и левой
// половин кармана) ничего не производят вовсе, и парователь, глядящий только на выходы, потерял бы
// ровно ту пару, на которой видно расхождение метода. Шаг с ДВУМЯ узлами на входе пропускается: у
// него нет одного имени, и выбрать из двух — уже не лексика, а догадка о догадке.
func mirrorSubjects(v *cardView) []mirrorSubject {
	out := make([]mirrorSubject, 0, len(v.ops))
	for ord, op := range v.ops {
		key := strings.TrimSpace(op.OutputUnitKey.String)
		if key == "" {
			var units []string
			for _, in := range op.AssemblyInputs {
				if in.Kind == entity.AssemblyInputUnit && in.Key != "" {
					units = append(units, in.Key)
				}
			}
			if len(units) != 1 {
				continue
			}
			key = units[0]
		}
		side, sideWord, norm, ok := splitSide(key)
		if !ok {
			continue
		}
		num, hasNum := opNumber(op)
		out = append(out, mirrorSubject{
			op: op, num: num, hasNum: hasNum, ord: ord,
			key: key, side: side, sideWord: sideWord, norm: norm,
		})
	}
	return out
}

// mirrorPair is one suspected twin pair, always with the smaller operation number first.
type mirrorPair struct{ a, b mirrorSubject }

// pairMirrors groups subjects by their side-stripped name and zips the two sides IN ROUTE ORDER.
//
// Зип по порядку, а не «первый попавшийся»: на карточке 8 имя «main pocket detail» несут ЧЕТЫРЕ
// шага (60 и 70 справа, 90 и 100 слева), и порядок даёт пары 60↔90 и 70↔100 — те самые, которыми
// эту карточку читает человек. Любая другая раскладка спарила бы шитьё с утюжкой.
func pairMirrors(subjects []mirrorSubject) ([]mirrorPair, []mirrorSubject) {
	type group struct {
		norm  string
		left  []mirrorSubject
		right []mirrorSubject
		seen  int
	}
	groups := map[string]*group{}
	order := make([]string, 0, len(subjects))
	for _, s := range subjects {
		g, ok := groups[s.norm]
		if !ok {
			g = &group{norm: s.norm, seen: len(order)}
			groups[s.norm] = g
			order = append(order, s.norm)
		}
		if s.side == "left" {
			g.left = append(g.left, s)
		} else {
			g.right = append(g.right, s)
		}
	}

	var (
		pairs   []mirrorPair
		singles []mirrorSubject
	)
	for _, norm := range order {
		g := groups[norm]
		n := len(g.left)
		if len(g.right) < n {
			n = len(g.right)
		}
		for i := 0; i < n; i++ {
			a, b := g.left[i], g.right[i]
			if b.ord < a.ord {
				a, b = b, a
			}
			pairs = append(pairs, mirrorPair{a: a, b: b})
		}
		singles = append(singles, g.left[n:]...)
		singles = append(singles, g.right[n:]...)
	}

	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].a.ord < pairs[j].a.ord })
	sort.SliceStable(singles, func(i, j int) bool { return singles[i].ord < singles[j].ord })
	return pairs, singles
}

// splitSide finds the ONE left/right token in a name and returns the rest of it, folded. Два слова
// стороны в одном имени («left to right») — не близнец, а фраза, и такое имя пропускается целиком.
func splitSide(key string) (side, sideWord, norm string, ok bool) {
	tokens := splitNameTokens(key)
	rest := make([]string, 0, len(tokens))
	found := 0
	for _, t := range tokens {
		switch strings.ToLower(t) {
		case "l", "left":
			found++
			side, sideWord = "left", t
		case "r", "right":
			found++
			side, sideWord = "right", t
		default:
			rest = append(rest, strings.ToLower(t))
		}
	}
	if found != 1 {
		return "", "", "", false
	}
	return side, sideWord, strings.Join(rest, " "), true
}

// splitNameTokens cuts a unit key or a piece name into its words: whitespace, underscores and
// dashes all separate, because «SL_OUT_L» and «left sleeve» are the same convention written twice.
func splitNameTokens(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// observeMirrorPairs prints the pairing itself, with the contract that the input lists — not the
// names — are the ground truth.
func observeMirrorPairs(v *cardView, pairs []mirrorPair, singles []mirrorSubject) {
	type entry struct {
		ord  int
		text string
	}
	entries := make([]entry, 0, len(pairs)+len(singles))
	for _, p := range pairs {
		entries = append(entries, entry{ord: p.a.ord, text: opNumWord(p.a) + "<->" + opNumWord(p.b)})
	}
	for _, s := range singles {
		entries = append(entries, entry{ord: s.ord, text: opNumWord(s) + "<->?"})
	}
	if len(entries) == 0 {
		return
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ord < entries[j].ord })

	shown := make([]string, 0, maxObservationPairs)
	for _, e := range entries {
		if len(shown) == maxObservationPairs {
			break
		}
		shown = append(shown, e.text)
	}
	tail := ""
	if len(entries) > len(shown) {
		tail = fmt.Sprintf(", and %d more", len(entries)-len(shown))
	}

	v.observe(fmt.Sprintf("Lexical mirror pairing over unit names suggests left/right twins: %s%s "+
		"(<->? means the pairer found no partner). The pairing is derived from NAMES only; the input "+
		"lists are the ground truth - correct it freely.", strings.Join(shown, ", "), tail))
}

// observeMirrorDiscrepancies prints what differs INSIDE the suspected twins: method, distance along
// the route, capitalisation.
func observeMirrorDiscrepancies(v *cardView, pairs []mirrorPair) {
	var methods, splits, cases []string
	for _, p := range pairs {
		if p.a.op.OperationType != p.b.op.OperationType {
			methods = append(methods, fmt.Sprintf("op %s is %s, op %s is %s",
				opNumWord(p.a), string(p.a.op.OperationType), opNumWord(p.b), string(p.b.op.OperationType)))
		} else if ma, mb := machineToken(p.a.op), machineToken(p.b.op); ma != mb {
			methods = append(methods, fmt.Sprintf("op %s runs on %s, op %s on %s",
				opNumWord(p.a), machineOrNone(ma), opNumWord(p.b), machineOrNone(mb)))
		}

		if gap := p.b.ord - p.a.ord - 1; gap >= mirrorGapThreshold {
			splits = append(splits, fmt.Sprintf("ops %s and %s are separated by ops %s-%s",
				opNumWord(p.a), opNumWord(p.b),
				opNumWord(mirrorSubject{op: v.ops[p.a.ord+1], ord: p.a.ord + 1}.withNumber()),
				opNumWord(mirrorSubject{op: v.ops[p.b.ord-1], ord: p.b.ord - 1}.withNumber())))
		}

		if caseShape(p.a.sideWord) != caseShape(p.b.sideWord) || sideStrippedCaseDiffers(p.a.key, p.b.key) {
			cases = append(cases, fmt.Sprintf("op %s %q vs op %s %q",
				opNumWord(p.a), p.a.key, opNumWord(p.b), p.b.key))
		}
	}

	if len(methods) > 0 {
		v.observe("Method differs inside suspected twins: " + strings.Join(methods, "; ") + ".")
	}
	if len(splits) > 0 {
		v.observe("Suspected twins are split along the route: " + strings.Join(splits, "; ") + ".")
	}
	if len(cases) > 0 {
		v.observe("Capitalisation differs inside suspected twins: " + strings.Join(cases, "; ") + ".")
	}
}

// observePieceMirrors is the cut-piece half of the pairer (§3.4: «пары по _L/_R … в именах узлов и
// деталях входов»). Строкой-СВОДКОЙ, а не списком двадцати пар: интересен здесь не список
// совпавших, а тот, кто НЕ совпал — деталь, у которой на карточке нет зеркала.
func observePieceMirrors(v *cardView) {
	type sided struct {
		name string
		side string
		norm string
		ord  int
	}
	seen := map[string]bool{}
	var sides []sided
	for _, op := range v.ops {
		for _, in := range op.AssemblyInputs {
			if in.Kind != entity.AssemblyInputPiece || in.Key == "" || seen[in.Key] {
				continue
			}
			seen[in.Key] = true
			name := v.pieceName(in.Key)
			side, _, norm, ok := splitSide(name)
			if !ok {
				continue
			}
			sides = append(sides, sided{name: name, side: side, norm: norm, ord: len(seen)})
		}
	}
	if len(sides) == 0 {
		return
	}

	byNorm := map[string][]sided{}
	order := make([]string, 0, len(sides))
	for _, s := range sides {
		if _, ok := byNorm[s.norm]; !ok {
			order = append(order, s.norm)
		}
		byNorm[s.norm] = append(byNorm[s.norm], s)
	}

	matched := 0
	var lonely []string
	for _, norm := range order {
		var left, right int
		for _, s := range byNorm[norm] {
			if s.side == "left" {
				left++
			} else {
				right++
			}
		}
		n := left
		if right < n {
			n = right
		}
		matched += n
		if left != right {
			for _, s := range byNorm[norm] {
				lonely = append(lonely, s.name)
			}
		}
	}

	if len(lonely) == 0 {
		v.observe(fmt.Sprintf("Lexical mirror pairing over cut-piece names matched %d _L/_R pairs and "+
			"left none unpaired.", matched))
		return
	}
	sort.Strings(lonely)
	v.observe(fmt.Sprintf("Lexical mirror pairing over cut-piece names matched %d _L/_R pairs; %d "+
		"side-named pieces have no twin under this rule: %s. The names are the only evidence here - a "+
		"twin with a different suffix reads as unpaired.", matched, len(lonely), strings.Join(lonely, ", ")))
}

// ── ПОДОЗРЕНИЯ НА ОПЕЧАТКУ ──────────────────────────────────────────────────────────────────────

// observeTypos prints the two typo heuristics of §3.4: irregular capitalisation inside a word, and
// unit keys within a Levenshtein distance of 2.
//
// ⚠️ ВТОРАЯ ЭВРИСТИКА ОШИБАЕТСЯ НА ЭТОЙ ЖЕ КАРТОЧКЕ, И ЭТО ЗАФИКСИРОВАНО ТЕСТОМ: «lining back» (340)
// и «lining base» (350) отстоят на две правки и при этом являются двумя разными, совершенно
// законными узлами. Наблюдение — единственная форма, в которой такую догадку можно показать, не
// соврав.
func observeTypos(v *cardView) {
	facts := map[string]*unitFact{}
	order := make([]string, 0, len(v.ops))
	touch := func(key string) *unitFact {
		f, ok := facts[key]
		if !ok {
			f = &unitFact{key: key, ord: len(order)}
			facts[key] = f
			order = append(order, key)
		}
		return f
	}
	for _, op := range v.ops {
		num, hasNum := opNumber(op)
		if key := strings.TrimSpace(op.OutputUnitKey.String); key != "" {
			f := touch(key)
			if hasNum {
				f.producers = append(f.producers, num)
			}
		}
		for _, in := range op.AssemblyInputs {
			if in.Kind != entity.AssemblyInputUnit || in.Key == "" {
				continue
			}
			f := touch(in.Key)
			if hasNum {
				f.users = append(f.users, num)
			}
		}
	}

	lines := make([]string, 0, maxObservationTypos)
	add := func(line string) {
		if len(lines) < maxObservationTypos {
			lines = append(lines, line)
		}
	}

	// 1. Нерегулярный регистр внутри слова.
	for _, key := range order {
		word, ok := irregularCaseWord(key)
		if !ok {
			continue
		}
		f := facts[key]
		where := make([]string, 0, 2)
		if len(f.producers) > 0 {
			where = append(where, "produced by "+opList(f.producers))
		}
		if len(f.users) > 0 {
			where = append(where, "consumed by "+opList(f.users))
		}
		add(fmt.Sprintf("Suspected typo: irregular capitalisation in %q inside unit key %q (%s).",
			word, key, strings.Join(where, "; ")))
	}

	// 2. Левенштейн <= 2 между РАЗЛИЧНЫМИ ключами. Пары, различающиеся ТОЛЬКО регистром, отсюда
	//    исключены: их уже подала A1 находкой, а промпт прямо запрещает пересказывать поданное.
	for i := 0; i < len(order); i++ {
		for j := i + 1; j < len(order); j++ {
			a, b := order[i], order[j]
			if strings.EqualFold(a, b) {
				continue
			}
			d, ok := levenshteinAtMost(a, b, 2)
			if !ok {
				continue
			}
			add(fmt.Sprintf("Suspected typo: unit keys %q (%s) and %q (%s) differ by %d edit(s).",
				a, unitWhere(facts[a]), b, unitWhere(facts[b]), d))
		}
	}

	for _, l := range lines {
		v.observe(l)
	}
}

// unitFact is one unit key with the steps that produce and consume it.
type unitFact struct {
	key       string
	ord       int
	producers []int32
	users     []int32
}

// unitWhere names where a unit key lives, so a typo suspicion can be jumped to without a search.
func unitWhere(f *unitFact) string {
	switch {
	case f == nil:
		return "nowhere"
	case len(f.producers) > 0:
		return opList(f.producers)
	case len(f.users) > 0:
		return "input of " + opList(f.users)
	default:
		return "no numbered step"
	}
}

// irregularCaseWord finds a word with an uppercase letter after its first character that is neither
// all-caps (an abbreviation) nor Title case. «LEft» qualifies; «Back», «PCK» and «pockets» do not.
func irregularCaseWord(key string) (string, bool) {
	for _, w := range splitNameTokens(key) {
		if len([]rune(w)) < 2 {
			continue
		}
		if shape := caseShape(w); shape == caseShapeMixed {
			return w, true
		}
	}
	return "", false
}

const (
	caseShapeLower = "lower"
	caseShapeUpper = "upper"
	caseShapeTitle = "title"
	caseShapeMixed = "mixed"
	caseShapeNone  = "none"
)

// caseShape classifies a word's capitalisation. Слова без букв («2») формы не имеют — сравнивать их
// регистр не с чем, и «none» отличается от всех трёх настоящих форм тем, что равно только себе.
func caseShape(w string) string {
	runes := []rune(w)
	letters, upper, lower := 0, 0, 0
	for _, r := range runes {
		if !unicode.IsLetter(r) {
			continue
		}
		letters++
		if unicode.IsUpper(r) {
			upper++
		} else {
			lower++
		}
	}
	switch {
	case letters == 0:
		return caseShapeNone
	case upper == 0:
		return caseShapeLower
	case lower == 0:
		return caseShapeUpper
	case unicode.IsUpper(runes[0]) && upper == 1:
		return caseShapeTitle
	default:
		return caseShapeMixed
	}
}

// sideStrippedCaseDiffers reports whether two twin names differ in case ANYWHERE outside their
// left/right word — «Collar inner» against «collar outer» kind of difference.
func sideStrippedCaseDiffers(a, b string) bool {
	ta, tb := sideStrippedWords(a), sideStrippedWords(b)
	if len(ta) != len(tb) {
		return false
	}
	for i := range ta {
		if ta[i] != tb[i] && strings.EqualFold(ta[i], tb[i]) {
			return true
		}
	}
	return false
}

func sideStrippedWords(s string) []string {
	tokens := splitNameTokens(s)
	out := make([]string, 0, len(tokens))
	for _, t := range tokens {
		switch strings.ToLower(t) {
		case "l", "left", "r", "right":
			continue
		}
		out = append(out, t)
	}
	return out
}

// levenshteinAtMost computes the edit distance of two strings but gives up as soon as it exceeds
// max. Порог здесь не оптимизация: без него карточка на 200 шагов считала бы двадцать тысяч полных
// матриц по имени длиной в предложение, и always-on слой стал бы платным.
func levenshteinAtMost(a, b string, max int) (int, bool) {
	ra, rb := []rune(a), []rune(b)
	if d := len(ra) - len(rb); d > max || -d > max {
		return 0, false // разница длин — нижняя граница расстояния
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		best := cur[0]
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			v := prev[j-1] + cost
			if d := prev[j] + 1; d < v {
				v = d
			}
			if d := cur[j-1] + 1; d < v {
				v = d
			}
			cur[j] = v
			if v < best {
				best = v
			}
		}
		if best > max {
			return 0, false
		}
		prev, cur = cur, prev
	}
	if prev[len(rb)] > max {
		return 0, false
	}
	return prev[len(rb)], true
}

// machineOrNone renders a machine token for prose.
func machineOrNone(m string) string {
	if m == "" {
		return "no named machine"
	}
	return m
}

// opNumWord renders a subject's operation number, or a placeholder for a legacy row without one.
func opNumWord(s mirrorSubject) string {
	if s.hasNum {
		return itoa32(s.num)
	}
	return "?"
}

// withNumber fills the number fields of a bare subject built from a step index.
func (s mirrorSubject) withNumber() mirrorSubject {
	s.num, s.hasNum = opNumber(s.op)
	return s
}
