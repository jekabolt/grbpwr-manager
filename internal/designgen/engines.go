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
	// IsDefault marks the deployment's own slug (OPENROUTER_MODEL_IMAGE): the engine a run with no
	// params.image — and a params.image with no model — is drawn by.
	IsDefault bool
}

// Engine slugs of phase 2. Gemini / Seedream rows are phase 3 (B-16).
const (
	EngineGPTImage2  = "openai/gpt-image-2"
	EngineGPTImage25 = "openai/gpt-image-2.5-sunburst"
)

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
	}
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
// A fresh slice on every call: the band puts it on the wire and the door filters it.
func EngineTable(defaultSlug string) []Engine {
	def := strings.TrimSpace(defaultSlug)
	if def == "" {
		def = orimages.DefaultModel
	}
	rows := []Engine{
		gptEngine(EngineGPTImage2, "GPT Image 2", nil),
		// `transparent` on 2.5 through OpenRouter is UNVERIFIED (12-PROVIDERS §E): G-02 measures it,
		// and a 400 there deletes this entry.
		gptEngine(EngineGPTImage25, "GPT Image 2.5", []string{"transparent"}),
	}
	at := -1
	for i := range rows {
		if rows[i].Slug == def {
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
