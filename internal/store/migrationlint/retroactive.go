package migrationlint

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rubenv/sql-migrate/sqlparse"
)

type retroactiveHazard struct {
	Kind      string
	Table     string
	Statement string
}

var (
	sqlIdentifierPattern = "(?:`[^`]+`|[A-Za-z0-9_$]+)(?:\\s*\\.\\s*(?:`[^`]+`|[A-Za-z0-9_$]+))?"
	createTableStatement = regexp.MustCompile(`(?is)^\s*create\s+table\s+(?:if\s+not\s+exists\s+)?(` + sqlIdentifierPattern + `)`)
	alterTableStatement  = regexp.MustCompile(`(?is)^\s*alter\s+table\s+(` + sqlIdentifierPattern + `)\s+(.+)$`)
	addCheckClause       = regexp.MustCompile(`(?is)^\s*add\s+(?:constraint\s+(?:` + sqlIdentifierPattern + `)\s+)?check\s*\(`)
	addUniqueClause      = regexp.MustCompile(`(?is)^\s*add\s+(?:constraint\s+(?:` + sqlIdentifierPattern + `)\s+)?unique(?:\s+(?:index|key))?\b`)
	addForeignKeyClause  = regexp.MustCompile(`(?is)^\s*add\s+(?:constraint\s+(?:` + sqlIdentifierPattern + `)\s+)?foreign\s+key\b`)
	changeColumnClause   = regexp.MustCompile(`(?is)^\s*(?:modify|change)\s+(?:column\s+)?`)
	notNullClause        = regexp.MustCompile(`(?is)\bnot\s+null\b`)
	enumTypeClause       = regexp.MustCompile(`(?is)\benum\s*\(`)
)

func detectRetroactiveHazards(content string) ([]retroactiveHazard, error) {
	parsed, err := sqlparse.ParseMigration(strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("parse migration: %w", err)
	}

	createdHere := make(map[string]bool)
	cleaned := make([]string, 0, len(parsed.UpStatements))
	for _, statement := range parsed.UpStatements {
		statement = maskSQLCommentsAndStrings(statement)
		cleaned = append(cleaned, statement)
		if match := createTableStatement.FindStringSubmatch(statement); match != nil {
			createdHere[normalizeTableName(match[1])] = true
		}
	}

	var hazards []retroactiveHazard
	for _, statement := range cleaned {
		match := alterTableStatement.FindStringSubmatch(statement)
		if match == nil {
			continue
		}
		table := normalizeTableName(match[1])
		if createdHere[table] {
			continue
		}

		for _, clause := range splitTopLevelCommas(match[2]) {
			kind := ""
			switch {
			case addCheckClause.MatchString(clause):
				kind = "CHECK constraint validates every existing row"
			case addUniqueClause.MatchString(clause):
				kind = "UNIQUE constraint validates existing rows for duplicates"
			case addForeignKeyClause.MatchString(clause):
				kind = "FOREIGN KEY validates existing rows for orphans"
			case changeColumnClause.MatchString(clause) && notNullClause.MatchString(clause):
				kind = "NOT NULL conversion rejects existing NULL values"
			case changeColumnClause.MatchString(clause) && enumTypeClause.MatchString(clause):
				kind = "ENUM conversion may reject legacy values"
			}
			if kind != "" {
				hazards = append(hazards, retroactiveHazard{
					Kind:      kind,
					Table:     table,
					Statement: compactSQL(clause),
				})
			}
		}
	}
	return hazards, nil
}

func normalizeTableName(name string) string {
	parts := strings.Split(name, ".")
	for i := range parts {
		parts[i] = strings.ToLower(strings.Trim(strings.TrimSpace(parts[i]), "`"))
	}
	return strings.Join(parts, ".")
}

func compactSQL(statement string) string {
	const maxLen = 180
	statement = strings.Join(strings.Fields(statement), " ")
	if len(statement) > maxLen {
		return statement[:maxLen] + "..."
	}
	return statement
}

// splitTopLevelCommas keeps commas inside CHECK/ENUM expressions within their
// ALTER clause. Strings and comments have already been replaced with spaces.
func splitTopLevelCommas(body string) []string {
	start, depth := 0, 0
	var clauses []string
	for i, r := range body {
		switch r {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				clauses = append(clauses, body[start:i])
				start = i + 1
			}
		}
	}
	return append(clauses, body[start:])
}

// maskSQLCommentsAndStrings preserves identifiers and punctuation but replaces
// comments and quoted values with spaces, keeping regexes from seeing SQL-like
// examples in prose or dynamic SQL stored in a string.
func maskSQLCommentsAndStrings(sql string) string {
	masked := []byte(sql)
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || sql[i] == '"':
			quote := sql[i]
			masked[i] = ' '
			i++
			for i < len(sql) {
				masked[i] = ' '
				if sql[i] == '\\' && i+1 < len(sql) {
					i++
					masked[i] = ' '
					i++
					continue
				}
				if sql[i] == quote {
					if i+1 < len(sql) && sql[i+1] == quote {
						i++
						masked[i] = ' '
						i++
						continue
					}
					i++
					break
				}
				i++
			}
		case sql[i] == '#' || isDashCommentStart(sql, i):
			for i < len(sql) && sql[i] != '\n' {
				masked[i] = ' '
				i++
			}
		case i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*':
			masked[i], masked[i+1] = ' ', ' '
			i += 2
			for i < len(sql) {
				if i+1 < len(sql) && sql[i] == '*' && sql[i+1] == '/' {
					masked[i], masked[i+1] = ' ', ' '
					i += 2
					break
				}
				masked[i] = ' '
				i++
			}
		default:
			i++
		}
	}
	return string(masked)
}

func isDashCommentStart(sql string, i int) bool {
	if i+1 >= len(sql) || sql[i] != '-' || sql[i+1] != '-' {
		return false
	}
	return i+2 == len(sql) || sql[i+2] <= ' '
}

func retroactiveAllowance(content string) (bool, error) {
	const marker = "migrationlint:allow-retroactive"
	for _, comment := range sqlLineComments(content) {
		comment = strings.TrimSpace(comment)
		if !strings.HasPrefix(comment, marker) {
			continue
		}
		if len(comment) > len(marker) && comment[len(marker)] > ' ' {
			continue
		}
		reason := strings.TrimSpace(strings.TrimPrefix(comment, marker))
		if reason == "" {
			return false, fmt.Errorf("%s requires a non-empty reason", marker)
		}
		return true, nil
	}
	return false, nil
}

func sqlLineComments(sql string) []string {
	var comments []string
	for i := 0; i < len(sql); {
		switch {
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			quote := sql[i]
			i++
			for i < len(sql) {
				if sql[i] == '\\' && quote != '`' && i+1 < len(sql) {
					i += 2
					continue
				}
				if sql[i] == quote {
					if i+1 < len(sql) && sql[i+1] == quote {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case isDashCommentStart(sql, i):
			start := i + 2
			i = start
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			comments = append(comments, sql[start:i])
		case i+1 < len(sql) && sql[i] == '/' && sql[i+1] == '*':
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			if i+1 < len(sql) {
				i += 2
			}
		default:
			i++
		}
	}
	return comments
}
