// Package pricing is the curated price table of the AI providers: per (provider, model) USD per
// million tokens, or per call for image rows, each row naming where its number was read and when.
//
// ⚠ IT IS THE THIRD SOURCE OF A LEDGER PRICE, NOT THE FIRST. The rank (02-PLAN §6) is: `provider`
// (the provider's own number — OpenRouter usage.cost, runblob's price) > `units` (billable units or
// credits × a tariff — fal) > `table` (this package) > `none` (NULL). A row here is
// what the ledger records when nothing better arrived; the report says `cost_source` beside it.
//
// ⚠ NO NUMBER HERE IS INVENTED. A slug whose price could not be sourced has NULL rates and a Source
// that starts with "unpriced"; Price answers (invalid, "none") for it, and the report counts it
// under "unpriced calls" instead of showing a guess — a made-up price is a lie in the one place the
// owner reads money from. Rows marked UNVERIFIED carry the best sourced figure and say what to check.
package pricing

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Version is recorded on every table-priced ledger row (ai_usage_event.price_version), so a price
// edit here never silently re-prices history: rows keep saying which table priced them.
const Version = "2026-09-29"

// Row kinds — the capability vocabulary (entity.AIProviderCapabilities) a row serves.
const (
	KindChat  = entity.AICapabilityChat
	KindImage = entity.AICapabilityImage
	KindVideo = entity.AICapabilityVideo
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

// Where the numbers come from (read or recorded 2026-09-27 … 2026-09-29).
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
	// The direct providers' own list prices (lane H2): each page read 2026-09-29, the standard (not
	// batch / flex / fast) tier, the short-context rate where the page has two.
	srcOpenAIList    = "https://developers.openai.com/api/docs/pricing (Standard tier), read 2026-09-29"
	srcAnthropicList = "https://platform.claude.com/docs/en/about-claude/pricing (base input / output), read 2026-09-29"
	srcGoogleList    = "https://ai.google.dev/gemini-api/docs/pricing (Paid tier, Standard), read 2026-09-29"
	// A direct row whose slug apibost proves exists but whose list price no page stated.
	srcDirectUnpriced = "unpriced — list price not sourced 2026-09-29; "
	// OpenRouter returns the provider's own cost (usage.cost); its rows are the fallback when it did not.
	orFallback = " — fallback only: OpenRouter's usage.cost wins"
)

// srcRunblob — runblob's image families, as the panel lists them. UNPRICED ON PURPOSE: runblob states
// the price of EVERY generation at submit (Submission.PriceUSD — «0.0210», «0.0290»), and the image
// transport books that number as cost_source provider; a rate here would be a second number that
// disagrees the day runblob edits its page. The slugs are the transport's (runblob.ImageSlugs; a test
// there pins the two lists together).
const srcRunblob = "unpriced — runblob states its price per generation at submit (Submission.PriceUSD, " +
	"booked as cost_source provider); tmp/plans/ai-providers/runblob-specs/kling.json and the Nano Banana " +
	"docs page, read 2026-09-28"

// catalogue — the curated rows: the direct providers and OpenRouter here, the resellers' full lists in
// their own files (catalogue_apibost.go). fal has no rows yet: its money arrives as billable units × a
// tariff and is priced by its caller. runblob's rows are LISTED and UNPRICED (srcRunblob): the panel
// needs the slugs to offer, and the ledger takes runblob's own number.
//
// The rows the A brief curated (srcBrief and the others above) keep their numbers; the rows lane H2
// added (2026-09-29) are priced only from the provider's own page (srcOpenAIList, srcAnthropicList,
// srcGoogleList), else unpriced with the reseller's relay price in the note.
var catalogue = map[string][]Model{
	entity.AIProviderOpenAI: {
		chat(entity.AIProviderOpenAI, "gpt-5.2", "GPT-5.2", "1.25", "10",
			srcBrief+" — UNVERIFIED (marked «verify»; check https://openai.com/api/pricing/ on beta)"),
		chat(entity.AIProviderOpenAI, "gpt-5-mini", "GPT-5 mini", "0.25", "2",
			srcORModels+": openai/gpt-5-mini $0.25/M in, $2/M out (OpenRouter passes the list price through)"),
		image(entity.AIProviderOpenAI, "gpt-image-2", "GPT Image 2", "0.053", srcORGPTImage2),
		image(entity.AIProviderOpenAI, "gpt-image-2.5-sunburst", "GPT Image 2.5", "0.013", srcGPTImage25),
		// Lane H2: the models apibost proves exist, priced from OpenAI's own page.
		chat(entity.AIProviderOpenAI, "gpt-6-astra", "GPT-6 Astra", "10", "50", srcOpenAIList+" (short context; the long-context rate is not modelled)"),
		chat(entity.AIProviderOpenAI, "gpt-6-sol", "GPT-6 Sol", "2", "10", srcOpenAIList+" (short context; the long-context rate is not modelled)"),
		chat(entity.AIProviderOpenAI, "gpt-6-luna", "GPT-6 Luna", "0.1", "0.5", srcOpenAIList+" (short context; the long-context rate is not modelled)"),
		chat(entity.AIProviderOpenAI, "gpt-5.6-sol", "GPT-5.6 Sol", "4", "20", srcOpenAIList+" — a promotional price «at least through November 21, 2026»: re-read then"),
		chat(entity.AIProviderOpenAI, "gpt-5.6-terra", "GPT-5.6 Terra", "2", "12", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-5.6-luna", "GPT-5.6 Luna", "0.2", "1.2", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-5.5", "GPT-5.5", "5", "30", srcOpenAIList+" (the <272K-context rate)"),
		chat(entity.AIProviderOpenAI, "gpt-5.4", "GPT-5.4", "2.5", "15", srcOpenAIList+" (the <272K-context rate)"),
		chat(entity.AIProviderOpenAI, "gpt-5.4-mini", "GPT-5.4 mini", "0.75", "4.5", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-5.4-nano", "GPT-5.4 nano", "0.2", "1.25", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-5", "GPT-5", "1.25", "10", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-5-nano", "GPT-5 nano", "0.05", "0.4", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "o3", "o3", "2", "8", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "o3-mini", "o3-mini", "1.1", "4.4", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "o4-mini", "o4-mini", "1.1", "4.4", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-4.1", "GPT-4.1", "2", "8", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-4.1-mini", "GPT-4.1 mini", "0.4", "1.6", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-4.1-nano", "GPT-4.1 nano", "0.1", "0.4", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-4o", "GPT-4o", "2.5", "10", srcOpenAIList),
		chat(entity.AIProviderOpenAI, "gpt-4o-mini", "GPT-4o mini", "0.15", "0.6", srcOpenAIList),
		unpricedRow(entity.AIProviderOpenAI, "gpt-image-1.5", "GPT Image 1.5", KindImage,
			srcDirectUnpriced+"OpenAI prices image output per token, not per picture; apibost relays it at $0.16 per image"),
	},
	entity.AIProviderAnthropic: {
		chat(entity.AIProviderAnthropic, "claude-sonnet-5", "Claude Sonnet 5", "3", "15",
			srcBrief+" (the Sonnet-class tariff $3/M in, $15/M out — the same pair as design_run.go designChatUSDPerMTok*)"),
		chat(entity.AIProviderAnthropic, "claude-opus-5-5", "Claude Opus 5.5", "5", "25",
			"UNVERIFIED — $5/M in, $25/M out carried from OpenRouter's anthropic/claude-opus-5 price (live tech-card "+
				"analysis run, 2026-08-25); apibost Model Square lists $75/$75 (2026-09-27); check https://www.anthropic.com/pricing on beta"),
		chat(entity.AIProviderAnthropic, "claude-haiku-4-5-20251001", "Claude Haiku 4.5", "1", "5",
			srcBrief+" (apibost Model Square lists the same $1/$5)"),
		// Lane H2: the models apibost proves exist (its "-thinking" names are apibost's own, not
		// Anthropic's), priced from Anthropic's own page.
		chat(entity.AIProviderAnthropic, "claude-fable-5-1", "Claude Fable 5.1", "10", "50", srcAnthropicList),
		chat(entity.AIProviderAnthropic, "claude-fable-5", "Claude Fable 5", "10", "50", srcAnthropicList),
		chat(entity.AIProviderAnthropic, "claude-opus-4-8", "Claude Opus 4.8", "5", "25", srcAnthropicList),
		chat(entity.AIProviderAnthropic, "claude-opus-4-6", "Claude Opus 4.6", "5", "25", srcAnthropicList),
		chat(entity.AIProviderAnthropic, "claude-sonnet-4-6", "Claude Sonnet 4.6", "3", "15", srcAnthropicList),
		chat(entity.AIProviderAnthropic, "claude-sonnet-4-5-20250929", "Claude Sonnet 4.5", "3", "15", srcAnthropicList),
	},
	entity.AIProviderGoogle: {
		chat(entity.AIProviderGoogle, "gemini-2.5-pro", "Gemini 2.5 Pro", "1.25", "10",
			srcBrief+" (Google AI Studio list price; the long-prompt tier is not modelled)"),
		chat(entity.AIProviderGoogle, "gemini-2.5-flash", "Gemini 2.5 Flash", "0.30", "2.50",
			srcBrief+" (Google AI Studio list price)"),
		image(entity.AIProviderGoogle, "gemini-3-pro-image", "Gemini 3 Pro Image", "0.134", srcGemini3ProImage),
		unpricedRow(entity.AIProviderGoogle, "gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite", KindChat,
			"unpriced — no Google AI Studio price sourced by 2026-09-27 ("+srcBrief+")"),
		// Lane H2: the models apibost proves exist, priced from Google's own page.
		chat(entity.AIProviderGoogle, "gemini-3.8-flash", "Gemini 3.8 Flash", "0.75", "3.75", srcGoogleList+" — «through December 31, 2026»; $1.50/$7.50 from 2027-01-01: re-price then"),
		chat(entity.AIProviderGoogle, "gemini-3.7-flash", "Gemini 3.7 Flash", "0.75", "3.75", srcGoogleList+" — «through December 31, 2026»; $1.50/$7.50 from 2027-01-01: re-price then"),
		chat(entity.AIProviderGoogle, "gemini-3.5-flash", "Gemini 3.5 Flash", "1.5", "9", srcGoogleList),
		chat(entity.AIProviderGoogle, "gemini-3.1-pro-preview", "Gemini 3.1 Pro (preview)", "2", "12", srcGoogleList+" (prompts ≤ 200k tokens; the long-prompt tier is not modelled)"),
		unpricedRow(entity.AIProviderGoogle, "gemini-3.1-flash-lite-preview", "Gemini 3.1 Flash-Lite (preview)", KindChat,
			srcDirectUnpriced+"not on Google's pricing page (the GA gemini-3.1-flash-lite is); apibost relays it at $0.25/$1.50"),
		image(entity.AIProviderGoogle, "gemini-3.1-flash-image", "Gemini 3.1 Flash Image", "0.067", srcGoogleList+": $0.067 per 1K image (0.5K $0.045, 2K $0.101, 4K $0.151 not modelled)"),
		image(entity.AIProviderGoogle, "gemini-3.1-flash-lite-image", "Gemini 3.1 Flash-Lite Image", "0.0336", srcGoogleList+": $0.0336 per 1K-resolution image"),
		unpricedRow(entity.AIProviderGoogle, "gemini-3.1-flash-image-preview", "Gemini 3.1 Flash Image (preview)", KindImage,
			srcDirectUnpriced+"not on Google's pricing page (the GA gemini-3.1-flash-image is); apibost relays it at $0.04 per image"),
		unpricedRow(entity.AIProviderGoogle, "gemini-3-pro-image-preview", "Gemini 3 Pro Image (preview)", KindImage,
			srcDirectUnpriced+"not on Google's pricing page (the GA gemini-3-pro-image is); apibost relays it at $0.13 per image"),
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
	entity.AIProviderApibost: apibostRows,
	entity.AIProviderRunblob: {
		unpricedRow(entity.AIProviderRunblob, "gemini/standard", "Nano Banana (standard)", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "gemini/pro", "Nano Banana Pro", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "gemini/v2", "Nano Banana 2", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "gemini/v2_lite", "Nano Banana 2 Lite", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "gemini/pro_vip", "Nano Banana Pro VIP", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "gemini/v2_vip", "Nano Banana 2 VIP", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "kling/o1-photo", "Kling O1 Photo", KindImage, srcRunblob),
		unpricedRow(entity.AIProviderRunblob, "kling/o3-photo", "Kling O3 Photo", KindImage, srcRunblob),
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

// Lookup finds the row that prices slug on provider: the EXACT slug first; else, when slug ends in a
// dated snapshot suffix (-YYYY-MM-DD, a real calendar date), the row of the slug without it; else no
// row.
//
// WHY THE SECOND STEP. The ledger prices the slug the provider REPORTED (ChatResult.Model), and OpenAI
// reports the dated snapshot it served, not the alias it was asked for: a call to "gpt-5-mini" comes
// back as "gpt-5-mini-2025-08-07". An exact-only lookup would book every direct OpenAI call as
// unpriced, forever, while the table holds its price. The snapshot is the alias's model on the day it
// was cut, and the alias's row is the best sourced number for it — the row keeps saying where that
// number came from, and the ledger's model_actual keeps the dated slug.
//
// WHY ONLY THAT SUFFIX. Anything else after the alias ("-preview", "-high", "-search") can be a
// DIFFERENT model at a different price, and a guessed price is the lie this table exists to refuse —
// so it stays a miss, and the report counts it as unpriced. The date must parse: eleven characters
// that merely look like one are not a snapshot. An exact row always wins over the stripped one.
func Lookup(provider, slug string) (Model, bool) {
	if m, ok := lookupExact(provider, slug); ok {
		return m, true
	}
	if alias, ok := snapshotAlias(slug); ok {
		return lookupExact(provider, alias)
	}
	return Model{}, false
}

func lookupExact(provider, slug string) (Model, bool) {
	for _, m := range catalogue[provider] {
		if m.Slug == slug {
			return m, true
		}
	}
	return Model{}, false
}

// snapshotDate is the layout of the dated suffix OpenAI puts on the snapshot it served.
const snapshotDate = "2006-01-02"

// snapshotAlias is slug without a trailing "-YYYY-MM-DD" that parses as a calendar date; ok=false
// when there is no such suffix or nothing is left before it.
func snapshotAlias(slug string) (string, bool) {
	cut := len(slug) - len(snapshotDate) - 1
	if cut <= 0 || slug[cut] != '-' {
		return "", false
	}
	if _, err := time.Parse(snapshotDate, slug[cut+1:]); err != nil {
		return "", false
	}
	alias := slug[:cut]
	if strings.TrimSpace(alias) == "" {
		return "", false
	}
	return alias, true
}

// defaultChatSlugs — the slug a chat route row on a DIRECT provider is called with when the row
// names no model (router.Defaults.ByProvider, filled by admin.AIRouterDefaults). Before B-23 such a
// row resolved to "" and was silently uncallable: saved in the panel, passed over on every press.
//
// CHOSEN FOR PRICE, NOT POWER (06-BRIEFS-E, E3). A default is what a route gets when nobody picked a
// model; whoever wants a stronger one names it on the row, and a stronger model as the default would
// be spend nobody chose. Every value is a PRICED chat row of its own provider in the catalogue above
// (TestDefaultChatSlugsArePricedRows), so a defaulted call is never booked as unpriced.
//
// OpenRouter has NO row here on purpose: its defaults are the per-purpose env slugs
// (OPENROUTER_MODEL, _ANALYSIS, _IDEAS — router.Defaults.Chat / Analysis / Ideas), which the seeded
// routes answer with today; the router never reads this table for an openrouter row.
var defaultChatSlugs = map[string]string{
	entity.AIProviderOpenAI:    "gpt-5-mini",
	entity.AIProviderAnthropic: "claude-sonnet-5",
	entity.AIProviderGoogle:    "gemini-2.5-flash",
	entity.AIProviderApibost:   "claude-sonnet-5",
}

// DefaultChatSlug is the default chat slug of a direct provider (see defaultChatSlugs); ok=false for
// OpenRouter (env defaults) and for every provider that does not serve chat.
func DefaultChatSlug(provider string) (string, bool) {
	slug, ok := defaultChatSlugs[provider]
	return slug, ok
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
