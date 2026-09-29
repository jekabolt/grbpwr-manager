package design

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// These probes need NO DATABASE. They are the half of the wave's guarantees that lives in the
// SHAPE of the code — the form of a statement, the discrimination of an error, the scope of an
// aggregate — and every one of them was written against a mutation that must turn it red. The
// half that needs rows (the actual lazy-birth race, the actual CAS, the actual guard) is in
// design_db_test.go, which is written and waits for a disposable container.

// TestBenchUpsertIsAnUpsertNotASelectThenInsert is the citation half of probe 1.
//
// MUTATION IT CATCHES: replacing the lazy birth with «SELECT, no row, INSERT». Two people putting
// a plate on `front` at the same moment would then both see no row, both insert, and the second
// would get a bare 1062 — an error that is in no taxonomy and that the client cannot undo,
// because what it waits for is Aborted: slot_rev_mismatch.
func TestBenchUpsertIsAnUpsertNotASelectThenInsert(t *testing.T) {
	up := strings.ToUpper(benchSlotUpsert)
	if !strings.Contains(up, "INSERT INTO DESIGN_BENCH_SLOT") {
		t.Fatal("the lazy birth of a bench slot must be an INSERT")
	}
	if !strings.Contains(up, "ON DUPLICATE KEY UPDATE") {
		t.Fatal("the lazy birth of a bench slot must be an UPSERT: without ON DUPLICATE KEY UPDATE " +
			"two simultaneous first placements race into 1062, which no client can undo")
	}
	if strings.Contains(up, "SELECT") {
		t.Fatal("the placement statement must not read first: a select-then-insert is exactly the race")
	}
}

// TestBenchUpsertAssignsSlotRevLast is the second half of probe 1, and it guards a defect the
// plan's own printed form carries.
//
// MUTATION IT CATCHES: moving `slot_rev` up among the ON DUPLICATE KEY UPDATE assignments. MySQL
// evaluates them left to right and every later expression sees what an earlier one just wrote, so
// a `slot_rev = :expected_rev` guard placed AFTER the increment is false — and set_by/set_at are
// then silently left at the previous author and the previous time on a CAS that SUCCEEDED. The
// picture still lands in the slot; only the byline lies, which is why no round trip would show it.
func TestBenchUpsertAssignsSlotRevLast(t *testing.T) {
	idx := strings.Index(benchSlotUpsert, "ON DUPLICATE KEY UPDATE")
	if idx < 0 {
		t.Fatal("no ON DUPLICATE KEY UPDATE clause")
	}
	clause := benchSlotUpsert[idx:]
	revAt := strings.Index(clause, "slot_rev    =")
	if revAt < 0 {
		revAt = strings.Index(clause, "slot_rev =")
	}
	if revAt < 0 {
		t.Fatal("slot_rev is not assigned in the duplicate branch")
	}
	for _, col := range []string{"picture_id", "detail_name", "set_by", "set_at"} {
		at := strings.Index(clause, col)
		if at < 0 {
			t.Fatalf("%s is not assigned in the duplicate branch", col)
		}
		if at > revAt {
			t.Fatalf("%s is assigned AFTER slot_rev: its CAS guard would compare against the "+
				"already-incremented revision and silently never fire", col)
		}
	}
}

// TestDupKeyTellsTheTwoUniqueKeysApart is probe 3's sibling and a correctness guard in its own
// right. design_bench_slot carries TWO unique keys that mean opposite things, and collapsing them
// would tell a person to reload when the true answer is «that plate is taken».
//
// MUTATION IT CATCHES: mapping every 1062 onto one refusal.
func TestDupKeyTellsTheTwoUniqueKeysApart(t *testing.T) {
	cases := map[string]string{
		"Duplicate entry '7-front' for key 'design_bench_slot.uq_design_bench_view'": "uq_design_bench_view",
		"Duplicate entry '7-42' for key 'design_bench_slot.uq_design_bench_picture'": "uq_design_bench_picture",
		"Duplicate entry 'abc' for key 'uq_design_batch_client_request'":             "uq_design_batch_client_request",
	}
	for msg, want := range cases {
		key, dup := mysqlDupKey(&mysql.MySQLError{Number: 1062, Message: msg})
		if !dup {
			t.Fatalf("1062 was not recognised: %q", msg)
		}
		if key != want {
			t.Fatalf("key of %q = %q, want %q", msg, key, want)
		}
	}
	if _, dup := mysqlDupKey(errors.New("some other failure")); dup {
		t.Fatal("a non-MySQL error must not be read as a duplicate key")
	}
	if _, dup := mysqlDupKey(&mysql.MySQLError{Number: 1452, Message: "fk"}); dup {
		t.Fatal("a foreign-key failure must not be read as a duplicate key")
	}
}

// TestHeaderAggregatesAreCardWideNotPageWide is probe 5's citation half.
//
// MUTATION IT CATCHES: scoping an aggregate to the loaded page — adding a LIMIT, a cursor, or a
// join to the page's ids. The header would then report «12 runs» for a card with forty, and the
// number would look entirely plausible, which is what makes the defect survive review.
func TestHeaderAggregatesAreCardWideNotPageWide(t *testing.T) {
	for name, q := range map[string]string{
		"total_runs":    designCountRuns,
		"archived_runs": designCountArchivedRuns,
		"max_rrev":      designMaxRrev,
		"total_batches": designCountBatches,
	} {
		if !strings.Contains(q, "tech_card_id = :card") {
			t.Fatalf("%s is not scoped to the card", name)
		}
		up := strings.ToUpper(q)
		for _, forbidden := range []string{"LIMIT", ":CURSOR", ":LIMIT", " IN (:IDS)"} {
			if strings.Contains(up, forbidden) {
				t.Fatalf("%s mentions %q: an aggregate computed over the page truncates the header "+
					"by exactly what is off screen", name, forbidden)
			}
		}
	}
}

// TestColourRecipesReadSnakeCaseJSONPaths pins the seam with wave 2.
//
// The snapshot columns hold protojson written with UseProtoNames: true. protojson's DEFAULT is
// lowerCamelCase, so a writer that forgets the option makes this query — and the HidePicture guard
// — return nothing at all, with no error anywhere: an empty result is a legal state for a card
// with no runs, so nothing goes red and the chips simply never appear.
func TestColourRecipesReadSnakeCaseJSONPaths(t *testing.T) {
	if !strings.Contains(entity.DesignRunJSONFieldColour, "$.colour") {
		t.Fatal("the colour path moved; the store's query must move with it")
	}
	for _, p := range []string{
		entity.DesignInputsJSONSlotMedia,
		entity.DesignInputsJSONRefMedia,
		entity.DesignParamsJSONExtraMedia,
	} {
		if strings.ContainsAny(p, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Fatalf("JSON path %q is not snake_case: the writer stores protojson with "+
				"UseProtoNames and a camelCase path silently matches nothing", p)
		}
	}
}

// TestEveryRefusalOfTheTaxonomyExists keeps the store's vocabulary and the contract's aligned. A
// refusal the client has never heard of is a refusal it cannot undo.
func TestEveryRefusalOfTheTaxonomyExists(t *testing.T) {
	for _, err := range []error{
		entity.ErrDesignSlotRevMismatch, entity.ErrDesignForeignCardPlate,
		entity.ErrDesignCompositePlate, entity.ErrDesignHiddenPlate, entity.ErrDesignWrongKind,
		entity.ErrDesignPictureAlreadyInSlot, entity.ErrDesignDetailNameRequired,
		entity.ErrDesignSlotFilled, entity.ErrDesignNotADetailSlot,
		entity.ErrDesignInSlot, entity.ErrDesignLiveRunInput,
		entity.ErrDesignLiveCropParent, entity.ErrDesignNotComposite,
		entity.ErrDesignLayerRevMismatch, entity.ErrDesignEmptyLayer,
		entity.ErrDesignStrokesTooLarge,
	} {
		if err == nil || err.Error() == "" {
			t.Fatal("a refusal of the taxonomy is missing")
		}
	}
}

// TestBudgetDayKeyIsComputedInTheOrgTimezone. The day that resets the money bar is an
// organisational decision, not a property of whichever database session answered.
func TestBudgetDayKeyIsComputedInTheOrgTimezone(t *testing.T) {
	// 2026-03-01T23:30Z is already 2026-03-02 in Warsaw (UTC+1).
	when := mustTime(t, "2026-03-01T23:30:00Z")
	if got := DesignBudgetDayKey(when, "Europe/Warsaw"); got != "2026-03-02" {
		t.Fatalf("Warsaw day = %q, want 2026-03-02", got)
	}
	if got := DesignBudgetDayKey(when, "UTC"); got != "2026-03-01" {
		t.Fatalf("UTC day = %q, want 2026-03-01", got)
	}
	// An unloadable zone must fall back to UTC rather than to the server's own local day, which
	// would move the reset by hours without telling anybody.
	if got := DesignBudgetDayKey(when, "Mars/Olympus"); got != "2026-03-01" {
		t.Fatalf("unloadable zone day = %q, want the UTC fallback 2026-03-01", got)
	}
}

// TestSilhouetteAndDetailAddressSpacesDoNotOverlap. A detail is never addressed by view; the four
// sides never by a minted id. Overlapping the two address spaces is how a rename moves a plate.
func TestSilhouetteAndDetailAddressSpacesDoNotOverlap(t *testing.T) {
	if entity.IsDesignSilhouetteView(entity.DesignViewDetail) {
		t.Fatal("`detail` must not be one of the four silhouette sides")
	}
	if !entity.IsDesignGhostView(entity.DesignViewDetail) {
		t.Fatal("`detail` is a legal ghost view and a legal reference role")
	}
	for _, v := range entity.DesignSilhouetteViews {
		if !entity.IsDesignGhostView(v) {
			t.Fatalf("%q must be a legal ghost view", v)
		}
	}
	if entity.IsDesignGhostView("sleeve") {
		t.Fatal("an unknown view must be refused, not accepted as an open string")
	}
}

// mustTime parses an RFC3339 instant or fails the probe.
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

// TestOrphanedMediaCatchesTheIdempotentShortCircuit is the running half of the compensation probe
// pair.
//
// The byte work of a split happens BEFORE the transaction, so every crop already has a public
// media row when the transaction runs. The verbatim upload helper cleans up its bucket object only
// while the media row does not yet exist; once AddMedia succeeds the row belongs to the caller,
// and that is exactly where this window opens.
//
// THE CASE THAT LOOKS FINE AND IS NOT: err == nil. An idempotent split returns the crops of an
// EARLIER cut, so this call's fresh uploads were adopted by nothing — and a compensation that only
// ran on error would leave them public and ownerless forever.
//
// MUTATION: make the handler sweep only when the store returned an error (i.e. treat err == nil as
// "everything was adopted"). THIS probe must go red on the third case.
func TestOrphanedMediaCatchesTheIdempotentShortCircuit(t *testing.T) {
	cases := []struct {
		name    string
		minted  []int
		adopted []int
		want    []int
	}{
		{"nothing minted", nil, []int{7}, nil},
		{"everything adopted", []int{7, 8}, []int{7, 8}, nil},
		{"idempotent short circuit: the store returned OLDER crops", []int{9, 10}, []int{7, 8}, []int{9, 10}},
		{"partial adoption", []int{9, 10}, []int{9}, []int{10}},
		{"a zero id is not a media row", []int{0, 9}, []int{}, []int{9}},
	}
	for _, c := range cases {
		got := OrphanedMedia(c.minted, c.adopted)
		if len(got) != len(c.want) {
			t.Fatalf("%s: orphans = %v, want %v", c.name, got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: orphans = %v, want %v", c.name, got, c.want)
			}
		}
	}
}

// ─────────────────── выходы карточки: цитаты, не строки ───────────────────

// TestCardOutputsCountAndListShareOnePredicate — счётчик и список говорят про ОДНО множество,
// И ЭТО ПРОВЕРЯЕТСЯ ПО СБОРКЕ, А НЕ ПО ТЕКСТУ.
//
// ЧТО ЭТО СТЕРЕЖЁТ. OutputsTotalByColorway существует затем, чтобы усечение на потолке было
// измеримо. Две копии предиката разошлись бы при первой правке словаря родов, и разошлись бы
// МОЛЧА: сервер сказал бы «14 из 20» там, где их ровно 14, — то есть клиент нарисовал бы фразу об
// усечении на полном списке. То же и с ключом раздела: разойдясь, он подписал бы раздел числом,
// посчитанным не по тем строкам, которые в разделе лежат.
//
// ⚠ ПОЧЕМУ ЗДЕСЬ БОЛЬШЕ НЕТ `strings.Contains(запрос, константа)`. Такую проверку проходит и
// побайтно ВПИСАННАЯ копия предиката: она содержит ту же подстроку. То есть прежний сторож ловил
// «кусок пропал» и НЕ ЛОВИЛ «кусок продублирован», ради чего он и заведён — дублю ничто не мешает
// разойтись следующей правкой. Проверок здесь две, и они ловят разное:
//
//  1. Builder зовётся с ЧАСОВЫМИ вместо области и ключа. Если он сам несёт вписанную копию, в
//     часовой сборке останутся слова настоящего предиката.
//     МУТАЦИЯ: вписать `designCardOutputsFrom + designCardOutputsWhere` внутрь `count` вместо
//     параметра `scope` — часовая сборка покажет `design_picture p` там, где его быть не может.
//  2. Запросы, которые стор ДЕЙСТВИТЕЛЬНО исполняет, сверяются с тем, что builder делает из
//     настоящих кусков. Это и есть сторож против дубля мимо builder'а.
//     МУТАЦИЯ: объявить designCountCardOutputsByColorway отдельной константой с вписанной копией
//     и убрать из неё `recolor`.
//
// ⚠ ЧЕГО ЭТА ПРОБА НЕ ЛОВИТ, И ЭТО ИЗМЕРЕНО, А НЕ ПРЕДПОЛОЖЕНО. Дубль, ПОБАЙТНО РАВНЫЙ сборке
// builder'а, проходит: сравнивать нечего, строки равны. Текстом это и нельзя поймать. Ловится
// ровно то, ради чего сторож существует, — РАСХОЖДЕНИЕ дубля: проверка 2 краснеет в тот же миг,
// когда копия отличается хоть байтом. Вторую половину пары держит живая проба
// TestDesignDBBandOutputsAreWholeCardNotThePage: разошедшийся счёт даёт {cw: 4} против пяти строк
// списка (проверено обеими мутациями выше).
func TestCardOutputsCountAndListShareOnePredicate(t *testing.T) {
	const (
		scopeSentinel    = "<<SCOPE-SENTINEL>>"
		colorwaySentinel = "<<COLORWAY-SENTINEL>>"
		sectionSentinel  = "<<SECTION-SENTINEL>>"
		workflowSentinel = "<<WORKFLOW-SENTINEL>>"
	)
	list, count := designCardOutputsStatements(scopeSentinel, colorwaySentinel, sectionSentinel,
		workflowSentinel)

	// ⚠ PHASE 2: THE WORKFLOW EXPRESSION, EXACT OCCURRENCES. Its text must reach each statement
	// only through the parameter, and exactly where it is used — list: the stamp + inside the
	// window key (2); count: the SELECT + the GROUP BY (2). The key itself is the composition of
	// the section and workflow parameters, once in PARTITION BY.
	// MUTATIONS: inline a copy of designCardOutputsWorkflow in the count's GROUP BY (count of the
	// sentinel drops to 1 and 'tryon' appears); partition by the `run_workflow` alias (key count 0).
	key := designCardOutputsWindowKey(sectionSentinel, workflowSentinel)
	for name, want := range map[string]struct {
		stmt          string
		workflow, key int
	}{"list": {list, 2, 1}, "count": {count, 2, 0}} {
		if got := strings.Count(want.stmt, workflowSentinel); got != want.workflow {
			t.Fatalf("%s carries the workflow expression %d times, want %d: the stamp, the window "+
				"and the count are ONE expression passed in, never a copy", name, got, want.workflow)
		}
		if got := strings.Count(want.stmt, key); got != want.key {
			t.Fatalf("%s carries the section-1 window key %d times, want %d", name, got, want.key)
		}
	}
	if key != "CASE WHEN "+sectionSentinel+" = 1 THEN "+workflowSentinel+" ELSE '' END" {
		t.Fatalf("the window key is the workflow INSIDE section 1 and '' elsewhere, built from the "+
			"same two pieces: %q", key)
	}

	for name, stmt := range map[string]string{"list": list, "count": count} {
		if strings.Count(stmt, scopeSentinel) != 1 {
			t.Fatalf("%s must take the FROM+WHERE scope from its single source exactly once", name)
		}
		if !strings.Contains(stmt, colorwaySentinel) {
			t.Fatalf("%s must take the section key from its single source", name)
		}
		// ⚠ И ВТОРОЙ КЛЮЧ ОКНА — ТОЖЕ ИЗ ОДНОГО ИСТОЧНИКА, ПО ТОЙ ЖЕ ПРИЧИНЕ. Секция режет окно
		// списка и группирует счёт; разойдясь между ними, она отдала бы поколорвейный итог,
		// сложенный не из тех групп, которые в списке усечены.
		if !strings.Contains(stmt, sectionSentinel) {
			t.Fatalf("%s must take the SECTION key from its single source: the window and the "+
				"count are cut by it together or the caption is counted over other rows", name)
		}
		// Ни одного слова настоящей области и настоящих ключей: всё, что от них осталось бы, —
		// это вписанная вторая копия.
		for _, inlined := range []string{
			"design_picture p", "design_run r", "p.tech_card_id", "r.kind IN", "p.colorway_id",
			"'freeform'", "'cutout'", "'extend'", "'inpaint'", "'tryon'", "'swap_fabrics'", "JSON_EXTRACT",
		} {
			if strings.Contains(stmt, inlined) {
				t.Fatalf("%s carries its OWN copy of %q instead of the shared piece: a second "+
					"copy drifts silently and makes the truncation caption lie", name, inlined)
			}
		}
	}

	// …и в бою собраны ровно те же два запроса из настоящих кусков.
	gotList, gotCount := designCardOutputsStatements(
		designCardOutputsFrom+designCardOutputsWhere, designCardOutputsColorway, designCardOutputsSection,
		designCardOutputsWorkflow)
	if gotList != designListCardOutputs || gotCount != designCountCardOutputsByColorway {
		t.Fatal("the statements the store actually runs must be what this builder produces from " +
			"the shared pieces, or the probe above proves nothing about them")
	}
}

// sqlKindSet — словарь, перечисленный в `<prefix>'a', 'b', …)`, прочитанный МНОЖЕСТВОМ.
//
// ⚠ ЗАЧЕМ РАЗБОР, А НЕ strings.Contains ПО ГОТОВОЙ СТРОКЕ. Побайтная цитата предиката проверяет
// не членство, а ПОРЯДОК И ПРОБЕЛЫ: она краснеет на переставленных словах (поведение не
// изменилось) и — что хуже — краснеет на ДОБАВЛЕННОМ роде, то есть требует править пробу при
// каждом расширении словаря. Ровно это и случилось с плейграундом: цитата `…, 'recolor')` стала
// ложной ровно тогда, когда список законно вырос. Проба обязана утверждать то, что несёт смысл, —
// КАКИЕ роды здесь есть и каких нет.
func sqlKindSet(t *testing.T, stmt, prefix string) map[string]bool {
	t.Helper()
	i := strings.Index(stmt, prefix)
	if i < 0 {
		t.Fatalf("the predicate has no %q branch at all", prefix)
	}
	rest := stmt[i+len(prefix):]
	j := strings.Index(rest, ")")
	if j < 0 {
		t.Fatalf("the %q list is not closed", prefix)
	}
	out := map[string]bool{}
	for _, w := range strings.Split(rest[:j], ",") {
		if k := strings.Trim(strings.TrimSpace(w), "'"); k != "" {
			out[k] = true
		}
	}
	if len(out) == 0 {
		t.Fatalf("the %q list is empty", prefix)
	}
	return out
}

// TestCardOutputsClassifyByRunKindNotPictureKind — род берётся у ПРОГОНА, и СОСТАВ ОБЕИХ ВЕТОК
// проверяется по членству.
//
// ЧТО ЭТО СТЕРЕЖЁТ (L-1). Перекрас рождает кадры рода `render` — это правда, на выходе фотография
// изделия, — поэтому отбор по роду КАРТИНКИ смешал бы результаты ON MODEL с рендерами, и штамп в
// ответе не смог бы их развести: у него был бы тот же `render`. Ветка `r.kind IN (...)` обязана
// содержать `recolor`, а колонка штампа — приезжать из `r.kind`.
//
// ⚠ И ВТОРАЯ ВЕТКА ЗДЕСЬ НЕ УКРАШЕНИЕ. Кадр БЕЗ прогона отбирается по СВОЕМУ роду, и `freeform`/
// `cutout` в этом списке были бы утверждением «такой кадр бывает загружен руками» — прямо
// противоположным узкому предикату ручной загрузки (entity.IsDesignUploadKind). Два места, где
// словарь ручной загрузки объявлен по-разному, разошлись бы молча: раздел показывал бы кадры,
// которых дверь загрузки не пускает.
//
// МУТАЦИИ: заменить `r.kind IN` на `p.kind IN` в прогонной ветке; выкинуть `recolor` или любой из
// двух родов плейграунда; дописать `freeform` в безпрогонную ветку; взять штамп из `p.kind`.
func TestCardOutputsClassifyByRunKindNotPictureKind(t *testing.T) {
	byRun := sqlKindSet(t, designCardOutputsWhere, "r.kind IN (")
	for _, kind := range []string{
		entity.DesignRunKindRender, entity.DesignRunKindThreed, entity.DesignRunKindPattern,
		entity.DesignRunKindRecolor, entity.DesignRunKindFreeform, entity.DesignRunKindCutout,
		entity.DesignRunKindVideo, // B-32: the clip is an output of the card like every other run's
	} {
		if !byRun[kind] {
			t.Fatalf("a picture born by a %q run is an output of this card and must be in the "+
				"run branch: a section that does not list it is a section where the person "+
				"cannot find what the card paid for", kind)
		}
	}
	// …и ровно эти. Флэт рождает ПЛОСКИЙ кадр верстака (как и снятый 29.09 вектор — его старые строки
	// остаются), а черновик идеи не рождает кадра вовсе; попав сюда, они смешали бы верстак с лентой
	// выходов.
	for _, kind := range []string{
		entity.DesignRunKindFlat, "vector", entity.DesignRunKindDraftIdea,
	} {
		if byRun[kind] {
			t.Fatalf("a %q run does not produce a card OUTPUT: listing it here mixes the bench "+
				"into the generated feed", kind)
		}
	}

	byPicture := sqlKindSet(t, designCardOutputsWhere, "p.kind IN (")
	for _, kind := range []string{entity.DesignPictureKindFreeform, entity.DesignPictureKindCutout, entity.DesignPictureKindVideo} {
		if byPicture[kind] {
			t.Fatalf("%q exists only as the OUTPUT of a run — a picture of that kind with no run "+
				"row cannot be uploaded by hand (entity.IsDesignUploadKind), and claiming "+
				"otherwise here is a second, disagreeing copy of that vocabulary", kind)
		}
	}
	for _, kind := range []string{
		entity.DesignPictureKindRender, entity.DesignPictureKindThreed, entity.DesignPictureKindPattern,
	} {
		if !byPicture[kind] {
			t.Fatalf("a hand-uploaded %q is an output of this card too — a plate that is not in "+
				"the list cannot be chosen", kind)
		}
	}

	if !strings.Contains(designListCardOutputs, "COALESCE(r.kind, '') AS run_kind") {
		t.Fatal("the stamp's kind must come from the run row, not from the picture")
	}
}

// TestCardOutputsDoNotFilterHidden — спрятанные едут СО СВОИМ ФЛАГОМ.
//
// ЧТО ЭТО СТЕРЕЖЁТ. Контракт полосы: сервер не врёт о том, что существует, фильтрует клиент
// (band.go, шапка GetBand). `hidden_at IS NULL` здесь завёл бы ВТОРОЕ место, где кадр исчезает, —
// причём невидимое: раздел просто показывал бы меньше плиток, чем говорит счётчик карточки.
//
// МУТАЦИЯ: дописать AND p.hidden_at IS NULL в предикат.
func TestCardOutputsDoNotFilterHidden(t *testing.T) {
	if strings.Contains(designCardOutputsWhere, "hidden_at") {
		t.Fatal("hidden pictures travel WITH their flag; filtering them here is a second, " +
			"invisible hiding place the client cannot see or undo")
	}
}

// TestCardOutputsAreCappedPerColorwayAndNewestFirst — потолок ПОКОЛОРВЕЙНЫЙ, порядок свежий,
// и число ПРИБИТО.
//
// ЧТО ЭТО СТЕРЕЖЁТ. Потолок достижим бесплатно (кропы и флэттены наследуют run_id и kind — довод у
// MaxCardOutputsPerColorway), поэтому важно не «есть ли он», а КАК он тратится. Общий `LIMIT` по
// карточке выбрасывал бы самые старые её строки, и раздел колорвея, целиком лежащего за
// горизонтом, приходил бы ПУСТЫМ — то есть дефект H-9, отложенный на 200 строк. Окно PARTITION BY
// делает это невыразимым.
//
// ⚠ ЧИСЛО ЗДЕСЬ ПРИБИТО НАМЕРЕННО. Прежняя редакция утверждала лишь `MaxCardOutputs >
// MaxRunPageLimit`, и потому молчаливое понижение 200 → 25 проходило весь пакет: сравнение с
// соседней константой не удостоверяет НИ ОДНОГО поведения. Понижение потолка меняет, сколько
// кадров человек видит, и обязано быть отдельным решением, а не правкой цифры.
//
// ЧТО ПОТОЛОК ДЕЙСТВИТЕЛЬНО СВЯЗАН С ЧТЕНИЕМ — доказывается не здесь, а живой пробой
// TestDesignDBBandOutputsAreCappedPerColorway (band_outputs_db_test.go): там колорвею кладут
// MaxCardOutputsPerColorway+3 кадра и считают, сколько вернулось. Текстовая проба может лишь
// удостоверить, что запрос параметризован, а не зашит числом.
//
// МУТАЦИЯ: перевернуть `ORDER BY p.id DESC` внутри окна (усечение оставило бы САМЫЕ СТАРЫЕ
// выходы), убрать PARTITION BY (общий потолок, голодание раздела), либо понизить константу.
func TestCardOutputsAreCappedPerColorwayAndNewestFirst(t *testing.T) {
	if MaxCardOutputsPerColorway != 60 {
		t.Fatalf("MaxCardOutputsPerColorway = %d: the per-colourway ceiling is 60 and changing it "+
			"changes how much of a section a person can see — decide it, do not drift it",
			MaxCardOutputsPerColorway)
	}
	// Отступы запроса к делу не относятся — сравнение идёт по словам.
	flat := strings.Join(strings.Fields(designListCardOutputs), " ")
	words := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if !strings.Contains(flat, "ROW_NUMBER() OVER ( PARTITION BY "+
		words(designCardOutputsColorway)+", "+words(designCardOutputsSection)+", "+
		words(designCardOutputsWindowKey(designCardOutputsSection, designCardOutputsWorkflow))+
		" ORDER BY p.id DESC )") {
		t.Fatal("the ceiling is spent PER COLOURWAY AND PER SECTION, newest first: a whole-card " +
			"LIMIT drops the card's oldest rows and can empty one colourway's section entirely, " +
			"and a colourway-only window lets sixty free playground pictures evict the paid " +
			"renders that share the uncoloured section with them; and inside section 1 the " +
			"window is cut PER WORKFLOW too (the FULL expression, not the run_workflow alias), " +
			"or one busy playground tile empties every other tile's results")
	}
	if !strings.Contains(words(designCountCardOutputsByColorway),
		"GROUP BY "+words(designCardOutputsColorway)+", "+words(designCardOutputsSection)+", "+
			words(designCardOutputsWorkflow)) {
		t.Fatal("the count is grouped by the SAME keys the window is cut by (the workflow refines " +
			"the section-1 key), or the per-colourway and per-workflow totals are summed from " +
			"groups other than the ones that were truncated")
	}
	if !strings.Contains(designListCardOutputs, "WHERE o.rn <= :per_colorway") {
		t.Fatal("the outputs list must carry the ceiling in the statement itself, as a parameter " +
			"rather than a literal, so the constant is what the read is bound by")
	}
	if strings.Contains(designListCardOutputs, "LIMIT") {
		t.Fatal("a whole-card LIMIT beside the per-colourway window would re-open the starvation " +
			"the window closes: the oldest colourway loses its rows again")
	}
	if !strings.HasSuffix(designListCardOutputs, "ORDER BY o.id DESC") {
		t.Fatal("outputs reach the client newest first")
	}
}

// TestRegisterBatchRefusesTheGeneratedONLY_KINDS — СТОР ПОСЛЕДНИЙ, КТО МОЖЕТ ОТКАЗАТЬ, И ОН
// СПРАШИВАЕТ ТОТ ЖЕ ПРЕДИКАТ, ЧТО ДВЕРЬ.
//
// ⚠ БЕЗ БАЗЫ, И ЭТО НЕ УЛОВКА. Проверка входа стоит ДО s.txFunc — вся она про форму запроса, а не
// про строки, — поэтому нулевой Store доходит до неё и не доходит до соединения. Ровно это и
// проверяется заодно: отказ выносится ДО транзакции, то есть без единого касания базы.
//
// ЧТО ЭТО СТЕРЕЖЁТ. `freeform` и `cutout` объявлены родами ВЫХОДА («кадром такого рода нельзя
// стать загрузкой руками»), и на этом утверждении стоит предикат выходов полосы. До сужения
// непрозрачный JPEG, зарегистрированный руками с `kind: "cutout"`, вставал в ленту кадром выреза —
// с провенансом «это вырезано» и мимо проверки альфы, которая существует затем, чтобы такую
// картинку не принять даже от поставщика.
//
// МУТАЦИЯ: вернуть IsDesignPictureKind — оба рода снова принимаются.
func TestRegisterBatchRefusesTheGeneratedONLY_KINDS(t *testing.T) {
	for _, kind := range []string{entity.DesignPictureKindFreeform, entity.DesignPictureKindCutout} {
		_, err := (&Store{}).RegisterBatch(context.Background(), entity.DesignBatchRegister{
			TechCardId:      1,
			ClientRequestId: "req-1",
			Items:           []entity.DesignUploadItem{{MediaId: 501, Kind: kind}},
		})
		if err == nil {
			t.Fatalf("kind %q is generated-only and must not be registerable by hand", kind)
		}
		if !errors.Is(err, entity.ErrDesignInvalidArgument) {
			t.Fatalf("kind %q: refusal must be the store's own invalid-argument, got %v", kind, err)
		}
		if !strings.Contains(err.Error(), "uploaded by hand") {
			t.Fatalf("kind %q: the refusal must say WHY, not just «unknown»: %v", kind, err)
		}
	}

	// ЧЕТЫРЕ ЗАКОННЫХ РОДА ПРОХОДЯТ ФОРМУ И ИДУТ ДАЛЬШЕ — до транзакции, которой у нулевого Store
	// нет. Без этой половины проба была бы зелена и у предиката, отказывающего всему.
	for _, kind := range []string{
		entity.DesignPictureKindFlat, entity.DesignPictureKindRender,
		entity.DesignPictureKindThreed, entity.DesignPictureKindPattern, "",
	} {
		func() {
			defer func() { _ = recover() }() // нулевой txFunc — это и есть «форма прошла»
			_, err := (&Store{}).RegisterBatch(context.Background(), entity.DesignBatchRegister{
				TechCardId:      1,
				ClientRequestId: "req-1",
				Items:           []entity.DesignUploadItem{{MediaId: 501, Kind: kind}},
			})
			if err != nil && errors.Is(err, entity.ErrDesignInvalidArgument) &&
				strings.Contains(err.Error(), "uploaded by hand") {
				t.Fatalf("kind %q is a legal manual upload and must pass the kind check", kind)
			}
		}()
	}
}
