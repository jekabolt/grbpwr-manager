// The rows below are transcribed mechanically from tmp/plans/ai-providers/catalogue/apibost.json (lane H2,
// 2026-09-29): one row per model, in a fixed order. Change a row together with its source, not alone.

package pricing

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// apibost — every chat and image model apibost serves, read from its public New API pricing endpoint.
//
// Source: https://apibost.com/api/pricing, read 2026-09-29 (no auth).
// Convention (New API): quota_type=0 → USD per 1M input = model_ratio × 2, per 1M output = that ×
// completion_ratio; quota_type=1 → USD per request = model_price (one picture for an image row). Rows
// billed by billing_mode=tiered_expr are UNPRICED here: the flat ratio derives a meaningless $75/$75 for
// them, and the expression apibost evaluates is noted on the row instead.
//
// Dropped on purpose (24 of 91): embeddings, tts / whisper / moderation, speech-to-text
// (gpt-4o-transcribe), the legacy completion models (text-*-001) and every video model (veo*): apibost
// has no video, audio or embedding transport here, and a row is a promise the router can keep.
// Kept: 54 chat + 13 image.
const (
	srcApibostAPI      = "https://apibost.com/api/pricing, read 2026-09-29 (New API: model_ratio×2 per 1M in, ×completion_ratio out; model_price per request)"
	srcApibostUnpriced = "unpriced — https://apibost.com/api/pricing, read 2026-09-29: "
)

var apibostRows = []Model{
	chat(entity.AIProviderApibost, "claude-fable-5", "Claude Fable 5", "5", "25", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-fable-5-1", "Claude Fable 5.1", "8", "40", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-haiku-4-5-20251001", "Claude Haiku 4.5", "1", "5", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-opus-4-6", "Claude Opus 4.6", "5", "25", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-opus-4-6-thinking", "Claude Opus 4.6 (thinking)", "5", "25", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-opus-4-8", "Claude Opus 4.8", "5", "25", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-opus-4-8-thinking", "Claude Opus 4.8 (thinking)", "5", "25", srcApibostAPI),
	unpricedRow(entity.AIProviderApibost, "claude-opus-5-5", "Claude Opus 5.5", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 4 + c * 20)); the flat ratio's derived $75/$75 is not what it charges"),
	chat(entity.AIProviderApibost, "claude-sonnet-4-5-20250929", "Claude Sonnet 4.5", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-sonnet-4-5-20250929-thinking", "Claude Sonnet 4.5 (thinking)", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-sonnet-4-6", "Claude Sonnet 4.6", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-sonnet-4-6-thinking", "Claude Sonnet 4.6 (thinking)", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "claude-sonnet-5", "Claude Sonnet 5", "2", "10", srcApibostAPI),
	chat(entity.AIProviderApibost, "gemini-2.5-flash", "Gemini 2.5 Flash", "0.3", "2.5", srcApibostAPI),
	chat(entity.AIProviderApibost, "gemini-2.5-pro", "Gemini 2.5 Pro", "1.25", "10", srcApibostAPI),
	chat(entity.AIProviderApibost, "gemini-3.1-flash-lite", "Gemini 3.1 Flash-Lite", "0.25", "1.5", srcApibostAPI),
	chat(entity.AIProviderApibost, "gemini-3.1-flash-lite-preview", "Gemini 3.1 Flash-Lite (preview)", "0.25", "1.5", srcApibostAPI),
	chat(entity.AIProviderApibost, "gemini-3.1-pro-preview", "Gemini 3.1 Pro (preview)", "2", "12", srcApibostAPI),
	unpricedRow(entity.AIProviderApibost, "gemini-3.5-flash", "Gemini 3.5 Flash", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 1.5 + c * 9)); the flat ratio's derived $75/$300 is not what it charges"),
	unpricedRow(entity.AIProviderApibost, "gemini-3.7-flash", "Gemini 3.7 Flash", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 0.75 + c * 3.75)); the flat ratio's derived $75/$300 is not what it charges"),
	unpricedRow(entity.AIProviderApibost, "gemini-3.8-flash", "Gemini 3.8 Flash", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 0.75 + c * 3.75)); the flat ratio's derived $75/$300 is not what it charges"),
	unpricedRow(entity.AIProviderApibost, "gemini-omni-flash", "Gemini Omni Flash", KindChat,
		srcApibostUnpriced+"per-request usage-based billing (base example $0.075), not a token rate"),
	chat(entity.AIProviderApibost, "gpt-4.1", "GPT-4.1", "2", "8", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4.1-mini", "GPT-4.1 mini", "0.4", "1.6", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4.1-mini-2025-04-14", "GPT-4.1 mini (2025-04-14)", "0.4", "1.6", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4.1-nano", "GPT-4.1 nano", "0.1", "0.4", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4o", "GPT-4o", "2.5", "10", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4o-audio-preview", "GPT-4o Audio (preview)", "2.5", "10", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4o-mini", "GPT-4o mini", "0.15", "0.6", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-4o-mini-2024-07-18", "GPT-4o mini (2024-07-18)", "0.15", "0.6", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5", "GPT-5", "1.25", "10", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5-mini", "GPT-5 mini", "0.25", "2", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5-nano", "GPT-5 nano", "0.05", "0.4", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.2", "GPT-5.2", "1.75", "14", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.3-chat-latest", "GPT-5.3 Chat (latest)", "1.75", "14", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.4", "GPT-5.4", "2.5", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.4-mini", "GPT-5.4 mini", "0.75", "4.5", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.4-nano", "GPT-5.4 nano", "0.2", "1.25", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.5", "GPT-5.5", "5", "30", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.6-luna", "GPT-5.6 Luna", "0.2", "1.2", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.6-sol", "GPT-5.6 Sol", "5", "30", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-5.6-terra", "GPT-5.6 Terra", "2.5", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "gpt-6-astra", "GPT-6 Astra", "10", "50", srcApibostAPI),
	unpricedRow(entity.AIProviderApibost, "gpt-6-luna", "GPT-6 Luna", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 0.1 + c * 0.5)); the flat ratio's derived $75/$150 is not what it charges"),
	unpricedRow(entity.AIProviderApibost, "gpt-6-sol", "GPT-6 Sol", KindChat,
		srcApibostUnpriced+"apibost bills it by billing_mode=tiered_expr (tier(\"base\", p * 2 + c * 10)); the flat ratio's derived $75/$150 is not what it charges"),
	chat(entity.AIProviderApibost, "grok-3", "Grok 3", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "grok-3-deepsearch", "Grok 3 DeepSearch", "4", "4", srcApibostAPI),
	chat(entity.AIProviderApibost, "grok-4", "Grok 4", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "grok-4-fast", "Grok 4 Fast", "0.2", "1", srcApibostAPI),
	chat(entity.AIProviderApibost, "grok-4.1", "Grok 4.1", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "grok-4.1-thinking", "Grok 4.1 (thinking)", "3", "15", srcApibostAPI),
	chat(entity.AIProviderApibost, "o3", "o3", "2", "8", srcApibostAPI),
	chat(entity.AIProviderApibost, "o3-mini", "o3-mini", "1.1", "4.4", srcApibostAPI),
	chat(entity.AIProviderApibost, "o4-mini", "o4-mini", "1.1", "1.1", srcApibostAPI),
	image(entity.AIProviderApibost, "dall-e-3", "DALL·E 3", "0.04", srcApibostAPI),
	image(entity.AIProviderApibost, "flux", "FLUX", "0.015", srcApibostAPI),
	image(entity.AIProviderApibost, "flux-kontext-max", "FLUX Kontext Max", "0.13", srcApibostAPI),
	image(entity.AIProviderApibost, "flux-kontext-pro", "FLUX Kontext Pro", "0.08", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-2.5-flash-image", "Gemini 2.5 Flash Image", "0.02", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-3-pro-image-preview", "Gemini 3 Pro Image (preview)", "0.13", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-3-pro-image-preview-4k", "Gemini 3 Pro Image 4K (preview)", "0.25", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-3.1-flash-image", "Gemini 3.1 Flash Image", "0.02", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-3.1-flash-image-preview", "Gemini 3.1 Flash Image (preview)", "0.04", srcApibostAPI),
	image(entity.AIProviderApibost, "gemini-3.1-flash-lite-image", "Gemini 3.1 Flash-Lite Image", "0.02", srcApibostAPI),
	image(entity.AIProviderApibost, "gpt-image-1.5", "GPT Image 1.5", "0.16", srcApibostAPI),
	image(entity.AIProviderApibost, "gpt-image-2", "GPT Image 2", "0.16", srcApibostAPI),
	image(entity.AIProviderApibost, "grok-3-image", "Grok 3 Image", "0.07", srcApibostAPI),
}
