package platformauth

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestIssueHandoffDeletedTeam (teams D1b, found end to end on beta): a member signed in at the root
// asks for a hand-off into their team after it was deleted. No code is issued and no email_token
// row is written; the answer is the same no-access a non-member gets. Also when this instance's
// directory still holds the team as active (a delete made by another instance): the store refuses
// under the team-row lock. A code issued before the delete does not come back with a restore.
func TestIssueHandoffDeletedTeam(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	const pw = "Member-Pass-7712"
	hash, err := f.svc.d.Hasher.HashPassword(pw)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{ // the first takes the hash
		`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES (701, 'del7@x.io', 'del7', ?, 'active')`,
		`INSERT INTO team (id, slug, name, db_name, status) VALUES (70, 'gone-soon', 'Gone Soon', 'lk_t_gone0070', 'active')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (70, 701, 'owner', 'active')`,
	} {
		var args []any
		if strings.Contains(q, "?") {
			args = []any{hash}
		}
		if _, err := f.st.DB().Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	sctx := ctxFrom("10.0.7.1", "")
	si, err := f.svc.SignIn(sctx, &auth.SignInRequest{Login: "del7@x.io", Password: pw, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(slug string) (*auth.IssueHandoffResponse, error) {
		return f.svc.IssueHandoff(sctx, si.GetToken(), &auth.IssueHandoffRequest{TeamSlug: slug})
	}
	tokens := func() (all, live int) {
		t.Helper()
		if err := f.st.DB().QueryRow(`SELECT COUNT(*), COALESCE(SUM(used_at IS NULL), 0) FROM email_token
WHERE team_id = 70 AND purpose = 'handoff'`).Scan(&all, &live); err != nil {
			t.Fatal(err)
		}
		return all, live
	}
	if ih, err := issue("gone-soon"); err != nil || ih.GetCode() == "" {
		t.Fatalf("hand-off into the live team: %v %v", ih, err)
	}
	_, noAccess := issue("no-such-team")
	if status.Code(noAccess) != codes.PermissionDenied {
		t.Fatalf("non-member answer: %v", noAccess)
	}

	if _, err := f.st.DeleteTeam(ctx, 70, f.clock, platformstore.DeleteRetention); err != nil {
		t.Fatal(err)
	}
	if all, live := tokens(); all != 1 || live != 0 {
		t.Fatalf("after delete: %d hand-offs, %d live (the earlier code is consumed)", all, live)
	}
	ih, err := issue("gone-soon")
	if err == nil || err.Error() != noAccess.Error() || ih.GetCode() != "" {
		t.Fatalf("hand-off into a deleted team: %v %v, want %v", ih, err, noAccess)
	}
	if all, _ := tokens(); all != 1 {
		t.Fatalf("a hand-off row was written for a deleted team: %d", all)
	}

	// Restored: the code from before the delete stays dead.
	if _, err := f.st.RestoreTeam(ctx, 70, f.clock); err != nil {
		t.Fatal(err)
	}
	if _, live := tokens(); live != 0 {
		t.Fatalf("a hand-off came back with the restore: %d live", live)
	}
	// A stale directory (another instance deleted the team): the slug still resolves here, the
	// store refuses under the team-row lock with the same answer.
	if tm, err := f.st.Directory().BySlug(ctx, "gone-soon"); err != nil || tm.ID != 70 {
		t.Fatalf("warm the directory: %+v %v", tm, err)
	}
	if _, err := f.st.DB().Exec(`UPDATE team SET status = 'deleted', deleted_at = UTC_TIMESTAMP(3),
 purge_at = UTC_TIMESTAMP(3) + INTERVAL 30 DAY, deleted_from = 'active' WHERE id = 70`); err != nil {
		t.Fatal(err)
	}
	ih, err = issue("gone-soon")
	if err == nil || err.Error() != noAccess.Error() || ih.GetCode() != "" {
		t.Fatalf("hand-off through a stale directory: %v %v, want %v", ih, err, noAccess)
	}
	if all, _ := tokens(); all != 1 {
		t.Fatalf("a hand-off row was written through a stale directory: %d", all)
	}
}
