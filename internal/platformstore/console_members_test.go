package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

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
	// Both owners' team-side rows confirmed: only the last-owner rule decides here.
	by := func(actor, target int64) platformstore.MemberChangeParams {
		return platformstore.MemberChangeParams{TeamID: 2, UserID: target, Actor: platformstore.Actor{UserID: actor},
			MirrorOwners: []int64{ann, bob}, Now: now}
	}

	if _, err := st.RemoveMember(ctx, by(ann, ann)); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("remove the last owner: %v", err)
	}
	if _, err := st.SetMemberRole(ctx, by(ann, ann), "member"); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("demote the last owner: %v", err)
	}
	ch, err := st.SetMemberRole(ctx, by(ann, bob), "owner")
	if err != nil || ch.Handle != "bob" || ch.WasOwner || len(ch.RevokedSessions) != 1 || ch.RevokedSessions[0] != "sbob0000000000000000000000000000" {
		t.Fatalf("promote: %+v %v", ch, err)
	}
	if ch, err := st.SetMemberRole(ctx, by(ann, bob), "owner"); err != nil || len(ch.RevokedSessions) != 0 {
		t.Fatalf("same role is a no-op: %+v %v", ch, err)
	}
	if _, err := st.SetMemberRole(ctx, by(ann, ann), "member"); err != nil {
		t.Fatalf("demote with another owner left: %v", err)
	}
	if _, err := st.RemoveMember(ctx, by(bob, bob)); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("bob is now the last owner: %v", err)
	}
	// ann's team session already died with her demotion: nothing left to revoke.
	ch, err = st.RemoveMember(ctx, by(bob, ann))
	if err != nil || ch.Handle != "ann" || ch.WasOwner || len(ch.RevokedSessions) != 0 {
		t.Fatalf("remove ann: %+v %v", ch, err)
	}
	if _, ok, _ := st.TeamSession(ctx, "sann0000000000000000000000000000", ann, 2); ok {
		t.Fatal("removed member's session still live")
	}
	if _, err := st.RemoveMember(ctx, by(bob, ann)); !errors.Is(err, platformstore.ErrNotMember) {
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

// TestPeopleChangesRecheckActor: every people change re-checks, inside its own transaction, that the
// actor is still an active owner (or an active operator). An owner demoted a moment ago cannot
// finish a change the edge let through on their old token.
func TestPeopleChangesRecheckActor(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, op := seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	both := []int64{ann, bob}
	by := func(a platformstore.Actor, target int64) platformstore.MemberChangeParams {
		return platformstore.MemberChangeParams{TeamID: 2, UserID: target, Actor: a, MirrorOwners: both, Now: now}
	}
	annA, bobA := platformstore.Actor{UserID: ann}, platformstore.Actor{UserID: bob}
	inviteBy := func(a platformstore.Actor, email string) error {
		_, err := st.UpsertInvite(ctx, platformstore.InviteParams{TeamID: 2, Email: email, Role: "member", Permissions: []byte(`[]`),
			TokenHash: strings.Repeat(email[:1], 64), ExpiresAt: now.Add(time.Hour), Actor: a, Now: now, MaxPerHour: 3,
			Letter: platformstore.Letter{To: email, Template: "invite"}})
		return err
	}
	notOwner := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, platformstore.ErrNotOwner) {
			t.Fatalf("%s: %v, want ErrNotOwner", what, err)
		}
	}

	// A plain member is refused by the store whatever the handler believed.
	notOwner("member invites", inviteBy(bobA, "m@x.io"))
	_, err := st.RemoveMember(ctx, by(bobA, ann))
	notOwner("member removes", err)
	_, err = st.SetMemberRole(ctx, by(bobA, bob), "owner")
	notOwner("member promotes self", err)
	// An actor claiming operator who is not one is refused, owner or not: the claim is checked, not
	// silently downgraded.
	_, err = st.SetMemberRole(ctx, by(platformstore.Actor{UserID: ann, Operator: true}, bob), "owner")
	notOwner("owner with a false operator claim", err)
	notOwner("fake operator", inviteBy(platformstore.Actor{UserID: bob, Operator: true}, "f@x.io"))
	if _, err := st.SetMemberRole(ctx, by(annA, bob), "owner"); err != nil {
		t.Fatal(err)
	}

	// bob (now owner) demotes ann; ann's old token still says owner: the store refuses her.
	if _, err := st.SetMemberRole(ctx, by(bobA, ann), "member"); err != nil {
		t.Fatal(err)
	}
	notOwner("demoted owner invites", inviteBy(annA, "late@x.io"))
	_, err = st.RemoveMember(ctx, by(annA, bob))
	notOwner("demoted owner removes", err)
	_, err = st.SetMemberRole(ctx, by(annA, ann), "owner")
	notOwner("demoted owner re-promotes self", err)
	if err := inviteBy(bobA, "ok@x.io"); err != nil {
		t.Fatalf("the owner invites: %v", err)
	}
	open, err := st.OpenInvites(ctx, 2)
	if err != nil || len(open) != 1 {
		t.Fatalf("open invites %+v %v", open, err)
	}
	err = st.ResendInvite(ctx, platformstore.ResendParams{TeamID: 2, InviteID: open[0].ID, Actor: annA, TokenHash: strings.Repeat("r", 64),
		ExpiresAt: now.Add(time.Hour), Now: now, Letter: func(platformstore.TeamInvite) platformstore.Letter { return platformstore.Letter{Template: "invite"} }})
	notOwner("demoted owner resends", err)
	notOwner("demoted owner revokes", st.RevokeInvite(ctx, 2, open[0].ID, annA, now))
	// An entered operator acts as an owner.
	if err := st.RevokeInvite(ctx, 2, open[0].ID, platformstore.Actor{UserID: op, Operator: true}, now); err != nil {
		t.Fatalf("operator revokes: %v", err)
	}
	// A removed owner is refused too.
	if _, err := st.RemoveMember(ctx, by(bobA, ann)); err != nil {
		t.Fatal(err)
	}
	notOwner("removed member invites", inviteBy(annA, "gone@x.io"))
}

// TestLastWorkingOwner: an owner is demoted or removed only while another owner's team-side row is
// confirmed (super, enabled). An owner whose mirror write failed does not count.
func TestLastWorkingOwner(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, _ := seedConsole(t, st)
	now := time.Now().UTC()
	annA := platformstore.Actor{UserID: ann}
	if _, err := st.SetMemberRole(ctx, platformstore.MemberChangeParams{TeamID: 2, UserID: bob, Actor: annA, Now: now}, "owner"); err != nil {
		t.Fatal(err)
	}
	// bob is an owner in the control plane, his team-side row not confirmed: ann stays.
	only := platformstore.MemberChangeParams{TeamID: 2, UserID: ann, Actor: annA, MirrorOwners: []int64{ann}, Now: now}
	if _, err := st.SetMemberRole(ctx, only, "member"); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("demote the last working owner: %v", err)
	}
	if _, err := st.RemoveMember(ctx, only); !errors.Is(err, platformstore.ErrLastOwner) {
		t.Fatalf("remove the last working owner: %v", err)
	}
	confirmed := only
	confirmed.MirrorOwners = []int64{ann, bob}
	if _, err := st.SetMemberRole(ctx, confirmed, "member"); err != nil {
		t.Fatalf("demote with a confirmed owner left: %v", err)
	}
}

// TestTeamSessionNeedsActiveMembership: a team session is created only under the membership row's
// lock while it is active, and a membership coming back through a new invite starts with every
// older session of the person in that team revoked.
func TestTeamSessionNeedsActiveMembership(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, bob, _ := seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	// A password changed since the sign-in read it (generation moved on) opens nothing either.
	if _, err := st.CreateTeamSession(ctx, "sann1000000000000000000000000000", ann, 2, 1, now); !errors.Is(err, platformstore.ErrCredentialsChanged) {
		t.Fatalf("stale credential generation: %v", err)
	}
	if ok, err := st.CreateTeamSession(ctx, "sann1000000000000000000000000000", ann, 2, 0, now); err != nil || !ok {
		t.Fatalf("active member: %v %v", ok, err)
	}
	if _, err := st.RemoveMember(ctx, platformstore.MemberChangeParams{TeamID: 2, UserID: bob, Actor: platformstore.Actor{UserID: ann}, Now: now}); err != nil {
		t.Fatal(err)
	}
	// A sign-in that read the membership before the removal now tries to open its session.
	if ok, err := st.CreateTeamSession(ctx, "sbob2000000000000000000000000000", bob, 2, 0, now); err != nil || ok {
		t.Fatalf("removed member got a session: %v %v", ok, err)
	}
	var n int
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM user_session WHERE sid = 'sbob2000000000000000000000000000'`); err != nil || n != 0 {
		t.Fatalf("session row written: %d %v", n, err)
	}
	// A hand-off issued before the removal is refused too.
	h := func(c string) string { return strings.Repeat(c, 64) }
	if err := st.IssueHandoff(ctx, bob, "sbob1000000000000000000000000000", platformstore.NewToken{TeamID: 2, TokenHash: h("h"), ExpiresAt: now.Add(time.Minute)}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExchangeHandoff(ctx, h("h"), 2, "sbob3000000000000000000000000000", now); !errors.Is(err, platformstore.ErrTokenInvalid) {
		t.Fatalf("hand-off after removal: %v", err)
	}

	// A session left behind from before the removal (written by an older build) …
	if _, err := st.DB().Exec(`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES ('sbobleak000000000000000000000000', 102, 2, 'team')`); err != nil {
		t.Fatal(err)
	}
	// … dies when bob comes back through a new invite.
	if _, err := st.UpsertInvite(ctx, platformstore.InviteParams{TeamID: 2, Email: "bob@x.io", Role: "member", Permissions: []byte(`[]`),
		TokenHash: h("i"), ExpiresAt: now.Add(time.Hour), Actor: platformstore.Actor{UserID: ann}, Now: now, MaxPerHour: 3,
		Letter: platformstore.Letter{To: "bob@x.io", Template: "invite"}}); err != nil {
		t.Fatal(err)
	}
	res, err := st.AcceptInvite(ctx, platformstore.AcceptParams{TokenHash: h("i"), Now: now,
		PasswordMatches: func(string) bool { return true },
		Handoff:         platformstore.NewToken{TokenHash: h("j"), ExpiresAt: now.Add(time.Minute)}})
	if err != nil || res.UserID != bob {
		t.Fatalf("re-join %+v %v", res, err)
	}
	if _, ok, _ := st.TeamSession(ctx, "sbobleak000000000000000000000000", bob, 2); ok {
		t.Fatal("a session from before the removal came back with the new membership")
	}
	if err := st.DB().Get(&n, `SELECT COUNT(*) FROM user_session WHERE sid = 'sbobleak000000000000000000000000' AND revoked_at IS NULL`); err != nil || n != 0 {
		t.Fatalf("old session not revoked: %d %v", n, err)
	}
}

// TestConsoleReadsOnlyListedTeams: the spend read covers only the asked teams.
func TestConsoleReadsOnlyListedTeams(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	day := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []int64{1, 2, 4} {
		if err := st.UpsertAISpend(ctx, id, day, platformstore.AISpend{CostUSD: decimal.NewFromInt(id), Calls: 1}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := st.AISpendFor(ctx, day, []int64{2, 3})
	if err != nil || len(got) != 1 || !got[2].CostUSD.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("spend for teams 2,3: %+v %v", got, err)
	}
	if got, err := st.AISpendFor(ctx, day, nil); err != nil || len(got) != 0 {
		t.Fatalf("no teams: %+v %v", got, err)
	}
	if err := st.SetFeature(ctx, 4, "ai", true, ann); err != nil {
		t.Fatal(err)
	}
	if tm, err := st.ConsoleTeam(ctx, "acme"); err != nil || len(tm.Overrides) != 0 {
		t.Fatalf("acme picked up another team's switch: %+v %v", tm.Overrides, err)
	}
}

func TestInvites(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	ann, _, _ := seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	p := func(email, hash string) platformstore.InviteParams {
		return platformstore.InviteParams{TeamID: 2, Email: email, Role: "member", Permissions: []byte(`[{"section":"products","access":"write"}]`),
			TokenHash: hash, ExpiresAt: now.Add(7 * 24 * time.Hour), Actor: platformstore.Actor{UserID: ann}, Now: now, MaxPerHour: 3,
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
	if err := st.ResendInvite(ctx, platformstore.ResendParams{TeamID: 2, InviteID: inv.ID, Actor: platformstore.Actor{UserID: ann}, TokenHash: h("c"), ExpiresAt: now.Add(time.Hour), Now: now, MaxPerHour: 3,
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
		TokenHash: h("f"), ExpiresAt: now, Actor: platformstore.Actor{UserID: ann}, Now: now, Letter: platformstore.Letter{To: "z@x.io", Template: "invite"}}); !errors.Is(err, platformstore.ErrWrongTeamStatus) {
		t.Fatalf("invite into a review team: %v", err)
	}
	open, err := st.OpenInvites(ctx, 2)
	if err != nil || len(open) != 1 || open[0].Role != "owner" {
		t.Fatalf("open invites %+v %v", open, err)
	}
	if err := st.RevokeInvite(ctx, 2, inv.ID, platformstore.Actor{UserID: ann}, now); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeInvite(ctx, 2, inv.ID, platformstore.Actor{UserID: ann}, now); !errors.Is(err, platformstore.ErrInviteNotFound) {
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
		root := fmt.Sprintf("sroot%027d", uid)
		if _, err := st.DB().Exec(`INSERT IGNORE INTO user_session (sid, user_id, team_id, aud) VALUES (?, ?, NULL, 'console')`, root, uid); err != nil {
			t.Fatal(err)
		}
		if err := st.IssueHandoff(ctx, uid, root, platformstore.NewToken{TeamID: team, TokenHash: hash, ExpiresAt: now.Add(10 * time.Minute)}, now); err != nil {
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
