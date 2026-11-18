package provision_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/provision"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

// Databases of this package only: the store tests own grbpwr_test.
const (
	itCP     = "provision_cp_test"
	itOne    = "provision_one_test"
	itPrefix = "prvt_"
)

func itDSN(t *testing.T, db string) string {
	t.Helper()
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("CI set but MYSQL_HOST empty")
		}
		t.Skip("no test MySQL (run through test-db.sh)")
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true",
		os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

func TestMain(m *testing.M) {
	os.Exit(serialized(m))
}

// serialized holds the server-wide lock shared with the tenantdb / teamsync tests: the disposable
// test MySQL keeps its data in memory, and two packages migrating full team schemas at once can
// exhaust it.
func serialized(m *testing.M) int {
	host := os.Getenv("MYSQL_HOST")
	if host == "" {
		return m.Run()
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	db, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s:%s)/", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK('teams_schema_tests', 900)`).Scan(&got); err != nil || got.Int64 != 1 {
		fmt.Fprintf(os.Stderr, "test schema lock: %v %v\n", got, err)
		return 1
	}
	defer conn.ExecContext(ctx, `SELECT RELEASE_LOCK('teams_schema_tests')`)
	return m.Run()
}

// itEnv is a fresh control plane, an empty team #1 database and no prvt_ databases.
type itEnv struct {
	root *sql.DB
	pf   *platformstore.Store
}

func newEnv(t *testing.T, ctx context.Context) *itEnv {
	t.Helper()
	if err := testdbguard.CheckMySQLDSN(itDSN(t, itCP)); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", itDSN(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	dropPrefixed := func() {
		rows, err := root.Query(`SHOW DATABASES LIKE 'prvt\_%'`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			names = append(names, n)
		}
		rows.Close()
		for _, n := range names {
			if err := store.NewTeamDBName(itPrefix, itOne, n); err != nil {
				t.Fatalf("unexpected database %q: %v", n, err)
			}
			if _, err := root.Exec("DROP DATABASE `" + n + "`"); err != nil {
				t.Fatal(err)
			}
		}
	}
	dropPrefixed()
	for _, db := range []string{itCP, itOne} {
		for _, q := range []string{"DROP DATABASE IF EXISTS `" + db + "`", "CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4"} {
			if _, err := root.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		dropPrefixed()
		for _, db := range []string{itCP, itOne} {
			_, _ = root.Exec("DROP DATABASE IF EXISTS `" + db + "`")
		}
	})
	pf, err := platformstore.Open(ctx, itDSN(t, itCP))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Close() })
	// Team #1 exists in every control plane (its row names the grbpwr database).
	if _, err := pf.DB().ExecContext(ctx, `INSERT INTO team (id, slug, name, db_name, status) VALUES (1, 'grbpwr', 'grbpwr', ?, 'active')`, itOne); err != nil {
		t.Fatal(err)
	}
	return &itEnv{root: root, pf: pf}
}

// team inserts a user and a team in state status with that user as owner.
func (e *itEnv) team(t *testing.T, slug, handle, status string) int64 {
	t.Helper()
	db := e.pf.DB()
	res, err := db.Exec(`INSERT INTO platform_user (email, handle, password_hash, status) VALUES (?, ?, 'x', ?)`,
		handle+"@example.com", handle, map[string]string{"pending": "pending"}[status]+map[bool]string{true: "active"}[status != "pending"])
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	res, err = db.Exec(`INSERT INTO team (slug, name, status, created_by, pending_expires_at) VALUES (?, ?, ?, ?, ?)`,
		slug, strings.ToUpper(slug), status, uid, time.Now().UTC().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	tid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO team_member (team_id, user_id, role, status) VALUES (?, ?, 'owner', 'active')`, tid, uid); err != nil {
		t.Fatal(err)
	}
	return tid
}

// TestSpareClaimSkipLocked: two workers claiming at once take two different spares (control plane
// only: the spare rows name no real database).
func TestSpareClaimSkipLocked(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	for _, n := range []string{"prvt_aaaaaaa1", "prvt_aaaaaaa2", "prvt_aaaaaaa3"} {
		if err := e.pf.InsertSpare(ctx, n); err != nil {
			t.Fatal(err)
		}
		if err := e.pf.MarkSpareReady(ctx, n, "v1", strings.Repeat("a", 64)); err != nil {
			t.Fatal(err)
		}
	}
	a := e.team(t, "alpha", "kate", "provisioning")
	b := e.team(t, "bravo", "ann", "provisioning")
	var wg sync.WaitGroup
	got := make([]string, 2)
	errs := make([]error, 2)
	for i, id := range []int64{a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got[i], errs[i] = e.pf.ClaimSpare(ctx, id, "v1")
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || got[0] == got[1] || got[0] == "" {
		t.Fatalf("claims %v %v", got, errs)
	}
	// A rerun returns the same database; a wrong version finds nothing.
	if again, err := e.pf.ClaimSpare(ctx, a, "v1"); err != nil || again != got[0] {
		t.Fatalf("rerun %q %v", again, err)
	}
	c := e.team(t, "charlie", "bob", "provisioning")
	if _, err := e.pf.ClaimSpare(ctx, c, "v2"); !errors.Is(err, platformstore.ErrNoSpare) {
		t.Fatalf("wrong version: %v", err)
	}
	var fileKey sql.NullString
	if err := e.pf.DB().Get(&fileKey, `SELECT file_key FROM team WHERE id = ?`, a); err != nil || len(fileKey.String) != 32 {
		t.Fatalf("file_key %q %v", fileKey.String, err)
	}
	if _, err := e.pf.ClaimSpare(ctx, tenant.TeamOneID, "v1"); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("claim for a missing team: %v", err)
	}
	cnt, err := e.pf.CountSpares(ctx, "v1", time.Now().Add(-time.Hour))
	if err != nil || cnt.Ready != 1 || cnt.CreatedSince != 3 {
		t.Fatalf("counts %+v %v", cnt, err)
	}
}

// TestProvisionPendingSweep: an expired sign-up is removed with its pending owner; a pending user
// who also belongs elsewhere, and unexpired sign-ups, stay.
func TestProvisionPendingSweep(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	old := e.team(t, "oldsign", "olduser", "pending")
	fresh := e.team(t, "freshsign", "freshuser", "pending")
	if _, err := e.pf.DB().Exec(`UPDATE team SET pending_expires_at = ? WHERE id = ?`, time.Now().UTC().Add(time.Hour), fresh); err != nil {
		t.Fatal(err)
	}
	n, err := e.pf.SweepPendingTeams(ctx, time.Now(), 10)
	if err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	var teams, users, audits int
	_ = e.pf.DB().Get(&teams, `SELECT COUNT(*) FROM team WHERE id IN (?, ?)`, old, fresh)
	_ = e.pf.DB().Get(&users, `SELECT COUNT(*) FROM platform_user WHERE handle IN ('olduser','freshuser')`)
	_ = e.pf.DB().Get(&audits, `SELECT COUNT(*) FROM platform_audit WHERE action = 'team_pending_expired' AND team_id = ?`, old)
	if teams != 1 || users != 1 || audits != 1 {
		t.Fatalf("teams %d users %d audits %d", teams, users, audits)
	}
	if n, err := e.pf.SweepPendingTeams(ctx, time.Now(), 10); err != nil || n != 0 {
		t.Fatalf("second sweep %d %v", n, err)
	}
}

// TestProvisionTeamEndToEnd migrates two real team schemas, no more (the in-memory test MySQL is
// small and go test's package timeout counts the wait for the shared schema lock): the keeper
// creates a spare, the fleet migrator brings it from N-1 to N, two teams wait at once — one takes the
// spare, the other goes through the cold path — and both end on the reference fingerprint. The
// spare team then: owner is the only admin, seed rerun is a no-op, no grbpwr text, the seven basic
// actions work. Team #1's database is never touched.
func TestProvisionTeamEndToEnd(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	base := itDSN(t, itOne)

	cfg := store.Config{DSN: base, MaxOpenConnections: 3, MaxIdleConnections: 3}
	cfg.Tenant.Team1 = tenant.TeamOne(itOne)
	cfg.Tenant.DBPrefix = itPrefix
	cfg.Tenant.Strict = true
	ms, err := store.NewForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	schema := provision.StoreSchema{BaseDSN: base, Prefix: itPrefix}
	newW := func(spares int) *provision.Worker {
		w, err := provision.New(provision.Config{Prefix: itPrefix, Team1DB: itOne, Spares: spares, PerHour: 10}, e.pf, schema, ms.DB(), ms.Admin())
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	latest := store.LatestMigrationID()

	// ── keeper: one spare at latest, fingerprinted ──
	w := newW(1)
	for range 2 {
		if err := w.KeepSparesOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	spares, err := e.pf.ReadySpares(ctx)
	if err != nil || len(spares) != 1 || spares[0].SchemaVersion.String != latest || len(spares[0].SchemaFingerprint.String) != 64 {
		t.Fatalf("ready spares %+v %v", spares, err)
	}
	refFP := spares[0].SchemaFingerprint.String
	if fp, ok, err := e.pf.ReferenceFingerprint(ctx, latest); err != nil || !ok || fp != refFP {
		t.Fatalf("reference %q %v %v", fp, ok, err)
	}
	if sum, err := store.SchemaFingerprint(ctx, base, spares[0].DBName); err != nil || sum != refFP {
		t.Fatalf("SchemaFingerprint %q %v, keeper wrote %q", sum, err, refFP)
	}

	// ── migrator: the spare from N-1 to N (the latest migration is idempotent) ──
	behind := spares[0].DBName
	spareDB, err := sql.Open("mysql", itDSN(t, behind))
	if err != nil {
		t.Fatal(err)
	}
	defer spareDB.Close()
	if _, err := spareDB.Exec(`DELETE FROM gorp_migrations WHERE id = ?`, latest); err != nil {
		t.Fatal(err)
	}
	if err := e.pf.SetSpareSchema(ctx, behind, "previous", refFP); err != nil {
		t.Fatal(err)
	}
	// Team #1 here is an empty database: the fleet only warns about it (drift), never fails on it.
	if err := w.MigrateFleetOnce(ctx); err != nil {
		t.Fatalf("fleet: %v", err)
	}
	var applied int
	if err := spareDB.QueryRow(`SELECT COUNT(*) FROM gorp_migrations WHERE id = ?`, latest).Scan(&applied); err != nil || applied != 1 {
		t.Fatalf("latest migration re-applied: %d %v", applied, err)
	}
	if s, _ := e.pf.ReadySpares(ctx); len(s) != 1 || s[0].SchemaVersion.String != latest || s[0].SchemaFingerprint.String != refFP {
		t.Fatalf("after fleet %+v", s)
	}

	// ── two teams at once: one from the spare, one through the cold path ──
	alpha := e.team(t, "alpha", "kate", "provisioning")
	bravo := e.team(t, "bravo", "ann", "provisioning")
	if err := w.ClaimOnce(ctx); err != nil {
		t.Fatal(err)
	}
	dir := e.pf.Directory()
	byDB := map[string]int64{}
	for _, id := range []int64{alpha, bravo} {
		dir.Invalidate(id)
		tm, err := dir.ByID(ctx, id)
		if err != nil || tm.Status != tenant.StatusActive || tm.SchemaVersion != latest || tm.DBName == "" {
			t.Fatalf("team %d %+v %v", id, tm, err)
		}
		var fp string
		_ = e.pf.DB().Get(&fp, `SELECT schema_fingerprint FROM team WHERE id = ?`, id)
		if fp != refFP {
			t.Fatalf("team %d fingerprint %s, reference %s (spare and cold-path schemas must match)", id, fp, refFP)
		}
		byDB[tm.DBName] = id
	}
	if len(byDB) != 2 {
		t.Fatal("two teams on one database")
	}
	spareTeam, ok := byDB[behind]
	if !ok {
		t.Fatalf("no team took the spare %s: %v", behind, byDB)
	}
	ownerOf := map[int64]string{alpha: "kate", bravo: "ann"}
	owner := ownerOf[spareTeam]

	tmA, _ := dir.ByID(ctx, spareTeam)
	ctxA := tenant.With(ctx, tmA)
	teamDB, err := sql.Open("mysql", itDSN(t, tmA.DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer teamDB.Close()

	// The owner is the only admin, a super.
	var admins, supers int
	_ = teamDB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(is_super), 0) FROM admins`).Scan(&admins, &supers)
	var admin string
	_ = teamDB.QueryRow(`SELECT username FROM admins LIMIT 1`).Scan(&admin)
	if admins != 1 || supers != 1 || admin != owner {
		t.Fatalf("admins %d supers %d admin %q, owner %q", admins, supers, admin, owner)
	}

	// Seed rerun is a no-op.
	before := rowCounts(t, teamDB)
	if err := provision.Seed(ctxA, ms.DB(), ms.Admin(), owner); err != nil {
		t.Fatal(err)
	}
	after := rowCounts(t, teamDB)
	if d := diffCounts(before, after); d != "" {
		t.Fatalf("seed rerun changed rows: %s", d)
	}

	// No grbpwr text anywhere in the fresh team.
	if hits := grbpwrHits(t, teamDB); len(hits) > 0 {
		t.Fatalf("grbpwr text in a fresh team: %v", hits)
	}

	// Seed is refused on team #1, whatever ctx asks.
	if err := provision.Seed(tenant.With(ctx, tenant.TeamOne(itOne)), ms.DB(), ms.Admin(), "kate"); err == nil {
		t.Fatal("seed ran on team #1")
	}

	// The seven basic actions.
	sevenActions(t, ctxA, ms, ms.DB(), owner)

	// Team #1's database was never created, migrated or seeded: it is still empty.
	var t1Tables int
	_ = e.root.QueryRow(`SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = ?`, itOne).Scan(&t1Tables)
	if t1Tables != 0 {
		t.Fatalf("team #1's database got %d tables", t1Tables)
	}
}

func rowCounts(t *testing.T, db *sql.DB) map[string]int64 {
	t.Helper()
	rows, err := db.Query(`SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_TYPE = 'BASE TABLE'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	out := map[string]int64{}
	for _, tb := range tables {
		var n int64
		if err := db.QueryRow("SELECT COUNT(*) FROM `" + tb + "`").Scan(&n); err != nil {
			t.Fatalf("count %s: %v", tb, err)
		}
		out[tb] = n
	}
	return out
}

func diffCounts(a, b map[string]int64) string {
	var d []string
	for k, v := range a {
		if b[k] != v {
			d = append(d, fmt.Sprintf("%s %d→%d", k, v, b[k]))
		}
	}
	sort.Strings(d)
	return strings.Join(d, ", ")
}

// grbpwrAllowed lists table.column pairs that may name grbpwr in a fresh team: technical
// identifiers (media types, format names) that the code matches on, never brand content shown to a
// workshop.
var grbpwrAllowed = map[string]bool{}

// grbpwrHits returns table.column (rows) of every text column holding "grbpwr", case-insensitive.
func grbpwrHits(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT c.TABLE_NAME, c.COLUMN_NAME FROM information_schema.COLUMNS c
JOIN information_schema.TABLES tb ON tb.TABLE_SCHEMA = c.TABLE_SCHEMA AND tb.TABLE_NAME = c.TABLE_NAME
WHERE c.TABLE_SCHEMA = DATABASE() AND tb.TABLE_TYPE = 'BASE TABLE' AND c.TABLE_NAME <> 'gorp_migrations'
  AND c.DATA_TYPE IN ('char','varchar','tinytext','text','mediumtext','longtext','json','enum','set')`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		_ = rows.Scan(&c.table, &c.column)
		cols = append(cols, c)
	}
	rows.Close()
	var hits []string
	for _, c := range cols {
		if grbpwrAllowed[c.table+"."+c.column] {
			continue
		}
		var n int
		q := "SELECT COUNT(*) FROM `" + c.table + "` WHERE LOWER(CAST(`" + c.column + "` AS CHAR)) LIKE '%grbpwr%'"
		if err := db.QueryRow(q).Scan(&n); err != nil {
			t.Fatalf("scan %s.%s: %v", c.table, c.column, err)
		}
		if n > 0 {
			hits = append(hits, fmt.Sprintf("%s.%s (%d)", c.table, c.column, n))
		}
	}
	sort.Strings(hits)
	return hits
}
