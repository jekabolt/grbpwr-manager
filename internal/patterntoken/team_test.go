package patterntoken

import (
	"strings"
	"testing"
)

func TestTeamOneMintsV1Unchanged(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	for _, s := range allScopes {
		for _, team := range []int64{0, TeamOne} {
			if got, want := m.MintFor(team, s, 42, 3), m.Mint(s, 42, 3); got != want {
				t.Fatalf("team %d scope %c: %q, want v1 %q", team, s, got, want)
			}
		}
		team, scope, id, ep, err := m.ParseTeam(m.Mint(s, 42, 3))
		if err != nil || team != TeamOne || scope != s || id != 42 || ep != 3 {
			t.Fatalf("v1 parse: %d %c %d %d %v", team, scope, id, ep, err)
		}
	}
}

func TestV2RoundtripAndIsolation(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	for _, s := range allScopes {
		tok := m.MintFor(7, s, 42, 3)
		if strings.Count(tok, ".") != 3 {
			t.Fatalf("v2 shape: %q", tok)
		}
		team, scope, id, ep, err := m.ParseTeam(tok)
		if err != nil || team != 7 || scope != s || id != 42 || ep != 3 {
			t.Fatalf("v2 parse: %d %c %d %d %v", team, scope, id, ep, err)
		}
		// The old parser never accepts a v2 token (it would bind team #1).
		if _, _, _, err := m.Parse(tok); err == nil {
			t.Fatalf("v1 Parse accepted v2 token %q", tok)
		}
		// Team swapped in the address: signature breaks.
		parts := strings.SplitN(tok[1:], ".", 2)
		for _, other := range []string{"8", "1", "0", "-7", "07", "7 "} {
			forged := string(s) + other + "." + parts[1]
			if _, _, _, _, err := m.ParseTeam(forged); err == nil {
				t.Fatalf("forged team %q accepted", forged)
			}
		}
		// v1 signature grafted onto a v2 shape for team #1's object.
		v1 := m.Mint(s, 42, 3)
		graft := string(s) + "7." + v1[1:]
		if _, _, _, _, err := m.ParseTeam(graft); err == nil {
			t.Fatalf("grafted v1 sig accepted: %q", graft)
		}
		// Another pepper: nothing verifies.
		m2, _ := NewMinter("other")
		if _, _, _, _, err := m2.ParseTeam(tok); err == nil {
			t.Fatal("token verified under another pepper")
		}
		// Non-canonical spelling.
		if _, _, _, _, err := m.ParseTeam(strings.ToUpper(tok[:2]) + tok[2:]); err == nil && strings.ToUpper(tok[:2]) != tok[:2] {
			t.Fatal("non-canonical spelling accepted")
		}
	}
	if team, ok := m.TokenTeam(m.MintFor(9, ScopeFile, 1, 1)); !ok || team != 9 {
		t.Fatalf("TokenTeam: %d %v", team, ok)
	}
	if _, ok := m.TokenTeam("f9.1.1.AAAAAAAAAAAAAAAAAAAAAA"); ok {
		t.Fatal("TokenTeam accepted a bad signature")
	}
}

func TestV2NamingTeamOneRefused(t *testing.T) {
	m, _ := NewMinter("test-pepper")
	// Hand-build what a v2 token for team #1 would be: it must not verify (one spelling per object).
	tok := "i1.1a.3." + "AAAAAAAAAAAAAAAAAAAAAA"
	if _, _, _, _, err := m.ParseTeam(tok); err == nil {
		t.Fatal("v2 token for team #1 accepted")
	}
}
