package rbac

import (
	"slices"
	"sort"
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
			if _, pending := pendingProtoMethods[name]; pending {
				t.Logf("methodRequirements has %q ahead of the proto (lane P, P-01, adds it)", name)
				continue
			}
			t.Errorf("methodRequirements has %q but AdminService has no such method", name)
		}
	}
	for name := range allowlist {
		if _, ok := live[name]; !ok {
			t.Errorf("allowlist has %q but AdminService has no such method", name)
		}
	}
}

// pendingProtoMethods are classified in methodRequirements BEFORE AdminService has them: the six AI
// providers RPCs are being added to proto/admin by lane P (ai-providers task P-01) in parallel with
// their classification (task B-19, lane B1), and they are classified first so that the moment they
// are generated they are already super-only — an RPC that lands unclassified is denied to scoped
// accounts but WIDE OPEN to a legacy token. TestNoStaleMappings tolerates exactly these names while
// they are missing from the descriptor and nothing else; once lane P is merged the tolerance is a
// no-op and this set should be deleted.
var pendingProtoMethods = map[string]struct{}{
	"GetAiProvidersConfig": {},
	"UpdateAiProvider":     {},
	"SetAiProviderKey":     {},
	"SetAiDefaults":        {},
	"SetAiRoute":           {},
	"GetAiSpendReport":     {},
}

// aiProviderMethods are the six RPCs of the AI providers panel (ai-providers plan A5).
var aiProviderMethods = []string{
	"GetAiProvidersConfig",
	"UpdateAiProvider",
	"SetAiProviderKey",
	"SetAiDefaults",
	"SetAiRoute",
	"GetAiSpendReport",
}

// TestAiMethodsAreSuperOnly is the owner's rule for the AI providers panel (plan A8, D-03): every one
// of its six RPCs, the reads included, is callable by a super account and by nobody else. The two
// callers that the rest of the RBAC lets through are the ones that matter here: a legacy pre-RBAC
// token (full access everywhere else, but not super) and a scoped account holding the section the
// entries are filed under (settings:write) — or, for that matter, every section at write.
//
// It also pins the SET of super-only methods to exactly these six, so the flag cannot quietly spread
// to a method a legacy token is still entitled to, nor drop off one of the six.
func TestAiMethodsAreSuperOnly(t *testing.T) {
	everySectionWrite := make(map[string]entity.AccessLevel, len(catalog))
	for _, s := range Sections() {
		everySectionWrite[s.Key] = entity.AccessWrite
	}
	callers := []struct {
		name   string
		legacy bool
		super  bool
		perms  map[string]entity.AccessLevel
		want   bool
	}{
		{"legacy full-access token", true, false, nil, false},
		{"scoped settings:write", false, false, map[string]entity.AccessLevel{SectionSettings: entity.AccessWrite}, false},
		{"scoped every section at write", false, false, everySectionWrite, false},
		{"super", false, true, nil, true},
	}
	for _, m := range aiProviderMethods {
		full := MethodPrefix + m
		req, allowlisted, known := Lookup(full)
		if allowlisted || !known {
			t.Fatalf("%s must be classified in methodRequirements (allowlisted=%v, known=%v)", m, allowlisted, known)
		}
		if !req.SuperOnly {
			t.Errorf("%s must be SuperOnly", m)
		}
		if !ValidSection(req.Section) || !req.Access.Valid() {
			t.Errorf("%s maps to an invalid requirement %s:%s", m, req.Section, req.Access)
		}
		for _, c := range callers {
			t.Run(m+"/"+c.name, func(t *testing.T) {
				if got := Authorize(full, c.legacy, c.super, c.perms); got != c.want {
					t.Errorf("Authorize(%s) for %s = %v, want %v", m, c.name, got, c.want)
				}
			})
		}
	}

	var superOnly []string
	for name, r := range methodRequirements {
		if r.SuperOnly {
			superOnly = append(superOnly, name)
		}
	}
	sort.Strings(superOnly)
	want := slices.Clone(aiProviderMethods)
	sort.Strings(want)
	if !slices.Equal(superOnly, want) {
		t.Errorf("SuperOnly methods = %v, want exactly %v", superOnly, want)
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
