package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// 102-QUICKWIN: the target picture's "what do we change", the detail picture's "what do we take".

func qwQ(id, key, picture, kind, question string, options ...string) string {
	return `{"id":"` + id + `","decision_key":"` + key + `","picture":` + picture +
		`,"category":"design","part":"whole","kind":"` + kind + `","question":"` + question + `"` +
		`,"spots":[{"label":"strap","x":400,"y":300,"scale":"zone"}]` +
		`,"options":["` + strings.Join(options, `","`) + `"]}`
}

func TestQuizQuickwinRuleText(t *testing.T) {
	for _, w := range []string{
		`target — "What do we change from picture N?" (decision_key pic_change, kind multi)`,
		`ACTIONABLE change, a verb or comparative plus the part ("narrower straps", "lower crossing point", "shallower open back"), never a bare noun ("strap width")`,
		`detail — "What do we take from picture N?" (decision_key pic_take, kind multi)`,
		"(pic_change, pic_take or a more specific aspect)",
	} {
		require.Contains(t, designQuizSystemPrompt, w)
	}
	require.NotContains(t, designQuizSystemPrompt, "which aspects of this garment to match exactly")
	require.Contains(t, designQuizSpotsRule, "only on a question about a DETAIL picture")
}

func TestQuizQuickwinTargetChange(t *testing.T) {
	roles := map[int]entity.TechCardMediaRole{
		501: entity.TechCardMediaRoleTarget, 502: entity.TechCardMediaRoleDetail, 503: entity.TechCardMediaRoleMaterial,
		504: entity.TechCardMediaRoleTarget, 505: entity.TechCardMediaRoleTarget, 506: entity.TechCardMediaRoleTarget,
		507: entity.TechCardMediaRoleTarget,
	}
	// picture: 1=501 2=504 3=505 4=506 5=507 (targets, one each: the forced key is per picture), 6=502 detail, 7=503 material.
	raw := `{"questions":[` + strings.Join([]string{
		// aspect match → change, single → multi, match prepended, spots dropped (target).
		qwQ("t_match", "pic_match", "1", "single", "Which aspects of picture 1 to match?", "narrower straps", "lower crossing point"),
		// text "what do we change", the model's own no-change twin deduped, 6 options → last dropped.
		qwQ("t_text", "pic_silhouette", "2", "multi", "What do we change from picture 1?",
			"No changes", "narrower straps", "lower crossing point", "shallower open back", "longer hem", "MATCH AS SHOWN — no changes"),
		// 6 real deltas: the 6th goes to make room.
		qwQ("t_cap", "pic_change", "3", "multi", "What else to change?", "a1", "a2", "a3", "a4", "a5", "a6"),
		// only a no-change twin + 1 delta: kept (2 options).
		qwQ("t_min", "pic_change", "4", "multi", "Change on the back?", "nothing", "deeper back"),
		// only no-change twins: dropped (fewer than 2 options).
		qwQ("t_none", "pic_change", "5", "multi", "Change anything?", "no change", "as shown"),
		// a part question on a target picture: untouched, but no spots.
		qwQ("t_part", "pic_strap_width", "1", "single", "How wide are the straps?", "1 cm", "2 cm"),
		// detail whole: multi, key kept, spots kept.
		qwQ("d_take", "pic_detail", "6", "single", "What do we take from picture 6?", "crossed back straps", "bound neckline"),
		qwQ("d_text", "pic_back", "6", "single", "What do we take for the back?", "x", "y"),
		// a material picture: nothing forced.
		qwQ("m", "pic_match", "7", "single", "What do we match from picture 7?", "fabric", "colour"),
	}, ",") + `]}`
	qs, st, ok := parseDesignQuizBoard(raw, "top", nil, []int{501, 504, 505, 506, 507, 502, 503}, roles)
	require.True(t, ok)
	require.Equal(t, 1, st.invalid, "t_none: no delta left")
	byID := map[string]entity.DesignQuizQuestion{}
	for _, q := range qs {
		byID[q.ID] = q
	}
	require.NotContains(t, byID, "t_none")

	q := byID["t_match"]
	require.Equal(t, entity.DesignQuizKindMulti, q.Kind)
	require.Equal(t, "pic_501_change", q.DecisionKey)
	require.Equal(t, []string{designQuizMatchAsShown, "narrower straps", "lower crossing point"}, q.Options)
	require.Empty(t, q.Spots, "no spots on a target picture")

	q = byID["t_text"]
	require.Equal(t, "pic_504_change", q.DecisionKey)
	require.Equal(t, []string{designQuizMatchAsShown, "narrower straps", "lower crossing point", "shallower open back", "longer hem"}, q.Options)

	q = byID["t_cap"]
	require.Equal(t, []string{designQuizMatchAsShown, "a1", "a2", "a3", "a4", "a5"}, q.Options)
	require.Len(t, q.Options, designQuizMaxOptions)
	require.Len(t, qs, 8)

	require.Equal(t, []string{designQuizMatchAsShown, "deeper back"}, byID["t_min"].Options)

	q = byID["t_part"]
	require.Equal(t, entity.DesignQuizKindSingle, q.Kind)
	require.Equal(t, "pic_501_strap_width", q.DecisionKey)
	require.Equal(t, []string{"1 cm", "2 cm"}, q.Options)
	require.Empty(t, q.Spots)

	for _, id := range []string{"d_take", "d_text"} {
		q = byID[id]
		require.Equal(t, entity.DesignQuizKindMulti, q.Kind, id)
		require.NotContains(t, q.Options, designQuizMatchAsShown, id)
		require.Len(t, q.Spots, 1, "a detail picture keeps its spots: %s", id)
	}
	require.Equal(t, "pic_502_detail", byID["d_take"].DecisionKey)

	q = byID["m"]
	require.Equal(t, entity.DesignQuizKindSingle, q.Kind)
	require.Equal(t, "pic_503_match", q.DecisionKey)
	require.Empty(t, q.Spots)
}

func TestQuizQuickwinContradictsShift(t *testing.T) {
	roles := map[int]entity.TechCardMediaRole{501: entity.TechCardMediaRoleTarget}
	raw := `{"questions":[{"id":"t","decision_key":"pic_change","picture":1,"category":"design","part":"whole","kind":"multi",` +
		`"question":"What do we change from picture 1?","options":[{"label":"narrower straps"},{"label":"add sleeves","contradicts_picture":true}],` +
		`"clarify":{"question":"Sleeves on a strappy top?","options":["yes, add them","no, keep it strappy"]}}]}`
	qs, _, ok := parseDesignQuizBoard(raw, "top", nil, []int{501}, roles)
	require.True(t, ok)
	require.Len(t, qs, 1)
	require.Equal(t, []string{designQuizMatchAsShown, "narrower straps", "add sleeves"}, qs[0].Options)
	require.Equal(t, []bool{false, false, true}, qs[0].Contradicts, "the flags move with their options")
}

func TestQuizQuickwinDecisionLines(t *testing.T) {
	q := entity.DesignQuizQuestion{ID: "t", Category: "design", Part: "whole", Kind: "multi",
		Question: "What do we change from picture 1?", DecisionKey: "pic_501_change", MediaID: 501,
		Options: []string{designQuizMatchAsShown, "narrower straps"}}
	card := &entity.TechCard{
		QuizAnswers: []entity.TechCardQuizAnswer{
			{Question: q, Selected: []string{designQuizMatchAsShown}},
			{Question: q, Selected: []string{designQuizMatchAsShown, "narrower straps"}},
			{Question: q, Selected: []string{"narrower straps"}, FreeText: "and a lower back"},
			{Question: func() entity.DesignQuizQuestion { g := q; g.MediaID = 999; return g }(), Selected: []string{designQuizMatchAsShown}},
			{Question: q, Selected: []string{designQuizMatchAsShown}, Skipped: true},
		},
	}
	card.Media = []entity.TechCardMediaItem{
		{MediaId: 700, Category: entity.TechCardMediaCategoryMoodboard},
		{MediaId: 501, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleTarget},
	}
	require.Equal(t, []string{
		"- picture 2 (target garment): match as shown, no changes",
		"- picture 2 (target garment) — What do we change from picture 1? → match as shown — no changes; narrower straps",
		`- picture 2 (target garment) — What do we change from picture 1? → narrower straps; own words: "and a lower back"`,
		"- a picture since removed from the board: match as shown, no changes",
	}, designQuizDecisionLines(card, false))
	require.Contains(t, designQuizDecisionsBlock(card), "  picture 2 (target garment): match as shown, no changes\n")
}
