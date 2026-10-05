package designgen

import (
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
)

// ═══ THE ENGINE TABLE — ONE SERVER TABLE FOR THE DOOR, THE RESERVE, THE BAND AND THE WORKER ═══
//
// A per-run engine (DesignRunParams.image, PLAYGROUND phase 2) is a model slug, a quality tier, an
// aspect ratio and a background. Every one of those words is checked against THIS table at the
// door (apisrv/admin design_engine.go), priced from it, advertised from it
// (GetDesignBandResponse.image_models) and resolved from it here, in the worker. Four readers of
// one table, never four tables: a tier the band offers and the door prices differently is the
// money defect this package keeps writing comments about.
//
// ⚠ THE CEILINGS ARE ABSOLUTE AND PER ENGINE + TIER, NOT A FACTOR OVER SOMETHING ELSE (Codex S-02
// §4). A factor over «the render base» is two numbers that drift; a dollar figure next to the tier
// that buys it is one.

// Tier dials: which provider field a UI tier moves.
const (
	TierDialQuality    = "quality"
	TierDialResolution = "resolution"
)

// Tier is one position of an engine's price dial as the person sees it (UI) and as the provider
// receives it (Dial + Value). CeilingUSD is the most one output of this tier may cost.
type Tier struct {
	UI         string
	Dial       string
	Value      string
	CeilingUSD decimal.Decimal
}

// Engine is one image model the door accepts in params.image.
type Engine struct {
	Slug  string
	Label string
	// Ratios the provider takes for this slug; `auto` included.
	Ratios []string
	// Tiers in ascending price order.
	Tiers []Tier
	// MaxRefs — the most reference pictures ONE call may carry.
	MaxRefs int
	// Backgrounds a person may ask for beyond the provider's default ('' is always legal).
	Backgrounds []string
	// InputUSD — the reserve per reference picture a call carries (input tokens).
	InputUSD decimal.Decimal
	// MaxN — the most pictures ONE call may ask for (the catalogue's `n` range). Every route builds
	// n = 1 calls today (imageCalls); the image route refuses a call above it before paying.
	MaxN int
	// NoRouteDefaults — the slug's catalogue lists neither `background` nor `output_format`, so the
	// route's own words for them (the kind's `opaque`, `png`) are not sent (images.go). A stated
	// background is still refused at the door by Backgrounds. False on the GPT rows: today's bytes.
	NoRouteDefaults bool
	// IsDefault marks the deployment's own slug (OPENROUTER_MODEL_IMAGE): the engine a run with no
	// params.image — and a params.image with no model — is drawn by.
	IsDefault bool
}

// Engine slugs. GPT Image rows are phase 2; Gemini / Seedream rows are phase 3 (B-16), each behind
// its own flag (EngineFlags).
const (
	EngineGPTImage2  = "openai/gpt-image-2"
	EngineGPTImage25 = "openai/gpt-image-2.5-sunburst"
	// EngineGPTImage25Flare — the FLAT route's engine (tmp/plans/flat-consistency rounds 3–7: the only
	// slug that drew card 38's construction right; measured $0.049 per 16:9 sheet through OpenRouter's
	// /images). FlatDefaultEngine names it.
	EngineGPTImage25Flare = "openai/gpt-image-2.5-flare"
	EngineGemini3Pro      = "google/gemini-3-pro-image"
	EngineSeedream5Pro    = "bytedance-seed/seedream-5-0-pro"
)

// EngineFlags — the rows that are the owner's money decision (DESIGN_ENGINE_GEMINI,
// DESIGN_ENGINE_SEEDREAM; Config.EngineFlags). Both OFF by default: a row is listed — advertised by
// the band, accepted and priced by the door — only while its flag is on.
type EngineFlags struct {
	Gemini   bool
	Seedream bool
}

// engineRatios — the GPT Image aspect enum (orimages.Request.AspectRatio, measured on gpt-image-2).
var engineRatios = []string{"auto", "9:16", "1:1", "3:4", "2:3", "16:9", "4:3", "3:2", "21:9"}

// gptTiers — low / medium / high on the `quality` dial, same word on the wire.
//
// high = $0.32, which is today's freeform / render reserve (0.08 × the ×4 quality ceiling): the
// top tier of an engine may not reserve less than the default path it replaces. 2.5's published
// prices are lower but UNMEASURED, so it inherits the same ceilings. `xhigh` / `max` are not
// offered in phase 2.
func gptTiers() []Tier {
	return []Tier{
		{UI: "low", Dial: TierDialQuality, Value: "low", CeilingUSD: decimal.RequireFromString("0.03")},
		{UI: "medium", Dial: TierDialQuality, Value: "medium", CeilingUSD: decimal.RequireFromString("0.10")},
		{UI: "high", Dial: TierDialQuality, Value: "high", CeilingUSD: decimal.RequireFromString("0.32")},
	}
}

// EngineReferenceReserveUSD — the reserve per reference picture ONE call carries (input tokens),
// on every engine row. $0.01 is a placeholder, not a measurement: OpenRouter forces
// input_fidelity=high on gpt-image-2, whose per-picture input tokens are unpublished, so the G-02
// beta smoke measures one `low` call with eight references (usage.cost) and this is the one number
// that measurement replaces (Fable m-1). Accounting only — there is no daily cap (migration 0358).
const EngineReferenceReserveUSD = "0.01"

var engineInputUSD = decimal.RequireFromString(EngineReferenceReserveUSD)

// gptMaxN — the catalogue's `n` range on every GPT Image slug (1..10, /api/v1/images/models).
const gptMaxN = 10

// gptEngine is the row shape of every GPT Image slug.
func gptEngine(slug, label string, backgrounds []string) Engine {
	return Engine{
		Slug:        slug,
		Label:       label,
		Ratios:      append([]string(nil), engineRatios...),
		Tiers:       gptTiers(),
		MaxRefs:     orimages.MaxInputReferences,
		Backgrounds: backgrounds,
		InputUSD:    engineInputUSD,
		MaxN:        gptMaxN,
	}
}

// ═══ THE PHASE-3 ROWS (B-16): GEMINI 3 PRO IMAGE AND SEEDREAM 5 PRO ═══
//
// Every value below was read from the live OpenRouter catalogue on 2026-09-27:
//   - https://openrouter.ai/api/v1/images/models (supported_parameters: resolution, aspect_ratio,
//     n, input_references — and NO quality, background or output_format on either slug);
//   - https://openrouter.ai/api/v1/images/models/<slug>/endpoints (pricing);
//   - Gemini's token counts: https://ai.google.dev/gemini-api/docs/pricing (Gemini 3 Pro Image:
//     output $120/M image tokens, 1K/2K = 1120 tokens ≈ $0.134, 4K = 2000 tokens ≈ $0.24; input
//     $2/M, one picture = 560 tokens ≈ $0.0011; text and thinking output $12/M);
//   - Seedream's variant: https://www.atlascloud.ai/blog/ai-updates/seedream-5-0-pro-price (output
//     ≤ 2.36 MP = $0.045, above = $0.09 — the catalogue's `high_resolution` variant — so 1K is the
//     base price and 2K the variant; each reference $0.003, the first one free).
//
// ⚠ THE CEILING RULE, AND WHY IT IS STATED HERE. A run reserves CeilingUSD(tier) + InputUSD ×
// pictures per call (admin designEstimateForRun), so the worst charge — the published output price
// at that tier plus MaxRefs published input prices — is covered when, for every tier,
//
//	CeilingUSD ≥ 1.3 × published output price   and   InputUSD ≥ published price of one input picture.
//
// The 1.3 is the headroom for what the catalogue does not price per picture: Gemini's prompt text
// ($2/M) and thinking tokens ($12/M) — 0.066 at 1K/2K is ≈ 5 500 thinking tokens — and a rounding
// of Seedream's pixel tier. engines_test.go holds the published table and fails a ceiling under it.
// Tiers are non-decreasing per row. The `≥ 0.32` floor of the tests is the DEFAULT engine's top tier
// only, and the default is a GPT row (EngineTable).
//
// The dial is `resolution` on both (the catalogue's own enum), never `quality`: applyImageOptions
// clears the quality word for these rows and the wire omits it (orimages buildRequest).

// geminiRatios — the catalogue's aspect_ratio enum for google/gemini-3-pro-image, which has NO
// `auto`: an unstated format sends no aspect_ratio at all (the provider's default).
var geminiRatios = []string{"9:16", "1:1", "4:5", "3:4", "2:3", "16:9", "4:3", "3:2", "5:4", "21:9"}

// seedreamRatios — the catalogue's enum for bytedance-seed/seedream-5-0-pro ∩ the playground grid
// (FORMAT_RATIOS). The catalogue also lists 1:2 2:1 9:19.5 19.5:9 9:20 20:9, which the grid cannot
// draw — not advertised, so not accepted.
var seedreamRatios = []string{"auto", "9:16", "1:1", "4:5", "3:4", "2:3", "16:9", "4:3", "3:2", "5:4", "21:9", "9:21"}

func geminiEngine() Engine {
	return Engine{
		Slug:   EngineGemini3Pro,
		Label:  "Gemini 3 Pro Image",
		Ratios: append([]string(nil), geminiRatios...),
		Tiers: []Tier{
			// 1K and 2K are the same 1120 output tokens ≈ $0.134 → 0.20 ≥ 1.3 × 0.134 = 0.175.
			{UI: "low", Dial: TierDialResolution, Value: "1K", CeilingUSD: decimal.RequireFromString("0.20")},
			{UI: "medium", Dial: TierDialResolution, Value: "2K", CeilingUSD: decimal.RequireFromString("0.20")},
			// 4K = 2000 tokens ≈ $0.24 → 0.32 ≥ 1.3 × 0.24 = 0.312.
			{UI: "high", Dial: TierDialResolution, Value: "4K", CeilingUSD: decimal.RequireFromString("0.32")},
		},
		MaxRefs:         14,
		MaxN:            1,
		InputUSD:        decimal.RequireFromString("0.01"), // ≥ 560 tokens × $2/M ≈ $0.0011
		NoRouteDefaults: true,
	}
}

func seedreamEngine() Engine {
	return Engine{
		Slug:   EngineSeedream5Pro,
		Label:  "Seedream 5 Pro",
		Ratios: append([]string(nil), seedreamRatios...),
		// No `medium`: the catalogue's resolution enum is 1K | 2K. A `medium` is quality_not_supported
		// at the door, never a silent 1K or 2K.
		Tiers: []Tier{
			{UI: "low", Dial: TierDialResolution, Value: "1K", CeilingUSD: decimal.RequireFromString("0.08")},  // ≥ 1.3 × 0.045
			{UI: "high", Dial: TierDialResolution, Value: "2K", CeilingUSD: decimal.RequireFromString("0.15")}, // ≥ 1.3 × 0.09
		},
		MaxRefs:         14,
		MaxN:            1,
		InputUSD:        decimal.RequireFromString("0.003"), // the published $0.003 per reference
		NoRouteDefaults: true,
	}
}

// engineCatalogue — EVERY row this package knows, flags ignored, no default marked. It is not what the
// door accepts (that is EngineTable); it is how the WORKER reads a frozen slug's dial and wire shape,
// so a run the door accepted while a flag was on is still sent with its own resolution word after
// the flag went off (and by a worker whose table was built without flags).
func engineCatalogue() []Engine {
	return []Engine{
		gptEngine(EngineGPTImage2, "GPT Image 2", nil),
		// `transparent` on 2.5 through OpenRouter is UNVERIFIED (12-PROVIDERS §E): G-02 measures it,
		// and a 400 there deletes this entry.
		gptEngine(EngineGPTImage25, "GPT Image 2.5", []string{"transparent"}),
		// The flat route's default (FlatDefaultEngine). No `transparent`: unmeasured on this slug.
		gptEngine(EngineGPTImage25Flare, "GPT Image 2.5 Flare", nil),
		geminiEngine(),
		seedreamEngine(),
	}
}

// catalogueEngine looks a slug up in engineCatalogue; an empty slug is not a row here.
func catalogueEngine(slug string) (Engine, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return Engine{}, false
	}
	for _, e := range engineCatalogue() {
		if e.Slug == slug {
			return e, true
		}
	}
	return Engine{}, false
}

// defaultCapable — whether a row may be the deployment default. In phase 3 only a GPT Image row may:
// an unnamed run is reserved at the default's top tier and sent with the deployment's QUALITY word,
// which means nothing to a resolution engine, and the kind tables (designEstimateFor) were sized on
// GPT. A Gemini / Seedream OPENROUTER_MODEL_IMAGE empties the table like any other unknown default.
func defaultCapable(e Engine) bool {
	return e.dial() == TierDialQuality && !e.NoRouteDefaults
}

// EngineTable — the engines this deployment accepts, the default first and marked IsDefault.
//
// defaultSlug is the image client's effective slug (orimages.Client.Model); empty reads as
// orimages.DefaultModel.
//
// ⚠ A DEFAULT SLUG THAT IS NOT A ROW (a custom OPENROUTER_MODEL_IMAGE) EMPTIES THE TABLE (G-02,
// Codex 2). It used to get a row shaped like gpt-image-2 — GPT ratios, 16 references, GPT ceilings —
// under its own slug, so `openai/gpt-image-1-mini` was advertised with 21:9 (it draws 1:1 3:2 2:3
// auto), accepted, reserved, and refused by the provider. Nothing here knows another model's ratios,
// reference ceiling or price, and a guess wearing a table's authority is the defect. So the table
// fails closed: the band advertises no engine (the client draws no picker and sends no
// params.image), the door refuses any params.image (unknown_image_model), and an unnamed run is
// reserved by the kind's own table (designEstimateFor) and sent exactly as before phase 2 — the
// deployment default, no model/ratio/tier words. app.go warns at boot. Adding the slug's own
// measured row here is how a new default gets its picker.
//
// FLAGS (B-16). The Gemini and Seedream rows are listed only while their flag is on; flags are
// optional so a caller that passes none (buildJob, tests) sees the flags-off table — the fail-closed
// direction. Several EngineFlags are OR-ed. The worker's dispatch reads Config.Engines — app.go's
// EngineTableFunc over Config.EngineFlags(), the same function it hands the door and the band (B-13): a frozen flagged slug is still READ through
// engineCatalogue (its dial), and REFUSED before any money when its flag is off at the pickup
// (engineOffAtSubmit, G-03 Codex 6 — the flag is the owner's spend switch). A flagged row is never
// the default (defaultCapable): a Gemini / Seedream defaultSlug empties the table whatever the flags
// say.
//
// A fresh slice on every call: the band puts it on the wire and the door filters it.
func EngineTable(defaultSlug string, flags ...EngineFlags) []Engine {
	def := strings.TrimSpace(defaultSlug)
	if def == "" {
		def = orimages.DefaultModel
	}
	var on EngineFlags
	for _, f := range flags {
		on.Gemini = on.Gemini || f.Gemini
		on.Seedream = on.Seedream || f.Seedream
	}
	rows := make([]Engine, 0, 4)
	for _, e := range engineCatalogue() {
		switch e.Slug {
		case EngineGemini3Pro:
			if !on.Gemini {
				continue
			}
		case EngineSeedream5Pro:
			if !on.Seedream {
				continue
			}
		}
		rows = append(rows, e)
	}
	at := -1
	for i := range rows {
		if rows[i].Slug == def && defaultCapable(rows[i]) {
			at = i
		}
	}
	if at < 0 {
		return []Engine{}
	}
	rows[at].IsDefault = true
	out := make([]Engine, 0, len(rows))
	out = append(out, rows[at])
	for i := range rows {
		if i != at {
			out = append(out, rows[i])
		}
	}
	return out
}

// FindEngine looks a slug up; an empty slug answers the default row.
func FindEngine(table []Engine, slug string) (Engine, bool) {
	slug = strings.TrimSpace(slug)
	for _, e := range table {
		if (slug == "" && e.IsDefault) || (slug != "" && e.Slug == slug) {
			return e, true
		}
	}
	return Engine{}, false
}

// Tier looks a UI tier word up. An empty word is not a tier (the caller decides what «not stated» means).
func (e Engine) Tier(ui string) (Tier, bool) {
	for _, t := range e.Tiers {
		if t.UI == ui {
			return t, true
		}
	}
	return Tier{}, false
}

// Accepts reports whether the engine takes the aspect ratio (empty is always legal: provider default).
func (e Engine) Accepts(ratio string) bool {
	if ratio == "" {
		return true
	}
	for _, r := range e.Ratios {
		if r == ratio {
			return true
		}
	}
	return false
}

// Background reports whether the engine takes the background (empty is always legal).
func (e Engine) Background(b string) bool {
	if b == "" {
		return true
	}
	for _, v := range e.Backgrounds {
		if v == b {
			return true
		}
	}
	return false
}

// CeilingUSD — the most one output of tier `ui` may cost; an empty tier (or a word that is not a tier) → the
// most expensive tier, because an unstated tier falls back to the deployment's dial, which the
// reserve cannot read (the same argument as the admin's designImageQualityCeiling).
func (e Engine) CeilingUSD(ui string) decimal.Decimal {
	if t, ok := e.Tier(ui); ok {
		return t.CeilingUSD
	}
	out := decimal.Zero
	for _, t := range e.Tiers {
		if t.CeilingUSD.GreaterThan(out) {
			out = t.CeilingUSD
		}
	}
	return out
}

// dial — the provider field this engine's tiers move (every phase-2 row: quality).
func (e Engine) dial() string {
	if len(e.Tiers) > 0 {
		return e.Tiers[0].Dial
	}
	return TierDialQuality
}

// imageOptionsStated — whether the frozen run named an engine at all. An empty block is the same
// run as no block: the deployment's dial.
func imageOptionsStated(o *imageOptions) bool {
	return o != nil && (strings.TrimSpace(o.Model) != "" || strings.TrimSpace(o.Quality) != "" ||
		strings.TrimSpace(o.AspectRatio) != "" || strings.TrimSpace(o.Background) != "")
}

// applyImageOptions puts the frozen engine on the job. Called by buildJob only.
//
//   - model: the frozen slug; an empty one leaves Job.Model empty, which is the client's own slug — the
//     default row — so the tier below is read off that row;
//   - tier: moves the engine's dial (quality → Job.Quality, resolution → Job.Resolution); an
//     unstated tier keeps the deployment's quality word on a quality engine;
//   - a slug or tier the table no longer lists (a redeploy between the door and the pass) is sent
//     VERBATIM, never swapped for something else: the door priced and froze exactly these words.
func applyImageOptions(job *Job, o *imageOptions, table []Engine) {
	if !imageOptionsStated(o) {
		return
	}
	job.Model = strings.TrimSpace(o.Model)
	job.AspectRatio = strings.TrimSpace(o.AspectRatio)
	job.Background = strings.TrimSpace(o.Background)
	ui := strings.TrimSpace(o.Quality)
	e, known := FindEngine(table, job.Model)
	if !known {
		// A flagged row (B-16) the table does not list (a caller that passed no flags) still has its
		// own dial: read it off the catalogue, never send a resolution engine the quality word. The
		// worker refuses such a run before the money when the flag is off (engineOffAtSubmit).
		e, known = catalogueEngine(job.Model)
	}
	if !known {
		if ui != "" {
			job.Quality = ui
		}
		return
	}
	if ui == "" {
		if e.dial() != TierDialQuality {
			job.Quality = "" // the deployment's quality word means nothing to a resolution engine
		}
		return
	}
	t, ok := e.Tier(ui)
	if !ok {
		job.Quality = ui
		return
	}
	switch t.Dial {
	case TierDialResolution:
		job.Quality, job.Resolution = "", t.Value
	default:
		job.Quality = t.Value
	}
}

// FlatDefaultEngine — the engine a FLAT run is drawn by when the person named none (owner, 05.10:
// «модель флэта по умолчанию → gpt-image-2.5-flare»). The door freezes it into params.image.model
// (admin designFreezeFlatModel), so the run's history says which model drew it and a later change
// of this constant never rewrites an old run. Other kinds keep the deployment default.
const FlatDefaultEngine = EngineGPTImage25Flare

// FlatCandidates — how many sheets ONE flat press buys (owner, 05.10: «4 кандидата на нажатие»; the
// designer picks one, the split flow cuts the chosen one). Only a garment sheet (`one` layout, at
// least one non-detail view) is bought four times: a per_view run is already one call per view and a
// detail callout is one close-up.
const FlatCandidates = 4

// FlatCandidatesFor — the outputs a flat run of these views and layout buys: FlatCandidates for a
// garment sheet, 0 when the rule does not apply (the caller keeps its own count).
func FlatCandidatesFor(views []string, layout string) int {
	if layout == layoutPerView || len(views) == 0 || detailOnlyRun(views) {
		return 0
	}
	return FlatCandidates
}
