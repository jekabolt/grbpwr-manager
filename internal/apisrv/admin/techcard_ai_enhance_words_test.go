package admin

import (
	"testing"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/require"
)

// T03 (owner item 3): WORDS is the flat-sketch brief, and it is ALWAYS English. The client seeds it
// with EnhanceText{field: WORDS, mode: PROMPT, text: <moodboard concept>, context: <card facts>}; the
// answer is an English, comma-separated flat-sketch brief with no mood or story. Every other mode on
// WORDS is English too; every other field keeps the input's language.
// MUTATION: return the shared pair from enhanceTextFieldPieces for WORDS — red.
func TestEnhanceTextWordsIsAlwaysAnEnglishFlatBrief(t *testing.T) {
	const answer = "boxy hip-length work jacket, dropped shoulders, two-piece sleeves, front zip with storm flap"
	client, rec := newEnhanceFakeOR(t, enhanceReply(answer, "stop"))
	s := newEnhanceServer(t, client)

	for _, mode := range []pb_admin.EnhanceTextMode{
		pb_admin.EnhanceTextMode_ENHANCE_TEXT_MODE_PROMPT,
		pb_admin.EnhanceTextMode_ENHANCE_TEXT_MODE_IMPROVE,
	} {
		resp, err := s.EnhanceText(adminCtx("alice"), &pb_admin.EnhanceTextRequest{
			Text:    "Куртка рабочая, свободная, навеяна мастерскими 30-х; молния с ветрозащитной планкой.",
			Context: "category: outerwear › jackets\nfit: oversized",
			Mode:    mode, Field: pb_admin.EnhanceTextField_ENHANCE_TEXT_FIELD_WORDS,
		})
		require.NoError(t, err)
		require.Equal(t, answer, resp.GetText())
		c := rec.all()[len(rec.all())-1]
		require.Contains(t, c.System, enhanceLanguageEnglish)
		require.NotContains(t, c.System, "SAME LANGUAGE")
		require.Contains(t, c.System, "prompt = condense the TEXT")
		require.Contains(t, c.System, "technical flat sketches (black line drawings)")
		require.Contains(t, c.System, "leave out mood, story, inspiration")
		require.Contains(t, c.System, "materials only as they show in a line drawing")
		require.NotContains(t, c.System, "Куртка", "the text is data, it stays in the user turn")
	}

	// Positive control: another field keeps the input's language and the shared prompt definition.
	_, err := s.EnhanceText(adminCtx("bob"), &pb_admin.EnhanceTextRequest{
		Text: "заметка", Mode: pb_admin.EnhanceTextMode_ENHANCE_TEXT_MODE_PROMPT,
		Field: pb_admin.EnhanceTextField_ENHANCE_TEXT_FIELD_DESCRIPTION,
	})
	require.NoError(t, err)
	c := rec.all()[len(rec.all())-1]
	require.Contains(t, c.System, "Write in the SAME LANGUAGE as the input.")
	require.NotContains(t, c.System, "ENGLISH")
	require.Contains(t, c.System, "prompt = rewrite it as ONE image-generation prompt")
}

// T56: FABRIC RENDER › IN WORDS has its own brief — English, cloth/colour/drape/surface, no
// flat-sketch instructions. MUTATION: drop the RENDER_WORDS branch in enhanceTextFieldPieces — red.
func TestEnhanceTextRenderWordsIsAnEnglishFabricBrief(t *testing.T) {
	const answer = "boxy work jacket in heavy indigo cotton drill, stiff hand, faded wash, crisp folds"
	client, rec := newEnhanceFakeOR(t, enhanceReply(answer, "stop"))
	s := newEnhanceServer(t, client)

	resp, err := s.EnhanceText(adminCtx("alice"), &pb_admin.EnhanceTextRequest{
		Text:    "Куртка рабочая из плотного индиго, навеяна мастерскими 30-х.",
		Context: "category: outerwear › jackets",
		Mode:    pb_admin.EnhanceTextMode_ENHANCE_TEXT_MODE_PROMPT,
		Field:   pb_admin.EnhanceTextField_ENHANCE_TEXT_FIELD_RENDER_WORDS,
	})
	require.NoError(t, err)
	require.Equal(t, answer, resp.GetText())
	c := rec.all()[len(rec.all())-1]
	require.Contains(t, c.System, enhanceLanguageEnglish)
	require.Contains(t, c.System, "photoreal fabric render of the flats")
	require.Contains(t, c.System, "photoreal render of the garment's flats made up in real cloth")
	require.NotContains(t, c.System, "black line drawings")
	require.NotContains(t, c.System, "Куртка")
}
