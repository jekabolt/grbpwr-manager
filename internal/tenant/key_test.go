package tenant_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

func TestKeyScopesEveryTeamButOne(t *testing.T) {
	bg := context.Background()
	one := tenant.With(bg, tenant.TeamOne("grbpwr_beta"))
	a := tenant.With(bg, tenant.Team{ID: 2, Slug: "alpha"})
	b := tenant.With(bg, tenant.Team{ID: 3, Slug: "bravo"})

	if got := tenant.Key(bg, "42"); got != "42" {
		t.Fatalf("no team: %q", got)
	}
	if got := tenant.Key(one, "42"); got != "42" {
		t.Fatalf("team #1 must keep the key byte for byte: %q", got)
	}
	ka, kb := tenant.Key(a, "42"), tenant.Key(b, "42")
	if ka == "42" || kb == "42" || ka == kb {
		t.Fatalf("same local id must give distinct keys: %q %q", ka, kb)
	}

	d := sha256.Sum256([]byte("req"))
	if tenant.KeyHash(bg, d) != d || tenant.KeyHash(one, d) != d {
		t.Fatal("team #1 / no team must keep the digest")
	}
	if ha, hb := tenant.KeyHash(a, d), tenant.KeyHash(b, d); ha == d || hb == d || ha == hb {
		t.Fatal("same digest must give distinct keys per team")
	}
}
