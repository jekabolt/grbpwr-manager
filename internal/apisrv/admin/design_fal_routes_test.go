package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/fal"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/status"
)

// ═══ B-13 — kind=extend AT THE LIVE DOOR, and the capability it advertises ═══

// falRoutesFor — the route objects exactly as app.go builds them (designgen.FalRouteOf over one fal
// client with a key and the given tariff settings).
func falRoutesFor(cfg fal.Config) map[string]designgen.FalRoute {
	cfg.APIKey = "k"
	c := fal.New(cfg)
	out := map[string]designgen.FalRoute{}
	for _, k := range []string{entity.DesignRunKindExtend, entity.DesignRunKindInpaint} {
		r, _ := designgen.FalRouteOf(c, k)
		out[k] = r
	}
	return out
}

func withFalRoutes(cfg fal.Config) func(t *testing.T, rig *designRunRig) {
	return func(t *testing.T, rig *designRunRig) { rig.srv.SetDesignFalRoutes(falRoutesFor(cfg)) }
}

func extendParams(ratio string, ids ...int32) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{ExtraInputMediaIds: ids, Extend: &pb_common.DesignExtendParams{AspectRatio: ratio}}
}

type falDoorRow struct {
	name   string
	kind   string
	ask    string
	params *pb_common.DesignRunParams
	setup  []func(t *testing.T, rig *designRunRig)
	want   string // '' = legal
	price  string // legal rows: the reserve
}

func falDoorRows() []falDoorRow {
	routes := withFalRoutes(fal.Config{})
	return []falDoorRow{
		{name: "legal extend reserves the outpaint ceiling", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID), setup: []func(*testing.T, *designRunRig){routes}, price: "0.12"},
		{name: "a configured tariff raises the reserve to tariff × ceiling", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID),
			setup:  []func(*testing.T, *designRunRig){withFalRoutes(fal.Config{UnitUSDOutpaint: 0.05, UnitsCeilingOutpaint: 4})},
			price:  "0.2"},
		{name: "legal extend with known dims", kind: entity.DesignRunKindExtend,
			params: extendParams("16:9", designRefMediaID),
			setup:  []func(*testing.T, *designRunRig){routes, pgMediaDims(designRefMediaID, 600, 800)}, price: "0.12"},
		{name: "no source", kind: entity.DesignRunKindExtend, params: extendParams("21:9"),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeOneSourcePicture},
		{name: "two sources", kind: entity.DesignRunKindExtend, params: extendParams("21:9", designRefMediaID, designPlateMediaID),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeOneSourcePicture},
		{name: "words on an extend", kind: entity.DesignRunKindExtend, ask: "make it a beach",
			params: extendParams("21:9", designRefMediaID), setup: []func(*testing.T, *designRunRig){routes},
			want: entity.DesignErrorCodeExtendTakesNoWords},
		{name: "auto is not a target", kind: entity.DesignRunKindExtend, params: extendParams("auto", designRefMediaID),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeExtendAspectUnknown},
		{name: "no target", kind: entity.DesignRunKindExtend, params: extendParams("", designRefMediaID),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeExtendAspectUnknown},
		{name: "a ratio outside the nine", kind: entity.DesignRunKindExtend, params: extendParams("4:5", designRefMediaID),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeExtendAspectUnknown},
		{name: "the target is the source's own proportion", kind: entity.DesignRunKindExtend,
			params: extendParams("3:4", designRefMediaID),
			setup:  []func(*testing.T, *designRunRig){routes, pgMediaDims(designRefMediaID, 600, 800)},
			want:   entity.DesignErrorCodeTargetAspectMustExtend},
		{name: "a source too small to extend", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID),
			setup:  []func(*testing.T, *designRunRig){routes, pgMediaDims(designRefMediaID, 50, 50)},
			want:   entity.DesignErrorCodeSourceTooSmall},
		{name: "params.image on an extend", kind: entity.DesignRunKindExtend,
			params: func() *pb_common.DesignRunParams {
				p := extendParams("21:9", designRefMediaID)
				p.Image = &pb_common.DesignImageOptions{Quality: "high"}
				return p
			}(),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeImageOptionsForbidden},
		{name: "params.inpaint on an extend", kind: entity.DesignRunKindExtend,
			params: func() *pb_common.DesignRunParams {
				p := extendParams("21:9", designRefMediaID)
				p.Inpaint = &pb_common.DesignInpaintParams{SourceMediaId: designRefMediaID, MaskMediaId: designPlateMediaID}
				return p
			}(),
			setup: []func(*testing.T, *designRunRig){routes}, want: entity.DesignErrorCodeInpaintForbidden},
		{name: "params.extend on a cut-out", kind: entity.DesignRunKindCutout,
			params: extendParams("21:9", designRefMediaID), want: entity.DesignErrorCodeExtendForbidden},
		{name: "a tariff without its ceiling closes the door", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID),
			setup:  []func(*testing.T, *designRunRig){withFalRoutes(fal.Config{UnitUSDOutpaint: 0.05})},
			want:   entity.DesignErrorCodeRouteReserveUnbounded},
		{name: "no route object: fail closed", kind: entity.DesignRunKindExtend,
			params: extendParams("21:9", designRefMediaID), want: designReasonKindUnavailable},
	}
}

// TestTheExtendDoorREFUSES_BEFORE_THE_STORE — every row through the real StartDesignRun; a refusal
// never reaches StartRun (nothing reserved), a legal row reserves the route's own ceiling.
// MUTATIONS (measured red): drop the designRefuseFalRouteUnbounded call → the two «closed» rows reach
// the store; drop designRefuseExtendTarget → the own-proportion and too-small rows reach it; drop the
// route max in designFalRouteEstimate → the tariff row reserves 0.12.
func TestTheExtendDoorREFUSES_BEFORE_THE_STORE(t *testing.T) {
	for _, c := range falDoorRows() {
		t.Run(c.name, func(t *testing.T) {
			rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
			for _, s := range c.setup {
				s(t, rig)
			}
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
			req := designStartRequest(c.kind)
			req.Params = c.params
			req.Ask = c.ask
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			if c.want == "" {
				require.NoError(t, err)
				require.NotNil(t, rig.sent)
				require.Equal(t, 1, rig.sent.RequestedOutputs)
				require.True(t, rig.sent.PriceEstimate.Valid)
				require.Equal(t, c.price, rig.sent.PriceEstimate.Decimal.String())
				return
			}
			_, md := errorReason(t, err)
			require.Equalf(t, c.want, md["reason"], "%v", err)
			require.Nil(t, rig.sent, "refused before the store: nothing reserved")
		})
	}
}

// TestRunKindsLISTS_A_FAL_KIND_ONLY_WITH_A_BOUNDED_ROUTE — band field 32 and playground_workflows
// read the same ladder as the door. Mutation: drop the bounded check in designRunKinds → red.
func TestRunKindsLISTS_A_FAL_KIND_ONLY_WITH_A_BOUNDED_ROUTE(t *testing.T) {
	band := func(routes map[string]designgen.FalRoute, enabled bool, closed ...string) *pb_admin.GetDesignBandResponse {
		repo := mocks.NewMockRepository(t)
		d := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(d).Maybe()
		d.EXPECT().GetBand(mock.Anything, mock.Anything, mock.Anything).Return(&entity.DesignBand{}, nil).Maybe()
		s := &Server{repo: repo}
		s.SetDesignGenerationEnabled(enabled)
		if routes != nil {
			s.SetDesignFalRoutes(routes)
		}
		s.SetDesignKindGate(func(kind string) error {
			for _, c := range closed {
				if c == kind {
					return status.Error(9, "closed")
				}
			}
			return nil
		})
		resp, err := s.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
		require.NoError(t, err)
		require.NotNil(t, resp.GetRunKinds(), "present, never absent: absent means an older binary")
		return resp
	}

	open := band(falRoutesFor(fal.Config{}), true)
	require.Contains(t, open.GetRunKinds(), entity.DesignRunKindExtend)
	require.Contains(t, open.GetRunKinds(), entity.DesignRunKindInpaint)
	require.Contains(t, open.GetRunKinds(), entity.DesignRunKindCutout)
	require.NotContains(t, open.GetRunKinds(), entity.DesignRunKindDraftIdea, "draft_idea has its own verb")
	require.Contains(t, open.GetPlaygroundWorkflows(), entity.DesignWorkflowExtendImage)

	unbounded := band(falRoutesFor(fal.Config{UnitUSDOutpaint: 0.05}), true)
	require.NotContains(t, unbounded.GetRunKinds(), entity.DesignRunKindExtend,
		"a tariff without its ceiling: the door refuses, so the band does not offer it")
	require.Contains(t, unbounded.GetRunKinds(), entity.DesignRunKindInpaint, "fill has its own tariff")
	require.NotContains(t, unbounded.GetPlaygroundWorkflows(), entity.DesignWorkflowExtendImage)

	none := band(nil, true)
	require.NotContains(t, none.GetRunKinds(), entity.DesignRunKindExtend, "no route object: fail closed")
	require.NotContains(t, none.GetRunKinds(), entity.DesignRunKindInpaint)
	require.NotContains(t, none.GetPlaygroundWorkflows(), entity.DesignWorkflowExtendImage)

	gated := band(falRoutesFor(fal.Config{}), true, entity.DesignRunKindExtend)
	require.NotContains(t, gated.GetRunKinds(), entity.DesignRunKindExtend, "the worker's own pre-flight closes it")
	require.NotContains(t, gated.GetPlaygroundWorkflows(), entity.DesignWorkflowExtendImage)

	// retouch_zone: open on the freeform gate (phase-2 window path) OR on inpaint.
	onlyInpaint := band(falRoutesFor(fal.Config{}), true, entity.DesignRunKindFreeform)
	require.Contains(t, onlyInpaint.GetPlaygroundWorkflows(), entity.DesignWorkflowRetouchZone)
	neither := band(nil, true, entity.DesignRunKindFreeform)
	require.NotContains(t, neither.GetPlaygroundWorkflows(), entity.DesignWorkflowRetouchZone)

	off := band(falRoutesFor(fal.Config{}), false)
	require.Empty(t, off.GetRunKinds(), "generation off: present and empty")
}

// TestTheFalKindsAreLIVE_PRICED_AND_NEVER_FALL_INTO_FLAT — the vocabulary rows of the two kinds:
// in IsDesignRunKind, priced, freeform pictures (never flat, never a bench axis, no colourway), one
// output, no card, their own tiles.
func TestTheFalKindsAreLIVE_PRICED_AND_NEVER_FALL_INTO_FLAT(t *testing.T) {
	for kind, tile := range map[string]string{
		entity.DesignRunKindExtend:  entity.DesignWorkflowExtendImage,
		entity.DesignRunKindInpaint: entity.DesignWorkflowRetouchZone,
	} {
		require.True(t, entity.IsDesignRunKind(kind))
		require.Contains(t, entity.DesignRunKinds(), kind)
		got := entity.DesignPictureKindOfRun(kind)
		require.Equal(t, entity.DesignPictureKindFreeform, got, "%s: a playground picture", kind)
		require.False(t, entity.IsDesignBenchKind(got))
		require.False(t, entity.DesignRunKindTakesColorway(kind))
		require.False(t, designKindReadsTheCard(kind))
		require.False(t, designKindReadsTheGarmentNote(kind))
		require.Equal(t, 1, designRequestedOutputs(kind, &pb_common.DesignRunParams{
			Views: []string{entity.DesignViewFront, entity.DesignViewBack}, Layout: designLayoutPerView}))
		require.Equal(t, tile, entity.DesignWorkflowOf(kind, "", false))
		est := designEstimateFor(kind, 1)
		require.True(t, est.Valid && est.Decimal.IsPositive(), "%s reserves nothing", kind)
	}
	require.Equal(t, "0.12", designEstimateFor(entity.DesignRunKindExtend, 1).Decimal.String())
	require.Equal(t, "0.15", designEstimateFor(entity.DesignRunKindInpaint, 1).Decimal.String())
}
