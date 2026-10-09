package admin

import (
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// 96-PICTURE-QUESTIONS: per-picture questions anchor to a moodboard media id.

func picQ(id, key, part, question, picture string) string {
	return `{"id":"` + id + `","decision_key":"` + key + `","picture":` + picture +
		`,"category":"materials","part":"` + part + `","kind":"single","question":"` + question +
		`","options":["brushed cotton twill","washed nylon"]}`
}

func TestQuizPictureMapsToMediaID(t *testing.T) {
	attached := []int{501, 502, 503}
	raw := `{"questions":[` + strings.Join([]string{
		picQ("pic3_fabric", "pic_material", "collar", "What to take from picture 3?", `3`),
		picQ("pic1_match", "pic_3_match", "sleeve", "What to match from picture 1?", `"1"`),
		picQ("out_of_range", "pic_detail", "collar", "Which detail from picture 9?", `9`),
		picQ("not_integer", "pic_mood", "collar", "What mood from picture 1.5?", `1.5`),
		picQ("zero", "collar_type", "collar", "Which collar?", `0`),
	}, ",") + `]}`
	qs, _, ok := parseDesignQuizBoard(raw, "jacket", nil, attached, nil)
	require.True(t, ok)
	require.Len(t, qs, 5)

	require.Equal(t, 503, qs[0].MediaID)
	require.Equal(t, entity.DesignQuizPartWhole, qs[0].Part, "a picture question is about the whole garment")
	require.Equal(t, "pic_503_material", qs[0].DecisionKey)

	require.Equal(t, 501, qs[1].MediaID, "a numeric string is read")
	require.Equal(t, "pic_501_match", qs[1].DecisionKey, "the model's picture number never leaks into the key")

	for _, q := range qs[2:] {
		require.Zero(t, q.MediaID, q.ID)
		require.NotEqual(t, entity.DesignQuizPartWhole, q.Part, "kept as a normal question: %s", q.ID)
	}
	require.Equal(t, "collar_type", qs[4].DecisionKey)
	require.Equal(t, "pic_detail", qs[2].DecisionKey, "out of range: the model key stays as given")

	// No board: the counted parse never maps.
	qs, _, _ = parseDesignQuizCounted(raw, "jacket", nil)
	require.Zero(t, qs[0].MediaID)
}

func TestQuizPictureKeyRewrite(t *testing.T) {
	for _, c := range []struct{ key, id, want string }{
		{"pic_material", "x", "pic_7_material"},
		{"pic_12_fabric_hand", "x", "pic_7_fabric_hand"},
		{"picture2_detail", "x", "pic_7_detail"},
		{"silhouette", "x", "pic_7_silhouette"},
		{"pic", "pic3_collar", "pic_7_collar"},
		{"pic_3", "", "pic_7_point"},
		{"", "", "pic_7_point"},
		{strings.Repeat("a", 64), "", "pic_7_" + strings.Repeat("a", 58)},
	} {
		require.Equal(t, c.want, designQuizPictureKey(7, c.key, c.id), c.key)
	}
}

func TestQuizPictureDedupeAcrossRenumbering(t *testing.T) {
	// The answer was given when the picture was «picture 1»; the board now puts it third.
	saved := []entity.TechCardQuizAnswer{{
		Question: entity.DesignQuizQuestion{ID: "pic1_fabric", Category: "materials", Part: "whole",
			Question: "What to take from it?", Options: []string{"a", "b"}, DecisionKey: "pic_503_material", MediaID: 503},
		Selected: []string{"a"},
	}}
	raw := `{"questions":[` + picQ("pic3_fabric_again", "pic_material", "whole", "What fabric from picture 3?", `3`) + `]}`
	qs, st, ok := parseDesignQuizBoard(raw, "jacket", saved, []int{501, 502, 503}, nil)
	require.True(t, ok)
	require.Empty(t, qs)
	require.Equal(t, 1, st.repeated)
}

func TestQuizPicturePromptRule(t *testing.T) {
	// T72: at most ONE question per target or detail picture; a material or mood picture gets none.
	require.Contains(t, designQuizSystemPrompt, `Pictures: at most ONE question per target or detail picture, tagged "picture": N`)
	require.Contains(t, designQuizSystemPrompt, `"picture":0`)
	for _, w := range []string{"target — ", "detail — ", "A material or mood picture gets no question of its own", "decision_key pic_<aspect>"} {
		require.Contains(t, designQuizSystemPrompt, w)
	}
	require.NotContains(t, designQuizSystemPrompt, "at least ONE question about EACH")

	card := &entity.TechCard{
		QuizAnswers: []entity.TechCardQuizAnswer{
			{Question: entity.DesignQuizQuestion{ID: "pic_fabric", Category: "materials", Part: "whole",
				Question: "What to take from it?", Options: []string{"a", "b"}, MediaID: 503}, Selected: []string{"a"}},
			{Question: entity.DesignQuizQuestion{ID: "pic_gone", Category: "design", Part: "whole",
				Question: "What mood?", Options: []string{"a", "b"}, MediaID: 999}, Selected: []string{"b"}},
		},
	}
	card.Media = []entity.TechCardMediaItem{{MediaId: 503, Category: entity.TechCardMediaCategoryMoodboard, Role: entity.TechCardMediaRoleMaterial}}
	p := designQuizUserPrompt(card, nil, []int{501, 502, 503}, "jacket", "")
	require.Contains(t, p, "[pic_fabric · materials · whole · about picture 3 (material reference)] What to take from it? → a")
	require.Contains(t, p, "[pic_gone · design · whole · about a picture since removed from the board] What mood? → b")
}

func TestQuizPictureMediaIDRoundTrip(t *testing.T) {
	in := []*pb_admin.DesignQuizAnswer{{
		Question: &pb_admin.DesignQuizQuestion{Id: "pic_fabric", Category: "materials", Part: "collar", Kind: "single",
			Question: "What to take from it?", Options: []string{"a", "b"}, DecisionKey: "pic_503_material", MediaId: 503},
		Selected: []string{"a"},
	}, {
		Question: &pb_admin.DesignQuizQuestion{Id: "neg", Category: "design", Part: "collar", Kind: "single",
			Question: "Collar?", Options: []string{"a", "b"}, MediaId: -4},
		Selected: []string{"a"},
	}}
	out, _, ve := validateDesignQuizAnswers(in)
	require.Nil(t, ve)
	require.Equal(t, 503, out[0].Question.MediaID)
	require.Equal(t, entity.DesignQuizPartWhole, out[0].Question.Part)
	require.Zero(t, out[1].Question.MediaID)
	require.Equal(t, "collar", out[1].Question.Part)
	require.Equal(t, int32(503), designQuizAnswersToPb(out)[0].GetQuestion().GetMediaId())
}
