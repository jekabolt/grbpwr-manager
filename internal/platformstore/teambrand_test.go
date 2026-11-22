package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// TestTeamBrand: team #1's seed is written once and an edit survives every boot; a write needs an
// active team (paused → ErrTeamReadOnly) and its owner or an operator; teams never see each other's
// row; the row goes with every hard delete of its team and stays through a soft delete and a logo
// removal.
func TestTeamBrand(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, op := seedConsole(t, st)
	db := st.DB()
	now := time.Now().UTC().Truncate(time.Millisecond)

	// The seed: today's papers' data, no author.
	one, ok, err := st.TeamBrand(ctx, 1)
	if err != nil || !ok {
		t.Fatalf("team #1 brand %v %v", ok, err)
	}
	if one.BrandName != "GRBPWR" || one.LegalEntity != "GRBPWR LIMITED" || one.ContactEmail != "customercare@grbpwr.com" ||
		one.Website != "grbpwr.com" || strings.Join(one.LegalAddress, "|") != "167-169 GREAT PORTLAND STREET|5TH FLOOR, LONDON, W1W 5PF" {
		t.Fatalf("seed %+v", one)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_brand WHERE team_id = 1 AND updated_by IS NULL`) != 1 {
		t.Fatal("the seed has an author")
	}

	// boss edits team #1; the next boots (any profile) leave the edit alone.
	var boss int64
	if err := db.Get(&boss, `SELECT id FROM platform_user WHERE handle = 'boss'`); err != nil {
		t.Fatal(err)
	}
	edited := platformstore.TeamBrand{BrandName: "GRBPWR", LegalEntity: "GRBPWR LTD", LegalAddress: []string{"1 New Street"}}
	changed, err := st.SetTeamBrand(ctx, platformstore.SetTeamBrandParams{TeamID: 1, Actor: platformstore.Actor{UserID: boss}, Brand: edited})
	if err != nil || strings.Join(changed, ",") != "legal_entity,legal_address,contact_email,website" {
		t.Fatalf("edit team #1: %v %v", changed, err)
	}
	admins := []platformstore.TeamOneAdmin{{Username: "boss", PasswordHash: "h", Super: true}, {Username: "helper", PasswordHash: "h"}}
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", admins); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureTeamOneProfile(ctx, "grbpwr_test", admins, platformstore.Team1ProfileProduction); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := st.TeamBrand(ctx, 1); got.LegalEntity != "GRBPWR LTD" || strings.Join(got.LegalAddress, "|") != "1 New Street" ||
		got.ContactEmail != "" || got.Website != "" {
		t.Fatalf("a boot overwrote the edit: %+v", got)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_brand WHERE team_id = 1 AND updated_by = ?`, boss) != 1 {
		t.Fatal("the edit lost its author")
	}

	// acme: no row yet, then ann's write; the same write again changes nothing; team #1 untouched.
	if _, ok, err := st.TeamBrand(ctx, 2); ok || err != nil {
		t.Fatalf("new team has a brand: %v %v", ok, err)
	}
	acme := platformstore.TeamBrand{BrandName: "Acme", LegalEntity: "Acme Ltd", LegalAddress: []string{"a", "b", "c"},
		ContactEmail: "hi@acme.io", Website: "acme.io"}
	set := func(team, actor int64, operator bool, b platformstore.TeamBrand) ([]string, error) {
		return st.SetTeamBrand(ctx, platformstore.SetTeamBrandParams{TeamID: team, Actor: platformstore.Actor{UserID: actor, Operator: operator}, Brand: b})
	}
	if changed, err := set(2, ann, false, acme); err != nil || len(changed) != 5 {
		t.Fatalf("first write %v %v", changed, err)
	}
	if changed, err := set(2, ann, false, acme); err != nil || len(changed) != 0 {
		t.Fatalf("same write %v %v", changed, err)
	}
	if got, ok, _ := st.TeamBrand(ctx, 2); !ok || fmt.Sprint(got) != fmt.Sprint(acme) {
		t.Fatalf("acme %+v", got)
	}
	if got, _, _ := st.TeamBrand(ctx, 1); got.LegalEntity != "GRBPWR LTD" {
		t.Fatal("acme's write reached team #1")
	}
	// A new brand name renames the team (header, sign-in page, letters); the slug stays.
	if _, err := set(2, ann, false, platformstore.TeamBrand{BrandName: "acme atelier"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team WHERE id = 2 AND name = 'acme atelier' AND slug = 'acme'`) != 1 {
		t.Fatal("the brand name did not reach the team name")
	}
	// An empty brand name leaves the team name alone.
	if _, err := set(2, ann, false, platformstore.TeamBrand{}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team WHERE id = 2 AND name = 'acme atelier'`) != 1 {
		t.Fatal("an empty brand name renamed the team")
	}
	if _, err := set(2, ann, false, acme); err != nil {
		t.Fatalf("restore: %v", err)
	}
	// An operator may write; a member may not; nor an owner of another team.
	if _, err := set(2, op, true, acme); err != nil {
		t.Fatalf("operator: %v", err)
	}
	if _, err := set(2, bob, false, acme); !errors.Is(err, platformstore.ErrNotOwner) {
		t.Fatalf("member: %v, want ErrNotOwner", err)
	}
	if _, err := set(1, ann, false, acme); !errors.Is(err, platformstore.ErrNotOwner) {
		t.Fatalf("owner of another team: %v, want ErrNotOwner", err)
	}
	if _, err := set(2, ann, false, platformstore.TeamBrand{LegalAddress: []string{"1", "2", "3", "4"}}); err == nil {
		t.Fatal("four address lines stored")
	}
	// Status: paused refuses with its own error, a team in review or unknown refuses too.
	if _, err := db.Exec(`UPDATE team SET status = 'suspended' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := set(2, ann, false, platformstore.TeamBrand{BrandName: "Paused"}); !errors.Is(err, platformstore.ErrTeamReadOnly) {
		t.Fatalf("paused: %v, want ErrTeamReadOnly", err)
	}
	if got, _, _ := st.TeamBrand(ctx, 2); got.BrandName != "Acme" {
		t.Fatal("a paused team's brand changed")
	}
	if _, err := db.Exec(`UPDATE team SET status = 'active' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := set(3, ann, false, acme); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("review: %v, want ErrWrongTeamStatus", err)
	}
	if _, err := set(999, ann, false, acme); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("no team: %v, want ErrTeamNotFound", err)
	}

	// Kept: a logo removal, a soft delete and its restore.
	if err := st.SetTeamLogo(ctx, platformstore.SetTeamLogoParams{TeamID: 2, UserID: ann, SID: "sann0000000000000000000000000000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM team_brand WHERE team_id = 2`) != 1 {
		t.Fatal("a soft delete took the brand")
	}
	if _, err := st.RestoreTeam(ctx, 2, now); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := st.TeamBrand(ctx, 2); !ok || got.BrandName != "Acme" {
		t.Fatal("restored team lost its brand")
	}

	// Gone with every hard delete: a declined review, the pending sweep, the replace of an expired
	// pending team by CreateTeam (the same deletePendingTeamsTx serves the provider claim and the
	// sign-up paths). A live pending team keeps its row.
	seedCreateTeamSessions(t, st)
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, status, created_by, pending_expires_at) VALUES
 (8, 'pend', 'Pend', 'pending', 101, UTC_TIMESTAMP(3) - INTERVAL 1 HOUR),
 (80, 'fresh', 'Fresh', 'pending', 102, UTC_TIMESTAMP(3) + INTERVAL 1 DAY)`,
		`INSERT INTO team_brand (team_id, brand_name) VALUES (3, 'w'), (8, 'p'), (80, 'f')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DeclineTeam(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if n, err := st.SweepPendingTeams(ctx, now, 10); err != nil || n != 1 {
		t.Fatalf("sweep %d %v", n, err)
	}
	// Inserted after the sweep, so the replace below is what removes it.
	for _, q := range []string{
		`INSERT INTO team (id, slug, name, status, created_by, pending_expires_at) VALUES (81, 'stale', 'Stale', 'pending', 102, UTC_TIMESTAMP(3) - INTERVAL 1 DAY)`,
		`INSERT INTO team_brand (team_id, brand_name) VALUES (81, 's')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.CreateTeam(ctx, newTeam(102, "stale", now)); err != nil {
		t.Fatalf("replace an expired pending team: %v", err)
	}
	for id, want := range map[int64]int{3: 0, 8: 0, 81: 0, 80: 1, 1: 1, 2: 1} {
		if got := countRows(t, st, `SELECT COUNT(*) FROM team_brand WHERE team_id = ?`, id); got != want {
			t.Errorf("team %d brand rows = %d, want %d", id, got, want)
		}
	}
}

// TestRevokeMemberSessions: the session step of a member-rights change — only an active member's
// team sessions in this team, by an owner or operator of an active team.
func TestRevokeMemberSessions(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, op := seedConsole(t, st)
	now := time.Now().UTC()
	revoke := func(actor int64, operator bool, target int64) (platformstore.MemberChange, error) {
		return st.RevokeMemberSessions(ctx, platformstore.MemberChangeParams{TeamID: 2, UserID: target,
			Actor: platformstore.Actor{UserID: actor, Operator: operator}, Now: now})
	}
	if _, err := revoke(bob, false, bob); !errors.Is(err, platformstore.ErrNotOwner) {
		t.Fatalf("member actor: %v, want ErrNotOwner", err)
	}
	if _, err := revoke(ann, false, ann); !errors.Is(err, platformstore.ErrOwnerTarget) {
		t.Fatalf("owner target: %v, want ErrOwnerTarget", err)
	}
	if _, err := revoke(ann, false, op); !errors.Is(err, platformstore.ErrNotMember) {
		t.Fatalf("not a member: %v, want ErrNotMember", err)
	}
	if _, err := st.DB().Exec(`UPDATE team SET status = 'suspended' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := revoke(ann, false, bob); !errors.Is(err, platformstore.ErrTeamReadOnly) {
		t.Fatalf("paused: %v, want ErrTeamReadOnly", err)
	}
	if _, err := st.DB().Exec(`UPDATE team SET status = 'active' WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE revoked_at IS NOT NULL`) != 0 {
		t.Fatal("a refusal revoked a session")
	}
	ch, err := revoke(ann, false, bob)
	if err != nil || ch.Handle != "bob" || strings.Join(ch.RevokedSessions, ",") != "sbob0000000000000000000000000000" {
		t.Fatalf("revoke bob: %+v %v", ch, err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM user_session WHERE sid = 'sbob1000000000000000000000000000' AND revoked_at IS NULL`) != 1 {
		t.Fatal("bob's console session was revoked")
	}
	// Idempotent; an operator may do it.
	if ch, err := revoke(op, true, bob); err != nil || len(ch.RevokedSessions) != 0 {
		t.Fatalf("retry by operator: %+v %v", ch, err)
	}
}

// TestTeamBrandMigrationRerun: 0008 applied again under the servers' ANSI sql_mode is a no-op.
func TestTeamBrandMigrationRerun(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	raw, err := os.ReadFile("sql/0008_team_brand.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := string(raw)
	up = up[strings.Index(up, "-- +migrate Up"):strings.Index(up, "-- +migrate Down")]
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `SET SESSION sql_mode = 'ANSI'`); err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		for _, q := range strings.Split(up, ";\n") {
			if strings.TrimSpace(stripComments(q)) == "" {
				continue
			}
			if _, err := conn.ExecContext(ctx, q); err != nil {
				t.Fatalf("round %d: %v\n%s", round, err, q)
			}
		}
	}
	if countRows(t, st, `SELECT COUNT(*) FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'team_brand'`) != 10 {
		t.Fatal("team_brand columns")
	}
}
