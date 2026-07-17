package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// TestTechCardRoleRoundTrip pins that every valid role maps to a distinct non-UNKNOWN enum and back
// (Q5), so a role can never silently degrade to UNKNOWN across the wire.
func TestTechCardRoleRoundTrip(t *testing.T) {
	roles := []entity.TechCardRole{
		entity.RoleDesigner, entity.RoleConstructor, entity.RoleTechnologist,
		entity.RolePatternMaker, entity.RoleGrader, entity.RoleApprover, entity.RoleOther,
	}
	seen := map[pb_common.TechCardRole]bool{}
	for _, r := range roles {
		pb := TechCardRoleToPb(r)
		if pb == pb_common.TechCardRole_TECH_CARD_ROLE_UNKNOWN {
			t.Errorf("role %q maps to UNKNOWN", r)
		}
		if seen[pb] {
			t.Errorf("role %q collides with another on enum %v", r, pb)
		}
		seen[pb] = true
		if back := TechCardRoleFromPb(pb); back != r {
			t.Errorf("round-trip %q -> %v -> %q", r, pb, back)
		}
	}
	// UNKNOWN maps to the empty string, which the handler rejects via ValidTechCardRoles.
	if TechCardRoleFromPb(pb_common.TechCardRole_TECH_CARD_ROLE_UNKNOWN) != "" {
		t.Error("UNKNOWN should map to empty role string")
	}
}
