package platformstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// Identity comparisons stay byte-exact even for legacy non-ASCII rows. This is a MySQL test: it
// also proves migration 0007 makes the two addresses and case-distinct provider subjects storable.
func TestIdentityLookupsAreByteExact(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	seedConsole(t, st)
	now := time.Now().UTC().Truncate(time.Millisecond)
	const ascii, accented = "jose@x.test", "josé@x.test"
	if _, err := st.DB().Exec(`UPDATE platform_user SET email = ? WHERE id = 101`, ascii); err != nil {
		t.Fatal(err)
	}

	if _, err := st.UserByEmail(ctx, accented); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("accented lookup found the ASCII account: %v", err)
	}
	letter := func(hash, typed string) platformstore.MakeLetter {
		return func(int64, *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
			return platformstore.NewToken{Purpose: "reset", TokenHash: hash, ExpiresAt: now.Add(time.Hour)},
				platformstore.Letter{To: typed, Template: "reset"}, nil
		}
	}
	sent, err := st.RequestPasswordReset(ctx, accented, now, 10, letter(hex64('r'), accented))
	if err != nil || sent {
		t.Fatalf("reset for lookalike = sent %v, %v", sent, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_email WHERE to_email = ?`, ascii); n != 0 {
		t.Fatalf("lookalike reset mailed the ASCII account: %d letters", n)
	}

	provider := google("subject-accented", accented, true, true)
	p := providerParams(accented, now, 'p')
	p.HandleBase = "jose-provider"
	res, err := st.CompleteProviderSignIn(ctx, provider, p)
	if err != nil || !res.Created || res.UserID == 101 {
		t.Fatalf("accented provider completion = %+v, %v", res, err)
	}
	if got, err := st.UserByEmail(ctx, accented); err != nil || got.ID != res.UserID {
		t.Fatalf("accented account lookup = %+v, %v", got, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE email IN (?, ?)`, ascii, accented); n != 2 {
		t.Fatalf("distinct email accounts = %d, want 2", n)
	}

	sent, err = st.RequestPasswordReset(ctx, accented, now.Add(time.Minute), 10, letter(hex64('s'), ascii))
	if err != nil || !sent {
		t.Fatalf("reset for stored accented account = sent %v, %v", sent, err)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_email WHERE to_email = ? AND template = 'reset'`, accented); n != 1 {
		t.Fatalf("reset did not use stored address: %d letters", n)
	}

	inv, err := st.UpsertInvite(ctx, platformstore.InviteParams{
		TeamID: 2, Email: accented, Role: "member", Permissions: []byte(`[]`), TokenHash: hex64('i'),
		ExpiresAt: now.Add(time.Hour), Actor: platformstore.Actor{UserID: 101}, Now: now, MaxPerHour: 10,
		Letter: platformstore.Letter{To: accented, Template: "invite"},
	})
	if err != nil || inv.Email != accented {
		t.Fatalf("invite for accented address = %+v, %v", inv, err)
	}
	accepted, err := st.AcceptInvite(ctx, platformstore.AcceptParams{
		TokenHash: hex64('i'), Now: now, PasswordMatches: func(hash string) bool { return hash == "!unusable" },
		Handoff: platformstore.NewToken{TokenHash: hex64('h'), ExpiresAt: now.Add(time.Hour)},
	})
	if err != nil || accepted.UserID != res.UserID || accepted.UserID == 101 {
		t.Fatalf("accented invite matched account %d (ASCII account 101, accented %d): %v", accepted.UserID, res.UserID, err)
	}
}

func TestProviderSubjectsAreByteExact(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	first := providerParams("upper@x.test", now, '1')
	first.HandleBase = "upper-subject"
	a, err := st.CompleteProviderSignIn(ctx, google("AbC", "upper@x.test", true, true), first)
	if err != nil || !a.Created {
		t.Fatalf("subject AbC = %+v, %v", a, err)
	}
	second := providerParams("lower@x.test", now, '2')
	second.HandleBase = "lower-subject"
	b, err := st.CompleteProviderSignIn(ctx, google("abc", "lower@x.test", true, true), second)
	if err != nil || !b.Created || b.UserID == a.UserID {
		t.Fatalf("subject abc = %+v, %v (first user %d)", b, err, a.UserID)
	}
	if n := countRows(t, st, `SELECT COUNT(*) FROM platform_identity WHERE provider = 'google' AND subject IN ('AbC', 'abc')`); n != 2 {
		t.Fatalf("case-distinct identities = %d, want 2", n)
	}
}
