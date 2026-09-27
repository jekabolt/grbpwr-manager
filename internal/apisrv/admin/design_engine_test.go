package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func engineServer() *Server {
	s := &Server{}
	s.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	return s
}

func withImage(p *pb_common.DesignRunParams, img *pb_common.DesignImageOptions) *pb_common.DesignRunParams {
	p.Image = img
	return p
}

// TestThePerRunEngineIsREFUSED_BY_THE_TABLE_BEFORE_MONEY — one row per shape, one machine word each.
func TestThePerRunEngineIsREFUSED_BY_THE_TABLE_BEFORE_MONEY(t *testing.T) {
	s := engineServer()
	pic := &pb_common.DesignFreeformItem{MediaId: 11}
	ff := func(img *pb_common.DesignImageOptions) *pb_common.DesignRunParams {
		return withImage(ffParams(entity.DesignFreeformPresetFree, pic), img)
	}
	for _, c := range []struct {
		name   string
		kind   string
		params *pb_common.DesignRunParams
		want   string
	}{
		{"image on threed", entity.DesignRunKindThreed,
			withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2}),
			entity.DesignErrorCodeImageOptionsForbidden},
		{"image on cutout", entity.DesignRunKindCutout,
			withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{AspectRatio: "1:1"}),
			entity.DesignErrorCodeImageOptionsForbidden},
		{"image on vector", entity.DesignRunKindVector,
			withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{Quality: "low"}),
			entity.DesignErrorCodeImageOptionsForbidden},
		{"unknown slug", entity.DesignRunKindFreeform,
			ff(&pb_common.DesignImageOptions{Model: "google/gemini-3-pro-image"}),
			entity.DesignErrorCodeUnknownImageModel},
		{"xhigh is not offered", entity.DesignRunKindFreeform,
			ff(&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Quality: "xhigh"}),
			entity.DesignErrorCodeQualityNotSupported},
		{"a ratio the engine does not draw", entity.DesignRunKindRender,
			withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{AspectRatio: "5:4"}),
			entity.DesignErrorCodeAspectNotSupported},
		{"transparent on gpt-image-2", entity.DesignRunKindFlat,
			withImage(&pb_common.DesignRunParams{}, &pb_common.DesignImageOptions{
				Model: designgen.EngineGPTImage2, Background: "transparent"}),
			entity.DesignErrorCodeBackgroundNotSupported},
		{"too many images for one recolour call", entity.DesignRunKindRecolor,
			withImage(recolorWithCloths(16), &pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2}),
			"too_many_pictures"},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, ffReason(t, s.designRefuseImageOptions(c.kind, c.params)))
		})
	}

	t.Run("legal shapes pass", func(t *testing.T) {
		for _, img := range []*pb_common.DesignImageOptions{
			nil,
			{},
			{Model: designgen.EngineGPTImage2, Quality: "high", AspectRatio: "3:4"},
			{Quality: "low"}, // the default engine
			{Model: designgen.EngineGPTImage25, Background: "transparent", AspectRatio: "auto"},
		} {
			require.NoError(t, s.designRefuseImageOptions(entity.DesignRunKindFreeform, ff(img)))
		}
		require.NoError(t, s.designRefuseImageOptions(entity.DesignRunKindRecolor,
			withImage(recolorWithCloths(15), &pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2})),
			"one photograph and fifteen cloths is sixteen images: the ceiling, inclusive")
	})

	t.Run("a server with no engine table takes no params.image", func(t *testing.T) {
		require.Equal(t, entity.DesignErrorCodeUnknownImageModel, ffReason(t,
			(&Server{}).designRefuseImageOptions(entity.DesignRunKindFreeform,
				ff(&pb_common.DesignImageOptions{Quality: "low"}))))
		require.NoError(t, (&Server{}).designRefuseImageOptions(entity.DesignRunKindFreeform, ff(nil)),
			"no block is today's request, engines or not")
	})
}

func recolorWithCloths(n int) *pb_common.DesignRunParams {
	c := &pb_common.DesignColourRecipe{}
	for i := 0; i < n; i++ {
		c.Fabrics = append(c.Fabrics, &pb_common.DesignFabricUse{MediaId: int32(100 + i)})
	}
	if n > 0 {
		c.FabricMediaId = 100 // the client's echo of the first cloth: counted once
	}
	return &pb_common.DesignRunParams{ExtraInputMediaIds: []int32{11}, Colour: c}
}

// TestTheReserveOfANamedEngineIsITS_ABSOLUTE_CEILING — ceiling × calls + $0.01 × images × calls.
func TestTheReserveOfANamedEngineIsITS_ABSOLUTE_CEILING(t *testing.T) {
	s := engineServer()
	three := withImage(ffParams(entity.DesignFreeformPresetFree,
		&pb_common.DesignFreeformItem{MediaId: 11},
		&pb_common.DesignFreeformItem{MediaId: 12},
		&pb_common.DesignFreeformItem{MediaId: 13}),
		&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Quality: "high"})

	got := s.designEstimateForRun(entity.DesignRunKindFreeform, 1, three, nil)
	require.True(t, got.Valid)
	require.Equal(t, "0.35", got.Decimal.String(), "0.32 high + 3 × 0.01")

	three.Image.Quality = "low"
	require.Equal(t, "0.06", s.designEstimateForRun(entity.DesignRunKindFreeform, 1, three, nil).Decimal.String())

	three.Image.Quality = "" // unstated tier = the deployment's dial, which the reserve cannot read
	require.Equal(t, "0.35", s.designEstimateForRun(entity.DesignRunKindFreeform, 1, three, nil).Decimal.String())

	t.Run("recolour: every call carries its photograph and the cloths", func(t *testing.T) {
		p := withImage(recolorWithCloths(2), &pb_common.DesignImageOptions{Quality: "medium"})
		p.ExtraInputMediaIds = []int32{11, 12, 13, 14}
		out := designRequestedOutputs(entity.DesignRunKindRecolor, p)
		require.Equal(t, 4, out)
		// 4 × 0.10 + 4 calls × 3 images × 0.01
		require.Equal(t, "0.52", s.designEstimateForRun(entity.DesignRunKindRecolor, out, p, nil).Decimal.String())
	})

	t.Run("a run naming no engine: the default engine's top tier with references, never below the kind", func(t *testing.T) {
		p := ffParams(entity.DesignFreeformPresetTryon,
			&pb_common.DesignFreeformItem{MediaId: 11}, &pb_common.DesignFreeformItem{MediaId: 12})
		require.Equal(t, "0.34", s.designEstimateForRun(entity.DesignRunKindFreeform, 1, p, nil).Decimal.String(),
			"0.32 + 2 pictures × 0.01")
		for _, kind := range []string{
			entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindRecolor,
			entity.DesignRunKindPattern, entity.DesignRunKindFreeform,
		} {
			got := s.designEstimateForRun(kind, 1, &pb_common.DesignRunParams{}, nil)
			require.Truef(t, got.Valid && got.Decimal.GreaterThanOrEqual(designEstimateFor(kind, 1).Decimal),
				"%s reserves %s, under its own table %s", kind, got.Decimal, designEstimateFor(kind, 1).Decimal)
		}
		require.Equal(t, designEstimateFor(entity.DesignRunKindThreed, 1),
			s.designEstimateForRun(entity.DesignRunKindThreed, 1, &pb_common.DesignRunParams{}, nil),
			"a non-image kind keeps its own price")
		require.Equal(t, designEstimateFor(entity.DesignRunKindFreeform, 1),
			(&Server{}).designEstimateForRun(entity.DesignRunKindFreeform, 1, p, nil),
			"no engine table: the kind's own price")

		// A default engine cheaper than the kind's table never lowers the unnamed reserve.
		cheap := &Server{}
		cheap.SetDesignEngines(func() []designgen.Engine {
			return []designgen.Engine{{Slug: "x/cheap", IsDefault: true, MaxRefs: 16,
				InputUSD: decimal.Zero, Tiers: []designgen.Tier{{UI: "high", Dial: designgen.TierDialQuality,
					Value: "high", CeilingUSD: decimal.RequireFromString("0.01")}}}}
		})
		require.Equal(t, designEstimateFor(entity.DesignRunKindRender, 1),
			cheap.designEstimateForRun(entity.DesignRunKindRender, 1, &pb_common.DesignRunParams{}, nil))
	})

	t.Run("the top tier never reserves less than the path it replaces", func(t *testing.T) {
		for _, kind := range []string{
			entity.DesignRunKindFlat, entity.DesignRunKindRender, entity.DesignRunKindRecolor,
			entity.DesignRunKindPattern, entity.DesignRunKindFreeform,
		} {
			for _, e := range designgen.EngineTable("") {
				ceiling := e.CeilingUSD("")
				require.Truef(t, ceiling.GreaterThanOrEqual(designPriceEstimate[kind]),
					"%s on %s reserves %s against today's %s", kind, e.Slug, ceiling, designPriceEstimate[kind])
			}
		}
	})

	t.Run("every accepted engine and tier is priced", func(t *testing.T) {
		custom := &Server{}
		custom.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("acme/custom-image") })
		for _, e := range designgen.EngineTable("acme/custom-image") {
			for _, tier := range append(designEngineTierWords(e), "") {
				p := withImage(ffParams(entity.DesignFreeformPresetFree, &pb_common.DesignFreeformItem{MediaId: 11}),
					&pb_common.DesignImageOptions{Model: e.Slug, Quality: tier})
				require.NoError(t, custom.designRefuseImageOptions(entity.DesignRunKindFreeform, p))
				got := custom.designEstimateForRun(entity.DesignRunKindFreeform, 1, p, nil)
				require.Truef(t, got.Valid && got.Decimal.GreaterThan(decimal.Zero), "%s/%q is unpriced", e.Slug, tier)
			}
		}
	})
}

// TestStartDesignRunPRICES_AND_REFUSES_BY_THE_ENGINE — the same two rules at the live door: the
// row the store receives carries the engine's reserve, and a refused engine never reaches it.
func TestStartDesignRunPRICES_AND_REFUSES_BY_THE_ENGINE(t *testing.T) {
	rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	rig.srv.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req := designStartRequest(entity.DesignRunKindFreeform)
	req.Params = withImage(ffParams(entity.DesignFreeformPresetFree, &pb_common.DesignFreeformItem{MediaId: 11}),
		&pb_common.DesignImageOptions{Model: designgen.EngineGPTImage2, Quality: "medium"})
	_, err := rig.srv.StartDesignRun(designRunCtx(), req)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.True(t, rig.sent.PriceEstimate.Valid)
	require.Equal(t, "0.11", rig.sent.PriceEstimate.Decimal.String(), "0.10 medium + 1 picture × 0.01")

	bad := newDesignRunRig(t, designMoodCard(), designBandWith(true))
	bad.srv.SetDesignEngines(func() []designgen.Engine { return designgen.EngineTable("") })
	bad.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
	req = designStartRequest(entity.DesignRunKindFreeform)
	req.Params = withImage(ffParams(entity.DesignFreeformPresetFree, &pb_common.DesignFreeformItem{MediaId: 11}),
		&pb_common.DesignImageOptions{Model: "nobody/knows"})
	_, err = bad.srv.StartDesignRun(designRunCtx(), req)
	require.Equal(t, entity.DesignErrorCodeUnknownImageModel, ffReason(t, err))
	require.Nil(t, bad.sent, "refused before the reserve")
}
