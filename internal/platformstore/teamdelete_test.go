package platformstore_test

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// seedDelete adds to seedConsole: acme is the newest team, has an open and an accepted invite, an
// unexchanged hand-off, and ann has a verify token (no team); team 6 is a legacy deleted team
// without purge_at.
func seedDelete(t *testing.T, st *platformstore.Store) {
	t.Helper()
	seedConsole(t, st)
	for _, q := range []string{
		`UPDATE team SET created_at = '2026-11-10' WHERE id = 2`,
		`INSERT INTO team (id, slug, name, db_name, status, created_at) VALUES (6, 'gone', 'Gone', NULL, 'deleted', '2026-11-01')`,
		`INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at, accepted_at) VALUES
 (2, 'new@x.io', 'member', '[]', REPEAT('a', 64), 101, '2099-01-01', NULL),
 (2, 'bob@x.io', 'member', '[]', REPEAT('b', 64), 101, '2099-01-01', '2026-11-05')`,
		`INSERT INTO email_token (user_id, team_id, purpose, token_hash, expires_at) VALUES
 (101, 2, 'handoff', REPEAT('c', 64), '2099-01-01'), (101, NULL, 'verify', REPEAT('d', 64), '2099-01-01')`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

func consoleSlugs(t *testing.T, st *platformstore.Store) string {
	t.Helper()
	teams, err := st.ConsoleTeams(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	var s []string
	for _, tm := range teams {
		s = append(s, tm.Slug)
	}
	return strings.Join(s, ",")
}

func TestDeleteAndRestoreTeam(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)
	db := st.DB()
	now := time.Now().UTC().Truncate(time.Millisecond)

	if got := consoleSlugs(t, st); got != "grbpwr,acme,prov,wait" {
		t.Fatalf("before: %s (legacy deleted team without purge_at is hidden)", got)
	}
	// The directory has acme cached before the delete: the delete must drop it.
	if _, err := st.Directory().BySlug(ctx, "acme"); err != nil {
		t.Fatal(err)
	}

	del, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention)
	if err != nil {
		t.Fatal(err)
	}
	if !del.PurgeAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("purge_at %v", del.PurgeAt)
	}
	sort.Strings(del.SIDs)
	if strings.Join(del.SIDs, ",") != "sann0000000000000000000000000000,sbob0000000000000000000000000000" {
		t.Fatalf("revoked sids %v (team sessions only, not bob's console one)", del.SIDs)
	}
	var row struct {
		Status      string         `db:"status"`
		DeletedAt   sql.NullTime   `db:"deleted_at"`
		PurgeAt     sql.NullTime   `db:"purge_at"`
		DeletedFrom sql.NullString `db:"deleted_from"`
	}
	if err := db.Get(&row, `SELECT status, deleted_at, purge_at, deleted_from FROM team WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if row.Status != "deleted" || !row.DeletedAt.Valid || !row.PurgeAt.Valid || row.DeletedFrom.String != "active" {
		t.Fatalf("row %+v", row)
	}
	var n int
	if err := db.Get(&n, `SELECT
 (SELECT COUNT(*) FROM user_session WHERE team_id = 2 AND revoked_at IS NULL) * 1000
 + (SELECT COUNT(*) FROM user_session WHERE sid = 'sbob1000000000000000000000000000' AND revoked_at IS NULL) * 100
 + (SELECT COUNT(*) FROM team_invite WHERE team_id = 2 AND revoked_at IS NOT NULL) * 10
 + (SELECT COUNT(*) FROM email_token WHERE used_at IS NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	// 0 live team sessions, the console session lives, 1 invite revoked (the open one), 1 token used
	// (the hand-off; the verify token stays).
	if n != 111 {
		t.Fatalf("sessions/invites/tokens = %d", n)
	}
	if err := db.Get(&n, `SELECT COUNT(*) FROM team_member WHERE team_id = 2`); err != nil || n != 2 {
		t.Fatalf("membership stays: %d %v", n, err)
	}

	// From outside the team is gone; in the console it is listed last.
	if _, err := st.Directory().BySlug(ctx, "acme"); !errors.Is(err, tenant.ErrNotFound) {
		t.Fatalf("directory after delete: %v", err)
	}
	if tm, err := st.Directory().ByID(ctx, 2); err != nil || tm.Status != tenant.StatusDeleted {
		t.Fatalf("by id %+v %v", tm, err)
	}
	if got := consoleSlugs(t, st); got != "grbpwr,prov,wait,acme" {
		t.Fatalf("after: %s (deleted last)", got)
	}
	if _, err := st.ConsoleTeam(ctx, "acme"); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("console actions on a deleted team: %v", err)
	}
	tm, err := st.ConsoleTeamWithDeleted(ctx, "acme")
	if err != nil || tm.Status != "deleted" || !tm.PurgeAt.Valid || !tm.PurgeAt.Time.Equal(del.PurgeAt) {
		t.Fatalf("with deleted %+v %v", tm, err)
	}
	if err := st.SetTeamReadOnly(ctx, 2, true); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("read-only on deleted: %v", err)
	}
	if err := st.RenameTeam(ctx, 2, "acme-x", "", now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("rename deleted: %v", err)
	}

	// Wrong statuses.
	for _, id := range []int64{tenant.TeamOneID, 2, 3, 4, 6} {
		if _, err := st.DeleteTeam(ctx, id, now, platformstore.DeleteRetention); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
			t.Fatalf("delete team %d: %v", id, err)
		}
	}
	if _, err := st.DeleteTeam(ctx, 99, now, platformstore.DeleteRetention); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	for _, id := range []int64{tenant.TeamOneID, 3, 4, 6} {
		if err := st.RestoreTeam(ctx, id, now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
			t.Fatalf("restore team %d: %v", id, err)
		}
	}
	if err := st.RestoreTeam(ctx, 99, now); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("restore missing: %v", err)
	}
	if err := st.RestoreTeam(ctx, 2, del.PurgeAt); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("restore when the wipe is due: %v", err)
	}

	// Restore: back to active, columns cleared, sessions and invites stay revoked.
	if err := st.RestoreTeam(ctx, 2, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.Get(&row, `SELECT status, deleted_at, purge_at, deleted_from FROM team WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if row.Status != "active" || row.DeletedAt.Valid || row.PurgeAt.Valid || row.DeletedFrom.Valid {
		t.Fatalf("restored row %+v", row)
	}
	if err := db.Get(&n, `SELECT
 (SELECT COUNT(*) FROM user_session WHERE team_id = 2 AND revoked_at IS NULL) * 10
 + (SELECT COUNT(*) FROM team_invite WHERE team_id = 2 AND revoked_at IS NULL AND accepted_at IS NULL)`); err != nil || n != 0 {
		t.Fatalf("revocations stay: %d %v", n, err)
	}
	if got, err := st.Directory().BySlug(ctx, "acme"); err != nil || got.Status != tenant.StatusActive {
		t.Fatalf("directory after restore %+v %v", got, err)
	}
	if err := st.RestoreTeam(ctx, 2, now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("restore an active team: %v", err)
	}

	// A read-only team comes back read-only.
	if err := st.SetTeamReadOnly(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	if err := st.RestoreTeam(ctx, 2, now); err != nil {
		t.Fatal(err)
	}
	if tm, err := st.ConsoleTeam(ctx, "acme"); err != nil || tm.Status != "suspended" {
		t.Fatalf("restored read-only %+v %v", tm, err)
	}
}

func TestDeletedTeamKeepsSlug(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)
	now := time.Now().UTC()

	// acme → acme-two: "acme" is a retired slug redirecting to team 2.
	if err := st.RenameTeam(ctx, 2, "acme-two", "", now); err != nil {
		t.Fatal(err)
	}
	if got, err := st.Directory().BySlug(ctx, "acme"); err != nil || got.ID != 2 {
		t.Fatalf("redirect before delete %+v %v", got, err)
	}
	if _, err := st.DeleteTeam(ctx, 2, now, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"acme", "acme-two"} {
		if _, err := st.Directory().BySlug(ctx, slug); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("%s resolves after delete: %v", slug, err)
		}
		if taken, err := st.SlugTaken(ctx, slug, now); err != nil || !taken {
			t.Fatalf("%s free after delete: %v %v", slug, taken, err)
		}
		if err := st.RenameTeam(ctx, 4, slug, "", now); !errors.Is(err, platformstore.ErrSlugTaken) {
			t.Fatalf("rename onto %s: %v", slug, err)
		}
	}
}

func TestSchemaTeamsKeepsDeletedUntilWipe(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedDelete(t, st)

	listed := func() bool {
		teams, err := st.SchemaTeams(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, tm := range teams {
			if tm.ID == 2 {
				return true
			}
		}
		return false
	}
	if _, err := st.DeleteTeam(ctx, 2, time.Now().UTC(), platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	if !listed() {
		t.Fatal("a deleted team whose wipe is ahead must stay in the fleet migrator's list")
	}
	if _, err := st.DB().Exec(`UPDATE team SET purge_at = UTC_TIMESTAMP(3) - INTERVAL 1 MINUTE WHERE id = 2`); err != nil {
		t.Fatal(err)
	}
	if listed() {
		t.Fatal("a team whose wipe is due is not migrated")
	}
}
