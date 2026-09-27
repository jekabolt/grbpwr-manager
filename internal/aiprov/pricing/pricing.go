// Package pricing is the curated price table of the AI providers: per (provider, model) USD per
// million tokens, or per call for image rows, each row naming where its number was read and when.
//
// ⚠ IT IS THE THIRD SOURCE OF A LEDGER PRICE, NOT THE FIRST. The rank (02-PLAN §6) is: `provider`
// (the provider's own number — OpenRouter usage.cost, runblob's price) > `units` (billable units or
// credits × a tariff — fal, Meshy, Recraft) > `table` (this package) > `none` (NULL). A row here is
// what the ledger records when nothing better arrived; the report says `cost_source` beside it.
//
// ⚠ NO NUMBER HERE IS INVENTED. A slug whose price could not be sourced has NULL rates and a Source
// that starts with "unpriced"; Price answers (invalid, "none") for it, and the report counts it
// under "unpriced calls" instead of showing a guess — a made-up price is a lie in the one place the
// owner reads money from. Rows marked UNVERIFIED carry the best sourced figure and say what to check.
package pricing

import (
	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Version is recorded on every table-priced ledger row (ai_usage_event.price_version), so a price
// edit here never silently re-prices history: rows keep saying which table priced them.
const Version = "2026-09-27"

// Row kinds — the capability vocabulary (entity.AIProviderCapabilities) a row serves.
const (
	KindChat  = "chat"
	KindImage = "image"
)

// Model is one curated row. A chat row has InputUSDPer1M + OutputUSDPer1M (CachedInputUSDPer1M
// when the provider publishes a cache rate — none of today's rows has a sourced one, so cached
// tokens are billed at the input rate: never under). An image row has PerCallUSD: the price of ONE
// output picture at the quality the row names. An unpriced row has neither.
type Model struct {
	Provider, Slug, Label, Kind                                    string
	InputUSDPer1M, OutputUSDPer1M, CachedInputUSDPer1M, PerCallUSD decimal.NullDecimal
	Source                                                         string // URL or page + date read
}

// Usage is what the caller knows about one call. The token counts follow aiprov.TokenUsage's
// convention (Cached ⊆ Prompt, Reasoning ⊆ Completion). Units is the number of outputs a per-call
// row is multiplied by (pictures in one call; Unit names them, e.g. "image"); unset or ≤ 0 means one.
type Usage struct {
	Prompt, Completion, Cached, Reasoning int
	Units                                 decimal.NullDecimal
	Unit                                  string
}

func usd(s string) decimal.NullDecimal {
	return decimal.NullDecimal{Decimal: decimal.RequireFromString(s), Valid: true}
}

var unpriced = decimal.NullDecimal{}

func chat(provider, slug, label, in, out, source string) Model {
	return Model{Provider: provider, Slug: slug, Label: label, Kind: KindChat,
		InputUSDPer1M: usd(in), OutputUSDPer1M: usd(out), Source: source}
}

func image(provider, slug, label, perCall, source string) Model {
	return Model{Provider: provider, Slug: slug, Label: label, Kind: KindImage,
		PerCallUSD: usd(perCall), Source: source}
}

func unpricedRow(provider, slug, label, kind, source string) Model {
	return Model{Provider: provider, Slug: slug, Label: label, Kind: kind, Source: source}
}

// Where the numbers come from (all read or recorded by 2026-09-27).
const (
	srcBrief = "tmp/plans/ai-providers/06-BRIEFS-A.md curated table, 2026-09-27"
	// OpenRouter's live chat catalogue, read 2026-09-27 (the IdeasFallbackModel note in openrouter.go).
	srcORModels = "https://openrouter.ai/api/v1/models, read 2026-09-27 (openrouter.go, Ideas slugs note)"
	// OpenRouter's image catalogue as measured for the PLAYGROUND wave.
	srcORGPTImage2 = "https://openrouter.ai/api/v1/images/models/openai/gpt-image-2/endpoints: medium 1024² $0.053 per image " +
		"(tmp/plans/playground-tab/12-PROVIDERS.md, 2026-09)"
	srcGPTImage25 = "fal's published GPT Image 2.5 table, 1024² medium $0.0133 per image " +
		"(tmp/plans/playground-tab/12-PROVIDERS.md, 2026-09) — UNVERIFIED, measure usage.cost on beta"
	srcGemini3ProImage = "https://ai.google.dev/gemini-api/docs/pricing, read 2026-09-27: 1K/2K image = 1120 output tokens " +
		"× $120/M ≈ $0.134 (4K ≈ $0.24 not modelled; designgen/engines.go)"
	srcSeedream5Pro = "https://www.atlascloud.ai/blog/ai-updates/seedream-5-0-pro-price, read 2026-09-27: ≤ 2.36 MP $0.045 " +
		"(the 2K variant $0.09 not modelled; designgen/engines.go)"
	srcApibost = "apibost.com Model Square, read 2026-09-27"
	// OpenRouter returns the provider's own cost (usage.cost); its rows are the fallback when it did not.
	orFallback = " — fallback only: OpenRouter's usage.cost wins"
)

// catalogue — the curated rows, in the order of the brief. fal, meshy, recraft and runblob have no
// rows on purpose: their money arrives as units / credits / a provider price (fal tariffs,
// MESHY_CREDIT_USD, recraft credits, runblob's `price`) and is priced by their callers.
var catalogue = map[string][]Model{
	entity.AIProviderOpenAI: {
		chat(entity.AIProviderOpenAI, "gpt-5.2", "GPT-5.2", "1.25", "10",
			srcBrief+" — UNVERIFIED (marked «verify»; check https://openai.com/api/pricing/ on beta)"),
		chat(entity.AIProviderOpenAI, "gpt-5-mini", "GPT-5 mini", "0.25", "2",
			srcORModels+": openai/gpt-5-mini $0.25/M in, $2/M out (OpenRouter passes the list price through)"),
		image(entity.AIProviderOpenAI, "gpt-image-2", "GPT Image 2", "0.053", srcORGPTImage2),
		image(entity.AIProviderOpenAI, "gpt-image-2.5-sunburst", "GPT Image 2.5", "0.013", srcGPTImage25),
	},
	entity.AIProviderAnthropic: {
		chat(entity.AIProviderAnthropic, "claude-sonnet-5", "Claude Sonnet 5", "3", "15",
			srcBrief+" (the Sonnet-class tariff $3/M in, $15/M out — the same pair as design_run.go designChatUSDPerMTok*)"),
		chat(entity.AIProviderAnthropic, "claude-opus-5-5", "Claude Opus 5.5", "5", "25",
			"UNVERIFIED — $5/M in, $25/M out carried from OpenRouter's anthropic/claude-opus-5 price (live tech-card "+
				"analysis run, 2026-08-25); apibost Model Square lists $75/$75 (2026-09-27); check https://www.anthropic.com/pricing on beta"),
		chat(entity.AIProviderAnthropic, "claude-haiku-4-5-20251001", "Claude Haiku 4.5", "1", "5",
			srcBrief+" (apibost Model Square lists the same $1/$5)"),
	},
	entity.AIProviderGoogle: {
		chat(entity.AIProviderGoogle, "gemini-2.5-pro", "Gemini 2.5 Pro", "1.25", "10",
			srcBrief+" (Google AI Studio list price; the long-prompt tier is not modelled)"),
		chat(entity.AIProviderGoogle, "gemini-2.5-flash", "Gemini 2.5 Flash", "0.30", "2.50",
			srcBrief+" (Google AI Studio list price)"),
		image(entity.AIProviderGoogle, "gemini-3-pro-image", "Gemini 3 Pro Image", "0.134", srcGemini3ProImage),
		unpricedRow(entity.AIProviderGoogle, "gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite", KindChat,
			"unpriced — no Google AI Studio price sourced by 2026-09-27 ("+srcBrief+")"),
	},
	entity.AIProviderOpenRouter: {
		chat(entity.AIProviderOpenRouter, "anthropic/claude-sonnet-5", "Claude Sonnet 5", "3", "15",
			srcBrief+" (OpenRouter catalogue)"+orFallback),
		chat(entity.AIProviderOpenRouter, "anthropic/claude-opus-5", "Claude Opus 5", "5", "25",
			"OpenRouter price $5/M in, $25/M out, recorded on the live tech-card analysis run 2026-08-25"+orFallback),
		chat(entity.AIProviderOpenRouter, "openai/gpt-5-mini", "GPT-5 mini", "0.25", "2",
			srcORModels+": $0.25/M in, $2/M out"+orFallback),
		// Unpriced ON PURPOSE (06-BRIEFS-A curated table; Codex review A1 #3): OpenRouter's usage.cost
		// prices every call it answers, and a call it does not price must read «—», not a number this
		// deployment made up. A live read of /api/v1/models is not a curated source.
		unpricedRow(entity.AIProviderOpenRouter, "google/gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite", KindChat,
			"unpriced — no curated price by 2026-09-27 ("+srcBrief+")"+orFallback),
		image(entity.AIProviderOpenRouter, "openai/gpt-image-2", "GPT Image 2", "0.053", srcORGPTImage2+orFallback),
		image(entity.AIProviderOpenRouter, "openai/gpt-image-2.5-sunburst", "GPT Image 2.5", "0.013", srcGPTImage25+orFallback),
		image(entity.AIProviderOpenRouter, "google/gemini-3-pro-image", "Gemini 3 Pro Image", "0.134", srcGemini3ProImage+orFallback),
		image(entity.AIProviderOpenRouter, "bytedance-seed/seedream-5-0-pro", "Seedream 5 Pro", "0.045", srcSeedream5Pro+orFallback),
	},
	entity.AIProviderApibost: {
		chat(entity.AIProviderApibost, "claude-fable-5-1", "Claude Fable 5.1", "8", "40", srcApibost),
		chat(entity.AIProviderApibost, "claude-sonnet-5", "Claude Sonnet 5", "2", "10", srcApibost),
		chat(entity.AIProviderApibost, "claude-haiku-4-5-20251001", "Claude Haiku 4.5", "1", "5", srcApibost),
		chat(entity.AIProviderApibost, "gemini-2.5-flash", "Gemini 2.5 Flash", "0.3", "2.5", srcApibost),
		chat(entity.AIProviderApibost, "gemini-2.5-pro", "Gemini 2.5 Pro", "1.25", "10", srcApibost),
		image(entity.AIProviderApibost, "dall-e-3", "DALL·E 3", "0.04", srcApibost),
		image(entity.AIProviderApibost, "flux-kontext-pro", "FLUX Kontext Pro", "0.08", srcApibost),
		image(entity.AIProviderApibost, "gemini-2.5-flash-image", "Gemini 2.5 Flash Image", "0.02", srcApibost),
	},
}

// Catalogue returns a copy of the curated rows of one provider (nil when it has none).
func Catalogue(provider string) []Model {
	rows := catalogue[provider]
	if len(rows) == 0 {
		return nil
	}
	return append([]Model(nil), rows...)
}

// Lookup finds one row by exact provider key and slug.
func Lookup(provider, slug string) (Model, bool) {
	for _, m := range catalogue[provider] {
		if m.Slug == slug {
			return m, true
		}
	}
	return Model{}, false
}

var million = decimal.NewFromInt(1_000_000)

// Price prices one call from the table. Answers (usd, entity.AICostTable) for a priced row and
// (invalid, entity.AICostNone) for everything the table cannot price honestly: an unknown provider
// or slug, an unpriced row, and a token row called with no token counts at all (usage missing is not
// "free" — $0 would read as a free call in the ledger).
//
//	token row:  (Prompt − Cached) × input + Cached × cached-rate (the input rate when the row has
//	            none) + Completion × output, per million. Reasoning is billed as output because it
//	            is PART OF Completion (aiprov.TokenUsage) — it is not added a second time.
//	per-call:   PerCallUSD × Units (Units unset or ≤ 0 → one output).
func Price(provider, slug string, u Usage) (usd decimal.NullDecimal, source string) {
	m, ok := Lookup(provider, slug)
	if !ok {
		return unpriced, entity.AICostNone
	}
	return priceModel(m, u)
}

func priceModel(m Model, u Usage) (decimal.NullDecimal, string) {
	if m.PerCallUSD.Valid {
		n := decimal.NewFromInt(1)
		if u.Units.Valid && u.Units.Decimal.IsPositive() {
			n = u.Units.Decimal
		}
		return decimal.NullDecimal{Decimal: m.PerCallUSD.Decimal.Mul(n), Valid: true}, entity.AICostTable
	}
	if !m.InputUSDPer1M.Valid || !m.OutputUSDPer1M.Valid {
		return unpriced, entity.AICostNone
	}
	prompt, cached, completion := max(u.Prompt, 0), max(u.Cached, 0), max(u.Completion, 0)
	if prompt == 0 && cached == 0 && completion == 0 {
		return unpriced, entity.AICostNone
	}
	cachedRate := m.InputUSDPer1M.Decimal
	if m.CachedInputUSDPer1M.Valid {
		cachedRate = m.CachedInputUSDPer1M.Decimal
	}
	// Cached ⊆ Prompt. A transport that broke the convention (Cached > Prompt) gets every cached
	// token billed at the cached rate and none at the input rate — never a negative term.
	uncached := max(prompt-cached, 0)
	total := m.InputUSDPer1M.Decimal.Mul(decimal.NewFromInt(int64(uncached))).
		Add(cachedRate.Mul(decimal.NewFromInt(int64(cached)))).
		Add(m.OutputUSDPer1M.Decimal.Mul(decimal.NewFromInt(int64(completion)))).
		Div(million)
	return decimal.NullDecimal{Decimal: total, Valid: true}, entity.AICostTable
}
