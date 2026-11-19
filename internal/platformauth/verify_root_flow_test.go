package platformauth

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// e1 is a flow with helpers for the entry tests (teams E1): every request from its own address, so
// no per-IP bucket decides an answer.
type e1 struct {
	*flow
	ip int
}

func newE1(t *testing.T, approval bool) *e1 { t.Helper(); return &e1{flow: newFlowWith(t, approval)} }

func (e *e1) ctx() context.Context {
	e.ip++
	return ctxFrom(fmt.Sprintf("10.9.%d.%d", e.ip/250, e.ip%250+1), "")
}

const e1Password = "e1-password-1"

func (e *e1) signUp(email, slug string, brand *pb_admin.TeamBrand) (code, link string) {
	e.t.Helper()
	if _, err := e.svc.SignUp(e.ctx(), &auth.SignUpRequest{Email: email, Password: e1Password, TeamName: slug, TeamSlug: slug,
		TermsAccepted: true, Brand: brand}); err != nil {
		e.t.Fatal(err)
	}
	_, code, link = e.lastLetter(email)
	return code, link
}

func (e *e1) count(q string, args ...any) (n int) {
	e.t.Helper()
	if err := e.st.DB().Get(&n, q, args...); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *e1) userID(email string) (id int64) {
	e.t.Helper()
	if err := e.st.DB().Get(&id, `SELECT id FROM platform_user WHERE email = ?`, email); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// sessions counts every session of the account (root or not, live or not).
func (e *e1) sessions(email string) int {
	return e.count(`SELECT COUNT(*) FROM user_session s JOIN platform_user u ON u.id = s.user_id WHERE u.email = ?`, email)
}

func wantCodeRefusal(t *testing.T, err error, what string) {
	t.Helper()
	if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != "invalid or expired code" {
		t.Fatalf("%s: got %v, want the code refusal", what, err)
	}
}

// tokenLife is exp − iat of a platform token.
func (e *e1) tokenLife(tok string) time.Duration {
	e.t.Helper()
	parsed, err := e.svc.d.JWT.Decode(tok)
	if err != nil {
		e.t.Fatal(err)
	}
	return parsed.Expiration().Sub(parsed.IssuedAt())
}

// TestVerifyEmailSignsIn: a first-time confirmation answers as before and adds the root session the
// password sign-in opens; nothing else mints one.
func TestVerifyEmailSignsIn(t *testing.T) {
	e := newE1(t, false)
	const (
		anna = "delivered+e1anna@resend.dev"
		bob  = "delivered+e1bob@resend.dev"
		dan  = "delivered+e1dan@resend.dev"
		op   = "delivered+b3op@resend.dev" // the flow's operator list
	)

	// By code: the slug and the hand-off as before, and a root token.
	acode, alink := e.signUp(anna, "e1-anna", nil)
	vr, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: acode})
	if err != nil || vr.GetTeamSlug() != "e1-anna" || vr.GetHandoffCode() == "" || vr.GetAwaitingApproval() || vr.GetRootToken() == "" {
		t.Fatalf("verify by code = %v, %v", vr, err)
	}
	annaID := e.userID(anna)
	// The root session of the password sign-in: aud root, no team, no parent, live, the sid in the token.
	claims, err := jwt.VerifyTeamToken(e.svc.d.JWT, vr.GetRootToken(), &jwt.Expectations{Audience: jwt.AudRoot})
	if err != nil || claims.UserID != annaID || claims.TeamID != 0 || claims.Super {
		t.Fatalf("root token claims = %+v, %v", claims, err)
	}
	var rows []struct {
		SID     string         `db:"sid"`
		Aud     string         `db:"aud"`
		Team    sql.NullInt64  `db:"team_id"`
		Parent  sql.NullString `db:"parent_sid"`
		Revoked sql.NullTime   `db:"revoked_at"`
	}
	if err := e.st.DB().Select(&rows, `SELECT sid, aud, team_id, parent_sid, revoked_at FROM user_session WHERE user_id = ?`, annaID); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].SID != claims.SessionID || rows[0].Aud != "root" || rows[0].Team.Valid || rows[0].Parent.Valid || rows[0].Revoked.Valid {
		t.Fatalf("sessions after verify = %+v", rows)
	}
	// The same lifetime as the password sign-in's root token, and the sign-in's audit.
	si, err := e.svc.SignIn(e.ctx(), &auth.SignInRequest{Login: anna, Password: e1Password, Target: auth.SignInTarget_SIGN_IN_TARGET_ROOT})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := e.tokenLife(vr.GetRootToken()), e.tokenLife(si.GetToken()); got != want || got != RootSessionTTL {
		t.Fatalf("root token life = %v, password sign-in %v", got, want)
	}
	if n := e.count(`SELECT COUNT(*) FROM platform_audit WHERE action = 'signin' AND actor_user_id = ?
  AND JSON_UNQUOTE(JSON_EXTRACT(detail, '$.target')) = 'root' AND JSON_UNQUOTE(JSON_EXTRACT(detail, '$.by')) = 'verify'`, annaID); n != 1 {
		t.Fatalf("verify sign-in audits = %d", n)
	}
	// It is a root token: the root list (the team, not ready yet).
	mt, err := e.svc.ListMyTeams(e.ctx(), vr.GetRootToken(), &auth.ListMyTeamsRequest{})
	if err != nil || len(mt.GetTeams()) != 1 || mt.GetTeams()[0].GetSlug() != "e1-anna" || mt.GetTeams()[0].GetReady() {
		t.Fatalf("teams of the verify token = %v, %v", mt, err)
	}
	// The hand-off is as before: one row of the team, bound to no session.
	if n := e.count(`SELECT COUNT(*) FROM email_token e JOIN team t ON t.id = e.team_id
WHERE e.user_id = ? AND e.purpose = 'handoff' AND t.slug = 'e1-anna' AND e.parent_sid IS NULL AND e.used_at IS NULL`, annaID); n != 1 {
		t.Fatalf("hand-offs after verify = %d", n)
	}
	sessionsBefore := e.sessions(anna)

	// (a) Replays: the same code, then the same letter's link — today's refusal, no token, no session.
	r, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: acode})
	wantCodeRefusal(t, err, "code replayed")
	if r.GetRootToken() != "" {
		t.Fatal("a replayed code carried a token")
	}
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: alink})
	wantCodeRefusal(t, err, "link of a used letter")
	// (b) The account is verified now: a code for it finds no live letter.
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: wrong(acode)})
	wantCodeRefusal(t, err, "code for a verified account")
	if n := e.sessions(anna); n != sessionsBefore {
		t.Fatalf("refusals opened %d sessions", n-sessionsBefore)
	}

	// A wrong code counts and mints nothing; the link then confirms (and signs in) once.
	bcode, blink := e.signUp(bob, "e1-bob", nil)
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: bob, Code: wrong(bcode)})
	wantCodeRefusal(t, err, "wrong code")
	if n := e.sessions(bob); n != 0 {
		t.Fatalf("a wrong code opened %d sessions", n)
	}
	if n := e.count(`SELECT code_attempts FROM email_token e JOIN platform_user u ON u.id = e.user_id
WHERE u.email = ? AND e.purpose = 'verify'`, bob); n != 1 {
		t.Fatalf("attempts on bob's letter = %d", n)
	}
	bv, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: blink})
	if err != nil || bv.GetHandoffCode() == "" || bv.GetRootToken() == "" {
		t.Fatalf("verify by link = %v, %v", bv, err)
	}
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: blink})
	wantCodeRefusal(t, err, "link reused")
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: bob, Code: bcode})
	wantCodeRefusal(t, err, "code of a letter confirmed by its link")
	if n := e.sessions(bob); n != 1 {
		t.Fatalf("bob's sessions = %d, want the one root session", n)
	}

	// (c) A disabled account: today's refusal, no session, the team still pending.
	dcode, dlink := e.signUp(dan, "e1-dan", nil)
	if _, err := e.st.DB().Exec(`UPDATE platform_user SET status = 'disabled' WHERE email = ?`, dan); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: dan, Code: dcode})
	wantCodeRefusal(t, err, "disabled account by code")
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: dlink})
	wantCodeRefusal(t, err, "disabled account by link")
	if n := e.sessions(dan) + e.count(`SELECT COUNT(*) FROM team WHERE slug = 'e1-dan' AND status <> 'pending'`); n != 0 {
		t.Fatalf("a disabled account: %d sessions or teams moved", n)
	}

	// (d) An operator: the address signed up before it was listed; the boot sync makes it an operator.
	// Its confirmation answers as before (the hand-off) but opens no session: operators use the password.
	ocode, _ := e.signUp(op, "e1-op", nil)
	if err := e.svc.SyncOperators(e.ctx()); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT COUNT(*) FROM platform_user WHERE email = ? AND is_operator = 1 AND status = 'pending'`, op); n != 1 {
		t.Fatalf("pending operator accounts = %d", n)
	}
	ov, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: op, Code: ocode})
	if err != nil || ov.GetTeamSlug() != "e1-op" || ov.GetHandoffCode() == "" || ov.GetRootToken() != "" {
		t.Fatalf("operator verify = %v, %v", ov, err)
	}
	if n := e.sessions(op); n != 0 {
		t.Fatalf("an operator's verify opened %d sessions", n)
	}

	// (a) An expired letter: today's refusal by code and by link, no session. Last: the clock moves.
	const cara = "delivered+e1cara@resend.dev"
	ccode, clink := e.signUp(cara, "e1-cara", nil)
	e.clock = e.clock.Add(VerifyTTL + time.Minute)
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: cara, Code: ccode})
	wantCodeRefusal(t, err, "expired code")
	_, err = e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: clink})
	wantCodeRefusal(t, err, "expired link")
	if n := e.sessions(cara); n != 0 {
		t.Fatalf("an expired letter opened %d sessions", n)
	}
}

// TestVerifyEmailLockedCode: the attempt limits stay in front — a locked code path refuses the right
// code with no token, and the letter's link still confirms.
func TestVerifyEmailLockedCode(t *testing.T) {
	e := newE1(t, false)
	const anna = "delivered+e1lock@resend.dev"
	code, link := e.signUp(anna, "e1-lock", nil)
	for range MaxRowCodeAttempts {
		_, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: wrong(code)})
		wantCodeRefusal(t, err, "wrong code")
	}
	r, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: code})
	wantCodeRefusal(t, err, "right code on a dead letter")
	if r.GetRootToken() != "" || e.sessions(anna) != 0 {
		t.Fatal("a dead code minted a session")
	}
	vr, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Token: link})
	if err != nil || vr.GetRootToken() == "" || e.sessions(anna) != 1 {
		t.Fatalf("link after a dead code = %v, %v", vr, err)
	}
}

type brandRowT struct {
	Name    string        `db:"brand_name"`
	Entity  string        `db:"legal_entity"`
	A1      string        `db:"legal_address_1"`
	A2      string        `db:"legal_address_2"`
	A3      string        `db:"legal_address_3"`
	Email   string        `db:"contact_email"`
	Website string        `db:"website"`
	By      sql.NullInt64 `db:"updated_by"`
}

// brandOf is the team's brand row; ok=false when it has none.
func (e *e1) brandOf(slug string) (brandRowT, bool) {
	e.t.Helper()
	var rows []brandRowT
	if err := e.st.DB().Select(&rows, `SELECT b.brand_name, b.legal_entity, b.legal_address_1, b.legal_address_2, b.legal_address_3,
  b.contact_email, b.website, b.updated_by FROM team_brand b JOIN team t ON t.id = b.team_id WHERE t.slug = ?`, slug); err != nil {
		e.t.Fatal(err)
	}
	if len(rows) == 0 {
		return brandRowT{}, false
	}
	return rows[0], true
}

// TestBrandAtCreation: sign-up and create team write the brand they carry, normalised, with the
// team, updated_by = the creator; an empty brand writes no row; a bad one refuses the whole request
// with the workspace's key before anything is written or mailed; a replaced sign-up replaces it.
func TestBrandAtCreation(t *testing.T) {
	e := newE1(t, false)
	const (
		anna = "delivered+e1banna@resend.dev"
		bob  = "delivered+e1bbob@resend.dev"
	)
	written := func() int {
		return e.count(`SELECT (SELECT COUNT(*) FROM platform_user) + (SELECT COUNT(*) FROM team) + (SELECT COUNT(*) FROM team_brand)
 + (SELECT COUNT(*) FROM platform_email) + (SELECT COUNT(*) FROM email_token)`)
	}

	// A bad brand: the key, nothing written, no letter.
	before := written()
	for key, b := range map[string]*pb_admin.TeamBrand{
		KeyBadText:    {BrandName: "a\nb"},
		KeyBadAddress: {LegalAddress: []string{"a", "b", "c", "d"}},
		KeyBadEmail:   {ContactEmail: "nope"},
		KeyBadWebsite: {Website: "localhost"},
	} {
		_, err := e.svc.SignUp(e.ctx(), &auth.SignUpRequest{Email: anna, Password: e1Password, TeamName: "Anna", TeamSlug: "e1-banna",
			TermsAccepted: true, Brand: b})
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != key {
			t.Fatalf("sign-up with a bad brand (%s) = %v", key, err)
		}
	}
	if n := written() - before; n != 0 {
		t.Fatalf("refused sign-ups wrote %d rows", n)
	}

	// A brand: written normalised, by the person signing up.
	_, _ = e.signUp(anna, "e1-banna", &pb_admin.TeamBrand{BrandName: "  Atelier   Anna ", LegalEntity: "Anna Ltd",
		LegalAddress: []string{"", "1 Road", " ", "London"}, ContactEmail: " Hi@Anna.IO ", Website: "https://Anna.io/", FirstTeam: true})
	got, ok := e.brandOf("e1-banna")
	want := brandRowT{Name: "Atelier Anna", Entity: "Anna Ltd", A1: "1 Road", A2: "London", Email: "hi@anna.io", Website: "anna.io",
		By: sql.NullInt64{Int64: e.userID(anna), Valid: true}}
	if !ok || got != want {
		t.Fatalf("brand at sign-up = %+v (%v), want %+v", got, ok, want)
	}

	// The same pending person signs up again: the old team goes with its brand, the new one has its own.
	_, _ = e.signUp(anna, "e1-banna2", &pb_admin.TeamBrand{BrandName: "Anna Two"})
	if _, ok := e.brandOf("e1-banna"); ok || e.count(`SELECT COUNT(*) FROM team WHERE slug = 'e1-banna'`) != 0 {
		t.Fatal("the replaced pending team or its brand survived")
	}
	if got, ok := e.brandOf("e1-banna2"); !ok || got.Name != "Anna Two" || got.Entity != "" || got.A1 != "" {
		t.Fatalf("brand of the replacing sign-up = %+v, %v", got, ok)
	}
	// Again without a brand: no row at all.
	acode, _ := e.signUp(anna, "e1-banna3", nil)
	if _, ok := e.brandOf("e1-banna2"); ok {
		t.Fatal("the brand of a replaced pending team survived")
	}
	if _, ok := e.brandOf("e1-banna3"); ok {
		t.Fatal("a sign-up without a brand wrote a row")
	}
	// An all-empty brand writes no row either.
	_, _ = e.signUp(bob, "e1-bbob", &pb_admin.TeamBrand{BrandName: " ", LegalAddress: []string{"", " "}, Website: " "})
	if _, ok := e.brandOf("e1-bbob"); ok {
		t.Fatal("an empty brand wrote a row")
	}

	// Create team, with the root token the confirmation returned.
	vr, err := e.svc.VerifyEmail(e.ctx(), &auth.VerifyEmailRequest{Email: anna, Code: acode})
	if err != nil || vr.GetRootToken() == "" {
		t.Fatalf("verify = %v, %v", vr, err)
	}
	root := vr.GetRootToken()
	teamsBefore := e.count(`SELECT COUNT(*) FROM team`)
	for key, b := range map[string]*pb_admin.TeamBrand{
		KeyBadText:    {LegalEntity: "x\x00"},
		KeyBadAddress: {LegalAddress: []string{"a", "b", "c", "d"}},
		KeyBadEmail:   {ContactEmail: "a@b"},
		KeyBadWebsite: {Website: "ftp://anna.io"},
	} {
		_, err := e.svc.CreateTeam(e.ctx(), root, &auth.CreateTeamRequest{TeamName: "More", TeamSlug: "e1-bmore", TermsAccepted: true, Brand: b})
		if status.Code(err) != codes.InvalidArgument || status.Convert(err).Message() != key {
			t.Fatalf("create team with a bad brand (%s) = %v", key, err)
		}
	}
	if n := e.count(`SELECT COUNT(*) FROM team`); n != teamsBefore {
		t.Fatalf("refused create-team wrote %d teams", n-teamsBefore)
	}
	ct, err := e.svc.CreateTeam(e.ctx(), root, &auth.CreateTeamRequest{TeamName: "More", TeamSlug: "e1-bmore", TermsAccepted: true,
		Brand: &pb_admin.TeamBrand{BrandName: "More", LegalAddress: []string{"2 Street"}, Website: "more.example.com"}})
	if err != nil || ct.GetTeam().GetSlug() != "e1-bmore" {
		t.Fatalf("create team = %v, %v", ct, err)
	}
	got, ok = e.brandOf("e1-bmore")
	want = brandRowT{Name: "More", A1: "2 Street", Website: "more.example.com", By: sql.NullInt64{Int64: e.userID(anna), Valid: true}}
	if !ok || got != want {
		t.Fatalf("brand at create team = %+v (%v), want %+v", got, ok, want)
	}
	if _, err := e.svc.CreateTeam(e.ctx(), root, &auth.CreateTeamRequest{TeamName: "Plain", TeamSlug: "e1-bplain", TermsAccepted: true,
		Brand: &pb_admin.TeamBrand{}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := e.brandOf("e1-bplain"); ok {
		t.Fatal("create team with an empty brand wrote a row")
	}
}
