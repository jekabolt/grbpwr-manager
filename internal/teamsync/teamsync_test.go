package teamsync_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/teamsync"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

// Team databases of this package only: the store tests own grbpwr_test.
const (
	dbOne    = "teamsync_one_test"
	dbPrefix = "tsyt_"
	dbTwo    = dbPrefix + "b0000001"
)

func dsn(t *testing.T, db string) string {
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

func TestMain(m *testing.M) {
	os.Exit(serialized(m))
}

// serialized runs the package holding a server-wide lock shared with the tenantdb tests: the
// disposable test MySQL keeps its data in memory, and two packages migrating full team schemas at
// once can exhaust it.
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

func TestEnsureAndDisable(t *testing.T) {
	d := dsn(t, dbOne)
	if err := testdbguard.CheckMySQLDSN(d); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	root, err := sql.Open("mysql", dsn(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, db := range []string{dbOne, dbTwo} {
		for _, q := range []string{"DROP DATABASE IF EXISTS `" + db + "`", "CREATE DATABASE `" + db + "` CHARACTER SET utf8mb4"} {
			if _, err := root.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
	}
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", dsn(t, "")); err == nil {
			for _, db := range []string{dbOne, dbTwo} {
				_, _ = r.Exec("DROP DATABASE IF EXISTS `" + db + "`")
			}
			r.Close()
		}
	})
	// Team B gets the real migrations. Team #1 only needs the two admin tables (the test container
	// keeps its data in memory; a second full schema is not worth it).
	if _, err := store.MigrateTeam(ctx, d, dbTwo); err != nil {
		t.Fatalf("migrate %s: %v", dbTwo, err)
	}
	for _, tb := range []string{"admins", "admin_permission"} {
		if _, err := root.ExecContext(ctx, "CREATE TABLE `"+dbOne+"`.`"+tb+"` LIKE `"+dbTwo+"`.`"+tb+"`"); err != nil {
			t.Fatalf("copy %s: %v", tb, err)
		}
	}

	one := tenant.TeamOne(dbOne)
	two := tenant.Team{ID: 2, Slug: "bravo", DBName: dbTwo, Status: tenant.StatusActive}
	cfg := store.Config{DSN: d, MaxOpenConnections: 2, MaxIdleConnections: 2}
	cfg.Tenant.Team1 = one
	cfg.Tenant.DBPrefix = dbPrefix
	cfg.Tenant.Strict = true
	st, err := store.NewForTest(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admins := st.Admin()
	ctxB := tenant.With(ctx, two)
	perms := []entity.AdminPermission{{Section: "orders", Access: entity.AccessRead}}

	// No team: refused before any statement.
	if err := teamsync.EnsureAdmin(ctx, admins, "kate", false, perms); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("no team: %v", err)
	}
	if err := teamsync.DisableAdmin(ctx, admins, "kate"); !errors.Is(err, tenant.ErrNoTenant) {
		t.Fatalf("disable, no team: %v", err)
	}

	// Create in team B only.
	if err := teamsync.EnsureAdmin(ctxB, admins, " kate ", false, perms); err != nil {
		t.Fatal(err)
	}
	acc, err := admins.GetAccountWithPermissions(ctxB, "kate")
	if err != nil {
		t.Fatal(err)
	}
	if acc.IsSuper || acc.Disabled || len(acc.Permissions) != 1 || acc.Permissions[0].Section != "orders" {
		t.Fatalf("created %+v", acc)
	}
	hash, err := admins.PasswordHashByUsername(ctxB, "kate")
	if err != nil || !strings.HasPrefix(hash, "!unusable$") || len(hash) < 40 {
		t.Fatalf("hash %q %v", hash, err)
	}
	if _, err := admins.GetAdminByUsername(tenant.With(ctx, one), "kate"); err == nil {
		t.Fatal("the member landed in team #1's database")
	}

	// Promote, then disable, then ensure again re-enables and keeps the hash.
	if err := teamsync.EnsureAdmin(ctxB, admins, "kate", true, nil); err != nil {
		t.Fatal(err)
	}
	if err := teamsync.DisableAdmin(ctxB, admins, "kate"); err != nil {
		t.Fatal(err)
	}
	if err := teamsync.DisableAdmin(ctxB, admins, "kate"); err != nil {
		t.Fatalf("second disable: %v", err)
	}
	if a, _ := admins.GetAdminByUsername(ctxB, "kate"); a == nil || !a.Disabled || !a.IsSuper {
		t.Fatalf("after disable %+v", a)
	}
	if err := teamsync.EnsureAdmin(ctxB, admins, "kate", false, perms); err != nil {
		t.Fatal(err)
	}
	a, err := admins.GetAdminByUsername(ctxB, "kate")
	if err != nil || a.Disabled || a.IsSuper {
		t.Fatalf("after re-ensure %+v %v", a, err)
	}
	if h2, _ := admins.PasswordHashByUsername(ctxB, "kate"); h2 != hash {
		t.Fatal("ensure replaced the password hash")
	}

	// Disabling someone with no row is not an error.
	if err := teamsync.DisableAdmin(ctxB, admins, "ghost"); err != nil {
		t.Fatalf("disable ghost: %v", err)
	}
	if err := teamsync.EnsureAdmin(ctxB, admins, "  ", false, nil); err == nil {
		t.Fatal("empty handle accepted")
	}
}
