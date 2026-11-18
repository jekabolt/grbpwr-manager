package tenantdb_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/store/tenantdb"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// A fake server: each connection has a database and maybe an open transaction. Knobs make the next
// USE switch the database and still fail (a cancel after the server acted), or the next COMMIT /
// ROLLBACK fail without ending the transaction.
type fakeServer struct {
	mu           sync.Mutex
	nextID       int
	failUse      bool
	failCommit   bool
	failRollback bool
}

type fakeConnector struct {
	srv  *fakeServer
	home string
}

func (c *fakeConnector) Connect(context.Context) (driver.Conn, error) {
	c.srv.mu.Lock()
	defer c.srv.mu.Unlock()
	c.srv.nextID++
	return &fakeConn{srv: c.srv, id: c.srv.nextID, db: c.home}, nil
}

func (c *fakeConnector) Driver() driver.Driver { return nil }

type fakeConn struct {
	srv  *fakeServer
	id   int
	db   string
	inTx bool
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) { return nil, errors.New("fake: no prepare") }
func (c *fakeConn) Close() error                        { return nil }
func (c *fakeConn) Begin() (driver.Tx, error)           { return nil, errors.New("fake: use BeginTx") }

func (c *fakeConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.inTx = true
	return &fakeTx{c: c}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	if db, ok := strings.CutPrefix(q, "USE `"); ok {
		c.db = strings.TrimSuffix(db, "`")
		c.srv.mu.Lock()
		fail := c.srv.failUse
		c.srv.failUse = false
		c.srv.mu.Unlock()
		if fail {
			return nil, context.Canceled
		}
		return driver.RowsAffected(0), nil
	}
	return driver.RowsAffected(1), nil
}

// QueryContext answers any query with one row: connection id, database, open transaction.
func (c *fakeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &fakeRows{vals: []driver.Value{int64(c.id), c.db, c.inTx}}, nil
}

type fakeTx struct{ c *fakeConn }

func (t *fakeTx) Commit() error {
	t.c.srv.mu.Lock()
	defer t.c.srv.mu.Unlock()
	if t.c.srv.failCommit {
		t.c.srv.failCommit = false
		return errors.New("fake: commit lost")
	}
	t.c.inTx = false
	return nil
}

func (t *fakeTx) Rollback() error {
	t.c.srv.mu.Lock()
	defer t.c.srv.mu.Unlock()
	if t.c.srv.failRollback {
		t.c.srv.failRollback = false
		return errors.New("fake: rollback lost")
	}
	t.c.inTx = false
	return nil
}

type fakeRows struct {
	vals []driver.Value
	done bool
}

func (r *fakeRows) Columns() []string { return []string{"conn", "db", "in_tx"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	copy(dest, r.vals)
	return nil
}

type fakeState struct {
	conn int64
	db   string
	inTx bool
}

func fakeQuery(t *testing.T, ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) fakeState {
	t.Helper()
	var s fakeState
	if err := q.QueryRowContext(ctx, "SELECT state").Scan(&s.conn, &s.db, &s.inTx); err != nil {
		t.Fatalf("query: %v", err)
	}
	return s
}

// TestFakeBrokenSessionNeverReachesAnotherTeam — after a USE that failed (the server may have
// switched anyway) or a COMMIT / ROLLBACK that failed (the server may keep the transaction open),
// the connection is thrown away: the next call, even for another team, runs on a fresh connection,
// on its own database, outside any transaction. MaxOpenConns=1 forces database/sql to reuse the one
// connection if it is still considered good.
func TestFakeBrokenSessionNeverReachesAnotherTeam(t *testing.T) {
	const home, other = "grbpwr_fake", "tdbt_f0000001"
	srv := &fakeServer{}
	conn, err := tenantdb.NewConnectorOver(&fakeConnector{srv: srv, home: home}, home,
		tenantdb.Options{Team1: tenant.TeamOne(home), Strict: true, DBPrefix: "tdbt_"})
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	bg := context.Background()
	ctxOne := tenant.With(bg, tenant.TeamOne(home))
	ctxB := tenant.With(bg, tenant.Team{ID: 2, Slug: "bravo", DBName: other, Status: tenant.StatusActive})

	first := fakeQuery(t, ctxOne, db)
	if first.db != home {
		t.Fatalf("team #1 ran on %q", first.db)
	}
	if again := fakeQuery(t, ctxOne, db); again.conn != first.conn {
		t.Fatal("a healthy connection was not reused (the fake cannot prove anything)")
	}

	t.Run("USE fails after switching", func(t *testing.T) {
		before := fakeQuery(t, ctxOne, db)
		srv.mu.Lock()
		srv.failUse = true
		srv.mu.Unlock()
		if err := db.QueryRowContext(ctxB, "SELECT state").Scan(new(int64), new(string), new(bool)); err == nil {
			t.Fatal("a failed USE was reported as success")
		}
		got := fakeQuery(t, ctxOne, db)
		if got.conn == before.conn || got.db != home {
			t.Fatalf("after a failed USE: %+v (old connection %d)", got, before.conn)
		}
	})

	for _, end := range []string{"commit", "rollback"} {
		t.Run(end+" fails", func(t *testing.T) {
			tx, err := db.BeginTx(ctxOne, nil)
			if err != nil {
				t.Fatal(err)
			}
			in := fakeQuery(t, ctxOne, tx)
			if !in.inTx || in.db != home {
				t.Fatalf("inside the transaction: %+v", in)
			}
			srv.mu.Lock()
			if end == "commit" {
				srv.failCommit = true
			} else {
				srv.failRollback = true
			}
			srv.mu.Unlock()
			if end == "commit" {
				err = tx.Commit()
			} else {
				err = tx.Rollback()
			}
			if err == nil {
				t.Fatalf("a failed %s was reported as success", end)
			}
			got := fakeQuery(t, ctxB, db)
			if got.conn == in.conn || got.db != other || got.inTx {
				t.Fatalf("after a failed %s, team B got %+v (old connection %d)", end, got, in.conn)
			}
		})
	}
}
