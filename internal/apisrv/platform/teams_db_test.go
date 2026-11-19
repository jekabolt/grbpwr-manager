package platform

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/testdbguard"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This package's own control plane.
const srvDB = "platform_srv_cp_test"

func testDSN(t *testing.T, db string) string {
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

func freshStore(t *testing.T) *platformstore.Store {
	t.Helper()
	d := testDSN(t, srvDB)
	if err := testdbguard.CheckMySQLDSN(d); err != nil {
		t.Fatal(err)
	}
	root, err := sql.Open("mysql", testDSN(t, ""))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, q := range []string{"DROP DATABASE IF EXISTS `" + srvDB + "`", "CREATE DATABASE `" + srvDB + "` CHARACTER SET utf8mb4"} {
		if _, err := root.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if r, err := sql.Open("mysql", testDSN(t, "")); err == nil {
			_, _ = r.Exec("DROP DATABASE IF EXISTS `" + srvDB + "`")
			r.Close()
		}
	})
	st, err := platformstore.Open(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.EnsureTeamOne(context.Background(), "grbpwr_test", []platformstore.TeamOneAdmin{{Username: "boss", PasswordHash: "h", Super: true}}); err != nil {
		t.Fatal(err)
	}
	exec(t, st, `INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (101, 'ann@x.io', 'ann', 'h', 'active', 0), (103, 'op@x.io', 'op', 'h', 'active', 1)`)
	return st
}

func exec(t *testing.T, st *platformstore.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().Exec(q, args...); err != nil {
		t.Fatal(err)
	}
}

// addTeams adds n active teams from id `from`, each with ann as owner, a schema version and a
// fingerprint that matches a ready spare.
func addTeams(t *testing.T, st *platformstore.Store, from, n int) {
	for i := from; i < from+n; i++ {
		exec(t, st, `INSERT INTO team (id, slug, name, db_name, status, schema_version, schema_fingerprint)
VALUES (?, ?, ?, ?, 'active', 'v1', ?)`, i, fmt.Sprintf("team%d", i), fmt.Sprintf("Team %d", i), fmt.Sprintf("lk_t_team%04d", i), strings.Repeat("f", 64))
		exec(t, st, `INSERT INTO team_member (team_id, user_id, role, status) VALUES (?, 101, 'owner', 'active')`, i)
	}
}

func opCtx() context.Context {
	return auth.PutPlatformCaller(context.Background(), auth.PlatformCaller{UserID: 103, Handle: "op"})
}

func newSrv(st *platformstore.Store) *Server {
	s := New(st)
	s.SetConfig(platformstore.Config{BaseDomain: "beta.lekalo.studio"})
	s.now = func() time.Time { return time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC) }
	return s
}

// questions is the server's statement counter of the store's only connection.
func questions(t *testing.T, st *platformstore.Store) int {
	t.Helper()
	var name string
	var n int
	if err := st.DB().QueryRow(`SHOW SESSION STATUS LIKE 'Questions'`).Scan(&name, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestPlatformListTeamsFixedQueriesAndSpend(t *testing.T) {
	st := freshStore(t)
	exec(t, st, `INSERT INTO spare_db (db_name, status, schema_version, schema_fingerprint) VALUES ('lk_t_spare001', 'ready', 'v1', ?)`, strings.Repeat("f", 64))
	addTeams(t, st, 2, 2)
	exec(t, st, `INSERT INTO team_ai_spend_daily (team_id, day, cost_usd, unpriced_calls, calls) VALUES
 (2, '2026-11-10', 1.25, 2, 5), (2, '2026-10-31', 9, 0, 1), (3, '2026-11-01', 0.004, 0, 1)`)
	s := newSrv(st)
	// One connection: SHOW SESSION STATUS counts exactly the statements of the call.
	st.DB().SetMaxOpenConns(1)
	st.DB().SetMaxIdleConns(1)

	count := func() (int, *pb_admin.ListTeamsResponse) {
		before := questions(t, st)
		res, err := s.ListTeams(opCtx(), &pb_admin.ListTeamsRequest{})
		if err != nil {
			t.Fatal(err)
		}
		return questions(t, st) - before, res
	}
	with3, res := count()
	if len(res.Teams) != 3 {
		t.Fatalf("teams %d", len(res.Teams))
	}
	byslug := map[string]*pb_admin.PlatformTeam{}
	for _, tm := range res.Teams {
		byslug[tm.Slug] = tm
	}
	if two := byslug["team2"]; two.AiSpendMonthUsd != "1.25" || two.AiUnpricedCallsMonth != 2 || !two.SchemaOk || two.People != 1 || two.Status != "active" {
		t.Fatalf("team2 %+v", two)
	}
	if res.AiSpendMonthTotalUsd != "1.25" {
		t.Fatalf("total %s (0.004 rounds to 0.00 per team, total is the sum of exact values)", res.AiSpendMonthTotalUsd)
	}
	addTeams(t, st, 4, 3)
	with6, res := count()
	if len(res.Teams) != 6 {
		t.Fatalf("teams %d", len(res.Teams))
	}
	if with3 != with6 || with3 > 12 {
		t.Fatalf("statements: %d with 3 teams, %d with 6 (must not grow with teams)", with3, with6)
	}
}

func TestPlatformTeamSwitches(t *testing.T) {
	st := freshStore(t)
	addTeams(t, st, 2, 1)
	exec(t, st, `INSERT INTO team (id, slug, name, status) VALUES (9, 'wait', 'Wait', 'review')`)
	s := newSrv(st)
	ctx := opCtx()
	code := func(err error) (codes.Code, string) { st, _ := status.FromError(err); return st.Code(), st.Message() }

	if c, m := code(func() error {
		_, err := s.SetTeamStatus(ctx, &pb_admin.SetTeamStatusRequest{TeamSlug: "grbpwr", ReadOnly: true})
		return err
	}()); c != codes.FailedPrecondition || m != "team_one" {
		t.Fatalf("team #1 read-only: %v %s", c, m)
	}
	res, err := s.SetTeamStatus(ctx, &pb_admin.SetTeamStatusRequest{TeamSlug: "team2", ReadOnly: true})
	if err != nil || res.Team.Status != "read_only" {
		t.Fatalf("pause: %+v %v", res, err)
	}
	if c, _ := code(func() error {
		_, err := s.SetTeamStatus(ctx, &pb_admin.SetTeamStatusRequest{TeamSlug: "wait", ReadOnly: true})
		return err
	}()); c != codes.FailedPrecondition {
		t.Fatalf("review team pause: %v", c)
	}

	var oneOnly feature.Key
	for _, info := range feature.Catalog() {
		if info.TeamOneOnly {
			oneOnly = info.Key
			break
		}
	}
	if c, m := code(func() error {
		_, err := s.SetTeamFeature(ctx, &pb_admin.SetTeamFeatureRequest{TeamSlug: "team2", Feature: string(oneOnly), Enabled: true})
		return err
	}()); c != codes.FailedPrecondition || m != "team_one_only" {
		t.Fatalf("TeamOneOnly toggle: %v %s", c, m)
	}
	if c, _ := code(func() error {
		_, err := s.SetTeamFeature(ctx, &pb_admin.SetTeamFeatureRequest{TeamSlug: "team2", Feature: "nope", Enabled: true})
		return err
	}()); c != codes.InvalidArgument {
		t.Fatalf("unknown feature: %v", c)
	}

	if c, m := code(func() error {
		_, err := s.RenameTeam(ctx, &pb_admin.RenameTeamRequest{TeamSlug: "grbpwr", NewSlug: "other"})
		return err
	}()); c != codes.FailedPrecondition || m != "team_one" {
		t.Fatalf("team #1 slug: %v %s", c, m)
	}
	rn, err := s.RenameTeam(ctx, &pb_admin.RenameTeamRequest{TeamSlug: "team2", NewSlug: " Acme ", NewName: "Acme"})
	if err != nil || rn.Team.Slug != "acme" || rn.Team.Name != "Acme" {
		t.Fatalf("rename %+v %v", rn, err)
	}

	en, err := s.EnterTeam(ctx, &pb_admin.EnterTeamRequest{TeamSlug: "acme"})
	if err != nil || !strings.HasPrefix(en.HandoffCode, platformauth.OperatorCodePrefix) {
		t.Fatalf("enter %+v %v", en, err)
	}
	if c, _ := code(func() error { _, err := s.EnterTeam(ctx, &pb_admin.EnterTeamRequest{TeamSlug: "wait"}); return err }()); c != codes.FailedPrecondition {
		t.Fatalf("enter a review team: %v", c)
	}
	if _, err := s.ListTeams(context.Background(), &pb_admin.ListTeamsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller: %v", err)
	}
	var audits int
	if err := st.DB().Get(&audits, `SELECT COUNT(*) FROM platform_audit WHERE action IN ('team_status', 'team_rename', 'operator_enter')`); err != nil || audits != 3 {
		t.Fatalf("audit rows %d %v", audits, err)
	}
}
