package tenanttest

import (
	"context"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestEveryMethodClassified: no AdminService method without an access level and a feature
// requirement (an unknown method is refused closed by the chain, but it is also a gap).
func TestEveryMethodClassified(t *testing.T) {
	var gaps []string
	for _, m := range adminMethods() {
		if _, ok := rbac.Access(m.full); !ok {
			gaps = append(gaps, m.name+" (rbac)")
		}
		if _, ok := feature.ForMethod(m.full); !ok {
			gaps = append(gaps, m.name+" (feature)")
		}
	}
	if len(gaps) > 0 {
		t.Errorf("%d unclassified: %s", len(gaps), strings.Join(gaps, ", "))
	}
	t.Logf("%d AdminService methods", len(adminMethods()))
}

func refusedNotReached(t *testing.T, label string, o outcome, code codes.Code, msg string) bool {
	t.Helper()
	if o.reached {
		t.Errorf("%s: handler reached (team %d)", label, o.team.ID)
		return false
	}
	if status.Code(o.err) != code || (msg != "" && !strings.Contains(status.Convert(o.err).Message(), msg)) {
		t.Errorf("%s: %v, want %s %q", label, o.err, code, msg)
		return false
	}
	return true
}

// TestForeignTeamPresentation: team A's valid token never runs while the request names team B —
// by X-Team or by B's browser origin — on any method.
func TestForeignTeamPresentation(t *testing.T) {
	x := setup(t)
	byXTeam := x.asA()
	byXTeam.xteam = slugB
	byOrigin := x.asA()
	byOrigin.origin = "https://" + slugB + "." + baseDomain
	for _, m := range adminMethods() {
		refusedNotReached(t, m.name+" A token, X-Team B", x.invoke(m, x.adm, byXTeam, true, ""), codes.PermissionDenied, "team_mismatch")
		refusedNotReached(t, m.name+" A token, B origin", x.invoke(m, x.adm, byOrigin, true, ""), codes.Unauthenticated, "")
	}
}

// TestTokenForTeamWithoutMembership: B's live session and user, a token naming team #1 (where B
// is no member) — refused on every method.
func TestTokenForTeamWithoutMembership(t *testing.T) {
	x := setup(t)
	c := x.asA()
	c.token = x.tokBAsOne
	for _, m := range adminMethods() {
		refusedNotReached(t, m.name, x.invoke(m, x.adm, c, true, ""), codes.Unauthenticated, "")
	}
}

// TestBoundTeamIsTokenTeam: when a call passes, the team in ctx is the token's team, for every
// method, both teams.
func TestBoundTeamIsTokenTeam(t *testing.T) {
	x := setup(t)
	effB, err := x.b.Features(context.Background(), x.teamB)
	if err != nil {
		t.Fatal(err)
	}
	closedForB := 0
	defer func() { t.Logf("%d methods closed for B by features it cannot have (team #1 only)", closedForB) }()
	for _, m := range adminMethods() {
		keys, _ := feature.ForMethod(m.full)
		for _, tc := range []struct {
			c  caller
			id int64
		}{{x.asA(), 1}, {x.asB(), 2}} {
			o := x.invoke(m, x.adm, tc.c, true, "")
			if tc.id == 2 && !feature.Allowed(effB, keys) {
				// Every catalog key was switched on for B; what stays off is not B's to have.
				closedForB++
				refusedNotReached(t, m.name+" as B", o, codes.PermissionDenied, "feature_off")
				continue
			}
			if !o.reached {
				t.Errorf("%s as team %d: refused %v", m.name, tc.id, o.err)
				continue
			}
			if o.team.ID != tc.id {
				t.Errorf("%s: bound team %d, token team %d", m.name, o.team.ID, tc.id)
			}
		}
	}
}

// TestSuspendedTeamReadOnly: B suspended → every write is FAILED_PRECONDITION team_read_only
// before its handler; every read still runs (as B).
func TestSuspendedTeamReadOnly(t *testing.T) {
	x := setup(t)
	ctx := context.Background()
	if err := x.ctl.SetTeamReadOnly(ctx, 2, true); err != nil {
		t.Fatal(err)
	}
	x.b.InvalidateTeam(2)
	t.Cleanup(func() {
		_ = x.ctl.SetTeamReadOnly(ctx, 2, false)
		x.b.InvalidateTeam(2)
	})
	writes, reads := 0, 0
	for _, m := range adminMethods() {
		o := x.invoke(m, x.adm, x.asB(), true, "")
		if m.read {
			reads++
			if !o.reached || o.team.ID != 2 {
				t.Errorf("%s (read) on suspended B: reached=%v team=%d %v", m.name, o.reached, o.team.ID, o.err)
			}
			continue
		}
		writes++
		refusedNotReached(t, m.name+" (write) on suspended B", o, codes.FailedPrecondition, "team_read_only")
		// A is untouched by B's status.
		if oa := x.invoke(m, x.adm, x.asA(), true, ""); !oa.reached {
			t.Errorf("%s: A refused while B suspended: %v", m.name, oa.err)
		}
	}
	t.Logf("suspended B: %d writes refused, %d reads served", writes, reads)
}

// TestTeamOneNeverSuspended: the console refuses to pause team #1, and even a control row set to
// suspended by hand does not make team #1 read-only (the binder serves the boot-time team).
func TestTeamOneNeverSuspended(t *testing.T) {
	x := setup(t)
	op := caller{token: x.tokOp}
	var setStatus method
	for _, pm := range pb_admin.PlatformService_ServiceDesc.Methods {
		if pm.MethodName == "SetTeamStatus" {
			setStatus = method{name: pm.MethodName, full: "/" + pb_admin.PlatformService_ServiceDesc.ServiceName + "/" + pm.MethodName, handler: pm.Handler}
		}
	}
	if setStatus.handler == nil {
		t.Fatal("PlatformService.SetTeamStatus not found")
	}
	o := x.invoke(setStatus, x.plat, op, false, `{"teamSlug":"`+tenant.TeamOneSlug+`","readOnly":true}`)
	if status.Code(o.err) != codes.FailedPrecondition || !strings.Contains(status.Convert(o.err).Message(), "team_one") {
		t.Errorf("SetTeamStatus(team #1): %v %s, want FAILED_PRECONDITION team_one", o.err, o.panic)
	}

	ctx := context.Background()
	if _, err := x.ctl.DB().ExecContext(ctx, `UPDATE team SET status = 'suspended' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	x.ctl.Directory().Invalidate(1)
	x.b.InvalidateTeam(1)
	t.Cleanup(func() {
		_, _ = x.ctl.DB().ExecContext(ctx, `UPDATE team SET status = 'active' WHERE id = 1`)
		x.ctl.Directory().Invalidate(1)
		x.b.InvalidateTeam(1)
	})
	for _, m := range adminMethods() {
		if m.read {
			continue
		}
		if oa := x.invoke(m, x.adm, x.asA(), true, ""); !oa.reached {
			t.Errorf("%s: team #1 write refused with its control row suspended: %v", m.name, oa.err)
		}
	}
}

// TestFeatureOffRefusedPerTeam: each feature switched off for B refuses every B method that needs
// it (feature_off, before the handler) and leaves the same method open for A.
func TestFeatureOffRefusedPerTeam(t *testing.T) {
	x := setup(t)
	ctx := context.Background()
	byKey := map[feature.Key][]method{}
	for _, m := range adminMethods() {
		keys, _ := feature.ForMethod(m.full)
		for _, k := range keys {
			if k != feature.Core {
				byKey[k] = append(byKey[k], m)
			}
		}
	}
	for k, ms := range byKey {
		if err := x.ctl.SetFeature(ctx, 2, string(k), false, uidOp); err != nil {
			t.Fatal(err)
		}
		x.b.InvalidateTeam(2)
		for _, m := range ms {
			refusedNotReached(t, m.name+" B with "+string(k)+" off", x.invoke(m, x.adm, x.asB(), true, ""), codes.PermissionDenied, "feature_off")
			if oa := x.invoke(m, x.adm, x.asA(), true, ""); !oa.reached {
				t.Errorf("%s: A refused while B has %s off: %v", m.name, k, oa.err)
			}
		}
		if err := x.ctl.SetFeature(ctx, 2, string(k), true, uidOp); err != nil {
			t.Fatal(err)
		}
		x.b.InvalidateTeam(2)
	}
	t.Logf("%d features × their methods checked", len(byKey))
}
