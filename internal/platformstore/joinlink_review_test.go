package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Review major 1: a plain sign-up must not re-key the password of a join sign-up whose
// verify_join letter is still live.
func TestSignUpKeepsPasswordOfLiveJoinSignUp(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	_, _, now, _, token := seedJoinLink(t, st)
	email := "victim@x.io"
	res, err := st.JoinSignUp(ctx, platformstore.JoinSignUpParams{
		TeamID: 2, Link: token, Pepper: joinPepper, Email: email, PasswordHash: "victim-hash", HandleBase: "victim",
		TermsVersion: "2026-11", Now: now, MaxLettersPerHour: 3, PasswordMatches: func(string) bool { return false },
		Letter: func(userID int64, _ *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
			return platformstore.NewToken{Purpose: "verify_join", TokenHash: hex64('v'), CodeHash: fmt.Sprintf("code-%d", userID),
				ExpiresAt: now.Add(time.Hour)}, platformstore.Letter{Template: "join_code", TeamID: 2}, nil
		},
	})
	if err != nil || !res.Created {
		t.Fatalf("join signup = %+v, %v", res, err)
	}
	again, err := st.SignUp(ctx, platformstore.SignUpParams{
		Email: email, PasswordHash: "attacker-hash", TeamName: "Evil", Slug: "evil-team", HandleBase: "evil",
		Now: now.Add(time.Minute), PendingTTL: time.Hour, MaxLettersPerHour: 3,
		PasswordMatches: func(string) bool { return false },
		Letter: func(int64, *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
			return platformstore.NewToken{Purpose: "verify", TokenHash: hex64('e'), CodeHash: "c-evil", ExpiresAt: now.Add(time.Hour)},
				platformstore.Letter{Template: "verify"}, nil
		},
		TermsVersion: "2026-11",
	})
	if err != nil || again.Created || again.Reason != "pending_password_mismatch" {
		t.Fatalf("plain signup over live join signup = %+v, %v", again, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE email = ? AND password_hash = 'victim-hash'`, email); n != 1 {
		t.Fatalf("join sign-up password replaced (%d rows keep it)", n)
	}
}

// Review major 2: approving a request revokes the address's live invites, and so does removal; a
// removed member pressing the link again only asks.
func TestRemovedMemberCannotRejoinThroughInvite(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	owner, stranger, now, _, token := seedJoinLink(t, st)
	invite := func(c byte, at time.Time) {
		t.Helper()
		if _, err := st.DB().ExecContext(ctx, `INSERT INTO team_invite (team_id, email, role, permissions, token_hash, invited_by, expires_at, created_at)
 VALUES (2, 'new@x.io', 'owner', '[]', ?, ?, ?, ?)`, hex64(c), owner, at.Add(7*24*time.Hour), at); err != nil {
			t.Fatal(err)
		}
	}
	liveInvites := func() int {
		return countRows(t, st, `SELECT COUNT(*) FROM team_invite WHERE team_id = 2 AND email = 'new@x.io' AND accepted_at IS NULL AND revoked_at IS NULL`)
	}
	// a request first, then an invite for the same address, then the request is approved
	if res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now}); err != nil || res.State != platformstore.JoinPending {
		t.Fatalf("request = %+v, %v", res, err)
	}
	invite('a', now.Add(time.Minute))
	rows, err := st.PendingJoinRequests(ctx, 2, now.Add(2*time.Minute))
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending = %+v, %v", rows, err)
	}
	if res, err := st.ApproveJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: 2, RequestID: rows[0].ID, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(2 * time.Minute),
		Role: "member", ApprovedLink: "https://acme.example/",
		Stage:  func(context.Context, platformstore.JoinMember) error { return nil },
		Enable: func(context.Context, platformstore.JoinMember) (bool, error) { return true, nil },
	}); err != nil || res.State != platformstore.JoinJoined {
		t.Fatalf("approve = %+v, %v", res, err)
	}
	if n := liveInvites(); n != 0 {
		t.Fatalf("live invites after approval = %d", n)
	}
	// an invite sent while the person is a member dies with the removal
	invite('b', now.Add(3*time.Minute))
	if _, err := st.RemoveMember(ctx, platformstore.MemberChangeParams{
		TeamID: 2, UserID: stranger, Actor: platformstore.Actor{UserID: owner}, MirrorOwners: []int64{owner}, Now: now.Add(4 * time.Minute),
	}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if n := liveInvites(); n != 0 {
		t.Fatalf("live invites after removal = %d", n)
	}
	res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(5 * time.Minute)})
	if err != nil || res.State != platformstore.JoinPending {
		t.Fatalf("removed member rejoin = %+v, %v", res, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND user_id = ? AND status IN ('joining','active')`, stranger); n != 0 {
		t.Fatalf("removed member back in team (%d rows)", n)
	}
}

// Review major 3: a press while an approved join is still finishing opens no second request, and a
// stale pending request of an active member changes nothing when approved.
func TestStaleJoinRequestKeepsMemberRights(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	owner, stranger, now, link, token := seedJoinLink(t, st)
	if res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now}); err != nil || res.State != platformstore.JoinPending {
		t.Fatalf("request = %+v, %v", res, err)
	}
	rows, err := st.PendingJoinRequests(ctx, 2, now)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending = %+v, %v", rows, err)
	}
	approve := func(id int64, role string, at time.Time, stage func(context.Context, platformstore.JoinMember) error) (platformstore.JoinDecisionResult, error) {
		return st.ApproveJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
			TeamID: 2, RequestID: id, Actor: platformstore.Actor{UserID: owner}, Now: at, Role: role,
			Permissions: []byte(`[{"section":"orders","access":"write"}]`), ApprovedLink: "https://acme.example/",
			Stage: stage, Enable: func(context.Context, platformstore.JoinMember) (bool, error) { return true, nil },
		})
	}
	stageErr := errors.New("team database down")
	if _, err := approve(rows[0].ID, "owner", now.Add(time.Minute), func(context.Context, platformstore.JoinMember) error { return stageErr }); !errors.Is(err, stageErr) {
		t.Fatalf("stage failure: %v", err)
	}
	// the member is 'joining' with an approved request: another press is already pending
	res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(2 * time.Minute)})
	if err != nil || res.State != platformstore.JoinPending {
		t.Fatalf("press while joining = %+v, %v", res, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_join_request WHERE team_id = 2 AND status = 'pending'`); n != 0 {
		t.Fatalf("second request opened while joining (%d pending)", n)
	}
	if res, err := approve(rows[0].ID, "owner", now.Add(3*time.Minute), func(context.Context, platformstore.JoinMember) error { return nil }); err != nil || res.State != platformstore.JoinJoined {
		t.Fatalf("approve retry = %+v, %v", res, err)
	}
	// a stale pending row (left by an older build) must not re-stage an active owner as a member
	if _, err := st.DB().ExecContext(ctx, `INSERT INTO team_join_request (team_id, user_id, gen, status, created_at) VALUES (2, ?, ?, 'pending', ?)`,
		stranger, link.Gen, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	stale, err := st.PendingJoinRequests(ctx, 2, now.Add(4*time.Minute))
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale pending = %+v, %v", stale, err)
	}
	staged := false
	res, err = approve(stale[0].ID, "member", now.Add(5*time.Minute), func(context.Context, platformstore.JoinMember) error { staged = true; return nil })
	if err != nil || res.State != platformstore.JoinAlreadyMember || staged {
		t.Fatalf("stale approve = %+v staged=%v, %v", res, staged, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND user_id = ? AND status = 'active' AND role = 'owner'`, stranger); n != 1 {
		t.Fatalf("owner demoted or disabled by a stale approve")
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_join_request WHERE team_id = 2 AND status = 'pending'`); n != 0 {
		t.Fatalf("stale request left open (%d)", n)
	}
}
