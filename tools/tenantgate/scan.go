// Package tenantgate holds static gates (go/ast scanners run as tests) that keep the
// multi-tenant backend from bypassing the per-team database pool.
//
//	G2  TestG2RawDBHandles       sql.Open / sqlx.Open & co only in tenantdb, platformstore,
//	                             migrate*.go, the migration replay tool, tools/, cmd/ and tests.
//	G3  TestG3NoSchemaInSQL      no `USE x` and no `db.table` in SQL strings or team
//	                             migrations (internal/store/sql/*.sql): a team DB stays unqualified.
//	G4a TestG4PackageGlobals     ratchet on package-level vars (process-wide state);
//	                             baseline testdata/g4_globals.txt.
//	G4b TestG4BackgroundContext  ratchet on context.Background()/TODO() per file in store,
//	                             apisrv, app and worker packages; baseline testdata/g4_background.txt.
//
// G2 and G3 are hard gates with no baseline. G4 fails both when the tree grew above the
// baseline and when it shrank below it. Refresh a baseline after an intended change with
//
//	go test ./tools/tenantgate/ -run TestG4PackageGlobals -update
//	go test ./tools/tenantgate/ -run TestG4BackgroundContext -update
package tenantgate

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// srcFile is one parsed Go file of the repo.
type srcFile struct {
	Rel       string // slash-separated, relative to the repo root
	Fset      *token.FileSet
	File      *ast.File
	IsTest    bool
	Generated bool
}

// finding is one violation of a gate.
type finding struct {
	Rel  string
	Line int
	Msg  string
}

func (f finding) String() string { return fmt.Sprintf("%s:%d: %s", f.Rel, f.Line, f.Msg) }

var (
	repoOnce  sync.Once
	repoRootP string
	repoFiles []*srcFile
	repoErr   error
)

// repoRoot walks up from the working dir to the dir holding go.mod.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found above %s", dir)
		}
		dir = parent
	}
}

func skipDir(rel string, name string) bool {
	switch name {
	case ".git", "vendor", "node_modules", "testdata":
		return true
	}
	return rel == "proto/gen" || rel == "internal/dependency/mocks"
}

var generatedRe = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

func isGenerated(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() >= f.Package {
			break
		}
		for _, c := range cg.List {
			if generatedRe.MatchString(c.Text) {
				return true
			}
		}
	}
	return false
}

func parseSource(rel string, src any) (*srcFile, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, rel, src, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	return &srcFile{
		Rel:       rel,
		Fset:      fset,
		File:      f,
		IsTest:    strings.HasSuffix(rel, "_test.go"),
		Generated: isGenerated(f),
	}, nil
}

// loadRepo parses every non-skipped .go file of the repo once.
func loadRepo(t *testing.T) (string, []*srcFile) {
	t.Helper()
	repoOnce.Do(func() {
		root, err := repoRoot()
		if err != nil {
			repoErr = err
			return
		}
		repoRootP = root
		repoErr = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if rel != "." && skipDir(rel, d.Name()) {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") {
				return nil
			}
			sf, err := parseSource(rel, mustRead(p))
			if err != nil {
				return fmt.Errorf("parse %s: %w", rel, err)
			}
			repoFiles = append(repoFiles, sf)
			return nil
		})
	})
	if repoErr != nil {
		t.Fatalf("scan repo: %v", repoErr)
	}
	return repoRootP, repoFiles
}

func mustRead(p string) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		panic(err)
	}
	return b
}

func hasPrefixDir(rel, dir string) bool { return rel == dir || strings.HasPrefix(rel, dir+"/") }

func (s *srcFile) pos(p token.Pos) int { return s.Fset.Position(p).Line }

// importNames returns the local names under which the import path is visible in the file.
func (s *srcFile) importNames(importPath string) map[string]bool {
	out := map[string]bool{}
	for _, im := range s.File.Imports {
		pth, err := strconv.Unquote(im.Path.Value)
		if err != nil || pth != importPath {
			continue
		}
		switch {
		case im.Name == nil:
			out[path.Base(pth)] = true
		case im.Name.Name == "_" || im.Name.Name == ".":
		default:
			out[im.Name.Name] = true
		}
	}
	return out
}

// pkgCall reports pkg.Func for a call expression whose selector base is an identifier.
func pkgCall(n ast.Node) (pkg, fn string, ok bool) {
	call, isCall := n.(*ast.CallExpr)
	if !isCall {
		return
	}
	sel, isSel := call.Fun.(*ast.SelectorExpr)
	if !isSel {
		return
	}
	id, isID := sel.X.(*ast.Ident)
	if !isID {
		return
	}
	return id.Name, sel.Sel.Name, true
}

// ---------------------------------------------------------------- G2

var rawHandleFuncs = map[string]map[string]bool{
	"database/sql":            {"Open": true, "OpenDB": true},
	"github.com/jmoiron/sqlx": {"Open": true, "Connect": true, "NewDb": true, "MustOpen": true, "MustConnect": true, "ConnectContext": true},
}

// g2Allowed reports whether raw DB handle constructors are allowed in the file.
func g2Allowed(rel string) bool {
	switch {
	case strings.HasSuffix(rel, "_test.go"):
		return true
	case hasPrefixDir(rel, "internal/store/tenantdb"), hasPrefixDir(rel, "internal/platformstore"),
		hasPrefixDir(rel, "internal/store/migreplay"), hasPrefixDir(rel, "tools"), hasPrefixDir(rel, "cmd"):
		return true
	case path.Dir(rel) == "internal/store" && strings.HasPrefix(path.Base(rel), "migrate"):
		return true
	}
	return false
}

func g2Scan(s *srcFile) []finding {
	if g2Allowed(s.Rel) {
		return nil
	}
	byLocal := map[string][]string{} // local name -> import paths of interest
	for ip := range rawHandleFuncs {
		for local := range s.importNames(ip) {
			byLocal[local] = append(byLocal[local], ip)
		}
	}
	if len(byLocal) == 0 {
		return nil
	}
	var out []finding
	ast.Inspect(s.File, func(n ast.Node) bool {
		pkg, fn, ok := pkgCall(n)
		if !ok {
			return true
		}
		for _, ip := range byLocal[pkg] {
			if rawHandleFuncs[ip][fn] {
				out = append(out, finding{s.Rel, s.pos(n.Pos()), fmt.Sprintf(
					"raw DB handle %s.%s outside tenantdb/platformstore/migrate*.go — go through tenantdb.Open", path.Base(ip), fn)})
			}
		}
		return true
	})
	return out
}

// ---------------------------------------------------------------- G3

var (
	sqlLikeRe = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|REPLACE|TRUNCATE|USE)\b`)
	wsRe      = regexp.MustCompile(`\s+`)
	identRe   = "(?:`[^`]+`|[A-Za-z_][A-Za-z0-9_$]*)"
	useStmtRe = regexp.MustCompile("(?i)(?:^|;)\\s*USE\\s+" + identRe + "\\s*(?:;|$)")
	qualRe    = regexp.MustCompile("(?i)\\b(?:DELETE\\s+FROM|FROM|JOIN|INTO|UPDATE|TABLE(?:\\s+IF\\s+(?:NOT\\s+)?EXISTS)?|REFERENCES)\\s+(" +
		identRe + ")\\s*\\.\\s*(" + identRe + ")")
	nestedStrRe = regexp.MustCompile(`''[^']+''`)
	wsOnlyRe    = regexp.MustCompile(`[^\n]`)
	dupKeyRe    = regexp.MustCompile(`(?i)\bON\s+DUPLICATE\s+KEY\s+UPDATE\b`)

	systemSchemas = map[string]bool{"information_schema": true, "performance_schema": true, "mysql": true, "sys": true}
)

func unquoteIdent(s string) string { return strings.Trim(s, "`") }

// sqlViolations returns the violations found in one SQL text (statements separated by `;`).
func sqlViolations(sql string) []string {
	sql = strings.TrimSpace(wsRe.ReplaceAllString(sql, " "))
	var out []string
	if m := useStmtRe.FindString(sql); m != "" {
		out = append(out, "USE statement")
	}
	flat := dupKeyRe.ReplaceAllString(sql, "ON DUPLICATE KEY SET")
	for _, m := range qualRe.FindAllStringSubmatch(flat, -1) {
		schema := strings.ToLower(unquoteIdent(m[1]))
		if systemSchemas[schema] {
			continue
		}
		out = append(out, fmt.Sprintf("schema-qualified table %q", unquoteIdent(m[1])+"."+unquoteIdent(m[2])))
	}
	return out
}

// looksLikeSQL is the cheap pre-filter for Go string literals.
func looksLikeSQL(s string) bool { return sqlLikeRe.MatchString(s) }

func g3Message(v string) string {
	return v + " in SQL — a team DB must stay unqualified"
}

// g3InScope reports whether non-test Go files at rel are scanned by G3.
func g3InScope(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") || hasPrefixDir(rel, "internal/store/tenantdb") || hasPrefixDir(rel, "tools") {
		return false
	}
	return hasPrefixDir(rel, "internal") || hasPrefixDir(rel, "app") || hasPrefixDir(rel, "cmd")
}

func g3ScanGo(s *srcFile) []finding {
	if !g3InScope(s.Rel) || s.Generated {
		return nil
	}
	var out []finding
	ast.Inspect(s.File, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		str, err := strconv.Unquote(lit.Value)
		if err != nil || !looksLikeSQL(str) {
			return true
		}
		for _, v := range sqlViolations(str) {
			out = append(out, finding{s.Rel, s.pos(lit.Pos()), g3Message(v)})
		}
		return true
	})
	return out
}

// stripSQLComments blanks `--` line comments and /* */ comments, keeping newlines and
// leaving quoted strings and identifiers untouched.
func stripSQLComments(src string) string {
	b := []byte(src)
	out := make([]byte, 0, len(b))
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := i + 1
			for j < len(b) && b[j] != c {
				if b[j] == '\\' && c != '`' {
					j++
				}
				j++
			}
			if j >= len(b) {
				j = len(b) - 1
			}
			out = append(out, b[i:j+1]...)
			i = j + 1
		case c == '-' && i+1 < len(b) && b[i+1] == '-':
			for i < len(b) && b[i] != '\n' {
				out = append(out, ' ')
				i++
			}
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			j := i + 2
			for j+1 < len(b) && !(b[j] == '*' && b[j+1] == '/') {
				j++
			}
			end := min(j+2, len(b))
			for _, x := range b[i:end] {
				if x == '\n' {
					out = append(out, '\n')
				} else {
					out = append(out, ' ')
				}
			}
			i = end
		default:
			out = append(out, c)
			i++
		}
	}
	return string(out)
}

// g3ScanSQLFile scans a migration file; lines are those of the original file.
func g3ScanSQLFile(rel, src string) []finding {
	// Blank text of strings nested in dynamic DDL ('...COMMENT ''x from a.b''...'), keeping newlines.
	clean := nestedStrRe.ReplaceAllStringFunc(stripSQLComments(src), func(m string) string {
		return wsOnlyRe.ReplaceAllString(m, " ")
	})
	var out []finding
	line := 1
	for _, stmt := range strings.SplitAfter(clean, ";") {
		lead := len(stmt) - len(strings.TrimLeft(stmt, " \t\r\n"))
		stmtLine := line + strings.Count(stmt[:lead], "\n")
		for _, v := range sqlViolations(stmt) {
			l := stmtLine
			if i := strings.Index(v, `"`); i >= 0 { // refine to the line holding the qualified name
				needle := strings.ToLower(strings.Trim(v[i:], `"`))
				for k, ln := range strings.Split(stmt, "\n") {
					if strings.Contains(strings.ToLower(strings.ReplaceAll(ln, "`", "")), needle) {
						l = line + k
						break
					}
				}
			}
			out = append(out, finding{rel, l, g3Message(v)})
		}
		line += strings.Count(stmt, "\n")
	}
	return out
}

// ---------------------------------------------------------------- G4a

func g4aInScope(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") || hasPrefixDir(rel, "tools") {
		return false
	}
	return hasPrefixDir(rel, "internal") || hasPrefixDir(rel, "app") || hasPrefixDir(rel, "config") || hasPrefixDir(rel, "cmd")
}

// exemptInit lists importPath -> funcs whose result is not mutable process state.
var exemptInit = map[string]map[string]bool{
	"errors":        {"New": true},
	"fmt":           {"Errorf": true},
	"regexp":        {"MustCompile": true, "MustCompilePOSIX": true},
	"html/template": {"Must": true},
	"text/template": {"Must": true},
}

func (s *srcFile) isExemptInit(e ast.Expr) bool {
	pkg, fn, ok := pkgCall(e)
	if !ok {
		return false
	}
	for ip, fns := range exemptInit {
		if fns[fn] && s.importNames(ip)[pkg] {
			return true
		}
	}
	return false
}

func hasEmbed(cgs ...*ast.CommentGroup) bool {
	for _, cg := range cgs {
		if cg == nil {
			continue
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:embed") {
				return true
			}
		}
	}
	return false
}

// g4aVars returns the counted package-level var names of one file.
func g4aVars(s *srcFile) []string {
	var out []string
	for _, d := range s.File.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, sp := range gd.Specs {
			vs := sp.(*ast.ValueSpec)
			if hasEmbed(vs.Doc, gd.Doc) {
				continue
			}
			for i, name := range vs.Names {
				if name.Name == "_" {
					continue
				}
				var init ast.Expr
				switch {
				case len(vs.Values) == len(vs.Names):
					init = vs.Values[i]
				case len(vs.Values) == 1:
					init = vs.Values[0]
				}
				if init != nil && s.isExemptInit(init) {
					continue
				}
				out = append(out, name.Name)
			}
		}
	}
	return out
}

// ---------------------------------------------------------------- G4b

func bgCount(s *srcFile) int {
	names := s.importNames("context")
	if len(names) == 0 {
		return 0
	}
	n := 0
	ast.Inspect(s.File, func(x ast.Node) bool {
		if pkg, fn, ok := pkgCall(x); ok && names[pkg] && (fn == "Background" || fn == "TODO") {
			n++
		}
		return true
	})
	return n
}

// workerDirs returns the directories under internal/ whose non-test files declare a
// type named Worker or Sweeper.
func workerDirs(files []*srcFile) []string {
	set := map[string]bool{}
	for _, s := range files {
		if s.IsTest || s.Generated || !hasPrefixDir(s.Rel, "internal") {
			continue
		}
		for _, d := range s.File.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, sp := range gd.Specs {
				if n := sp.(*ast.TypeSpec).Name.Name; n == "Worker" || n == "Sweeper" {
					set[path.Dir(s.Rel)] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

func g4bInScope(rel string, workers []string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	for _, d := range []string{"internal/store", "internal/apisrv", "app"} {
		if hasPrefixDir(rel, d) {
			return true
		}
	}
	dir := path.Dir(rel)
	for _, w := range workers {
		if dir == w {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- baselines

// readBaseline returns the non-comment, non-empty lines of a baseline file.
func readBaseline(file string) ([]string, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(ln) == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		out = append(out, ln)
	}
	return out, nil
}

func writeBaseline(file, header string, lines []string) error {
	sort.Strings(lines)
	var sb strings.Builder
	for _, h := range strings.Split(strings.TrimSpace(header), "\n") {
		sb.WriteString("# " + h + "\n")
	}
	for _, l := range lines {
		sb.WriteString(l + "\n")
	}
	return os.WriteFile(file, []byte(sb.String()), 0o644)
}
