package migrationlint

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/rubenv/sql-migrate/sqlparse"
)

const maxInnoDBIndexBytes = 3072

type namedMigration struct {
	Name    string
	Content string
}

type schemaHazard struct {
	Kind      string
	Table     string
	Statement string
}

type schemaColumn struct {
	Name      string
	DataType  string
	Length    int
	Precision int
	Scale     int
	Charset   string
}

type schemaCheck struct {
	Name       string
	References map[string]bool
}

type schemaTable struct {
	Charset  string
	Columns  map[string]schemaColumn
	Checks   map[string]schemaCheck
	nextAnon int
}

type schemaModel struct {
	Tables map[string]*schemaTable
}

func detectSchemaHazards(migrations []namedMigration, enforceAfter int) ([]schemaHazard, error) {
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Name < migrations[j].Name })
	model := schemaModel{Tables: make(map[string]*schemaTable)}
	var hazards []schemaHazard
	for _, migration := range migrations {
		parsed, err := sqlparse.ParseMigration(strings.NewReader(migration.Content))
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", migration.Name, err)
		}
		number := migrationNumber(migration.Name)
		enforce := number > enforceAfter
		allowDrop, err := structuredAllowance(migration.Content, "migrationlint:allow-drop-column-check")
		if err != nil {
			return nil, fmt.Errorf("%s: %w", migration.Name, err)
		}
		for _, statement := range parsed.UpStatements {
			found, err := model.apply(statement, enforce, allowDrop)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", migration.Name, err)
			}
			hazards = append(hazards, found...)
			dynamic, err := dynamicDDLStrings(statement)
			if err != nil {
				return nil, fmt.Errorf("%s dynamic DDL: %w", migration.Name, err)
			}
			for _, ddl := range dynamic {
				found, err := model.apply(ddl, enforce, allowDrop)
				if err != nil {
					return nil, fmt.Errorf("%s dynamic DDL: %w", migration.Name, err)
				}
				hazards = append(hazards, found...)
			}
		}
	}
	return hazards, nil
}

func migrationNumber(name string) int {
	match := migrationPrefixRe.FindStringSubmatch(name)
	if match == nil {
		return 0
	}
	number, _ := strconv.Atoi(match[1])
	return number
}

func structuredAllowance(content, marker string) (bool, error) {
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

func (model *schemaModel) apply(statement string, enforce, allowDrop bool) ([]schemaHazard, error) {
	tokens, err := tokenizeSQL(statement)
	if err != nil {
		return nil, err
	}
	if len(tokens) < 2 {
		return nil, nil
	}
	switch {
	case tokenIs(tokens[0], "create") && containsToken(tokens[:minInt(len(tokens), 5)], "table"):
		return model.applyCreateTable(tokens, statement, enforce)
	case tokenIs(tokens[0], "create") && containsToken(tokens[:minInt(len(tokens), 5)], "index"):
		return model.applyCreateIndex(tokens, statement, enforce)
	case tokenIs(tokens[0], "create") && containsToken(tokens[:minInt(len(tokens), 5)], "unique"):
		return model.applyCreateIndex(tokens, statement, enforce)
	case tokenIs(tokens[0], "alter") && tokenIs(tokens[1], "table"):
		return model.applyAlterTable(tokens, statement, enforce, allowDrop)
	case tokenIs(tokens[0], "drop") && tokenIs(tokens[1], "table"):
		name, _ := objectName(tokens, 2)
		delete(model.Tables, name)
	}
	return nil, nil
}

func (model *schemaModel) applyCreateTable(tokens []sqlToken, statement string, enforce bool) ([]schemaHazard, error) {
	i := 2
	if i < len(tokens) && tokenIs(tokens[i], "if") {
		i += 3 // IF NOT EXISTS
	}
	name, i := objectName(tokens, i)
	if name == "" || i >= len(tokens) || tokens[i].Text != "(" {
		return nil, nil
	}
	close := matchingParen(tokens, i)
	if close < 0 {
		return nil, fmt.Errorf("CREATE TABLE %s has unbalanced parentheses", name)
	}
	table := &schemaTable{Charset: tableCharset(tokens[close+1:]), Columns: make(map[string]schemaColumn), Checks: make(map[string]schemaCheck)}
	if table.Charset == "" {
		// Conservative server default: a key which only fits utf8mb3 must also be
		// rejected by the static lint before the utf8mb4 matrix job reaches MySQL.
		table.Charset = "utf8mb4"
	}
	clauses := splitTokenClauses(tokens[i+1 : close])
	for _, clause := range clauses {
		if column, ok := parseColumnDefinition(clause); ok {
			table.Columns[column.Name] = column
		}
	}
	for _, clause := range clauses {
		if check, ok := table.parseCheck(clause); ok {
			table.Checks[check.Name] = check
		}
	}
	model.Tables[name] = table

	var hazards []schemaHazard
	for _, clause := range clauses {
		if columns, label, ok := parseIndexClause(clause); ok {
			hazards = append(hazards, oversizedIndexHazard(table, name, label, columns, statement, enforce)...)
			continue
		}
		if column, ok := parseColumnDefinition(clause); ok && (containsToken(clause, "primary") || containsToken(clause, "unique")) {
			hazards = append(hazards, oversizedIndexHazard(table, name, "inline key", []indexColumn{{Name: column.Name}}, statement, enforce)...)
		}
	}
	return hazards, nil
}

func (model *schemaModel) applyCreateIndex(tokens []sqlToken, statement string, enforce bool) ([]schemaHazard, error) {
	on := tokenIndex(tokens, "on")
	if on < 0 {
		return nil, nil
	}
	name, afterName := objectName(tokens, on+1)
	table := model.Tables[name]
	if table == nil || afterName >= len(tokens) || tokens[afterName].Text != "(" {
		return nil, nil
	}
	close := matchingParen(tokens, afterName)
	if close < 0 {
		return nil, fmt.Errorf("CREATE INDEX on %s has unbalanced parentheses", name)
	}
	label := "index"
	indexWord := tokenIndex(tokens[:on], "index")
	if indexWord >= 0 && indexWord+1 < on {
		label = tokens[indexWord+1].Text
	}
	return oversizedIndexHazard(table, name, label, parseIndexColumns(tokens[afterName+1:close]), statement, enforce), nil
}

func (model *schemaModel) applyAlterTable(tokens []sqlToken, statement string, enforce, allowDrop bool) ([]schemaHazard, error) {
	name, bodyStart := objectName(tokens, 2)
	if name == "" || bodyStart >= len(tokens) {
		return nil, nil
	}
	table := model.Tables[name]
	if table == nil {
		return nil, nil
	}
	var hazards []schemaHazard
	for _, clause := range splitTokenClauses(tokens[bodyStart:]) {
		if len(clause) == 0 {
			continue
		}
		start := 0
		if tokenIs(clause[0], "add") {
			start++
			if start < len(clause) && tokenIs(clause[start], "column") {
				start++
			}
			if columns, label, ok := parseIndexClause(clause); ok {
				hazards = append(hazards, oversizedIndexHazard(table, name, label, columns, statement, enforce)...)
				continue
			}
			if check, ok := table.parseCheck(clause); ok {
				table.Checks[check.Name] = check
				continue
			}
			if column, ok := parseColumnDefinition(clause[start:]); ok {
				table.Columns[column.Name] = column
			}
			continue
		}

		switch {
		case tokenIs(clause[0], "drop"):
			i := 1
			if i < len(clause) && (tokenIs(clause[i], "check") || tokenIs(clause[i], "constraint")) {
				i++
				if i < len(clause) && clause[i].Kind == sqlWord {
					delete(table.Checks, strings.ToLower(clause[i].Text))
				}
				continue
			}
			if i < len(clause) && (tokenIs(clause[i], "foreign") || tokenIs(clause[i], "index") || tokenIs(clause[i], "key") || tokenIs(clause[i], "primary")) {
				continue
			}
			if i < len(clause) && tokenIs(clause[i], "column") {
				i++
			}
			if i < len(clause) && clause[i].Kind == sqlWord {
				column := strings.ToLower(clause[i].Text)
				if enforce && !allowDrop {
					for _, check := range table.Checks {
						if check.References[column] {
							hazards = append(hazards, schemaHazard{
								Kind:      fmt.Sprintf("DROP COLUMN %s while CHECK %s still references it", column, check.Name),
								Table:     name,
								Statement: compactSQL(statement),
							})
						}
					}
				}
				delete(table.Columns, column)
				for checkName, check := range table.Checks {
					if check.References[column] {
						delete(table.Checks, checkName)
					}
				}
			}
		case tokenIs(clause[0], "modify"):
			i := 1
			if i < len(clause) && tokenIs(clause[i], "column") {
				i++
			}
			if column, ok := parseColumnDefinition(clause[i:]); ok {
				table.Columns[column.Name] = column
			}
		case tokenIs(clause[0], "change"):
			i := 1
			if i < len(clause) && tokenIs(clause[i], "column") {
				i++
			}
			if i+1 < len(clause) {
				oldName := strings.ToLower(clause[i].Text)
				if column, ok := parseColumnDefinition(clause[i+1:]); ok {
					delete(table.Columns, oldName)
					table.Columns[column.Name] = column
					for checkName, check := range table.Checks {
						if check.References[oldName] {
							delete(check.References, oldName)
							check.References[column.Name] = true
							table.Checks[checkName] = check
						}
					}
				}
			}
		case tokenIs(clause[0], "rename") && len(clause) >= 5 && tokenIs(clause[1], "column") && tokenIs(clause[3], "to"):
			oldName := strings.ToLower(clause[2].Text)
			newName := strings.ToLower(clause[4].Text)
			if column, ok := table.Columns[oldName]; ok {
				delete(table.Columns, oldName)
				column.Name = newName
				table.Columns[newName] = column
			}
			for checkName, check := range table.Checks {
				if check.References[oldName] {
					delete(check.References, oldName)
					check.References[newName] = true
					table.Checks[checkName] = check
				}
			}
		case tokenIs(clause[0], "convert") || tokenIs(clause[0], "default") || tokenIs(clause[0], "character"):
			if charset := tableCharset(clause); charset != "" {
				table.Charset = charset
			}
		}
	}
	return hazards, nil
}

type indexColumn struct {
	Name   string
	Prefix int
}

func parseIndexClause(tokens []sqlToken) ([]indexColumn, string, bool) {
	if len(tokens) == 0 {
		return nil, "", false
	}
	i := 0
	if tokenIs(tokens[i], "add") {
		i++
	}
	if i < len(tokens) && tokenIs(tokens[i], "constraint") {
		i += 2
	}
	if i >= len(tokens) {
		return nil, "", false
	}
	label := "index"
	switch {
	case tokenIs(tokens[i], "primary"):
		label = "PRIMARY"
		i++
		if i < len(tokens) && tokenIs(tokens[i], "key") {
			i++
		}
	case tokenIs(tokens[i], "unique"):
		label = "UNIQUE"
		i++
		if i < len(tokens) && (tokenIs(tokens[i], "index") || tokenIs(tokens[i], "key")) {
			i++
		}
		if i < len(tokens) && tokens[i].Text != "(" {
			label = tokens[i].Text
			i++
		}
	case tokenIs(tokens[i], "index") || tokenIs(tokens[i], "key"):
		i++
		if i < len(tokens) && tokens[i].Text != "(" {
			label = tokens[i].Text
			i++
		}
	default:
		return nil, "", false
	}
	for i < len(tokens) && tokens[i].Text != "(" {
		i++
	}
	if i >= len(tokens) {
		return nil, "", false
	}
	close := matchingParen(tokens, i)
	if close < 0 {
		return nil, "", false
	}
	return parseIndexColumns(tokens[i+1 : close]), label, true
}

func parseIndexColumns(tokens []sqlToken) []indexColumn {
	var columns []indexColumn
	for _, part := range splitTokenClauses(tokens) {
		if len(part) == 0 || part[0].Kind != sqlWord {
			continue
		}
		column := indexColumn{Name: strings.ToLower(part[0].Text)}
		if len(part) >= 4 && part[1].Text == "(" && part[2].Kind == sqlWord && part[3].Text == ")" {
			column.Prefix, _ = strconv.Atoi(part[2].Text)
		}
		columns = append(columns, column)
	}
	return columns
}

func oversizedIndexHazard(table *schemaTable, tableName, label string, columns []indexColumn, statement string, enforce bool) []schemaHazard {
	if !enforce || len(columns) == 0 {
		return nil
	}
	total := 0
	for _, indexed := range columns {
		column, ok := table.Columns[indexed.Name]
		if !ok {
			return []schemaHazard{{Kind: fmt.Sprintf("cannot determine byte width of %s column %s", label, indexed.Name), Table: tableName, Statement: compactSQL(statement)}}
		}
		width, ok := column.indexBytes(table.Charset, indexed.Prefix)
		if !ok {
			return []schemaHazard{{Kind: fmt.Sprintf("cannot determine byte width of %s column %s (%s)", label, indexed.Name, column.DataType), Table: tableName, Statement: compactSQL(statement)}}
		}
		total += width
	}
	if total <= maxInnoDBIndexBytes {
		return nil
	}
	return []schemaHazard{{
		Kind:      fmt.Sprintf("%s encodes to %d bytes, over InnoDB's %d-byte key limit", label, total, maxInnoDBIndexBytes),
		Table:     tableName,
		Statement: compactSQL(statement),
	}}
}

func parseColumnDefinition(tokens []sqlToken) (schemaColumn, bool) {
	if len(tokens) < 2 || tokens[0].Kind != sqlWord || tokens[1].Kind != sqlWord || isTableClause(tokens[0].Text) {
		return schemaColumn{}, false
	}
	column := schemaColumn{Name: strings.ToLower(tokens[0].Text), DataType: strings.ToLower(tokens[1].Text)}
	i := 2
	if i < len(tokens) && tokens[i].Text == "(" {
		close := matchingParen(tokens, i)
		if close > i {
			numbers := tokenNumbers(tokens[i+1 : close])
			if len(numbers) > 0 {
				column.Length = numbers[0]
				column.Precision = numbers[0]
			}
			if len(numbers) > 1 {
				column.Scale = numbers[1]
			}
			i = close + 1
		}
	}
	for i < len(tokens) {
		if tokenIs(tokens[i], "character") && i+2 < len(tokens) && tokenIs(tokens[i+1], "set") {
			column.Charset = strings.ToLower(tokens[i+2].Text)
			break
		}
		if tokenIs(tokens[i], "charset") && i+1 < len(tokens) {
			j := i + 1
			if tokens[j].Text == "=" {
				j++
			}
			if j < len(tokens) {
				column.Charset = strings.ToLower(tokens[j].Text)
			}
			break
		}
		if tokenIs(tokens[i], "collate") && i+1 < len(tokens) {
			collation := strings.ToLower(tokens[i+1].Text)
			if underscore := strings.IndexByte(collation, '_'); underscore > 0 {
				column.Charset = collation[:underscore]
			}
			break
		}
		i++
	}
	return column, true
}

func (table *schemaTable) parseCheck(tokens []sqlToken) (schemaCheck, bool) {
	checkAt := tokenIndex(tokens, "check")
	if checkAt < 0 || checkAt+1 >= len(tokens) || tokens[checkAt+1].Text != "(" {
		return schemaCheck{}, false
	}
	name := ""
	constraintAt := tokenIndex(tokens[:checkAt], "constraint")
	if constraintAt >= 0 && constraintAt+1 < checkAt {
		name = strings.ToLower(tokens[constraintAt+1].Text)
	}
	if name == "" {
		table.nextAnon++
		name = fmt.Sprintf("<anonymous-%d>", table.nextAnon)
	}
	close := matchingParen(tokens, checkAt+1)
	if close < 0 {
		return schemaCheck{}, false
	}
	references := make(map[string]bool)
	for _, token := range tokens[checkAt+2 : close] {
		column := strings.ToLower(token.Text)
		if token.Kind == sqlWord {
			if _, ok := table.Columns[column]; ok {
				references[column] = true
			}
		}
	}
	return schemaCheck{Name: name, References: references}, true
}

func (column schemaColumn) indexBytes(tableCharset string, prefix int) (int, bool) {
	length := column.Length
	if prefix > 0 && (length == 0 || prefix < length) {
		length = prefix
	}
	switch column.DataType {
	case "char", "varchar", "tinytext", "text", "mediumtext", "longtext":
		if length <= 0 {
			return 0, false
		}
		charset := column.Charset
		if charset == "" {
			charset = tableCharset
		}
		return length * charsetMaxBytes(charset), true
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		if length <= 0 {
			return 0, false
		}
		return length, true
	case "tinyint", "boolean", "bool":
		return 1, true
	case "smallint":
		return 2, true
	case "mediumint":
		return 3, true
	case "int", "integer", "float":
		return 4, true
	case "bigint", "double", "real":
		return 8, true
	case "date":
		return 3, true
	case "datetime":
		return 5 + fractionalBytes(column.Precision), true
	case "timestamp":
		return 4 + fractionalBytes(column.Precision), true
	case "time":
		return 3 + fractionalBytes(column.Precision), true
	case "year":
		return 1, true
	case "decimal", "numeric":
		precision := column.Precision
		if precision == 0 {
			precision = 10
		}
		return decimalBytes(precision-column.Scale) + decimalBytes(column.Scale), true
	case "enum":
		return 2, true
	case "set":
		return 8, true
	default:
		return 0, false
	}
}

func decimalBytes(digits int) int {
	if digits <= 0 {
		return 0
	}
	remainderBytes := [...]int{0, 1, 1, 2, 2, 3, 3, 3, 4}
	return digits/9*4 + remainderBytes[digits%9]
}

func fractionalBytes(precision int) int {
	switch {
	case precision <= 0:
		return 0
	case precision <= 2:
		return 1
	case precision <= 4:
		return 2
	default:
		return 3
	}
}

func charsetMaxBytes(charset string) int {
	switch strings.ToLower(charset) {
	case "ascii", "binary", "latin1":
		return 1
	case "ucs2":
		return 2
	case "utf8", "utf8mb3":
		return 3
	default:
		return 4
	}
}

func tableCharset(tokens []sqlToken) string {
	for i := 0; i < len(tokens); i++ {
		if tokenIs(tokens[i], "charset") {
			j := i + 1
			if j < len(tokens) && tokens[j].Text == "=" {
				j++
			}
			if j < len(tokens) && tokens[j].Kind == sqlWord {
				return strings.ToLower(tokens[j].Text)
			}
		}
		if tokenIs(tokens[i], "character") && i+2 < len(tokens) && tokenIs(tokens[i+1], "set") {
			return strings.ToLower(tokens[i+2].Text)
		}
	}
	return ""
}

func objectName(tokens []sqlToken, start int) (string, int) {
	for start < len(tokens) && (tokenIs(tokens[start], "if") || tokenIs(tokens[start], "not") || tokenIs(tokens[start], "exists")) {
		start++
	}
	if start >= len(tokens) || tokens[start].Kind != sqlWord {
		return "", start
	}
	name := strings.ToLower(tokens[start].Text)
	start++
	if start+1 < len(tokens) && tokens[start].Text == "." && tokens[start+1].Kind == sqlWord {
		name += "." + strings.ToLower(tokens[start+1].Text)
		start += 2
	}
	return name, start
}

func matchingParen(tokens []sqlToken, open int) int {
	depth := 0
	for i := open; i < len(tokens); i++ {
		switch tokens[i].Text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func splitTokenClauses(tokens []sqlToken) [][]sqlToken {
	start, depth := 0, 0
	var clauses [][]sqlToken
	for i, token := range tokens {
		switch token.Text {
		case "(":
			depth++
		case ")":
			if depth > 0 {
				depth--
			}
		case ",":
			if depth == 0 {
				clauses = append(clauses, tokens[start:i])
				start = i + 1
			}
		}
	}
	return append(clauses, tokens[start:])
}

func containsToken(tokens []sqlToken, word string) bool {
	return tokenIndex(tokens, word) >= 0
}

func tokenIndex(tokens []sqlToken, word string) int {
	for i, token := range tokens {
		if tokenIs(token, word) {
			return i
		}
	}
	return -1
}

func tokenNumbers(tokens []sqlToken) []int {
	var numbers []int
	for _, token := range tokens {
		if token.Kind != sqlWord {
			continue
		}
		if number, err := strconv.Atoi(token.Text); err == nil {
			numbers = append(numbers, number)
		}
	}
	return numbers
}

func isTableClause(word string) bool {
	switch strings.ToLower(word) {
	case "constraint", "primary", "unique", "key", "index", "check", "foreign", "fulltext", "spatial":
		return true
	default:
		return false
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
