package tenantbind

import (
	"context"
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Review item 9: a team whose database is behind the binary's migrations is unavailable; the
// predicate is injected (B4's fleet migrator) and defaults to «current».
func TestSchemaGateIsInjected(t *testing.T) {
	b, _ := teamsBinder(t)
	if b.SchemaBehind(acme) {
		t.Fatal("default predicate must say «current»")
	}
	b.UseSchemaCheck(func(tm tenant.Team) bool { return tm.ID == 7 })
	if !b.SchemaBehind(acme) {
		t.Fatal("injected predicate ignored")
	}
	if b.SchemaBehind(teamOne) {
		t.Fatal("team #1 migrates at boot and is never behind")
	}
	b.UseSchemaCheck(nil)
	if b.SchemaBehind(acme) {
		t.Fatal("nil predicate must restore «current»")
	}
}

func TestSchemaGateOnStorefront(t *testing.T) {
	b := newBinder(t)
	p := &fakePlane{dir: &mapDir{teams: map[int64]tenant.Team{7: acme}}}
	b.usePlane(p, platformstore.Config{StorefrontTeamID: 7})
	b.UseSchemaCheck(func(tenant.Team) bool { return true })
	if _, err := b.bindFor(context.Background(), ClassStorefront); status.Code(err) != codes.Unavailable {
		t.Fatalf("storefront on a team behind: %v, want Unavailable", err)
	}
	b.usePlane(p, platformstore.Config{StorefrontTeamID: 1})
	if _, err := b.bindFor(context.Background(), ClassStorefront); err != nil {
		t.Fatalf("team #1 storefront gated: %v", err)
	}
}

// Review item 4: the team a request names by Host / X-Team.
func TestRequestedTeam(t *testing.T) {
	b, _ := teamsBinder(t) // base domain beta.lekalo.studio, team 7 = acme
	cases := []struct {
		name, host, xteam string
		want              int64 // 0 = names none
		mismatch          bool
	}{
		{"api host, no header", "api.beta.lekalo.studio", "", 0, false},
		{"root domain is not a team", "beta.lekalo.studio", "", 0, false},
		{"foreign host", "backend-beta.grbpwr.com", "", 0, false},
		{"loopback", "localhost:8081", "", 0, false},
		{"team host", "acme.beta.lekalo.studio", "", 7, false},
		{"team host, case, port, dot", "ACME.Beta.Lekalo.Studio.:443", "", 7, false},
		{"header only", "api.beta.lekalo.studio", "Acme", 7, false},
		{"host and header agree", "acme.beta.lekalo.studio", "acme", 7, false},
		{"host and header disagree", "acme.beta.lekalo.studio", "grbpwr", 0, true},
		{"team one by header", "", "grbpwr", 1, false},
		{"unknown team", "", "nobody", 0, true},
		{"reserved slug header", "", "console", 0, true},
		{"non-ascii header", "", "аcme", 0, true},
		{"punycode host", "xn--cme-8cd.beta.lekalo.studio", "", 0, true},
		{"nested host", "a.acme.beta.lekalo.studio", "", 0, true},
	}
	for _, c := range cases {
		got, ok, err := b.RequestedTeam(context.Background(), c.host, c.xteam)
		if c.mismatch {
			if status.Code(err) != codes.PermissionDenied || status.Convert(err).Message() != MsgTeamMismatch {
				t.Errorf("%s: err %v, want PermissionDenied team_mismatch", c.name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if (c.want == 0) == ok || (ok && got.ID != c.want) {
			t.Errorf("%s: team %d ok=%v, want %d", c.name, got.ID, ok, c.want)
		}
	}
}

func TestRequestedTeamModeOffNamesNone(t *testing.T) {
	b := newBinder(t)
	for _, h := range [][2]string{{"acme.beta.lekalo.studio", "acme"}, {"", "nobody"}} {
		if _, ok, err := b.RequestedTeam(context.Background(), h[0], h[1]); ok || err != nil {
			t.Fatalf("mode off %v: ok=%v err=%v, want none", h, ok, err)
		}
	}
}

// Review item 3: the live control tuple behind a team token.
func TestTeamSession(t *testing.T) {
	b, p := teamsBinder(t)
	p.members = map[string]Membership{"live": {UserID: 5, TeamID: 7, Role: "owner"}}
	role, active, err := b.TeamSession(context.Background(), "live", 5, 7)
	if err != nil || !active || role != "owner" {
		t.Fatalf("live member: %q %v %v", role, active, err)
	}
	for _, c := range []struct {
		sid      string
		uid, tid int64
	}{{"live", 6, 7}, {"live", 5, 8}, {"gone", 5, 7}, {"", 5, 7}} {
		if _, active, err := b.TeamSession(context.Background(), c.sid, c.uid, c.tid); err != nil || active {
			t.Errorf("%+v: active=%v %v, want inactive", c, active, err)
		}
	}
	// Removal: the cached answer is dropped at once on this instance.
	delete(p.members, "live")
	b.InvalidateSession("live")
	if _, active, _ := b.TeamSession(context.Background(), "live", 5, 7); active {
		t.Fatal("removed member still active after InvalidateSession")
	}
	p.err = errors.New("down")
	if _, _, err := b.TeamSession(context.Background(), "x", 5, 7); err == nil {
		t.Fatal("control-plane error swallowed")
	}
}
