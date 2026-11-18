package tenantdb_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/store/tenantdb"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	"github.com/jmoiron/sqlx"
)

// Databases of this package only: the store tests own grbpwr_test and drop its tables.
const (
	dbOne    = "tenantdb_one_test"
	dbPrefix = "tdbt_"
	dbTwo    = dbPrefix + "b0000001"
)

func TestAllowlistPattern(t *testing.T) {
	re, err := tenantdb.AllowlistPattern("grbpwr_beta", "lkb_t_")
	if err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"grbpwr_beta", "lkb_t_a1b2c3d4"} {
		if !re.MatchString(ok) {
			t.Errorf("%q refused", ok)
		}
	}
	for _, bad := range []string{"grbpwr", "grbpwr_beta2", "lkb_t_a1b2c3d", "lkb_t_a1b2c3d45", "lkb_t_A1B2C3D4",
		"mysql", "lkb_t_a1b2c3d4`; DROP", "information_schema", "lk_t_a1b2c3d4"} {
		if re.MatchString(bad) {
			t.Errorf("%q allowed", bad)
		}
	}
	only, err := tenantdb.AllowlistPattern("grbpwr", "")
	if err != nil {
		t.Fatal(err)
	}
	if only.MatchString("a1b2c3d4") || !only.MatchString("grbpwr") {
		t.Error("empty prefix must allow team #1 only")
	}
	if _, err := tenantdb.AllowlistPattern("bad`name", ""); err == nil {
		t.Error("backtick in team #1 name accepted")
	}
}

func TestNewConnectorRefusesMismatch(t *testing.T) {
	dsn := "u:p@tcp(127.0.0.1:3306)/grbpwr_beta"
	if _, err := tenantdb.NewConnector(dsn, tenantdb.Options{Team1: tenant.TeamOne("grbpwr")}); err == nil {
		t.Error("team #1 on another database than the dsn accepted")
	}
	if _, err := tenantdb.NewConnector(dsn, tenantdb.Options{Team1: tenant.Team{ID: 2, DBName: "grbpwr_beta"}}); err == nil {
		t.Error("Team1 that is not team #1 accepted")
	}
	if _, err := tenantdb.NewConnector("u:p@tcp(127.0.0.1:3306)/", tenantdb.Options{Team1: tenant.TeamOne("")}); err == nil {
		t.Error("dsn without database accepted")
	}
	if _, err := tenantdb.NewConnector(dsn, tenantdb.Options{Team1: tenant.TeamOne("grbpwr_beta")}); err != nil {
		t.Errorf("valid options refused: %v", err)
	}
}

// --- Docker (test-db.sh) ---------------------------------------------------------------------

func baseDSN(t *testing.T, db string) string {
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
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&multiStatements=true",
		os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

// migrated holds the package's two databases, created once (team #1 fully migrated): a full migration run is
// several hundred files, too slow to repeat per test.
var migrated struct {
	once sync.Once
	err  error
}

func migrateOnce(t *testing.T, dsnOne string) {
	t.Helper()
	migrated.once.Do(func() { migrated.err = createAndMigrate(t, dsnOne) })
	if migrated.err != nil {
		t.Fatal(migrated.err)
	}
}

func createAndMigrate(t *testing.T, dsnOne string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	root, err := sql.Open("mysql", baseDSN(t, ""))
	if err != nil {
		return err
	}
	defer root.Close()
	for _, db := range []string{dbOne, dbTwo} {
		if _, err := root.ExecContext(ctx, "DROP DATABASE IF EXISTS `"+db+"`"); err != nil {
			return err
		}
		if _, err := root.ExecContext(ctx, "CREATE DATABASE `"+db+"` CHARACTER SET utf8mb4"); err != nil {
			return err
		}
	}
	want := store.LatestMigrationID()
	if want == "" {
		return errors.New("LatestMigrationID is empty")
	}
	began := time.Now()
	v, err := store.MigrateTeam(ctx, dsnOne, dbOne)
	if err != nil {
		return fmt.Errorf("migrate %s: %w", dbOne, err)
	}
	if v != want {
		return fmt.Errorf("migrate %s: version %q, want %q", dbOne, v, want)
	}
	t.Logf("migrated %s in %s", dbOne, time.Since(began).Round(time.Second))
	// Running it again is a no-op.
	if _, err := store.MigrateTeam(ctx, dsnOne, dbOne); err != nil {
		return fmt.Errorf("second migrate: %w", err)
	}
	// Team B gets the admin tables only: the disposable test MySQL keeps its data in memory and does
	// not hold two full team schemas next to the other lanes' containers. The pool never looks at
	// the schema, only at which database a statement lands in.
	for _, tb := range []string{"admins", "admin_permission"} {
		if _, err := root.ExecContext(ctx, "CREATE TABLE `"+dbTwo+"`.`"+tb+"` LIKE `"+dbOne+"`.`"+tb+"`"); err != nil {
			return fmt.Errorf("copy %s: %w", tb, err)
		}
	}
	return nil
}

func TestMain(m *testing.M) {
	os.Exit(runPackage(m))
}

// runPackage holds a server-wide lock shared with the teamsync tests for the whole package: the
// disposable test MySQL keeps its data in memory, and two packages migrating full team schemas at
// once can exhaust it. Then it drops this package's databases.
func runPackage(m *testing.M) int {
	if os.Getenv("MYSQL_HOST") == "" {
		return m.Run()
	}
	port := os.Getenv("MYSQL_PORT")
	if port == "" {
		port = "3306"
	}
	r, err := sql.Open("mysql", fmt.Sprintf("%s:%s@tcp(%s:%s)/", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), os.Getenv("MYSQL_HOST"), port))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer r.Close()
	ctx := context.Background()
	conn, err := r.Conn(ctx)
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
	code := m.Run()
	for _, db := range []string{dbOne, dbTwo} {
		_, _ = r.Exec("DROP DATABASE IF EXISTS `" + db + "`")
	}
	return code
}

// setupTwoTeams makes sure both databases are migrated and reseeds admins: row id 1 = "alpha" in
// team #1 and id 1 = "bravo" in team B.
func setupTwoTeams(t *testing.T) (one, two tenant.Team, dsnOne string) {
	t.Helper()
	dsnOne = baseDSN(t, dbOne)
	if err := testdbguard.CheckMySQLDSN(dsnOne); err != nil {
		t.Fatal(err)
	}
	migrateOnce(t, dsnOne)
	ctx := context.Background()
	for db, name := range map[string]string{dbOne: "alpha", dbTwo: "bravo"} {
		dsn, err := store.TeamDSN(dsnOne, db)
		if err != nil {
			t.Fatal(err)
		}
		h, err := sql.Open("mysql", dsn)
		if err != nil {
			t.Fatal(err)
		}
		_, err = h.ExecContext(ctx, `DELETE FROM admins`)
		if err == nil {
			_, err = h.ExecContext(ctx, `INSERT INTO admins (id, username, password_hash, is_super) VALUES (1, ?, 'x', 0)`, name)
		}
		h.Close()
		if err != nil {
			t.Fatalf("seed %s: %v", db, err)
		}
	}
	one = tenant.TeamOne(dbOne)
	two = tenant.Team{ID: 2, Slug: "bravo", Name: "bravo", DBName: dbTwo, Profile: tenant.ProfileStandard, Status: tenant.StatusActive}
	return one, two, dsnOne
}

func openPool(t *testing.T, dsn string, team1 tenant.Team, strict bool, maxOpen int) (*sqlx.DB, *tenantdb.Connector) {
	t.Helper()
	db, c, err := tenantdb.Open(dsn, tenantdb.Options{Team1: team1, Strict: strict, DBPrefix: dbPrefix})
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxOpen)
	t.Cleanup(func() { db.Close() })
	return db, c
}

func username(ctx context.Context, q sqlx.QueryerContext, withArg bool) (string, error) {
	var s string
	var err error
	if withArg {
		err = sqlx.GetContext(ctx, q, &s, `SELECT username FROM admins WHERE id = ?`, 1)
	} else {
		err = sqlx.GetContext(ctx, q, &s, `SELECT username FROM admins WHERE id = 1`)
	}
	return s, err
}

func expect(t *testing.T, label string, got string, err error, want string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", label, err)
	}
	if got != want {
		t.Fatalf("%s: read %q, want %q — teams mixed", label, got, want)
	}
}

// TestTwoTeamsNeverMix is G7: one connection (MaxOpenConns=1) alternates between two team
// databases holding the same primary key with different content.
func TestTwoTeamsNeverMix(t *testing.T) {
	one, two, dsn := setupTwoTeams(t)
	db, c := openPool(t, dsn, one, false, 1)
	ctxA := tenant.With(context.Background(), one)
	ctxB := tenant.With(context.Background(), two)

	t.Run("query", func(t *testing.T) {
		for i := 0; i < 10; i++ {
			for _, withArg := range []bool{false, true} {
				s, err := username(ctxA, db, withArg)
				expect(t, "team #1", s, err, "alpha")
				s, err = username(ctxB, db, withArg)
				expect(t, "team B", s, err, "bravo")
			}
		}
	})

	t.Run("prepared", func(t *testing.T) {
		// Prepared under team #1; database/sql reuses it on the one connection for team B.
		st, err := db.PreparexContext(ctxA, `SELECT username FROM admins WHERE id = ?`)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		for i := 0; i < 5; i++ {
			var s string
			err := st.GetContext(ctxB, &s, 1)
			expect(t, "prepared, team B", s, err, "bravo")
			err = st.GetContext(ctxA, &s, 1)
			expect(t, "prepared, team #1", s, err, "alpha")
		}
	})

	t.Run("tx", func(t *testing.T) {
		tx, err := db.BeginTxx(ctxB, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctxB, `UPDATE admins SET username = 'bravo-tx' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		s, err := username(ctxB, tx, true)
		expect(t, "tx, team B", s, err, "bravo-tx")
		if _, err := username(ctxA, tx, true); err == nil {
			t.Fatal("a team #1 statement inside team B's transaction ran")
		}
		if _, err := username(context.Background(), tx, false); err == nil {
			t.Fatal("a statement without a team inside team B's transaction ran")
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		s, err = username(ctxA, db, true)
		expect(t, "after tx, team #1", s, err, "alpha")
		s, err = username(ctxB, db, true)
		expect(t, "after tx, team B", s, err, "bravo-tx")

		// A rolled-back transaction leaves the pin clear too.
		tx, err = db.BeginTxx(ctxA, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctxA, `UPDATE admins SET username = 'never' WHERE id = 1`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		s, err = username(ctxB, db, false)
		expect(t, "after rollback, team B", s, err, "bravo-tx")
		s, err = username(ctxA, db, false)
		expect(t, "after rollback, team #1", s, err, "alpha")
	})

	t.Run("no team, mode off", func(t *testing.T) {
		before := c.Stats().MissingCtx
		s, err := username(context.Background(), db, true)
		expect(t, "no team", s, err, "alpha")
		if c.Stats().MissingCtx != before+1 {
			t.Fatalf("missing-ctx counter %d → %d, want +1", before, c.Stats().MissingCtx)
		}
	})

	t.Run("outside the allowlist", func(t *testing.T) {
		for _, name := range []string{"mysql", "tdbt_short", dbTwo + "x", "information_schema"} {
			bad := tenant.With(context.Background(), tenant.Team{ID: 9, DBName: name})
			if _, err := username(bad, db, false); err == nil {
				t.Fatalf("database %q outside the allowlist was used", name)
			}
		}
		s, err := username(ctxB, db, false)
		expect(t, "after refusals, team B", s, err, "bravo-tx")
	})

	if c.Stats().Uses == 0 {
		t.Fatal("no USE was ever issued")
	}
}

// TestTwoTeamsConcurrent hammers a small pool from many goroutines with alternating teams.
func TestTwoTeamsConcurrent(t *testing.T) {
	one, two, dsn := setupTwoTeams(t)
	db, _ := openPool(t, dsn, one, true, 3)
	teams := []struct {
		ctx  context.Context
		want string
	}{
		{tenant.With(context.Background(), one), "alpha"},
		{tenant.With(context.Background(), two), "bravo"},
	}
	st, err := db.PreparexContext(teams[0].ctx, `SELECT username FROM admins WHERE id = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				tm := teams[(g+i)%2]
				var s string
				var err error
				switch i % 3 {
				case 0:
					s, err = username(tm.ctx, db, true)
				case 1:
					err = st.GetContext(tm.ctx, &s, 1)
				default:
					var tx *sqlx.Tx
					if tx, err = db.BeginTxx(tm.ctx, nil); err == nil {
						s, err = username(tm.ctx, tx, false)
						if cerr := tx.Commit(); err == nil {
							err = cerr
						}
					}
				}
				if err != nil {
					errs <- err
					return
				}
				if s != tm.want {
					errs <- fmt.Errorf("goroutine %d step %d: read %q, want %q", g, i, s, tm.want)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// TestStrictRefusesNoTeam: teams mode on, a ctx without a team never reaches a database.
func TestStrictRefusesNoTeam(t *testing.T) {
	one, two, dsn := setupTwoTeams(t)
	db, c := openPool(t, dsn, one, true, 1)
	bg := context.Background()

	if _, err := username(bg, db, false); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("query without team: err = %v, want ErrNoTenant", err)
	}
	if _, err := username(bg, db, true); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("arg query without team: err = %v, want ErrNoTenant", err)
	}
	if _, err := db.ExecContext(bg, `UPDATE admins SET username = 'x' WHERE id = 1`); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("exec without team: err = %v, want ErrNoTenant", err)
	}
	if _, err := db.PreparexContext(bg, `SELECT 1`); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("prepare without team: err = %v, want ErrNoTenant", err)
	}
	if _, err := db.BeginTxx(bg, nil); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("begin without team: err = %v, want ErrNoTenant", err)
	}
	// A statement prepared with a team, then run without one, is refused too.
	st, err := db.PreparexContext(tenant.With(bg, two), `SELECT username FROM admins WHERE id = ?`)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var s string
	if err := st.GetContext(bg, &s, 1); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("prepared run without team: err = %v, want ErrNoTenant", err)
	}
	// Ping touches no table and stays allowed (readiness probe).
	if err := db.PingContext(bg); err != nil {
		t.Fatalf("ping: %v", err)
	}
	if c.Stats().MissingCtx != 0 {
		t.Fatal("strict mode counted a fallback")
	}
	// The data is untouched.
	s, err = username(tenant.With(bg, one), db, true)
	expect(t, "team #1 after refusals", s, err, "alpha")
}

// TestStoreOpensOnRoutingPool: store.NewForTest runs on the routing pool with team #1 from the DSN.
// Team B's database is admitted by the prefix, as TENANT_DB_PREFIX does on beta and prod; without
// it the allowlist holds team #1 only.
func TestStoreOpensOnRoutingPool(t *testing.T) {
	one, two, dsn := setupTwoTeams(t)
	ctx := context.Background()
	cfg := store.Config{DSN: dsn, MaxOpenConnections: 1, MaxIdleConnections: 1}
	cfg.Tenant.DBPrefix = dbPrefix
	st, err := store.NewForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	a1, err := st.Admin().GetAdminByUsername(tenant.With(ctx, one), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Admin().GetAdminByUsername(tenant.With(ctx, two), "alpha"); err == nil {
		t.Fatal("team B sees team #1's admin")
	}
	b1, err := st.Admin().GetAdminByUsername(tenant.With(ctx, two), "bravo")
	if err != nil {
		t.Fatal(err)
	}
	if a1.Id != 1 || b1.Id != 1 {
		t.Fatalf("ids %d / %d, want 1 / 1", a1.Id, b1.Id)
	}
	// A store transaction pins its team.
	err = st.Tx(tenant.With(ctx, two), func(ctx context.Context, rep dependency.Repository) error {
		a, err := rep.Admin().GetAdminByUsername(ctx, "bravo")
		if err != nil {
			return err
		}
		if a.Username != "bravo" {
			return fmt.Errorf("tx read %q", a.Username)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("store tx on team B: %v", err)
	}
	mc, _ := mysql.ParseDSN(dsn)
	if !strings.HasSuffix(mc.DBName, "_test") {
		t.Fatal("test pool is not on a _test database")
	}
}
