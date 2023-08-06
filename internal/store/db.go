package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/Knetic/go-namedParameterQuery"
	"github.com/blockloop/scan"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jmoiron/sqlx"
	"github.com/lib/pq"
)

type ltx struct {
	*sqlx.Tx
}

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
	var e *pq.Error
	if errors.As(err, &e) {
		if e.Code == "40001" {
			return true
		}
	}
	return false
}

func (ms *MYSQLStore) IsErrUniqueViolation(err error) bool {
	var e *pq.Error
	if errors.As(err, &e) {
		if e.Code == "23505" {
			return true
		}
	}
	return false
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

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query context: %w", err)
	}
	defer rows.Close()

	var target []T
	if err := scan.Rows(&target, rows); err != nil {
		return nil, fmt.Errorf("struct scan: %w", err)
	}

	return target, nil
}

func QueryNamedOne[T any](ctx context.Context, conn dependency.DB, query string, params map[string]any) (T, error) {
	var target T
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)

	query, args, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return target, fmt.Errorf("sqlx in: %w", err)
	}

	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return target, fmt.Errorf("query context: %w", err)
	}
	defer rows.Close()

	var results []T
	if err := scan.Rows(&results, rows); err != nil {
		return target, fmt.Errorf("scan rows: %w", err)
	}
	if len(results) == 0 {
		return target, sql.ErrNoRows
	}
	return results[0], nil
}

func QueryCountNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int32, error) {
	queryCountNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryCountNamed.SetValuesFromMap(params)

	query, args, err := sqlx.In(queryCountNamed.GetParsedQuery(), queryCountNamed.GetParsedParameters()...)
	if err != nil {
		return 0, fmt.Errorf("sqlx in: %w", err)
	}

	var count int32
	if err := conn.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("query row scan: %w", err)
	}

	return count, nil
}

// nolint: interfacer
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
