package tenantgate

import (
	"flag"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the G4 baseline files from the current scan")

func TestG2RawDBHandles(t *testing.T) {
	_, files := loadRepo(t)
	for _, s := range files {
		if s.Generated {
			continue
		}
		for _, f := range g2Scan(s) {
			t.Error(f)
		}
	}
}

func TestG3NoSchemaInSQL(t *testing.T) {
	root, files := loadRepo(t)
	for _, s := range files {
		for _, f := range g3ScanGo(s) {
			t.Error(f)
		}
	}
	sqlFiles, err := filepath.Glob(filepath.Join(root, "internal", "store", "sql", "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range sqlFiles {
		rel := "internal/store/sql/" + filepath.Base(p)
		for _, f := range g3ScanSQLFile(rel, string(mustRead(p))) {
			t.Error(f)
		}
	}
}

const g4aHeader = `G4a ratchet: package-level vars (process-wide state) under internal/, app/, config/, cmd/.
Format: path<TAB>Name. Sentinel errors, regexp/template compiles and go:embed vars are not counted.
Refresh: go test ./tools/tenantgate/ -run TestG4PackageGlobals -update`

func g4aCurrent(files []*srcFile) []string {
	var out []string
	for _, s := range files {
		if s.Generated || !g4aInScope(s.Rel) {
			continue
		}
		for _, n := range g4aVars(s) {
			out = append(out, s.Rel+"\t"+n)
		}
	}
	sort.Strings(out)
	return out
}

func TestG4PackageGlobals(t *testing.T) {
	root, files := loadRepo(t)
	cur := g4aCurrent(files)
	base := filepath.Join(root, "tools", "tenantgate", "testdata", "g4_globals.txt")
	if *update {
		if err := writeBaseline(base, g4aHeader, cur); err != nil {
			t.Fatal(err)
		}
		return
	}
	old, err := readBaseline(base)
	if err != nil {
		t.Fatalf("read baseline: %v (create it with -update)", err)
	}
	added, removed := setDiff(cur, old)
	for _, e := range added {
		t.Errorf("new package-level var %s — keep state in a per-tenant/app struct, or refresh the baseline", show(e))
	}
	for _, e := range removed {
		t.Errorf("package-level var %s is gone — ratchet the baseline down", show(e))
	}
	if len(added)+len(removed) > 0 {
		t.Errorf("refresh with: go test ./tools/tenantgate/ -run TestG4PackageGlobals -update")
	}
}

const g4bHeader = `G4b ratchet: context.Background()/TODO() calls per file in internal/store, internal/apisrv, app and worker packages.
Format: path<TAB>count. Files without calls are not listed.
Refresh: go test ./tools/tenantgate/ -run TestG4BackgroundContext -update`

func g4bCurrent(t *testing.T, files []*srcFile) []string {
	workers := workerDirs(files)
	t.Logf("worker packages: %s", strings.Join(workers, ", "))
	var out []string
	for _, s := range files {
		if s.Generated || !g4bInScope(s.Rel, workers) {
			continue
		}
		if n := bgCount(s); n > 0 {
			out = append(out, s.Rel+"\t"+strconv.Itoa(n))
		}
	}
	sort.Strings(out)
	return out
}

func TestG4BackgroundContext(t *testing.T) {
	root, files := loadRepo(t)
	cur := g4bCurrent(t, files)
	base := filepath.Join(root, "tools", "tenantgate", "testdata", "g4_background.txt")
	if *update {
		if err := writeBaseline(base, g4bHeader, cur); err != nil {
			t.Fatal(err)
		}
		return
	}
	old, err := readBaseline(base)
	if err != nil {
		t.Fatalf("read baseline: %v (create it with -update)", err)
	}
	toMap := func(lines []string) map[string]int {
		m := map[string]int{}
		for _, l := range lines {
			k, v, _ := strings.Cut(l, "\t")
			m[k], _ = strconv.Atoi(v)
		}
		return m
	}
	cm, bm := toMap(cur), toMap(old)
	keys := map[string]bool{}
	for k := range cm {
		keys[k] = true
	}
	for k := range bm {
		keys[k] = true
	}
	var sorted []string
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	bad := false
	for _, k := range sorted {
		switch {
		case cm[k] > bm[k]:
			bad = true
			t.Errorf("%s: %d context.Background()/TODO() calls, baseline %d — thread the request ctx instead", k, cm[k], bm[k])
		case cm[k] < bm[k]:
			bad = true
			t.Errorf("%s: %d context.Background()/TODO() calls, baseline %d — ratchet the baseline down", k, cm[k], bm[k])
		}
	}
	if bad {
		t.Errorf("refresh with: go test ./tools/tenantgate/ -run TestG4BackgroundContext -update")
	}
}

func setDiff(cur, base []string) (added, removed []string) {
	cs, bs := map[string]bool{}, map[string]bool{}
	for _, c := range cur {
		cs[c] = true
	}
	for _, b := range base {
		bs[b] = true
	}
	for _, c := range cur {
		if !bs[c] {
			added = append(added, c)
		}
	}
	for _, b := range base {
		if !cs[b] {
			removed = append(removed, b)
		}
	}
	return
}

func show(e string) string { return strings.Replace(e, "\t", " ", 1) }

// ------------------------------------------------------------ matcher unit tests

func mustParse(t *testing.T, rel, src string) *srcFile {
	t.Helper()
	s, err := parseSource(rel, src)
	if err != nil {
		t.Fatalf("parse snippet: %v", err)
	}
	return s
}

func TestG2Matcher(t *testing.T) {
	aliased := `package x
import (
	stdsql "database/sql"
	jx "github.com/jmoiron/sqlx"
)
func f() { stdsql.Open("mysql", ""); jx.Connect("mysql", ""); jx.MustOpen("mysql", "") }
`
	if got := len(g2Scan(mustParse(t, "internal/store/store.go", aliased))); got != 3 {
		t.Errorf("aliased imports: want 3 findings, got %d", got)
	}
	plain := "package x\nimport \"github.com/jmoiron/sqlx\"\nfunc f() { sqlx.Open(\"a\", \"b\") }\n"
	fs := g2Scan(mustParse(t, "internal/other/x.go", plain))
	if len(fs) != 1 || !strings.Contains(fs[0].String(), "raw DB handle sqlx.Open outside") {
		t.Errorf("plain sqlx.Open: %v", fs)
	}
	for _, rel := range []string{
		"internal/store/tenantdb/open.go", "internal/platformstore/p.go", "internal/store/migrate_x.go",
		"internal/store/migreplay/main.go", "tools/x/main.go", "cmd/app/main.go", "internal/store/store_test.go",
	} {
		if got := g2Scan(mustParse(t, rel, plain)); len(got) != 0 {
			t.Errorf("%s should be allowed: %v", rel, got)
		}
	}
	if got := g2Scan(mustParse(t, "internal/store/sub/migrate.go", plain)); len(got) != 1 {
		t.Errorf("migrate*.go only allowed directly in internal/store: %v", got)
	}
	other := "package x\nimport sqlx \"example.com/other\"\nfunc f() { sqlx.Open(\"a\") }\n"
	if got := g2Scan(mustParse(t, "internal/x/x.go", other)); len(got) != 0 {
		t.Errorf("foreign package named sqlx must not match: %v", got)
	}
}

func TestG3Matcher(t *testing.T) {
	cases := []struct {
		sql  string
		want int // violations
	}{
		{"USE grbpwr_team1", 1},
		{"SELECT 1; use `team db`", 1},
		{"  \n use\n  tenant ;", 1},
		{"SELECT * FROM shop.orders", 1},
		{"select * from `shop`.`orders` o", 1},
		{"SELECT * FROM a JOIN db2.customer c ON c.id = a.id", 1},
		{"INSERT INTO db.t (a) VALUES (1)", 1},
		{"UPDATE db.t SET a = 1", 1},
		{"DELETE FROM db.t WHERE a = 1", 1},
		{"CREATE TABLE IF NOT EXISTS db.t (a int)", 1},
		{"CREATE TABLE t (a int, FOREIGN KEY (a) REFERENCES db.u(id))", 1},
		{"SELECT * FROM information_schema.tables", 0},
		{"SELECT * FROM `mysql`.user", 0},
		{"SELECT * FROM sys.x JOIN performance_schema.y ON 1", 0},
		{"SELECT * FROM orders o JOIN customer c ON c.id = o.customer_id", 0},
		{"SELECT * FROM orders o WHERE o.id = x.id", 0},
		{"SELECT * FROM %s.orders", 0},
		{"SELECT * FROM ?.orders", 0},
		{"UPDATE t SET a = 1 ON DUPLICATE KEY UPDATE t.a = 2", 0},
		{"INSERT INTO t (a) VALUES (1) ON DUPLICATE KEY UPDATE a = VALUES(a)", 0},
		{"use the force", 0},
		{"SELECT a FROM t FOR UPDATE", 0},
	}
	for _, c := range cases {
		got := sqlViolations(c.sql)
		if len(got) != c.want {
			t.Errorf("%q: want %d violations, got %v", c.sql, c.want, got)
		}
	}
	// SQL-likeness filter on Go strings
	src := "package x\nvar a = \"read from foo.bar\"\nvar b = `SELECT * FROM db.t`\n"
	fs := g3ScanGo(mustParse(t, "internal/x/x.go", src))
	if len(fs) != 1 || fs[0].Line != 3 || !strings.Contains(fs[0].Msg, `schema-qualified table "db.t" in SQL`) {
		t.Errorf("go string scan: %v", fs)
	}
	if got := g3ScanGo(mustParse(t, "internal/store/tenantdb/x.go", "package x\nvar a = \"USE db\"\n")); len(got) != 0 {
		t.Errorf("tenantdb is exempt: %v", got)
	}
	// .sql files: comments stripped, line numbers kept
	sqlSrc := "-- USE old;\n/* FROM a.b\n*/\nCREATE TABLE t (id int);\nSELECT 1\n  FROM shop.orders;\n"
	fs = g3ScanSQLFile("internal/store/sql/001.sql", sqlSrc)
	if len(fs) != 1 || fs[0].Line != 6 {
		t.Errorf("sql file scan: %v", fs)
	}
	if got := g3ScanSQLFile("x.sql", "SELECT '-- USE x' FROM t;"); len(got) != 0 {
		t.Errorf("comment marker in string: %v", got)
	}
}

func TestG4aMatcher(t *testing.T) {
	src := `package x
import (
	"errors"
	"fmt"
	"regexp"
	"html/template"
	_ "embed"
)
var ErrA = errors.New("a")
var ErrB = fmt.Errorf("b")
var re = regexp.MustCompile("x")
var re2 = regexp.MustCompilePOSIX("x")
var tpl = template.Must(template.New("t").Parse("x"))
//go:embed a.txt
var emb string
var _ = 1
var _ I = (*T)(nil)
var cache = map[string]int{}
var counter int
var (
	ErrC = errors.New("c")
	mu   sync.Mutex
	a, b = 1, 2
)
`
	got := g4aVars(mustParse(t, "internal/x/x.go", src))
	want := []string{"cache", "counter", "mu", "a", "b"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("g4aVars: got %v want %v", got, want)
	}
	aliased := "package x\nimport e \"errors\"\nvar A = e.New(\"x\")\nvar B = other.New(\"x\")\n"
	if got := g4aVars(mustParse(t, "internal/x/y.go", aliased)); fmt.Sprint(got) != "[B]" {
		t.Errorf("aliased errors: %v", got)
	}
	for rel, in := range map[string]bool{
		"internal/a/a.go": true, "app/app.go": true, "config/c.go": true, "cmd/x/main.go": true,
		"tools/tenantgate/scan.go": false, "internal/a/a_test.go": false, "proto/x.go": false,
	} {
		if g4aInScope(rel) != in {
			t.Errorf("g4aInScope(%s) != %v", rel, in)
		}
	}
}

func TestG4bMatcher(t *testing.T) {
	src := "package x\nimport c \"context\"\nfunc f() { c.Background(); c.TODO(); _ = c.WithValue }\ntype Worker struct{}\n"
	s := mustParse(t, "internal/job/w.go", src)
	if n := bgCount(s); n != 2 {
		t.Errorf("bgCount = %d, want 2", n)
	}
	if got := workerDirs([]*srcFile{s}); fmt.Sprint(got) != "[internal/job]" {
		t.Errorf("workerDirs = %v", got)
	}
	if !g4bInScope("internal/job/z.go", []string{"internal/job"}) || g4bInScope("internal/other/z.go", []string{"internal/job"}) {
		t.Error("g4bInScope worker dirs")
	}
}
