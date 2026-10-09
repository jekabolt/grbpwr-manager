package admin

import (
	"slices"
	"strings"
)

// ─── 91-EDGE-KEYS: canonical edge decision keys + the guaranteed edge_exceptions follow-up ───
//
// Live runs coined their own keys for edge finishes (neck_rib, sleeve_opening, trim_edges, a
// cuff_style whose options are edge finishes), so E1 dedupe and the coverage step could not see that
// two questions settle the same edge. After parsing, an edge question gets the canonical key of the
// edge zone it names (one zone → its key, several or "trim / edges" → edge_finish_main); dedupe runs
// on the canonical key. An edge_finish_main question always carries its follow-up (clarify) — the
// client asks it after the main answer as the edge_exceptions decision.

const (
	designQuizEdgeMainKey       = "edge_finish_main"
	designQuizEdgeExceptionsKey = "edge_exceptions"
	// designQuizEdgeExceptionsQuestion — the follow-up's fixed wording.
	designQuizEdgeExceptionsQuestion = "Which edges are finished differently?"
	// designQuizMaxEdgeExceptions — the follow-up's options (2–6 "<edge>: <finish>" pairs); the client
	// appends "none — all the same", so the saved edge_exceptions question holds one more.
	designQuizMaxEdgeExceptions = 6
)

// designQuizEdgeZones — the edge zones and the words that name them (whole words, singularised,
// longest first); key "" is the generic "trim / edges" word (→ edge_finish_main).
var designQuizEdgeZones = []struct {
	key     string
	aliases []string
}{
	{"neck_finish", []string{"neckline", "neck", "neckband", "neck opening", "neck edge", "collar edge", "crew neck"}},
	{"armhole_finish", []string{"armhole", "armhole edge"}},
	{"sleeve_finish", []string{"sleeve opening", "sleeve hem", "sleeve end", "sleeve edge", "sleeve", "cuff", "cuff finish", "cuff edge", "wrist"}},
	{"front_edge_finish", []string{"front edge", "centre front edge", "center front edge", "placket edge", "front opening"}},
	{"waistband_finish", []string{"waistband", "waistband edge", "waist edge", "waist"}},
	{"leg_finish", []string{"leg opening", "leg hem", "leg", "ankle", "trouser hem"}},
	{"pocket_edge_finish", []string{"pocket", "pocket opening", "pocket edge", "pocket flap"}},
	{"vent_finish", []string{"vent", "slit", "side slit"}},
	{"hem_finish", []string{"hem", "body hem", "bottom hem", "hemline", "bottom edge"}},
	{"hood_edge_finish", []string{"hood", "hood edge", "hood opening"}},
	{"", []string{"trim", "edge", "edging", "all edge", "every edge", "throughout"}},
}

var designQuizEdgeZoneAliases = func() []designQuizAlias {
	var out []designQuizAlias
	for _, z := range designQuizEdgeZones {
		for _, a := range z.aliases {
			out = append(out, designQuizAlias{z.key, designQuizAliasWords(a)})
		}
	}
	slices.SortStableFunc(out, func(a, b designQuizAlias) int { return len(b.words) - len(a.words) })
	return out
}()

// designQuizEdgeZonesOf — the edge zones a text names (whole words, longest match first, a word
// counted once), in order of first appearance, and whether a generic "trim / edges" word is there.
func designQuizEdgeZonesOf(text string) (zones []string, generic bool) {
	words := designQuizAliasWords(strings.ReplaceAll(text, "_", " "))
	used := make([]bool, len(words))
	at := map[string]int{}
	for _, a := range designQuizEdgeZoneAliases {
		for i := 0; i+len(a.words) <= len(words); i++ {
			if !slices.Equal(words[i:i+len(a.words)], a.words) || slices.Contains(used[i:i+len(a.words)], true) {
				continue
			}
			for j := i; j < i+len(a.words); j++ {
				used[j] = true
			}
			if a.key == "" {
				generic = true
				continue
			}
			if p, ok := at[a.key]; !ok || i < p {
				at[a.key] = i
			}
		}
	}
	for k := range at {
		zones = append(zones, k)
	}
	slices.SortFunc(zones, func(a, b string) int { return at[a] - at[b] })
	return zones, generic
}

// designQuizIsEdgeFinish — the option names an edge / hem finish (sm_hem_*, the 8 edge kinds, raw).
func designQuizIsEdgeFinish(label string) bool {
	k := designQuizSeamOf(label)
	if strings.HasPrefix(k, "sm_hem_") || designQuizEdgeKeys[k] {
		return true
	}
	return k == "" && slices.Contains(designQuizAliasWords(label), "raw")
}

// designQuizEdgeFinishWords — a name-only edge question must speak of a finish …
var designQuizEdgeFinishWords = map[string]bool{
	"finish": true, "finished": true, "finishing": true, "edge": true, "opening": true, "trim": true,
	"hem": true, "hemmed": true, "band": true, "binding": true, "bound": true, "piping": true, "piped": true,
}

// … and not of a shape, size, colour or reinforcement of that edge (neck tape, neckline depth).
var designQuizEdgeNonFinishWords = map[string]bool{
	"shape": true, "depth": true, "length": true, "width": true, "height": true, "position": true,
	"placement": true, "colour": true, "color": true, "tape": true, "reinforcement": true, "size": true,
	"count": true, "number": true, "curve": true,
}

// designQuizEdgeProtectedKeys — keys never rewritten: seam decisions, and the main key the model chose.
var designQuizEdgeProtectedKeys = map[string]bool{
	"main_seam": true, "extra_seams": true, "seams_visible": true,
	designQuizEdgeMainKey: true, designQuizEdgeExceptionsKey: true,
}

// designQuizCanonicalEdgeKey — the canonical decision key of an edge question, key unchanged when the
// question is not about an edge finish. An EDGE question: details, and ≥2 options are edge finishes,
// or its key / part names an edge and the key or wording speaks of a finish (never of its shape,
// size, colour or tape). A multi "which edges differ" question → edge_exceptions; one zone named →
// that zone's key; several zones or the generic "trim / edges" → edge_finish_main. Zones are read
// from the question, then the key, then the part.
func designQuizCanonicalEdgeKey(category, key, part, kind, question string, options []string) string {
	if category != "details" || designQuizEdgeProtectedKeys[key] {
		return key
	}
	edgeOpts, pairs := 0, 0
	for _, o := range options {
		if designQuizIsEdgeFinish(o) {
			edgeOpts++
		}
		if strings.Contains(o, ":") {
			pairs++
		}
	}
	if edgeOpts < 2 {
		keyZones, keyGeneric := designQuizEdgeZonesOf(key)
		partZones, _ := designQuizEdgeZonesOf(part)
		if len(keyZones) == 0 && !keyGeneric && len(partZones) == 0 {
			return key
		}
		finish := false
		for _, w := range designQuizAliasWords(strings.ReplaceAll(key, "_", " ") + " " + question) {
			if designQuizEdgeNonFinishWords[w] {
				return key
			}
			finish = finish || designQuizEdgeFinishWords[w]
		}
		if !finish {
			return key
		}
	}
	if kind == "multi" {
		words := designQuizAliasWords(strings.ReplaceAll(key, "_", " ") + " " + question)
		if pairs >= 2 || slices.ContainsFunc(words, func(w string) bool {
			return w == "other" || w == "extra" || w == "exception" || w == "differently" || w == "different"
		}) {
			return designQuizEdgeExceptionsKey
		}
	}
	for _, src := range []string{question, key, part} {
		zones, generic := designQuizEdgeZonesOf(src)
		switch {
		case len(zones) == 1:
			return zones[0]
		case len(zones) > 1 || generic:
			return designQuizEdgeMainKey
		}
	}
	return designQuizEdgeMainKey
}

// designQuizGroupEdges — the open edges of a checklist group, most likely exception first (the
// server's fallback follow-up when the model sent edge_finish_main without one).
func designQuizGroupEdges(group string) []string {
	switch group {
	case "outerwear":
		return []string{"front edge", "neckline", "sleeve openings", "hem", "pocket openings", "hood edge", "vents"}
	case "tops":
		return []string{"neckline", "sleeve openings", "hem", "front edge", "pocket openings", "armholes"}
	case "bottoms":
		return []string{"waistband edge", "leg openings", "pocket openings", "fly", "slits"}
	case "dresses and one-pieces":
		return []string{"neckline", "armholes", "sleeve openings", "hem", "slits"}
	case "underwear and swim":
		return []string{"leg openings", "waist", "straps", "neckline"}
	}
	return []string{"openings", "hem", "trims"}
}

// designQuizEdgeExceptionsFallback — "<edge>: different finish" for the group's open edges minus the
// ones the main question names; 2–6, never empty.
func designQuizEdgeExceptionsFallback(family, mainQuestion string) []string {
	named, _ := designQuizEdgeZonesOf(mainQuestion)
	var out []string
	for _, e := range designQuizGroupEdges(designQuizFamilyGroup(family)) {
		zones, _ := designQuizEdgeZonesOf(e)
		if len(zones) == 1 && slices.Contains(named, zones[0]) {
			continue
		}
		out = append(out, e+": different finish")
		if len(out) == designQuizMaxEdgeExceptions {
			break
		}
	}
	for _, pad := range []string{"other edges: different finish", "trims: different finish"} {
		if len(out) >= 2 {
			break
		}
		out = append(out, pad)
	}
	return out
}

// designQuizIsNoneOption — the client's "none — all the same" (any "none …" option).
func designQuizIsNoneOption(s string) bool {
	w := designQuizAliasWords(s)
	return len(w) > 0 && w[0] == "none"
}

// designQuizEdgeExceptionsLine — "edge exceptions: neckline: rib band; pocket openings: piping", or
// "edge exceptions: none" when only "none — all the same" was chosen; "" when nothing was answered.
func designQuizEdgeExceptionsLine(selected []string, free string) string {
	var items []string
	none := false
	for _, s := range selected {
		s = designOneLine(s)
		switch {
		case s == "":
		case designQuizIsNoneOption(s):
			none = true
		default:
			items = append(items, s)
		}
	}
	if free = designOneLine(free); free != "" {
		items = append(items, free)
	}
	switch {
	case len(items) > 0:
		return "edge exceptions: " + strings.Join(items, "; ")
	case none:
		return "edge exceptions: none"
	}
	return ""
}
