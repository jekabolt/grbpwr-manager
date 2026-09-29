// The rows below are transcribed mechanically from tmp/plans/ai-providers/catalogue/runblob.json (lane H2,
// 2026-09-29): one row per model, in a fixed order. Change a row together with its source, not alone.

package pricing

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// runblob — every family and model runblob serves, read 2026-09-29 from https://docs.runblob.io (the
// family pages: kling, kling-o1/o3 video, kling-image, nano-banana, chatgpt-image, seedance) and the
// runblob-llm-spec files in tmp/plans/ai-providers/runblob-specs. There is no veo family (the docs'
// search index lists none).
//
// Convention: EVERY row is UNPRICED (srcRunblob). runblob states the price of each generation at submit
// and the transports book that number as cost_source provider; the dollar figures the docs print are
// examples for one parameter set (a 5 s 720p clip, one 2K picture), not tariffs, and a row may only
// mention them.
//
// Slugs are what the transports send: an IMAGE row is `family/model` as the image transport spells it
// (runblob.ImageSlugs — `gemini/pro`; the Kling photo endpoints are their own path, `kling/o1-photo`); a
// VIDEO row is the bare model value the video route names and /v1/kling/generate receives as `model`
// (designgen.DefaultVideoModel = kling_2.5_turbo). 10 image + 21 video.
const (
	srcRunblobH5        = srcRunblob + "; the chatgpt-images family is wired into the image transport by lane H5"
	srcRunblobVideo     = srcRunblob + "; video: sent as `model` on /v1/kling/generate"
	srcRunblobKlingOmni = srcRunblob + "; video: the Kling omni endpoints (/v1/kling/o1-video, /v1/kling/o3-video) — lane H5 routes " +
		"them by slug"
	srcRunblobSeedance = srcRunblob + "; video: /v1/seedance/generate, billed per second — lane H5 routes the seedance family by " +
		"slug"
)

// NOT LISTED (Codex REVIEW-H #5, #7; designgen.IsVideoModel refuses them at the door): the four Kling
// Motion models (kling_3_motion[_pro], kling_2.6_motion[_pro]) need a reference VIDEO the route's body
// does not carry, and the four Seedance models submit with NO price (a per-second hold settled later,
// behind a cabinet JWT) — a row here is a promise the router can keep and the ledger can price.
var runblobRows = []Model{
	unpricedRow(entity.AIProviderRunblob, "gemini/standard", "Nano Banana (standard)", KindImage, srcRunblob+" — docs example $0.021, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "gemini/pro", "Nano Banana Pro", KindImage, srcRunblob),
	unpricedRow(entity.AIProviderRunblob, "gemini/v2", "Nano Banana 2", KindImage, srcRunblob),
	unpricedRow(entity.AIProviderRunblob, "gemini/v2_lite", "Nano Banana 2 Lite", KindImage, srcRunblob),
	unpricedRow(entity.AIProviderRunblob, "gemini/pro_vip", "Nano Banana Pro VIP", KindImage, srcRunblob),
	unpricedRow(entity.AIProviderRunblob, "gemini/v2_vip", "Nano Banana 2 VIP", KindImage, srcRunblob),
	unpricedRow(entity.AIProviderRunblob, "kling/o1-photo", "Kling O1 Photo", KindImage, srcRunblob+" — docs example $0.029, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "kling/o3-photo", "Kling O3 Photo", KindImage, srcRunblob+" — docs example $0.05, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "chatgpt-images/gpt-5-2", "ChatGPT Images (GPT-5.2)", KindImage, srcRunblobH5+" — docs example $0.039, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "chatgpt-images/chatgpt-2.5", "ChatGPT Images 2.5", KindImage, srcRunblobH5),
	unpricedRow(entity.AIProviderRunblob, "kling_3", "Kling 3.0", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_3_pro", "Kling 3.0 Pro", KindVideo, srcRunblobVideo+" — docs example $0.29, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "kling_2.6", "Kling 2.6 (Pro quality)", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_2.5_turbo", "Kling 2.5 Turbo (Std quality)", KindVideo, srcRunblobVideo+" — docs example $0.25, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "kling_2.5_turbo_pro", "Kling 2.5 Turbo Pro", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_2.1", "Kling 2.1 (Std)", KindVideo, srcRunblobVideo+" — image-to-video only (a text-only request is refused with 400)"),
	unpricedRow(entity.AIProviderRunblob, "kling_2.1_pro", "Kling 2.1 Pro", KindVideo, srcRunblobVideo+" — image-to-video only (a text-only request is refused with 400)"),
	unpricedRow(entity.AIProviderRunblob, "kling_2.1_master", "Kling 2.1 Master", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_1.6", "Kling 1.6 (Std)", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_1.6_pro", "Kling 1.6 Pro", KindVideo, srcRunblobVideo),
	unpricedRow(entity.AIProviderRunblob, "kling_o1", "Kling O1 Video (omni)", KindVideo, srcRunblobKlingOmni+" — docs example $0.9, not a tariff"),
	unpricedRow(entity.AIProviderRunblob, "kling_o3", "Kling O3 Video (std)", KindVideo, srcRunblobKlingOmni),
	unpricedRow(entity.AIProviderRunblob, "kling_o3_pro", "Kling O3 Video Pro", KindVideo, srcRunblobKlingOmni+" — docs example $1.8, not a tariff"),
}
