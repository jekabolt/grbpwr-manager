package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Knetic/go-namedParameterQuery"
	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jmoiron/sqlx"
	"github.com/jmoiron/sqlx/reflectx"
)

type (
	ltx struct {
		*sqlx.Tx
	}
	Tx struct {
		*sql.Tx
		Mapper *reflectx.Mapper
	}
)

func (t ltx) BeginTxx(ctx context.Context, opts *sql.TxOptions) (*sqlx.Tx, error) {
	return nil, fmt.Errorf("already in transaction")
}

type txDB interface {
	Commit() error
	Rollback() error
}

func (ms *MYSQLStore) DB() dependency.DB {
	return ms.db
}

// Tx starts transaction and executes the function passing to it Handler
// using this transaction. It automatically rolls the transaction back if
// function returns an error. If the error has been caused by serialization
// error, it calls the function again. In order for serialization errors
// handling to work, the function should return Handler errors
// unchanged, or wrap them using %w.
func (ms *MYSQLStore) Tx(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
	for {
		pst, err := ms.TxBegin(ctx)
		if err != nil {
			return err
		}
		err = f(ctx, pst)
		if err == nil {
			if err = pst.TxCommit(ctx); err == nil {
				return nil
			}
		}
		_ = pst.TxRollback(ctx)
		if ms.IsErrorRepeat(err) {
			continue
		}
		return err
	}
}

// InTx returns true if the object is in transaction
func (ms *MYSQLStore) InTx() bool {
	return ms.txDB != nil
}

func (ms *MYSQLStore) TxBegin(ctx context.Context) (dependency.Repository, error) {
	tx, err := ms.DB().BeginTxx(ctx, &sql.TxOptions{
		Isolation: sql.LevelSerializable,
	})
	if err != nil {
		return nil, err
	}

	return &MYSQLStore{
		db:   ltx{Tx: tx},
		txDB: tx,
		ts:   ms.Now(),
	}, nil
}

// Now returns current time for the store. It is frozen during transactions.
func (ms *MYSQLStore) Now() time.Time {
	if ms.ts.IsZero() {
		return time.Now()
	}
	return ms.ts
}

func (ms *MYSQLStore) TxCommit(ctx context.Context) error {
	if ms.txDB == nil {
		return fmt.Errorf("not in transaction")
	}
	err := ms.txDB.Commit()
	if err == nil {
		ms.db = nil
		ms.txDB = nil
	}
	return err
}

func (ms *MYSQLStore) TxRollback(ctx context.Context) error {
	if ms.txDB == nil {
		return fmt.Errorf("not in transaction")
	}
	err := ms.txDB.Rollback()
	if err == nil {
		ms.db = nil
		ms.txDB = nil
	}
	return err
}

func (ms *MYSQLStore) IsErrorRepeat(err error) bool {
	var e *mysql.MySQLError
	if errors.As(err, &e) {
		if e.Number == 1213 { // ER_LOCK_DEADLOCK
			return true
		}
	}
	return false
}

func (ms *MYSQLStore) IsErrUniqueViolation(err error) bool {
	var e *mysql.MySQLError
	if errors.As(err, &e) {
		if e.Number == 1062 { // ER_DUP_ENTRY
			return true
		}
	}
	return false
}

func MakeQuery(query string, params map[string]any) (string, []any, error) {
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)
	query, args, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return "", nil, fmt.Errorf("in: %w", err)
	}
	return query, args, nil
}

func QueryListNamed[T any](
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) ([]T, error) {
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)
	query, args, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return nil, fmt.Errorf("in: %w", err)
	}
	rows, err := conn.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query context: %w", err)
	}
	defer rows.Close()

	var target []T
	for rows.Next() {
		var t T
		if err := rows.StructScan(&t); err != nil {
			return nil, fmt.Errorf("struct scan: %w", err)
		}
		target = append(target, t)
	}
	return target, nil
}

// DefaultBQPageLimit is the default limit when 0 is passed to paginated BQ reads.
const DefaultBQPageLimit = 500

// BQPageParams holds limit/offset for paginated BQ cache reads.
type BQPageParams struct {
	Limit  int // 0 = DefaultBQPageLimit
	Offset int // must be >= 0
}

func (p BQPageParams) effectiveLimit() int {
	if p.Limit <= 0 {
		return DefaultBQPageLimit
	}
	return p.Limit
}

func (p BQPageParams) effectiveOffset() int {
	if p.Offset < 0 {
		return 0
	}
	return p.Offset
}

func QueryNamedOne[T any](ctx context.Context, conn dependency.DB, query string, params map[string]any) (T, error) {
	var target T
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)

	query, args, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return target, fmt.Errorf("sqlx in: %w", err)
	}

	row := conn.QueryRowxContext(ctx, query, args...)
	if err := row.Err(); err != nil {
		return target, fmt.Errorf("query row: %w", err)
	}

	if err := row.StructScan(&target); err != nil {
		return target, fmt.Errorf("struct scan: %w", err)
	}
	return target, nil
}

func QueryCountNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	queryCountNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryCountNamed.SetValuesFromMap(params)

	query, args, err := sqlx.In(queryCountNamed.GetParsedQuery(), queryCountNamed.GetParsedParameters()...)
	if err != nil {
		return 0, fmt.Errorf("sqlx in: %w", err)
	}

	var count int
	if err := conn.QueryRowxContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("query row scan: %w", err)
	}

	return count, nil
}

func ExecNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) error {
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)
	query, args, argsErr := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if argsErr != nil {
		return fmt.Errorf("sqlx In: %w", argsErr)
	}
	_, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("ExecContext: %w", err)
	}

	return nil
}

const bulkUpsertBatchSize = 250

// BulkUpsert executes INSERT ... ON DUPLICATE KEY UPDATE for multiple rows in one query.
// Batches rows to avoid max_allowed_packet. columns: ordered column names for INSERT.
// updateCols: columns for ON DUPLICATE KEY UPDATE (col=VALUES(col)); updated_at=CURRENT_TIMESTAMP is appended.
func BulkUpsert(ctx context.Context, conn dependency.DB, table string, columns []string, updateCols []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	placeholders := "(" + strings.Repeat("?,", len(columns)-1) + "?)"
	updateParts := make([]string, 0, len(updateCols)+1)
	for _, c := range updateCols {
		updateParts = append(updateParts, c+" = VALUES("+c+")")
	}
	updateParts = append(updateParts, "updated_at = CURRENT_TIMESTAMP")
	updateClause := strings.Join(updateParts, ", ")
	base := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES "
	for i := 0; i < len(rows); i += bulkUpsertBatchSize {
		end := i + bulkUpsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		valueStrings := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(columns))
		for j, row := range batch {
			valueStrings[j] = placeholders
			args = append(args, row...)
		}
		query := base + strings.Join(valueStrings, ", ") + " ON DUPLICATE KEY UPDATE " + updateClause
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("BulkUpsert %s: %w", table, err)
		}
	}
	return nil
}

// BulkInsertRows inserts rows in batches. Columns and row values must align; each row must have len(columns) values.
func BulkInsertRows(ctx context.Context, db dependency.DB, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 || len(columns) == 0 {
		return nil
	}
	valuePlaceholder := "(" + strings.Repeat("?,", len(columns)-1) + "?)"
	base := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES "
	for i := 0; i < len(rows); i += bulkUpsertBatchSize {
		end := i + bulkUpsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		valueStrings := make([]string, len(batch))
		insertArgs := make([]any, 0, len(batch)*len(columns))
		for j, row := range batch {
			valueStrings[j] = valuePlaceholder
			insertArgs = append(insertArgs, row...)
		}
		insertQuery := base + strings.Join(valueStrings, ", ")
		if _, err := db.ExecContext(ctx, insertQuery, insertArgs...); err != nil {
			return fmt.Errorf("BulkInsertRows %s: %w", table, err)
		}
	}
	return nil
}

// BulkUpsertByDate inserts rows using INSERT ... ON DUPLICATE KEY UPDATE.
// Use for tables with composite unique keys (e.g. date, product_id) where input may contain
// duplicate keys. keyColumns must match the table's unique key column order.
func BulkUpsertByDate(ctx context.Context, db dependency.DB, table string, columns []string, keyColumns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	if len(columns) == 0 || columns[0] != "date" {
		return fmt.Errorf("BulkUpsertByDate: first column must be 'date'")
	}
	keySet := make(map[string]struct{})
	for _, k := range keyColumns {
		keySet[k] = struct{}{}
	}
	var updateCols []string
	for _, c := range columns {
		if _, ok := keySet[c]; !ok {
			updateCols = append(updateCols, c+" = VALUES("+c+")")
		}
	}
	if len(updateCols) == 0 {
		return fmt.Errorf("BulkUpsertByDate: no columns to update on duplicate")
	}

	valuePlaceholder := "(" + strings.Repeat("?,", len(columns)-1) + "?)"
	base := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES "
	onDup := " ON DUPLICATE KEY UPDATE " + strings.Join(updateCols, ", ")
	for i := 0; i < len(rows); i += bulkUpsertBatchSize {
		end := i + bulkUpsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		valueStrings := make([]string, len(batch))
		insertArgs := make([]any, 0, len(batch)*len(columns))
		for j, row := range batch {
			valueStrings[j] = valuePlaceholder
			insertArgs = append(insertArgs, row...)
		}
		insertQuery := base + strings.Join(valueStrings, ", ") + onDup
		if _, err := db.ExecContext(ctx, insertQuery, insertArgs...); err != nil {
			return fmt.Errorf("BulkUpsertByDate %s: %w", table, err)
		}
	}
	return nil
}

// BulkReplaceByDate deletes existing rows for the date range covered by the input rows,
// then inserts the new rows. All operations execute within a transaction to prevent orphaned data.
// Assumes the first column is "date" and the first value in each row is a date string (YYYY-MM-DD).
func BulkReplaceByDate(ctx context.Context, db dependency.DB, table string, columns []string, rows [][]any) error {
	if len(rows) == 0 {
		return nil
	}
	if len(columns) == 0 || columns[0] != "date" {
		return fmt.Errorf("BulkReplaceByDate: first column must be 'date'")
	}

	dateSet := make(map[string]struct{})
	for _, row := range rows {
		if len(row) == 0 {
			continue
		}
		dateStr, ok := row[0].(string)
		if !ok {
			return fmt.Errorf("BulkReplaceByDate: first value must be a date string")
		}
		dateSet[dateStr] = struct{}{}
	}

	dates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		dates = append(dates, d)
	}
	if len(dates) == 0 {
		return nil
	}

	tx, err := db.BeginTxx(ctx, nil)
	if err != nil {
		return fmt.Errorf("BulkReplaceByDate %s: begin tx: %w", table, err)
	}
	defer tx.Rollback()

	placeholders := make([]string, len(dates))
	args := make([]any, len(dates))
	for i, d := range dates {
		placeholders[i] = "?"
		args[i] = d
	}
	deleteQuery := fmt.Sprintf("DELETE FROM %s WHERE date IN (%s)", table, strings.Join(placeholders, ", "))
	if _, err := tx.ExecContext(ctx, deleteQuery, args...); err != nil {
		return fmt.Errorf("BulkReplaceByDate %s: delete: %w", table, err)
	}

	valuePlaceholder := "(" + strings.Repeat("?,", len(columns)-1) + "?)"
	base := "INSERT INTO " + table + " (" + strings.Join(columns, ", ") + ") VALUES "
	for i := 0; i < len(rows); i += bulkUpsertBatchSize {
		end := i + bulkUpsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		valueStrings := make([]string, len(batch))
		insertArgs := make([]any, 0, len(batch)*len(columns))
		for j, row := range batch {
			valueStrings[j] = valuePlaceholder
			insertArgs = append(insertArgs, row...)
		}
		insertQuery := base + strings.Join(valueStrings, ", ")
		if _, err := tx.ExecContext(ctx, insertQuery, insertArgs...); err != nil {
			return fmt.Errorf("BulkReplaceByDate %s: insert: %w", table, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("BulkReplaceByDate %s: commit: %w", table, err)
	}

	return nil
}

// BulkInsert performs a bulk insert operation. Batches rows to avoid max_allowed_packet.
func BulkInsert(ctx context.Context, conn dependency.DB, tableName string, rows []map[string]any) error {
	if len(rows) == 0 {
		return nil
	}

	columns := make([]string, 0, len(rows[0]))
	for column := range rows[0] {
		columns = append(columns, column)
	}
	placeholders := "(" + strings.Repeat("?,", len(columns)-1) + "?)"
	base := "INSERT INTO " + tableName + " (" + strings.Join(columns, ", ") + ") VALUES "

	for i := 0; i < len(rows); i += bulkUpsertBatchSize {
		end := i + bulkUpsertBatchSize
		if end > len(rows) {
			end = len(rows)
		}
		batch := rows[i:end]
		valueStrings := make([]string, len(batch))
		args := make([]any, 0, len(batch)*len(columns))
		for j, row := range batch {
			valueStrings[j] = placeholders
			for _, c := range columns {
				args = append(args, row[c])
			}
		}
		query := base + strings.Join(valueStrings, ", ")
		if _, err := conn.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("BulkInsert %s: %w", tableName, err)
		}
	}
	return nil
}

func ExecNamedLastId(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)
	query, args, argsErr := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if argsErr != nil {
		return 0, fmt.Errorf("sqlx In: %w", argsErr)
	}

	res, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("ExecContext: %w", err)
	}
	lid, err := res.LastInsertId()

	return int(lid), err
}
