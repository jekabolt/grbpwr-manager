package product

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/jmoiron/sqlx"
)

// ─── a scripted database, so the T45 store decisions run without MySQL ───
//
// The store functions under test take a dependency.DB and speak SQL through it. A *sqlx.DB over
// this driver answers each statement from a script — in order, each step naming a fragment the
// statement must contain — and records everything that reached it. A statement the script did not
// expect fails the test, so «refused before any write» is proved by a script with no write in it.
// No socket, no config, no database: nothing here can reach a real base.

type t45Step struct {
	match    string           // a fragment the statement must contain
	cols     []string         // columns of a read
	rows     [][]driver.Value // rows of a read
	err      error            // the driver's refusal, if any
	affected int64            // rows affected by a write
}

type t45Script struct {
	t     *testing.T
	mu    sync.Mutex
	steps []t45Step
	seen  []string
}

func (s *t45Script) next(query string) t45Step {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, query)
	if len(s.steps) == 0 {
		s.t.Errorf("unexpected statement, the script is over:\n%s", query)
		return t45Step{err: errors.New("unscripted statement")}
	}
	step := s.steps[0]
	s.steps = s.steps[1:]
	if !strings.Contains(query, step.match) {
		s.t.Errorf("statement does not contain %q:\n%s", step.match, query)
		return t45Step{err: errors.New("statement out of script")}
	}
	return step
}

// done fails the test when a scripted step was never reached.
func (s *t45Script) done() {
	s.t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.steps) > 0 {
		s.t.Errorf("%d scripted statement(s) never ran, the first expected to contain %q", len(s.steps), s.steps[0].match)
	}
}

type t45Connector struct{ s *t45Script }

func (c t45Connector) Connect(context.Context) (driver.Conn, error) { return &t45Conn{s: c.s}, nil }
func (c t45Connector) Driver() driver.Driver                        { return t45Driver{s: c.s} }

type t45Driver struct{ s *t45Script }

func (d t45Driver) Open(string) (driver.Conn, error) { return &t45Conn{s: d.s}, nil }

type t45Conn struct{ s *t45Script }

func (c *t45Conn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare is not scripted")
}
func (c *t45Conn) Close() error              { return nil }
func (c *t45Conn) Begin() (driver.Tx, error) { return nil, errors.New("transactions are not scripted") }

func (c *t45Conn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	step := c.s.next(query)
	if step.err != nil {
		return nil, step.err
	}
	return &t45Rows{cols: step.cols, rows: step.rows}, nil
}

func (c *t45Conn) ExecContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Result, error) {
	step := c.s.next(query)
	if step.err != nil {
		return nil, step.err
	}
	return driver.RowsAffected(step.affected), nil
}

type t45Rows struct {
	cols []string
	rows [][]driver.Value
	i    int
}

func (r *t45Rows) Columns() []string { return r.cols }
func (r *t45Rows) Close() error      { return nil }
func (r *t45Rows) Next(dest []driver.Value) error {
	if r.i >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.i])
	r.i++
	return nil
}

// t45DB opens a *sqlx.DB over a script. MySQL placeholders, like the real store.
func t45DB(t *testing.T, steps ...t45Step) (*sqlx.DB, *t45Script) {
	t.Helper()
	s := &t45Script{t: t, steps: steps}
	db := sqlx.NewDb(sql.OpenDB(t45Connector{s: s}), "mysql")
	t.Cleanup(func() { _ = db.Close() })
	return db, s
}

// t45Tokens is the style's token read answering with these tokens.
func t45Tokens(tokens ...string) t45Step {
	rows := make([][]driver.Value, 0, len(tokens))
	for _, tk := range tokens {
		rows = append(rows, []driver.Value{tk})
	}
	return t45Step{match: "COALESCE(sku_color_token, color_code) AS token", cols: []string{"token"}, rows: rows}
}

// t45Dictionary is the colour dictionary read (the seeded 17 would do; these are enough).
func t45Dictionary() t45Step {
	rows := [][]driver.Value{}
	for _, c := range []string{"BLK", "BLU", "BRN", "GRN", "GRY", "NAV", "WHT"} {
		rows = append(rows, []driver.Value{c})
	}
	return t45Step{match: "SELECT code FROM color", cols: []string{"code"}, rows: rows}
}

func t45Count(n int64) t45Step {
	return t45Step{match: "product_colour WHERE product_id", cols: []string{"n"}, rows: [][]driver.Value{{n}}}
}

// t45Dup is the driver text of a MySQL 8 duplicate on one of product's indexes.
func t45Dup(index string) error {
	return fmt.Errorf("Error 1062 (23000): Duplicate entry '12-BKW' for key 'product.%s'", index)
}
