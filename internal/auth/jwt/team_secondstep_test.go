package jwt

import (
	"context"
	"testing"
	"time"
)

// The second-step claim (gap 4): a console token carries it, a team or root token can neither be
// minted with it nor read as having it, even when a hand-made token sets it.
func TestSecondStepClaim(t *testing.T) {
	a := teamAuth()
	now := time.Now().UTC()
	tok, err := NewTeamTokenAt(a, time.Hour, TeamClaims{Handle: "op", UserID: 3, SessionID: "s", Audience: AudConsole, SecondStep: true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyTeamToken(a, tok, nil); err != nil || !got.SecondStep {
		t.Fatalf("console token lost the second step: %+v %v", got, err)
	}
	plain, err := NewTeamTokenAt(a, time.Hour, TeamClaims{Handle: "op", UserID: 3, SessionID: "s", Audience: AudConsole}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyTeamToken(a, plain, nil); err != nil || got.SecondStep {
		t.Fatalf("plain console token read as second step: %+v %v", got, err)
	}
	if _, err := NewTeamTokenAt(a, time.Hour, TeamClaims{Handle: "ann", UserID: 3, TeamID: 7, SessionID: "s", Audience: AudTeam, SecondStep: true}, now); err == nil {
		t.Fatal("a team token was minted with the second step")
	}
	team, err := NewTeamTokenAt(a, time.Hour, TeamClaims{Handle: "ann", UserID: 3, TeamID: 7, SessionID: "s", Audience: AudTeam}, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := a.Decode(team)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parsed.AsMap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m[claimSecondStep] = true
	_, forged, err := a.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := VerifyTeamToken(a, forged, nil); err == nil && got.SecondStep {
		t.Fatal("a team token was read as having the second step")
	}
}
