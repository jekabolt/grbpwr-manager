// Package tenantdb is the tenant-routing pool: one shared database/sql pool whose connections switch
// to the database of the team carried by each call's context.
//
// Every connection remembers the database it is on (curDB). Before a statement, a prepare or a
// BEGIN, the connection compares the ctx team's database with curDB and issues USE only when they
// differ. The database name must match the allowlist (team #1's database, or TENANT_DB_PREFIX plus
// eight [a-z0-9]); anything else is refused.
//
// A transaction pins the team it began with: a statement for another team inside it is an error,
// never a silent switch. A prepared statement remembers the database it was prepared on and is
// prepared again when it is run for another team (database/sql reuses a statement per connection,
// whatever ctx prepared it first).
//
// A context without a team:
//   - teams mode ON (Strict): refused with tenant.ErrNoTenant;
//   - teams mode OFF: runs on team #1, and the miss is counted (tenant_missing_ctx_total) and logged
//     at WARN with a stack, at most once a minute — this is the G1 measurement before teams mode is
//     switched on.
//
// Ping is exempt: it touches no table.
package tenantdb

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jmoiron/sqlx"
)

// missingWarnEvery bounds the WARN log for a call without a team (teams mode off).
const missingWarnEvery = time.Minute

// Options configures the routing.
type Options struct {
	// Team1 is team #1. Its DBName must be the database of the DSN: with teams mode off every call
	// runs there and no USE is ever issued, so the binary behaves exactly as before.
	Team1 tenant.Team
	// Strict is teams mode ON: a ctx without a team is refused (tenant.ErrNoTenant).
	Strict bool
	// DBPrefix is TENANT_DB_PREFIX: other teams' databases are DBPrefix + 8 [a-z0-9]. Empty = only
	// team #1's database is allowed.
	DBPrefix string
}

// Stats are the connector's counters since start.
type Stats struct {
	// MissingCtx counts calls that arrived without a team (tenant_missing_ctx_total).
	MissingCtx int64
	// Uses counts USE statements issued (a switch of a connection to another team's database).
	Uses int64
}

// Connector is a driver.Connector around go-sql-driver/mysql that routes each connection to the
// ctx team's database.
type Connector struct {
	inner   driver.Connector
	homeDB  string
	team1   tenant.Team
	strict  bool
	allowed *regexp.Regexp

	missing      atomic.Int64
	uses         atomic.Int64
	lastWarnNano atomic.Int64
}

var errTxTeam = errors.New("tenantdb: statement for another team inside a transaction")

// NewConnector builds the routing connector for dsn.
func NewConnector(dsn string, o Options) (*Connector, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return nil, fmt.Errorf("tenantdb: parse dsn: %w", err)
	}
	if cfg.DBName == "" {
		return nil, errors.New("tenantdb: the dsn names no database (team #1's database is required)")
	}
	if o.Team1.ID != tenant.TeamOneID {
		return nil, fmt.Errorf("tenantdb: Team1 must be team #1, got team %d", o.Team1.ID)
	}
	if o.Team1.DBName != cfg.DBName {
		return nil, fmt.Errorf("tenantdb: team #1's database %q is not the dsn database %q", o.Team1.DBName, cfg.DBName)
	}
	allowed, err := AllowlistPattern(cfg.DBName, o.DBPrefix)
	if err != nil {
		return nil, err
	}
	inner, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("tenantdb: connector: %w", err)
	}
	return &Connector{
		inner:   inner,
		homeDB:  cfg.DBName,
		team1:   o.Team1,
		strict:  o.Strict,
		allowed: allowed,
	}, nil
}

// AllowlistPattern is ^(<team1 db>|<prefix>[a-z0-9]{8})$ (02-DESIGN §1). An empty prefix allows only
// team #1's database.
func AllowlistPattern(team1DB, prefix string) (*regexp.Regexp, error) {
	if !identPattern.MatchString(team1DB) {
		return nil, fmt.Errorf("tenantdb: team #1 database name %q is not a plain identifier", team1DB)
	}
	expr := `^(` + regexp.QuoteMeta(team1DB)
	if prefix != "" {
		if !identPattern.MatchString(prefix) {
			return nil, fmt.Errorf("tenantdb: db prefix %q is not a plain identifier", prefix)
		}
		expr += `|` + regexp.QuoteMeta(prefix) + `[a-z0-9]{8}`
	}
	expr += `)$`
	return regexp.MustCompile(expr), nil
}

// identPattern is what may stand inside the backticks of USE: no backtick, no quote, no dot.
var identPattern = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Open returns the shared pool: sqlx over database/sql over the routing connector.
func Open(dsn string, o Options) (*sqlx.DB, *Connector, error) {
	c, err := NewConnector(dsn, o)
	if err != nil {
		return nil, nil, err
	}
	return sqlx.NewDb(sql.OpenDB(c), "mysql"), c, nil
}

// Connect opens a connection; it starts on the dsn database (team #1).
func (c *Connector) Connect(ctx context.Context) (driver.Conn, error) {
	ic, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &conn{c: c, inner: ic, curDB: c.homeDB}, nil
}

// Driver returns the mysql driver.
func (c *Connector) Driver() driver.Driver { return c.inner.Driver() }

// Stats returns the counters.
func (c *Connector) Stats() Stats {
	return Stats{MissingCtx: c.missing.Load(), Uses: c.uses.Load()}
}

// Strict reports whether teams mode is on.
func (c *Connector) Strict() bool { return c.strict }

// target resolves the database a call with ctx must run on.
func (c *Connector) target(ctx context.Context) (string, error) {
	t, ok := tenant.From(ctx)
	if !ok {
		if c.strict {
			return "", tenant.ErrNoTenant
		}
		n := c.missing.Add(1)
		c.warnMissing(ctx, n)
		return c.homeDB, nil
	}
	if t.DBName == "" {
		return "", fmt.Errorf("tenantdb: team %d has no database", t.ID)
	}
	if !c.allowed.MatchString(t.DBName) {
		return "", fmt.Errorf("tenantdb: database name %q of team %d is outside the allowlist", t.DBName, t.ID)
	}
	return t.DBName, nil
}

func (c *Connector) warnMissing(ctx context.Context, total int64) {
	now := time.Now().UnixNano()
	last := c.lastWarnNano.Load()
	if last != 0 && now-last < int64(missingWarnEvery) {
		return
	}
	if !c.lastWarnNano.CompareAndSwap(last, now) {
		return
	}
	slog.Default().WarnContext(ctx, "tenantdb: database call without a team in ctx; ran on team #1 (teams mode off)",
		slog.Int64("tenant_missing_ctx_total", total),
		slog.String("stack", string(debug.Stack())),
	)
}

// conn is one routed connection. database/sql uses a driver.Conn from one goroutine at a time.
type conn struct {
	c     *Connector
	inner driver.Conn
	// curDB is the database the session is on; "" = unknown (a USE failed), forcing a USE next time.
	curDB string
	// txDB is the database the open transaction is pinned to; "" = no transaction.
	txDB string
}

var (
	_ driver.Conn               = (*conn)(nil)
	_ driver.ExecerContext      = (*conn)(nil)
	_ driver.QueryerContext     = (*conn)(nil)
	_ driver.ConnPrepareContext = (*conn)(nil)
	_ driver.ConnBeginTx        = (*conn)(nil)
	_ driver.SessionResetter    = (*conn)(nil)
	_ driver.Validator          = (*conn)(nil)
	_ driver.Pinger             = (*conn)(nil)
	_ driver.NamedValueChecker  = (*conn)(nil)
)

// route makes the session sit on the ctx team's database, honoring a transaction's pin.
func (cn *conn) route(ctx context.Context) (string, error) {
	db, err := cn.c.target(ctx)
	if err != nil {
		return "", err
	}
	if cn.txDB != "" {
		if db != cn.txDB {
			return "", fmt.Errorf("%w: pinned to %q, asked for %q", errTxTeam, cn.txDB, db)
		}
		return db, nil
	}
	if db == cn.curDB {
		return db, nil
	}
	ex, ok := cn.inner.(driver.ExecerContext)
	if !ok {
		return "", errors.New("tenantdb: driver connection cannot execute USE")
	}
	if _, err := ex.ExecContext(ctx, "USE `"+db+"`", nil); err != nil {
		cn.curDB = ""
		return "", fmt.Errorf("tenantdb: switch to %q: %w", db, err)
	}
	cn.curDB = db
	cn.c.uses.Add(1)
	return db, nil
}

func (cn *conn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	if _, err := cn.route(ctx); err != nil {
		return nil, err
	}
	ex, ok := cn.inner.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return ex.ExecContext(ctx, query, args)
}

func (cn *conn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if _, err := cn.route(ctx); err != nil {
		return nil, err
	}
	q, ok := cn.inner.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	return q.QueryContext(ctx, query, args)
}

func (cn *conn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	db, err := cn.route(ctx)
	if err != nil {
		return nil, err
	}
	is, err := cn.prepareInner(ctx, query)
	if err != nil {
		return nil, err
	}
	return &stmt{cn: cn, query: query, inner: is, db: db}, nil
}

func (cn *conn) prepareInner(ctx context.Context, query string) (driver.Stmt, error) {
	if p, ok := cn.inner.(driver.ConnPrepareContext); ok {
		return p.PrepareContext(ctx, query)
	}
	return cn.inner.Prepare(query)
}

// Prepare carries no ctx and so no team: refused. database/sql always calls PrepareContext.
func (cn *conn) Prepare(query string) (driver.Stmt, error) {
	return nil, errors.New("tenantdb: Prepare without a context is not supported; use PrepareContext")
}

func (cn *conn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if cn.txDB != "" {
		return nil, errors.New("tenantdb: transaction already open on this connection")
	}
	db, err := cn.route(ctx)
	if err != nil {
		return nil, err
	}
	b, ok := cn.inner.(driver.ConnBeginTx)
	if !ok {
		return nil, errors.New("tenantdb: driver connection cannot BeginTx")
	}
	itx, err := b.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	cn.txDB = db
	return &tx{cn: cn, inner: itx}, nil
}

// Begin carries no ctx and so no team: refused. database/sql always calls BeginTx.
func (cn *conn) Begin() (driver.Tx, error) {
	return nil, errors.New("tenantdb: Begin without a context is not supported; use BeginTx")
}

func (cn *conn) Close() error { return cn.inner.Close() }

func (cn *conn) Ping(ctx context.Context) error {
	if p, ok := cn.inner.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (cn *conn) ResetSession(ctx context.Context) error {
	if cn.txDB != "" {
		// A connection back in the pool with an open transaction is broken; drop it.
		return driver.ErrBadConn
	}
	if r, ok := cn.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (cn *conn) IsValid() bool {
	if v, ok := cn.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// CheckNamedValue keeps the mysql driver's argument conversion (uint64, json.RawMessage …).
func (cn *conn) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := cn.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// tx clears the connection's pin when the transaction ends.
type tx struct {
	cn    *conn
	inner driver.Tx
}

func (t *tx) Commit() error {
	err := t.inner.Commit()
	t.cn.txDB = ""
	return err
}

func (t *tx) Rollback() error {
	err := t.inner.Rollback()
	t.cn.txDB = ""
	return err
}

// stmt is a prepared statement bound to the database it was prepared on.
type stmt struct {
	cn    *conn
	query string
	inner driver.Stmt
	db    string
}

var (
	_ driver.StmtExecContext   = (*stmt)(nil)
	_ driver.StmtQueryContext  = (*stmt)(nil)
	_ driver.NamedValueChecker = (*stmt)(nil)
)

// ready routes the connection and prepares the statement again when it was prepared for another
// database than the one this call must run on.
func (s *stmt) ready(ctx context.Context) error {
	db, err := s.cn.route(ctx)
	if err != nil {
		return err
	}
	if db == s.db {
		return nil
	}
	is, err := s.cn.prepareInner(ctx, s.query)
	if err != nil {
		return fmt.Errorf("tenantdb: prepare again for %q: %w", db, err)
	}
	old := s.inner
	s.inner, s.db = is, db
	if cerr := old.Close(); cerr != nil {
		slog.Default().WarnContext(ctx, "tenantdb: closing a statement prepared for another team failed",
			slog.String("err", cerr.Error()))
	}
	return nil
}

func (s *stmt) Close() error  { return s.inner.Close() }
func (s *stmt) NumInput() int { return s.inner.NumInput() }

func (s *stmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if e, ok := s.inner.(driver.StmtExecContext); ok {
		return e.ExecContext(ctx, args)
	}
	return nil, errors.New("tenantdb: driver statement cannot ExecContext")
}

func (s *stmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.ready(ctx); err != nil {
		return nil, err
	}
	if q, ok := s.inner.(driver.StmtQueryContext); ok {
		return q.QueryContext(ctx, args)
	}
	return nil, errors.New("tenantdb: driver statement cannot QueryContext")
}

// Exec / Query without a ctx carry no team; database/sql never calls them when the *Context forms exist.
func (s *stmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("tenantdb: Exec without a context is not supported")
}

func (s *stmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("tenantdb: Query without a context is not supported")
}

func (s *stmt) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return s.cn.CheckNamedValue(nv)
}
