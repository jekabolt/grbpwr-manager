package aiprov

import (
	"context"

	"github.com/shopspring/decimal"
)

// ChatRequest is ONE chat completion as every transport understands it — provider-neutral on
// purpose: the router hands the same request to whichever candidate answers, and a handler that
// builds it never learns which provider that was (02-PLAN §3).
//
// Effort is the reasoning effort word ("" = the transport's own default; "none" | "minimal" | "low"
// | "medium" | "high"); each transport spells it in its dialect (OpenRouter `reasoning.effort`,
// OpenAI `reasoning_effort`) or drops it where the model has no such knob. MaxTokens 0 = no ceiling
// (and no time bought for one — see CompletionBudget). ImageURLs non-empty makes the user turn a
// list of parts; empty keeps it a plain string, byte for byte what the text path sent before —
// unless UserAsParts asks for the parts shape anyway.
type ChatRequest struct {
	System    string
	User      string
	ImageURLs []string
	JSONMode  bool
	MaxTokens int
	Effort    string
	// UserAsParts sends the user turn in the legacy MULTIMODAL shape even with no pictures: a
	// one-element parts array (`[{"type":"text","text":…}]`) instead of a plain string. It is the
	// CALLER's wire contract, not a provider dialect — the picture-capable doors (the draft idea, the
	// Ideas suggestions) have always sent that shape for a picture-less request, and a transport that
	// speaks OpenAI-style content parts honours it (oaichat: Options.PartsAlways).
	UserAsParts bool
}

// ChatResult is what came back, with the PROVENANCE the ledger books: Provider is the billing
// provider key that answered, Model the slug the provider reports (else the requested one),
// RequestID the provider's generation id when it gives one, CostUSD the provider's own charge
// when it reports one (OpenRouter `usage.cost`; NULL otherwise — the ledger then prices from the
// catalogue or leaves it unknown, never zero), Engaged whether the request was written to the
// wire (a successful answer is always engaged; it is here so a caller that keeps the result and
// the error apart still has the fact).
type ChatResult struct {
	Text         string
	FinishReason string
	Usage        TokenUsage
	Provider     string
	Model        string
	RequestID    string
	CostUSD      decimal.NullDecimal
	Engaged      bool
}

// Chatter is the chat capability of ONE transport (oaichat for OpenRouter / OpenAI / apibost,
// later anthropic and gemini). A failed call returns a *CallError (errors.go) so the router can
// read Engaged / Retryable / HTTPStatus without knowing the transport.
type Chatter interface {
	Chat(ctx context.Context, model string, req ChatRequest) (*ChatResult, error)
}
