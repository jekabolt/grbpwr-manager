package migrationlint

import (
	"fmt"
	"strings"
)

type sqlTokenKind uint8

const (
	sqlWord sqlTokenKind = iota
	sqlString
	sqlSymbol
)

type sqlToken struct {
	Kind sqlTokenKind
	Text string
}

// tokenizeSQL is deliberately small, but lexical rather than regexp based: it
// removes comments, decodes quoted values, and keeps punctuation/parentheses so
// DDL clauses can be split without mistaking examples in comments for SQL.
func tokenizeSQL(input string) ([]sqlToken, error) {
	var tokens []sqlToken
	for i := 0; i < len(input); {
		switch {
		case isSQLSpace(input[i]):
			i++
		case input[i] == '#' || isDashCommentStart(input, i):
			for i < len(input) && input[i] != '\n' {
				i++
			}
		case i+1 < len(input) && input[i] == '/' && input[i+1] == '*':
			i += 2
			for i+1 < len(input) && !(input[i] == '*' && input[i+1] == '/') {
				i++
			}
			if i+1 >= len(input) {
				return nil, fmt.Errorf("unterminated SQL block comment")
			}
			i += 2
		case input[i] == '\'' || input[i] == '"':
			quote := input[i]
			i++
			var value strings.Builder
			closed := false
			for i < len(input) {
				switch {
				case input[i] == '\\' && i+1 < len(input):
					i++
					value.WriteByte(input[i])
					i++
				case input[i] == quote && i+1 < len(input) && input[i+1] == quote:
					value.WriteByte(quote)
					i += 2
				case input[i] == quote:
					i++
					closed = true
				default:
					value.WriteByte(input[i])
					i++
				}
				if closed {
					break
				}
			}
			if !closed {
				return nil, fmt.Errorf("unterminated SQL string")
			}
			tokens = append(tokens, sqlToken{Kind: sqlString, Text: value.String()})
		case input[i] == '`':
			i++
			var value strings.Builder
			closed := false
			for i < len(input) {
				if input[i] == '`' {
					if i+1 < len(input) && input[i+1] == '`' {
						value.WriteByte('`')
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				value.WriteByte(input[i])
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated quoted identifier")
			}
			tokens = append(tokens, sqlToken{Kind: sqlWord, Text: value.String()})
		case isSQLWordByte(input[i]):
			start := i
			for i < len(input) && isSQLWordByte(input[i]) {
				i++
			}
			tokens = append(tokens, sqlToken{Kind: sqlWord, Text: input[start:i]})
		default:
			tokens = append(tokens, sqlToken{Kind: sqlSymbol, Text: input[i : i+1]})
			i++
		}
	}
	return tokens, nil
}

func isSQLSpace(b byte) bool {
	return b == ' ' || b == '\t' || b == '\r' || b == '\n' || b == '\f'
}

func isSQLWordByte(b byte) bool {
	return b == '_' || b == '$' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= utf8RuneSelf
}

// utf8RuneSelf mirrors utf8.RuneSelf without importing the whole package. SQL
// identifiers in this repository are ASCII; treating non-ASCII bytes as word
// bytes merely keeps token boundaries stable in comments/metadata.
const utf8RuneSelf = 0x80

func tokenIs(token sqlToken, word string) bool {
	return token.Kind == sqlWord && strings.EqualFold(token.Text, word)
}

func dynamicDDLStrings(statement string) ([]string, error) {
	tokens, err := tokenizeSQL(statement)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 || !tokenIs(tokens[0], "set") && !tokenIs(tokens[0], "prepare") {
		return nil, nil
	}
	var ddl []string
	for _, token := range tokens {
		if token.Kind != sqlString {
			continue
		}
		trimmed := strings.TrimSpace(token.Text)
		first := trimmed
		if at := strings.IndexAny(first, " \t\r\n("); at >= 0 {
			first = first[:at]
		}
		switch strings.ToLower(first) {
		case "alter", "create", "drop", "rename":
			// CONCAT-based dynamic SQL often stores only a fragment such as
			// "ALTER ... DROP CHECK `" in a literal and appends an identifier.
			// There is no complete DDL statement for the static rules to parse.
			if _, err := tokenizeSQL(trimmed); err != nil {
				continue
			}
			ddl = append(ddl, trimmed)
		}
	}
	return ddl, nil
}
