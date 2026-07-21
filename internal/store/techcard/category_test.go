package techcard

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

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jmoiron/sqlx"
)

// syncStyleCategoryTriple is the riskiest part of the category derivation and its ancestor walk needs
// a database, so it is driven here through a minimal in-process SQL driver instead. That is enough to
// pin the three things no DB-free test of the pure classifier can reach: that an unset category_id
// issues NO statements at all (the clobber rule), that a broken category tree is refused WITHOUT a
// partial write, and that a successful derivation writes all three columns including the NULLs.
//
// What this deliberately does NOT verify is the SQL itself -- the recursive ancestor query is handed
// to a fake that returns canned rows, so a mistake in the CTE would not be caught here. That needs a
// real MySQL (the store integration suite, run only against a throwaway container).

type recordedStatement struct {
	query string
	args  []driver.NamedValue
}

// fakeConn records every statement and replays a canned ancestor chain for the SELECT.
type fakeConn struct {
	chain    [][]driver.Value // rows for the ancestor query, as {id, level_id}
	queryErr error
	queries  []recordedStatement
	execs    []recordedStatement
}

func (c *fakeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (c *fakeConn) Close() error              { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) { return nil, errors.New("tx unsupported") }

func (c *fakeConn) QueryContext(_ context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	c.queries = append(c.queries, recordedStatement{query: q, args: args})
	if c.queryErr != nil {
		return nil, c.queryErr
	}
	return &fakeRows{cols: []string{"id", "level_id"}, vals: c.chain}, nil
}

func (c *fakeConn) ExecContext(_ context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	c.execs = append(c.execs, recordedStatement{query: q, args: args})
	return driver.RowsAffected(1), nil
}

type fakeRows struct {
	cols []string
	vals [][]driver.Value
	i    int
}

func (r *fakeRows) Columns() []string { return r.cols }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.i >= len(r.vals) {
		return io.EOF
	}
	copy(dest, r.vals[r.i])
	r.i++
	return nil
}

// The driver is registered once; each test gets its own connection keyed by a unique DSN.
var (
	fakeRegistry   = map[string]*fakeConn{}
	fakeRegistryMu sync.Mutex
	fakeRegisterer sync.Once
	fakeSeq        int
)

type fakeDriver struct{}

func (fakeDriver) Open(dsn string) (driver.Conn, error) {
	fakeRegistryMu.Lock()
	defer fakeRegistryMu.Unlock()
	c, ok := fakeRegistry[dsn]
	if !ok {
		return nil, fmt.Errorf("no fake conn registered for %q", dsn)
	}
	return c, nil
}

// newFakeDB returns a *sqlx.DB backed by conn, satisfying dependency.DB without a database.
func newFakeDB(t *testing.T, conn *fakeConn) *sqlx.DB {
	t.Helper()
	fakeRegisterer.Do(func() { sql.Register("techcardfake", fakeDriver{}) })

	fakeRegistryMu.Lock()
	fakeSeq++
	dsn := fmt.Sprintf("fake-%d", fakeSeq)
	fakeRegistry[dsn] = conn
	fakeRegistryMu.Unlock()
	t.Cleanup(func() {
		fakeRegistryMu.Lock()
		delete(fakeRegistry, dsn)
		fakeRegistryMu.Unlock()
	})

	db, err := sqlx.Open("techcardfake", dsn)
	if err != nil {
		t.Fatalf("open fake db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func nullInt32(v int32) sql.NullInt32 { return sql.NullInt32{Int32: v, Valid: true} }

// TestSyncStyleCategoryTripleLeavesTripleAloneWhenCategoryUnset pins THE CLOBBER RULE, the single
// most consequential decision in this change. Every style created before the derivation existed has
// a triple backfilled from its products and category_id NULL; if an unset category_id cleared the
// triple, the first tech-card save of any such style would destroy a correct category. The assertion
// is deliberately "no statements at all" rather than "no UPDATE" -- the function must not even look.
func TestSyncStyleCategoryTripleLeavesTripleAloneWhenCategoryUnset(t *testing.T) {
	for _, tt := range []struct {
		name       string
		categoryID sql.NullInt32
	}{
		{"null category", sql.NullInt32{}},
		{"zero category", nullInt32(0)},
		{"negative category", nullInt32(-1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			conn := &fakeConn{}
			db := newFakeDB(t, conn)

			if err := syncStyleCategoryTriple(context.Background(), db, 42, tt.categoryID); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(conn.queries) != 0 || len(conn.execs) != 0 {
				t.Errorf("an unset category_id must touch the database not at all, got %d queries and %d execs",
					len(conn.queries), len(conn.execs))
			}
		})
	}
}

// TestSyncStyleCategoryTripleWritesDerivedTriple checks a successful derivation writes ALL THREE
// columns, including the ones that must land as NULL. The dresses case is the one that matters: its
// level-3 types hang directly off the level-1 top category, so sub_category_id must be written as
// NULL rather than left at whatever the row held.
func TestSyncStyleCategoryTripleWritesDerivedTriple(t *testing.T) {
	const (
		tops     = 10
		tshirts  = 20
		crop     = 30
		dresses  = 40
		miniDres = 50
	)
	tests := []struct {
		name                       string
		chain                      [][]driver.Value
		wantTop, wantSub, wantType any
	}{
		{
			name:    "full three level chain",
			chain:   [][]driver.Value{{int64(crop), int64(3)}, {int64(tshirts), int64(2)}, {int64(tops), int64(1)}},
			wantTop: int64(tops), wantSub: int64(tshirts), wantType: int64(crop),
		},
		{
			name:    "dress type writes a NULL sub category",
			chain:   [][]driver.Value{{int64(miniDres), int64(3)}, {int64(dresses), int64(1)}},
			wantTop: int64(dresses), wantSub: nil, wantType: int64(miniDres),
		},
		{
			name:    "top only pick nulls sub and type",
			chain:   [][]driver.Value{{int64(tops), int64(1)}},
			wantTop: int64(tops), wantSub: nil, wantType: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conn := &fakeConn{chain: tt.chain}
			db := newFakeDB(t, conn)

			if err := syncStyleCategoryTriple(context.Background(), db, 7, nullInt32(99)); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(conn.execs) != 1 {
				t.Fatalf("expected exactly one UPDATE, got %d", len(conn.execs))
			}
			got := conn.execs[0]
			for _, col := range []string{"top_category_id", "sub_category_id", "type_id"} {
				if !strings.Contains(got.query, col) {
					t.Errorf("UPDATE must write %s, query was: %s", col, got.query)
				}
			}
			// Bind order follows placeholder order in the statement: top, sub, type, then the row id.
			if len(got.args) != 4 {
				t.Fatalf("expected 4 bound args (top, sub, type, id), got %d: %v", len(got.args), got.args)
			}
			for i, want := range []any{tt.wantTop, tt.wantSub, tt.wantType, int64(7)} {
				if got.args[i].Value != want {
					t.Errorf("arg %d = %#v, want %#v (nil means the column must be written NULL)",
						i, got.args[i].Value, want)
				}
			}
		})
	}
}

// TestSyncStyleCategoryTripleRefusesHeadlessChain pins the other half of the safety story: a category
// whose ancestry reaches no top-level node is a broken tree, and writing the partial path would set
// top_category_id NULL -- which ResolveSizeSystemPolicy reads as "no category assigned" and answers
// with Unrestricted, silently disabling size validation instead of surfacing the breakage. So it must
// fail, and it must fail WITHOUT having written anything.
func TestSyncStyleCategoryTripleRefusesHeadlessChain(t *testing.T) {
	conn := &fakeConn{chain: [][]driver.Value{{int64(30), int64(3)}, {int64(20), int64(2)}}}
	db := newFakeDB(t, conn)

	err := syncStyleCategoryTriple(context.Background(), db, 7, nullInt32(30))
	if err == nil {
		t.Fatal("expected a rejection for a chain with no top-level category")
	}
	var ve *entity.ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("expected a field-tagged *entity.ValidationError so the RPC layer returns InvalidArgument, got %T: %v", err, err)
	}
	if ve.Field != "category_id" {
		t.Errorf("violation field = %q, want %q", ve.Field, "category_id")
	}
	if len(conn.execs) != 0 {
		t.Errorf("a refused derivation must not write a partial path, got %d UPDATEs", len(conn.execs))
	}
}

// TestSyncStyleCategoryTripleWrapsQueryFailure checks a failed ancestor read is surfaced with context
// rather than silently yielding an empty chain (which would otherwise look like a broken tree and
// produce a misleading field violation pointing at the user's category choice).
func TestSyncStyleCategoryTripleWrapsQueryFailure(t *testing.T) {
	conn := &fakeConn{queryErr: errors.New("connection reset")}
	db := newFakeDB(t, conn)

	err := syncStyleCategoryTriple(context.Background(), db, 7, nullInt32(30))
	if err == nil {
		t.Fatal("expected the query failure to propagate")
	}
	var ve *entity.ValidationError
	if errors.As(err, &ve) {
		t.Errorf("an infrastructure failure must not masquerade as a user-facing field violation: %v", err)
	}
	if !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("error must wrap the cause, got: %v", err)
	}
	if len(conn.execs) != 0 {
		t.Errorf("nothing must be written when the ancestor read failed, got %d UPDATEs", len(conn.execs))
	}
}
