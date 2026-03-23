package storeutil

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jmoiron/sqlx"
)

func makeQuery(query string, params map[string]any) (string, []any, error) {
	if params == nil {
		params = map[string]any{}
	}
	q, args, err := sqlx.Named(query, params)
	if err != nil {
		return "", nil, fmt.Errorf("named: %w", err)
	}
	q, args, err = sqlx.In(q, args...)
	if err != nil {
		return "", nil, fmt.Errorf("in: %w", err)
	}
	return q, args, nil
}

// MakeQuery converts a named-parameter query into a positional-parameter query.
func MakeQuery(query string, params map[string]any) (string, []any, error) {
	return makeQuery(query, params)
}

// QueryListNamed executes a named-parameter SELECT and scans multiple rows into []T.
func QueryListNamed[T any](
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) ([]T, error) {
	query, args, err := makeQuery(query, params)
	if err != nil {
		return nil, err
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
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}
	return target, nil
}

// QueryNamedOne executes a named-parameter SELECT and scans a single row into T.
func QueryNamedOne[T any](ctx context.Context, conn dependency.DB, query string, params map[string]any) (T, error) {
	var target T
	query, args, err := makeQuery(query, params)
	if err != nil {
		return target, err
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

// QueryCountNamed executes a named-parameter COUNT query and returns the count.
func QueryCountNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	query, args, err := makeQuery(query, params)
	if err != nil {
		return 0, err
	}

	var count int
	if err := conn.QueryRowxContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("query row scan: %w", err)
	}

	return count, nil
}

// ExecNamed executes a named-parameter query (INSERT/UPDATE/DELETE) without returning a result.
func ExecNamed(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) error {
	query, args, err := makeQuery(query, params)
	if err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("ExecContext: %w", err)
	}

	return nil
}

// ExecNamedLastId executes a named-parameter INSERT and returns the last insert ID.
func ExecNamedLastId(
	ctx context.Context,
	conn dependency.DB,
	query string,
	params map[string]any,
) (int, error) {
	query, args, err := makeQuery(query, params)
	if err != nil {
		return 0, err
	}

	res, err := conn.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("ExecContext: %w", err)
	}
	lid, err := res.LastInsertId()

	return int(lid), err
}
