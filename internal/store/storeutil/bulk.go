package storeutil

import (
	"context"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

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
