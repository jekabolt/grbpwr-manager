package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

const joinPepper = "join-link-test-pepper-32-bytes-long"

func seedJoinLink(t *testing.T, st *platformstore.Store) (owner, stranger int64, now time.Time, link platformstore.TeamJoinLink, token string) {
	t.Helper()
	owner, _, _ = seedConsole(t, st)
	stranger = 104
	now = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	if _, err := st.DB().Exec(`UPDATE platform_user SET email_verified_at = ? WHERE id IN (101,102)`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO platform_user
(id, email, handle, password_hash, email_verified_at, status) VALUES
(104, 'new@x.io', 'new-person', 'h', ?, 'active')`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	var err error
	link, err = st.EnsureJoinLink(context.Background(), platformstore.JoinLinkChange{
		TeamID: 2, Actor: platformstore.Actor{UserID: owner}, IP: "2001:db8::1", Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	token = platformstore.JoinLinkToken(joinPepper, link.TeamID, link.Gen, link.Nonce)
	return owner, stranger, now, link, token
}

func TestJoinLinkTokenAndRequestRef(t *testing.T) {
	nonce := []byte("0123456789abcdef")
	a := platformstore.JoinLinkToken(joinPepper, 42, 7, nonce)
	b := platformstore.JoinLinkToken(joinPepper, 42, 7, nonce)
	if len(a) != 22 || a != b || !platformstore.JoinLinkMatches(joinPepper, 42, 7, nonce, a) {
		t.Fatalf("token %q %q", a, b)
	}
	if platformstore.JoinLinkMatches(joinPepper, 42, 8, nonce, a) ||
		platformstore.JoinLinkMatches(joinPepper, 42, 7, []byte("fedcba9876543210"), a) {
		t.Fatal("join token survived a generation or nonce change")
	}
	if a := platformstore.JoinRequestRef(joinPepper, 9); len(a) != 22 || a == platformstore.JoinRequestRef(joinPepper, 10) {
		t.Fatalf("request refs %q %q", a, platformstore.JoinRequestRef(joinPepper, 10))
	}
}

func TestJoinLinkEnsureRotateAndCaps(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	owner, stranger, now, first, token := seedJoinLink(t, st)
	again, err := st.EnsureJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: 2, Actor: platformstore.Actor{UserID: owner}, IP: "10.0.0.1", Now: now.Add(time.Minute),
	})
	if err != nil || again.Gen != first.Gen || string(again.Nonce) != string(first.Nonce) {
		t.Fatalf("second copy = %+v, %v", again, err)
	}
	res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{
		TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, IP: "10.0.0.2", Now: now.Add(2 * time.Minute),
		RequestLink: "https://acme.example/workspace#people",
	})
	if err != nil || res.State != platformstore.JoinPending {
		t.Fatalf("pending request = %+v, %v", res, err)
	}
	rotated, err := st.RotateJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: 2, Actor: platformstore.Actor{UserID: owner}, IP: "10.0.0.1", Now: now.Add(3 * time.Minute),
	})
	if err != nil || rotated.Gen != 2 || string(rotated.Nonce) == string(first.Nonce) {
		t.Fatalf("rotate = %+v, %v", rotated, err)
	}
	if _, err := st.JoinLinkInfo(ctx, 2, token, joinPepper); !errors.Is(err, platformstore.ErrJoinLinkInvalid) {
		t.Fatalf("old token: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_join_request WHERE team_id = 2 AND status = 'closed'`); n != 1 {
		t.Fatalf("closed requests = %d", n)
	}
	for i := 2; i <= 10; i++ {
		_, err := st.RotateJoinLink(ctx, platformstore.JoinLinkChange{
			TeamID: 2, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(time.Duration(i+2) * time.Minute),
		})
		if err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
	}
	if _, err := st.RotateJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: 2, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(20 * time.Minute),
	}); !errors.Is(err, platformstore.ErrRotateCap) {
		t.Fatalf("eleventh rotation: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_audit WHERE team_id = 2 AND action = 'join_link_copy'`); n != 2 {
		t.Fatalf("copy audits = %d", n)
	}
}

func TestJoinLinkUsesNewestLiveInvite(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	owner, stranger, now, _, token := seedJoinLink(t, st)
	if _, err := st.DB().Exec(`INSERT INTO team_join_request
(team_id, user_id, gen, status, created_at) VALUES (2, ?, 1, 'pending', ?)`, stranger, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO team_invite
(team_id, email, role, permissions, token_hash, invited_by, expires_at, created_at) VALUES
(2, 'new@x.io', 'member', '[{"section":"orders","access":"read"}]', ?, ?, ?, ?),
(2, 'new@x.io', 'owner', '[]', ?, ?, ?, ?)`,
		hex64('a'), owner, now.Add(time.Hour), now.Add(-time.Minute), hex64('b'), owner, now.Add(time.Hour), now); err != nil {
		t.Fatal(err)
	}
	var staged, enabled platformstore.JoinMember
	res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{
		TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, IP: "10.2.3.4", Now: now.Add(time.Minute),
		Stage:  func(_ context.Context, m platformstore.JoinMember) error { staged = m; return nil },
		Enable: func(_ context.Context, m platformstore.JoinMember) (bool, error) { enabled = m; return true, nil },
	})
	if err != nil || res.State != platformstore.JoinJoined || res.EnablePending {
		t.Fatalf("join = %+v, %v", res, err)
	}
	if staged.InviteID == 0 || staged.Role != "owner" || enabled.InviteID != staged.InviteID {
		t.Fatalf("staged %+v enabled %+v", staged, enabled)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_invite WHERE id = ? AND accepted_at IS NOT NULL`, staged.InviteID); n != 1 {
		t.Fatalf("accepted newest invite = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_invite WHERE team_id = 2 AND accepted_at IS NULL`); n != 1 {
		t.Fatalf("older invite changed, live = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_join_request WHERE team_id = 2 AND user_id = ? AND status = 'closed'`, stranger); n != 1 {
		t.Fatalf("stale join requests closed = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_audit WHERE action = 'join_by_link' AND ip = '10.2.3.4'`); n != 1 {
		t.Fatalf("join_by_link audits = %d", n)
	}
}

func TestJoinRequestCaps(t *testing.T) {
	t.Run("team queue", func(t *testing.T) {
		st := openFresh(t)
		_, stranger, now, _, token := seedJoinLink(t, st)
		for i := 0; i < 50; i++ {
			if _, err := st.DB().Exec(`INSERT INTO platform_user
(id, email, handle, password_hash, email_verified_at, status) VALUES (?, ?, ?, 'h', ?, 'active')`,
				200+i, fmt.Sprintf("queue-%d@x.io", i), fmt.Sprintf("queue-%d", i), now); err != nil {
				t.Fatal(err)
			}
			if _, err := st.DB().Exec(`INSERT INTO team_join_request
(team_id, user_id, gen, status, created_at) VALUES (2, ?, 1, 'pending', ?)`, 200+i, now); err != nil {
				t.Fatal(err)
			}
		}
		_, err := st.JoinTeam(context.Background(), platformstore.JoinDecisionParams{
			TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(time.Minute),
		})
		if !errors.Is(err, platformstore.ErrJoinQueueFull) {
			t.Fatalf("full queue: %v", err)
		}
	})

	t.Run("team hour", func(t *testing.T) {
		st := openFresh(t)
		_, stranger, now, _, token := seedJoinLink(t, st)
		for i := 0; i < 20; i++ {
			if _, err := st.DB().Exec(`INSERT INTO team_join_request
(team_id, user_id, gen, status, created_at) VALUES (2, ?, 1, 'closed', ?)`, stranger, now); err != nil {
				t.Fatal(err)
			}
		}
		_, err := st.JoinTeam(context.Background(), platformstore.JoinDecisionParams{
			TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(time.Minute),
		})
		if !errors.Is(err, platformstore.ErrJoinRateLimited) {
			t.Fatalf("team hour: %v", err)
		}
	})

	t.Run("user open", func(t *testing.T) {
		st := openFresh(t)
		_, stranger, now, _, token := seedJoinLink(t, st)
		for i := 0; i < 5; i++ {
			if _, err := st.DB().Exec(`INSERT INTO team_join_request
(team_id, user_id, gen, status, created_at) VALUES (?, ?, 1, 'pending', ?)`, 10+i, stranger, now); err != nil {
				t.Fatal(err)
			}
		}
		_, err := st.JoinTeam(context.Background(), platformstore.JoinDecisionParams{
			TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(time.Minute),
		})
		if !errors.Is(err, platformstore.ErrJoinRateLimited) {
			t.Fatalf("user open: %v", err)
		}
	})
}

func TestJoinRequestDeclineCooldownAndApproveResume(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	owner, stranger, now, link, token := seedJoinLink(t, st)
	request := func(at time.Time) platformstore.PendingJoinRequest {
		t.Helper()
		res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{
			TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, IP: "10.0.0.9", Now: at,
			RequestLink: "https://acme.example/workspace#people",
		})
		if err != nil || res.State != platformstore.JoinPending {
			t.Fatalf("request = %+v, %v", res, err)
		}
		rows, err := st.PendingJoinRequests(ctx, 2, at)
		if err != nil || len(rows) != 1 {
			t.Fatalf("pending rows = %+v, %v", rows, err)
		}
		return rows[0]
	}
	first := request(now.Add(time.Minute))
	if err := st.DeclineJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: 2, RequestID: first.ID, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(2 * time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	res, err := st.JoinTeam(ctx, platformstore.JoinDecisionParams{
		TeamID: 2, UserID: stranger, Token: token, Pepper: joinPepper, Now: now.Add(6 * 24 * time.Hour),
	})
	if err != nil || res.State != platformstore.JoinPending || countRows(t, st, `SELECT COUNT(*) FROM team_join_request`) != 1 {
		t.Fatalf("cooldown request = %+v, %v", res, err)
	}
	second := request(now.Add(8 * 24 * time.Hour))
	stageErr := errors.New("team database down")
	_, err = st.ApproveJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: 2, RequestID: second.ID, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(8*24*time.Hour + time.Minute),
		Permissions: []byte(`[{"section":"orders","access":"write"}]`), ApprovedLink: "https://acme.example/",
		Stage: func(context.Context, platformstore.JoinMember) error { return stageErr },
	})
	if !errors.Is(err, stageErr) {
		t.Fatalf("stage failure: %v", err)
	}
	if _, err := st.RotateJoinLink(ctx, platformstore.JoinLinkChange{
		TeamID: 2, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(8*24*time.Hour + 2*time.Minute),
	}); err != nil {
		t.Fatalf("rotate after approval: %v", err)
	}
	if n, err := st.SweepJoining(ctx, now.Add(30*24*time.Hour)); err != nil || n != 0 {
		t.Fatalf("approved reservation swept: %d, %v", n, err)
	}
	var staged platformstore.JoinMember
	approved, err := st.ApproveJoinRequest(ctx, platformstore.JoinRequestDecisionParams{
		TeamID: 2, RequestID: second.ID, Actor: platformstore.Actor{UserID: owner}, Now: now.Add(30*24*time.Hour + time.Minute),
		Permissions: []byte(`[{"section":"orders","access":"write"}]`), Role: "owner", ApprovedLink: "https://acme.example/",
		Stage:  func(_ context.Context, m platformstore.JoinMember) error { staged = m; return nil },
		Enable: func(context.Context, platformstore.JoinMember) (bool, error) { return true, nil },
	})
	if err != nil || approved.State != platformstore.JoinJoined || staged.Gen != link.Gen {
		t.Fatalf("approve retry = %+v staged %+v, %v", approved, staged, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_member WHERE team_id = 2 AND user_id = ? AND status = 'active' AND role = 'owner'`, stranger); n != 1 || staged.Role != "owner" {
		t.Fatalf("active owner membership = %d, staged role %q", n, staged.Role)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_email WHERE template = 'join_approved' AND to_email = 'new@x.io'`); n != 1 {
		t.Fatalf("join approved letters = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_audit WHERE action = 'join_approve'`); n != 1 {
		t.Fatalf("join approve audits = %d", n)
	}
}

func TestJoinSignUpAndConfirmCode(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	_, _, now, _, token := seedJoinLink(t, st)
	email := "code@x.io"
	makeLetter := func(userID int64, team *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
		if team == nil || team.ID != 2 {
			return platformstore.NewToken{}, platformstore.Letter{}, fmt.Errorf("wrong team: %+v", team)
		}
		return platformstore.NewToken{Purpose: "verify_join", TokenHash: hex64('c'), CodeHash: fmt.Sprintf("code-%d", userID),
			ExpiresAt: now.Add(time.Hour)}, platformstore.Letter{Template: "join_code", Link: "https://acme.example/join", Code: "123456", TeamID: 2}, nil
	}
	res, err := st.JoinSignUp(ctx, platformstore.JoinSignUpParams{
		TeamID: 2, Link: token, Pepper: joinPepper, Email: email, PasswordHash: "hash", HandleBase: "code",
		TermsVersion: "2026-11", Now: now, MaxLettersPerHour: 3, PasswordMatches: func(string) bool { return false },
		Letter: makeLetter,
	})
	if err != nil || !res.Created || res.TeamID != 2 {
		t.Fatalf("join signup = %+v, %v", res, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team WHERE created_by = ?`, res.UserID); n != 0 {
		t.Fatalf("join signup created %d teams", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM email_token WHERE user_id = ? AND purpose = 'verify_join' AND team_id IS NULL`, res.UserID); n != 1 {
		t.Fatalf("teamless join tokens = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_email WHERE to_email = ? AND template = 'join_code' AND team_id = 2`, email); n != 1 {
		t.Fatalf("attributed join letters = %d", n)
	}
	// No Purpose: the store names the letter it checks (the service never sets it).
	attempt := platformstore.CodeAttempt{Email: email, Now: now.Add(time.Minute), MaxRowAttempts: 5,
		Match:  func(uid int64, stored string) bool { return uid == res.UserID && stored == fmt.Sprintf("code-%d", uid) },
		Fail:   func(c platformstore.CodeCounters) platformstore.CodeCounters { return c },
		Locked: func(platformstore.CodeCounters) bool { return false },
	}
	// The inbox owner typing the code with another password cannot activate the sign-up.
	if _, err := st.ConfirmJoinCode(ctx, platformstore.ConfirmJoinParams{
		Attempt: attempt, TeamID: 2, Now: now.Add(time.Minute), RequestLink: "https://acme.example/workspace#people",
		PasswordMatches: func(string) bool { return false },
	}); !errors.Is(err, platformstore.ErrWrongPassword) {
		t.Fatalf("confirm with a wrong password: %v", err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE id = ? AND status = 'pending'`, res.UserID); n != 1 {
		t.Fatalf("wrong password activated the account: %d", n)
	}
	confirmed, err := st.ConfirmJoinCode(ctx, platformstore.ConfirmJoinParams{
		Attempt: attempt, TeamID: 2, Now: now.Add(time.Minute), RequestLink: "https://acme.example/workspace#people",
		PasswordMatches: func(stored string) bool { return stored == "hash" },
	})
	if err != nil || confirmed.State != platformstore.JoinPending {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_join_intent WHERE user_id = ?`, res.UserID); n != 0 {
		t.Fatalf("join intents = %d", n)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM team_join_request WHERE user_id = ? AND status = 'pending'`, res.UserID); n != 1 {
		t.Fatalf("pending requests = %d", n)
	}
}
