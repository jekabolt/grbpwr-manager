package rbac

import (
	"testing"

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
