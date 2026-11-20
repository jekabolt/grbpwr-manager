package migrationlint

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/schemacompat"
	"github.com/rubenv/sql-migrate/sqlparse"
)

// X3 (tmp/plans/teams/lanes/X3-design.md, internal/store/sql/MIGRATIONS.md): a team migration is
// additive by default, so code one release back survives it. A migration that breaks older code
// must say so, and must not ship in the release whose code still needs the old shape.

// compatEnforceAfter: migrations up to this number predate the rule.
const compatEnforceAfter = 485

// nameStillUsedMarker lets a breaking migration drop a name that Go code still writes for an
// unrelated reason (another table's column of the same name): "-- migrationlint:name-still-used <name> <reason>".
const nameStillUsedMarker = "migrationlint:name-still-used"

type compatProblem struct {
	Migration string
	Problem   string
}

// destructiveChange is one DROP / RENAME / CHANGE and the old name it removes.
type destructiveChange struct {
	What, Name string
}

// detectCompatProblems checks declarations and destructive statements of migrations numbered
// above enforceAfter. It returns, per breaking migration, the names its statements remove.
func detectCompatProblems(migrations []namedMigration, enforceAfter int) ([]compatProblem, map[string][]destructiveChange, error) {
	sort.Slice(migrations, func(i, j int) bool { return schemacompat.Compare(migrations[i].Name, migrations[j].Name) < 0 })
	var problems []compatProblem
	removed := map[string][]destructiveChange{}
	for _, m := range migrations {
		if migrationNumber(m.Name) <= enforceAfter {
			continue
		}
		header, err := schemacompat.ParseHeader(m.Content)
		if err != nil {
			problems = append(problems, compatProblem{m.Name, err.Error()})
			continue
		}
		if schemacompat.IsBreaking(m.Name) != header.Breaking {
			problems = append(problems, compatProblem{m.Name, fmt.Sprintf("the file name marker %q and the \"-- compat: breaking\" line must come together", schemacompat.BreakingMarker)})
			continue
		}
		if header.Breaking && header.Lazy {
			problems = append(problems, compatProblem{m.Name, "breaking and lazy at once"})
			continue
		}
		changes, err := destructiveChanges(m.Content)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", m.Name, err)
		}
		if len(changes) == 0 {
			continue
		}
		if !header.Breaking {
			for _, c := range changes {
				problems = append(problems, compatProblem{m.Name, fmt.Sprintf("%s %s breaks the code one release back: declare it breaking in its own release (MIGRATIONS.md)", c.What, c.Name)})
			}
			continue
		}
		removed[m.Name] = changes
	}
	return problems, removed, nil
}

func destructiveChanges(content string) ([]destructiveChange, error) {
	parsed, err := sqlparse.ParseMigration(strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	var out []destructiveChange
	for _, statement := range parsed.UpStatements {
		found, err := statementChanges(statement)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
		dynamic, err := dynamicDDLStrings(statement)
		if err != nil {
			return nil, err
		}
		for _, ddl := range dynamic {
			found, err := statementChanges(ddl)
			if err != nil {
				return nil, err
			}
			out = append(out, found...)
		}
	}
	return out, nil
}

func statementChanges(statement string) ([]destructiveChange, error) {
	tokens, err := tokenizeSQL(statement)
	if err != nil || len(tokens) < 3 {
		return nil, err
	}
	switch {
	case tokenIs(tokens[0], "drop") && tokenIs(tokens[1], "table"):
		name, _ := objectName(tokens, 2)
		return []destructiveChange{{"DROP TABLE", name}}, nil
	case tokenIs(tokens[0], "rename") && tokenIs(tokens[1], "table"):
		name, _ := objectName(tokens, 2)
		return []destructiveChange{{"RENAME TABLE", name}}, nil
	case tokenIs(tokens[0], "alter") && tokenIs(tokens[1], "table"):
		table, bodyStart := objectName(tokens, 2)
		if table == "" || bodyStart >= len(tokens) {
			return nil, nil
		}
		var out []destructiveChange
		for _, clause := range splitTokenClauses(tokens[bodyStart:]) {
			if c, ok := alterClauseChange(table, clause); ok {
				out = append(out, c)
			}
		}
		return out, nil
	}
	return nil, nil
}

func alterClauseChange(table string, clause []sqlToken) (destructiveChange, bool) {
	if len(clause) < 2 {
		return destructiveChange{}, false
	}
	next := 1
	switch {
	case tokenIs(clause[0], "drop"):
		for _, w := range []string{"index", "key", "check", "foreign", "primary", "constraint", "default"} {
			if tokenIs(clause[1], w) {
				return destructiveChange{}, false
			}
		}
		if tokenIs(clause[1], "column") {
			next++
		}
		if name, _ := objectName(clause, next); name != "" {
			return destructiveChange{"DROP COLUMN " + table + ".", name}, true
		}
	case tokenIs(clause[0], "rename"):
		if tokenIs(clause[1], "index") || tokenIs(clause[1], "key") {
			return destructiveChange{}, false
		}
		if tokenIs(clause[1], "to") || tokenIs(clause[1], "as") {
			return destructiveChange{"RENAME TABLE", table}, true
		}
		if tokenIs(clause[1], "column") {
			next++
		}
		if name, _ := objectName(clause, next); name != "" {
			return destructiveChange{"RENAME COLUMN " + table + ".", name}, true
		}
	case tokenIs(clause[0], "change"):
		if tokenIs(clause[1], "column") {
			next++
		}
		if name, _ := objectName(clause, next); name != "" {
			return destructiveChange{"CHANGE COLUMN " + table + ".", name}, true
		}
	}
	return destructiveChange{}, false
}

// stillUsedAllowed lists the names a migration says Go code writes for another reason.
func stillUsedAllowed(content string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, comment := range sqlLineComments(content) {
		comment = strings.TrimSpace(comment)
		if !strings.HasPrefix(comment, nameStillUsedMarker+" ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(comment, nameStillUsedMarker))
		if len(fields) < 2 {
			return nil, fmt.Errorf("%s needs a name and a reason", nameStillUsedMarker)
		}
		out[strings.ToLower(fields[0])] = true
	}
	return out, nil
}

// goStringUse is a Go string literal in non-test code that still names a removed identifier.
type goStringUse struct {
	Name, Pos string
}

// namesInGoStrings finds the given names (whole words, any case) inside string literals of the
// non-test Go files under roots: SQL in this codebase is written in string literals, so a name
// still there is code that still needs the old shape.
func namesInGoStrings(roots []string, names map[string]bool) ([]goStringUse, error) {
	if len(names) == 0 {
		return nil, nil
	}
	parts := make([]string, 0, len(names))
	for n := range names {
		parts = append(parts, regexp.QuoteMeta(n))
	}
	sort.Strings(parts)
	re := regexp.MustCompile(`(?i)(^|[^a-z0-9_])(` + strings.Join(parts, "|") + `)($|[^a-z0-9_])`)
	fset := token.NewFileSet()
	var uses []goStringUse
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if strings.HasSuffix(filepath.ToSlash(path), "store/migrationlint") || d.Name() == "mocks" || d.Name() == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
			if err != nil {
				return fmt.Errorf("parse %s: %w", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				for _, m := range re.FindAllStringSubmatch(value, -1) {
					uses = append(uses, goStringUse{Name: strings.ToLower(m[2]), Pos: fset.Position(lit.Pos()).String()})
				}
				return true
			})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return uses, nil
}

// breakingStillNeeded reports names a breaking migration removes that Go code still writes.
func breakingStillNeeded(migrations []namedMigration, removed map[string][]destructiveChange, roots []string) ([]compatProblem, error) {
	byName := map[string][]string{} // removed name → migrations
	for _, m := range migrations {
		changes := removed[m.Name]
		if len(changes) == 0 {
			continue
		}
		allowed, err := stillUsedAllowed(m.Content)
		if err != nil {
			return []compatProblem{{m.Name, err.Error()}}, nil
		}
		for _, c := range changes {
			if !allowed[c.Name] {
				byName[c.Name] = append(byName[c.Name], m.Name)
			}
		}
	}
	names := map[string]bool{}
	for n := range byName {
		names[n] = true
	}
	uses, err := namesInGoStrings(roots, names)
	if err != nil {
		return nil, err
	}
	var problems []compatProblem
	for _, u := range uses {
		for _, m := range byName[u.Name] {
			problems = append(problems, compatProblem{m, fmt.Sprintf("%s is removed but Go code still names it at %s: stop using it one release earlier", u.Name, u.Pos)})
		}
	}
	return problems, nil
}
