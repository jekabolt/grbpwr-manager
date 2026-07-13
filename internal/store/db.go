package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
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

const (
	// maxTxRetries caps how many times Tx re-runs the callback when it hits a
	// retryable transient error (deadlock / lock-wait timeout). After this many
	// retries the last error is returned instead of spinning forever.
	maxTxRetries = 5
	// txRetryBaseDelay is the base backoff before the first retry. Delays grow
	// exponentially and are jittered, capped at txRetryMaxDelay. Kept small
	// since these are local tx-contention retries, not external calls.
	txRetryBaseDelay = 10 * time.Millisecond
	// txRetryMaxDelay caps the per-retry backoff.
	txRetryMaxDelay = 300 * time.Millisecond
)

// Tx starts transaction and executes the function passing to it Handler
// using this transaction. It automatically rolls the transaction back if
// function returns an error. If the error has been caused by a retryable
// transient error (MySQL deadlock 1213 or lock-wait timeout 1205), it calls
// the function again with exponential backoff, up to maxTxRetries times. In
// order for retry handling to work, the function should return Handler errors
// unchanged, or wrap them using %w.
func (ms *MYSQLStore) Tx(ctx context.Context, f func(context.Context, dependency.Repository) error) error {
	// runAttempt executes a single transaction attempt. The rollback is deferred and
	// guarded by a committed flag so it runs on every non-commit exit — crucially
	// including a panic inside the callback. An inline rollback (the previous form)
	// was skipped when f panicked, leaking the pooled connection together with its
	// SERIALIZABLE row/gap locks until the context cancelled or ConnMaxLifetime
	// recycled it; repeated panics could exhaust MaxOpenConns while the process, whose
	// gRPC interceptor recovers the panic, still looked healthy. The deferred func does
	// not recover, so the panic still propagates to that interceptor after rollback.
	runAttempt := func() error {
		pst, err := ms.TxBegin(ctx)
		if err != nil {
			return err
		}
		committed := false
		defer func() {
			if committed {
				return
			}
			if rbErr := pst.TxRollback(ctx); rbErr != nil {
				slog.Default().ErrorContext(ctx, "transaction rollback failed",
					slog.String("err", rbErr.Error()),
				)
			}
		}()
		if err = f(ctx, pst); err != nil {
			return err
		}
		if err = pst.TxCommit(ctx); err != nil {
			return err
		}
		committed = true
		return nil
	}

	var err error
	for attempt := 0; ; attempt++ {
		err = runAttempt()
		if err == nil {
			return nil
		}
		// Non-retryable error: return immediately, no wasted retries.
		if !ms.IsErrorRepeat(err) {
			return err
		}
		// Retryable, but the cap is reached: stop and surface the last error.
		if attempt >= maxTxRetries {
			return fmt.Errorf("transaction failed after %d retries: %w", maxTxRetries, err)
		}
		var code uint16
		var me *mysql.MySQLError
		if errors.As(err, &me) {
			code = me.Number
		}
		delay := txRetryBackoff(attempt)
		slog.Default().WarnContext(ctx, "retrying transaction after transient error",
			slog.Int("attempt", attempt+1),
			slog.Int("max_retries", maxTxRetries),
			slog.Int("mysql_code", int(code)),
			slog.Duration("backoff", delay),
		)
		// Respect context cancellation while waiting.
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("transaction retry aborted: %w", ctx.Err())
		case <-t.C:
		}
	}
}

// txRetryBackoff returns the backoff before retrying attempt-number `attempt`
// (0-based). It grows exponentially from txRetryBaseDelay, is capped at
// txRetryMaxDelay, and has up to 50% added jitter to avoid thundering herds.
func txRetryBackoff(attempt int) time.Duration {
	d := txRetryBaseDelay << attempt
	if d > txRetryMaxDelay || d <= 0 {
		d = txRetryMaxDelay
	}
	// Add jitter in [0, d/2).
	if half := int64(d) / 2; half > 0 {
		d += time.Duration(rand.Int63n(half))
	}
	return d
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
		switch e.Number {
		case 1213, // ER_LOCK_DEADLOCK
			1205: // ER_LOCK_WAIT_TIMEOUT
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

// IsErrForeignKeyViolation reports whether err is a MySQL foreign-key constraint
// failure: 1452 (child row references a missing parent on INSERT/UPDATE) or 1451
// (parent row still referenced on DELETE/UPDATE). Used to map bad client-supplied
// ids to InvalidArgument instead of Internal.
func (ms *MYSQLStore) IsErrForeignKeyViolation(err error) bool {
	var e *mysql.MySQLError
	if errors.As(err, &e) {
		switch e.Number {
		case 1452, // ER_NO_REFERENCED_ROW_2
			1451: // ER_ROW_IS_REFERENCED_2
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
