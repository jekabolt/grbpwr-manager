package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
)

// The five methods the tech-card page calls outside its own section (X7): a member a new team
// invites with the default grant must reach all of them, by rbac alone.
func TestCardMethodsForTeamMembers(t *testing.T) {
	reads := []string{"GetStyleSizeChart", "GetStyleEconomics", "GetAlertSettings"}
	writes := []string{"UpdateStyleSizeChart", "UpdateStyle"}
	grant := func(p ...entity.AdminPermission) map[string]entity.AccessLevel {
		return rbac.ParsePermissions(rbac.EncodePermissions(p))
	}
	perm := func(sec string, lvl entity.AccessLevel) entity.AdminPermission {
		return entity.AdminPermission{Section: sec, Access: lvl}
	}
	defaultInvite := grant(DefaultInvitePermissions(feature.DefaultOn())...)
	for _, sec := range []string{rbac.SectionProducts, rbac.SectionAnalytics} {
		if _, ok := defaultInvite[sec]; ok {
			t.Fatalf("default invite of a new team now holds %s; this test assumes it does not", sec)
		}
	}
	cases := []struct {
		name  string
		perms map[string]entity.AccessLevel
		read  bool
		write bool
	}{
		{"default invite of a new team", defaultInvite, true, true},
		{"tech_cards + costing read only", grant(perm(rbac.SectionTechCards, entity.AccessRead), perm(rbac.SectionCosting, entity.AccessRead)), true, false},
		{"neither section", grant(perm(rbac.SectionTasks, entity.AccessWrite), perm(rbac.SectionFiles, entity.AccessWrite)), false, false},
		{"products + analytics write (unchanged)", grant(perm(rbac.SectionProducts, entity.AccessWrite), perm(rbac.SectionAnalytics, entity.AccessWrite)), true, true},
		{"products + analytics read (unchanged)", grant(perm(rbac.SectionProducts, entity.AccessRead), perm(rbac.SectionAnalytics, entity.AccessRead)), true, false},
	}
	for _, c := range cases {
		for _, m := range reads {
			if got := rbac.Allow(rbac.MethodPrefix+m, false, c.perms); got != c.read {
				t.Errorf("%s: %s allowed=%v, want %v", c.name, m, got, c.read)
			}
		}
		for _, m := range writes {
			if got := rbac.Allow(rbac.MethodPrefix+m, false, c.perms); got != c.write {
				t.Errorf("%s: %s allowed=%v, want %v", c.name, m, got, c.write)
			}
		}
	}
}
