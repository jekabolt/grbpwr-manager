package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	migrate "github.com/rubenv/sql-migrate"
)

const (
	// migrationLockWaitSeconds bounds how long one migration statement waits for a table's
	// metadata lock. MySQL's default is a year, and while an ALTER waits, every read and write of
	// that table queues behind it, ALGORITHM=INSTANT included. 20s outlasts the short transactions
	// of normal traffic, and the deploy simply fails with the old instance still serving when
	// something longer holds the table.
	migrationLockWaitSeconds = 20
	// migrationStopTimeout bounds the KILL round trip and the wait for the killed statement to return.
	migrationStopTimeout    = 15 * time.Second
	migrationKillRetryDelay = time.Second

	mysqlErrLockWaitTimeout = 1205
	mysqlErrUnknownThread   = 1094
)

// MigrateDSN applies the embedded migrations on a pool of its own, so the bounded lock wait never
// reaches the application's connections. Canceling ctx kills the statement on the server.
func MigrateDSN(ctx context.Context, dsn string) error {
	source := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: fs,
		Root:       "sql",
	}
	n, err := runMigrations(ctx, dsn, migrate.MigrationSet{}, source, migrationLockWaitSeconds)
	if err != nil {
		return err
	}
	slog.Default().InfoContext(ctx, "applied migrations",
		slog.Int("count", n),
	)
	return nil
}

func runMigrations(ctx context.Context, dsn string, set migrate.MigrationSet, source migrate.MigrationSource, lockWaitSeconds int) (int, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return 0, fmt.Errorf("parse migration dsn: %w", err)
	}
	if cfg.Params == nil {
		cfg.Params = make(map[string]string, 1)
	}
	// The driver sends DSN params on every new connection, so a reconnect keeps the bound.
	cfg.Params["lock_wait_timeout"] = strconv.Itoa(lockWaitSeconds)
	inner, err := mysql.NewConnector(cfg)
	if err != nil {
		return 0, fmt.Errorf("migration connector: %w", err)
	}
	connector := &migrationConnector{ctx: ctx, inner: inner}
	db := sql.OpenDB(connector)
	defer db.Close()
	// sql-migrate runs one statement at a time, so one connection is enough and there is one to kill.
	db.SetMaxOpenConns(1)

	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := set.Exec(db, "mysql", source, migrate.Up)
		done <- result{n: n, err: err}
	}()

	select {
	case res := <-done:
		if res.err != nil {
			return res.n, describeMigrationError(res.err, lockWaitSeconds)
		}
		return res.n, nil
	case <-ctx.Done():
		stopCtx, stopCancel := context.WithTimeout(context.Background(), migrationStopTimeout)
		defer stopCancel()
		errs := []error{fmt.Errorf("migration timeout: %w", ctx.Err())}
		if err := connector.killAll(stopCtx); err != nil {
			errs = append(errs, err)
		}
		select {
		case <-done:
		case <-stopCtx.Done():
			errs = append(errs, errors.New("migration statement may still be running on the server"))
		}
		return 0, errors.Join(errs...)
	}
}

func describeMigrationError(err error, lockWaitSeconds int) error {
	var txErr *migrate.TxError
	if errors.As(err, &txErr) {
		var myErr *mysql.MySQLError
		if errors.As(txErr.Err, &myErr) && myErr.Number == mysqlErrLockWaitTimeout {
			return fmt.Errorf("db migrations have failed: %s could not get a table lock in %ds, a long transaction holds the table, retry after it ends: %w",
				txErr.Migration.Id, lockWaitSeconds, err)
		}
	}
	return fmt.Errorf("db migrations have failed: %w", err)
}

// migrationConnector remembers the server-side id of every connection it opens, because closing a
// client socket does not stop a statement that is already running on the server. Once ctx is
// canceled it opens nothing new, so database/sql cannot carry the migration on after the KILL.
type migrationConnector struct {
	ctx   context.Context
	inner driver.Connector

	mu  sync.Mutex
	ids []int64
}

func (c *migrationConnector) Connect(context.Context) (driver.Conn, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	// database/sql passes a background context here, so the migration's own deadline has to bound
	// a stalled handshake: this connection has no id to kill yet.
	conn, err := c.inner.Connect(c.ctx)
	if err != nil {
		return nil, err
	}
	id, err := driverConnectionID(c.ctx, conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read migration connection id: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		conn.Close()
		return nil, err
	}
	c.ids = append(c.ids, id)
	return conn, nil
}

func (c *migrationConnector) Driver() driver.Driver {
	return c.inner.Driver()
}

// killAll retries until every known connection is killed or ctx ends, and reports what it could not
// kill: a statement left behind is the very thing the caller has to hear about.
func (c *migrationConnector) killAll(ctx context.Context) error {
	c.mu.Lock()
	ids := append([]int64(nil), c.ids...)
	c.mu.Unlock()

	db := sql.OpenDB(c.inner)
	defer db.Close()
	for {
		var failed []int64
		var lastErr error
		for _, id := range ids {
			_, err := db.ExecContext(ctx, "KILL "+strconv.FormatInt(id, 10))
			var myErr *mysql.MySQLError
			if err == nil || (errors.As(err, &myErr) && myErr.Number == mysqlErrUnknownThread) {
				continue // killed, or this connection is already gone
			}
			failed = append(failed, id)
			lastErr = err
		}
		if len(failed) == 0 {
			return nil
		}
		ids = failed
		select {
		case <-ctx.Done():
			return fmt.Errorf("can't kill %d migration connection(s): %w", len(failed), lastErr)
		case <-time.After(migrationKillRetryDelay):
		}
	}
}

func driverConnectionID(ctx context.Context, conn driver.Conn) (int64, error) {
	queryer, ok := conn.(driver.QueryerContext)
	if !ok {
		return 0, errors.New("driver connection can't run queries")
	}
	rows, err := queryer.QueryContext(ctx, "SELECT CONNECTION_ID()", nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	row := make([]driver.Value, 1)
	if err := rows.Next(row); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, errors.New("no row")
		}
		return 0, err
	}
	switch v := row[0].(type) {
	case int64:
		return v, nil
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}
