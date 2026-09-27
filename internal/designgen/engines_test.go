package designgen

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/orimages"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestEveryEngineRowIsPRICED_AND_DRAWABLE — the rows the door accepts, the band advertises and the
// reserve reads.
func TestEveryEngineRowIsPRICED_AND_DRAWABLE(t *testing.T) {
	for _, e := range EngineTable("") {
		t.Run(e.Slug, func(t *testing.T) {
			require.NotEmpty(t, e.Label)
			require.NotEmpty(t, e.Tiers, "an engine with no tier cannot be priced")
			require.Contains(t, e.Ratios, "auto")
			require.Equal(t, orimages.MaxInputReferences, e.MaxRefs)
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
			// Today's freeform / render reserve is 0.08 × 4 = 0.32 per output; the top tier of any
			// engine may not reserve less than the path it replaces.
			require.Truef(t, max.GreaterThanOrEqual(decimal.RequireFromString("0.32")),
				"%s tops out at %s, under today's 0.32", e.Slug, max)

			require.True(t, e.Accepts(""))
			require.False(t, e.Accepts("5:4"))
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
