package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/aiprovtest"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/registry"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// newTestRouter is the chat door every handler test rig builds from its fake OpenRouter client: one fixed
// OpenRouter candidate over the client's OWN transport (the configured *oaichat.Client app.go
// registers, not a rebuilt one), with the client's env slugs as the default table — so a rig's
// openrouter.Config{Model, AnalysisModel, IdeasModel, HTTPTimeout} reaches the wire exactly as before.
// A nil client is a Server with no router (every AI door refuses «not configured»).
func newTestRouter(client *openrouter.Client) *router.Router {
	if client == nil {
		return nil
	}
	return router.NewSingle(entity.AIProviderOpenRouter, client.Transport(), "",
		router.WithDefaults(AIRouterDefaults(client)))
}

// seedCfgStore is a dependency.AI whose config half answers the 0373 seed: openrouter on (its env
// key), every purpose routed to it with no model. The registry-backed router over it is the one
// app.go builds, breakers included — the only kind that can be PAUSED.
type seedCfgStore struct {
	*aiprovtest.Store
	cfg entity.AIConfig
}

func (s *seedCfgStore) GetConfig(context.Context) (*entity.AIConfig, error) {
	c := s.cfg
	return &c, nil
}

func (s *seedCfgStore) ConfigVersion(context.Context) (uint64, error) {
	return s.cfg.Settings.ConfigVersion, nil
}

// newSeededRouter is app.go's router over the 0373 seed: registry, breakers, the client's own transport.
// The registry's env key follows the client's: a client with no key is a deployment with no
// OPENROUTER_API_KEY, which the registry drops before the router sees the candidates — exactly as in
// production, where the client reads its key through the registry's KeyFunc.
func newSeededRouter(t *testing.T, client *openrouter.Client) *router.Router {
	t.Helper()
	var cfg entity.AIConfig
	for _, k := range entity.AIProviderKeys() {
		cfg.Providers = append(cfg.Providers, entity.AIProvider{Key: k, Label: k, Enabled: k == entity.AIProviderOpenRouter})
	}
	for _, p := range entity.AIPurposes() {
		cfg.Routes = append(cfg.Routes, entity.AIRoute{Purpose: p,
			Candidates: []entity.AIRouteCandidate{{Position: 1, ProviderKey: entity.AIProviderOpenRouter}}})
	}
	cfg.Settings = entity.AISettings{ConfigVersion: 1,
		DefaultChatProviderKey: entity.AIProviderOpenRouter, DefaultImageProviderKey: entity.AIProviderOpenRouter}
	cfg.BudgetTimezone = "Europe/Warsaw"
	var env registry.EnvKeys
	if client.Enabled() {
		env.OpenRouter = "test-key"
	}
	reg := registry.New(&seedCfgStore{Store: &aiprovtest.Store{}, cfg: cfg}, nil, env)
	require.NoError(t, reg.Reload(context.Background()))
	return router.New(reg, nil, map[string]aiprov.Chatter{entity.AIProviderOpenRouter: client.Transport()},
		AIRouterDefaults(client), client.CompletionBase())
}

// TestEveryChatDoorSaysPausedWhenTheBreakerHoldsTheProvider — three transient faults open the
// provider's breaker; until its window passes every chat door refuses in ONE sentence, «paused …
// try again in a few minutes» (Unavailable, AI_PAUSED), never «not configured»: nothing about the
// deployment is wrong, and the person is told the truth about when to press again. Nobody is called.
//
// MUTATION (measured red): aiOffRefusal without the Paused branch → every door says «not configured».
// MUTATION (measured red): aiPausedRefusal with FailedPrecondition → the code assertion.
func TestEveryChatDoorSaysPausedWhenTheBreakerHoldsTheProvider(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	client := openrouter.New(openrouter.Config{APIKey: "test-key", BaseURL: srv.URL})
	ai := newSeededRouter(t, client)

	require.True(t, ai.Enabled(entity.AIPurposeNoteMarkdown), "precondition: the seeded route is callable")
	for range 3 {
		_, err := ai.Chat(context.Background(), entity.AIPurposeNoteMarkdown, aiprov.ChatRequest{User: "x"})
		require.ErrorIs(t, err, aiprov.ErrAllCandidatesFailed)
	}
	require.True(t, ai.Paused(entity.AIPurposeNoteMarkdown), "precondition: the breaker holds openrouter")
	before := calls.Load()

	enhance := newEnhanceServer(t, client)
	enhance.ai = ai
	note := newNoteFormatServer(client)
	note.ai = ai
	suggest := newSuggestServer(t, client)
	suggest.ai = ai
	doors := map[string]func() error{
		"EnhanceText": func() error {
			_, err := enhance.EnhanceText(adminCtx("alice"), noteImprove("a note"))
			return err
		},
		"FormatLibraryNoteMarkdown": func() error {
			_, err := note.FormatLibraryNoteMarkdown(adminCtx("alice"),
				&pb_admin.FormatLibraryNoteMarkdownRequest{Content: "a note"})
			return err
		},
		"SuggestPrompts": func() error {
			_, err := suggest.SuggestPrompts(adminCtx("alice"), tryOnPose("x"))
			return err
		},
		"DraftDesignIdea": func() error {
			_, err := (&Server{ai: ai, designGenerationEnabled: true}).DraftDesignIdea(designRunCtx(),
				&pb_admin.DraftDesignIdeaRequest{TechCardId: 7, ClientRequestId: "66666666-6666-6666-6666-666666666666"})
			return err
		},
		"AutoTranslateEmailCampaign": func() error {
			_, err := (&Server{ai: ai}).AutoTranslateEmailCampaign(adminCtx("alice"),
				&pb_admin.AutoTranslateEmailCampaignRequest{Id: 1})
			return err
		},
	}
	for name, press := range doors {
		t.Run(name, func(t *testing.T) {
			err := press()
			require.Equal(t, codes.Unavailable, status.Code(err), "%v", err)
			require.Equal(t, "the AI provider is paused after repeated failures; try again in a few minutes",
				status.Convert(err).Message())
			require.Equal(t, aiReasonPaused, aiReasonOf(t, err))
		})
	}
	require.Equal(t, before, calls.Load(), "a paused provider is not called by any door")
}

// TestAIFaultWordsAreBuiltFromTheFields — the sentence a person reads about a failed call names the
// provider, the fault and a refusal's status from the CallError's FIELDS; the transport's own text
// (which may echo the prompt) and the router's "ai: every candidate failed" never lead it.
//
// MUTATION (measured red): aiFaultWords returns err.Error() → red. MUTATION: providerHTTPStatus
// without the 2xx exclusion → red (a 200 that broke is not "(HTTP 200)").
func TestAIFaultWordsAreBuiltFromTheFields(t *testing.T) {
	exhausted := func(ce *aiprov.CallError) error {
		return &wrapErr{msg: "ai: every candidate failed: " + ce.Error(), err: ce}
	}
	for name, tc := range map[string]struct {
		err  error
		want string
	}{
		"refusal with a status": {exhausted(&aiprov.CallError{Provider: "openrouter", Code: aiprov.CodeRateLimited,
			HTTPStatus: 429, Err: errText("openrouter: API error (HTTP 429): PROMPT-ECHO")}), "openrouter: rate limited (HTTP 429)"},
		"a 2xx that broke": {exhausted(&aiprov.CallError{Provider: "openrouter", Code: aiprov.CodeEmptyAnswer,
			HTTPStatus: 200, Engaged: true, Err: errText("PROMPT-ECHO")}), "openrouter: empty answer"},
		"no code, no provider": {&aiprov.CallError{Err: errText("PROMPT-ECHO")}, "the provider: call failed"},
		"paused":               {router.ErrPaused, aiPausedMsg},
		"deadline":             {context.DeadlineExceeded, "the call ran out of time"},
		"not a call error":     {errText("PROMPT-ECHO"), "the provider did not answer"},
	} {
		t.Run(name, func(t *testing.T) {
			got := aiFaultWords(tc.err)
			require.Equal(t, tc.want, got)
			require.NotContains(t, got, "PROMPT-ECHO")
			require.NotContains(t, got, "every candidate failed")
		})
	}
	require.Zero(t, providerHTTPStatus(&aiprov.CallError{HTTPStatus: 200}), "a 2xx is not a refusal")
	require.Equal(t, 502, providerHTTPStatus(exhausted(&aiprov.CallError{HTTPStatus: 502})))
	require.True(t, isEmptyModelAnswer(exhausted(&aiprov.CallError{Code: aiprov.CodeEmptyAnswer})))
	require.False(t, isEmptyModelAnswer(errText("openrouter: empty message")), "the wording is not the code")
}

type errText string

func (e errText) Error() string { return string(e) }

type wrapErr struct {
	msg string
	err error
}

func (w *wrapErr) Error() string { return w.msg }
func (w *wrapErr) Unwrap() error { return w.err }

// legacyReplay reads the system prompt, the user text, JSON mode and the ceiling back out of a
// request body the router sent, so the pre-B-18 entry point can be asked the same question.
func legacyReplay(t *testing.T, raw string) (sys, user string, jsonMode bool, maxTokens int) {
	t.Helper()
	var body struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		MaxTokens      int `json:"max_tokens"`
		ResponseFormat *struct {
			Type string `json:"type"`
		} `json:"response_format"`
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &body))
	for _, m := range body.Messages {
		switch m.Role {
		case "system":
			require.NoError(t, json.Unmarshal(m.Content, &sys))
		case "user":
			var parts []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}
			require.NoError(t, json.Unmarshal(m.Content, &parts),
				"the user turn is not a parts array: %s", m.Content)
			require.Len(t, parts, 1, "no pictures: ONE text part")
			user = parts[0].Text
		}
	}
	return sys, user, body.ResponseFormat != nil && body.ResponseFormat.Type == "json_object", body.MaxTokens
}

// TestThePictureDoorsSendThePreB18BytesWithoutPictures (FIX-G2) — a word-only moodboard draft (prose
// and structured) and an Ideas click with no media send, through the router, EXACTLY the bytes the
// pre-B-18 entry points send for the same question (openrouter.CompleteWithImages /
// CompleteWithImagesOn — still in the package, and what the doors called before the cutover): the
// user turn as a one-element parts array, `[{"type":"text",…}]`, not a plain string.
//
// MUTATION (measured red): DraftDesignIdea / SuggestPrompts without UserAsParts → legacyReplay finds a
// plain-string user turn; oaichat.Chat ignoring the flag → the same.
func TestThePictureDoorsSendThePreB18BytesWithoutPictures(t *testing.T) {
	words := designMoodCard()
	words.Media, words.Callouts = nil, nil // the words stay (MoodNote); no picture reaches the wire

	for _, tc := range []struct {
		name string
		req  *pb_admin.DraftDesignIdeaRequest
	}{{"draft, prose", draftRequest()}, {"draft, structured", draftConstructionRequest()}} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newDraftRigWithCard(t, http.StatusOK, constructionAnswer, words, nil, nil)
			_, _ = rig.srv.DraftDesignIdea(designRunCtx(), tc.req) // the request bytes are the subject
			routed := rig.stub.body
			require.NotEmpty(t, routed, "the provider was not called")
			require.Empty(t, rig.stub.imageURLs(t), "precondition: no picture on the wire")

			sys, user, jsonMode, maxTokens := legacyReplay(t, routed)
			legacy := openrouter.New(openrouter.Config{
				APIKey: "test-key", BaseURL: rig.stub.srv.URL, Model: "anthropic/claude-sonnet-5",
			})
			_, _, _, err := legacy.CompleteWithImages(context.Background(), sys, user, nil, jsonMode, maxTokens)
			require.NoError(t, err)
			require.Equal(t, rig.stub.body, routed, "the router's draft request differs from the pre-B-18 bytes")
		})
	}

	t.Run("ideas, no media", func(t *testing.T) {
		client, rec := newSuggestFakeOR(t, openrouter.Config{}, suggestAnswer(goodIdeas))
		_, err := newSuggestServer(t, client).SuggestPrompts(adminCtx("alice"), tryOnPose("x"))
		require.NoError(t, err)
		routed := rec.all()[0].Raw

		sys, user, jsonMode, maxTokens := legacyReplay(t, routed)
		_, _, _, err = client.CompleteWithImagesOn(context.Background(), openrouter.DefaultIdeasModel,
			sys, user, nil, jsonMode, maxTokens)
		require.NoError(t, err)
		require.Equal(t, rec.all()[1].Raw, routed, "the router's Ideas request differs from the pre-B-18 bytes")
	})
}
