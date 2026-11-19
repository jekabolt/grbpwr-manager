package platform

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const deletePassword = "Correct-Horse-7731"

func TestPlatformDeleteAndRestoreTeam(t *testing.T) {
	st := freshStore(t)
	addTeams(t, st, 2, 1)
	exec(t, st, `INSERT INTO team (id, slug, name, db_name, status) VALUES (3, 'wait', 'Wait', NULL, 'review')`)
	exec(t, st, `INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('sann0000000000000000000000000000', 101, 2, 'team')`)
	s := newSrv(st)
	ctx := opCtx()
	code := func(err error) (codes.Code, string) { st, _ := status.FromError(err); return st.Code(), st.Message() }
	del := func(slug, pw string) (*pb_admin.DeleteTeamResponse, error) {
		return s.DeleteTeam(ctx, &pb_admin.DeleteTeamRequest{TeamSlug: slug, Password: pw})
	}

	// No password check wired: the console cannot confirm, nothing happens.
	if c, _ := code(func() error { _, err := del("team2", deletePassword); return err }()); c != codes.Unavailable {
		t.Fatalf("no password check: %v", c)
	}

	limited := false
	var checked []int64
	s.SetPasswordCheck(func(_ context.Context, userID int64, pw string) error {
		checked = append(checked, userID)
		if limited {
			return platformauth.ErrTooManyAttempts
		}
		if pw != deletePassword {
			return platformauth.ErrWrongPassword
		}
		return nil
	})
	var dropped []string
	s.SetSessionInvalidator(func(sid string) { dropped = append(dropped, sid) })
	var invalidated []int64
	s.SetInvalidator(func(teamID int64) { invalidated = append(invalidated, teamID) })

	for _, pw := range []string{"", "wrong"} {
		if c, m := code(func() error { _, err := del("team2", pw); return err }()); c != codes.PermissionDenied || m != "wrong_password" {
			t.Fatalf("password %q: %v %s", pw, c, m)
		}
	}
	// The password is checked first: a wrong one on team #1 or an unknown slug says nothing more.
	if c, m := code(func() error { _, err := del("grbpwr", "wrong"); return err }()); c != codes.PermissionDenied || m != "wrong_password" {
		t.Fatalf("wrong password on team #1: %v %s", c, m)
	}
	limited = true
	if c, _ := code(func() error { _, err := del("team2", deletePassword); return err }()); c != codes.ResourceExhausted {
		t.Fatalf("limited: %v", c)
	}
	limited = false
	for _, id := range checked {
		if id != 103 {
			t.Fatalf("password checked against user %d, not the calling operator", id)
		}
	}
	if c, m := code(func() error { _, err := del("grbpwr", deletePassword); return err }()); c != codes.FailedPrecondition || m != "team_one" {
		t.Fatalf("team #1: %v %s", c, m)
	}
	if c, m := code(func() error { _, err := del("wait", deletePassword); return err }()); c != codes.FailedPrecondition || m != "wrong_status" {
		t.Fatalf("review team: %v %s", c, m)
	}
	if c, _ := code(func() error { _, err := del("nope", deletePassword); return err }()); c != codes.NotFound {
		t.Fatalf("unknown slug: %v", c)
	}
	if _, err := s.DeleteTeam(context.Background(), &pb_admin.DeleteTeamRequest{TeamSlug: "team2", Password: deletePassword}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("no caller: %v", err)
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE action = 'team_delete'`); err != nil || n != 0 {
		t.Fatalf("refused deletes audited: %d %v", n, err)
	}

	res, err := del("team2", deletePassword)
	if err != nil {
		t.Fatal(err)
	}
	wantPurge := s.nowUTC().Add(30 * 24 * time.Hour)
	if res.Team.Status != "deleted" || res.Team.PurgeAt == nil || !res.Team.PurgeAt.AsTime().Equal(wantPurge) {
		t.Fatalf("delete answer %+v", res.Team)
	}
	sort.Strings(dropped)
	if strings.Join(dropped, ",") != "sann0000000000000000000000000000" {
		t.Fatalf("dropped sessions %v", dropped)
	}
	if len(invalidated) == 0 || invalidated[len(invalidated)-1] != 2 {
		t.Fatalf("edge feature cache not dropped: %v", invalidated)
	}
	var detail string
	if err := st.DB().Get(&detail, `SELECT CAST(detail AS CHAR) FROM platform_audit WHERE action = 'team_delete' AND team_id = 2 AND actor_user_id = 103`); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detail, deletePassword) || !strings.Contains(detail, `"slug": "team2"`) || !strings.Contains(detail, "2026-12-15T12:00:00Z") {
		t.Fatalf("audit detail %s", detail)
	}

	// The console lists it with purge_at; GetTeam reads it; every other action answers wrong_status.
	list, err := s.ListTeams(ctx, &pb_admin.ListTeamsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if last := list.Teams[len(list.Teams)-1]; last.Slug != "team2" || last.Status != "deleted" || last.PurgeAt == nil {
		t.Fatalf("list: last %+v", last)
	}
	if g, err := s.GetTeam(ctx, &pb_admin.GetTeamRequest{TeamSlug: "team2"}); err != nil || g.Team.Status != "deleted" {
		t.Fatalf("get deleted: %+v %v", g, err)
	}
	var someKey feature.Key
	for _, info := range feature.Catalog() {
		if !info.TeamOneOnly {
			someKey = info.Key
			break
		}
	}
	for name, call := range map[string]func() error{
		"status": func() error {
			_, err := s.SetTeamStatus(ctx, &pb_admin.SetTeamStatusRequest{TeamSlug: "team2", ReadOnly: true})
			return err
		},
		"feature": func() error {
			_, err := s.SetTeamFeature(ctx, &pb_admin.SetTeamFeatureRequest{TeamSlug: "team2", Feature: string(someKey), Enabled: true})
			return err
		},
		"rename": func() error {
			_, err := s.RenameTeam(ctx, &pb_admin.RenameTeamRequest{TeamSlug: "team2", NewName: "Other"})
			return err
		},
		"enter": func() error { _, err := s.EnterTeam(ctx, &pb_admin.EnterTeamRequest{TeamSlug: "team2"}); return err },
		"approve": func() error {
			_, err := s.ApproveTeam(ctx, &pb_admin.ApproveTeamRequest{TeamSlug: "team2"})
			return err
		},
		"delete": func() error { _, err := del("team2", deletePassword); return err },
		"decline": func() error {
			_, err := s.DeclineTeam(ctx, &pb_admin.DeclineTeamRequest{TeamSlug: "team2"})
			return err
		},
	} {
		if c, m := code(call()); c != codes.FailedPrecondition || m != "wrong_status" {
			t.Fatalf("%s on a deleted team: %v %s", name, c, m)
		}
	}

	// Restore: no password, back to active, audited.
	if c, m := code(func() error { _, err := s.RestoreTeam(ctx, &pb_admin.RestoreTeamRequest{TeamSlug: "wait"}); return err }()); c != codes.FailedPrecondition || m != "wrong_status" {
		t.Fatalf("restore a review team: %v %s", c, m)
	}
	rs, err := s.RestoreTeam(ctx, &pb_admin.RestoreTeamRequest{TeamSlug: "team2"})
	if err != nil || rs.Team.Status != "active" || rs.Team.PurgeAt != nil {
		t.Fatalf("restore %+v %v", rs, err)
	}
	if c, _ := code(func() error {
		_, err := s.RestoreTeam(ctx, &pb_admin.RestoreTeamRequest{TeamSlug: "team2"})
		return err
	}()); c != codes.FailedPrecondition {
		t.Fatalf("restore twice: %v", c)
	}
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE action = 'team_restore' AND team_id = 2`); err != nil || n != 1 {
		t.Fatalf("restore audit %d %v", n, err)
	}
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM platform_audit WHERE CAST(detail AS CHAR) LIKE ?`, "%"+deletePassword+"%"); err != nil || n != 0 {
		t.Fatalf("password in the audit: %d %v", n, err)
	}

	// A wipe that is due cannot be undone.
	if _, err := del("team2", deletePassword); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return wantPurge.Add(time.Minute) }
	if c, m := code(func() error {
		_, err := s.RestoreTeam(ctx, &pb_admin.RestoreTeamRequest{TeamSlug: "team2"})
		return err
	}()); c != codes.FailedPrecondition || m != "wrong_status" {
		t.Fatalf("restore after purge_at: %v %s", c, m)
	}
}
