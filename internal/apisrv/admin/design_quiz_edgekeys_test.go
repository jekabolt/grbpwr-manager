package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// 91-EDGE-KEYS K1: the live5/live6 coined keys land on the canonical edge keys; non-finish neckline
// questions keep theirs.
func TestDesignQuizCanonicalEdgeKey(t *testing.T) {
	for _, c := range []struct {
		name, category, key, part, kind, question string
		options                                   []string
		want                                      string
	}{
		{"neck_rib (live6)", "details", "neck_rib", "neckline", "single", "Rib neckband width and depth?",
			[]string{"narrow rib, close crew", "medium rib, crew", "wide chunky rib, high crew"}, "neck_finish"},
		{"sleeve_opening (live5)", "details", "sleeve_opening", "cuff", "single", "How is the sleeve opening finished?",
			[]string{"coverstitch hem 406/602", "self-fabric band", "rib band", "raw edge"}, "sleeve_finish"},
		{"cuff_style with edge options (live6)", "details", "cuff_style", "cuff", "single", "How is the sleeve opening finished?",
			[]string{"coverstitch hem 406/602", "hem turned twice, 301", "self-fabric band", "rib band"}, "sleeve_finish"},
		{"cuff_style without edge options", "details", "cuff_style", "cuff", "single", "How do the cuffs close?",
			[]string{"one shank button", "two snaps on a tab", "two shank buttons"}, "cuff_style"},
		{"trim_edges (live6)", "details", "trim_edges", "closure", "single", "Front edge, cuff and pocket trim?",
			[]string{"velvet piped edge (piping)", "bound edge (binding) in self-fabric", "faced edge, no trim", "raw edge"}, "edge_finish_main"},
		{"rib_colour is not an edge question", "design", "rib_colour", "neckline", "single", "Neck rib colour?",
			[]string{"tone-on-tone with the body", "contrast colour rib"}, "rib_colour"},
		{"neck_tape is not an edge question", "details", "neck_tape", "neckline", "multi", "Which neck and shoulder reinforcements?",
			[]string{"shoulder seam tape", "back neck tape", "neck seam topstitched", "none"}, "neck_tape"},
		{"neckline shape keeps its key", "details", "neckline", "neckline", "single", "Which neckline?",
			[]string{"crew", "v-neck", "scoop"}, "neckline"},
		{"hem_finish stays", "details", "hem_finish", "hem", "single", "Body hem finish?",
			[]string{"coverstitch hem 406/602", "hem turned twice, 301", "raw edge"}, "hem_finish"},
		{"several zones → main", "details", "hem_finish", "hem", "single", "Finish of waistband hem, cuffs and pocket flaps?",
			[]string{"self-fabric band", "hem turned twice, 301", "faced edge", "raw edge"}, "edge_finish_main"},
		{"named by key only", "details", "pocket_opening", "pocket", "single", "How are they finished?",
			[]string{"turned", "clean"}, "pocket_edge_finish"},
		{"hood edge", "details", "hood_trim", "hood", "single", "Hood edge finish?",
			[]string{"bound edge (binding)", "drawcord casing", "faced edge"}, "hood_edge_finish"},
		{"exceptions multi (live6 extra_edges)", "details", "extra_edges", "whole", "multi", "Which edges are finished differently?",
			[]string{"hood edge: bound edge (binding)", "hood edge: drawcord casing", "front edge: faced edge"}, "edge_exceptions"},
		{"main seam never rewritten", "details", "main_seam", "side_seam", "single", "Main seam construction?",
			[]string{"bound seam", "Hong Kong finish (bias-bound edges)", "raw edge", "faced edge"}, "main_seam"},
		{"fit hem length untouched", "fit", "body_length", "hem", "single", "Where does the hem sit?",
			[]string{"hip", "mid-thigh"}, "body_length"},
	} {
		got := designQuizCanonicalEdgeKey(c.category, c.key, c.part, c.kind, c.question, c.options)
		require.Equal(t, c.want, got, c.name)
	}
}

// K1 in the parse: keys rewritten and counted, dedupe on the canonical key; K2: edge_finish_main
// keeps the model's pairs (up to 6) or gets the group fallback, never empty; a separate
// edge_exceptions question in the same batch is a repeat.
func TestDesignQuizParseEdgeKeys(t *testing.T) {
	raw := `{"questions":[
	 {"id":"neck_rib","decision_key":"neck_rib","category":"details","part":"neckline","kind":"single","question":"Rib neckband width and depth?","options":["narrow rib, close crew","medium rib, crew","wide chunky rib, high crew"]},
	 {"id":"neck_finish","decision_key":"neck_finish","category":"details","part":"neckline","kind":"single","question":"Neckline finish?","options":["rib band","self-fabric band"]},
	 {"id":"edges","decision_key":"edge_finish_main","category":"details","part":"whole","kind":"single","question":"Main finish of hem, sleeve openings and pocket openings?","options":["hem turned twice, 301","faced edge","raw edge"]},
	 {"id":"extra_edges","decision_key":"edge_exceptions","category":"details","part":"whole","kind":"multi","question":"Which edges are finished differently?","options":["hood edge: bound edge (binding)","front edge: faced edge"]},
	 {"id":"neck_tape","decision_key":"neck_tape","category":"details","part":"neckline","kind":"multi","question":"Which neck and shoulder reinforcements?","options":["shoulder seam tape","back neck tape"]}
	]}`
	qs, st, ok := parseDesignQuizCounted(raw, "jacket", nil)
	require.True(t, ok)
	require.Equal(t, []string{"neck_rib", "edges", "neck_tape"}, func() []string {
		var ids []string
		for _, q := range qs {
			ids = append(ids, q.ID)
		}
		return ids
	}())
	require.Equal(t, 1, st.keysFixed)
	require.Equal(t, 2, st.repeated, "neck_finish after neck_rib, edge_exceptions after edge_finish_main")
	require.Equal(t, "neck_finish", qs[0].DecisionKey)
	require.Equal(t, "neck_tape", qs[2].DecisionKey)
	main := qs[1]
	require.Equal(t, "Which edges are finished differently?", main.ClarifyQuestion)
	require.Equal(t, []string{"front edge: different finish", "neckline: different finish", "hood edge: different finish", "vents: different finish"}, main.ClarifyOptions,
		"group edges minus hem, sleeve and pocket openings named by the main question")

	raw = `{"questions":[{"id":"edges","decision_key":"edge_finish_main","category":"details","part":"whole","kind":"single","question":"Main edge finish?","options":["faced edge","raw edge"],
	 "clarify":{"question":"anything","options":["a: 1","b: 2","c: 3","d: 4","e: 5","f: 6","g: 7"]}}]}`
	qs, _, _ = parseDesignQuizCounted(raw, "tee", nil)
	require.Equal(t, "Which edges are finished differently?", qs[0].ClarifyQuestion)
	require.Equal(t, []string{"a: 1", "b: 2", "c: 3", "d: 4", "e: 5", "f: 6"}, qs[0].ClarifyOptions)

	// Every zone named: still never empty.
	require.Len(t, designQuizEdgeExceptionsFallback("briefs", "Finish of leg openings, waist, straps and neckline?"), 2)
}

// K2 at the door: edge_finish_main carries up to 6 follow-up options, the edge_exceptions answer up
// to 7 (6 + "none — all the same"); the decision lines read "edge exceptions: …" / "… none".
func TestDesignQuizEdgeExceptionsSaveAndLines(t *testing.T) {
	six := []string{"a: 1", "b: 2", "c: 3", "d: 4", "e: 5", "f: 6"}
	m := quizAnswer("edges", "single", []string{"faced edge", "raw edge"}, "faced edge")
	m.Question.DecisionKey = "edge_finish_main"
	m.Question.ClarifyQuestion, m.Question.ClarifyOptions = "Which edges are finished differently?", six
	x := quizAnswer("edge_exceptions_edges", "multi", append(append([]string{}, six...), "none — all the same"), "a: 1", "c: 3")
	x.Question.DecisionKey = "edge_exceptions"
	out, _, ve := validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{m, x})
	require.Nil(t, ve)
	require.Len(t, out, 2)

	m.Question.DecisionKey = "neck_finish"
	_, _, ve = validateDesignQuizAnswers([]*pb_admin.DesignQuizAnswer{m})
	require.NotNil(t, ve, "6 follow-up options only on edge_finish_main")

	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{
		{Question: entity.DesignQuizQuestion{ID: "edge_exceptions_edges", DecisionKey: "edge_exceptions", Category: "details", Part: "whole", Question: "Which edges are finished differently?"},
			Selected: []string{"neckline: rib band", "pocket openings: piping"}},
	}}
	require.Equal(t, []string{"- edge exceptions: neckline: rib band; pocket openings: piping"}, designQuizDecisionLines(card, false))
	card.QuizAnswers[0].Selected = []string{"none — all the same"}
	require.Equal(t, []string{"- edge exceptions: none"}, designQuizDecisionLines(card, false))
	card.QuizAnswers[0].FreeText = "hood edge: piping"
	require.Equal(t, []string{"- edge exceptions: hood edge: piping"}, designQuizDecisionLines(card, false))
}
