package platformstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// seedConsole: team #1 (boss owner, helper member), team 2 acme active (ann owner, bob member,
// one live session each), team 3 wait in review, team 4 prov provisioning, op an operator.
func seedConsole(t *testing.T, st *platformstore.Store) (ann, bob, op int64) {
	t.Helper()
	ctx := context.Background()
	if err := st.EnsureTeamOne(ctx, "grbpwr_test", []platformstore.TeamOneAdmin{
		{Username: "boss", PasswordHash: "h", Super: true}, {Username: "helper", PasswordHash: "h"},
	}); err != nil {
		t.Fatal(err)
	}
	db := st.DB()
	for _, q := range []string{
		`INSERT INTO platform_user (id, email, handle, password_hash, status, is_operator) VALUES
 (101, 'ann@x.io', 'ann', 'h', 'active', 0), (102, 'bob@x.io', 'bob', 'h', 'active', 0), (103, 'op@x.io', 'op', 'h', 'active', 1)`,
		`INSERT INTO team (id, slug, name, db_name, status, created_at) VALUES
 (2, 'acme', 'Acme', 'lk_t_acme0001', 'active', '2026-11-02'), (3, 'wait', 'Wait', NULL, 'review', '2026-11-03'),
 (4, 'prov', 'Prov', 'lk_t_prov0001', 'provisioning', '2026-11-04')`,
		`INSERT INTO team_member (team_id, user_id, role, status) VALUES (2, 101, 'owner', 'active'), (2, 102, 'member', 'active'),
 (3, 101, 'owner', 'active')`,
		`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES
 ('sann0000000000000000000000000000', 101, 2, 'team'), ('sbob0000000000000000000000000000', 102, 2, 'team'),
 ('sbob1000000000000000000000000000', 102, NULL, 'console')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return 101, 102, 103
}

func TestConsoleTeamsAndStatus(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)

	teams, err := st.ConsoleTeams(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	people := map[string]int{}
	for _, tm := range teams {
		slugs = append(slugs, tm.Slug)
		people[tm.Slug] = tm.People
	}
	if strings.Join(slugs, ",") != "grbpwr,prov,wait,acme" {
		t.Fatalf("order %v (team #1 first, then newest)", slugs)
	}
	if people["grbpwr"] != 2 || people["acme"] != 2 {
		t.Fatalf("people %v", people)
	}
	if got, err := st.ConsoleTeams(ctx, "ac"); err != nil || len(got) != 1 || got[0].Slug != "acme" {
		t.Fatalf("query: %v %v", got, err)
	}
	if got, err := st.ConsoleTeams(ctx, "%"); err != nil || len(got) != 0 {
		t.Fatalf("a %% query matches literally: %v %v", got, err)
	}

	if err := st.SetTeamReadOnly(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTeamReadOnly(ctx, 2, true); err != nil {
		t.Fatalf("idempotent pause: %v", err)
	}
	if tm, err := st.ConsoleTeam(ctx, "acme"); err != nil || tm.Status != "suspended" {
		t.Fatalf("after pause %+v %v", tm, err)
	}
	if err := st.SetTeamReadOnly(ctx, 2, false); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{3, 4} {
		if err := st.SetTeamReadOnly(ctx, id, true); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
			t.Fatalf("team %d: %v", id, err)
		}
	}
	if _, err := st.ConsoleTeam(ctx, "nope"); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("unknown slug: %v", err)
	}

	now := time.Now().UTC()
	if err := st.RenameTeam(ctx, 2, "acme-two", "Acme Two", now); err != nil {
		t.Fatal(err)
	}
	if err := st.RenameTeam(ctx, 4, "acme", "", now); !errors.Is(err, platformstore.ErrSlugTaken) {
		t.Fatalf("a retired slug is never reused: %v", err)
	}
	if err := st.RenameTeam(ctx, 4, "acme-two", "", now); !errors.Is(err, platformstore.ErrSlugTaken) {
		t.Fatalf("a live slug: %v", err)
	}
	if err := st.RenameTeam(ctx, 3, "waited", "", now); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("review team rename: %v", err)
	}
	if tm, err := st.ConsoleTeam(ctx, "acme-two"); err != nil || tm.Name != "Acme Two" || tm.ID != 2 {
		t.Fatalf("renamed %+v %v", tm, err)
	}
}

func TestApproveDecline(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	if err := st.ApproveTeam(ctx, 2, "https://acme.x/", time.Now()); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("approve an active team: %v", err)
	}
	if err := st.DeclineTeam(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ConsoleTeam(ctx, "wait"); !errors.Is(err, platformstore.ErrTeamNotFound) {
		t.Fatalf("declined team still there: %v", err)
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM team_member WHERE team_id = 3`); err != nil || n != 0 {
		t.Fatalf("declined team members %d %v", n, err)
	}
}

func TestMembersLastOwnerAndSessions(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, _ := seedConsole(t, st)
	now := time.Now().UTC()

	if _, err := st.RemoveMember(ctx, 2, ann, now); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("remove the last owner: %v", err)
	}
	if _, err := st.SetMemberRole(ctx, 2, ann, "member", now); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("demote the last owner: %v", err)
	}
	ch, err := st.SetMemberRole(ctx, 2, bob, "owner", now)
	if err != nil || ch.Handle != "bob" || ch.WasOwner || len(ch.RevokedSessions) != 1 || ch.RevokedSessions[0] != "sbob0000000000000000000000000000" {
		t.Fatalf("promote: %+v %v", ch, err)
	}
	if ch, err := st.SetMemberRole(ctx, 2, bob, "owner", now); err != nil || len(ch.RevokedSessions) != 0 {
		t.Fatalf("same role is a no-op: %+v %v", ch, err)
	}
	if _, err := st.SetMemberRole(ctx, 2, ann, "member", now); err != nil {
		t.Fatalf("demote with another owner left: %v", err)
	}
	if _, err := st.RemoveMember(ctx, 2, bob, now); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("bob is now the last owner: %v", err)
	}
	// ann's team session already died with her demotion: nothing left to revoke.
	ch, err = st.RemoveMember(ctx, 2, ann, now)
	if err != nil || ch.Handle != "ann" || ch.WasOwner || len(ch.RevokedSessions) != 0 {
		t.Fatalf("remove ann: %+v %v", ch, err)
	}
	if _, ok, _ := st.TeamSession(ctx, "sann0000000000000000000000000000", ann, 2); ok {
		t.Fatal("removed member's session still live")
	}
	if _, err := st.RemoveMember(ctx, 2, ann, now); !errors.Is(err, platformstore.ErrNotMember) {
		t.Fatalf("remove twice: %v", err)
	}
	// The console session of bob is not a team session: untouched.
	var live int
	if err := st.DB().Get(&live, `SELECT COUNT(*) FROM user_session WHERE sid = 'sbob1000000000000000000000000000' AND revoked_at IS NULL`); err != nil || live != 1 {
		t.Fatalf("console session revoked: %d %v", live, err)
	}
	members, err := st.TeamMembers(ctx, 2)
	if err != nil || len(members) != 1 || members[0].Handle != "bob" || members[0].Role != "owner" {
		t.Fatalf("members %+v %v", members, err)
	}
}

func TestInvites(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := func(email, hash string) platformstore.InviteParams {
		return platformstore.InviteParams{TeamID: 2, Email: email, Role: "member", Permissions: []byte(`[{"section":"products","access":"write"}]`),
			TokenHash: hash, ExpiresAt: now.Add(7 * 24 * time.Hour), InvitedBy: ann, Now: now, MaxPerHour: 3,
			Letter: platformstore.Letter{To: email, Template: "invite", Link: "https://acme.x/join#token=" + hash, TeamName: "Acme", Slug: "acme", From: "ann"}}
	}
	h := func(c string) string { return strings.Repeat(c, 64) }
	inv, err := st.UpsertInvite(ctx, p("new@x.io", h("a")))
	if err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := st.DB().Get(&payload, `SELECT payload FROM platform_email WHERE to_email = 'new@x.io' AND template = 'invite'`); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, "/join#token=") || strings.Contains(payload, `"code"`) {
		t.Fatalf("invite letter: %s", payload)
	}
	// Re-inviting the same email re-uses the invite: one row, the old link dead.
	again := p("new@x.io", h("b"))
	again.Role = "owner"
	inv2, err := st.UpsertInvite(ctx, again)
	if err != nil || inv2.ID != inv.ID {
		t.Fatalf("re-invite %+v %v", inv2, err)
	}
	if err := st.ResendInvite(ctx, platformstore.ResendParams{TeamID: 2, InviteID: inv.ID, TokenHash: h("c"), ExpiresAt: now.Add(time.Hour), Now: now, MaxPerHour: 3,
		Letter: func(platformstore.TeamInvite) platformstore.Letter {
			return platformstore.Letter{Template: "invite", Link: "https://acme.x/join#token=c"}
		}}); err != nil {
		t.Fatal(err)
	}
	var hashes []string
	if err := st.DB().Select(&hashes, `SELECT token_hash FROM team_invite WHERE team_id = 2`); err != nil || len(hashes) != 1 || hashes[0] != h("c") {
		t.Fatalf("after resend %v %v (old tokens must be gone)", hashes, err)
	}
	// Three letters to new@x.io this hour: the fourth is refused.
	if _, err := st.UpsertInvite(ctx, p("new@x.io", h("d"))); !errors.Is(err, platformstore.ErrLetterCap) {
		t.Fatalf("letter cap: %v", err)
	}
	if _, err := st.UpsertInvite(ctx, p("bob@x.io", h("e"))); !errors.Is(err, platformstore.ErrAlreadyMember) {
		t.Fatalf("active member: %v", err)
	}
	if _, err := st.UpsertInvite(ctx, platformstore.InviteParams{TeamID: 3, Email: "z@x.io", Role: "member", Permissions: []byte(`[]`),
		TokenHash: h("f"), ExpiresAt: now, InvitedBy: ann, Now: now, Letter: platformstore.Letter{To: "z@x.io", Template: "invite"}}); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("invite into a review team: %v", err)
	}
	open, err := st.OpenInvites(ctx, 2)
	if err != nil || len(open) != 1 || open[0].Role != "owner" {
		t.Fatalf("open invites %+v %v", open, err)
	}
	if err := st.RevokeInvite(ctx, 2, inv.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeInvite(ctx, 2, inv.ID, now); !errors.Is(err, platformstore.ErrInviteNotFound) {
		t.Fatalf("revoke twice: %v", err)
	}
	if open, err := st.OpenInvites(ctx, 2); err != nil || len(open) != 0 {
		t.Fatalf("after revoke %+v %v", open, err)
	}
}

func TestOperatorHandoff(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, op := seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	issue := func(uid, team int64, hash string) {
		t.Helper()
		if err := st.IssueHandoff(ctx, uid, platformstore.NewToken{TeamID: team, TokenHash: hash, ExpiresAt: now.Add(10 * time.Minute)}, now); err != nil {
			t.Fatal(err)
		}
	}
	h := func(c string) string { return strings.Repeat(c, 64) }
	issue(op, 2, h("1"))
	res, err := st.ExchangeOperatorHandoff(ctx, h("1"), 2, "sop00000000000000000000000000000", now)
	if err != nil || res.User.ID != op || res.Team.ID != 2 {
		t.Fatalf("operator hand-off %+v %v", res, err)
	}
	if _, err := st.ExchangeOperatorHandoff(ctx, h("1"), 2, "sop10000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("single use: %v", err)
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM team_member WHERE user_id = ?`, op); err != nil || n != 0 {
		t.Fatalf("operator entry made a membership: %d %v", n, err)
	}
	issue(ann, 2, h("2"))
	if _, err := st.ExchangeOperatorHandoff(ctx, h("2"), 2, "sop20000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("non-operator: %v", err)
	}
	issue(op, 4, h("3"))
	if _, err := st.ExchangeOperatorHandoff(ctx, h("3"), 4, "sop30000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTeamNotReady) {
		t.Fatalf("provisioning team: %v", err)
	}
	issue(op, 2, h("4"))
	if _, err := st.ExchangeOperatorHandoff(ctx, h("4"), 4, "sop40000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("code for another team: %v", err)
	}
}
