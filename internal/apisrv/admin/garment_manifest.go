package admin

import (
	_ "embed"
	"encoding/json"
	"regexp"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// ─── the garment manifest (95-GARMENT-TAXONOMY §3): ONE source of truth for both repos ───
//
// garment-manifest.json is a byte-identical copy of the admin client's
// src/components/managers/tech-card/components/design/garment-manifest.json, copied by the client's
// scripts/sync-garment-manifest.sh. Never edit it here: TestGarmentManifest pins its sha256.
// It holds vocabulary only — families, their parts and views, category → family rules, the
// detail-word refinements and the shared truth table; geometry stays in the client.

//go:embed garment-manifest.json
var garmentManifestJSON []byte

type garmentManifestFamily struct {
	Label  string   `json:"label"`
	Group  string   `json:"group"`
	Traits []string `json:"traits"`
	Base   string   `json:"base"`
	Status string   `json:"status"`
	Parts  string   `json:"parts"`
}

type garmentManifestRefinement struct {
	ID         string            `json:"id"`
	DetailKeys []string          `json:"detail_keys"`
	AnyWords   []string          `json:"any_words"`
	FromTo     map[string]string `json:"from_to"`
}

type garmentManifestVector struct {
	Top     string            `json:"top"`
	Sub     string            `json:"sub"`
	Type    string            `json:"type"`
	Details map[string]string `json:"details"`
	Family  string            `json:"family"`
}

type garmentManifest struct {
	Version     int                              `json:"version"`
	Groups      []string                         `json:"groups"`
	Traits      map[string]string                `json:"traits"`
	PartLabels  map[string]string                `json:"part_labels"`
	Families    map[string]garmentManifestFamily `json:"families"`
	Categories  map[string]string                `json:"categories"`
	Refinements []garmentManifestRefinement      `json:"refinements"`
	Vectors     []garmentManifestVector          `json:"vectors"`
}

// Garment traits the checklist reads (manifest `traits`).
const (
	garmentTraitKnitwear    = "knitwear"
	garmentTraitCutSewKnit  = "cut_sew_knit"
	garmentTraitBottomsFit  = "bottoms_fit"
	garmentTraitSkirtVolume = "skirt_volume"
	garmentTraitBraFit      = "bra_fit"
)

var garmentManifestData = mustParseGarmentManifest(garmentManifestJSON)

func mustParseGarmentManifest(raw []byte) garmentManifest {
	var m garmentManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		panic("garment manifest: " + err.Error())
	}
	return m
}

// designQuizPartTableFromManifest renders `families.*.parts` in the table syntax
// parseDesignQuizPartTable reads ("family token token …" per line).
func designQuizPartTableFromManifest(m garmentManifest) string {
	var b strings.Builder
	for key, f := range m.Families {
		b.WriteString(key + " " + f.Parts + "\n")
	}
	return b.String()
}

// designQuizFamily maps the category NAMES (top, sub, type) to a pictogram family; "" when unknown.
// Lower-case + trim, then the first hit of "top/sub/type", "top/sub", "top" in the manifest's
// `categories` (the client's familyFor does the same).
func designQuizFamily(top, sub, typ string) string {
	top = strings.ToLower(strings.TrimSpace(top))
	sub = strings.ToLower(strings.TrimSpace(sub))
	typ = strings.ToLower(strings.TrimSpace(typ))
	for _, key := range []string{top + "/" + sub + "/" + typ, top + "/" + sub, top} {
		if f, ok := garmentManifestData.Categories[key]; ok {
			return f
		}
	}
	return ""
}

var garmentWordRe = regexp.MustCompile(`[a-z]+`)

// designQuizRefineFamily — §3.4: the card's own detail rows may move a family to its sibling
// (a sleeveless blouse → cami, a sleeveless tee → tank). Whole words, multi-word entries as
// substrings, only `from_to` siblings; "" is never refined.
func designQuizRefineFamily(family string, details []entity.TechCardDetail) string {
	if family == "" || len(details) == 0 {
		return family
	}
	for _, r := range garmentManifestData.Refinements {
		to, ok := r.FromTo[family]
		if !ok {
			continue
		}
		keys := map[string]bool{}
		for _, k := range r.DetailKeys {
			keys[strings.ToLower(k)] = true
		}
		var parts []string
		for _, d := range details {
			if keys[strings.ToLower(strings.TrimSpace(d.Key.String))] {
				parts = append(parts, d.Text.String)
			}
		}
		text := strings.ToLower(strings.Join(parts, " "))
		if strings.TrimSpace(text) == "" {
			continue
		}
		words := map[string]bool{}
		for _, w := range garmentWordRe.FindAllString(text, -1) {
			words[w] = true
		}
		for _, w := range r.AnyWords {
			if (strings.Contains(w, " ") && strings.Contains(text, w)) || words[w] {
				family = to
				break
			}
		}
	}
	return family
}

// designQuizCardFamily — the card's family: category first, then its detail words.
func designQuizCardFamily(card *entity.TechCard) string {
	family := designQuizFamily(designQuizCategoryPath(card))
	if card == nil {
		return family
	}
	return designQuizRefineFamily(family, card.Details)
}

// designQuizFamilyGroup — the checklist group of the system prompt a family belongs to.
func designQuizFamilyGroup(family string) string {
	if f, ok := garmentManifestData.Families[family]; ok {
		return f.Group
	}
	return "unknown"
}

// designQuizFamilyHas — the family carries the manifest trait.
func designQuizFamilyHas(family, trait string) bool {
	for _, t := range garmentManifestData.Families[family].Traits {
		if t == trait {
			return true
		}
	}
	return false
}
