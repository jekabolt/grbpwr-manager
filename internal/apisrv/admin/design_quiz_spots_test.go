package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// 99-SPOTS: places in a picture question's picture.

func spotQ(id, key, picture, spots string) string {
	return `{"id":"` + id + `","decision_key":"` + key + `","picture":` + picture +
		`,"category":"details","part":"whole","kind":"single","question":"How are the inner strap and back edges of ` + id + ` finished?"` +
		`,"spots":` + spots + `,"options":["bound","turned and stitched"]}`
}

func spotRoles() map[int]entity.TechCardMediaRole {
	return map[int]entity.TechCardMediaRole{
		501: entity.TechCardMediaRoleTarget, 502: entity.TechCardMediaRoleDetail,
		503: entity.TechCardMediaRoleMaterial, 504: entity.TechCardMediaRoleMood,
	}
}

func TestQuizSpotsParseAndShapeGate(t *testing.T) {
	spots := `[
		{"label":"inner strap","x":420,"y":"310","scale":"detail"},
		{"label":"too close","x":430,"y":320,"scale":"zone"},
		{"label":"out of range","x":1200,"y":10},
		{"label":"far too many words here","x":100,"y":100},
		{"label":"","x":100,"y":100},
		{"label":"not a number","x":"left","y":100},
		{"label":"back edges","x":0.5,"y":0.25,"scale":"ZONE"},
		{"label":"hem","x":"900.4","y":1000,"scale":"huge"},
		{"label":"fourth","x":10,"y":10}
	]`
	raw := `{"questions":[` + spotQ("edges", "edge_finish", "1", spots) + `]}`
	qs, _, ok := parseDesignQuizBoard(raw, "top", nil, []int{502}, spotRoles())
	require.True(t, ok)
	require.Len(t, qs, 1)
	require.Equal(t, []entity.DesignQuizSpot{
		{Label: "inner strap", X: 420, Y: 310, Scale: "detail"},
		{Label: "back edges", X: 500, Y: 250, Scale: "zone"},
		{Label: "hem", X: 900, Y: 1000, Scale: "zone"},
	}, qs[0].Spots, "near duplicate, out of range, >4 words, empty, non-number dropped; fraction ×1000; unknown scale → zone; ≤3")

	// The spots are optional and never cost the question.
	for _, s := range []string{`null`, `"nope"`, `[]`, `[{"label":"x"}]`, `{"label":"a","x":1,"y":1}`} {
		raw := `{"questions":[` + spotQ("q", "edge_finish", "1", s) + `]}`
		qs, _, ok := parseDesignQuizBoard(raw, "top", nil, []int{502}, spotRoles())
		require.True(t, ok, s)
		require.Len(t, qs, 1, s)
		require.Empty(t, qs[0].Spots, s)
	}
}

func TestQuizSpotsRoleAndWholePictureGates(t *testing.T) {
	one := `[{"label":"inner strap","x":420,"y":310,"scale":"detail"}]`
	// Two batches: T72 keeps at most designQuizMaxPerPicture (2) questions per picture and 8 per run.
	raws := []string{
		`{"questions":[` + strings.Join([]string{
			spotQ("on_target", "pic_edges", "1", one),
			spotQ("on_detail", "pic_detail", "2", one),
			spotQ("on_material", "pic_fabric", "3", one),
			spotQ("on_mood", "pic_colour", "4", one),
			spotQ("on_unmarked", "pic_edges2", "5", one),
			spotQ("not_a_picture", "collar_type", "0", one),
		}, ",") + `]}`,
		`{"questions":[` + strings.Join([]string{
			spotQ("whole_match", "pic_match", "1", one),
			spotQ("match_a_part", "pic_match_collar", "1", one),
			spotQ("whole_material", "pic_material", "2", one),
		}, ",") + `]}`,
	}
	got := map[string]int{}
	for i, raw := range raws {
		qs, _, ok := parseDesignQuizBoard(raw, "top", nil, []int{501, 502, 503, 504, 505}, spotRoles())
		require.True(t, ok)
		require.Len(t, qs, []int{6, 3}[i])
		for _, q := range qs {
			got[q.ID] = len(q.Spots)
		}
	}
	require.Equal(t, map[string]int{
		// 06.10: the key is not a gate — a detail picture keeps its spots under any key.
		"on_detail": 1, "whole_material": 1,
		// 102 B3: no spots on ANY target-picture question.
		"on_target": 0, "match_a_part": 0, "whole_match": 0,
		"on_material": 0, "on_mood": 0, "on_unmarked": 0, "not_a_picture": 0,
	}, got)

	// No roles known (counted parse, no board): no spots at all.
	for _, raw := range raws {
		qs, _, _ := parseDesignQuizCounted(raw, "top", nil)
		for _, q := range qs {
			require.Empty(t, q.Spots, q.ID)
		}
	}
}

func TestQuizSpotAt(t *testing.T) {
	q := "How are the inner strap and back edges finished?"
	require.Equal(t, int32(12), designQuizSpotAt(q, "Inner Strap"))
	require.Equal(t, int32(-1), designQuizSpotAt(q, "inner strap edge"))
	require.Equal(t, int32(-1), designQuizSpotAt(q, ""))
	// UTF-16 offset (a JS string index): the em dash is 3 bytes, 1 code unit; 😀 is 4 bytes, 2 units.
	require.Equal(t, int32(10), designQuizSpotAt("Hem — and back neck?", "back neck"))
	require.Equal(t, int32(5), designQuizSpotAt("😀 a hem", "hem"))
}

func TestQuizSpotsSaveRoundTrip(t *testing.T) {
	in := []*pb_admin.DesignQuizAnswer{{
		Question: &pb_admin.DesignQuizQuestion{Id: "pic_edges", Category: "details", Part: "whole", Kind: "single",
			Question: "How are the inner strap and back edges finished?", Options: []string{"bound", "turned"},
			DecisionKey: "pic_501_edges", MediaId: 501, Spots: []*pb_admin.DesignQuizSpot{
				{Label: "inner strap", X: 420, Y: 310, Scale: "detail", At: 99},
				{Label: "back edges", X: 600, Y: 200, Scale: "zone"},
				{Label: "bad", X: -1, Y: 200},
				nil,
			}},
		Selected: []string{"bound"},
	}, {
		Question: &pb_admin.DesignQuizQuestion{Id: "collar", Category: "details", Part: "collar", Kind: "single",
			Question: "Collar?", Options: []string{"a", "b"},
			Spots: []*pb_admin.DesignQuizSpot{{Label: "collar", X: 500, Y: 100, Scale: "zone"}}},
		Selected: []string{"a"},
	}}
	out, _, ve := validateDesignQuizAnswers(in)
	require.Nil(t, ve)
	require.Equal(t, []entity.DesignQuizSpot{
		{Label: "inner strap", X: 420, Y: 310, Scale: "detail"},
		{Label: "back edges", X: 600, Y: 200, Scale: "zone"},
	}, out[0].Question.Spots, "echo kept through the shape gate; the client's at is ignored")
	require.Empty(t, out[1].Question.Spots, "no picture → no spots")

	pb := designQuizAnswersToPb(out)
	sp := pb[0].GetQuestion().GetSpots()
	require.Len(t, sp, 2)
	require.Equal(t, int32(12), sp[0].GetAt(), "at is recomputed by the server")
	require.Equal(t, int32(28), sp[1].GetAt())
	require.Equal(t, int32(420), sp[0].GetX())
	require.Equal(t, "detail", sp[0].GetScale())
	require.Nil(t, pb[1].GetQuestion().GetSpots())
}

func TestQuizSpotsPromptRule(t *testing.T) {
	require.Contains(t, designQuizSystemPrompt, "- "+designQuizSpotsRule)
	require.Contains(t, designQuizSystemPrompt, `"spots":[{"label":"…","x":0,"y":0,"scale":"zone|detail"}]`)
	for _, w := range []string{"only on a question about a DETAIL picture (never a target, material or mood picture)", "1 to 3 places IN THAT PICTURE", "0–1000 from the left edge as the viewer sees it",
		"Omit spots when the question is about the whole picture", "a wrong spot is worse than none"} {
		require.Contains(t, designQuizSpotsRule, w)
	}

	card := &entity.TechCard{QuizAnswers: []entity.TechCardQuizAnswer{{
		Question: entity.DesignQuizQuestion{ID: "pic_edges", Category: "details", Part: "whole",
			Question: "How are the edges finished?", Options: []string{"a", "b"}, MediaID: 501,
			Spots: []entity.DesignQuizSpot{{Label: "inner strap edge", X: 1, Y: 2, Scale: "detail"}, {Label: "back neckline", X: 500, Y: 500}}},
		Selected: []string{"a"},
	}}}
	card.Media = []entity.TechCardMediaItem{{MediaId: 501, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleTarget}}
	p := designQuizUserPrompt(card, nil, []int{501}, "top", "")
	require.Contains(t, p, "[pic_edges · details · whole · about picture 1 (target garment) — at the inner strap edge] How are the edges finished? → a")
}
