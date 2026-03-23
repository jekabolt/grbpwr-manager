package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
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
		if rbErr := pst.TxRollback(ctx); rbErr != nil {
			slog.Default().ErrorContext(ctx, "transaction rollback failed",
				slog.String("err", rbErr.Error()),
				slog.String("original_err", err.Error()),
			)
		}
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

	txStore := &MYSQLStore{
		db:   ltx{Tx: tx},
		txDB: tx,
		ts:   ms.Now(),
	}
	initSubStoresForTx(txStore, ms.Tx)
	return txStore, nil
}

// Now returns current time for the store. It is frozen during transactions.
func (ms *MYSQLStore) Now() time.Time {
	if ms.ts.IsZero() {
		return time.Now().UTC()
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

// MakeQuery delegates to storeutil.MakeQuery for backward compatibility.
func MakeQuery(query string, params map[string]any) (string, []any, error) {
	return storeutil.MakeQuery(query, params)
}

// QueryListNamed delegates to storeutil.QueryListNamed for backward compatibility.
func QueryListNamed[T any](
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) ([]T, error) {
	return storeutil.QueryListNamed[T](ctx, conn, query, params)
}

// DefaultBQPageLimit is the default limit when 0 is passed to paginated BQ reads.
const DefaultBQPageLimit = storeutil.DefaultBQPageLimit

// BQPageParams holds limit/offset for paginated BQ cache reads.
type BQPageParams = storeutil.BQPageParams

// QueryNamedOne delegates to storeutil.QueryNamedOne for backward compatibility.
func QueryNamedOne[T any](ctx context.Context, conn dependency.DB, query string, params map[string]any) (T, error) {
	return storeutil.QueryNamedOne[T](ctx, conn, query, params)
}

// QueryCountNamed delegates to storeutil.QueryCountNamed for backward compatibility.
func QueryCountNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	return storeutil.QueryCountNamed(ctx, conn, query, params)
}

// ExecNamed delegates to storeutil.ExecNamed for backward compatibility.
func ExecNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) error {
	return storeutil.ExecNamed(ctx, conn, query, params)
}

// BulkUpsert delegates to storeutil.BulkUpsert for backward compatibility.
func BulkUpsert(ctx context.Context, conn dependency.DB, table string, columns []string, updateCols []string, rows [][]any) error {
	return storeutil.BulkUpsert(ctx, conn, table, columns, updateCols, rows)
}

// BulkInsertRows delegates to storeutil.BulkInsertRows for backward compatibility.
func BulkInsertRows(ctx context.Context, db dependency.DB, table string, columns []string, rows [][]any) error {
	return storeutil.BulkInsertRows(ctx, db, table, columns, rows)
}

// BulkUpsertByDate delegates to storeutil.BulkUpsertByDate for backward compatibility.
func BulkUpsertByDate(ctx context.Context, db dependency.DB, table string, columns []string, keyColumns []string, rows [][]any) error {
	return storeutil.BulkUpsertByDate(ctx, db, table, columns, keyColumns, rows)
}

// BulkReplaceByDate delegates to storeutil.BulkReplaceByDate for backward compatibility.
func BulkReplaceByDate(ctx context.Context, db dependency.DB, table string, columns []string, rows [][]any) error {
	return storeutil.BulkReplaceByDate(ctx, db, table, columns, rows)
}

// BulkInsert delegates to storeutil.BulkInsert for backward compatibility.
func BulkInsert(ctx context.Context, conn dependency.DB, tableName string, rows []map[string]any) error {
	return storeutil.BulkInsert(ctx, conn, tableName, rows)
}

// ExecNamedLastId delegates to storeutil.ExecNamedLastId for backward compatibility.
func ExecNamedLastId(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	return storeutil.ExecNamedLastId(ctx, conn, query, params)
}
