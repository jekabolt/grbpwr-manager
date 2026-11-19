package platform

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/pwhash"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestDeleteRestoreAnswerByID: another operator renames the team between the handler's slug read
// and its transaction (delete) or right after the commit (restore). The committed change is
// answered, not NOT_FOUND, with the team's current slug, and the audit carries the identity the
// transaction locked.
func TestDeleteRestoreAnswerByID(t *testing.T) {
	st := freshStore(t)
	addTeams(t, st, 2, 1)
	s := newSrv(st)
	ctx := opCtx()
	s.SetPasswordCheck(func(context.Context, int64, string) error { return nil })
	fixed := s.now()

	// Delete: the clock is read after the slug and before the store transaction.
	renameOnClock := true
	s.now = func() time.Time {
		if renameOnClock {
			renameOnClock = false
			if err := st.RenameTeam(context.Background(), 2, "team2-new", "Team Two", fixed); err != nil {
				t.Errorf("rename in the gap: %v", err)
			}
		}
		return fixed
	}
	res, err := s.DeleteTeam(ctx, &pb_admin.DeleteTeamRequest{TeamSlug: "team2", Password: deletePassword})
	if err != nil {
		t.Fatalf("delete after a concurrent rename: %v", err)
	}
	if renameOnClock || res.Team.Slug != "team2-new" || res.Team.Status != "deleted" {
		t.Fatalf("delete answer %+v", res.Team)
	}
	var detail string
	if err := st.DB().Get(&detail, `SELECT CAST(detail AS CHAR) FROM platform_audit WHERE action = 'team_delete' AND team_id = 2`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, `"slug": "team2-new"`) || !strings.Contains(detail, `"name": "Team Two"`) {
		t.Fatalf("delete audit carries a stale identity: %s", detail)
	}

	// Restore: the rename lands right after the commit (the cache drop runs in that gap).
	renameOnDrop := true
	s.SetInvalidator(func(teamID int64) {
		if renameOnDrop && teamID == 2 {
			renameOnDrop = false
			if err := st.RenameTeam(context.Background(), 2, "team2-back", "", fixed); err != nil {
				t.Errorf("rename after the restore: %v", err)
			}
		}
	})
	rs, err := s.RestoreTeam(ctx, &pb_admin.RestoreTeamRequest{TeamSlug: "team2-new"})
	if err != nil {
		t.Fatalf("restore with a rename right after: %v", err)
	}
	if renameOnDrop || rs.Team.Slug != "team2-back" || rs.Team.Status != "active" {
		t.Fatalf("restore answer %+v", rs.Team)
	}
	if err := st.DB().Get(&detail, `SELECT CAST(detail AS CHAR) FROM platform_audit WHERE action = 'team_restore' AND team_id = 2`); err != nil {
		t.Fatalf("restore audit lost: %v", err)
	}
	if !strings.Contains(detail, `"slug": "team2-new"`) || !strings.Contains(detail, `"status": "active"`) {
		t.Fatalf("restore audit %s", detail)
	}
}

// TestDeleteOperatorWithoutUsablePassword: through the real sign-in check, an operator whose
// account has no usable password (empty or marked unusable) gets exactly the wrong-password
// answer, whatever they type.
func TestDeleteOperatorWithoutUsablePassword(t *testing.T) {
	st := freshStore(t)
	addTeams(t, st, 2, 1)
	ph, err := pwhash.New(16, 1000)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := platformauth.New(platformauth.Deps{
		Store:  st,
		Config: platformstore.Config{BaseDomain: "beta.lekalo.studio", TokenPepper: "0123456789abcdef0123456789abcdef-pepper"},
		JWT:    jwtauth.New("HS256", []byte("delete-test-secret-delete-test-secret-0123"), nil),
		Hasher: ph,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := newSrv(st)
	s.SetPasswordCheck(svc.CheckOperatorPassword)
	ctx := opCtx()
	answer := func(pw string) (codes.Code, string) {
		_, err := s.DeleteTeam(ctx, &pb_admin.DeleteTeamRequest{TeamSlug: "team2", Password: pw})
		sv, _ := status.FromError(err)
		return sv.Code(), sv.Message()
	}

	hash, err := ph.HashPassword(deletePassword)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, st, `UPDATE platform_user SET password_hash = ? WHERE id = 103`, hash)
	wc, wm := answer("not-" + deletePassword)
	if wc != codes.PermissionDenied || wm != "wrong_password" {
		t.Fatalf("wrong password: %v %s", wc, wm)
	}
	for _, unusable := range []string{"", "!unusable$00ff"} {
		exec(t, st, `UPDATE platform_user SET password_hash = ? WHERE id = 103`, unusable)
		if c, m := answer(deletePassword); c != wc || m != wm {
			t.Fatalf("hash %q: %v %s, want the wrong-password answer %v %s", unusable, c, m, wc, wm)
		}
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM team WHERE id = 2 AND status = 'active'`); err != nil || n != 1 {
		t.Fatalf("team changed: %d %v", n, err)
	}
}
