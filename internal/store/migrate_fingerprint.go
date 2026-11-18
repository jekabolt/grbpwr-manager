package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const fingerprintTimeout = time.Minute

// The schema of the connected database only (DATABASE()): no database name ever enters the SQL.
// gorp_migrations is left out: it records history, not shape.
const (
	fingerprintColumns = `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLUMN_DEFAULT, EXTRA
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> 'gorp_migrations'`
	fingerprintIndexes = `SELECT TABLE_NAME, INDEX_NAME, SEQ_IN_INDEX, COLUMN_NAME, NON_UNIQUE
		FROM information_schema.STATISTICS
		WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME <> 'gorp_migrations'`
)

// SchemaFingerprint is the sha256 (hex) of SchemaLines: the same function for spares, new teams and
// team #1, so equal fingerprints mean equal columns and indexes (02-DESIGN §17 G8).
func SchemaFingerprint(ctx context.Context, baseDSN, dbName string) (string, error) {
	lines, err := SchemaLines(ctx, baseDSN, dbName)
	if err != nil {
		return "", err
	}
	return FingerprintOf(lines), nil
}

// FingerprintOf hashes sorted schema lines.
func FingerprintOf(lines []string) string {
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SchemaLines returns one sorted line per column (table, column, type, nullable, default, extra) and
// per index column (table, index, seq, column, non_unique) of database dbName, read on a short-lived
// handle of its own pointed at that database.
func SchemaLines(ctx context.Context, baseDSN, dbName string) ([]string, error) {
	dsn, err := TeamDSN(baseDSN, dbName)
	if err != nil {
		return nil, err
	}
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse team dsn: %w", err)
	}
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("schema fingerprint: connector: %w", err)
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(ctx, fingerprintTimeout)
	defer cancel()
	var lines []string
	for _, q := range []struct{ kind, sql string }{{"c", fingerprintColumns}, {"i", fingerprintIndexes}} {
		got, err := schemaRows(ctx, db, q.kind, q.sql)
		if err != nil {
			return nil, fmt.Errorf("schema fingerprint of %s: %w", dbName, err)
		}
		lines = append(lines, got...)
	}
	sort.Strings(lines)
	return lines, nil
}

func schemaRows(ctx context.Context, db *sql.DB, kind, q string) ([]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []string
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		parts := make([]string, 0, len(vals)+1)
		parts = append(parts, kind)
		for _, v := range vals {
			if !v.Valid {
				parts = append(parts, "<null>")
				continue
			}
			parts = append(parts, v.String)
		}
		out = append(out, strings.Join(parts, "\t"))
	}
	return out, rows.Err()
}

// FirstSchemaDiff returns up to n lines present in only one of two sorted line sets, each marked
// "-" (only in want) or "+" (only in got): what a schema_drift warning shows.
func FirstSchemaDiff(want, got []string, n int) []string {
	var out []string
	i, j := 0, 0
	for (i < len(want) || j < len(got)) && len(out) < n {
		switch {
		case j >= len(got) || (i < len(want) && want[i] < got[j]):
			out = append(out, "- "+want[i])
			i++
		case i >= len(want) || got[j] < want[i]:
			out = append(out, "+ "+got[j])
			j++
		default:
			i++
			j++
		}
	}
	return out
}
