package platformauth

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/avatar"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// The account RPCs of P0 (profile, @username, avatar, first entry) and the name split of the
// sign-up and join sheets.

const (
	profAnnaTeamSID  = "profannateam00000000000000000000"
	profAnnaRootSID  = "profannaroot00000000000000000000"
	profBorisTeamSID = "profboristeam0000000000000000000"
	profPassword     = "prof-anna-password-1"
)

func seedProfiles(t *testing.T, f *flow) {
	t.Helper()
	hash, err := f.svc.d.Hasher.HashPassword(profPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO platform_user (id, email, handle, password_hash, status) VALUES
 (301, 'prof-anna@x.test', 'prof-anna', ?, 'active'), (302, 'prof-boris@x.test', 'prof-boris', '!x', 'active')`, []any{hash}},
		{`INSERT INTO team (id, slug, name, db_name, status) VALUES
 (30, 'prof-alpha', 'Alpha', 'lk_t_profalpha', 'active'), (31, 'prof-beta', 'Beta', 'lk_t_profbeta', 'active')`, nil},
		{`INSERT INTO team_member (team_id, user_id, role, status, entry_pending) VALUES
 (30, 301, 'member', 'active', 1), (30, 302, 'owner', 'active', 0)`, nil},
		{`INSERT INTO user_session (sid, user_id, team_id, aud) VALUES
 ('` + profAnnaTeamSID + `', 301, 30, 'team'), ('` + profAnnaRootSID + `', 301, NULL, 'root'),
 ('` + profBorisTeamSID + `', 302, 30, 'team')`, nil},
	} {
		if _, err := f.st.DB().Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func profToken(t *testing.T, f *flow, uid int64, handle, sid string, teamID int64, operator bool) string {
	t.Helper()
	aud := jwt.AudTeam
	if teamID == 0 {
		aud = jwt.AudRoot
	}
	tok, err := jwt.NewTeamTokenAt(f.svc.d.JWT, time.Hour, jwt.TeamClaims{
		Handle: handle, UserID: uid, TeamID: teamID, SessionID: sid, Audience: aud, Operator: operator,
	}, f.clock)
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

func wantKey(t *testing.T, err error, c codes.Code, key, what string) {
	t.Helper()
	if status.Code(err) != c || status.Convert(err).Message() != key {
		t.Fatalf("%s: got %v, want %v %s", what, err, c, key)
	}
}

func testAvatarPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 12, 8))
	for x := range 12 {
		for y := range 8 {
			img.Set(x, y, color.RGBA{R: uint8(x * 20), G: uint8(y * 30), B: 90, A: 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestAccountProfileFlow(t *testing.T) {
	f := newFlow(t)
	seedProfiles(t, f)
	alpha := ctxFrom("10.9.0.1", "prof-alpha")
	anna := profToken(t, f, 301, "prof-anna", profAnnaTeamSID, 30, false)
	annaRoot := profToken(t, f, 301, "prof-anna", profAnnaRootSID, 0, false)
	boris := profToken(t, f, 302, "prof-boris", profBorisTeamSID, 30, false)

	// Read: a team token on its own host, a root token anywhere; the @username is the handle.
	gp, err := f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{})
	if err != nil {
		t.Fatal(err)
	}
	p := gp.GetProfile()
	if p.GetPublicUsername() != "prof-anna" || p.GetEmail() != "prof-anna@x.test" || !p.GetPasswordSet() || p.GetAvatarUrl() != "" {
		t.Fatalf("profile = %v", p)
	}
	if _, err := f.svc.GetMyProfile(ctxFrom("10.9.0.1", ""), annaRoot, &auth.GetMyProfileRequest{}); err != nil {
		t.Fatalf("root token: %v", err)
	}
	// The wrong host, no host, an operator's entry token: refused.
	_, err = f.svc.GetMyProfile(ctxFrom("10.9.0.1", "prof-beta"), anna, &auth.GetMyProfileRequest{})
	wantCode(t, err, codes.PermissionDenied, "team token on another team's host")
	_, err = f.svc.GetMyProfile(ctxFrom("10.9.0.1", ""), anna, &auth.GetMyProfileRequest{})
	wantCode(t, err, codes.PermissionDenied, "team token without a host")
	_, err = f.svc.GetMyProfile(alpha, profToken(t, f, 301, "prof-anna", profAnnaTeamSID, 30, true), &auth.GetMyProfileRequest{})
	wantKey(t, err, codes.Unauthenticated, "sign_in_again", "operator entry token")

	// Update: names trimmed, the @username normalised.
	up, err := f.svc.UpdateMyProfile(alpha, anna, &auth.UpdateMyProfileRequest{FirstName: " Anna ", LastName: "Karenina", PublicUsername: "@Anna.K"})
	if err != nil {
		t.Fatal(err)
	}
	if p := up.GetProfile(); p.GetFirstName() != "Anna" || p.GetLastName() != "Karenina" || p.GetPublicUsername() != "anna.k" {
		t.Fatalf("updated profile = %v", p)
	}
	// Boris cannot take anna's @username nor her handle; bad values have their keys.
	for _, name := range []string{"anna.k", "ANNA.K", "prof-anna"} {
		_, err = f.svc.UpdateMyProfile(alpha, boris, &auth.UpdateMyProfileRequest{PublicUsername: name})
		wantKey(t, err, codes.AlreadyExists, "username_taken", "boris takes "+name)
	}
	_, err = f.svc.UpdateMyProfile(alpha, boris, &auth.UpdateMyProfileRequest{PublicUsername: "a"})
	wantKey(t, err, codes.InvalidArgument, "bad_username", "one-letter @username")
	_, err = f.svc.UpdateMyProfile(alpha, boris, &auth.UpdateMyProfileRequest{FirstName: strings.Repeat("б", MaxPersonNameLen+1)})
	wantKey(t, err, codes.InvalidArgument, "name_too_long", "long first name")

	// Sign in with the @username, with or without its at sign; the handle still works.
	for _, login := range []string{"@anna.k", "anna.k", "prof-anna", "@prof-anna"} {
		si, err := f.svc.SignIn(ctxFrom("10.9.1.1", ""), &auth.SignInRequest{Login: login, Password: profPassword, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
		if err != nil || si.GetToken() == "" {
			t.Fatalf("sign in as %q = %v, %v", login, si, err)
		}
	}

	// Avatar: set (its URL is content-addressed), shown in the profile, refused when broken, removed.
	raw := testAvatarPNG(t)
	sa, err := f.svc.SetMyAvatar(alpha, anna, &auth.SetMyAvatarRequest{Avatar: raw})
	if err != nil {
		t.Fatal(err)
	}
	if u := sa.GetAvatarUrl(); !strings.HasPrefix(u, "https://api.beta.lekalo.studio"+avatar.Path) || len(u) != len("https://api.beta.lekalo.studio"+avatar.Path)+64 {
		t.Fatalf("avatar url = %q", u)
	}
	if gp, err := f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{}); err != nil || gp.GetProfile().GetAvatarUrl() != sa.GetAvatarUrl() {
		t.Fatalf("profile avatar = %v, %v", gp, err)
	}
	sha := sa.GetAvatarUrl()[len(sa.GetAvatarUrl())-64:]
	if _, ok, err := f.st.AvatarMetaBySHA(alpha, sha); err != nil || !ok {
		t.Fatalf("stored avatar: %v %v", ok, err)
	}
	_, err = f.svc.SetMyAvatar(alpha, anna, &auth.SetMyAvatarRequest{Avatar: []byte("not a picture")})
	wantKey(t, err, codes.InvalidArgument, "bad_avatar", "broken avatar")
	if sa, err := f.svc.SetMyAvatar(alpha, anna, &auth.SetMyAvatarRequest{}); err != nil || sa.GetAvatarUrl() != "" {
		t.Fatalf("remove avatar = %v, %v", sa, err)
	}
	if _, ok, err := f.st.AvatarMetaBySHA(alpha, sha); err != nil || ok {
		t.Fatalf("removed avatar still served: %v %v", ok, err)
	}

	// First entry: a root token has no team; the team token finishes it, and again is a no-op.
	_, err = f.svc.FinishTeamEntry(ctxFrom("10.9.0.1", ""), annaRoot, &auth.FinishTeamEntryRequest{})
	wantCode(t, err, codes.PermissionDenied, "finish entry with a root token")
	for range 2 {
		if _, err := f.svc.FinishTeamEntry(alpha, anna, &auth.FinishTeamEntryRequest{}); err != nil {
			t.Fatal(err)
		}
		if pending, err := f.st.EntryPending(alpha, 30, 301); err != nil || pending {
			t.Fatalf("entry pending after finish = %v, %v", pending, err)
		}
	}

	// A revoked session reads nothing.
	if _, err := f.st.DB().Exec(`UPDATE user_session SET revoked_at = ? WHERE sid = ?`, f.clock, profAnnaTeamSID); err != nil {
		t.Fatal(err)
	}
	_, err = f.svc.GetMyProfile(alpha, anna, &auth.GetMyProfileRequest{})
	wantCode(t, err, codes.Unauthenticated, "revoked session")
}

func TestNamesFromSheetsAndInvites(t *testing.T) {
	j := newJoinTeam(t, "name-team")
	// The owner's membership came from the verified sign-up: it owes its first entry.
	if pending, err := j.st.EntryPending(ctxFrom("10.9.2.1", ""), j.teamID, j.ownerID); err != nil || !pending {
		t.Fatalf("owner entry pending = %v, %v", pending, err)
	}
	names := func(email string) (first, last string) {
		t.Helper()
		var r struct {
			First string `db:"first_name"`
			Last  string `db:"last_name"`
		}
		if err := j.st.DB().Get(&r, `SELECT first_name, last_name FROM platform_user WHERE email = ?`, email); err != nil {
			t.Fatal(err)
		}
		return r.First, r.Last
	}

	// Sign-up: the one-box name splits at its first space; a control character is bad_name.
	sctx := ctxFrom("10.9.2.2", "")
	const ola = "delivered+b3name-ola@resend.dev"
	_, err := j.svc.SignUp(sctx, &auth.SignUpRequest{Email: ola, Password: "ola-password-1", TeamName: "Ola", TeamSlug: "ola-team", TermsAccepted: true, Name: "Ola\x01"})
	wantKey(t, err, codes.InvalidArgument, "bad_name", "sign-up with a control character")
	if _, err := j.svc.SignUp(sctx, &auth.SignUpRequest{Email: ola, Password: "ola-password-1", TeamName: "Ola", TeamSlug: "ola-team", TermsAccepted: true, Name: "  Ola   Nordmann Jr "}); err != nil {
		t.Fatal(err)
	}
	if first, last := names(ola); first != "Ola" || last != "Nordmann Jr" {
		t.Fatalf("sign-up names = %q %q", first, last)
	}

	// Join: the inviter's name shows on the sheet and seeds the new account when the sheet has none.
	invite := func(email, name string) string {
		t.Helper()
		tok, id := j.invite(email, writeProducts)
		if _, err := j.st.DB().Exec(`UPDATE team_invite SET name = ? WHERE id = ?`, name, id); err != nil {
			t.Fatal(err)
		}
		return tok
	}
	const kira = "delivered+b3name-kira@resend.dev"
	ktok := invite(kira, "Kira Lee")
	gi, err := j.svc.GetInvite(ctxFrom("10.9.2.3", ""), &auth.GetInviteRequest{Token: ktok})
	if err != nil || gi.GetName() != "Kira Lee" {
		t.Fatalf("get invite = %v, %v", gi, err)
	}
	if _, err := j.svc.AcceptInvite(ctxFrom("10.9.2.3", ""), &auth.AcceptInviteRequest{Token: ktok, Password: "kira-password-1", Handle: "kira", TermsAccepted: true}); err != nil {
		t.Fatal(err)
	}
	if first, last := names(kira); first != "Kira" || last != "Lee" {
		t.Fatalf("invite-seeded names = %q %q", first, last)
	}
	var kiraID int64
	if err := j.st.DB().Get(&kiraID, `SELECT id FROM platform_user WHERE email = ?`, kira); err != nil {
		t.Fatal(err)
	}
	if pending, err := j.st.EntryPending(ctxFrom("10.9.2.3", ""), j.teamID, kiraID); err != nil || !pending {
		t.Fatalf("new member entry pending = %v, %v", pending, err)
	}
	// The name typed on the sheet wins over the inviter's; a bad one refuses the join.
	const mona = "delivered+b3name-mona@resend.dev"
	mtok := invite(mona, "M")
	_, err = j.svc.AcceptInvite(ctxFrom("10.9.2.4", ""), &auth.AcceptInviteRequest{Token: mtok, Password: "mona-password-1", Handle: "mona", TermsAccepted: true, Name: strings.Repeat("m", 81)})
	wantKey(t, err, codes.InvalidArgument, "bad_name", "join with an 81-character name")
	if _, err := j.svc.AcceptInvite(ctxFrom("10.9.2.4", ""), &auth.AcceptInviteRequest{Token: mtok, Password: "mona-password-1", Handle: "mona", TermsAccepted: true, Name: "Mona  Lisa Smith"}); err != nil {
		t.Fatal(err)
	}
	if first, last := names(mona); first != "Mona" || last != "Lisa Smith" {
		t.Fatalf("sheet names = %q %q", first, last)
	}
}
