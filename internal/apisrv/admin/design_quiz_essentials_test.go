package admin

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// T72 (owner 06.10, «только то, что реально важно»): a HARD cap of designQuizMaxQuestions per run —
// when the model overshoots, the parser keeps the best by tier (fit → construction → fabric →
// colourways → pictures → the rest), at most designQuizMaxPerPicture per picture, in the model's
// order; rechecks and clarifies always survive.
func TestDesignQuizSelectEssentials(t *testing.T) {
	require.Equal(t, 8, designQuizMaxQuestions)
	require.Contains(t, designQuizSystemPrompt, "a run is at most "+designQuizMaxQuestionsWord+" questions")
	require.Contains(t, designQuizUserPrompt(nil, nil, nil, "jacket", ""), "at most 8 questions, usually 3 to 6")

	keyed := func(id, cat, key string) string {
		return `{"id":"` + id + `","decision_key":"` + key + `","category":"` + cat + `","part":"whole","kind":"single",` +
			`"question":"` + id + `?","options":["a","b"]}`
	}
	pic := func(id, key, n string) string {
		return `{"id":"` + id + `","decision_key":"` + key + `","picture":` + n + `,"category":"design","part":"whole","kind":"multi",` +
			`"question":"` + id + `?","options":["a","b"]}`
	}
	// The model's order is the owner's 16-question session: fluff first, essentials late.
	raw := `{"questions":[` + strings.Join([]string{
		keyed("fit_basis", "fit", "fit_basis"),                    // other → dropped
		keyed("layering", "fit", "layering"),                      // other → dropped
		keyed("chest_room", "fit", "chest_room"),                  // 1
		pic("p1_shoulder", "shoulder_build", "1"),                 // 1 (aspect)
		pic("p1_hem", "body_length", "1"),                         // 1 (aspect) — picture 1 full
		pic("p1_sleeve", "sleeve_length", "1"),                    // over the per-picture cap → dropped
		keyed("seams_visible", "details", "seams_visible"),        // other → dropped
		keyed("topstitch", "finish", "topstitch"),                 // other → dropped
		keyed("closure", "details", "closure_type"),               // 2
		keyed("pockets", "details", "pocket_style"),               // 2
		keyed("fabric", "materials", "shell_fabric"),              // 3
		keyed("colourway_count", "design", "colourway_count"),     // 4
		keyed("colourway_colours", "design", "colourway_colours"), // 4
		pic("p2_change", "pic_change", "2"),                       // 5 — 9th by tier → dropped
		keyed("care", "use", "care"),                              // other → dropped
	}, ",") + `]}`
	roles := map[int]entity.TechCardMediaRole{501: entity.TechCardMediaRoleTarget, 502: entity.TechCardMediaRoleTarget}
	qs, st, ok := parseDesignQuizBoard(raw, "jacket", nil, []int{501, 502}, roles)
	require.True(t, ok)
	var ids []string
	for _, q := range qs {
		ids = append(ids, q.ID)
	}
	require.Equal(t, []string{"chest_room", "p1_shoulder", "p1_hem", "closure", "pockets", "fabric", "colourway_count", "colourway_colours"}, ids,
		"the 8 essentials, in the model's order")
	require.Equal(t, 15, st.raw)
	require.Equal(t, 8, st.kept)
	require.Equal(t, 7, st.capped)

	// A recheck_ of a stale answer and a clarify_ of a contradicted one outrank everything and go first.
	saved := []entity.TechCardQuizAnswer{{Question: entity.DesignQuizQuestion{ID: "old_hem", DecisionKey: "hem_finish",
		Category: "details", Part: "hem", Question: "Old hem?"}, Selected: []string{"a"}, Stale: true}}
	raw = `{"questions":[` + strings.Join([]string{
		keyed("chest_room", "fit", "chest_room"), keyed("body_length", "fit", "body_length"),
		keyed("shoulder_build", "fit", "shoulder_build"), keyed("closure", "details", "closure_type"),
		keyed("pockets", "details", "pocket_style"), keyed("fabric", "materials", "shell_fabric"),
		keyed("colourway_count", "design", "colourway_count"), keyed("colourway_colours", "design", "colourway_colours"),
		keyed("recheck_old_hem", "details", "hem_finish"),
		keyed("clarify_closure", "details", "closure_type"),
	}, ",") + `]}`
	qs, st, ok = parseDesignQuizCounted(raw, "jacket", saved)
	require.True(t, ok)
	require.Len(t, qs, 8)
	require.Equal(t, "recheck_old_hem", qs[0].ID, "a recheck is fronted")
	require.Equal(t, "clarify_closure", qs[len(qs)-1].ID, "a clarify keeps the model's slot")
	require.Equal(t, 2, st.capped, "two essentials of the lowest tier gave way")
	for _, q := range qs {
		require.NotContains(t, []string{"colourway_count", "colourway_colours"}, q.ID, "the last tier went first")
	}

	// Under the cap nothing is trimmed and the order is the model's.
	raw = `{"questions":[` + keyed("care", "use", "care") + "," + keyed("chest_room", "fit", "chest_room") + `]}`
	qs, st, _ = parseDesignQuizCounted(raw, "jacket", nil)
	require.Equal(t, []string{"care", "chest_room"}, []string{qs[0].ID, qs[1].ID})
	require.Equal(t, 0, st.capped)

	// Past the scratch bound nothing is read.
	var items []string
	for i := 0; i < designQuizMaxParsed+3; i++ {
		items = append(items, keyed(fmt.Sprintf("q_%d", i), "use", fmt.Sprintf("care_%d", i)))
	}
	_, st, _ = parseDesignQuizCounted(`{"questions":[`+strings.Join(items, ",")+`]}`, "jacket", nil)
	require.Equal(t, designQuizMaxParsed+3, st.raw)
	require.Equal(t, designQuizMaxQuestions, st.kept)
	require.Equal(t, designQuizMaxParsed+3-designQuizMaxQuestions, st.capped)
}
