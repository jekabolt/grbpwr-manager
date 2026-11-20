package platformstore_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
)

// The control plane of this package only: the store tests own grbpwr_test.
const cpDB = "platformstore_cp_test"

func TestConfig(t *testing.T) {
	c := platformstore.Config{DSN: "u:secret@tcp(h:1)/lekalo", TokenPepper: "pep", OperatorEmails: " A@x.io, ,b@y.io ",
		LegacyAdminOrigins: "https://admin.grbpwr.com,https://admin.beta.grbpwr.com"}
	if !c.Enabled() || (platformstore.Config{DSN: "  "}).Enabled() {
		t.Fatal("Enabled is wrong")
	}
	if got := c.Operators(); len(got) != 2 || got[0] != "a@x.io" || got[1] != "b@y.io" {
		t.Fatalf("operators %v", got)
	}
	if len(c.LegacyOrigins()) != 2 {
		t.Fatal("legacy origins")
	}
	if s := c.String(); strings.Contains(s, "secret") || strings.Contains(s, "pep") {
		t.Fatalf("String leaks a secret: %s", s)
	}
}

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
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/%s", os.Getenv("MYSQL_USER"), os.Getenv("MYSQL_PASSWORD"), host, port, db)
}

// freshDSN recreates the package's control-plane database empty (dropped again at cleanup) and
// returns its DSN; nothing is migrated yet.
func freshDSN(t *testing.T) string {
	t.Helper()
	d := dsn(t, cpDB)
	if err := testdbguard.CheckMySQLDSN(d); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", dsn(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + cpDB + "`", "CREATE DATABASE `" + cpDB + "` CHARACTER SET utf8mb4"} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", dsn(t, "")); err == nil {
			_, _ = r.Exec("DROP DATABASE IF EXISTS `" + cpDB + "`")
			r.Close()
		}
	})
	return d
}

func openFresh(t *testing.T) *platformstore.Store {
	t.Helper()
	d := freshDSN(t)
	ctx := context.Background()
	st, err := platformstore.Open(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	// A second open re-runs the migrations: nothing to apply, no error.
	st, err = platformstore.Open(ctx, d)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	var n int
	// 0001 control plane + 0002 team_review (B3, columns only) + 0003 provision_lease (B4, one table)
	// + 0004 credential_gen (I1, one column) + 0005 team_delete (D1, columns and keys only)
	// + 0006 oauth (O0: platform_identity, oauth_state, team_logo + columns)
	// + 0007 exact identity collations (column changes only) + 0008 team_brand (W1, one table)
	// + 0009 profile (P0: account_avatar, oauth_link_ticket + columns, keys and ENUM members)
	// + 0010 team_schema_min_code (X3, one column).
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM platform_migrations`); err != nil || n != 10 {
		t.Fatalf("platform_migrations rows = %d, %v", n, err)
	}
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()`); err != nil || n != 20 {
		t.Fatalf("tables = %d (want 19 + platform_migrations), %v", n, err)
	}
	return st
}

func TestIdentityColumnsUseBinaryCollation(t *testing.T) {
	st := openFresh(t)
	type column struct {
		Table     string         `db:"TABLE_NAME"`
		Name      string         `db:"COLUMN_NAME"`
		Type      string         `db:"COLUMN_TYPE"`
		Nullable  string         `db:"IS_NULLABLE"`
		Collation sql.NullString `db:"COLLATION_NAME"`
	}
	var got []column
	if err := st.DB().Select(&got, `SELECT TABLE_NAME, COLUMN_NAME, COLUMN_TYPE, IS_NULLABLE, COLLATION_NAME
FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND (
  (TABLE_NAME = 'platform_user' AND COLUMN_NAME = 'email') OR
  (TABLE_NAME = 'team_invite' AND COLUMN_NAME = 'email') OR
  (TABLE_NAME = 'platform_identity' AND COLUMN_NAME IN ('subject', 'email')) OR
  (TABLE_NAME = 'platform_email' AND COLUMN_NAME = 'to_email'))
ORDER BY TABLE_NAME, COLUMN_NAME`); err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("identity columns = %d, want 5: %+v", len(got), got)
	}
	want := map[string]string{
		"platform_email.to_email":   "varchar(254):NO",
		"platform_identity.email":   "varchar(254):YES",
		"platform_identity.subject": "varchar(255):NO",
		"platform_user.email":       "varchar(254):YES",
		"team_invite.email":         "varchar(254):NO",
	}
	for _, c := range got {
		key := c.Table + "." + c.Name
		if c.Collation.String != "utf8mb4_bin" || c.Type+":"+c.Nullable != want[key] {
			t.Errorf("%s = %s %s %s, want utf8mb4_bin %s", key, c.Type, c.Nullable, c.Collation.String, want[key])
		}
	}
}

func TestEnsureTeamOne(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	admins := []platformstore.TeamOneAdmin{
		{Username: "boss", PasswordHash: "h-boss", Super: true},
		{Username: "helper", PasswordHash: "h-helper"},
		{Username: "gone", PasswordHash: "h-gone", Disabled: true},
	}
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", admins); err != nil {
		t.Fatal(err)
	}
	// Later the boss changes the password on the platform and helper is disabled there.
	if err := st.SetPasswordHashByHandle(ctx, "boss", "h-boss-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE platform_user SET status = 'disabled' WHERE handle = 'helper'`); err != nil {
		t.Fatal(err)
	}
	// Rerun at the next boot, the admins table unchanged except gone is now disabled → still disabled.
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", admins); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	type row struct {
		Handle string `db:"handle"`
		Hash   string `db:"password_hash"`
		Status string `db:"status"`
		Role   string `db:"role"`
	}
	var rows []row
	if err := st.DB().Select(&rows, `SELECT u.handle, u.password_hash, u.status, m.role
FROM platform_user u JOIN team_member m ON m.user_id = u.id AND m.team_id = 1 ORDER BY u.handle`); err != nil {
		t.Fatal(err)
	}
	want := []row{{"boss", "h-boss-2", "active", "owner"}, {"gone", "h-gone", "disabled", "member"}, {"helper", "h-helper", "disabled", "member"}}
	if fmt.Sprint(rows) != fmt.Sprint(want) {
		t.Fatalf("users\n got %v\nwant %v", rows, want)
	}
	if err := st.EnsureTeamOne(ctx, "grbpwr_other", nil); err == nil {
		t.Fatal("team #1 re-pointed to another database")
	}
	if err := st.SetPasswordHashByHandle(ctx, "nobody", "h"); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("unknown handle: %v", err)
	}
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{{Username: "new", PasswordHash: ""}}); err == nil {
		t.Fatal("admin without a hash adopted")
	}

	// A stranger signed up on the platform as "x"; later team #1 gets an admin "x" (a super, and
	// disabled on the next boot). Neither boot adopts the stranger, disables them or touches the hash.
	if _, err := st.DB().Exec(`INSERT INTO platform_user (email, handle, password_hash, status, created_at)
VALUES ('x@example.test', 'x', 'h-stranger', 'active', UTC_TIMESTAMP(3))`); err != nil {
		t.Fatal(err)
	}
	for _, disabled := range []bool{false, true} {
		if err := st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{
			{Username: "x", PasswordHash: "h-admin-x", Super: true, Disabled: disabled},
			{Username: "boss", PasswordHash: "h-boss", Super: true},
		}); err != nil {
			t.Fatalf("stranger handle, disabled=%v: %v", disabled, err)
		}
		var got struct {
			Hash    string `db:"password_hash"`
			Status  string `db:"status"`
			Members int    `db:"members"`
		}
		if err := st.DB().Get(&got, `SELECT u.password_hash, u.status,
  (SELECT COUNT(*) FROM team_member m WHERE m.user_id = u.id) AS members
FROM platform_user u WHERE u.handle = 'x'`); err != nil {
			t.Fatal(err)
		}
		if got.Hash != "h-stranger" || got.Status != "active" || got.Members != 0 {
			t.Fatalf("stranger after boot (disabled=%v): %+v", disabled, got)
		}
	}

	team, err := st.Directory().ByID(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if team.Slug != tenant.TeamOneSlug || team.DBName != "grbpwr_test" || team.Profile != tenant.ProfileAll || team.Status != tenant.StatusActive {
		t.Fatalf("team #1 = %+v", team)
	}
}

func TestDirectory(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	db := st.DB()
	if _, err := db.Exec(`INSERT INTO team (id, slug, name, db_name, profile, status) VALUES
 (2, 'acme', 'Acme', 'lkb_t_acme0001', 'standard', 'active'),
 (3, 'zombie', 'Zombie', 'lkb_t_zomb0001', 'standard', 'deleted'),
 (4, 'pend', 'Pend', NULL, 'standard', 'pending'),
 (5, 'pause', 'Pause', 'lkb_t_paus0001', 'standard', 'suspended')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO team_slug_history (slug, team_id, retired_at, redirect_until) VALUES
 ('acme-old', 2, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3) + INTERVAL 1 DAY),
 ('acme-ancient', 2, UTC_TIMESTAMP(3) - INTERVAL 40 DAY, UTC_TIMESTAMP(3) - INTERVAL 10 DAY),
 ('zombie-old', 3, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3) + INTERVAL 1 DAY)`); err != nil {
		t.Fatal(err)
	}
	dir := st.Directory()
	for slug, want := range map[string]int64{"acme": 2, "ACME ": 2, "acme-old": 2, "pause": 5, "pend": 4} {
		got, err := dir.BySlug(ctx, slug)
		if err != nil || got.ID != want {
			t.Fatalf("BySlug(%q) = %d, %v; want %d", slug, got.ID, err, want)
		}
	}
	for _, slug := range []string{"zombie", "zombie-old", "acme-ancient", "nope", ""} {
		if _, err := dir.BySlug(ctx, slug); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("BySlug(%q): %v, want ErrNotFound", slug, err)
		}
	}
	if got, err := dir.ByID(ctx, 3); err != nil || got.Status != tenant.StatusDeleted {
		t.Fatalf("ByID of a deleted team: %+v %v", got, err)
	}
	if _, err := dir.ByID(ctx, 99); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("ByID(99): %v", err)
	}
	list, err := dir.ListActive(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != 2 || list[1].ID != 5 {
		t.Fatalf("ListActive = %+v", list)
	}

	// The cache holds a renamed team until Invalidate.
	if _, err := db.Exec(`UPDATE team SET name = 'Acme 2' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if got, _ := dir.ByID(ctx, 2); got.Name != "Acme" {
		t.Fatalf("cache miss too early: %q", got.Name)
	}
	dir.Invalidate(2)
	if got, _ := dir.ByID(ctx, 2); got.Name != "Acme 2" {
		t.Fatalf("Invalidate kept a stale team: %q", got.Name)
	}
	if got, _ := dir.BySlug(ctx, "acme-old"); got.Name != "Acme 2" {
		t.Fatalf("Invalidate kept a stale slug entry: %q", got.Name)
	}
}

func TestFeaturesSessionsLetters(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if err := st.SetFeature(ctx, 2, "ai.generate", true, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFeature(ctx, 2, "ai.generate", false, 7); err != nil {
		t.Fatal(err)
	}
	if err := st.SetFeature(ctx, 2, "warehouse", true, 7); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "Bad", "x y", "_x", strings.Repeat("a", 41)} {
		if err := st.SetFeature(ctx, 2, bad, true, 7); err == nil {
			t.Fatalf("feature key %q accepted", bad)
		}
	}
	got, err := st.FeatureOverrides(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["ai.generate"] || !got["warehouse"] {
		t.Fatalf("overrides %v", got)
	}
	if other, err := st.FeatureOverrides(ctx, 3); err != nil || len(other) != 0 {
		t.Fatalf("team 3 overrides %v %v", other, err)
	}

	now := time.Now().UTC()
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud, revoked_at) VALUES
 ('live0000000000000000000000000000', 1, 2, 'team', NULL),
 ('dead0000000000000000000000000000', 1, NULL, 'console', ?)`, now); err != nil {
		t.Fatal(err)
	}
	for sid, want := range map[string]bool{"live0000000000000000000000000000": true, "dead0000000000000000000000000000": false, "unknown": false, "": false} {
		if ok, err := st.SessionActive(ctx, sid); err != nil || ok != want {
			t.Fatalf("SessionActive(%q) = %v %v", sid, ok, err)
		}
	}

	if err := st.EnqueueLetter(ctx, platformstore.Letter{To: "a@b.io", Template: "invite", Link: "https://x", TeamName: "Acme", Slug: "acme"}); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []platformstore.Letter{{To: "nobody", Template: "verify"}, {To: "a@b.io", Template: "spam"}} {
		if err := st.EnqueueLetter(ctx, bad); err == nil {
			t.Fatalf("letter %+v accepted", bad)
		}
	}
	var l struct {
		To      string `db:"to_email"`
		Status  string `db:"status"`
		Payload string `db:"payload"`
	}
	if err := st.DB().Get(&l, `SELECT to_email, status, payload FROM platform_email`); err != nil {
		t.Fatal(err)
	}
	if l.To != "a@b.io" || l.Status != "pending" || !strings.Contains(l.Payload, `"team_name": "Acme"`) || strings.Contains(l.Payload, `"code"`) {
		t.Fatalf("letter row %+v", l)
	}
}
