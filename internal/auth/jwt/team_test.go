package jwt

import (
	"errors"
	"testing"
	"time"

	"github.com/go-chi/jwtauth/v5"
)

func teamAuth() *jwtauth.JWTAuth {
	return jwtauth.New("HS256", []byte("0123456789abcdef0123456789abcdef"), nil)
}

func TestTeamTokenRoundTrip(t *testing.T) {
	a := teamAuth()
	now := time.Now().UTC()
	in := TeamClaims{Handle: "anna", UserID: 42, TeamID: 7, SessionID: "s1", Audience: AudTeam, Perms: []string{"orders:read"}}
	tok, err := NewTeamTokenAt(a, time.Hour, in, now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyTeamToken(a, tok, &Expectations{Audience: AudTeam})
	if err != nil {
		t.Fatal(err)
	}
	if got.Legacy || got.UserID != 42 || got.TeamID != 7 || got.SessionID != "s1" || got.Audience != AudTeam ||
		got.Handle != "anna" || got.Super || len(got.Perms) != 1 || got.Perms[0] != "orders:read" {
		t.Fatalf("claims: %+v", got)
	}
	// A console endpoint refuses a team token.
	if _, err := VerifyTeamToken(a, tok, &Expectations{Audience: AudConsole}); err == nil {
		t.Fatal("team token accepted as console")
	}
	// The legacy verifier with the prod issuer expectation refuses a platform token.
	if _, _, _, _, err := VerifyAdminToken(a, tok, &Expectations{Issuer: "grbpwr-admin", Audience: "grbpwr-admin"}); err == nil {
		t.Fatal("platform token passed the legacy issuer check")
	}
}

func TestConsoleTokenIsNotLegacy(t *testing.T) {
	a := teamAuth()
	tok, err := NewTeamTokenAt(a, time.Hour, TeamClaims{Handle: "op", UserID: 3, SessionID: "s", Audience: AudConsole}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyTeamToken(a, tok, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Legacy || got.TeamID != 0 || got.Audience != AudConsole {
		t.Fatalf("console token read as %+v", got)
	}
}

func TestLegacyToken(t *testing.T) {
	a := teamAuth()
	opts := &IssueOpts{Issuer: "grbpwr-admin", Audience: "grbpwr-admin"}
	tok, err := NewAdminTokenAt(a, time.Hour, "boss", true, nil, opts, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err := VerifyTeamToken(a, tok, &Expectations{Issuer: "grbpwr-admin", Audience: "grbpwr-admin"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Legacy || got.TeamID != 0 || !got.Super || got.Handle != "boss" {
		t.Fatalf("legacy read as %+v", got)
	}
	// Wrong issuer still refused for a legacy token.
	if _, err := VerifyTeamToken(a, tok, &Expectations{Issuer: "other"}); err == nil {
		t.Fatal("legacy token with wrong issuer accepted")
	}
	// A team endpoint takes a legacy token; console / root do not.
	if _, err := VerifyTeamToken(a, tok, &Expectations{Audience: AudTeam}); err != nil {
		t.Fatalf("team endpoint refused legacy: %v", err)
	}
	for _, aud := range []string{AudConsole, AudRoot} {
		if _, err := VerifyTeamToken(a, tok, &Expectations{Audience: aud}); err == nil {
			t.Fatalf("%s endpoint accepted a legacy token", aud)
		}
	}
	// Pre-RBAC legacy token (no super claim) carries no access.
	old, err := NewTokenWithSubjectOptsAt(a, time.Hour, "boss", nil, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	got, err = VerifyTeamToken(a, old, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Legacy || got.Super || len(got.Perms) != 0 {
		t.Fatalf("pre-RBAC token read as %+v", got)
	}
}

func TestMalformedPlatformTokensRefused(t *testing.T) {
	a := teamAuth()
	now := time.Now()
	enc := func(claims map[string]any) string {
		claims["exp"] = now.Add(time.Hour).Unix()
		_, s, err := a.Encode(claims)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	cases := map[string]map[string]any{
		"team aud without tid":     {"sub": "x", "aud": AudTeam, "uid": 1, "sid": "s"},
		"console aud with tid":     {"sub": "x", "aud": AudConsole, "uid": 1, "tid": 2, "sid": "s"},
		"uid without platform aud": {"sub": "x", "aud": "grbpwr-admin", "uid": 1, "tid": 2, "sid": "s"},
		"tid without uid":          {"sub": "x", "tid": 2, "super": true},
		"no sid":                   {"sub": "x", "aud": AudTeam, "uid": 1, "tid": 2},
		"zero uid":                 {"sub": "x", "aud": AudTeam, "uid": 0, "tid": 2, "sid": "s"},
		"fractional tid":           {"sub": "x", "aud": AudTeam, "uid": 1, "tid": 2.5, "sid": "s"},
		"two audiences":            {"sub": "x", "aud": []string{AudTeam, AudRoot}, "uid": 1, "tid": 2, "sid": "s"},
	}
	for name, c := range cases {
		if _, err := VerifyTeamToken(a, enc(c), nil); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestNewTeamTokenRefusesBadClaims(t *testing.T) {
	a := teamAuth()
	bad := []TeamClaims{
		{Handle: "x", UserID: 1, SessionID: "s", Audience: "admin", TeamID: 1},
		{Handle: "x", UserID: 1, SessionID: "s", Audience: AudTeam},
		{Handle: "x", UserID: 1, SessionID: "s", Audience: AudRoot, TeamID: 1},
		{Handle: "x", SessionID: "s", Audience: AudRoot},
		{Handle: "x", UserID: 1, Audience: AudRoot},
	}
	for i, c := range bad {
		if _, err := NewTeamTokenAt(a, time.Hour, c, time.Now()); !errors.Is(err, errTeamClaims) {
			t.Errorf("case %d: err = %v", i, err)
		}
	}
}

func TestExpiredTeamTokenRefused(t *testing.T) {
	a := teamAuth()
	tok, err := NewTeamTokenAt(a, time.Minute, TeamClaims{Handle: "x", UserID: 1, TeamID: 1, SessionID: "s", Audience: AudTeam}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTeamToken(a, tok, nil); err == nil {
		t.Fatal("expired token accepted")
	}
}
