package platformstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// X6: an invite whose letter bounced shows bounced; a re-send clears it; the breaker counts.
func TestInviteBouncedAndSignupStats(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const to = "typo@x.test"
	if _, err := st.UpsertInvite(ctx, platformstore.InviteParams{
		TeamID: 2, Email: to, Role: "member", Permissions: []byte(`[]`), TokenHash: hex64('b'),
		ExpiresAt: now.Add(time.Hour), Actor: platformstore.Actor{UserID: 101}, Now: now, MaxPerHour: 10,
		Letter: platformstore.Letter{To: to, Template: "invite"},
	}); err != nil {
		t.Fatal(err)
	}
	bounced := func() bool {
		t.Helper()
		inv, err := st.OpenInvites(ctx, 2)
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
	if bounced() {
		t.Fatal("fresh invite bounced")
	}
	if _, err := st.DB().Exec(`UPDATE platform_email SET status = 'sent', delivery = 'bounced' WHERE to_email = ?`, to); err != nil {
		t.Fatal(err)
	}
	if !bounced() {
		t.Fatal("bounced letter not shown")
	}
	s, err := st.SignupLetterStats(ctx, time.Now().UTC())
	if err != nil || s.Sent24h != 1 || s.Bounced24h != 1 {
		t.Fatalf("stats = %+v, %v", s, err)
	}
	// A later invite of the same address (re-sent) starts clean: the old letter predates it.
	if _, err := st.DB().Exec(`UPDATE team_invite SET created_at = ? WHERE email = ?`, now.Add(time.Minute), to); err != nil {
		t.Fatal(err)
	}
	if bounced() {
		t.Fatal("old bounce leaks into a re-sent invite")
	}
	// A letter held back by the suppression list counts as bounced too.
	if _, err := st.DB().Exec(`UPDATE platform_email SET status = 'failed', delivery = NULL, last_error = 'suppressed: bounce',
  created_at = ? WHERE to_email = ?`, now.Add(time.Minute), to); err != nil {
		t.Fatal(err)
	}
	if !bounced() {
		t.Fatal("suppressed letter not shown")
	}
}
