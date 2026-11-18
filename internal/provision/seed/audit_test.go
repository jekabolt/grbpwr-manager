package seed

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"unicode"
)

func TestAuditMigrationInsertTargets(t *testing.T) {
	files, err := filepath.Glob("../../store/sql/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migrations found")
	}

	targetFiles := make(map[string]map[string]struct{})
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for table := range insertTargets(upSection(string(data))) {
			if targetFiles[table] == nil {
				targetFiles[table] = make(map[string]struct{})
			}
			targetFiles[table][filepath.Base(file)] = struct{}{}
		}
	}

	gotClasses := Classes()
	var missing []string
	for table, names := range targetFiles {
		if _, ok := gotClasses[table]; ok {
			continue
		}
		for name := range names {
			missing = append(missing, fmt.Sprintf("%s: %s", name, table))
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("unclassified migration INSERT targets:\n%s", strings.Join(missing, "\n"))
	}

	var stale []string
	for table, class := range gotClasses {
		if class != Keep && class != Scrub && class != Derived {
			t.Errorf("%s has invalid class %d", table, class)
		}
		if _, ok := targetFiles[table]; !ok {
			stale = append(stale, table)
		}
	}
	sort.Strings(stale)
	if len(stale) != 0 {
		t.Errorf("stale classifications with no migration INSERT target:\n%s", strings.Join(stale, "\n"))
	}
}

func TestAuditScrubSQL(t *testing.T) {
	gotClasses := Classes()
	deleted := make(map[string]struct{})

	for n, statement := range Statements() {
		if strings.Contains(statement, `"`) {
			t.Errorf("statement %d contains a double-quoted string or identifier", n+1)
		}

		tokens := sqlTokens(statement)
		for _, token := range tokens {
			switch strings.ToUpper(token) {
			case "USE", "DROP", "ALTER", "TRUNCATE":
				t.Errorf("statement %d contains forbidden %s", n+1, strings.ToUpper(token))
			}
		}

		kind, table, qualified := mutationTarget(tokens)
		if qualified {
			t.Errorf("statement %d uses schema-qualified target %s", n+1, table)
		}
		class, known := gotClasses[table]
		if !known {
			t.Errorf("statement %d targets unclassified table %q", n+1, table)
			continue
		}

		switch kind {
		case "DELETE":
			if class != Scrub {
				t.Errorf("statement %d DELETEs %s table %s", n+1, className(class), table)
			}
			deleted[table] = struct{}{}
		case "UPDATE":
			_, allowed := scrubUpdates()[table]
			if class != Keep || !allowed {
				t.Errorf("statement %d UPDATEs %s table %s without scrubUpdates permission", n+1, className(class), table)
			}
		default:
			t.Errorf("statement %d is not a DELETE or UPDATE", n+1)
		}
	}

	var missing []string
	for table, class := range gotClasses {
		if class == Scrub {
			if _, ok := deleted[table]; !ok {
				missing = append(missing, table)
			}
		}
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("Scrub tables without DELETE:\n%s", strings.Join(missing, "\n"))
	}
}

func TestAuditStatements(t *testing.T) {
	input := `
-- leading comment; ignored
DELETE FROM first_table WHERE note = 'a; -- still text'; # trailing comment;
/* block; comment */
UPDATE second_table SET note = 'it''s; intact', marker = ` + "`semi;colon`" + `;
-- final comment
`
	got := splitStatements(input)
	if len(got) != 2 {
		t.Fatalf("got %d statements, want 2: %#v", len(got), got)
	}
	if !strings.Contains(got[0], "'a; -- still text'") {
		t.Errorf("single-quoted semicolon/comment was changed: %q", got[0])
	}
	if !strings.Contains(got[1], "'it''s; intact'") || !strings.Contains(got[1], "`semi;colon`") {
		t.Errorf("quoted semicolon was changed: %q", got[1])
	}
	for _, statement := range got {
		if strings.Contains(statement, "leading comment") || strings.Contains(statement, "block;") || strings.Contains(statement, "trailing comment") {
			t.Errorf("comment survived splitting: %q", statement)
		}
	}
}

func upSection(sql string) string {
	const upMarker = "-- +migrate Up"
	const downMarker = "-- +migrate Down"
	start := strings.Index(sql, upMarker)
	if start < 0 {
		return ""
	}
	up := sql[start+len(upMarker):]
	if end := strings.Index(up, downMarker); end >= 0 {
		up = up[:end]
	}
	return up
}

func insertTargets(sql string) map[string]struct{} {
	targets := make(map[string]struct{})
	for _, statement := range splitStatements(sql) {
		collectInsertTargets(sqlTokens(statement), targets)

		upper := strings.ToUpper(strings.TrimSpace(statement))
		if strings.HasPrefix(upper, "SET @") || strings.HasPrefix(upper, "PREPARE ") {
			for _, dynamicSQL := range singleQuotedStrings(statement) {
				collectInsertTargets(sqlTokens(dynamicSQL), targets)
			}
		}
	}
	return targets
}

func collectInsertTargets(tokens []string, targets map[string]struct{}) {
	for i := 0; i < len(tokens); i++ {
		keyword := strings.ToUpper(tokens[i])
		if keyword != "INSERT" && keyword != "REPLACE" {
			continue
		}
		j := i + 1
		if keyword == "INSERT" && j < len(tokens) && strings.EqualFold(tokens[j], "IGNORE") {
			j++
		}
		if j < len(tokens) && strings.EqualFold(tokens[j], "INTO") {
			j++
		}
		if j >= len(tokens) || !isIdentifier(tokens[j]) {
			continue
		}
		if j+1 < len(tokens) && tokens[j+1] == "." {
			j += 2
			if j >= len(tokens) || !isIdentifier(tokens[j]) {
				continue
			}
		}
		targets[strings.ToLower(tokens[j])] = struct{}{}
	}
}

func mutationTarget(tokens []string) (kind, table string, qualified bool) {
	if len(tokens) == 0 {
		return "", "", false
	}
	kind = strings.ToUpper(tokens[0])
	i := 1
	if kind == "DELETE" && i < len(tokens) && strings.EqualFold(tokens[i], "FROM") {
		i++
	}
	if (kind != "DELETE" && kind != "UPDATE") || i >= len(tokens) || !isIdentifier(tokens[i]) {
		return kind, "", false
	}
	table = strings.ToLower(tokens[i])
	if i+2 < len(tokens) && tokens[i+1] == "." && isIdentifier(tokens[i+2]) {
		return kind, strings.ToLower(tokens[i+2]), true
	}
	return kind, table, false
}

func sqlTokens(sql string) []string {
	var tokens []string
	for i := 0; i < len(sql); {
		switch {
		case unicode.IsSpace(rune(sql[i])):
			i++
		case sql[i] == '\'' || sql[i] == '"':
			i = skipQuoted(sql, i, sql[i])
		case sql[i] == '`':
			start := i + 1
			i++
			var identifier strings.Builder
			for i < len(sql) {
				if sql[i] == '`' {
					if i+1 < len(sql) && sql[i+1] == '`' {
						identifier.WriteString(sql[start:i])
						identifier.WriteByte('`')
						i += 2
						start = i
						continue
					}
					identifier.WriteString(sql[start:i])
					i++
					break
				}
				i++
			}
			tokens = append(tokens, identifier.String())
		case isWordStart(sql[i]):
			start := i
			i++
			for i < len(sql) && isWordPart(sql[i]) {
				i++
			}
			tokens = append(tokens, sql[start:i])
		default:
			tokens = append(tokens, sql[i:i+1])
			i++
		}
	}
	return tokens
}

func singleQuotedStrings(sql string) []string {
	var stringsFound []string
	for i := 0; i < len(sql); {
		if sql[i] != '\'' {
			i++
			continue
		}
		i++
		var value strings.Builder
		for i < len(sql) {
			switch {
			case sql[i] == '\\' && i+1 < len(sql):
				value.WriteByte(sql[i+1])
				i += 2
			case sql[i] == '\'' && i+1 < len(sql) && sql[i+1] == '\'':
				value.WriteByte('\'')
				i += 2
			case sql[i] == '\'':
				i++
				stringsFound = append(stringsFound, value.String())
				goto next
			default:
				value.WriteByte(sql[i])
				i++
			}
		}
	next:
	}
	return stringsFound
}

func skipQuoted(sql string, i int, quote byte) int {
	i++
	for i < len(sql) {
		if sql[i] == '\\' {
			i += 2
			continue
		}
		if sql[i] == quote {
			if i+1 < len(sql) && sql[i+1] == quote {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return i
}

func isIdentifier(token string) bool {
	if token == "" || !isWordStart(token[0]) {
		return false
	}
	for i := 1; i < len(token); i++ {
		if !isWordPart(token[i]) {
			return false
		}
	}
	return true
}

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isWordPart(c byte) bool {
	return isWordStart(c) || c >= '0' && c <= '9' || c == '$'
}

func className(class Class) string {
	switch class {
	case Keep:
		return "Keep"
	case Scrub:
		return "Scrub"
	case Derived:
		return "Derived"
	default:
		return fmt.Sprintf("Class(%d)", class)
	}
}
