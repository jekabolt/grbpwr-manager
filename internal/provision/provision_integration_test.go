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
		if err := e.pf.ReserveSpare(ctx, n, "v1", 10, 0); err != nil {
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
			got[i], _, errs[i] = e.pf.ClaimSpare(ctx, id, "v1")
		}()
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil || got[0] == got[1] || got[0] == "" {
		t.Fatalf("claims %v %v", got, errs)
	}
	// A rerun returns the same database; a wrong version finds nothing.
	if again, claimed, err := e.pf.ClaimSpare(ctx, a, "v1"); err != nil || again != got[0] || claimed {
		t.Fatalf("rerun %q %v %v", again, claimed, err)
	}
	c := e.team(t, "charlie", "bob", "provisioning")
	if _, _, err := e.pf.ClaimSpare(ctx, c, "v2"); !errors.Is(err, platformstore.ErrNoSpare) {
		t.Fatalf("wrong version: %v", err)
	}
	var fileKey sql.NullString
	if err := e.pf.DB().Get(&fileKey, `SELECT file_key FROM team WHERE id = ?`, a); err != nil || len(fileKey.String) != 32 {
		t.Fatalf("file_key %q %v", fileKey.String, err)
	}
	if _, _, err := e.pf.ClaimSpare(ctx, tenant.TeamOneID, "v1"); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("claim for a missing team: %v", err)
	}
	cnt, err := e.pf.CountSpares(ctx, "v1", time.Now().Add(-time.Hour))
	if err != nil || cnt.Ready != 1 || cnt.CreatedSince != 3 {
		t.Fatalf("counts %+v %v", cnt, err)
	}
}

// TestProvisionLease: one holder per key; a live lease is not taken, an expired one is; the old
// holder can neither renew nor release it; guarded writes follow the token (control plane only).
func TestProvisionLease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	key := platformstore.TeamLeaseKey(42)
	first, ok, err := e.pf.AcquireLease(ctx, key, time.Minute)
	if err != nil || !ok || len(first) != 32 {
		t.Fatalf("first %q %v %v", first, ok, err)
	}
	if _, ok, err := e.pf.AcquireLease(ctx, key, time.Minute); err != nil || ok {
		t.Fatalf("second holder got a live lease: %v %v", ok, err)
	}
	if err := e.pf.ExtendLease(ctx, key, first, time.Minute); err != nil {
		t.Fatalf("holder cannot renew: %v", err)
	}
	// Expire it: the next worker takes over, the old token is dead.
	if _, err := e.pf.DB().Exec(`UPDATE provision_lease SET expires_at = UTC_TIMESTAMP(3) - INTERVAL 1 SECOND WHERE lease_key = ?`, key); err != nil {
		t.Fatal(err)
	}
	second, ok, err := e.pf.AcquireLease(ctx, key, time.Minute)
	if err != nil || !ok || second == first {
		t.Fatalf("expired lease not re-taken: %q %v %v", second, ok, err)
	}
	if err := e.pf.ExtendLease(ctx, key, first, time.Minute); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("old holder renewed: %v", err)
	}
	if err := e.pf.ReleaseLease(ctx, key, first); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.pf.AcquireLease(ctx, key, time.Minute); ok {
		t.Fatal("old holder's release freed the new holder's lease")
	}
	if err := e.pf.ReleaseLease(ctx, key, second); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := e.pf.AcquireLease(ctx, key, time.Minute); !ok {
		t.Fatal("released lease not free")
	}
	if _, ok, err := e.pf.AcquireLease(ctx, "spare-quota", time.Minute); ok || err == nil {
		t.Fatal("the quota row handed out as a lease")
	}

	// Activation needs the team's lease token, and the guard sees provisioning + database.
	tid := e.team(t, "leased", "lena", "provisioning")
	if _, err := e.pf.DB().Exec(`UPDATE team SET db_name = 'prvt_leased01' WHERE id = ?`, tid); err != nil {
		t.Fatal(err)
	}
	tok, ok, err := e.pf.AcquireLease(ctx, platformstore.TeamLeaseKey(tid), time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := e.pf.CheckProvisionLease(ctx, tid, "prvt_leased01", "wrong"); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("guard with a wrong token: %v", err)
	}
	if err := e.pf.CheckProvisionLease(ctx, tid, "prvt_other001", tok); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("guard with another database: %v", err)
	}
	if err := e.pf.CheckProvisionLease(ctx, tid, "prvt_leased01", tok); err != nil {
		t.Fatalf("guard: %v", err)
	}
	if err := e.pf.ActivateTeam(ctx, tid, "wrong", "v1", strings.Repeat("a", 64)); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("activate with a wrong token: %v", err)
	}
	if err := e.pf.ActivateTeam(ctx, tid, tok, "v1", strings.Repeat("a", 64)); err != nil {
		t.Fatalf("activate: %v", err)
	}
	if err := e.pf.ActivateTeam(ctx, tid, tok, "v1", strings.Repeat("a", 64)); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("second activate: %v", err)
	}
	if err := e.pf.CheckProvisionLease(ctx, tid, "prvt_leased01", tok); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("guard on an active team: %v", err)
	}
	// Schema writes need the database's lease.
	if err := e.pf.SetTeamSchema(ctx, tid, "prvt_leased01", tok, "v2", ""); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("schema write under the team lease, not the database's: %v", err)
	}
	dbTok, ok, err := e.pf.AcquireLease(ctx, platformstore.DBLeaseKey("prvt_leased01"), time.Minute)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := e.pf.SetTeamSchema(ctx, tid, "prvt_leased01", dbTok, "v2", ""); err != nil {
		t.Fatal(err)
	}
	var v string
	_ = e.pf.DB().Get(&v, `SELECT schema_version FROM team WHERE id = ?`, tid)
	if v != "v2" {
		t.Fatalf("schema_version %q", v)
	}
}

// TestSpareQuota: the hourly cap is one reservation shared by every caller — eight concurrent
// reservations under a cap of three get exactly three; failed rows count; the keeper stops at
// its spare count (control plane only).
// TestSpareBreaker: three failed spares of one version stop reservations for that version (control
// plane only); another version, or clearing a failed row, opens the way again.
func TestSpareBreaker(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	for _, n := range []string{"prvt_brk00001", "prvt_brk00002", "prvt_brk00003"} {
		if err := e.pf.ReserveSpare(ctx, n, "v1", 10, 0); err != nil {
			t.Fatal(err)
		}
		if err := e.pf.MarkSpareFailed(ctx, n, "migrate: boom"); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.pf.ReserveSpare(ctx, "prvt_brk00004", "v1", 10, 0); !errors.Is(err, platformstore.ErrSpareBreaker) {
		t.Fatalf("fourth spare of a failing version: %v", err)
	}
	var fourth int
	if err := e.pf.DB().Get(&fourth, `SELECT COUNT(*) FROM spare_db WHERE db_name = 'prvt_brk00004'`); err != nil || fourth != 0 {
		t.Fatalf("fourth row %d %v", fourth, err)
	}
	if err := e.pf.ReserveSpare(ctx, "prvt_brk00005", "v2", 10, 0); err != nil {
		t.Fatalf("another version: %v", err)
	}
	if _, err := e.pf.DB().Exec(`DELETE FROM spare_db WHERE db_name = 'prvt_brk00003'`); err != nil {
		t.Fatal(err)
	}
	if err := e.pf.ReserveSpare(ctx, "prvt_brk00006", "v1", 10, 0); err != nil {
		t.Fatalf("after an operator cleared a failed spare: %v", err)
	}
}

func TestSpareQuota(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	e := newEnv(t, ctx)
	if err := e.pf.ReserveSpare(ctx, "prvt_failed01", "v1", 3, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.pf.MarkSpareFailed(ctx, "prvt_failed01", "boom"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = e.pf.ReserveSpare(ctx, fmt.Sprintf("prvt_quota%03d", i), "v1", 3, 0)
		}()
	}
	wg.Wait()
	okN, quotaN := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			okN++
		case errors.Is(err, platformstore.ErrSpareQuota):
			quotaN++
		default:
			t.Fatal(err)
		}
	}
	if okN != 2 || quotaN != 6 {
		t.Fatalf("reserved %d, refused %d (cap 3, one failed row already counted)", okN, quotaN)
	}
	// The keeper: two creating spares already, keep 2 → enough; keep 5 → the cap still refuses.
	if err := e.pf.ReserveSpare(ctx, "prvt_keeper01", "v1", 100, 2); !errors.Is(err, platformstore.ErrEnoughSpares) {
		t.Fatalf("keeper over its count: %v", err)
	}
	if err := e.pf.ReserveSpare(ctx, "prvt_keeper01", "v1", 3, 5); !errors.Is(err, platformstore.ErrSpareQuota) {
		t.Fatalf("keeper past the cap: %v", err)
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
		w, err := provision.New(provision.Config{Prefix: itPrefix, Team1DB: itOne, Spares: spares, PerHour: 10}, e.pf, schema, ms.DB())
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
	if err := e.pf.SetSpareSchema(ctx, behind, "no-lease", "previous", refFP); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("schema write without the lease: %v", err)
	}
	tok, ok, err := e.pf.AcquireLease(ctx, platformstore.DBLeaseKey(behind), time.Minute)
	if err != nil || !ok {
		t.Fatalf("lease %v %v", ok, err)
	}
	if err := e.pf.SetSpareSchema(ctx, behind, tok, "previous", refFP); err != nil {
		t.Fatal(err)
	}
	if err := e.pf.ReleaseLease(ctx, platformstore.DBLeaseKey(behind), tok); err != nil {
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

	// The live team is never scrubbed again: Seed refuses on an active team, and even when the
	// caller forges the directory status, the lease guard (team not provisioning) stops it before
	// the first statement. No row changes.
	before := rowCounts(t, teamDB)
	teamTok, ok, err := e.pf.AcquireLease(ctx, platformstore.TeamLeaseKey(spareTeam), time.Minute)
	if err != nil || !ok {
		t.Fatalf("team lease %v %v", ok, err)
	}
	guard := func(ctx context.Context) error { return e.pf.CheckProvisionLease(ctx, spareTeam, tmA.DBName, teamTok) }
	if err := provision.Seed(ctxA, ms.DB(), owner, guard); err == nil {
		t.Fatal("seed ran on an active team")
	}
	forged := tmA
	forged.Status = tenant.StatusProvisioning
	if err := provision.Seed(tenant.With(ctx, forged), ms.DB(), owner, guard); !errors.Is(err, platformstore.ErrNotProvisioning) {
		t.Fatalf("seed on an active team behind a forged status: %v", err)
	}
	if err := e.pf.ActivateTeam(ctx, spareTeam, "not-the-token", latest, refFP); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("activate without the lease: %v", err)
	}
	_ = e.pf.ReleaseLease(ctx, platformstore.TeamLeaseKey(spareTeam), teamTok)
	after := rowCounts(t, teamDB)
	if d := diffCounts(before, after); d != "" {
		t.Fatalf("a refused seed changed rows: %s", d)
	}

	// The database lock on the real server (fixes 1 and 2). A live row the scrub would delete.
	if _, err := teamDB.Exec(`INSERT INTO file_topic (name) VALUES ('live topic')`); err != nil {
		t.Fatal(err)
	}
	liveTopics := func() int {
		var n int
		_ = teamDB.QueryRow(`SELECT COUNT(*) FROM file_topic WHERE name = 'live topic'`).Scan(&n)
		return n
	}
	pt := platformstore.ProvisioningTeam{ID: spareTeam, Slug: tmA.Slug, OwnerHandle: owner}
	staleTok, ok, err := e.pf.AcquireLease(ctx, platformstore.TeamLeaseKey(spareTeam), time.Minute)
	if err != nil || !ok {
		t.Fatalf("stale lease %v %v", ok, err)
	}
	if _, err := e.pf.DB().Exec(`UPDATE provision_lease SET expires_at = UTC_TIMESTAMP(3) - INTERVAL 1 SECOND WHERE lease_key = ?`, platformstore.TeamLeaseKey(spareTeam)); err != nil {
		t.Fatal(err)
	}
	freshTok, ok, err := e.pf.AcquireLease(ctx, platformstore.TeamLeaseKey(spareTeam), time.Minute)
	if err != nil || !ok {
		t.Fatalf("fresh lease %v %v", ok, err)
	}
	// While another holder has the database lock, a worker that lost its lease does not even start,
	// and a second holder cannot take the lock (a second migrator refuses the same way).
	held, ok, err := store.LockTeamDB(ctx, base, itPrefix, tmA.DBName)
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	if _, ok, err := store.LockTeamDB(ctx, base, itPrefix, tmA.DBName); err != nil || ok {
		t.Fatalf("second holder took a held database lock: %v %v", ok, err)
	}
	if err := w.SeedAndActivateForTest(ctx, pt, staleTok, tmA.DBName, true); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("seed under another holder's lock: %v", err)
	}
	held.Release(ctx)
	// With the lock free, the control row is re-read inside it: the stale holder and even the current
	// lease holder find the team active and run nothing.
	if err := w.SeedAndActivateForTest(ctx, pt, staleTok, tmA.DBName, true); !errors.Is(err, platformstore.ErrLeaseLost) {
		t.Fatalf("stale holder on an active team: %v", err)
	}
	if err := w.SeedAndActivateForTest(ctx, pt, freshTok, tmA.DBName, true); err != nil {
		t.Fatalf("lease holder on an active team: %v", err)
	}
	_ = e.pf.ReleaseLease(ctx, platformstore.TeamLeaseKey(spareTeam), freshTok)
	if liveTopics() != 1 {
		t.Fatal("a live row was deleted from an activated team database")
	}
	// The migration runs on the lock's own session: with gorp_migrations write-locked, the migrator
	// waits — on the very connection that holds the lock. Killing that connection ends the run, and a
	// second holder gets the lock only once nothing of the first run is left on the server. Before the
	// fix the DDL ran on a pool of its own and kept waiting after its lock was gone.
	blocker, err := e.root.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.ExecContext(ctx, "LOCK TABLES `"+tmA.DBName+"`.gorp_migrations WRITE"); err != nil {
		t.Fatal(err)
	}
	mig, ok, err := store.LockTeamDB(ctx, base, itPrefix, tmA.DBName)
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	var migLock int64
	if err := e.root.QueryRow(`SELECT IS_USED_LOCK(?)`, store.TeamLockName(tmA.DBName)).Scan(&migLock); err != nil || migLock == 0 {
		t.Fatalf("lock owner %d %v", migLock, err)
	}
	migErr := make(chan error, 1)
	go func() { _, err := mig.Migrate(ctx); migErr <- err }()
	waitingOn := func() (ids []int64) {
		rows, err := e.root.Query(`SELECT ID FROM information_schema.PROCESSLIST WHERE DB = ? AND STATE = 'Waiting for table metadata lock'`, tmA.DBName)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			_ = rows.Scan(&id)
			ids = append(ids, id)
		}
		return ids
	}
	var waiting []int64
	for deadline := time.Now().Add(15 * time.Second); len(waiting) == 0; time.Sleep(50 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the migration never reached gorp_migrations")
		}
		waiting = waitingOn()
	}
	if len(waiting) != 1 || waiting[0] != migLock {
		t.Fatalf("migration statements wait on connections %v, the lock is held by %d", waiting, migLock)
	}
	if _, err := e.root.Exec(fmt.Sprintf("KILL %d", migLock)); err != nil {
		t.Fatal(err)
	}
	var second *store.TeamDBLock
	for deadline := time.Now().Add(10 * time.Second); second == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the lock never came free after its connection was killed")
		}
		if second, ok, err = store.LockTeamDB(ctx, base, itPrefix, tmA.DBName); err != nil {
			t.Fatal(err)
		} else if !ok {
			second = nil
		}
	}
	if left := waitingOn(); len(left) != 0 {
		t.Fatalf("a second holder has the lock while the first run still waits on %v", left)
	}
	select {
	case err := <-migErr:
		if err == nil {
			t.Fatal("a migration whose lock connection was killed reported success")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the migration never ended after its lock connection was killed")
	}
	mig.Release(ctx)
	second.Release(ctx)
	if _, err := blocker.ExecContext(ctx, `UNLOCK TABLES`); err != nil {
		t.Fatal(err)
	}
	_ = blocker.Close()

	// The fence dies with its connection: once killed, statements on it fail, Migrate refuses, and
	// the next holder gets the lock.
	lost, ok, err := store.LockTeamDB(ctx, base, itPrefix, tmA.DBName)
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	var lockConn int64
	if err := e.root.QueryRow(`SELECT IS_USED_LOCK(?)`, store.TeamLockName(tmA.DBName)).Scan(&lockConn); err != nil || lockConn == 0 {
		t.Fatalf("lock owner %d %v", lockConn, err)
	}
	if _, err := e.root.Exec(fmt.Sprintf("KILL %d", lockConn)); err != nil {
		t.Fatal(err)
	}
	if err := lost.Alive(ctx); !errors.Is(err, store.ErrTeamLockLost) {
		t.Fatalf("killed lock alive: %v", err)
	}
	if _, err := lost.ExecContext(ctx, `DELETE FROM file_topic`); err == nil {
		t.Fatal("a statement ran on a killed lock connection")
	}
	if _, err := lost.Migrate(ctx); !errors.Is(err, store.ErrTeamLockLost) {
		t.Fatalf("migrate under a killed lock: %v", err)
	}
	// A stale seeder cannot touch the owner's admin row through a dead lock, even when its guard
	// still passes (the race between a check and the next statement): the row stays as it is.
	if _, err := teamDB.Exec(`UPDATE admins SET disabled = TRUE, is_super = FALSE WHERE username = ?`, owner); err != nil {
		t.Fatal(err)
	}
	if err := provision.Seed(tenant.With(ctx, forged), lost, owner, func(context.Context) error { return nil }); err == nil {
		t.Fatal("seed ran through a killed lock")
	}
	var disabled, super bool
	if err := teamDB.QueryRow(`SELECT disabled, is_super FROM admins WHERE username = ?`, owner).Scan(&disabled, &super); err != nil || !disabled || super {
		t.Fatalf("a stale seeder changed the owner's row: disabled %v super %v %v", disabled, super, err)
	}
	if _, err := teamDB.Exec(`UPDATE admins SET disabled = FALSE, is_super = TRUE WHERE username = ?`, owner); err != nil {
		t.Fatal(err)
	}
	lost.Release(ctx)
	next, ok, err := store.LockTeamDB(ctx, base, itPrefix, tmA.DBName)
	if err != nil || !ok {
		t.Fatalf("lock after the holder died %v %v", ok, err)
	}
	if v, err := next.Migrate(ctx); err != nil || v != latest {
		t.Fatalf("migrate under the lock %q %v", v, err)
	}
	next.Release(ctx)
	if liveTopics() != 1 {
		t.Fatal("a live row was deleted")
	}
	if _, err := teamDB.Exec(`DELETE FROM file_topic WHERE name = 'live topic'`); err != nil {
		t.Fatal(err)
	}

	// No grbpwr text anywhere in the fresh team.
	if hits := grbpwrHits(t, teamDB); len(hits) > 0 {
		t.Fatalf("grbpwr text in a fresh team: %v", hits)
	}

	// Seed is refused on team #1, whatever ctx asks.
	if err := provision.Seed(tenant.With(ctx, tenant.TeamOne(itOne)), ms.DB(), "kate", func(context.Context) error { return nil }); err == nil {
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
