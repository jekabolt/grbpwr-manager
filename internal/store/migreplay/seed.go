package migreplay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

type SeedStats struct {
	TablesVisited      int
	TablesSeeded       int
	RowsInserted       int
	CandidatesRejected int
	UnseededTables     []string
	UnseededReasons    map[string]string
}

type column struct {
	Name          string
	DataType      string
	ColumnType    string
	Nullable      bool
	Default       sql.NullString
	Extra         string
	CharMax       sql.NullInt64
	NumPrecision  sql.NullInt64
	NumScale      sql.NullInt64
	GenerationExp sql.NullString
}

type uniqueIndex struct {
	Name    string
	Columns []string
}

type boundaryValue struct {
	Label string
	Value any
}

func SeedBoundaryData(ctx context.Context, db *sql.DB, schema string, progress io.Writer) (stats SeedStats, retErr error) {
	stats.UnseededReasons = make(map[string]string)
	conn, err := db.Conn(ctx)
	if err != nil {
		return stats, err
	}
	defer conn.Close()

	var originalMode string
	var originalFK int
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_mode, @@SESSION.foreign_key_checks").Scan(&originalMode, &originalFK); err != nil {
		return stats, err
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION sql_mode = ''"); err != nil {
		return stats, err
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION foreign_key_checks = 0"); err != nil {
		return stats, err
	}
	defer func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.ExecContext(restoreCtx, fmt.Sprintf("SET SESSION foreign_key_checks = %d", originalFK)); err != nil && retErr == nil {
			retErr = fmt.Errorf("restore foreign_key_checks: %w", err)
		}
		if _, err := conn.ExecContext(restoreCtx, "SET SESSION sql_mode = ?", originalMode); err != nil && retErr == nil {
			retErr = fmt.Errorf("restore sql_mode: %w", err)
		}
	}()

	tables, err := tableNames(ctx, conn, schema)
	if err != nil {
		return stats, err
	}
	for _, table := range tables {
		stats.TablesVisited++
		columns, err := tableColumns(ctx, conn, schema, table)
		if err != nil {
			return stats, fmt.Errorf("columns for %s: %w", table, err)
		}
		indexes, err := tableUniqueIndexes(ctx, conn, schema, table)
		if err != nil {
			return stats, fmt.Errorf("unique indexes for %s: %w", table, err)
		}
		checks, err := tableChecks(ctx, conn, schema, table)
		if err != nil {
			return stats, fmt.Errorf("checks for %s: %w", table, err)
		}
		hints := inferCheckHints(columns, checks)
		template, err := existingRow(ctx, conn, table, columns)
		if err != nil {
			return stats, fmt.Errorf("read seed template from %s: %w", table, err)
		}

		insertedForTable := 0
		sequence := 1
		lastRejection := "no insertable boundary columns"
		// A check-aware baseline gets constrained dictionaries (status, kind,
		// section, and similar columns) onto an admissible row before individual
		// boundaries deliberately try invalid values.
		for copyNumber := 0; copyNumber < 2; copyNumber++ {
			values := buildRow(columns, indexes, template, hints, "", nil, sequence)
			sequence++
			inserted, err := insertBoundaryRow(ctx, conn, table, values)
			if err != nil {
				if !isBoundaryRejection(err) {
					return stats, fmt.Errorf("insert check-aware baseline into %s: %w", table, err)
				}
				lastRejection = boundaryRejectionSummary(err)
				stats.CandidatesRejected++
				continue
			}
			if !inserted {
				stats.CandidatesRejected++
				continue
			}
			insertedForTable++
			stats.RowsInserted++
		}
		for _, target := range columns {
			if !target.insertable() {
				continue
			}
			for _, boundary := range boundaries(target) {
				// A duplicate pair catches a future UNIQUE over columns that are not
				// already unique in the base schema.
				for copyNumber := 0; copyNumber < 2; copyNumber++ {
					values := buildRow(columns, indexes, template, hints, target.Name, boundary.Value, sequence)
					sequence++
					inserted, err := insertBoundaryRow(ctx, conn, table, values)
					if err != nil {
						if !isBoundaryRejection(err) {
							return stats, fmt.Errorf("insert %s boundary into %s: %w", boundary.Label, table, err)
						}
						lastRejection = boundaryRejectionSummary(err)
						stats.CandidatesRejected++
						continue
					}
					if !inserted {
						stats.CandidatesRejected++
						continue
					}
					insertedForTable++
					stats.RowsInserted++
				}
			}
		}
		if insertedForTable == 0 {
			stats.UnseededTables = append(stats.UnseededTables, table)
			stats.UnseededReasons[table] = lastRejection
		} else {
			stats.TablesSeeded++
		}
	}
	if progress != nil {
		fmt.Fprintf(progress, "migration replay: boundary generator visited %d table(s) with foreign_key_checks=0 and sql_mode=''\n", stats.TablesVisited)
	}
	return stats, nil
}

func tableNames(ctx context.Context, conn *sql.Conn, schema string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT TABLE_NAME FROM information_schema.TABLES
		WHERE TABLE_SCHEMA = ? AND TABLE_TYPE = 'BASE TABLE' AND TABLE_NAME <> 'gorp_migrations'
		ORDER BY TABLE_NAME`, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	return names, rows.Err()
}

func tableColumns(ctx context.Context, conn *sql.Conn, schema, table string) ([]column, error) {
	rows, err := conn.QueryContext(ctx, `SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE,
		COLUMN_DEFAULT, EXTRA, CHARACTER_MAXIMUM_LENGTH, NUMERIC_PRECISION, NUMERIC_SCALE,
		GENERATION_EXPRESSION
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? ORDER BY ORDINAL_POSITION`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var columns []column
	for rows.Next() {
		var c column
		var nullable string
		if err := rows.Scan(&c.Name, &c.DataType, &c.ColumnType, &nullable, &c.Default, &c.Extra,
			&c.CharMax, &c.NumPrecision, &c.NumScale, &c.GenerationExp); err != nil {
			return nil, err
		}
		c.Nullable = nullable == "YES"
		columns = append(columns, c)
	}
	return columns, rows.Err()
}

func tableUniqueIndexes(ctx context.Context, conn *sql.Conn, schema, table string) ([]uniqueIndex, error) {
	rows, err := conn.QueryContext(ctx, `SELECT INDEX_NAME, COLUMN_NAME
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND NON_UNIQUE = 0 AND COLUMN_NAME IS NOT NULL
		ORDER BY INDEX_NAME, SEQ_IN_INDEX`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var indexes []uniqueIndex
	for rows.Next() {
		var indexName, columnName string
		if err := rows.Scan(&indexName, &columnName); err != nil {
			return nil, err
		}
		if len(indexes) == 0 || indexes[len(indexes)-1].Name != indexName {
			indexes = append(indexes, uniqueIndex{Name: indexName})
		}
		indexes[len(indexes)-1].Columns = append(indexes[len(indexes)-1].Columns, columnName)
	}
	return indexes, rows.Err()
}

func tableChecks(ctx context.Context, conn *sql.Conn, schema, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT cc.CHECK_CLAUSE
		FROM information_schema.TABLE_CONSTRAINTS tc
		JOIN information_schema.CHECK_CONSTRAINTS cc
		  ON cc.CONSTRAINT_SCHEMA = tc.CONSTRAINT_SCHEMA
		 AND cc.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
		WHERE tc.TABLE_SCHEMA = ? AND tc.TABLE_NAME = ? AND tc.CONSTRAINT_TYPE = 'CHECK'
		ORDER BY tc.CONSTRAINT_NAME`, schema, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var checks []string
	for rows.Next() {
		var clause string
		if err := rows.Scan(&clause); err != nil {
			return nil, err
		}
		checks = append(checks, clause)
	}
	return checks, rows.Err()
}

func existingRow(ctx context.Context, conn *sql.Conn, table string, columns []column) (map[string]any, error) {
	var names []string
	for _, c := range columns {
		if c.insertable() {
			names = append(names, quoteIdentifier(c.Name))
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	row := conn.QueryRowContext(ctx, "SELECT "+strings.Join(names, ", ")+" FROM "+quoteIdentifier(table)+" LIMIT 1")
	values := make([]any, len(names))
	destinations := make([]any, len(names))
	for i := range values {
		destinations[i] = &values[i]
	}
	if err := row.Scan(destinations...); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	template := make(map[string]any, len(names))
	valueIndex := 0
	for _, c := range columns {
		if !c.insertable() {
			continue
		}
		if raw, ok := values[valueIndex].([]byte); ok {
			template[c.Name] = append([]byte(nil), raw...)
		} else {
			template[c.Name] = values[valueIndex]
		}
		valueIndex++
	}
	return template, nil
}

func buildRow(columns []column, indexes []uniqueIndex, template map[string]any, hints map[string][]any, target string, boundary any, sequence int) map[string]any {
	byName := make(map[string]column, len(columns))
	values := make(map[string]any)
	for _, c := range columns {
		byName[c.Name] = c
		if !c.insertable() {
			continue
		}
		if template != nil {
			values[c.Name] = template[c.Name]
			continue
		}
		if c.Nullable {
			values[c.Name] = nil
		} else if !c.Default.Valid {
			if hint := firstHint(hints[c.Name]); hint != nil {
				values[c.Name] = hint
			} else {
				values[c.Name] = safeValue(c, sequence)
			}
		}
	}
	if target != "" {
		values[target] = boundary
	}

	for _, index := range indexes {
		uniqueAlready := false
		for _, name := range index.Columns {
			c := byName[name]
			value, present := values[name]
			if !c.insertable() || (present && value == nil) {
				uniqueAlready = true
				break
			}
		}
		if uniqueAlready {
			continue
		}
		name := uniqueColumnToVary(index.Columns, byName, hints, target)
		if name != "" {
			values[name] = uniqueValue(byName[name], hints[name], sequence)
		}
	}
	return values
}

func uniqueColumnToVary(names []string, columns map[string]column, hints map[string][]any, target string) string {
	// Prefer an unconstrained column. Varying a finite checked vocabulary (for
	// example size.sku_system) used to turn otherwise valid templates into rows
	// which every CHECK rejected.
	for _, name := range names {
		if name != target && columns[name].insertable() && columns[name].supportsGeneratedUnique() && len(hints[name]) == 0 {
			return name
		}
	}
	for _, name := range names {
		if name != target && columns[name].insertable() && columns[name].supportsGeneratedUnique() {
			return name
		}
	}
	// Do not replace a boundary just to satisfy an already-unique target. The
	// first copy may still fit; the duplicate is expected to be rejected.
	return ""
}

func insertBoundaryRow(ctx context.Context, conn *sql.Conn, table string, values map[string]any) (bool, error) {
	if len(values) == 0 {
		result, err := conn.ExecContext(ctx, "INSERT INTO "+quoteIdentifier(table)+" () VALUES ()")
		if err != nil {
			return false, err
		}
		affected, err := result.RowsAffected()
		return affected > 0, err
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sortStrings(names)
	quoted := make([]string, len(names))
	marks := make([]string, len(names))
	args := make([]any, len(names))
	for i, name := range names {
		quoted[i] = quoteIdentifier(name)
		marks[i] = "?"
		args[i] = values[name]
	}
	query := "INSERT INTO " + quoteIdentifier(table) + " (" + strings.Join(quoted, ", ") + ") VALUES (" + strings.Join(marks, ", ") + ")"
	result, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected > 0, err
}

func boundaryRejectionSummary(err error) string {
	var mysqlErr *mysql.MySQLError
	if errors.As(err, &mysqlErr) {
		return fmt.Sprintf("MySQL %d: %s", mysqlErr.Number, mysqlErr.Message)
	}
	return err.Error()
}

func isBoundaryRejection(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case 1048, // column cannot be NULL
		1062,       // duplicate key
		1264, 1265, // range/truncation
		1292,       // invalid temporal value
		1364, 1366, // missing default/invalid value
		1406, 1411, // value too long/invalid function argument
		1452,       // trigger-created FK failure
		1644,       // trigger SIGNAL
		3140,       // invalid JSON
		3819, 4025: // CHECK constraint failures
		return true
	default:
		return false
	}
}

func (c column) insertable() bool {
	extra := strings.ToLower(c.Extra)
	return !strings.Contains(extra, "auto_increment") && !strings.Contains(extra, "generated") &&
		(!c.GenerationExp.Valid || c.GenerationExp.String == "")
}

func (c column) supportsGeneratedUnique() bool {
	switch c.DataType {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "char", "varchar", "binary", "varbinary":
		return true
	default:
		return false
	}
}

var (
	checkInValuesRe     = regexp.MustCompile(`(?is)(?:\bbinary\s+)?` + "`?" + `([a-z0-9_]+)` + "`?" + `\s+in\s*\(([^()]*)\)`)
	checkRegexpRe       = regexp.MustCompile(`(?is)` + "`?" + `([a-z0-9_]+)` + "`?" + `\s+regexp\s+(?:_[a-z0-9]+)?'((?:''|\\.|[^'])*)'`)
	checkRegexpLikeRe   = regexp.MustCompile(`(?is)regexp_like\s*\(\s*` + "`?" + `([a-z0-9_]+)` + "`?" + `\s*,\s*(?:_[a-z0-9]+)?'((?:''|\\.|[^'])*)'`)
	checkEqualityRe     = regexp.MustCompile(`(?is)` + "`?" + `([a-z0-9_]+)` + "`?" + `\s*=\s*(?:(?:_[a-z0-9]+)?'((?:''|\\.|[^'])*)'|(-?[0-9]+(?:\.[0-9]+)?))`)
	simpleRegexpValueRe = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

// inferCheckHints extracts finite, known-good values from the CHECK shapes used
// throughout the schema. The hints are only for filling the other columns of a
// boundary row; the target column still receives the hostile boundary value.
func inferCheckHints(columns []column, checks []string) map[string][]any {
	actualNames := make(map[string]string, len(columns))
	for _, c := range columns {
		actualNames[strings.ToLower(c.Name)] = c.Name
	}
	hints := make(map[string][]any)
	add := func(rawName string, values []any) {
		name, ok := actualNames[strings.ToLower(rawName)]
		if !ok {
			return
		}
		for _, value := range values {
			if value == "" || containsAny(hints[name], value) {
				continue
			}
			hints[name] = append(hints[name], value)
		}
	}
	for _, check := range checks {
		for _, match := range checkInValuesRe.FindAllStringSubmatch(check, -1) {
			add(match[1], stringsToAny(sqlQuotedValues(match[2])))
		}
		for _, match := range checkRegexpRe.FindAllStringSubmatch(check, -1) {
			add(match[1], stringsToAny(valuesFromCheckRegexp(decodeSQLStringContent(match[2]))))
		}
		for _, match := range checkRegexpLikeRe.FindAllStringSubmatch(check, -1) {
			add(match[1], stringsToAny(valuesFromCheckRegexp(decodeSQLStringContent(match[2]))))
		}
		for _, match := range checkEqualityRe.FindAllStringSubmatch(check, -1) {
			if match[2] != "" {
				add(match[1], []any{decodeSQLStringContent(match[2])})
				continue
			}
			if integer, err := strconv.ParseInt(match[3], 10, 64); err == nil {
				add(match[1], []any{integer})
			} else {
				add(match[1], []any{match[3]})
			}
		}
	}
	return hints
}

func stringsToAny(values []string) []any {
	result := make([]any, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

func sqlQuotedValues(input string) []string {
	var values []string
	for i := 0; i < len(input); i++ {
		if input[i] != '\'' {
			continue
		}
		i++
		var value strings.Builder
		for i < len(input) {
			switch {
			case input[i] == '\\' && i+1 < len(input):
				i++
				value.WriteByte(input[i])
			case input[i] == '\'' && i+1 < len(input) && input[i+1] == '\'':
				value.WriteByte('\'')
				i++
			case input[i] == '\'':
				values = append(values, value.String())
				goto next
			default:
				value.WriteByte(input[i])
			}
			i++
		}
	next:
	}
	return values
}

func decodeSQLStringContent(input string) string {
	return strings.ReplaceAll(strings.ReplaceAll(input, "''", "'"), `\\`, `\`)
}

func valuesFromCheckRegexp(pattern string) []string {
	if pattern == `^AR[0-9A-Z]{1,10}$` {
		return []string{"AR1"}
	}
	if !strings.HasPrefix(pattern, "^(") {
		return nil
	}
	end := strings.LastIndex(pattern, ")$")
	if end < 2 {
		return nil
	}
	var values []string
	for _, candidate := range strings.Split(pattern[2:end], "|") {
		if simpleRegexpValueRe.MatchString(candidate) {
			values = append(values, candidate)
		}
	}
	return values
}

func containsAny(values []any, want any) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func firstHint(values []any) any {
	if len(values) == 0 {
		return nil
	}
	return values[0]
}

func boundaries(c column) []boundaryValue {
	var values []boundaryValue
	if c.Nullable {
		values = append(values, boundaryValue{Label: "null", Value: nil})
	}
	unsigned := strings.Contains(strings.ToLower(c.ColumnType), "unsigned")
	switch c.DataType {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint":
		values = append(values, boundaryValue{Label: "zero", Value: int64(0)})
		if !unsigned {
			values = append(values, boundaryValue{Label: "negative", Value: int64(-1)})
		}
		values = append(values, boundaryValue{Label: "maximum", Value: integerMaximum(c.DataType, unsigned)})
	case "decimal", "numeric":
		values = append(values, boundaryValue{Label: "zero", Value: "0"})
		if !unsigned {
			values = append(values, boundaryValue{Label: "negative", Value: "-1"})
		}
		values = append(values, boundaryValue{Label: "maximum", Value: decimalMaximum(c)})
	case "float":
		values = append(values, boundaryValue{Label: "zero", Value: 0}, boundaryValue{Label: "negative", Value: -1}, boundaryValue{Label: "maximum", Value: float64(math.MaxFloat32)})
	case "double", "real":
		values = append(values, boundaryValue{Label: "zero", Value: 0}, boundaryValue{Label: "negative", Value: -1}, boundaryValue{Label: "maximum", Value: math.MaxFloat64})
	case "enum", "set":
		for _, value := range enumValues(c.ColumnType) {
			values = append(values, boundaryValue{Label: "legacy value", Value: value})
		}
	case "char", "varchar":
		length := int64(1)
		if c.CharMax.Valid && c.CharMax.Int64 > 0 {
			length = c.CharMax.Int64
		}
		values = append(values, boundaryValue{Label: "empty", Value: ""}, boundaryValue{Label: "maximum length", Value: strings.Repeat("x", int(length))})
	case "tinytext", "text", "mediumtext", "longtext":
		length := int64(4096)
		if c.CharMax.Valid && c.CharMax.Int64 < length {
			length = c.CharMax.Int64
		}
		values = append(values, boundaryValue{Label: "empty", Value: ""}, boundaryValue{Label: "long text sample", Value: strings.Repeat("x", int(length))})
	case "binary", "varbinary":
		length := int64(1)
		if c.CharMax.Valid && c.CharMax.Int64 > 0 {
			length = c.CharMax.Int64
		}
		values = append(values, boundaryValue{Label: "empty", Value: []byte{}}, boundaryValue{Label: "maximum length", Value: bytesOf(0xff, length)})
	case "tinyblob", "blob", "mediumblob", "longblob":
		values = append(values, boundaryValue{Label: "empty", Value: []byte{}}, boundaryValue{Label: "binary sample", Value: bytesOf(0xff, 4096)})
	case "date":
		values = append(values, boundaryValue{Label: "lower date", Value: "1000-01-01"}, boundaryValue{Label: "upper date", Value: "9999-12-31"})
	case "datetime":
		values = append(values, boundaryValue{Label: "lower datetime", Value: "1000-01-01 00:00:00"}, boundaryValue{Label: "upper datetime", Value: "9999-12-31 23:59:59"})
	case "timestamp":
		values = append(values, boundaryValue{Label: "lower timestamp", Value: "1970-01-02 00:00:01"}, boundaryValue{Label: "upper timestamp", Value: "2038-01-19 03:14:07"})
	case "time":
		values = append(values, boundaryValue{Label: "zero time", Value: "00:00:00"}, boundaryValue{Label: "negative time", Value: "-838:59:59"}, boundaryValue{Label: "upper time", Value: "838:59:59"})
	case "year":
		values = append(values, boundaryValue{Label: "lower year", Value: 1901}, boundaryValue{Label: "upper year", Value: 2155})
	case "json":
		values = append(values, boundaryValue{Label: "empty object", Value: "{}"}, boundaryValue{Label: "empty array", Value: "[]"})
	case "bit":
		values = append(values, boundaryValue{Label: "zero bits", Value: []byte{0}}, boundaryValue{Label: "set bits", Value: []byte{1}})
	}
	if len(values) == 0 && c.Nullable {
		return values
	}
	return values
}

func safeValue(c column, sequence int) any {
	if values := enumValues(c.ColumnType); len(values) > 0 {
		return values[0]
	}
	switch c.DataType {
	case "tinyint", "smallint", "mediumint", "int", "integer", "bigint", "decimal", "numeric", "float", "double", "real", "year":
		return 1
	case "bit":
		return []byte{1}
	case "date":
		return "2000-01-01"
	case "datetime", "timestamp":
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	case "time":
		return "00:00:01"
	case "json":
		return "{}"
	case "binary", "varbinary", "tinyblob", "blob", "mediumblob", "longblob":
		return []byte{1}
	default:
		name := strings.ToLower(c.Name)
		if strings.Contains(name, "email") {
			return sizedString(c, fmt.Sprintf("migfuzz+%d@example.invalid", sequence))
		}
		if strings.Contains(name, "phone") {
			return sizedString(c, fmt.Sprintf("100%07d", sequence%10000000))
		}
		return sizedString(c, "migfuzz_"+strconv.Itoa(sequence))
	}
}

func uniqueValue(c column, hints []any, sequence int) any {
	if len(hints) > 0 {
		return hints[sequence%len(hints)]
	}
	unsigned := strings.Contains(strings.ToLower(c.ColumnType), "unsigned")
	switch c.DataType {
	case "tinyint":
		if unsigned {
			return 128 + sequence%127
		}
		return 1 + sequence%126
	case "smallint":
		if unsigned {
			return 40000 + sequence%25000
		}
		return 1000 + sequence%30000
	case "mediumint", "int", "integer", "bigint", "decimal", "numeric":
		return 900000000 + sequence
	case "binary", "varbinary":
		return []byte(sizedString(c, fmt.Sprintf("f%09d", sequence)))
	default:
		if strings.Contains(strings.ToLower(c.Name), "email") {
			return sizedString(c, fmt.Sprintf("migfuzz+%d@example.invalid", sequence))
		}
		return sizedString(c, fmt.Sprintf("migfuzz_%d", sequence))
	}
}

func sizedString(c column, value string) string {
	if !c.CharMax.Valid || c.CharMax.Int64 <= 0 {
		return value
	}
	length := int(c.CharMax.Int64)
	if len(value) > length {
		return value[:length]
	}
	return value
}

func integerMaximum(dataType string, unsigned bool) any {
	if unsigned {
		switch dataType {
		case "tinyint":
			return int64(255)
		case "smallint":
			return int64(65535)
		case "mediumint":
			return int64(16777215)
		case "int", "integer":
			return int64(4294967295)
		default:
			return "18446744073709551615"
		}
	}
	switch dataType {
	case "tinyint":
		return int64(127)
	case "smallint":
		return int64(32767)
	case "mediumint":
		return int64(8388607)
	case "int", "integer":
		return int64(2147483647)
	default:
		return int64(math.MaxInt64)
	}
}

func decimalMaximum(c column) string {
	precision, scale := int64(10), int64(0)
	if c.NumPrecision.Valid {
		precision = c.NumPrecision.Int64
	}
	if c.NumScale.Valid {
		scale = c.NumScale.Int64
	}
	integerDigits := precision - scale
	value := strings.Repeat("9", int(integerDigits))
	if scale > 0 {
		value += "." + strings.Repeat("9", int(scale))
	}
	return value
}

func enumValues(columnType string) []string {
	open := strings.IndexByte(columnType, '(')
	close := strings.LastIndexByte(columnType, ')')
	if open < 0 || close <= open {
		return nil
	}
	input := columnType[open+1 : close]
	var values []string
	for i := 0; i < len(input); {
		for i < len(input) && (input[i] == ' ' || input[i] == ',') {
			i++
		}
		if i >= len(input) || input[i] != '\'' {
			break
		}
		i++
		var value strings.Builder
		for i < len(input) {
			if input[i] == '\\' && i+1 < len(input) {
				i++
				value.WriteByte(input[i])
				i++
				continue
			}
			if input[i] == '\'' {
				if i+1 < len(input) && input[i+1] == '\'' {
					value.WriteByte('\'')
					i += 2
					continue
				}
				i++
				break
			}
			value.WriteByte(input[i])
			i++
		}
		values = append(values, value.String())
	}
	return values
}

func quoteIdentifier(identifier string) string {
	return "`" + strings.ReplaceAll(identifier, "`", "``") + "`"
}

func bytesOf(value byte, length int64) []byte {
	result := make([]byte, int(length))
	for i := range result {
		result[i] = value
	}
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
