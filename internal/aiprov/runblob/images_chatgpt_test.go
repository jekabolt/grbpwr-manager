package runblob

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
)

// ═══ H5 — ChatGPT Images through the image transport ═══
//
// FIXTURES from runblob-specs/chatgpt-images.md (the docs pages read 2026-09-29): POST
// /v1/chatgpt-images/generate {prompt, model, quality (chatgpt-2.5 only), background (chatgpt-2.5
// only), aspect_ratio, images} → 201 {task_uuid, status, price}; GET /v1/chatgpt-images/generations/
// {task_uuid} → {task_uuid, status, prompt, result_image_url, message}. UNVERIFIED (G-06): every VALUE.

const chatgptSubmit = `{"task_uuid":"` + taskUUID + `","status":"pending","price":"0.0390"}`

func (s *imageStand) completedChatGPT() stand {
	return stand{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"completed","prompt":"p","result_image_url":"` + s.cdnURL() + `","message":null}`}
}

// TestImagesChatGPTGolden — gpt-5-2 end to end: the chatgpt-images path, `model` sent, NO quality and
// NO background (gpt-5-2 has neither dial: «silently ignored» — not sent), `images` with an http(s)
// and a data: url (the page: «mixing is allowed»), result_image_url read, the submit's price booked.
//
// MUTATION (measured red → green): `dials` ignored (quality sent on every chatgpt model) → the body
// golden red; the ChatGPT dialect refusing data: urls like Kling → the call is refused, red.
func TestImagesChatGPTGolden(t *testing.T) {
	s := newImageStand(t)
	s.submit = chatgptSubmit
	s.statuses = []stand{pendingGemini, s.completedChatGPT()}
	tr := imagesAt(s)
	require.True(t, tr.Serves(SlugChatGPTImage))
	require.True(t, tr.Serves(SlugChatGPTImage25))
	require.False(t, tr.Serves("chatgpt-images"), "the family alone is not a slug")

	res, err := tr.Generate(context.Background(), orimages.Request{
		Model: SlugChatGPTImage, Prompt: "a coat", Quality: "high", Background: "transparent", AspectRatio: "4:5",
		InputReferences: []string{"https://cdn.grbpwr.com/m/1.png", "data:image/png;base64,AAAA"},
	})
	require.NoError(t, err)
	require.Equal(t, "chatgpt-images/gpt-5-2", res.Model)
	require.Len(t, res.Images, 1)
	require.Equal(t, pngBytes(t), res.Images[0].Bytes)
	require.InDelta(t, 0.039, res.Usage.Cost, 1e-9)

	paths, _, bodies, auths := s.recorded()
	require.Equal(t, []string{"/v1/chatgpt-images/generate", "/v1/chatgpt-images/generations/" + taskUUID,
		"/v1/chatgpt-images/generations/" + taskUUID, "/cdn/out.png"}, paths)
	require.Equal(t, `{"aspect_ratio":"4:5","images":["https://cdn.grbpwr.com/m/1.png","data:image/png;base64,AAAA"],`+
		`"model":"gpt-5-2","prompt":"a coat"}`, bodies[0])
	require.Empty(t, auths[3], "no key on the download")
}

// TestImagesChatGPTBody — chatgpt-2.5's dials: `high` → sunburst, everything else → flare (the band's
// cheap-tier rule; sunburst is the page's default AND the dearer tier, so it is never left to the
// default); background sent only when it is one of the page's three words; aspect sent only from the
// page's nine ("auto" = not sent, the page's own rule).
//
// MUTATION (measured red → green): quality mapped `medium` → sunburst → the medium row red; the
// background enum check dropped → the "checkerboard" row red.
func TestImagesChatGPTBody(t *testing.T) {
	cases := []struct {
		name, slug, quality, background, aspect, want string
	}{
		{"2.5 high → sunburst", SlugChatGPTImage25, "high", "", "16:9",
			`{"aspect_ratio":"16:9","model":"chatgpt-2.5","prompt":"p","quality":"sunburst"}`},
		{"2.5 medium → flare, background opaque", SlugChatGPTImage25, "medium", "opaque", "",
			`{"background":"opaque","model":"chatgpt-2.5","prompt":"p","quality":"flare"}`},
		{"2.5 low → flare, background Transparent (case-folded)", SlugChatGPTImage25, "low", " Transparent ", "1:1",
			`{"aspect_ratio":"1:1","background":"transparent","model":"chatgpt-2.5","prompt":"p","quality":"flare"}`},
		{"2.5 auto/empty → flare; auto background; auto aspect not sent", SlugChatGPTImage25, "", "auto", "auto",
			`{"background":"auto","model":"chatgpt-2.5","prompt":"p","quality":"flare"}`},
		{"2.5: a background outside the enum and a ratio outside the nine are not sent", SlugChatGPTImage25, "HIGH", "checkerboard", "5:4",
			`{"model":"chatgpt-2.5","prompt":"p","quality":"sunburst"}`},
		{"gpt-5-2 has no dials", SlugChatGPTImage, "low", "opaque", "21:9",
			`{"aspect_ratio":"21:9","model":"gpt-5-2","prompt":"p"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newImageStand(t)
			s.submit, s.statuses = chatgptSubmit, []stand{s.completedChatGPT()}
			_, err := imagesAt(s).Generate(context.Background(), orimages.Request{Model: tc.slug, Prompt: "p",
				Quality: tc.quality, Background: tc.background, AspectRatio: tc.aspect})
			require.NoError(t, err)
			_, _, bodies, _ := s.recorded()
			require.Equal(t, tc.want, bodies[0])
		})
	}
}

// TestImagesChatGPTRefusals — nine references (the page's ceiling is eight) are refused before the
// wire; eight pass. A failed task (OPENAI_DECLINED, refunded by the page's rule) is free and terminal.
func TestImagesChatGPTRefusals(t *testing.T) {
	nine := make([]string, 9)
	for i := range nine {
		nine[i] = "https://a/" + strings.Repeat("x", i+1)
	}
	s := newImageStand(t)
	s.submit, s.statuses = chatgptSubmit, []stand{s.completedChatGPT()}
	tr := imagesAt(s)

	_, err := tr.Generate(context.Background(), orimages.Request{Model: SlugChatGPTImage25, Prompt: "p", InputReferences: nine})
	ce := callErr(t, err)
	require.Equal(t, aiprov.CodeBadRequest, ce.Code)
	require.False(t, ce.Engaged)
	require.Equal(t, "runblob: 9 reference pictures in one call, and chatgpt-images/chatgpt-2.5 takes at most 8", err.Error())
	paths, _, _, _ := s.recorded()
	require.Empty(t, paths)

	_, err = tr.Generate(context.Background(), orimages.Request{Model: SlugChatGPTImage25, Prompt: "p", InputReferences: nine[:8]})
	require.NoError(t, err)

	f := newImageStand(t)
	f.submit = chatgptSubmit
	f.statuses = []stand{{http.StatusOK, `{"task_uuid":"` + taskUUID + `","status":"failed","prompt":"p","result_image_url":null,"message":"OPENAI_DECLINED"}`}}
	_, err = imagesAt(f).Generate(context.Background(), orimages.Request{Model: SlugChatGPTImage, Prompt: "p"})
	ce = callErr(t, err)
	require.False(t, ce.Engaged, "refunded: free")
	require.False(t, ce.Retryable)
	require.Contains(t, err.Error(), "OPENAI_DECLINED")
}
