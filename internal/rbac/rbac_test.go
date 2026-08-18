package rbac

import (
	"strings"
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

// TestProjectTasksStayUnderTaskRights охраняет решение фазы 0322: «какие задачи у этого проекта»
// читается ФИЛЬТРОМ существующего ListTasks, а не отдельным RPC.
//
// Утверждение здесь ровно одно, и оно про ПРАВА, а не про экономию: пока обратный вопрос ходит
// через ListTasks, у него по построению те же tasks:read, что у доски. Отдельный RPC пришлось бы
// классифицировать заново, и живой соблазн — повесить его на files, раз он живёт на странице
// проекта. Тогда обладатель одного лишь files:read прочитал бы заголовки, исполнителей и сроки
// задач, которых ему не показывают нигде больше, — то есть проект стал бы боковым каналом к доске.
//
// Тест краснеет двумя способами: если ListTasks переклассифицируют, и если рядом заведут RPC с
// «project» и «task» в имени, которому дали права ФАЙЛОВ.
func TestProjectTasksStayUnderTaskRights(t *testing.T) {
	req, allowlisted, known := Lookup(MethodPrefix + "ListTasks")
	if allowlisted || !known {
		t.Fatal("ListTasks must stay classified: it is the only path to the tasks of a project")
	}
	if req.Section != SectionTasks || req.Access != entity.AccessRead {
		t.Errorf("ListTasks must require %s:read, got %s:%s", SectionTasks, req.Section, req.Access)
	}

	for name, r := range methodRequirements {
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "task") || !strings.Contains(lower, "project") {
			continue
		}
		if r.Section == SectionFiles {
			t.Errorf("%s reads tasks of a project but is gated by %s — a person holding only "+
				"files:read would learn titles, assignees and deadlines of tasks they cannot see "+
				"anywhere else", name, SectionFiles)
		}
	}
}
