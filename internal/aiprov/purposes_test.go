package aiprov

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// seededPurposes reads the purposes 0373 seeds into ai_route straight from the migration file, less
// every purpose a later migration's Up section retires with `DELETE FROM ai_route WHERE purpose = '…';`
// (0378: the operations draft, O-66): the catalogue must describe what the database holds, not what
// this package believes it holds.
func seededPurposes(t *testing.T) []string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "store", "sql", "0373_ai_providers.sql"))
	if err != nil {
		t.Fatalf("read 0373: %v", err)
	}
	insert := regexp.MustCompile(`(?s)INSERT INTO ai_route \(purpose, position, provider_key, model\) VALUES(.*?);`).
		FindStringSubmatch(string(body))
	if insert == nil {
		t.Fatal("sanity: no ai_route seed found in 0373 — the extractor is broken")
	}
	var out []string
	for _, m := range regexp.MustCompile(`\('([^']+)', 1,`).FindAllStringSubmatch(insert[1], -1) {
		out = append(out, m[1])
	}
	if len(out) == 0 {
		t.Fatal("sanity: the ai_route seed parsed to no purposes")
	}
	return slices.DeleteFunc(out, func(p string) bool { return slices.Contains(retiredPurposes(t), p) })
}

// retiredPurposes — the purposes the Up sections of the migrations after 0373 delete from ai_route
// outright (a whole purpose, not one of its rows).
func retiredPurposes(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "store", "sql", "0*.sql"))
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	retire := regexp.MustCompile(`DELETE FROM ai_route WHERE purpose = '([^']+)';`)
	var out []string
	for _, f := range files {
		if filepath.Base(f) <= "0373" {
			continue
		}
		body, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		up, _, _ := strings.Cut(string(body), "-- +migrate Down")
		for _, m := range retire.FindAllStringSubmatch(up, -1) {
			out = append(out, m[1])
		}
	}
	return out
}

// TestTheOperationsDraftPurposeIsRetired — the purpose is gone from all four places at once: 0378's Up
// deletes its route (the extractor above sees it), the seed as this package reads it, the entity
// vocabulary, and the panel's catalogue. Any one left behind is a label or a route for nothing.
func TestTheOperationsDraftPurposeIsRetired(t *testing.T) {
	require := func(ok bool, msg string) {
		t.Helper()
		if !ok {
			t.Fatal(msg)
		}
	}
	require(slices.Contains(retiredPurposes(t), "chat.techcard_operations_draft"),
		"0378's Up must delete the operations draft's route")
	require(!slices.Contains(seededPurposes(t), "chat.techcard_operations_draft"), "a retired purpose is not seeded")
	require(!entity.IsAIPurpose("chat.techcard_operations_draft"), "a retired purpose is not a purpose")
	_, ok := PurposeInfo("chat.techcard_operations_draft")
	require(!ok, "the panel's catalogue has no row for a retired purpose")
}

// TestAIPurposesCatalogueCoversEverySeededPurpose.
//
// MUTATIONS IT CATCHES: a purpose seeded in 0373 with no row here (the panel would show a route with
// no label — or not show it at all); a row whose key no migration seeds (a label for nothing); a row
// out of the vocabulary's order.
func TestAIPurposesCatalogueCoversEverySeededPurpose(t *testing.T) {
	seeded := seededPurposes(t)
	var keys []string
	for _, p := range Purposes() {
		keys = append(keys, p.Key)
	}
	for _, s := range seeded {
		if !slices.Contains(keys, s) {
			t.Errorf("0373 seeds purpose %q and the catalogue has no row for it", s)
		}
	}
	for _, k := range keys {
		if !slices.Contains(seeded, k) {
			t.Errorf("catalogue row %q names a purpose 0373 does not seed", k)
		}
	}
	if !slices.Equal(keys, entity.AIPurposes()) {
		t.Errorf("catalogue order %v, want entity.AIPurposes() %v", keys, entity.AIPurposes())
	}
}

// TestAIPurposesCatalogueRowsAgreeWithTheVocabulary.
//
// MUTATIONS IT CATCHES: a row's capability that is not entity.AIPurposeCapability of its key (the
// panel would offer providers the registry then refuses); a chat purpose filed under images; a label
// or hint left empty or capitalised (DESIGN.md: lowercase copy); a duplicate key (PurposeInfo would
// answer the first one).
func TestAIPurposesCatalogueRowsAgreeWithTheVocabulary(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Purposes() {
		if seen[p.Key] {
			t.Errorf("purpose %q appears twice", p.Key)
		}
		seen[p.Key] = true
		if want := entity.AIPurposeCapability(p.Key); p.Capability != want {
			t.Errorf("%s: capability %q, entity says %q", p.Key, p.Capability, want)
		}
		wantGroup := PurposeGroupImages
		switch p.Capability {
		case entity.AICapabilityChat:
			wantGroup = PurposeGroupChat
		case entity.AICapabilityThreed:
			wantGroup = PurposeGroup3D
		}
		if p.Group != wantGroup {
			t.Errorf("%s: group %q, want %q for capability %s", p.Key, p.Group, wantGroup, p.Capability)
		}
		for field, v := range map[string]string{"label": p.Label, "hint": p.Hint} {
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: empty %s", p.Key, field)
			}
			if strings.ToLower(v) != v {
				t.Errorf("%s: %s %q is not lowercase", p.Key, field, v)
			}
		}
		if got, ok := PurposeInfo(p.Key); !ok || got != p {
			t.Errorf("PurposeInfo(%q) = %+v, %t", p.Key, got, ok)
		}
	}
	if _, ok := PurposeInfo("chat.nothing"); ok {
		t.Error("PurposeInfo answered an unknown purpose")
	}
}
