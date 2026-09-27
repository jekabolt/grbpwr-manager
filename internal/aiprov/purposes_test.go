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

// seededPurposes reads the purposes 0373 seeds into ai_route straight from the migration file: the
// catalogue must describe what the database holds, not what this package believes it holds.
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
	return out
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
