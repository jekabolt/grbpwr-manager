package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/aiprov"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/pricing"
	"github.com/jekabolt/grbpwr-manager/internal/aiprov/router"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/openrouter"
)

// ─── THE CHAT DOOR (B-18) ─────────────────────────────────────────────────────────────────────────
//
// Six features call the model through s.ai (router.Router): EnhanceText, AnalyzeTechCardConstruction,
// FormatLibraryNoteMarkdown, AutoTranslateEmailCampaign, DraftDesignIdea and SuggestPrompts. Each names
// its PURPOSE (entity.AIPurpose*) and the router picks the provider and the slug from the route the
// owner set in admin → AI providers, falls back where no money moved, and books a ledger row per call.
//
// WHAT THE DOORS READ, AND WHAT THEY NO LONGER READ. A failure is judged by the *aiprov.CallError's
// FIELDS (Code, HTTPStatus, Engaged) through aiprov.AsCallError — never by its sentence: the router
// wraps an exhausted chain as "ai: every candidate failed: …", so a regex anchored at the start of the
// transport's sentence (the old providerHTTPStatusRe) no longer sees it, and a sentence shown to a
// person is built from the fields (aiFaultWords), not echoed.

// SetAIRouter wires the chat door. A Server without it has a nil router: every AI door refuses with
// «not configured» (the router is nil-safe), nothing panics.
func (s *Server) SetAIRouter(r *router.Router) { s.ai = r }

// AIRouterDefaults is the router's default-slug table. OpenRouter keeps its per-purpose env slugs —
// today's OPENROUTER_MODEL / OPENROUTER_MODEL_ANALYSIS / OPENROUTER_MODEL_IDEAS — so the seeded
// routes (an empty model) answer exactly as the buttons did before the router. Every direct chat
// provider gets pricing.DefaultChatSlug: an explicit, priced slug chosen for price rather than power,
// so a model-less row saved in the panel is callable and its ledger row can be priced.
//
// IdeasOff is OPENROUTER_MODEL_IDEAS=off: IdeasModel() answers "" for that and only for that (unset is
// the default slug), so "" is read as the switch — and it closes the whole Ideas door, the seeded
// fallback row included (router.Defaults).
func AIRouterDefaults(c *openrouter.Client) router.Defaults {
	ideas := c.IdeasModel()
	byProvider := make(map[string]string)
	for _, provider := range entity.AIProviderKeys() {
		if slug, ok := pricing.DefaultChatSlug(provider); ok {
			byProvider[provider] = slug
		}
	}
	return router.Defaults{
		Chat: c.Model(), Analysis: c.AnalysisModel(), Ideas: ideas, IdeasOff: ideas == "",
		ByProvider: byProvider,
	}
}

const (
	// aiReasonPaused — every provider that could serve the purpose is held by its circuit breaker
	// after repeated failures. It passes by itself in minutes; it is not a setting.
	aiReasonPaused = "AI_PAUSED"
	// aiPausedMsg is THE sentence for that state, the same on every door.
	aiPausedMsg = "the AI provider is paused after repeated failures; try again in a few minutes"
)

// aiPausedRefusal — Unavailable (weather: «try again» is true), with the machine-readable reason so a
// client can tell it from AI_NOT_CONFIGURED without matching prose.
func aiPausedRefusal() error {
	st := status.New(codes.Unavailable, aiPausedMsg)
	withDetails, err := st.WithDetails(&errdetails.ErrorInfo{Reason: aiReasonPaused, Domain: aiErrorDomain})
	if err != nil {
		return st.Err()
	}
	return withDetails.Err()
}

// aiOffRefusal is the refusal of a door whose purpose is not Enabled: «paused» when only the
// breakers hold it (router.Paused), else the door's own «not configured» sentence.
func (s *Server) aiOffRefusal(purpose, notConfiguredMsg string) error {
	if s.ai.Paused(purpose) {
		return aiPausedRefusal()
	}
	return aiRefusal(aiReasonNotConfigured, notConfiguredMsg, nil)
}

// aiUncalledRefusal answers the two failures of a Chat that CALLED NOBODY — the route emptied between
// the door's Enabled check and the call: paused (router.ErrPaused) or not configured
// (aiprov.ErrNotConfigured, the router's own or a keyless transport's). ok=false for everything else.
func aiUncalledRefusal(err error, notConfiguredMsg string) (error, bool) {
	switch {
	case errors.Is(err, router.ErrPaused):
		return aiPausedRefusal(), true
	case errors.Is(err, aiprov.ErrNotConfigured):
		return aiRefusal(aiReasonNotConfigured, notConfiguredMsg, nil), true
	}
	return nil, false
}

// aiModelOf is the slug to NAME for a call of purpose: the one that answered (ChatResult.Model — a
// partial result on an engaged failure carries it too), else the slug the router would call first,
// or would have called had it a key (router.RouteHead) — a «not configured» line still names the knob.
func (s *Server) aiModelOf(purpose string, res *aiprov.ChatResult) string {
	if res != nil && strings.TrimSpace(res.Model) != "" {
		return res.Model
	}
	_, model := s.ai.RouteHead(purpose)
	return model
}

// aiProviderOf is aiModelOf for the provider key.
func (s *Server) aiProviderOf(purpose string, res *aiprov.ChatResult) string {
	if res != nil && strings.TrimSpace(res.Provider) != "" {
		return res.Provider
	}
	provider, _ := s.ai.RouteHead(purpose)
	return provider
}

// aiFaultWords names a failed call for a PERSON from the CallError's fields: the provider, the
// transport's fault word, and the status of a refusal ("openrouter: rate limited (HTTP 429)"). Never
// the error's text — the router's wrapping would lead it, and a provider's body may echo the prompt.
func aiFaultWords(err error) string {
	if errors.Is(err, router.ErrPaused) {
		return aiPausedMsg
	}
	ce, ok := aiprov.AsCallError(err)
	if !ok {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "the call ran out of time"
		case errors.Is(err, context.Canceled):
			return "the call was cancelled"
		}
		return "the provider did not answer"
	}
	provider := strings.TrimSpace(ce.Provider)
	if provider == "" {
		provider = "the provider"
	}
	word := strings.ReplaceAll(strings.TrimSpace(ce.Code), "_", " ")
	if word == "" {
		word = "call failed"
	}
	if providerHTTPStatus(err) != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d)", provider, word, ce.HTTPStatus)
	}
	return provider + ": " + word
}

// providerHTTPStatus is the provider's status for a REFUSED answer (non-2xx), read from the
// CallError's field, or 0. A 2xx that broke after it arrived (a garbled envelope, an empty message)
// carries its 200 in the same field and is not a refusal — B-11 sets HTTPStatus on those too — so
// "HTTPStatus != 0" is never the test.
func providerHTTPStatus(err error) int {
	ce, ok := aiprov.AsCallError(err)
	if !ok || ce.HTTPStatus == 0 || (ce.HTTPStatus >= 200 && ce.HTTPStatus < 300) {
		return 0
	}
	return ce.HTTPStatus
}

// isEmptyModelAnswer — "the model said nothing" (a 2xx with no choices or an empty message), from
// the CallError's code, not from the transport's wording.
func isEmptyModelAnswer(err error) bool {
	ce, ok := aiprov.AsCallError(err)
	return ok && ce.Code == aiprov.CodeEmptyAnswer
}
