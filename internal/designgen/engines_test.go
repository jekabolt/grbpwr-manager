package designgen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"errors"
	"sync/atomic"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestEveryEngineRowIsPRICED_AND_DRAWABLE — the rows the door accepts, the band advertises and the
// reserve reads.
//
// Every row the catalogue knows, flagged or not (B-16): a flag decides whether a row is LISTED, never
// whether it is priced.
func TestEveryEngineRowIsPRICED_AND_DRAWABLE(t *testing.T) {
	all := EngineTable("", EngineFlags{Gemini: true, Seedream: true})
	require.Len(t, all, len(engineCatalogue()), "both flags on list every catalogue row")
	for _, e := range all {
		t.Run(e.Slug, func(t *testing.T) {
			require.NotEmpty(t, e.Label)
			require.NotEmpty(t, e.Tiers, "an engine with no tier cannot be priced")
			gpt := e.Slug == EngineGPTImage2 || e.Slug == EngineGPTImage25
			if e.Slug != EngineGemini3Pro {
				require.Contains(t, e.Ratios, "auto")
			}
			for _, r := range e.Ratios {
				require.Containsf(t, playgroundFormatRatios, r, "%s advertises %s, which no grid can draw", e.Slug, r)
			}
			if gpt {
				require.Equal(t, orimages.MaxInputReferences, e.MaxRefs)
				require.Equal(t, 10, e.MaxN)
			} else {
				require.Positive(t, e.MaxRefs)
				require.LessOrEqual(t, e.MaxRefs, orimages.MaxInputReferences)
				require.Equal(t, 1, e.MaxN)
			}
			require.True(t, e.InputUSD.GreaterThan(decimal.Zero), "a reference picture is not free")

			max := decimal.Zero
			for i, tier := range e.Tiers {
				require.Truef(t, tier.CeilingUSD.GreaterThan(decimal.Zero), "tier %s is unpriced", tier.UI)
				require.NotEmpty(t, tier.Value)
				require.Contains(t, []string{TierDialQuality, TierDialResolution}, tier.Dial)
				if i > 0 {
					require.Truef(t, tier.CeilingUSD.GreaterThanOrEqual(e.Tiers[i-1].CeilingUSD),
						"tiers are listed low → high: %s below %s", tier.UI, e.Tiers[i-1].UI)
				}
				max = decimal.Max(max, tier.CeilingUSD)
				got, ok := e.Tier(tier.UI)
				require.True(t, ok)
				require.Equal(t, tier, got)
			}
			require.True(t, e.CeilingUSD("").Equal(max), "an unstated tier is priced at the top")
			require.True(t, e.CeilingUSD("xhigh").Equal(max), "a word that is not a tier is priced at the top")
			// Today's freeform / render reserve is 0.08 × 4 = 0.32 per output; the top tier of a
			// DEFAULT-capable engine may not reserve less than the path it replaces. A flagged row is
			// never the default (B-16) and is held to its published price instead
			// (TestThePhase3CeilingsCOVER_THE_PUBLISHED_PRICE).
			if defaultCapable(e) {
				require.Truef(t, max.GreaterThanOrEqual(decimal.RequireFromString("0.32")),
					"%s tops out at %s, under today's 0.32", e.Slug, max)
			}

			require.True(t, e.Accepts(""))
			if gpt {
				require.False(t, e.Accepts("5:4"))
			}
			require.True(t, e.Background(""))
			require.False(t, e.Background("opaque-ish"))
		})
	}
}

// TestTheEngineTableMarksONE_DEFAULT_FIRST — the env slug is the default row, and a slug the table
// does not know still gets a priced row.
func TestTheEngineTableMarksONE_DEFAULT_FIRST(t *testing.T) {
	defaults := func(table []Engine) []string {
		var out []string
		for _, e := range table {
			if e.IsDefault {
				out = append(out, e.Slug)
			}
		}
		return out
	}

	std := EngineTable("")
	require.Len(t, std, 2)
	require.Equal(t, []string{orimages.DefaultModel}, defaults(std))
	require.Equal(t, orimages.DefaultModel, std[0].Slug)

	alt := EngineTable(EngineGPTImage25)
	require.Len(t, alt, 2)
	require.Equal(t, []string{EngineGPTImage25}, defaults(alt))
	require.Equal(t, EngineGPTImage25, alt[0].Slug)

	// G-02 Codex 2: a default slug the table does not know is NOT dressed as gpt-image-2 — its
	// ratios, reference ceiling and price are unknown, so the table is empty (present, never nil):
	// no picker, every params.image refused, unnamed runs on the kind's own price. MUTATION
	// (measured red): the old synthesized GPT-shaped row for the custom slug.
	custom := EngineTable("openai/gpt-image-1-mini")
	require.NotNil(t, custom)
	require.Empty(t, custom, "an unknown default slug advertises no engine")
	_, ok := FindEngine(custom, "")
	require.False(t, ok, "and has no default row to price an unnamed run by")
	_, ok = FindEngine(custom, EngineGPTImage2)
	require.False(t, ok, "nor any other row: the table is one decision, not a partial one")

	e, ok := FindEngine(std, "")
	require.True(t, ok)
	require.Equal(t, orimages.DefaultModel, e.Slug, "an empty model is the default engine")
	_, ok = FindEngine(std, "nobody/knows")
	require.False(t, ok)

	std[0].Ratios[0] = "mutated"
	require.Equal(t, "auto", EngineTable("")[0].Ratios[0], "a fresh table on every call")
	require.Equal(t, "0.01", engineInputUSD.String(), "the per-reference reserve G-02 measures")
}

// TestAFrozenEngineREACHES_THE_JOB — params.image overrides the deployment's dial; an empty block
// leaves today's job untouched.
func TestAFrozenEngineREACHES_THE_JOB(t *testing.T) {
	run := func(params string) entity.DesignRun {
		r := testRun(1, entity.DesignRunKindRender)
		r.Params = entity.RawJSON(params)
		return r
	}
	execute := func(t *testing.T, r entity.DesignRun) Job {
		img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
		w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
		require.NoError(t, w.execute(context.Background(), r, "tok"))
		require.Len(t, img.calls, 1)
		return img.calls[0]
	}

	j := execute(t, run(`{"image":{"model":"openai/gpt-image-2.5-sunburst","quality":"low",`+
		`"aspect_ratio":"3:4","background":"transparent"}}`))
	require.Equal(t, EngineGPTImage25, j.Model)
	require.Equal(t, "low", j.Quality, "the tier overrides QualityFor")
	require.Equal(t, "3:4", j.AspectRatio)
	require.Equal(t, "transparent", j.Background)
	require.Empty(t, j.Resolution)

	j = execute(t, run(`{"image":{"aspect_ratio":"16:9"}}`))
	require.Empty(t, j.Model, "no model = the client's own slug")
	require.Equal(t, DefaultConfig().ImageQuality, j.Quality, "no tier = the deployment's dial")
	require.Equal(t, "16:9", j.AspectRatio)

	for _, params := range []string{`{}`, `{"image":{}}`, `{"image":null}`} {
		j = execute(t, run(params))
		require.Equal(t, DefaultConfig().ImageQuality, j.Quality, params)
		require.Empty(t, j.Model+j.AspectRatio+j.Background+j.Resolution, params)
	}
}

// TestAResolutionEngineMOVES_ITS_OWN_DIAL — the resolution branch of applyImageOptions, which no
// phase-2 row uses yet.
func TestAResolutionEngineMOVES_ITS_OWN_DIAL(t *testing.T) {
	table := []Engine{{Slug: "x/res", IsDefault: true, Tiers: []Tier{
		{UI: "high", Dial: TierDialResolution, Value: "4K", CeilingUSD: decimal.RequireFromString("0.5")},
	}}}
	j := Job{Quality: "medium"}
	applyImageOptions(&j, &imageOptions{Model: "x/res", Quality: "high"}, table)
	require.Equal(t, "4K", j.Resolution)
	require.Empty(t, j.Quality, "a resolution engine is not sent a quality word")

	j = Job{Quality: "medium"}
	applyImageOptions(&j, &imageOptions{Model: "x/res"}, table)
	require.Empty(t, j.Quality)

	j = Job{Quality: "medium"}
	applyImageOptions(&j, &imageOptions{Model: "gone/slug", Quality: "high"}, table)
	require.Equal(t, "gone/slug", j.Model, "a slug the table forgot is sent as frozen, never swapped")
	require.Equal(t, "high", j.Quality)
}

// TestTheEngineReachesTHE_WIRE — images.go builds the request from the job's engine fields.
func TestTheEngineReachesTHE_WIRE(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = nil
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"aGk=","media_type":"image/png"}],"usage":{"cost":0.01}}`)
	}))
	defer srv.Close()
	p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))

	out, err := p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one",
		Model: EngineGPTImage25, Quality: "low", AspectRatio: "2:3", Background: "transparent",
	})
	require.NoError(t, err)
	require.Equal(t, EngineGPTImage25, body["model"])
	require.Equal(t, "low", body["quality"])
	require.Equal(t, "2:3", body["aspect_ratio"])
	require.Equal(t, "transparent", body["background"], "a stated background wins over the kind's own")
	require.NotContains(t, body, "resolution")
	require.Equal(t, EngineGPTImage25, out.Model)

	_, err = p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one",
	})
	require.NoError(t, err)
	require.Equal(t, orimages.DefaultModel, body["model"], "no engine = the configured slug")
	require.Equal(t, "opaque", body["background"], "no engine = the kind's own background")
	require.NotContains(t, body, "aspect_ratio")
}

// TestAWordsOnlyFreeRunIsONE_CALL_WITH_NO_PICTURE — tile 11 (text → image) [Codex 6]: the preset
// reaches the money boundary, and only `free` with words may go with no reference.
func TestAWordsOnlyFreeRunIsONE_CALL_WITH_NO_PICTURE(t *testing.T) {
	calls, err := imageCalls(Job{Kind: entity.DesignRunKindFreeform,
		FreeformPreset: entity.DesignFreeformPresetFree, Prompt: "a red coat on a hanger"})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, 1, calls[0].n)
	require.Empty(t, calls[0].refs)

	_, err = imageCalls(Job{Kind: entity.DesignRunKindFreeform,
		FreeformPreset: entity.DesignFreeformPresetFree, Prompt: "   "})
	require.Error(t, err, "no words and no picture is nothing to draw")
	for _, preset := range []string{
		entity.DesignFreeformPresetTryon, entity.DesignFreeformPresetRetouch,
		entity.DesignFreeformPresetAddHardware, "",
	} {
		_, err = imageCalls(Job{Kind: entity.DesignRunKindFreeform, FreeformPreset: preset, Prompt: "x"})
		require.Errorf(t, err, "preset %q works on a picture", preset)
	}
}

// playgroundFormatRatios — the client's FORMAT_RATIOS (grbpwr-admin-client
// design/playground/fields/format-grid.tsx): every ratio a playground grid can draw. A row that
// advertised a ratio outside it would be a choice nobody can make.
var playgroundFormatRatios = []string{"auto", "9:16", "1:1", "4:5", "3:4", "2:3", "16:9", "4:3", "3:2", "5:4", "21:9", "9:21"}

// TestTheEngineFlagsLIST_THE_PHASE3_ROWS — B-16: Gemini / Seedream appear only while their flag is on,
// and never shift the default.
func TestTheEngineFlagsLIST_THE_PHASE3_ROWS(t *testing.T) {
	slugs := func(table []Engine) []string {
		var out []string
		for _, e := range table {
			out = append(out, e.Slug)
		}
		return out
	}
	gpt := []string{EngineGPTImage2, EngineGPTImage25}

	// MUTATION (measured red): drop the `if !on.Gemini { continue }` in EngineTable.
	require.Equal(t, gpt, slugs(EngineTable("")), "no flags = the phase-2 table, byte for byte")
	require.Equal(t, gpt, slugs(EngineTable("", EngineFlags{})), "both flags off = the phase-2 table")
	require.Equal(t, append(append([]string{}, gpt...), EngineGemini3Pro),
		slugs(EngineTable("", EngineFlags{Gemini: true})))
	require.Equal(t, append(append([]string{}, gpt...), EngineSeedream5Pro),
		slugs(EngineTable("", EngineFlags{Seedream: true})))
	require.Equal(t, append(append([]string{}, gpt...), EngineGemini3Pro, EngineSeedream5Pro),
		slugs(EngineTable("", EngineFlags{Gemini: true}, EngineFlags{Seedream: true})), "flags are OR-ed")

	on := EngineTable(EngineGPTImage25, EngineFlags{Gemini: true, Seedream: true})
	require.Equal(t, EngineGPTImage25, on[0].Slug)
	require.True(t, on[0].IsDefault)
	for _, e := range on[1:] {
		require.False(t, e.IsDefault, e.Slug)
	}
	_, ok := FindEngine(EngineTable(""), EngineGemini3Pro)
	require.False(t, ok, "flag off: the door does not find Gemini (unknown_image_model)")
	_, ok = FindEngine(EngineTable("", EngineFlags{Gemini: true}), EngineSeedream5Pro)
	require.False(t, ok, "one flag does not open the other row")
}

// TestAFlaggedEngineIsNEVER_THE_DEFAULT — in phase 3 the default is a GPT row: an unnamed run is sent
// the deployment's QUALITY word and reserved against GPT-sized kind tables. A Gemini / Seedream
// OPENROUTER_MODEL_IMAGE empties the table exactly like any unknown slug (G-02 Codex 2), flags or not.
// MUTATION (measured red): `defaultCapable` returning true.
func TestAFlaggedEngineIsNEVER_THE_DEFAULT(t *testing.T) {
	for _, slug := range []string{EngineGemini3Pro, EngineSeedream5Pro} {
		for _, flags := range []EngineFlags{{}, {Gemini: true, Seedream: true}} {
			table := EngineTable(slug, flags)
			require.NotNilf(t, table, "%s %+v", slug, flags)
			require.Emptyf(t, table, "%s as the default slug (%+v) lists no engine", slug, flags)
		}
	}
}

// publishedUSD — THE MEASUREMENT, kept apart from the rows so an edit of a row cannot also edit its
// evidence. Read 2026-09-27:
//   - https://openrouter.ai/api/v1/images/models/google/gemini-3-pro-image/endpoints — output_image
//     $0.00012/token, input_image $0.000002/token; token counts from
//     https://ai.google.dev/gemini-api/docs/pricing — 1K/2K 1120 tokens, 4K 2000, one input 560;
//   - https://openrouter.ai/api/v1/images/models/bytedance-seed/seedream-5-0-pro/endpoints —
//     output_image $0.045, `high_resolution` $0.09 (> 2.36 MP, i.e. 2K), input_image $0.003.
var publishedUSD = map[string]struct {
	tiers map[string]string // UI tier → published price of one output at that tier's value
	input string            // one input picture
}{
	EngineGemini3Pro:   {tiers: map[string]string{"low": "0.1344", "medium": "0.1344", "high": "0.24"}, input: "0.00112"},
	EngineSeedream5Pro: {tiers: map[string]string{"low": "0.045", "high": "0.09"}, input: "0.003"},
}

// TestThePhase3CeilingsCOVER_THE_PUBLISHED_PRICE — the ceiling rule of engines.go: every tier ≥ 1.3 ×
// its published output price, every reference reserve ≥ one published input, so the reserve at MaxRefs
// covers the worst charge. MUTATION (measured red): Gemini high 0.32 → 0.23.
func TestThePhase3CeilingsCOVER_THE_PUBLISHED_PRICE(t *testing.T) {
	margin := decimal.RequireFromString("1.3")
	resolutions := map[string]map[string]string{
		EngineGemini3Pro:   {"low": "1K", "medium": "2K", "high": "4K"},
		EngineSeedream5Pro: {"low": "1K", "high": "2K"},
	}
	for slug, pub := range publishedUSD {
		e, ok := catalogueEngine(slug)
		require.True(t, ok, slug)
		require.Len(t, e.Tiers, len(pub.tiers), "%s: a tier with no published price, or a price with no tier", slug)
		input := decimal.RequireFromString(pub.input)
		require.Truef(t, e.InputUSD.GreaterThanOrEqual(input), "%s reserves %s per reference, published %s",
			slug, e.InputUSD, input)
		for _, tier := range e.Tiers {
			price, ok := pub.tiers[tier.UI]
			require.Truef(t, ok, "%s tier %s is not in the published table", slug, tier.UI)
			out := decimal.RequireFromString(price)
			require.Truef(t, tier.CeilingUSD.GreaterThanOrEqual(out.Mul(margin)),
				"%s %s: ceiling %s under 1.3 × published %s", slug, tier.UI, tier.CeilingUSD, out)
			// The worst charge of one call: the output plus MaxRefs inputs, against the reserve.
			refs := decimal.NewFromInt(int64(e.MaxRefs))
			require.True(t, tier.CeilingUSD.Add(e.InputUSD.Mul(refs)).GreaterThanOrEqual(out.Add(input.Mul(refs))))
			require.Equal(t, TierDialResolution, tier.Dial, "%s moves resolution, never quality", slug)
			require.Equal(t, resolutions[slug][tier.UI], tier.Value)
		}
	}

	seedream, _ := catalogueEngine(EngineSeedream5Pro)
	_, ok := seedream.Tier("medium")
	require.False(t, ok, "Seedream has no medium (1K | 2K): the door says quality_not_supported")
	require.True(t, seedream.Accepts("9:21"))
	require.False(t, seedream.Accepts("9:20"), "a catalogue ratio the grid cannot draw is not advertised")

	gemini, _ := catalogueEngine(EngineGemini3Pro)
	require.False(t, gemini.Accepts("auto"), "Gemini lists no auto: an unstated format sends no ratio")
	require.True(t, gemini.Accepts("4:5"))
	require.True(t, gemini.Accepts(""))
	for _, e := range []Engine{gemini, seedream} {
		require.Equal(t, 14, e.MaxRefs, e.Slug)
		require.Equal(t, 1, e.MaxN, e.Slug)
		require.True(t, e.NoRouteDefaults, e.Slug)
		require.False(t, e.Background("opaque"), "%s takes no background", e.Slug)
	}
	for _, slug := range []string{EngineGPTImage2, EngineGPTImage25} {
		e, _ := catalogueEngine(slug)
		require.False(t, e.NoRouteDefaults, "a GPT row keeps today's background and png")
		for _, tier := range e.Tiers {
			require.Equal(t, TierDialQuality, tier.Dial, "GPT rows still move quality")
		}
	}
}

// TestAFrozenFlaggedEngineREACHES_THE_JOB — a Gemini / Seedream run the door froze is sent its own
// resolution word and no quality while its flag is ON at the pickup (G-03, Codex 6: a flag that is off
// at the pickup refuses the run — TestAFlagTurnedOffSTOPS_QUEUED_SPEND). MUTATION (measured red): drop
// the resolution branch of applyImageOptions — the job then carries Quality "medium" and no resolution.
func TestAFrozenFlaggedEngineREACHES_THE_JOB(t *testing.T) {
	for _, c := range []struct {
		params, model, resolution string
	}{
		{`{"image":{"model":"google/gemini-3-pro-image","quality":"medium","aspect_ratio":"4:5"}}`, EngineGemini3Pro, "2K"},
		{`{"image":{"model":"google/gemini-3-pro-image","quality":"high"}}`, EngineGemini3Pro, "4K"},
		{`{"image":{"model":"bytedance-seed/seedream-5-0-pro","quality":"low"}}`, EngineSeedream5Pro, "1K"},
		{`{"image":{"model":"bytedance-seed/seedream-5-0-pro","quality":"high","aspect_ratio":"9:21"}}`, EngineSeedream5Pro, "2K"},
		// An unstated tier on a resolution engine: no word at all (provider default), priced at the top.
		{`{"image":{"model":"google/gemini-3-pro-image"}}`, EngineGemini3Pro, ""},
	} {
		t.Run(c.params, func(t *testing.T) {
			r := testRun(1, entity.DesignRunKindFlat)
			r.Params = entity.RawJSON(c.params)
			img := &fakeProvider{name: "image", out: okOutcome(1, 0.04)}
			w := testWorker(&fakeStore{}, nil, newFakeSink(ContentTypePNG), Providers{Image: img})
			w.c.EngineGemini, w.c.EngineSeedream = true, true
			require.NoError(t, w.execute(context.Background(), r, "tok"))
			require.Len(t, img.calls, 1)
			j := img.calls[0]
			require.Equal(t, c.model, j.Model)
			require.Equal(t, c.resolution, j.Resolution)
			require.Empty(t, j.Quality, "a resolution engine is never sent the flat's `high`")
		})
	}
}

// TestAFlaggedEngineReachesTHE_WIRE — the orimages `resolution` field (phase 2) reaches the provider
// for these slugs, and none of the keys their catalogue does not list does: no quality, no background
// (not even the flat's own `opaque`), no output_format. MUTATION (measured red): Resolution dropped
// from the orimages.Request in images.go; and NoRouteDefaults ignored (background "opaque" on the wire).
func TestAFlaggedEngineReachesTHE_WIRE(t *testing.T) {
	var body map[string]any
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		body = nil
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"data":[{"b64_json":"aGk=","media_type":"image/jpeg"}],"usage":{"cost":0.134}}`)
	}))
	defer srv.Close()
	p := NewImageProvider(orimages.New(orimages.Config{APIKey: "k", BaseURL: srv.URL}))

	for _, c := range []struct{ model, resolution, ratio string }{
		{EngineGemini3Pro, "2K", "4:5"},
		{EngineGemini3Pro, "4K", ""},
		{EngineSeedream5Pro, "1K", "9:21"},
	} {
		out, err := p.Execute(context.Background(), Job{
			RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one",
			Model: c.model, Resolution: c.resolution, AspectRatio: c.ratio,
		})
		require.NoError(t, err)
		require.Equal(t, c.model, body["model"])
		require.Equal(t, c.resolution, body["resolution"], "the tier's resolution word is on the wire")
		require.NotContains(t, body, "quality")
		require.NotContains(t, body, "background", "the flat's own `opaque` is not in this slug's catalogue")
		require.NotContains(t, body, "output_format")
		require.EqualValues(t, 1, body["n"])
		if c.ratio == "" {
			require.NotContains(t, body, "aspect_ratio")
		} else {
			require.Equal(t, c.ratio, body["aspect_ratio"])
		}
		require.Equal(t, c.model, out.Model)
		require.Equal(t, "image/jpeg", out.Artifacts[0].ContentType, "whatever raster comes back is filed")
	}

	// The GPT row keeps today's bytes: the kind's `opaque` and `png`.
	_, err := p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindFlat, Prompt: "a flat", Layout: "one",
		Model: EngineGPTImage2, Quality: "high",
	})
	require.NoError(t, err)
	require.Equal(t, "opaque", body["background"])
	require.Equal(t, "png", body["output_format"])
	require.NotContains(t, body, "resolution")

	// FREE LOCK: 15 references on a 14-reference engine are refused before ANY request. MUTATION
	// (measured red): drop the MaxRefs check — the server is hit and the provider would refuse (or
	// charge) instead.
	refs := make([]string, 15)
	for i := range refs {
		refs[i] = "https://example.com/p.png"
	}
	before := hits.Load()
	_, err = p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindRender, Prompt: "a render", Layout: "one",
		Model: EngineGemini3Pro, Resolution: "1K", References: refs,
	})
	require.Error(t, err)
	require.True(t, errors.Is(err, orimages.ErrBadRequest), "a terminal local refusal: %v", err)
	require.Equal(t, before, hits.Load(), "nothing reached the provider")
	// …while the same 15 on a 16-reference GPT row go through.
	_, err = p.Execute(context.Background(), Job{
		RunID: 1, TechCardID: 1, Kind: entity.DesignRunKindRender, Prompt: "a render", Layout: "one",
		Model: EngineGPTImage2, References: refs,
	})
	require.NoError(t, err)
}
