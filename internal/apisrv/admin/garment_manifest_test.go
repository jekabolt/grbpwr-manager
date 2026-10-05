package admin

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// garmentManifestSHA256 — the client's scripts/sync-garment-manifest.sh rewrites this line; the
// client probe (scripts/garment-manifest-probe.mjs) pins the same number.
const garmentManifestSHA256 = "01442db110eed6bfb711d6460e7ac1c5dc2a535b0cd6b794e2a3bfbd2e200697"

// TestDesignQuizGarmentManifest — 95-GARMENT-TAXONOMY §3.3, the backend half of the anti-drift
// pair: the embedded copy is the synced one, its invariants hold, and the shared vectors resolve
// here exactly as the client's familyFor resolves them.
func TestDesignQuizGarmentManifest(t *testing.T) {
	sum := sha256.Sum256(garmentManifestJSON)
	if got := hex.EncodeToString(sum[:]); got != garmentManifestSHA256 {
		t.Fatalf("garment-manifest.json sha256 %s ≠ %s: copy it with the client's scripts/sync-garment-manifest.sh, never edit it here", got, garmentManifestSHA256)
	}
	m := garmentManifestData
	keyRe := regexp.MustCompile(`^[a-z0-9_]{1,16}$`)
	groups := map[string]bool{}
	for _, g := range m.Groups {
		groups[g] = true
	}
	views := map[string]bool{"f": true, "b": true, "s": true}
	for key, f := range m.Families {
		if !keyRe.MatchString(key) || len(key) > designQuizMaxFamilyLen || !designQuizIDRe.MatchString(key) {
			t.Errorf("%s: key does not fit the stored family column", key)
		}
		if !groups[f.Group] {
			t.Errorf("%s: group %q", key, f.Group)
		}
		for _, tr := range f.Traits {
			if _, ok := m.Traits[tr]; !ok {
				t.Errorf("%s: trait %q", key, tr)
			}
		}
		if f.Base != "" && m.Families[f.Base].Status != "existing" {
			t.Errorf("%s: base %q is not a kept family", key, f.Base)
		}
		tokens := strings.Fields(f.Parts)
		if len(tokens) == 0 || !strings.HasPrefix(tokens[0], "whole:") {
			t.Errorf("%s: whole first: %q", key, f.Parts)
		}
		for _, tok := range tokens {
			k, v, _ := strings.Cut(tok, ":")
			if !views[strings.TrimSuffix(v, "(z)")] {
				t.Errorf("%s: view of %s", key, tok)
			}
			if _, ok := m.PartLabels[k]; !ok {
				t.Errorf("%s: part %s not in part_labels", key, k)
			}
		}
	}
	for path, fam := range m.Categories {
		if _, ok := m.Families[fam]; !ok {
			t.Errorf("categories %s → %s: no such family", path, fam)
		}
	}
	for _, r := range m.Refinements {
		for from, to := range r.FromTo {
			ff, ok1 := m.Families[from]
			tf, ok2 := m.Families[to]
			if !ok1 || !ok2 || ff.Group != tf.Group {
				t.Errorf("refinement %s: %s → %s must stay inside one group", r.ID, from, to)
			}
		}
	}
	if len(m.Vectors) == 0 {
		t.Fatal("no shared vectors")
	}
	for _, v := range m.Vectors {
		var details []entity.TechCardDetail
		for k, text := range v.Details {
			details = append(details, entity.TechCardDetail{
				Key:  sql.NullString{String: k, Valid: true},
				Text: sql.NullString{String: text, Valid: true},
			})
		}
		if got := designQuizRefineFamily(designQuizFamily(v.Top, v.Sub, v.Type), details); got != v.Family {
			t.Errorf("vector %s/%s/%s %v = %q, want %q", v.Top, v.Sub, v.Type, v.Details, got, v.Family)
		}
	}
}

// TestDesignQuizFamilyRefineOwnerCard — 95 §1.3: the owner's strappy top filed under blouses,
// tanks or crop reads as cami / tank, never as the long-sleeve shirt; words never cross groups.
func TestDesignQuizFamilyRefineOwnerCard(t *testing.T) {
	detail := func(k, text string) entity.TechCardDetail {
		return entity.TechCardDetail{Key: sql.NullString{String: k, Valid: true}, Text: sql.NullString{String: text, Valid: true}}
	}
	cases := []struct {
		top, sub, typ string
		details       []entity.TechCardDetail
		want          string
	}{
		{"tops", "blouses", "", []entity.TechCardDetail{detail("silhouette", "Sleeveless strappy top")}, "cami"},
		{"tops", "blouses", "", []entity.TechCardDetail{detail("sleeveCuff", "strappy")}, "cami"},
		{"tops", "tanks", "", []entity.TechCardDetail{detail("collar", "strappy")}, "tank"},
		{"tops", "crop", "", []entity.TechCardDetail{detail("silhouette", "sleeveless")}, "tank"},
		{"tops", "shirts", "", []entity.TechCardDetail{detail("collar", "no sleeves at all")}, "cami"},
		{"tops", "blouses", "", []entity.TechCardDetail{detail("fabric", "sleeveless")}, "blouse"},              // other detail key
		{"tops", "blouses", "", []entity.TechCardDetail{detail("silhouette", "tanker-style volume")}, "blouse"}, // whole words only
		{"bottoms", "pants", "", []entity.TechCardDetail{detail("silhouette", "sleeveless")}, "trousers"},       // never across groups
		{"", "", "", []entity.TechCardDetail{detail("silhouette", "sleeveless")}, ""},
	}
	for _, c := range cases {
		if got := designQuizRefineFamily(designQuizFamily(c.top, c.sub, c.typ), c.details); got != c.want {
			t.Errorf("%s/%s/%s = %q, want %q", c.top, c.sub, c.typ, got, c.want)
		}
	}
	card := &entity.TechCard{}
	card.Details = []entity.TechCardDetail{detail("silhouette", "strappy")}
	if got := designQuizCardFamily(card); got != "" {
		t.Errorf("no category → no family, got %q", got)
	}
}
