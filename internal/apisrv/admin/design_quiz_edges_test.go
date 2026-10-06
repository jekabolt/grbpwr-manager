package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// 90-EDGES: the 8 edge finishes resolve by their aliases, read back as "edge: <name>", and the
// prompt walks every open edge with its decision keys.
func TestDesignQuizEdgeKinds(t *testing.T) {
	for label, want := range map[string]string{
		"rolled hem (baby hem)":          "sm_hem_rolled",
		"narrow hem on the sleeve":       "sm_hem_rolled",
		"pin hems":                       "sm_hem_rolled",
		"piped edge (piping)":            "sm_piping",
		"cord piping on the pockets":     "sm_piping",
		"rib band 2 cm":                  "sm_rib_band",
		"ribbing at the cuffs":           "sm_rib_band",
		"neckline: rib band":             "sm_rib_band",
		"self-fabric band":               "sm_self_band",
		"neckband":                       "sm_self_band",
		"elastic casing":                 "sm_casing_elastic",
		"elastic waist, 3 cm":            "sm_casing_elastic",
		"drawcord casing":                "sm_casing_drawcord",
		"drawstring channel":             "sm_casing_drawcord",
		"overlocked edge":                "sm_edge_overlocked",
		"serged edges":                   "sm_edge_overlocked",
		"merrow edge":                    "sm_edge_overlocked",
		"lettuce edge":                   "sm_hem_lettuce",
		"lettuce hems":                   "sm_hem_lettuce",
		"self-fabric binding":            "sm_hem_bound", // binding stays sm_hem_bound
		"bias tape":                      "sm_hem_bound",
		"plain seam overlocked together": "sm_plain_overlock",
		"raw edge":                       "sm_hem_raw",
		"seam allowance 1 cm":            "",
	} {
		require.Equal(t, want, designQuizSeamOf(label), label)
	}
	n := 0
	for _, sm := range designQuizSeams {
		require.Equal(t, sm.key, designQuizSeamOf(sm.name), sm.name)
		if designQuizEdgeKeys[sm.key] {
			n++
			require.Equal(t, "edge: "+sm.name, designQuizPartLabel(sm.key))
			got, _ := designQuizResolvePart("tee", sm.key, "", "")
			require.Equal(t, sm.key, got)
		}
	}
	require.Equal(t, 8, n, "every edge key is in designQuizSeams")
	require.Len(t, designQuizEdgeKeys, 8)
	require.Equal(t, "seam: French seam", designQuizPartLabel("sm_french"))
	require.Equal(t, "seam: bound edge (binding)", designQuizPartLabel("sm_hem_bound"))

	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{{Question: entity.DesignQuizQuestion{
		ID: "rib", Category: "details", Part: "sm_rib_band", Question: "Rib band width?", Options: []string{"2 cm", "3 cm"}},
		Selected: []string{"2 cm"}}}}
	lines := designQuizDecisionLines(card, false)
	require.Len(t, lines, 1)
	require.True(t, strings.HasPrefix(lines[0], "- edge: rib band — "), lines[0])

	// T72: the edge walk is gone — ONE construction-finish question at most (main seam OR
	// edge_finish_main with its exceptions follow-up), only when the finish IS the visible design.
	for _, s := range []string{
		"the finish of every edge",
		"decision_key edge_finish_main", "IS the edge_exceptions decision",
		"Which edges are finished differently?",
		"armhole_finish, sleeve_finish, front_edge_finish, waistband_finish, leg_finish, pocket_edge_finish, vent_finish, hood_edge_finish",
		"rolled hem (baby hem) · piped edge (piping) · rib band · self-fabric band · elastic casing · drawcord casing · overlocked edge · lettuce edge",
		"never both, never two questions about stitching",
	} {
		require.Contains(t, designQuizSystemPrompt, s)
	}
	user := designQuizUserPrompt(nil, nil, nil, "tee", "")
	require.Contains(t, user, "Seam and edge part keys")
	require.Contains(t, user, "sm_rib_band (rib band)")
	require.Contains(t, user, "sm_hem_lettuce (lettuce edge)")
}
