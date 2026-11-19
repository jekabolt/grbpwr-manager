package platformstore_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
)

// The profile, the @username namespace, the avatar and the first-entry flag in the store (P0 §3, §4).

const avatarSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestProfileUsernameNamespace(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (201, 'anna@x.io', 'anna', 'h', 'active'), (202, 'boris@x.io', 'boris', 'h', 'active'), (203, 'pen@x.io', 'pen', 'h', 'pending')`); err != nil {
		t.Fatal(err)
	}
	upd := func(uid int64, first, last, name string) error {
		return st.UpdateProfile(ctx, platformstore.UpdateProfileParams{UserID: uid, FirstName: first, LastName: last, PublicUsername: name})
	}

	// Another person's handle, in any case, is taken.
	for _, name := range []string{"anna", "ANNA"} {
		if err := upd(202, "", "", name); !errors.Is(err, platformstore.ErrUsernameTaken) {
			t.Fatalf("boris takes %q: %v, want ErrUsernameTaken", name, err)
		}
	}
	if err := upd(202, "Boris", "Pasternak", "zed"); err != nil {
		t.Fatal(err)
	}
	// Another person's @username is taken too.
	if err := upd(201, "", "", "zed"); !errors.Is(err, platformstore.ErrUsernameTaken) {
		t.Fatalf("anna takes boris's @username: %v", err)
	}
	p, err := st.Profile(ctx, 202)
	if err != nil || p.FirstName != "Boris" || p.LastName != "Pasternak" || p.PublicUsername != "zed" || p.Handle != "boris" {
		t.Fatalf("profile of boris = %+v, %v", p, err)
	}
	if u, err := st.UserByPublicUsername(ctx, "zed"); err != nil || u.ID != 202 {
		t.Fatalf("user by @zed = %v, %v", u.ID, err)
	}
	if _, err := st.UserByPublicUsername(ctx, "ZED"); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("user by @ZED = %v, want an exact match only", err)
	}
	// An empty @username keeps the current one; names change.
	if err := upd(202, "B.", "", ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := st.Profile(ctx, 202); p.PublicUsername != "zed" || p.FirstName != "B." || p.LastName != "" {
		t.Fatalf("names-only update = %+v", p)
	}
	// Back to the own handle: stored as NULL, the effective @username is the handle. The names are
	// the form's whole state: sent again, they stay.
	if err := upd(202, "B.", "", "boris"); err != nil {
		t.Fatal(err)
	}
	if countRows(t, st, `SELECT COUNT(*) FROM platform_user WHERE id = 202 AND public_username IS NULL`) != 1 {
		t.Fatal("the own handle was stored as an @username")
	}
	if p, _ := st.Profile(ctx, 202); p.PublicUsername != "boris" {
		t.Fatalf("effective @username = %q, want the handle", p.PublicUsername)
	}
	// A freed @username is free at once.
	if err := upd(201, "", "", "zed"); err != nil {
		t.Fatalf("anna takes the freed @zed: %v", err)
	}
	// A pending account has no profile.
	if _, err := st.Profile(ctx, 203); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("profile of a pending account = %v", err)
	}
	if err := upd(203, "", "", "pen2"); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("update of a pending account = %v", err)
	}

	// The handle allocator skips a taken @username: a sign-up whose email makes "zed" gets "zed-…".
	now := time.Now().UTC().Truncate(time.Second)
	res, err := st.SignUp(ctx, platformstore.SignUpParams{
		Email: "zed@x.io", PasswordHash: "h", TeamName: "Zed", Slug: "zed-team", HandleBase: "zed",
		Now: now, PendingTTL: time.Hour, MaxLettersPerHour: 3,
		PasswordMatches: func(string) bool { return false },
		Letter: func(int64, *platformstore.TeamBrief) (platformstore.NewToken, platformstore.Letter, error) {
			return platformstore.NewToken{Purpose: "verify", TokenHash: "t-zed", CodeHash: "c-zed", ExpiresAt: now.Add(time.Hour)},
				platformstore.Letter{Template: "verify"}, nil
		},
		TermsVersion: "2026-11", FirstName: "Zed", LastName: "Zedson",
	})
	if err != nil || !res.Created {
		t.Fatalf("sign-up = %+v, %v", res, err)
	}
	var row struct {
		Handle string `db:"handle"`
		First  string `db:"first_name"`
		Last   string `db:"last_name"`
	}
	if err := st.DB().Get(&row, `SELECT handle, first_name, last_name FROM platform_user WHERE email = 'zed@x.io'`); err != nil {
		t.Fatal(err)
	}
	if row.Handle == "zed" || !strings.HasPrefix(row.Handle, "zed-") || row.First != "Zed" || row.Last != "Zedson" {
		t.Fatalf("new account = %+v, want a zed-… handle and the typed names", row)
	}

	// The directory: exact handles only, the effective @username, the avatar.
	people, err := st.PeopleByHandles(ctx, []string{"anna", "boris", "ANNA", "nobody", ""})
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 || people["anna"].PublicUsername != "zed" || people["boris"].PublicUsername != "boris" ||
		people["boris"].FirstName != "B." {
		t.Fatalf("people = %+v", people)
	}
}

func TestAvatarAndFirstEntry(t *testing.T) {
	st := openFresh(t)
	ctx := context.Background()
	if _, err := st.DB().Exec(`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (101, 'a@x.io', 'anna', 'h', 'active'), (102, 'dis@x.io', 'dis', 'h', 'disabled')`); err != nil {
		t.Fatal(err)
	}

	// Avatar: set, read by its sha, shown in the profile and the directory, removed.
	png := []byte("\x89PNG-avatar")
	if err := st.SetAvatar(ctx, 101, &platformstore.Avatar{PNG: png, SHA256: avatarSHA}); err != nil {
		t.Fatal(err)
	}
	m, ok, err := st.AvatarMetaBySHA(ctx, avatarSHA)
	if err != nil || !ok || m.Size != int64(len(png)) || m.UpdatedAt.IsZero() {
		t.Fatalf("avatar meta = %+v %v %v", m, ok, err)
	}
	got, ok, err := st.AvatarPNGBySHA(ctx, avatarSHA)
	if err != nil || !ok || string(got) != string(png) {
		t.Fatalf("avatar png = %q %v %v", got, ok, err)
	}
	if p, err := st.Profile(ctx, 101); err != nil || p.AvatarSHA != avatarSHA {
		t.Fatalf("profile avatar = %q, %v", p.AvatarSHA, err)
	}
	if people, err := st.PeopleByHandles(ctx, []string{"anna"}); err != nil || people["anna"].AvatarSHA != avatarSHA {
		t.Fatalf("directory avatar = %+v, %v", people, err)
	}
	if err := st.SetAvatar(ctx, 102, &platformstore.Avatar{PNG: png, SHA256: avatarSHA}); !errors.Is(err, platformstore.ErrUserNotFound) {
		t.Fatalf("avatar of a disabled account = %v", err)
	}
	if err := st.SetAvatar(ctx, 101, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := st.AvatarMetaBySHA(ctx, avatarSHA); err != nil || ok {
		t.Fatalf("removed avatar still found: %v %v", ok, err)
	}

	// First entry: a team created now owes it; finishing clears it, once and for good.
	seedCreateTeamSessions(t, st)
	now := time.Now().UTC().Truncate(time.Second)
	created, err := st.CreateTeam(ctx, newTeam(101, "acme", now))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := st.EntryPending(ctx, created.TeamID, 101); err != nil || !pending {
		t.Fatalf("entry pending after create = %v, %v", pending, err)
	}
	for range 2 {
		if err := st.FinishTeamEntry(ctx, created.TeamID, 101); err != nil {
			t.Fatal(err)
		}
		if pending, err := st.EntryPending(ctx, created.TeamID, 101); err != nil || pending {
			t.Fatalf("entry pending after finish = %v, %v", pending, err)
		}
	}
	// No membership: nothing pending, nothing to finish.
	if pending, err := st.EntryPending(ctx, created.TeamID, 102); err != nil || pending {
		t.Fatalf("entry pending without a membership = %v, %v", pending, err)
	}
	// A membership that existed before (the migration's default) owes nothing.
	if _, err := st.DB().Exec(`INSERT INTO team_member (team_id, user_id, role, status) VALUES (?, 102, 'member', 'active')`, created.TeamID); err != nil {
		t.Fatal(err)
	}
	if pending, err := st.EntryPending(ctx, created.TeamID, 102); err != nil || pending {
		t.Fatalf("entry pending of an old membership = %v, %v", pending, err)
	}
}
