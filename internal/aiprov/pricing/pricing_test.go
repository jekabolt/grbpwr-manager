package pricing

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// allProviders — every provider key the panel knows (entity.AIProviderKeys), so a provider added or
// removed there is walked here without a second list to keep in step.
var allProviders = entity.AIProviderKeys()

func requireUSD(t *testing.T, want string, got decimal.NullDecimal, source string) {
	t.Helper()
	require.True(t, got.Valid, "expected a price, got NULL")
	require.Equal(t, entity.AICostTable, source)
	require.True(t, decimal.RequireFromString(want).Equal(got.Decimal), "want %s, got %s", want, got.Decimal)
}

func requireNone(t *testing.T, got decimal.NullDecimal, source string) {
	t.Helper()
	require.False(t, got.Valid, "expected NULL, got %s", got.Decimal)
	require.Equal(t, entity.AICostNone, source)
}

// TestPricingKnownSlugPricesTokens — tokens × the row's rates, per million.
//
// The opus-5 case is a REAL call: the first live tech-card analysis (2026-08-25) reported
// prompt_tokens 10149, completion_tokens 2500, and cost ≈ $0.11 — the table must land there.
//
// MUTATION: divide by 1_000 instead of 1_000_000 → red. MUTATION: swap the input and output rates
// in priceModel → red.
func TestPricingKnownSlugPricesTokens(t *testing.T) {
	usd, src := Price(entity.AIProviderOpenAI, "gpt-5-mini", Usage{Prompt: 1_000_000, Completion: 1_000_000})
	requireUSD(t, "2.25", usd, src)

	usd, src = Price(entity.AIProviderOpenRouter, "anthropic/claude-opus-5", Usage{Prompt: 10149, Completion: 2500})
	requireUSD(t, "0.113245", usd, src) // 10149 × 5/M + 2500 × 25/M

	usd, src = Price(entity.AIProviderAnthropic, "claude-sonnet-5", Usage{Prompt: 2000, Completion: 1600})
	requireUSD(t, "0.03", usd, src) // the hand-computed draft-idea base in design_run.go

	usd, src = Price(entity.AIProviderApibost, "claude-fable-5-1", Usage{Prompt: 1000, Completion: 0})
	requireUSD(t, "0.008", usd, src)
}

// TestPricingReasoningBilledAsOutput — reasoning is part of Completion (aiprov.TokenUsage) and is
// billed at the output rate exactly once: not dropped, not added a second time.
//
// MUTATION: bill Completion + Reasoning → red (0.0032 instead of 0.002). MUTATION: bill
// Completion − Reasoning at output → red.
func TestPricingReasoningBilledAsOutput(t *testing.T) {
	usd, src := Price(entity.AIProviderOpenAI, "gpt-5-mini", Usage{Completion: 1000, Reasoning: 600})
	requireUSD(t, "0.002", usd, src) // 1000 × $2/M
}

// TestPricingCachedAtTheCachedRate — cached tokens are a part of Prompt billed at the row's cached
// rate; a row without a sourced cached rate (every row today) bills them at the input rate — never
// below it.
//
// MUTATION: bill Prompt (not Prompt − Cached) at the input rate → red (cached tokens paid twice).
// MUTATION: ignore CachedInputUSDPer1M → the synthetic row goes red.
func TestPricingCachedAtTheCachedRate(t *testing.T) {
	withCache := Model{Provider: "test", Slug: "cached", Kind: KindChat,
		InputUSDPer1M: usd("2"), OutputUSDPer1M: usd("8"), CachedInputUSDPer1M: usd("0.5"), Source: "test"}
	got, src := priceModel(withCache, Usage{Prompt: 1_000_000, Cached: 400_000, Completion: 100_000})
	requireUSD(t, "2.2", got, src) // 600k × 2 + 400k × 0.5 + 100k × 8 = 1.2 + 0.2 + 0.8

	// A transport that broke the convention (Cached > Prompt): no negative term, cached billed whole.
	got, src = priceModel(withCache, Usage{Prompt: 100, Cached: 1_000_000})
	requireUSD(t, "0.5", got, src)

	// Real rows publish no cache rate yet: cached tokens cost the input rate.
	m, ok := Lookup(entity.AIProviderGoogle, "gemini-2.5-pro")
	require.True(t, ok)
	require.False(t, m.CachedInputUSDPer1M.Valid, "precondition: no sourced cache rate on this row")
	a, _ := Price(entity.AIProviderGoogle, "gemini-2.5-pro", Usage{Prompt: 10_000, Cached: 8_000, Completion: 1_000})
	b, _ := Price(entity.AIProviderGoogle, "gemini-2.5-pro", Usage{Prompt: 10_000, Completion: 1_000})
	require.True(t, a.Decimal.Equal(b.Decimal), "no cache rate → cached tokens billed at the input rate")
}

// TestPricingPerCallRows — an image row prices one output picture; Units multiplies.
//
// MUTATION: ignore Units → the "three pictures" case goes red. MUTATION: price per-call rows by
// tokens (drop the PerCallUSD branch) → red (image rows have no token rates → NULL).
func TestPricingPerCallRows(t *testing.T) {
	usd, src := Price(entity.AIProviderOpenRouter, "openai/gpt-image-2", Usage{Prompt: 900, Completion: 4000})
	requireUSD(t, "0.053", usd, src)

	three := Usage{Units: decimal.NewNullDecimal(decimal.NewFromInt(3)), Unit: "image"}
	usd, src = Price(entity.AIProviderOpenRouter, "openai/gpt-image-2", three)
	requireUSD(t, "0.159", usd, src)

	for _, units := range []decimal.NullDecimal{{}, decimal.NewNullDecimal(decimal.Zero), decimal.NewNullDecimal(decimal.NewFromInt(-2))} {
		usd, src = Price(entity.AIProviderApibost, "dall-e-3", Usage{Units: units})
		requireUSD(t, "0.04", usd, src) // unset / zero / negative → one output
	}
	usd, src = Price(entity.AIProviderGoogle, "gemini-3-pro-image", Usage{})
	requireUSD(t, "0.134", usd, src)
}

// TestPricingUnknownIsNone — whatever the table cannot price honestly is (NULL, "none"), never $0.
//
// MUTATION: Price returns (0, table) for an unknown slug → red. MUTATION: drop the "no token counts"
// guard → the empty-usage case prices $0 and goes red.
func TestPricingUnknownIsNone(t *testing.T) {
	u := Usage{Prompt: 1000, Completion: 1000}
	for _, c := range []struct{ provider, slug string }{
		{entity.AIProviderOpenAI, "gpt-9000"},
		{"nonexistent", "gpt-5-mini"},
		{entity.AIProviderOpenAI, "openai/gpt-5-mini"},     // an OpenRouter slug on the direct provider
		{entity.AIProviderOpenRouter, "gpt-5-mini"},        // and the reverse
		{entity.AIProviderGoogle, "gemini-3.1-flash-lite"}, // the OpenRouter row IS priced (live read in openrouter.go:131-133)
		{entity.AIProviderFal, "fal-ai/birefnet/v2"},
		{entity.AIProviderRunblob, "kling_2.5_turbo"},
		{"", ""},
	} {
		got, src := Price(c.provider, c.slug, u)
		requireNone(t, got, src)
	}
	got, src := Price(entity.AIProviderOpenAI, "gpt-5-mini", Usage{})
	requireNone(t, got, src) // usage missing is not a free call

	require.Empty(t, Catalogue(entity.AIProviderFal), "fal is priced by billable units, not by this table")
	// runblob (B-31): LISTED so the panel can offer the eight image slugs, UNPRICED so the ledger takes
	// runblob's own submit price and never a number this table made up.
	for _, m := range Catalogue(entity.AIProviderRunblob) {
		got, src := Price(entity.AIProviderRunblob, m.Slug, Usage{Units: decimal.NewNullDecimal(decimal.NewFromInt(1)), Unit: "image"})
		requireNone(t, got, src)
	}
}

// TestPricingRunblobRowsAreListedUnpriced — the eight image slugs of runblob's transport are in the
// catalogue as image rows with no rate: the panel lists them (priced=false), the ledger books the
// provider's own price (cost_source provider) and never a table number.
//
// MUTATION (measured red→green): give "gemini/standard" a PerCallUSD of "0.021" → red (a rate that
// would silently disagree with runblob's own price); drop the kling/o3-photo row → red.
func TestPricingRunblobRowsAreListedUnpriced(t *testing.T) {
	rows := Catalogue(entity.AIProviderRunblob)
	slugs := make([]string, 0, len(rows))
	for _, m := range rows {
		slugs = append(slugs, m.Slug)
		require.Equal(t, KindImage, m.Kind, m.Slug)
		require.False(t, m.PerCallUSD.Valid, "%s: runblob's price is per call, from the submit, never a table rate", m.Slug)
		require.False(t, m.InputUSDPer1M.Valid || m.OutputUSDPer1M.Valid, m.Slug)
		require.True(t, strings.HasPrefix(m.Source, "unpriced"), m.Slug)
		require.Contains(t, m.Source, "cost_source provider", m.Slug)
	}
	require.Equal(t, []string{"gemini/standard", "gemini/pro", "gemini/v2", "gemini/v2_lite", "gemini/pro_vip", "gemini/v2_vip",
		"kling/o1-photo", "kling/o3-photo"}, slugs)
}

// TestPricingEveryRowHasSource — every row says where its number came from; an unpriced row says
// "unpriced"; the shape of every row is one of the three legal ones.
//
// MUTATION: blank the Source of any row → red. MUTATION: give an unpriced row a Source without the
// "unpriced" prefix → red.
func TestPricingEveryRowHasSource(t *testing.T) {
	require.Equal(t, "2026-09-29", Version)
	count := map[string]int{}
	for _, p := range allProviders {
		seen := map[string]bool{}
		for _, m := range Catalogue(p) {
			count[p]++
			require.NotEmpty(t, strings.TrimSpace(m.Source), "%s/%s", p, m.Slug)
			require.Equal(t, p, m.Provider, "%s/%s", p, m.Slug)
			require.NotEmpty(t, m.Label, "%s/%s", p, m.Slug)
			require.False(t, seen[m.Slug], "duplicate slug %s/%s", p, m.Slug)
			seen[m.Slug] = true

			tokens := m.InputUSDPer1M.Valid || m.OutputUSDPer1M.Valid
			switch {
			case m.PerCallUSD.Valid:
				require.Equal(t, KindImage, m.Kind, "%s/%s", p, m.Slug)
				require.False(t, tokens, "%s/%s: a per-call row carries no token rates", p, m.Slug)
				require.True(t, m.PerCallUSD.Decimal.IsPositive(), "%s/%s", p, m.Slug)
			case tokens:
				require.Equal(t, KindChat, m.Kind, "%s/%s", p, m.Slug)
				require.True(t, m.InputUSDPer1M.Valid && m.OutputUSDPer1M.Valid, "%s/%s: both token rates or none", p, m.Slug)
				require.True(t, m.InputUSDPer1M.Decimal.IsPositive() && m.OutputUSDPer1M.Decimal.IsPositive(), "%s/%s", p, m.Slug)
			default:
				require.True(t, strings.HasPrefix(m.Source, "unpriced"), "%s/%s: a NULL row says so", p, m.Slug)
			}
			if strings.HasPrefix(m.Source, "unpriced") {
				require.False(t, tokens || m.PerCallUSD.Valid, "%s/%s: 'unpriced' with a price", p, m.Slug)
			}
			got, ok := Lookup(p, m.Slug)
			require.True(t, ok)
			require.Equal(t, m, got)
		}
	}
	// The A brief's 19 direct/OpenRouter rows + lane H2's 36 direct rows (21 openai, 6 anthropic, 9 google)
	// + apibost's 67 chat/image models (catalogue_apibost.go) + runblob's 8 unpriced image slugs (B-31).
	require.Equal(t, map[string]int{
		entity.AIProviderOpenAI: 25, entity.AIProviderAnthropic: 9, entity.AIProviderGoogle: 13,
		entity.AIProviderOpenRouter: 8, entity.AIProviderApibost: 67, entity.AIProviderRunblob: 8,
	}, count)
}

// TestPricingCatalogueMatchesTheBrief — the curated numbers, one by one, as 06-BRIEFS-A lists them
// (the sunburst row on OpenRouter carries the brief's figure for the same model on the direct row).
//
// MUTATION: change any one number in the catalogue → red.
func TestPricingCatalogueMatchesTheBrief(t *testing.T) {
	type row struct{ provider, slug, in, out, perCall string } // "" = NULL
	want := []row{
		{"openai", "gpt-5.2", "1.25", "10", ""},
		{"openai", "gpt-5-mini", "0.25", "2", ""},
		{"openai", "gpt-image-2", "", "", "0.053"},
		{"openai", "gpt-image-2.5-sunburst", "", "", "0.013"},
		{"anthropic", "claude-sonnet-5", "3", "15", ""},
		{"anthropic", "claude-opus-5-5", "5", "25", ""},
		{"anthropic", "claude-haiku-4-5-20251001", "1", "5", ""},
		{"google", "gemini-2.5-pro", "1.25", "10", ""},
		{"google", "gemini-2.5-flash", "0.30", "2.50", ""},
		{"google", "gemini-3-pro-image", "", "", "0.134"},
		{"google", "gemini-3.1-flash-lite", "", "", ""},
		{"openrouter", "anthropic/claude-sonnet-5", "3", "15", ""},
		{"openrouter", "anthropic/claude-opus-5", "5", "25", ""},
		{"openrouter", "openai/gpt-5-mini", "0.25", "2", ""},
		{"openrouter", "google/gemini-3.1-flash-lite", "", "", ""},
		{"openrouter", "openai/gpt-image-2", "", "", "0.053"},
		{"openrouter", "openai/gpt-image-2.5-sunburst", "", "", "0.013"},
		{"openrouter", "google/gemini-3-pro-image", "", "", "0.134"},
		{"openrouter", "bytedance-seed/seedream-5-0-pro", "", "", "0.045"},
		{"apibost", "claude-fable-5-1", "8", "40", ""},
		{"apibost", "claude-sonnet-5", "2", "10", ""},
		{"apibost", "claude-haiku-4-5-20251001", "1", "5", ""},
		{"apibost", "gemini-2.5-flash", "0.3", "2.5", ""},
		{"apibost", "gemini-2.5-pro", "1.25", "10", ""},
		{"apibost", "dall-e-3", "", "", "0.04"},
		{"apibost", "flux-kontext-pro", "", "", "0.08"},
		{"apibost", "gemini-2.5-flash-image", "", "", "0.02"},
	}
	eq := func(name, want string, got decimal.NullDecimal) {
		t.Helper()
		if want == "" {
			require.False(t, got.Valid, "%s: want NULL, got %s", name, got.Decimal)
			return
		}
		require.True(t, got.Valid, "%s: want %s, got NULL", name, want)
		require.True(t, decimal.RequireFromString(want).Equal(got.Decimal), "%s: want %s, got %s", name, want, got.Decimal)
	}
	for _, w := range want {
		m, ok := Lookup(w.provider, w.slug)
		require.True(t, ok, "%s/%s missing", w.provider, w.slug)
		eq(w.provider+"/"+w.slug+" in", w.in, m.InputUSDPer1M)
		eq(w.provider+"/"+w.slug+" out", w.out, m.OutputUSDPer1M)
		eq(w.provider+"/"+w.slug+" per call", w.perCall, m.PerCallUSD)
		require.False(t, m.CachedInputUSDPer1M.Valid, "%s/%s: no cache rate was sourced", w.provider, w.slug)
	}
	require.Contains(t, mustLookup(t, "anthropic", "claude-opus-5-5").Source, "UNVERIFIED")
	require.Contains(t, mustLookup(t, "anthropic", "claude-opus-5-5").Source, "$75/$75")
	require.Contains(t, mustLookup(t, "openai", "gpt-5.2").Source, "UNVERIFIED")
}

func mustLookup(t *testing.T, provider, slug string) Model {
	t.Helper()
	m, ok := Lookup(provider, slug)
	require.True(t, ok)
	return m
}

// TestPricingCatalogueIsACopy — a caller editing what Catalogue returned cannot re-price the table.
//
// MUTATION: Catalogue returns catalogue[provider] directly → red.
func TestPricingCatalogueIsACopy(t *testing.T) {
	rows := Catalogue(entity.AIProviderOpenAI)
	require.NotEmpty(t, rows)
	rows[0].InputUSDPer1M = usd("999")
	rows[0].Source = ""
	m, _ := Lookup(entity.AIProviderOpenAI, rows[0].Slug)
	require.False(t, m.InputUSDPer1M.Decimal.Equal(decimal.NewFromInt(999)))
	require.NotEmpty(t, Catalogue(entity.AIProviderOpenAI)[0].Source)
}

// TestPricingLookupStripsASnapshotDate — OpenAI reports the dated snapshot it served, and the ledger
// prices what was reported: "gpt-5-mini-2025-08-07" prices as the "gpt-5-mini" row (the row itself
// comes back, Source and all), through Price as well; anything else after the alias stays a miss —
// "-preview" may be another model at another price, and eleven characters shaped like a date that is
// not one are not a snapshot.
//
// MUTATIONS (each measured red → restored green): Lookup without the snapshot step → the dated rows
// go red; snapshotAlias cutting at the LAST "-" (any suffix) → the "-preview" / "-mini-high" rows go
// red; snapshotAlias without the time.Parse check (any "-dddd-dd-dd"-shaped tail) → the impossible-date
// row goes red.
func TestPricingLookupStripsASnapshotDate(t *testing.T) {
	alias := mustLookup(t, entity.AIProviderOpenAI, "gpt-5-mini")
	got, ok := Lookup(entity.AIProviderOpenAI, "gpt-5-mini-2025-08-07")
	require.True(t, ok, "a dated snapshot is priced by its alias's row")
	require.Equal(t, alias, got)

	u := Usage{Prompt: 1000, Completion: 500}
	dated, src := Price(entity.AIProviderOpenAI, "gpt-5-mini-2025-08-07", u)
	requireUSD(t, "0.00125", dated, src) // (1000 × 0.25 + 500 × 2) / 1e6 — the alias's rates
	plain, _ := Price(entity.AIProviderOpenAI, "gpt-5-mini", u)
	require.True(t, plain.Decimal.Equal(dated.Decimal))

	// The rule is the suffix, not the provider: a dated OpenRouter slug is its alias's fallback row.
	_, ok = Lookup(entity.AIProviderOpenRouter, "openai/gpt-5-mini-2025-08-07")
	require.True(t, ok)

	for _, slug := range []string{
		"gpt-5-mini-preview",    // a suffix that is not a date: maybe another model
		"gpt-5-mini-high",       // likewise
		"gpt-5-mini-2025-13-45", // shaped like a date, not a date
		"gpt-5-mini-20250807",   // Anthropic's dated form is not OpenAI's; not stripped
		"gpt-5-mini2025-08-07",  // no separator
		"-2025-08-07",           // nothing before the date
		"2025-08-07",
		"gpt-9000-2025-08-07", // the alias has no row either
	} {
		_, ok := Lookup(entity.AIProviderOpenAI, slug)
		require.False(t, ok, "%q must stay unpriced", slug)
		usd, src := Price(entity.AIProviderOpenAI, slug, u)
		requireNone(t, usd, src)
	}
}

// TestDefaultChatSlugsArePricedRows — the default of every direct chat provider, exactly as the brief
// chose them (for price, not power), each a PRICED chat row of its own provider, so a route row with no
// model is callable AND a defaulted call is never booked unpriced; OpenRouter (env defaults) and every
// provider that does not serve chat have none.
//
// MUTATIONS: the table emptied → red; a default that is not a row of its provider ("gpt-5-mini" on
// google) → red; an openrouter entry added → red.
func TestDefaultChatSlugsArePricedRows(t *testing.T) {
	want := map[string]string{
		entity.AIProviderOpenAI:    "gpt-5-mini",
		entity.AIProviderAnthropic: "claude-sonnet-5",
		entity.AIProviderGoogle:    "gemini-2.5-flash",
		entity.AIProviderApibost:   "claude-sonnet-5",
	}
	for _, p := range allProviders {
		slug, ok := DefaultChatSlug(p)
		w, has := want[p]
		require.Equal(t, has, ok, "%s: default present", p)
		require.Equal(t, w, slug, p)
		if !ok {
			continue
		}
		require.True(t, entity.AIProviderServes(p, entity.AICapabilityChat), "%s serves chat", p)
		m := mustLookup(t, p, slug)
		require.Equal(t, KindChat, m.Kind, "%s/%s", p, slug)
		usd, src := Price(p, slug, Usage{Prompt: 1000, Completion: 1000})
		require.True(t, usd.Valid, "%s/%s: a default must be priced", p, slug)
		require.Equal(t, entity.AICostTable, src)
	}
	_, ok := DefaultChatSlug(entity.AIProviderOpenRouter)
	require.False(t, ok, "openrouter's defaults are the env slugs, never this table")
}

// TestCatalogueRowsFitTheProvider — every row is a promise the router can keep: it sits under a known
// provider, names a capability that provider serves (entity.AIProviderCapabilities — a video row on
// apibost, which has no video transport, would be offered on a route and fail on every press), carries
// a label for the panel's datalist, a slug unique within its provider and free of padding, and a
// source: a priced row names where its number was read, an unpriced one says "unpriced".
//
// MUTATIONS: a veo row with KindVideo in apibostRows → red (apibost serves chat + image only); a row
// with Kind "vector" on openai → red; a duplicated slug → red; a priced row whose Source starts with
// "unpriced" or is blank → red; a catalogue key that is not a provider key → red.
func TestCatalogueRowsFitTheProvider(t *testing.T) {
	keys := map[string]bool{}
	for _, p := range entity.AIProviderKeys() {
		keys[p] = true
	}
	for p := range catalogue {
		require.True(t, keys[p], "catalogue key %q is not a provider (entity.AIProviderKeys)", p)
	}
	for _, p := range entity.AIProviderKeys() {
		seen := map[string]bool{}
		for _, m := range Catalogue(p) {
			name := p + "/" + m.Slug
			require.Equal(t, p, m.Provider, name)
			require.NotEmpty(t, m.Slug, name)
			require.Equal(t, strings.TrimSpace(m.Slug), m.Slug, "%s: padded slug", name)
			require.False(t, seen[m.Slug], "duplicate slug %s", name)
			seen[m.Slug] = true
			require.True(t, entity.IsAICapability(m.Kind), "%s: kind %q is not a capability", name, m.Kind)
			require.True(t, entity.AIProviderServes(p, m.Kind), "%s: %s does not serve %q", name, p, m.Kind)
			require.NotEmpty(t, strings.TrimSpace(m.Label), "%s: no label", name)
			priced := m.PerCallUSD.Valid || m.InputUSDPer1M.Valid || m.OutputUSDPer1M.Valid
			if priced {
				require.NotEmpty(t, strings.TrimSpace(m.Source), "%s: a priced row names its source", name)
				require.False(t, strings.HasPrefix(m.Source, "unpriced"), "%s: priced but says unpriced", name)
			} else {
				require.True(t, strings.HasPrefix(m.Source, "unpriced"), "%s: an unpriced row says so", name)
			}
		}
	}
}
