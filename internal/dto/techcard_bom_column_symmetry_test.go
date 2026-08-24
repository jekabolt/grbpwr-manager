package dto

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// СИММЕТРИЯ ЗАПИСИ И ЧТЕНИЯ СТРОКИ СПЕЦИФИКАЦИИ — ЛОВУШКА ЧЕТВЁРТОГО СПИСКА.
//
// Колонка строки BOM живёт в ПЯТИ списках: ALTER миграции, поля entity, ключи named-map, обе
// команды upsert'а (INSERT и UPDATE) и СПИСОК КОЛОНОК SELECT'а карточки. Четыре из пяти ошибаются
// громко: забудь колонку в ALTER'е — упадёт вставка; в entity — не скомпилируется; в named-map или
// в тексте запроса — упадёт TestBomItemUpsertQueriesBind, то есть сохранение карточки. Пятый,
// SELECT, ошибается МОЛЧА: запись проходит целиком, а чтение возвращает NULL. Строка открывается
// пустой по этому полю, отпечаток ЧТЕНИЯ расходится с отпечатком ЗАПИСИ, и подпись, поставленная
// после такого чтения, рождается протухшей — навсегда и без единой ошибки в логе.
//
// ПОЧЕМУ ТЕСТ ЧИТАЕТ ИСХОДНИК, А НЕ ХОДИТ В БАЗУ. Запрос лежит внутри неэкспортированной функции
// другого пакета, а тесты internal/store бить нельзя вовсе (они идут в живую базу и дропают
// таблицы). Текст запроса — то же самое утверждение, что и запрос: колонки, которых нет в тексте,
// не появятся в результате ни при какой базе. Тот же приём и та же причина, что в
// techcard_operation_work_symmetry_test.go.
//
// ⚠️ МУТАЦИИ, КОТОРЫМИ ФАЙЛ ПРОВЕРЕН (прогнаны и откачены): из списка колонок SELECT'а убрана
// `bi.qty_per_garment` — TestBomColumnsAllSelected назвал колонку поимённо; из bomItemInsertQuery
// убрана `spare_qty` — TestBomColumnsAllWritten назвал её. Возвращены — обе зелёные.

const materialsStoreSource = "../store/techcard/materials.go"

var (
	// Список колонок чтения карточки: от SELECT до FROM того запроса, который читает bi.
	bomSelectRe = regexp.MustCompile(`(?s)SELECT bi\.id.*?FROM tech_card_bom_item bi`)
	bomInsertRe = regexp.MustCompile("(?s)const bomItemInsertQuery = `(.*?)`")
	bomUpdateRe = regexp.MustCompile("(?s)const bomItemUpdateQuery = `(.*?)`")
)

func materialsSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Clean(materialsStoreSource))
	if err != nil {
		t.Fatalf("не читается %s: %v — тест обязан упасть, а не «не найти колонок» и позеленеть",
			materialsStoreSource, err)
	}
	return string(body)
}

func bomQueryText(t *testing.T, re *regexp.Regexp, what string) string {
	t.Helper()
	m := re.FindStringSubmatch(materialsSource(t))
	if m == nil {
		t.Fatalf("в %s не найден %s — его переписали или переименовали; пустой разбор молча "+
			"пропустил бы ЛЮБУЮ забытую колонку", materialsStoreSource, what)
	}
	return m[len(m)-1]
}

// bomEntityColumns — колонки, которые объявляет entity.TechCardBomItem. `db:"-"` — не колонка
// (значения, проставляемые в памяти: коэффициент раскроя, транспортные флаги присутствия).
func bomEntityColumns(t *testing.T) []string {
	t.Helper()
	rt := reflect.TypeOf(entity.TechCardBomItem{})
	out := make([]string, 0, rt.NumField())
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("db")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, tag)
	}
	if len(out) < 20 {
		t.Fatalf("у entity.TechCardBomItem нашлось всего %d колонок — разбор тегов сломан, "+
			"а сломанный разбор сравнивает пустоту с пустотой", len(out))
	}
	return out
}

// bomWriteOnlyRead — колонки, которые НЕ читаются запросом карточки. Пустой список сегодня, и это
// утверждение: каждая колонка строки BOM, которую сервер пишет, им же и читается. Новая запись
// здесь обязана объяснять, ПОЧЕМУ поле не возвращается на чтении, — иначе это и есть тот самый
// молчаливый NULL.
var bomWriteOnlyRead = map[string]string{}

// bomReadOnlyColumns — поля, заполняемые ТОЛЬКО обогащением чтения: они не колонки строки, а
// разрешение по каталогу, и в upsert им делать нечего.
var bomReadOnlyColumns = map[string]string{
	"id":                        "PK, сервер присваивает",
	"effective_fabric_width_cm": "обогащение 0259: COALESCE ширины строки и артикула",
	"selvedge_cm":               "обогащение 0259: кромка артикула",
}

// bomInsertOnlyColumns — колонки, которые пишутся ТОЛЬКО на вставке. line_key здесь по существу:
// это ключ сверки (S2/S3), по которому UPDATE находит строку, и переписывать его значило бы менять
// строке личность прямо в запросе, который её ищет.
var bomInsertOnlyColumns = map[string]string{
	"line_key": "ключ сверки upsert'а: назначается при создании строки и неизменен",
}

func TestBomColumnsAllSelected(t *testing.T) {
	sel := bomQueryText(t, bomSelectRe, "список колонок SELECT'а строки BOM")
	for _, col := range bomEntityColumns(t) {
		if why, ok := bomWriteOnlyRead[col]; ok {
			t.Logf("колонка %q намеренно не читается: %s", col, why)
			continue
		}
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(col) + `\b`).MatchString(sel) {
			t.Fatalf("колонка %q объявлена в entity.TechCardBomItem, но НЕ читается запросом карточки: "+
				"она вернётся NULL, строка откроется пустой по этому полю, а подпись, поставленная "+
				"после такого чтения, родится протухшей — молча", col)
		}
	}
}

func TestBomColumnsAllWritten(t *testing.T) {
	ins := bomQueryText(t, bomInsertRe, "bomItemInsertQuery")
	upd := bomQueryText(t, bomUpdateRe, "bomItemUpdateQuery")
	for _, col := range bomEntityColumns(t) {
		if why, ok := bomReadOnlyColumns[col]; ok {
			t.Logf("колонка %q только для чтения: %s", col, why)
			continue
		}
		word := regexp.MustCompile(`\b` + regexp.QuoteMeta(col) + `\b`)
		// В INSERT'е колонка обязана быть ДВАЖДЫ — в списке колонок и в VALUES. Одна сторона из
		// двух — это падение на BIND, то есть на запросе сохранения карточки.
		if n := len(word.FindAllString(ins, -1)); n < 2 {
			t.Fatalf("колонка %q встречается в bomItemInsertQuery %d раз(а): в INSERT'е она обязана "+
				"быть и в списке колонок, и в VALUES", col, n)
		}
		if why, ok := bomInsertOnlyColumns[col]; ok {
			t.Logf("колонка %q пишется только на вставке: %s", col, why)
			continue
		}
		if !word.MatchString(upd) {
			t.Fatalf("колонка %q не обновляется bomItemUpdateQuery: правка строки её потеряет молча", col)
		}
	}
}

// TestBomCountableColumnsAreGuardedTogether — пара счётной нормы (0333) защищена ОДНИМ флагом
// присутствия, и обе её половины обязаны стоять под ним. Половина без гарда стиралась бы сейвом
// вкладки со старым бандлом, а половина под ЧУЖИМ гардом рассинхронизировала бы пару.
func TestBomCountableColumnsAreGuardedTogether(t *testing.T) {
	upd := bomQueryText(t, bomUpdateRe, "bomItemUpdateQuery")
	for _, col := range []string{"qty_per_garment", "spare_qty"} {
		want := col + "=IF(:countable_omitted, " + col + ", :" + col + ")"
		if !strings.Contains(strings.Join(strings.Fields(upd), " "), want) {
			t.Fatalf("в bomItemUpdateQuery нет гарда %q: колонка либо не защищена вовсе, либо "+
				"защищена не тем флагом — вкладка со старым бандлом сотрёт счётную норму карточки", want)
		}
	}
}
