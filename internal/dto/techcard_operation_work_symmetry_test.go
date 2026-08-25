package dto

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// СИММЕТРИЯ ЗАПИСИ И ЧТЕНИЯ — ЛОВУШКА ПЯТОГО СПИСКА.
//
// ЧТО ЗДЕСЬ ЛОВИТСЯ И ПОЧЕМУ ЭТОГО НЕ ЛОВИТ НИЧТО ДРУГОЕ. Колонка шага живёт в ПЯТИ списках: ALTER
// миграции, ключи named-map INSERT'а, СПИСОК КОЛОНОК SELECT'а, поля entity и хвост проекции
// дайджеста. Четыре из пяти ошибаются громко: забудь колонку в ALTER'е — упадёт вставка, забудь в
// entity — не скомпилируется, забудь в хвосте — поле просто не хешируется, и это видно голденом.
// Пятый, SELECT, ошибается МОЛЧА: запись проходит целиком, а чтение возвращает NULL. Шаг открывается
// пустым по этому полю, отпечаток ЧТЕНИЯ расходится с отпечатком ЗАПИСИ, и подпись, поставленная
// после такого чтения, рождается протухшей — навсегда и без единой ошибки в логе.
//
// ПОЧЕМУ ТЕСТ ЧИТАЕТ ИСХОДНИК, А НЕ ХОДИТ В БАЗУ. Запрос лежит неэкспортированной константой в
// другом пакете, а тесты internal/store бить нельзя вовсе (они идут в живую базу и дропают таблицы).
// Текст запроса — то же самое утверждение, что и запрос: колонки, которых нет в тексте, не появятся
// в результате ни при какой базе.
//
// ⚠️ МУТАЦИЯ, КОТОРОЙ ЭТОТ ФАЙЛ ПРОВЕРЕН (прогнана и откачена): из списка колонок
// techCardOperationsQuery убрана строка `o.work`. TestOperationColumnsAllSelected назвал колонку
// поимённо, TestOperationWorkDigestSymmetry показал расхождение двух отпечатков. Возвращено — обе
// зелёные. Без этого прогона тест доказывал бы только то, что он компилируется.

// productionStoreSource — путь к единственному файлу, который здесь читается как текст.
const productionStoreSource = "../store/techcard/production.go"

var (
	operationsSelectRe = regexp.MustCompile(`(?s)const techCardOperationsQuery = ` + "`" + `(.*?)` + "`")
	selectColumnRe     = regexp.MustCompile(`\bo\.([a-z0-9_]+)\b`)
	insertMapKeyRe     = regexp.MustCompile(`(?m)^\s*"([a-z0-9_]+)":`)
)

func productionSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(productionStoreSource))
	if err != nil {
		t.Fatalf("не читается %s: %v — тест обязан упасть, а не «не найти колонок» и позеленеть",
			productionStoreSource, err)
	}
	return string(body)
}

// operationSelectColumns — колонки списка SELECT операций, по тексту запроса.
func operationSelectColumns(t *testing.T) map[string]bool {
	t.Helper()
	m := operationsSelectRe.FindStringSubmatch(productionSource(t))
	if m == nil {
		t.Fatalf("в %s не найдена константа techCardOperationsQuery — её переименовали или переписали "+
			"форму; пустой разбор молча пропустил бы ЛЮБУЮ забытую колонку", productionStoreSource)
	}
	query := m[1]
	head, _, ok := strings.Cut(query, "FROM tech_card_operation")
	if !ok {
		t.Fatalf("в запросе операций нет FROM tech_card_operation — разбор списка колонок сломан")
	}
	cols := map[string]bool{}
	for _, c := range selectColumnRe.FindAllStringSubmatch(head, -1) {
		cols[c[1]] = true
	}
	if len(cols) < 10 {
		t.Fatalf("из списка колонок SELECT'а извлечено %d имён — извлекатель сломан, а сломанный "+
			"извлекатель зеленит тест на любой ошибке", len(cols))
	}
	return cols
}

// operationInsertColumns — ключи named-map вставки операций (четвёртый список).
func operationInsertColumns(t *testing.T) map[string]bool {
	t.Helper()
	src := productionSource(t)
	start := strings.Index(src, "func insertTechCardOperations")
	if start < 0 {
		t.Fatalf("в %s не найдена insertTechCardOperations", productionStoreSource)
	}
	end := strings.Index(src[start:], "\n}\n")
	if end < 0 {
		t.Fatalf("не найден конец insertTechCardOperations — разбор четвёртого списка сломан")
	}
	cols := map[string]bool{}
	for _, c := range insertMapKeyRe.FindAllStringSubmatch(src[start:start+end], -1) {
		cols[c[1]] = true
	}
	if len(cols) < 10 {
		t.Fatalf("из named-map вставки извлечено %d ключей — извлекатель сломан", len(cols))
	}
	return cols
}

// operationDBColumns — колонки, объявленные полями entity.TechCardOperation (первый список).
func operationDBColumns() []string {
	var out []string
	rt := reflect.TypeOf(entity.TechCardOperation{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, tag)
	}
	return out
}

// TestOperationColumnsAllSelected — ЦИТАТА ПЯТОГО СПИСКА: ни одна колонка сущности не забыта в
// SELECT'е. Правило общее, а не про work: щель, в которую провалилась бы одна колонка, одинаково
// глубока для любой.
func TestOperationColumnsAllSelected(t *testing.T) {
	selected := operationSelectColumns(t)
	for _, col := range operationDBColumns() {
		if !selected[col] {
			t.Errorf("колонка %q объявлена полем entity.TechCardOperation, но её НЕТ в списке колонок "+
				"techCardOperationsQuery: запись пройдёт, чтение вернёт NULL, а подпись, поставленная "+
				"после такого чтения, протухнет молча и навсегда", col)
		}
	}
}

// TestOperationWorkIsInAllFiveLists — та же цитата точечно про ось «работа», по одному имени в
// каждом списке. Общее правило выше отвечает на «не забыли ли колонку»; это — на «доехала ли
// ИМЕННО работа», и падает оно с именем оси, а не с именем случайной колонки.
func TestOperationWorkIsInAllFiveLists(t *testing.T) {
	if !operationInsertColumns(t)["work"] {
		t.Error("колонки \"work\" нет среди ключей named-map insertTechCardOperations — работа не пишется вовсе")
	}
	if !operationSelectColumns(t)["work"] {
		t.Error("колонки \"work\" нет в списке колонок techCardOperationsQuery — работа пишется, но не читается")
	}
	found := false
	for _, col := range operationDBColumns() {
		if col == "work" {
			found = true
		}
	}
	if !found {
		t.Error("у entity.TechCardOperation нет поля с тегом db:\"work\"")
	}
	// Пятый список — хвост дайджеста: работа обязана в него попадать, иначе она не подписывается.
	tags := opKindTagsOf(t, entity.TechCardOperation{
		OperationType: entity.OpTypeMachine,
		Work:          opGoldStr("topstitch"),
	})
	if !slicesContains(tags, "work") {
		t.Errorf("шаг с названной работой не эмитит хвоста \"work\": %v — работа не входит в подпись", tags)
	}
}

func slicesContains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// readBackAsSelectWould возвращает шаг ТАКИМ, каким его вернуло бы чтение: каждое поле, чьей
// колонки НЕТ в списке SELECT'а, обнуляется — ровно то, что делает sqlx, когда колонки нет в
// результате. Поля с тегом db:"-" не трогаются: они приезжают другими запросами (связки деталей,
// медиа, входы сборки) и к этому списку отношения не имеют.
func readBackAsSelectWould(t *testing.T, op entity.TechCardOperation, selected map[string]bool) entity.TechCardOperation {
	t.Helper()
	out := op
	rv := reflect.ValueOf(&out).Elem()
	rt := rv.Type()
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" || selected[tag] {
			continue
		}
		rv.Field(i).Set(reflect.Zero(rt.Field(i).Type))
	}
	return out
}

// TestOperationWorkDigestSymmetry — САМА СИММЕТРИЯ.
//
// Путь ЗАПИСИ: payload с названной работой → parseTechCardOperations → сущность → отпечаток.
// Путь ЧТЕНИЯ: та же сущность, из которой вычеркнуто всё, чего нет в списке колонок SELECT'а, →
// отпечаток. Равенство двух чисел и есть утверждение «что записали, то и прочли».
//
// Тест НАМЕРЕННО начинается с провода, а не с готовой сущности: разбор — часть пути записи, и
// правило, которое отвергло бы работу или подменило её пустотой, обязано проявиться здесь же.
func TestOperationWorkDigestSymmetry(t *testing.T) {
	restore := withWorkCatalog(t)
	defer restore()

	pbOps := []*pb_common.TechCardOperation{{
		OperationNumber: 20,
		OperationType:   pb_common.TechCardOperationType_TECH_CARD_OPERATION_TYPE_MACHINE,
		MachineType:     pb_common.TechCardMachineType_TECH_CARD_MACHINE_TYPE_LOCKSTITCH,
		Zone:            pb_common.TechCardGarmentZone_TECH_CARD_GARMENT_ZONE_OUTER,
		Work:            "topstitch",
	}}
	written, err := parseTechCardOperations(pbOps, map[int]bool{}, 0, nil, nil, true)
	if err != nil {
		t.Fatalf("payload с названной работой не разобрался: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("ожидался один шаг, получено %d", len(written))
	}
	if got := written[0].Work; !got.Valid || got.String != "topstitch" {
		t.Fatalf("работа не доехала с провода в сущность: %#v — дальше сравнивать нечего", got)
	}

	selected := operationSelectColumns(t)
	read := readBackAsSelectWould(t, written[0], selected)

	writeDigest := digestOf(constructionProjection(&entity.TechCardInsert{Operations: written}))
	readDigest := digestOf(constructionProjection(&entity.TechCardInsert{
		Operations: []entity.TechCardOperation{read},
	}))
	if writeDigest != readDigest {
		t.Errorf("отпечаток ЗАПИСИ и отпечаток ЧТЕНИЯ разошлись — какая-то колонка пишется, но не "+
			"читается, и подпись после чтения протухает молча.\n--- записали ---\n%s\n--- прочли ---\n%s\n"+
			"работа после чтения: %#v", writeDigest, readDigest, read.Work)
	}
}

// withWorkCatalog публикует маленький каталог на время теста и возвращает восстановитель.
//
// Каталог — процессный снимок, поэтому тест обязан его ВЕРНУТЬ: оставленный набор сделал бы соседний
// тест «каталог не загружен» зелёным по чужой причине.
func withWorkCatalog(t *testing.T) func() {
	t.Helper()
	if entity.OperationWorkCatalogSnapshot() != nil {
		t.Fatal("снимок каталога уже опубликован до начала теста — предыдущий тест его не вернул, " +
			"и тест «каталог не загружен» позеленел бы по чужой причине")
	}
	entity.SetOperationWorkCatalog(testWorkCatalog())
	return func() { entity.SetOperationWorkCatalog(nil) }
}

// testWorkCatalog — четыре работы, снятые с настоящего сида 0329 (токен, глагол, режим и машинки
// дословно). Настоящие, а не выдуманные: правила когерентности сравнивают глагол шага с глаголом
// каталога, и выдуманная пара проверяла бы согласие теста с самим собой.
func testWorkCatalog() []entity.OperationWork {
	return []entity.OperationWork{
		{
			Token: "join_lockstitch", Verb: "machine", Stage: "join_seam", Label: "Join — lockstitch",
			MachineMode: entity.OperationWorkMachineModeFixed,
			Machines:    []string{"lockstitch"},
		},
		{
			Token: "topstitch", Verb: "machine", Stage: "join_seam", Label: "Topstitch",
			MachineMode: entity.OperationWorkMachineModeAsk,
			Machines: []string{"lockstitch", "lockstitch_double_needle", "chainstitch",
				"coverstitch", "handstitch_imitation"},
		},
		{
			// hardware_set с режимом none — тот самый случай, из-за которого правило машинки
			// проверяется ТОЛЬКО при ask: 0328 сделала машинку на этом глаголе законной, а шести
			// работам каталога она не задаётся вовсе.
			Token: "set_hardware", Verb: "hardware_set", Stage: "hardware", Label: "Set hardware",
			MachineMode: entity.OperationWorkMachineModeNone,
		},
		{
			Token: "press_open", Verb: "press_open", Stage: "pressing", Label: "Press open",
			MachineMode: entity.OperationWorkMachineModeNone,
		},
	}
}
