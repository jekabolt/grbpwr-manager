package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// 70-SEAMS D2: sm_ seam constructions and col_palette resolve as parts, read back as words, and the
// colourway decisions travel to the construction draft as ONE brief before "Slots to colour".
func TestDesignQuizSeamPartResolve(t *testing.T) {
	for _, c := range []struct{ family, part, id, question, want string }{
		{"shirt", "French seam", "", "", "sm_french"},
		{"jacket", "flat-felled", "felled_topstitch", "Topstitch width on the flat-felled seams?", "sm_flat_felled"},
		{"shirt", "seam", "main_seam", "Main seam construction for the body?", "side_seam"},
		{"dress", "seam", "main_seam", "Main seam construction for the body?", "whole"},
		{"coat", "colourways", "", "", "col_palette"},
		{"tee", "neckline", "neck_binding", "", "neckline"},
		{"tee", "binding", "binding_width", "", "sm_hem_bound"},
		{"tee", "sm_safety", "", "", "sm_safety"},
		{"trousers", "seam allowance", "", "", "side_seam"}, // the blocker keeps it off the sm_ keys
		{"jacket", "button", "", "", "hw_button"},
	} {
		got, _ := designQuizResolvePart(c.family, c.part, c.id, c.question)
		require.Equal(t, c.want, got, "%s %q %q", c.family, c.part, c.id)
	}
	view, ok := designQuizPartView("tee", "sm_hong_kong")
	require.True(t, ok)
	require.Equal(t, entity.DesignQuizViewFront, view)
	_, ok = designQuizPartView("bag", designQuizPaletteKey)
	require.True(t, ok)
}

func TestDesignQuizSeamOf(t *testing.T) {
	for label, want := range map[string]string{
		"Hong Kong finish (bias-bound edges)":       "sm_hong_kong",
		"plain seam overlocked together":            "sm_plain_overlock",
		"plain seam pressed open, edges overlocked": "sm_plain_open",
		"bound neckline":                            "sm_hem_bound",
		"bound pocket bags":                         "",
		"binding tape seam":                         "sm_bound",
		"safety stitch 516":                         "sm_safety",
		"flatlock 607":                              "sm_flatlock",
		"coverstitch 406, 2.5 cm":                   "sm_hem_cover",
		"mock flat-fell, topstitched to one side":   "sm_mock_felled",
		"flat-felled yoke and armhole":              "sm_flat_felled",
		"taped seams throughout":                    "sm_taped",
		"seam allowance 1 cm":                       "",
		"corduroy":                                  "",
		"tape measure":                              "",
	} {
		require.Equal(t, want, designQuizSeamOf(label), label)
	}
	// every seam's canonical name resolves to itself
	for _, sm := range designQuizSeams {
		require.Equal(t, sm.key, designQuizSeamOf(sm.name), sm.name)
	}
}

func TestDesignQuizSeamHumaniserAndPrompts(t *testing.T) {
	require.Equal(t, "seam: French seam", designQuizPartLabel("sm_french"))
	require.Equal(t, "colourways", designQuizPartLabel("col_palette"))
	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{{Question: entity.DesignQuizQuestion{
		ID: "w", Category: "details", Part: "sm_flat_felled", Question: "Topstitch width?", Options: []string{"6 mm", "8 mm"}},
		Selected: []string{"6 mm"}}}}
	lines := designQuizDecisionLines(card, false)
	require.Len(t, lines, 1)
	require.True(t, strings.HasPrefix(lines[0], "- seam: flat-felled — "), lines[0])

	shirt := designQuizUserPrompt(nil, nil, nil, "shirt", "")
	require.Contains(t, shirt, "sm_hong_kong")
	require.Contains(t, shirt, "Colour part key: col_palette.")
	require.Contains(t, shirt, " · seams: main construction")
	require.Contains(t, shirt, " · colourways: how many")
	require.NotContains(t, shirt, "knit seams")
	require.Contains(t, designQuizUserPrompt(nil, nil, nil, "tee", ""), "knit seams: 514 / 516 / 607")
	require.Contains(t, designQuizSystemPrompt, "SEAMS AND INSIDE FINISH")
	require.Contains(t, designQuizSystemPrompt, "main_seam, extra_seams, neck_finish")
	require.Contains(t, designQuizSystemPrompt, "label_set")
}

func TestDesignQuizColourwayBrief(t *testing.T) {
	ans := func(key string, sel []string, skipped, stale bool) entity.TechCardQuizAnswer {
		return entity.TechCardQuizAnswer{Question: entity.DesignQuizQuestion{ID: key, DecisionKey: key,
			Category: "design", Part: "col_palette", Question: "?", Options: sel},
			Selected: sel, Skipped: skipped, Stale: stale}
	}
	card := draftBindCard()
	card.QuizAnswers = []entity.TechCardQuizAnswer{
		ans("colourway_colours", []string{"black", "bone"}, false, false),
		ans("colourway_count", []string{"two"}, false, false),
		ans("thread_colour", []string{"contrast"}, true, false),
		ans("hardware_finish", []string{"antique brass"}, false, true),
		ans("main_seam", []string{"French seam"}, false, false),
	}
	brief := designQuizColourwayBrief(card)
	require.Equal(t, "Colourway brief — decided with the designer: count two; main colours black, bone", brief)
	require.Empty(t, designQuizColourwayBrief(&entity.TechCard{}))

	card.BomItems = []entity.TechCardBomItem{{Section: entity.BomSectionFabric, Name: "SLOTNAME-main"}}
	prompt := designConstructionUserPrompt(card, designMoodSnapshot(card), []int{11, 22, 33}, draftProbeColours())
	at, slots := strings.Index(prompt, brief), strings.Index(prompt, "\nSlots to colour")
	require.True(t, at > 0 && slots > at, "brief before Slots to colour")
	require.Contains(t, designConstructionSystemPrompt, "When the prompt carries a Colourway brief")
	require.Contains(t, designConstructionSystemPrompt, `ONE aspect with key "seams"`)
	require.Contains(t, designConstructionAspectKeys, "seams")
}
