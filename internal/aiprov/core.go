// Package aiprov is the provider-neutral layer of the AI stack: who asked for a call (actor.go), the
// ONE error shape every transport returns for a failed call (errors.go), the normalised usage of a
// call (this file) and the ledger writer that books every physical call (ledger.go). Transports, the
// registry and the router build on it; it knows nothing about any of them — and imports no client
// package, directly or through a store package that does (see entity.BudgetDayKey).
//
// Subpackages:
//
//	keyring/  AES-256-GCM seal/open of provider keys stored in the database (master key from env)
//	pricing/  the curated per-(provider, model) price table and token → USD
//	oaichat/  the chat transport of every OpenAI-shaped API (OpenRouter, OpenAI, apibost dialects)
package aiprov

// TokenUsage is the normalised usage of one call.
//
// ⚠ THE FOUR NUMBERS ARE NOT DISJOINT, and every transport must map its provider's usage block onto
// this ONE convention — the OpenAI/OpenRouter one, because three of the five chat transports
// (OpenRouter, OpenAI, apibost) already speak it and the ledger stores the numbers as they came:
//
//	Prompt      ALL input tokens of the call, cached ones included;
//	Cached      the part of Prompt served from the provider's cache (Cached ⊆ Prompt);
//	Completion  ALL output tokens, reasoning included — OpenRouter: «reasoning tokens are considered
//	            output tokens», they are billed at the output rate and count against max_tokens;
//	Reasoning   the part of Completion spent on thinking (Reasoning ⊆ Completion).
//
// Providers that report them apart must ADD before filling this struct: Anthropic's input_tokens
// excludes cache_read_input_tokens (Prompt = input + cache_read), Gemini's candidatesTokenCount
// excludes thoughtsTokenCount (Completion = candidates + thoughts). Getting this wrong prices the
// call wrong in the ledger and nowhere else — pricing.Price trusts the convention.
type TokenUsage struct {
	Prompt, Completion, Cached, Reasoning int
}
