package teamwipe_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	"github.com/jekabolt/grbpwr-manager/internal/teamwipe"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

// Databases of this package only.
const (
	itCP     = "teamwipe_cp_test"
	itOne    = "teamwipe_one_test"
	itPrefix = "twt_"
	itFK     = "0123456789abcdef0123456789abcdef"
)

func TestCheckDropName(t *testing.T) {
	ok := []string{"twt_abcd1234", "twt_00000000"}
	for _, n := range ok {
		if err := store.CheckDropName(itPrefix, itOne, itCP, n); err != nil {
			t.Errorf("%s refused: %v", n, err)
		}
	}
	bad := map[string][3]string{ // name → prefix, team1, platform
		"team #1":        {"twt_", "twt_abcd1234", itCP},
		"control plane":  {"twt_", itOne, "twt_abcd1234"},
		"no team1 name":  {"twt_", "", itCP},
		"no cp name":     {"twt_", itOne, ""},
		"mysql":          {"", itOne, itCP},
		"prefix+7":       {"twt_", itOne, itCP},
		"prefix+9":       {"twt_", itOne, itCP},
		"upper":          {"twt_", itOne, itCP},
		"backtick":       {"twt_", itOne, itCP},
		"dot":            {"twt_", itOne, itCP},
		"other prefix":   {"twt_", itOne, itCP},
		"empty":          {"twt_", itOne, itCP},
		"sys via prefix": {"sy", itOne, itCP},
	}
	names := map[string]string{
		"team #1": "twt_abcd1234", "control plane": "twt_abcd1234", "no team1 name": "twt_abcd1234",
		"no cp name": "twt_abcd1234", "mysql": "mysql", "prefix+7": "twt_abcd123", "prefix+9": "twt_abcd12345",
		"upper": "twt_ABCD1234", "backtick": "twt_abcd`123", "dot": "twt_abc.1234", "other prefix": "lkb_abcd1234",
		"empty": "", "sys via prefix": "sys",
	}
	for k, a := range bad {
		if err := store.CheckDropName(a[0], a[1], a[2], names[k]); err == nil {
			t.Errorf("%s: %q accepted", k, names[k])
		}
	}
}

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
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

type dbs struct{ base string }

func (d dbs) Drop(ctx context.Context, n string) (store.DropResult, error) {
	return store.DropTeamDB(ctx, d.base, itPrefix, itCP, n)
}

func (d dbs) Size(ctx context.Context, n string) (bool, int64, error) {
	return store.TeamDBSize(ctx, d.base, itPrefix, itCP, n)
}

func (d dbs) CheckDrop(n string) error {
	db, err := store.DSNDatabase(d.base)
	if err != nil {
		return err
	}
	return store.CheckDropName(itPrefix, db, itCP, n)
}

// files is the bucket: keys per file key; wipes delete only their own key's objects.
type files struct{ objects map[string]int }

func (f *files) WipeTeamFiles(_ context.Context, fk string, _ int) (bucket.WipeStats, error) {
	n := f.objects[fk]
	delete(f.objects, fk)
	return bucket.WipeStats{Objects: int64(n), Done: true}, nil
}

func (f *files) PreviewTeamFiles(_ context.Context, fk string) (bucket.WipeStats, error) {
	return bucket.WipeStats{Objects: int64(f.objects[fk])}, nil
}

func (f *files) PreflightTeamFiles(context.Context, string) error { return nil }

func schemaExists(t *testing.T, root *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := root.QueryRow(`SELECT COUNT(*) FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?`, name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

// TestDay30EndToEnd: a due team "xd3-test" with a real database, files and rows, a live team
// "live" and team #1 forced into a due deleted state. Dry writes nothing; on wipes xd3-test only.
func TestDay30EndToEnd(t *testing.T) {
	ctx := context.Background()
	if err := testdbguard.CheckMySQLDSN(itDSN(t, itCP)); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", itDSN(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() }) // registered first: runs after the drops below
	gone, live := itPrefix+"gone0001", itPrefix+"live0001"
	reset := func() {
		for _, db := range []string{itCP, itOne, gone, live} {
			if _, err := root.Exec("DROP DATABASE IF EXISTS `" + db + "`"); err != nil {
				t.Fatal(err)
			}
		}
	}
	reset()
	t.Cleanup(reset)
	for _, db := range []string{itCP, itOne, gone, live} {
		if _, err := root.Exec("CREATE DATABASE `" + db + "`"); err != nil {
			t.Fatal(err)
		}
	}
	for _, db := range []string{itOne, gone, live} {
		if _, err := root.Exec("CREATE TABLE `" + db + "`.product (id INT PRIMARY KEY)"); err != nil {
			t.Fatal(err)
		}
	}
	pf, err := platformstore.Open(ctx, itDSN(t, itCP))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pf.Close() }()
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, db_name, status, purge_at) VALUES (1, 'grbpwr', 'grbpwr', '` + itOne + `', 'deleted', '2026-01-01')`,
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (11, 'o@x.io', 'own', 'h', 'active'),
 (12, 'd@x.io', 'dead', 'h', 'active')`,
		`UPDATE platform_user SET status = 'deleted', deleted_at = '2026-01-01', purge_at = '2026-01-31' WHERE id = 12`,
		`INSERT INTO team (id, slug, name, db_name, file_key, status, purge_at, closed_with_user_id) VALUES
 (2, 'xd3-test', 'X', '` + gone + `', '` + itFK + `', 'deleted', '2026-01-01', 12),
 (3, 'live', 'Live', '` + live + `', NULL, 'active', NULL, NULL)`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (2, 12, 'owner', 'frozen'), (3, 11, 'owner', 'active')`,
	} {
		if _, err := pf.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	fs := &files{objects: map[string]int{itFK: 4, "ffffffffffffffffffffffffffffffff": 9}}
	base := itDSN(t, itOne)
	unusable := func() (string, error) { return "!unusable", nil }

	teamwipe.New(teamwipe.ModeDry, pf, fs, dbs{base}, unusable, time.Hour, time.Hour).Tick(ctx)
	var n int
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM team_wipe`)
	if n != 0 || !schemaExists(t, root, gone) || fs.objects[itFK] != 4 {
		t.Fatal("dry run wrote something")
	}
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM platform_user WHERE purged_at IS NOT NULL`)
	if n != 0 {
		t.Fatal("dry run purged an account")
	}

	// One pass: the account first, then the team that closed with it.
	teamwipe.New(teamwipe.ModeOn, pf, fs, dbs{base}, unusable, time.Hour, time.Hour).Tick(ctx)
	if schemaExists(t, root, gone) {
		t.Fatal("wiped team's database still there")
	}
	if !schemaExists(t, root, live) || !schemaExists(t, root, itOne) || !schemaExists(t, root, itCP) {
		t.Fatal("a database that is not the wiped team's was dropped")
	}
	if _, ok := fs.objects[itFK]; ok || fs.objects["ffffffffffffffffffffffffffffffff"] != 9 {
		t.Fatalf("files %v", fs.objects)
	}
	var fin sql.NullTime
	if err := pf.DB().Get(&fin, `SELECT finished_at FROM team_wipe WHERE team_id = 2`); err != nil || !fin.Valid {
		t.Fatalf("wipe not finished: %v", err)
	}
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM team_member WHERE team_id = 2`)
	if n != 0 {
		t.Fatal("wiped team's members kept")
	}
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM team_member WHERE team_id = 3`)
	if n != 1 {
		t.Fatal("live team's members touched")
	}
	var one struct {
		Slug   string `db:"slug"`
		DBName string `db:"db_name"`
	}
	if err := pf.DB().Get(&one, `SELECT slug, db_name FROM team WHERE id = 1`); err != nil || one.Slug != "grbpwr" || one.DBName != itOne {
		t.Fatalf("team #1 row %+v %v", one, err)
	}
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM team_wipe WHERE team_id = 1`)
	if n != 0 {
		t.Fatal("team #1 claimed")
	}
	_ = pf.DB().Get(&n, `SELECT COUNT(*) FROM platform_user WHERE id = 12 AND purged_at IS NOT NULL AND email IS NULL`)
	if n != 1 {
		t.Fatal("closing account not purged")
	}
	if _, err := root.Exec("SELECT COUNT(*) FROM `" + itOne + "`.product"); err != nil {
		t.Fatalf("team #1 database: %v", err)
	}

	// A drop of a database another session holds the named lock of is busy, not forced.
	lock, ok, err := store.LockTeamDB(ctx, base, itPrefix, live)
	if err != nil || !ok {
		t.Fatalf("lock %v %v", ok, err)
	}
	if res, err := store.DropTeamDB(ctx, base, itPrefix, itCP, live); err != nil || res != store.DropBusy {
		t.Fatalf("drop under lock %s %v", res, err)
	}
	lock.Release(ctx)
	if res, err := store.DropTeamDB(ctx, base, itPrefix, itCP, gone); err != nil || res != store.DropGone {
		t.Fatalf("drop of a gone database %s %v", res, err)
	}
	if _, err := store.DropTeamDB(ctx, base, itPrefix, itCP, itOne); err == nil || !schemaExists(t, root, itOne) {
		t.Fatal("team #1's database passed the drop guard")
	}
}
