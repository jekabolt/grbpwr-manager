// The rows below are transcribed mechanically from tmp/plans/ai-providers/catalogue/fal.json (lane H2,
// 2026-09-29): one row per model, in a fixed order. Change a row together with its source, not alone.

package pricing

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// fal — the models fal hosts that a route here can actually call, read 2026-09-29 from
// https://fal.ai/models (one page per endpoint) and https://fal.ai/pricing; each request body was
// checked against fal's OpenAPI input schema (https://fal.ai/api/openapi/queue/openapi.json?endpoint_id=…).
//
// Slug = the endpoint id EXACTLY as fal names it — `openai/gpt-image-2` and `bytedance/seedream/v5/…` carry
// no fal-ai/ prefix, the Meshy default is `meshy/v7/multi-image-to-3d` (fal.DefaultModel3D) — and every
// slug matches designgen's falSlugRe (a test there walks this list).
//
// Convention: a row is PRICED only when fal states one flat figure per image / per generation (a per-call
// row, PerCallUSD × pictures). Per-megapixel, per-compute-second, token-based and size/quality-tiered
// prices are UNPRICED with the unit in the note: fal's ledger line is x-fal-billable-units × the route's
// tariff (FAL_UNIT_USD*), not this table.
//
// Kinds: image (24) = image.generate through the generic fal transport, text-to-image plus the
// reference-guided generators (kontext, */edit); edit (3) = only the slugs the extend / inpaint
// routes build a body for; cutout (4); threed (4) = the multi-image Meshy family
// (the `image_urls` body) and hitem3d (named slots).
//
// Dropped on purpose: every video row (no fal video transport here; runblob serves video); Imagen 4 Fast
// (404 on fal); Recraft V3 (Recraft is removed); FASHN try-on (model_image/garment_image — no route
// builds that body); the single-image Meshy endpoints (meshy/v7/image-to-3d, fal-ai/meshy/v6/image-to-3d,
// fal-ai/meshy/v6-preview/image-to-3d — they require `image_url`, the 3D route sends `image_urls`);
// hunyuan3d / trellis / tripo3d / rodin (each its own request body, which the 3D route does not build).
const (
	srcFal         = "fal, read 2026-09-29"
	srcFalUnpriced = "unpriced — fal, read 2026-09-29: "
)

var falRows = []Model{
	unpricedRow(entity.AIProviderFal, "fal-ai/flux-pro/v1.1", "FLUX 1.1 [pro]", KindImage, srcFalUnpriced+"$0.04 per megapixel (https://fal.ai/models/fal-ai/flux-pro/v1.1) — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/flux-pro/v1.1-ultra", "FLUX 1.1 [pro] ultra", KindImage, "0.06", srcFal+": https://fal.ai/models/fal-ai/flux-pro/v1.1-ultra: $0.06 per image"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux/dev", "FLUX.1 [dev]", KindImage, srcFalUnpriced+"$0.025 per megapixel (https://fal.ai/models/fal-ai/flux/dev) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux/schnell", "FLUX.1 [schnell]", KindImage, srcFalUnpriced+"$0.003 per megapixel (https://fal.ai/models/fal-ai/flux/schnell) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux-2", "FLUX.2 [dev]", KindImage, srcFalUnpriced+"$0.012 per megapixel (https://fal.ai/models/fal-ai/flux-2) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/gpt-image-1/text-to-image", "GPT Image 1", KindImage, srcFalUnpriced+"$0.011 per image (low, 1024x1024; tiered up to $0.25 high quality non-square) (https://fal.ai/models/fal-ai/gpt-image-1/text-to-image) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "openai/gpt-image-2", "GPT Image 2 (alpha)", KindImage, srcFalUnpriced+"$0.006 per image (1024x1024, low quality; tiered up to $0.401 at 3840x2160 high quality) (https://fal.ai/models/openai/gpt-image-2) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "openai/gpt-image-2.5/sunburst/text-to-image", "GPT Image 2.5 Sunburst", KindImage, srcFalUnpriced+"token-based: $5/1M input text tokens, $8/1M input image tokens, $30/1M output image tokens (https://fal.ai/models/openai/gpt-image-2.5/sunburst/text-to-image) — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/nano-banana", "Nano Banana (Gemini 2.5 Flash Image)", KindImage, "0.039", srcFal+": https://fal.ai/models/fal-ai/nano-banana: $0.039 per image"),
	perCall(entity.AIProviderFal, "fal-ai/bytedance/seedream/v4/text-to-image", "Seedream 4.0", KindImage, "0.03", srcFal+": https://fal.ai/models/fal-ai/bytedance/seedream/v4/text-to-image: $0.03 per image"),
	unpricedRow(entity.AIProviderFal, "bytedance/seedream/v5/pro/text-to-image", "Seedream 5.0 Pro", KindImage, srcFalUnpriced+"$0.0675 per image (<=1536x1536; $0.135 for 1536-2048px) (https://fal.ai/models/bytedance/seedream/v5/pro/text-to-image) — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/ideogram/v2", "Ideogram V2", KindImage, "0.08", srcFal+": https://fal.ai/models/fal-ai/ideogram/v2: $0.08 per image"),
	unpricedRow(entity.AIProviderFal, "fal-ai/qwen-image", "Qwen Image", KindImage, srcFalUnpriced+"$0.02 per megapixel (https://fal.ai/models/fal-ai/qwen-image) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/hidream-i1-full", "HiDream I1 Full", KindImage, srcFalUnpriced+"$0.05 per megapixel (https://fal.ai/models/fal-ai/hidream-i1-full) — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/stable-diffusion-v35-large", "Stable Diffusion 3.5 Large", KindImage, srcFalUnpriced+"$0.065 per megapixel (https://fal.ai/models/fal-ai/stable-diffusion-v35-large) — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/flux-pro/kontext", "FLUX.1 Kontext [pro]", KindImage, "0.04", srcFal+": https://fal.ai/models/fal-ai/flux-pro/kontext: $0.04 per image; a reference-guided generator: takes a single `image_url`"),
	perCall(entity.AIProviderFal, "fal-ai/flux-pro/kontext/max", "FLUX.1 Kontext [max]", KindImage, "0.08", srcFal+": https://fal.ai/models/fal-ai/flux-pro/kontext/max: $0.08 per image; a reference-guided generator: takes a single `image_url`"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux-2/edit", "FLUX.2 [dev] Edit", KindImage, srcFalUnpriced+"$0.012 per megapixel (input + output, input resized to 1MP) (https://fal.ai/models/fal-ai/flux-2/edit); a reference-guided generator: takes `image_urls` — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/nano-banana/edit", "Nano Banana Edit", KindImage, "0.039", srcFal+": https://fal.ai/models/fal-ai/nano-banana/edit: $0.039 per image; a reference-guided generator: takes `image_urls`"),
	unpricedRow(entity.AIProviderFal, "fal-ai/gpt-image-1/edit-image", "GPT Image 1 Edit", KindImage, srcFalUnpriced+"$0.011 per image (low, 1024x1024; tiered up to $0.25 high quality non-square) (https://fal.ai/models/fal-ai/gpt-image-1/edit-image); a reference-guided generator: takes `image_urls` — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "openai/gpt-image-2/edit", "GPT Image 2 Edit", KindImage, srcFalUnpriced+"$0.015 per image (1024x1024 low quality; up to $0.219 high quality) (https://fal.ai/models/openai/gpt-image-2/edit); a reference-guided generator: takes `image_urls` — fal books billable units × tariff"),
	perCall(entity.AIProviderFal, "fal-ai/bytedance/seedream/v4/edit", "Seedream 4.0 Edit", KindImage, "0.03", srcFal+": https://fal.ai/models/fal-ai/bytedance/seedream/v4/edit: $0.03 per image; a reference-guided generator: takes `image_urls`"),
	unpricedRow(entity.AIProviderFal, "bytedance/seedream/v5/pro/edit", "Seedream 5.0 Pro Edit", KindImage, srcFalUnpriced+"$0.0675 per output image (<=1536x1536; +$0.0045 per additional reference image, up to 10) (https://fal.ai/models/bytedance/seedream/v5/pro/edit); a reference-guided generator: takes `image_urls` — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/qwen-image-edit", "Qwen Image Edit", KindImage, srcFalUnpriced+"$0.03 per megapixel (https://fal.ai/models/fal-ai/qwen-image-edit); a reference-guided generator: takes a single `image_url` — fal books billable units × tariff"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux-2-pro/outpaint", "FLUX.2 [pro] Outpaint", KindEdit, srcFalUnpriced+"per megapixel: $0.03 the first MP of output + $0.015 per extra MP of input and output (fal page, read 2026-09-27, internal/fal/generic.go); image.extend's default (FAL_MODEL_OUTPAINT)"),
	perCall(entity.AIProviderFal, "fal-ai/bria/expand", "Bria Expand (outpaint)", KindEdit, "0.04", srcFal+": https://fal.ai/models/fal-ai/bria/expand: $0.04 per generation, read 2026-09-27 (internal/fal/generic.go); image.extend's accepted alternative slug"),
	unpricedRow(entity.AIProviderFal, "fal-ai/flux-pro/v1/fill", "FLUX.1 [pro] Fill (inpaint)", KindEdit, srcFalUnpriced+"per megapixel: $0.05, rounded up to the nearest MP; image.inpaint's default (FAL_MODEL_FILL)"),
	unpricedRow(entity.AIProviderFal, "fal-ai/birefnet", "BiRefNet Background Removal", KindCutout, srcFalUnpriced+"per compute second (billed by actual processing time, not a flat per-image rate) — the cutout route books x-fal-billable-units × FAL_UNIT_USD_CUTOUT, not this table"),
	unpricedRow(entity.AIProviderFal, "fal-ai/birefnet/v2", "BiRefNet Background Removal V2", KindCutout, srcFalUnpriced+"per compute second — the cutout route books x-fal-billable-units × FAL_UNIT_USD_CUTOUT, not this table"),
	unpricedRow(entity.AIProviderFal, "fal-ai/bria/background/remove", "Bria Background Remove", KindCutout, srcFalUnpriced+"$0.018 per generation — the cutout route books x-fal-billable-units × FAL_UNIT_USD_CUTOUT, not this table"),
	unpricedRow(entity.AIProviderFal, "fal-ai/imageutils/rembg", "Rembg", KindCutout, srcFalUnpriced+"per compute second — the cutout route books x-fal-billable-units × FAL_UNIT_USD_CUTOUT, not this table"),
	unpricedRow(entity.AIProviderFal, "meshy/v7/multi-image-to-3d", "Meshy 7 Multi-Image to 3D", KindThreed, srcFalUnpriced+"the 3D route books x-fal-billable-units × FAL_UNIT_USD; the 3D route's default (fal.DefaultModel3D); fal.go's fallback is $1.20 a textured build (fal page, read 2026-09-02)"),
	unpricedRow(entity.AIProviderFal, "meshy/v7.1/multi-image-to-3d", "Meshy 7.1 Multi-Image to 3D", KindThreed, srcFalUnpriced+"the 3D route books x-fal-billable-units × FAL_UNIT_USD; the Meshy `image_urls` body (isMeshyFamily)"),
	unpricedRow(entity.AIProviderFal, "fal-ai/meshy/v5/multi-image-to-3d", "Meshy 5 Multi-Image to 3D", KindThreed, srcFalUnpriced+"the 3D route books x-fal-billable-units × FAL_UNIT_USD; the Meshy `image_urls` body (isMeshyFamily)"),
	unpricedRow(entity.AIProviderFal, "hitem3d/hi3d/v3.0/multi-view-to-3d", "Hitem3D 3.0 Multi-View to 3D", KindThreed, srcFalUnpriced+"the 3D route books x-fal-billable-units × FAL_UNIT_USD; the previous default; takes the named-slot body (front/back/left/right_image_url) the 3D route builds for every non-Meshy slug"),
}
