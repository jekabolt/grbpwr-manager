package rbac

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// TestEveryAdminMethodIsClassified is the safety net that makes fail-closed
// enforcement safe: every method of AdminService must be either mapped to a
// section requirement or explicitly allowlisted. A newly added admin RPC that is
// forgotten here fails this test instead of silently shipping unprotected (the
// interceptor denies unmapped methods).
func TestEveryAdminMethodIsClassified(t *testing.T) {
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		full := MethodPrefix + m.MethodName
		req, allowlisted, known := Lookup(full)
		switch {
		case allowlisted:
			// fine: any authenticated account may call it.
		case known:
			if !ValidSection(req.Section) {
				t.Errorf("method %s maps to unknown section %q", m.MethodName, req.Section)
			}
			if !req.Access.Valid() {
				t.Errorf("method %s maps to invalid access %q", m.MethodName, req.Access)
			}
		default:
			t.Errorf("admin method %s is neither mapped to a section nor allowlisted; "+
				"add it to methodRequirements or allowlist in rbac.go", m.MethodName)
		}
	}
}

// TestCostingIsGrantableFieldShapingSection guards the task-19 costing section: it is a
// valid, catalogued, round-trippable grant even though NO method maps to it (it redacts
// response fields rather than gating whole RPCs). A regression that drops it from the
// catalog would silently make every costing:* grant unparseable (fail closed → no access).
func TestCostingIsGrantableFieldShapingSection(t *testing.T) {
	if !ValidSection(SectionCosting) {
		t.Fatalf("costing is not a valid section")
	}
	inCatalog := false
	for _, s := range Sections() {
		if s.Key == SectionCosting {
			inCatalog = true
		}
	}
	if !inCatalog {
		t.Errorf("costing is missing from the grantable catalog")
	}
	// It is deliberately method-less: no RPC requires it (enforcement is field shaping).
	for name, req := range methodRequirements {
		if req.Section == SectionCosting {
			t.Errorf("method %s maps to costing, but costing is a field-shaping section with no methods", name)
		}
	}
	// A costing grant survives the JWT encode→parse round-trip at both access levels.
	for _, lvl := range []entity.AccessLevel{entity.AccessRead, entity.AccessWrite} {
		got := ParsePermissions(EncodePermissions([]entity.AdminPermission{{Section: SectionCosting, Access: lvl}}))
		if have, ok := got[SectionCosting]; !ok || !have.Covers(lvl) {
			t.Errorf("costing:%s did not round-trip through encode/parse (got %v, ok=%v)", lvl, have, ok)
		}
	}
}

// TestNoStaleMappings guards the other direction: every mapped/allowlisted method
// must still exist on AdminService, so renamed/removed RPCs don't leave dead
// entries that could mask a real gap.
func TestNoStaleMappings(t *testing.T) {
	live := make(map[string]struct{}, len(pb_admin.AdminService_ServiceDesc.Methods))
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		live[m.MethodName] = struct{}{}
	}
	for name := range methodRequirements {
		if _, ok := live[name]; !ok {
			t.Errorf("methodRequirements has %q but AdminService has no such method", name)
		}
	}
	for name := range allowlist {
		if _, ok := live[name]; !ok {
			t.Errorf("allowlist has %q but AdminService has no such method", name)
		}
	}
}
