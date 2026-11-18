package seed

import (
	_ "embed"
	"strings"
)

//go:embed scrub.sql
var scrubSQL string

// Statements returns the executable statements from scrub.sql.
func Statements() []string {
	return splitStatements(scrubSQL)
}

func splitStatements(sql string) []string {
	var statements []string
	var statement strings.Builder
	var quote byte

	for i := 0; i < len(sql); {
		if quote != 0 {
			c := sql[i]
			statement.WriteByte(c)
			i++
			if c == '\\' && quote != '`' && i < len(sql) {
				statement.WriteByte(sql[i])
				i++
				continue
			}
			if c == quote {
				if i < len(sql) && sql[i] == quote {
					statement.WriteByte(sql[i])
					i++
					continue
				}
				quote = 0
			}
			continue
		}

		switch {
		case sql[i] == '\'' || sql[i] == '"' || sql[i] == '`':
			quote = sql[i]
			statement.WriteByte(sql[i])
			i++
		case sql[i] == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLine(sql, i+2)
			statement.WriteByte('\n')
		case sql[i] == '#':
			i = skipLine(sql, i+1)
			statement.WriteByte('\n')
		case sql[i] == '/' && i+1 < len(sql) && sql[i+1] == '*':
			i = skipBlockComment(sql, i+2)
			statement.WriteByte(' ')
		case sql[i] == ';':
			if s := strings.TrimSpace(statement.String()); s != "" {
				statements = append(statements, s)
			}
			statement.Reset()
			i++
		default:
			statement.WriteByte(sql[i])
			i++
		}
	}

	if s := strings.TrimSpace(statement.String()); s != "" {
		statements = append(statements, s)
	}
	return statements
}

func skipLine(sql string, i int) int {
	for i < len(sql) && sql[i] != '\n' {
		i++
	}
	return i
}

func skipBlockComment(sql string, i int) int {
	for i+1 < len(sql) {
		if sql[i] == '*' && sql[i+1] == '/' {
			return i + 2
		}
		i++
	}
	return len(sql)
}
