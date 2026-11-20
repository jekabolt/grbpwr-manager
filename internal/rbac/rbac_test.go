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

// The one-write seam gesture carries the same right as the two seam RPCs it replaces: a technologist
// with tech_cards:write must not be denied the new path while the old one still lets them through.
func TestChangeTechCardSeamsUsesTheSeamWriteAccess(t *testing.T) {
	want, _, known := Lookup(MethodPrefix + "UpsertTechCardSeams")
	if !known {
		t.Fatal("UpsertTechCardSeams is no longer mapped")
	}
	req, allowlisted, known := Lookup(MethodPrefix + "ChangeTechCardSeams")
	if allowlisted || !known {
		t.Fatalf("ChangeTechCardSeams must be section-gated (allowlisted=%v, known=%v)", allowlisted, known)
	}
	if req != want {
		t.Fatalf("ChangeTechCardSeams = %s:%s, want UpsertTechCardSeams' %s:%s", req.Section, req.Access, want.Section, want.Access)
	}
}

func TestDeleteTechCardPatternRevisionUsesTechCardWriteAccess(t *testing.T) {
	req, allowlisted, known := Lookup(MethodPrefix + "DeleteTechCardPatternRevision")
	if allowlisted || !known {
		t.Fatalf("DeleteTechCardPatternRevision must be section-gated (allowlisted=%v, known=%v)", allowlisted, known)
	}
	if req.Section != SectionTechCards || req.Access != entity.AccessWrite {
		t.Fatalf("DeleteTechCardPatternRevision = %s:%s, want %s:write", req.Section, req.Access, SectionTechCards)
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

func TestModelMannequinPoseUsesModelRights(t *testing.T) {
	for method, access := range map[string]entity.AccessLevel{
		"GetModelMannequinPose": entity.AccessRead,
		"PutModelMannequinPose": entity.AccessWrite,
	} {
		req, allowlisted, known := Lookup(MethodPrefix + method)
		if allowlisted || !known {
			t.Fatalf("%s must be explicitly classified", method)
		}
		if req.Section != SectionModels || req.Access != access {
			t.Errorf("%s = %s:%s, want %s:%s", method, req.Section, req.Access, SectionModels, access)
		}
	}
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
		name  string
		super bool
		perms map[string]entity.AccessLevel
		want  bool
	}{
		{"token without super claim", false, nil, false},
		{"scoped settings:write", false, map[string]entity.AccessLevel{SectionSettings: entity.AccessWrite}, false},
		{"scoped every section at write", false, everySectionWrite, false},
		{"super", true, nil, true},
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
				if got := Allow(full, c.super, c.perms); got != c.want {
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

// TestNoSuperClaimMeansNoAccess pins the removal of the pre-RBAC rule (teams §3): a token without
// the super claim and without perms reaches the allowlist and nothing else.
func TestNoSuperClaimMeansNoAccess(t *testing.T) {
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		full := MethodPrefix + m.MethodName
		_, allowlisted, _ := Lookup(full)
		if got := Allow(full, false, nil); got != allowlisted {
			t.Errorf("Allow(%s, no super, no perms) = %v, want %v", m.MethodName, got, allowlisted)
		}
		if got := Authorize(full, true, false, nil); got != allowlisted {
			t.Errorf("Authorize(%s, legacy, no super) = %v, want %v: legacy is ignored", m.MethodName, got, allowlisted)
		}
	}
}

// TestEveryAdminMethodHasAccessClass is G6: every AdminService method has an access class, so the
// read-only (suspended team) gate never meets an unclassified method.
func TestEveryAdminMethodHasAccessClass(t *testing.T) {
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		lvl, ok := Access(MethodPrefix + m.MethodName)
		if !ok || !lvl.Valid() {
			t.Errorf("admin method %s has no access class", m.MethodName)
		}
	}
	for _, s := range pb_admin.AdminService_ServiceDesc.Streams {
		lvl, ok := Access(MethodPrefix + s.StreamName)
		if !ok || !lvl.Valid() {
			t.Errorf("admin stream %s has no access class", s.StreamName)
		}
	}
	if _, ok := Access("/admin.AdminService/NoSuchMethod"); ok {
		t.Error("unknown method has an access class")
	}
	if _, ok := Access("/frontend.FrontendService/GetHero"); ok {
		t.Error("non-admin method has an access class")
	}
}

func TestAllowlistClasses(t *testing.T) {
	want := map[string]entity.AccessLevel{
		"GetDictionary":         entity.AccessRead,
		"GetCurrentAccount":     entity.AccessRead,
		"ListAccountSections":   entity.AccessRead,
		"GetWorkshopSettings":   entity.AccessRead,
		"ListAdmins":            entity.AccessRead,
		"SetAccountSpecialties": entity.AccessWrite,
		"TranslateFields":       entity.AccessWrite,
		"ListTeamMembers":       entity.AccessRead,
		"ListLibraryItems":      entity.AccessRead,
		"GetLibraryFacets":      entity.AccessRead,
		"ListLibraryFolders":    entity.AccessRead,
		"CreateLibraryFolder":   entity.AccessWrite,
		"RenameLibraryFolder":   entity.AccessWrite,
		"MoveLibraryFolder":     entity.AccessWrite,
		"DeleteLibraryFolder":   entity.AccessWrite,
		"MoveLibraryItems":      entity.AccessWrite,
	}
	if len(allowlist) != len(want) {
		t.Errorf("allowlist has %d entries, want %d", len(allowlist), len(want))
	}
	for name, lvl := range want {
		if got, ok := Access(MethodPrefix + name); !ok || got != lvl {
			t.Errorf("Access(%s) = %q, %v; want %q", name, got, ok, lvl)
		}
	}
	for _, m := range aiProviderMethods {
		if got, _ := Access(MethodPrefix + m); got != entity.AccessWrite {
			t.Errorf("SuperOnly %s must be write on a read-only team, got %q", m, got)
		}
	}
}

// mutatingPrefixes name methods that change state. None of them may be classed read, or a
// suspended team could call it.
var mutatingPrefixes = []string{
	"Create", "Update", "Delete", "Set", "Add", "Remove", "Upsert", "Import",
	"Generate", "Run", "Start", "Cancel",
}

// readByDesign are mutating-prefix names that really are reads, each with its reason.
var readByDesign = map[string]string{}

func TestNoMutatingMethodIsRead(t *testing.T) {
	for _, m := range pb_admin.AdminService_ServiceDesc.Methods {
		name := m.MethodName
		mutating := false
		for _, p := range mutatingPrefixes {
			if strings.HasPrefix(name, p) {
				mutating = true
				break
			}
		}
		if !mutating {
			continue
		}
		lvl, _ := Access(MethodPrefix + name)
		if lvl == entity.AccessRead {
			if _, ok := readByDesign[name]; !ok {
				t.Errorf("%s looks mutating but is classed read", name)
			}
		}
	}
	for name := range readByDesign {
		if lvl, _ := Access(MethodPrefix + name); lvl != entity.AccessRead {
			t.Errorf("readByDesign %s is no longer read (%q); drop the exception", name, lvl)
		}
	}
}

func TestAllowPreRBAC(t *testing.T) {
	if !AllowPreRBAC(MethodPrefix + "ListOrders") {
		t.Error("a pre-RBAC token must keep a scoped read")
	}
	if !AllowPreRBAC(MethodPrefix + "UpdateColorway") {
		t.Error("a pre-RBAC token must keep a scoped write")
	}
	if AllowPreRBAC(MethodPrefix + "UpdateAiProvider") {
		t.Error("a pre-RBAC token is not super: super-only methods stay closed")
	}
}
