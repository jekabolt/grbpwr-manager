package platformstore_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// X6: an invite shows «bounced» from its OWN letter only; a re-sent invite starts clean.
func TestInviteBouncedOwnLetter(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	activeTeam3(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const to = "typo@x.test"
	invite := func(team int64, h byte) platformstore.TeamInvite {
		t.Helper()
		inv, err := st.UpsertInvite(ctx, platformstore.InviteParams{
			TeamID: team, Email: to, Role: "member", Permissions: []byte(`[]`), TokenHash: hex64(h),
			ExpiresAt: now.Add(time.Hour), Actor: platformstore.Actor{UserID: 101}, Now: now, MaxPerHour: 10,
			Letter: platformstore.Letter{To: to, Template: "invite"},
		})
		if err != nil {
			t.Fatal(err)
		}
		return inv
	}
	bounced := func(team int64) bool {
		t.Helper()
		inv, err := st.OpenInvites(ctx, team)
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range inv {
			if i.Email == to {
				return i.Bounced
			}
		}
		t.Fatal("invite not listed")
		return false
	}
	a := invite(2, 'b')
	invite(3, 'c')
	if bounced(2) || bounced(3) {
		t.Fatal("fresh invite bounced")
	}
	// Team 2's letter bounces: team 2 sees it, team 3 (same address) does not.
	if _, err := st.DB().Exec(`UPDATE platform_email SET status = 'sent', delivery = 'bounced' WHERE invite_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if !bounced(2) {
		t.Fatal("bounced letter not shown")
	}
	if bounced(3) {
		t.Fatal("team 2's bounce marks team 3's invite")
	}
	// A soft bounce is not «bounced».
	if _, err := st.DB().Exec(`UPDATE platform_email SET delivery = 'soft_bounce' WHERE invite_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	if bounced(2) {
		t.Fatal("soft bounce shown as bounced")
	}
	// A re-send queues a new letter; the invite is judged by letters since its (new) creation.
	if _, err := st.DB().Exec(`UPDATE platform_email SET delivery = 'bounced' WHERE invite_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}
	later := now.Add(time.Minute)
	if err := st.ResendInvite(ctx, platformstore.ResendParams{TeamID: 2, InviteID: a.ID, Actor: platformstore.Actor{UserID: 101},
		TokenHash: hex64('d'), ExpiresAt: later.Add(time.Hour), Now: later, MaxPerHour: 10,
		Letter: func(platformstore.TeamInvite) platformstore.Letter { return platformstore.Letter{Template: "invite"} }}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`UPDATE platform_email SET created_at = ? WHERE invite_id = ? AND delivery = 'bounced'`, now.Add(-time.Minute), a.ID); err != nil {
		t.Fatal(err)
	}
	if bounced(2) {
		t.Fatal("old bounce leaks into a re-sent invite")
	}
	// The re-sent letter held back by the block list shows «bounced» again.
	if _, err := st.DB().Exec(`UPDATE platform_email SET status = 'failed', last_error = 'suppressed: bounce'
WHERE invite_id = ? AND delivery IS NULL`, a.ID); err != nil {
		t.Fatal(err)
	}
	if !bounced(2) {
		t.Fatal("suppressed letter not shown")
	}
}

// X6: the breaker counts confirm letters only, and only permanent bounces and complaints.
func TestSignupLetterStats(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, q := range []string{
		`INSERT INTO platform_email (to_email, template, payload, status, created_at, delivery) VALUES
 ('a@x.test', 'verify', '{}', 'sent', ?, 'delivered'),
 ('b@x.test', 'verify', '{}', 'sent', ?, 'bounced'),
 ('c@x.test', 'verify', '{}', 'sent', ?, 'complained'),
 ('d@x.test', 'verify', '{}', 'sent', ?, 'soft_bounce'),
 ('e@x.test', 'verify', '{}', 'sent', ?, 'failed'),
 ('f@x.test', 'invite', '{}', 'sent', ?, 'bounced'),
 ('g@x.test', 'verify', '{}', 'sent', ?, 'bounced')`,
	} {
		day := now.Add(-2 * time.Hour)
		if _, err := st.DB().Exec(q, now, day, day, day, day, now, now.Add(-25*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	s, err := st.SignupLetterStats(ctx, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if s.VerifyLastHour != 1 || s.Sent24h != 5 || s.Bounced24h != 2 {
		t.Fatalf("stats = %+v, want hour 1, sent 5, bounced 2", s)
	}
}

// X6: one team's invite letters are capped per hour and per day (invites and re-sends together).
func TestTeamLetterCap(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	activeTeam3(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	capOf := platformstore.TeamLetterCap{PerHour: 3, PerDay: 4}
	invite := func(team int64, i int) error {
		to := fmt.Sprintf("p%d-%d@x.test", team, i)
		_, err := st.UpsertInvite(ctx, platformstore.InviteParams{
			TeamID: team, Email: to, Role: "member", Permissions: []byte(`[]`), TokenHash: hex64(byte('a' + int(team)*8 + i)),
			ExpiresAt: now.Add(time.Hour), Actor: platformstore.Actor{UserID: 101}, Now: now, MaxPerHour: 10, TeamCap: capOf,
			Letter: platformstore.Letter{To: to, Template: "invite"},
		})
		return err
	}
	for i := range 3 {
		if err := invite(2, i); err != nil {
			t.Fatalf("invite %d: %v", i, err)
		}
	}
	if err := invite(2, 3); !errors.Is(err, platformstore.ErrTeamLetterCap) {
		t.Fatalf("hourly cap: %v", err)
	}
	// Another team has its own budget.
	if err := invite(3, 0); err != nil {
		t.Fatalf("other team: %v", err)
	}
	// The hour passes, the day still holds: one more, then the daily cap.
	if _, err := st.DB().Exec(`UPDATE platform_email SET created_at = ? WHERE team_id = 2`, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := invite(2, 4); err != nil {
		t.Fatalf("after the hour: %v", err)
	}
	if err := invite(2, 5); !errors.Is(err, platformstore.ErrTeamLetterCap) {
		t.Fatalf("daily cap: %v", err)
	}
}

// X6: mail-unblock removes the address whatever its case; a second call finds nothing.
func TestUnblockMailAddress(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO platform_mail_suppression (email, reason, first_at, last_at, events)
VALUES ('anna@x.test', 'bounce', NOW(), NOW(), 1)`); err != nil {
		t.Fatal(err)
	}
	if ok, err := st.UnblockMailAddress(ctx, " Anna@X.test "); err != nil || !ok {
		t.Fatalf("unblock: %v %v", ok, err)
	}
	if ok, err := st.UnblockMailAddress(ctx, "anna@x.test"); err != nil || ok {
		t.Fatalf("second unblock: %v %v", ok, err)
	}
}

// activeTeam3 makes the seeded team 3 (in review) a working team, so two teams can invite.
func activeTeam3(t *testing.T, st *platformstore.Store) {
	t.Helper()
	if _, err := st.DB().Exec(`UPDATE team SET status = 'active', db_name = 'lk_t_wait0001' WHERE id = 3`); err != nil {
		t.Fatal(err)
	}
}
