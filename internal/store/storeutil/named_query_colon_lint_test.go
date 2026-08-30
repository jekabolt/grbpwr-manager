package storeutil

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// ЛИНТ: В ЗАПРОСЕ С ИМЕНОВАННЫМИ ПАРАМЕТРАМИ НЕ БЫВАЕТ ДВОЕТОЧИЯ В `--` КОММЕНТАРИИ.
//
// ПОЧЕМУ ЭТОТ СТОРОЖ СУЩЕСТВУЕТ ОТДЕЛЬНО ОТ СОСЕДНЕГО. Рядом стоит
// TestMakeQueryParameterlessColonSafe, и он закрывает ПОЛОВИНУ беды: беспараметрический запрос
// makeQuery проводит мимо сканера sqlx целиком, и двоеточие в нём безвредно. Запрос С
// ПАРАМЕТРАМИ мимо сканера пройти не может по построению — параметры и есть то, ради чего сканер
// зовут. Для него правило «никаких двоеточий, кроме самих параметров» не сторожилось НИЧЕМ, хотя
// комментарий в query.go перечисляет три случая, когда эта ловушка уже роняла продакшен
// (GetReceivables, tech-card afbdcf0, JPK-evidence dfb69b4), и назвал верное лекарство —
// «комментировать на стороне Go».
//
// КАК ОНА ВЫГЛЯДИТ, КОГДА СРАБАТЫВАЕТ. Не как ошибка SQL. Запрос падает на СВЯЗЫВАНИИ, до
// драйвера, с текстом `could not find name  in map[...]` — с пустым именем в середине фразы.
// Искать причину в комментарии не станет никто, потому что комментарий MySQL всё равно
// проигнорировал бы; беда в том, что до MySQL дело не доходит.
//
// ЧТО ЭТА ПРОБА ДОКАЗЫВАЕТ И ЧЕГО НЕ ДОКАЗЫВАЕТ. Правило ЛЕКСИЧЕСКОЕ, и проверять его можно
// только по тексту исходника — здесь это не «сторож над мёртвым кодом», потому что охраняемое
// и есть текст. Но отсюда следует и ограничение: подмена через `go test -overlay` этот тест НЕ
// шевелит, он читает файлы с диска. Мутировать его надо ФИЗИЧЕСКОЙ правкой дерева.
//
// Проба НЕ утверждает, что запрос вообще уходит в Named*-вызов: определить это статически нельзя.
// Она сужает поле лексически — литерал, несущий `:имя`, почти наверняка именованный, — и в этом
// сужении даёт ноль ложных срабатываний на сегодняшнем дереве (замерено).
func TestNamedQueriesCarryNoColonInsideSQLComments(t *testing.T) {
	root := storeRoot(t)

	// `:` + буква/подчёркивание — это именованный параметр. Не подходит ни `':'` (двоеточие в
	// кавычках), ни `::` , ни `: ` — то есть литерал попадает под правило только если он
	// ДЕЙСТВИТЕЛЬНО именованный.
	namedParam := regexp.MustCompile(`:[A-Za-z_]\w*`)

	type violation struct {
		file, comment string
		line          int
	}
	var found []violation
	named, withComments := 0, 0

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "mocks" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil // не наше дело: несобирающийся файл поймает компилятор
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			body, uerr := strconv.Unquote(lit.Value)
			if uerr != nil {
				return true
			}
			if !namedParam.MatchString(body) {
				return true // беспараметрический — его закрывает соседняя проба
			}
			named++
			if !strings.Contains(body, "--") {
				return true
			}
			withComments++
			base := fset.Position(lit.Pos()).Line
			for i, line := range strings.Split(body, "\n") {
				idx := strings.Index(line, "--")
				if idx < 0 {
					continue
				}
				if strings.Contains(line[idx:], ":") {
					found = append(found, violation{
						file: path, line: base + i, comment: strings.TrimSpace(line[idx:]),
					})
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("обход дерева не удался: %v", err)
	}

	// ПОЛОЖИТЕЛЬНЫЙ КОНТРОЛЬ, И ОН УЖЕ ОТРАБОТАЛ. Первая редакция считала не то — литералы,
	// в которых ЕСТЬ комментарий, — и порог поймал это сразу: таких во всём сторе четыре, а не
	// двадцать. Счётчика теперь два, и это важнее, чем кажется: «нарушений нет» при нуле
	// прочитанного — вранье того же сорта, что пустой список падений у несобравшегося пакета.
	//
	// Порог стоит на ИМЕНОВАННЫХ ЗАПРОСАХ (их сотни): он проверяет, что линт вообще нашёл, где
	// смотреть. Число запросов С КОММЕНТАРИЕМ намеренно НЕ ограничено снизу — сегодня их четыре,
	// и требовать их наличия значило бы запретить их удалять.
	if named < 20 {
		t.Fatalf("линт осмотрел всего %d именованных запросов — он смотрит не туда", named)
	}
	t.Logf("осмотрено именованных запросов: %d, из них с SQL-комментарием: %d", named, withComments)

	for _, v := range found {
		t.Errorf("%s:%d — двоеточие в SQL-комментарии именованного запроса: %q\n"+
			"    sqlx прочтёт его как параметр с ПУСТЫМ именем, и запрос упадёт на связывании, "+
			"а не на синтаксисе. Убери двоеточие или перенеси мысль в Go-комментарий над запросом.",
			v.file, v.line, v.comment)
	}
}

// storeRoot возвращает корень internal/store — линт светит на весь стор, а не на свой пакет:
// правило одно для всех, кто зовёт storeutil, и нарушить его можно где угодно.
func storeRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("не удалось узнать рабочий каталог: %v", err)
	}
	return filepath.Dir(wd) // .../internal/store/storeutil -> .../internal/store
}
