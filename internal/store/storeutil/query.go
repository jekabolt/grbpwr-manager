package storeutil

import (
	"context"
	"fmt"

	"github.com/Knetic/go-namedParameterQuery"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jmoiron/sqlx"
)

// MakeQuery converts a named-parameter query into a positional-parameter query.
func MakeQuery(query string, params map[string]any) (string, []any, error) {
	queryNamed := namedParameterQuery.NewNamedParameterQuery(query)
	queryNamed.SetValuesFromMap(params)
	query, args, err := sqlx.In(queryNamed.GetParsedQuery(), queryNamed.GetParsedParameters()...)
	if err != nil {
		return "", nil, fmt.Errorf("in: %w", err)
	}
	return query, args, nil
}

// QueryListNamed executes a named-parameter SELECT and scans multiple rows into []T.
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

// QueryNamedOne executes a named-parameter SELECT and scans a single row into T.
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

// QueryCountNamed executes a named-parameter COUNT query and returns the count.
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

// ExecNamed executes a named-parameter query (INSERT/UPDATE/DELETE) without returning a result.
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

// ExecNamedLastId executes a named-parameter INSERT and returns the last insert ID.
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
