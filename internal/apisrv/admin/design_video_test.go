package admin

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// ═══ THE VIDEO DOOR (B-32) — one picture + words → a 5-second Kling clip through StartDesignRun ═══

func videoParams(sourceID int32) *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Video: &pb_common.DesignVideoParams{SourceMediaId: sourceID}}
}

func withVideoRoute(route designgen.VideoRoute) func(t *testing.T, rig *designRunRig) {
	return func(t *testing.T, rig *designRunRig) {
		rig.srv.SetDesignVideoRoute(func() designgen.VideoRoute { return route })
	}
}

// TestTheVideoReserveEQUALS_THE_CONFIGURED_CEILING — ACCEPTANCE: «the reservation equals
// RUNBLOB_VIDEO_CEILING_USD». The door reserves the route's ceiling (what designgen.VideoRouteFunc
// carries from Config.VideoCeiling ← RUNBLOB_VIDEO_CEILING_USD), and the table's default 1.50 when no
// route is wired or the ceiling is not positive; a legal run reaches the store with exactly that number
// and the frozen slug. MUTATION (measured red → green): designVideoRunEstimate ignoring the route's
// CeilingUSD → the 2.25 row reserves 1.5.
func TestTheVideoReserveEQUALS_THE_CONFIGURED_CEILING(t *testing.T) {
	require.True(t, designPriceEstimate[entity.DesignRunKindVideo].Equal(decimal.RequireFromString(designgen.DefaultVideoCeilingUSD)),
		"the table row is the config default: one number in two places would drift")
	for _, c := range []struct {
		name  string
		setup []func(*testing.T, *designRunRig)
		price string
		model string
	}{
		{"RUNBLOB_VIDEO_CEILING_USD=2.25", []func(*testing.T, *designRunRig){
			withVideoRoute(designgen.VideoRoute{Model: "kling_2.5_turbo", CeilingUSD: decimal.RequireFromString("2.25")})},
			"2.25", "kling_2.5_turbo"},
		{"no route wired: the table's default, Kling's default slug", nil, "1.5", designgen.DefaultVideoModel},
		{"a zero ceiling: the table's default", []func(*testing.T, *designRunRig){
			withVideoRoute(designgen.VideoRoute{Model: "kling_2.1"})}, "1.5", "kling_2.1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
			for _, s := range c.setup {
				s(t, rig)
			}
			rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
			req := designStartRequest(entity.DesignRunKindVideo)
			req.Params = videoParams(designRefMediaID)
			req.Ask = "the coat sways in a slow breeze"
			_, err := rig.srv.StartDesignRun(designRunCtx(), req)
			require.NoError(t, err)
			require.NotNil(t, rig.sent)
			require.Equal(t, entity.DesignRunKindVideo, rig.sent.Kind)
			require.Equal(t, 1, rig.sent.RequestedOutputs, "one clip")
			require.True(t, rig.sent.PriceEstimate.Valid)
			require.Equal(t, c.price, rig.sent.PriceEstimate.Decimal.String())
			// The frozen params: the slug written before the money, the picture as the only input.
			require.Contains(t, string(rig.sent.Params), `"model":"`+c.model+`"`)
			require.Contains(t, string(rig.sent.Params), `"source_media_id":100`)
			require.Equal(t, 1, strings.Count(string(rig.sent.Inputs), `"media_id":100`),
				"the input snapshot names the one picture, once: %s", rig.sent.Inputs)
		})
	}
}

// TestTheVideoDoorREFUSES_BEFORE_THE_STORE — every malformed shape is refused with its word before
// StartRun (nothing reserved): no picture, a second list beside it, no words, too many words, a
// duration the route does not sell, a slug that is not Kling's, params.video on another kind; and a
// keyless runblob — ACCEPTANCE: «a missing key → the door refuses before any run row» — through the
// same kind gate the worker's PreflightKind feeds, kind_not_available with the worker's own sentence.
// MUTATIONS (measured red → green): drop the video case of designRefuseUnworkableSources → the shape
// rows reach the store; drop the video branch of designRefuseMalformedRoutes → the cutout row reaches it.
func TestTheVideoDoorREFUSES_BEFORE_THE_STORE(t *testing.T) {
	long := strings.Repeat("s", designgen.VideoMaxPromptRunes+1)
	keyless := func(t *testing.T, rig *designRunRig) {
		rig.srv.SetDesignKindGate(func(kind string) error {
			if kind == entity.DesignRunKindVideo {
				return status.Error(codes.FailedPrecondition,
					"designgen: the provider for this run kind is not configured: runblob — no key for runblob — "+
						"set it in admin → AI providers (runblob has no environment variable)")
			}
			return nil
		})
	}
	for _, c := range []struct {
		name   string
		kind   string
		ask    string
		params *pb_common.DesignRunParams
		setup  []func(*testing.T, *designRunRig)
		want   string
		says   string
	}{
		{name: "no picture", kind: entity.DesignRunKindVideo, ask: "sway", params: videoParams(0),
			want: entity.DesignErrorCodeOneSourcePicture},
		{name: "a second list beside the picture", kind: entity.DesignRunKindVideo, ask: "sway",
			params: func() *pb_common.DesignRunParams {
				p := videoParams(designRefMediaID)
				p.ExtraInputMediaIds = []int32{designPlateMediaID}
				return p
			}(), want: entity.DesignErrorCodeOneSourcePicture},
		{name: "no words", kind: entity.DesignRunKindVideo, ask: "   ", params: videoParams(designRefMediaID),
			want: entity.DesignErrorCodeWordsRequired},
		{name: "too many words", kind: entity.DesignRunKindVideo, ask: long, params: videoParams(designRefMediaID),
			want: entity.DesignErrorCodeWordsRequired},
		{name: "a 10-second clip is not sold", kind: entity.DesignRunKindVideo, ask: "sway",
			params: &pb_common.DesignRunParams{Video: &pb_common.DesignVideoParams{SourceMediaId: designRefMediaID, Duration: 10}},
			want:   entity.DesignErrorCodeUnknownOption},
		{name: "a slug that is not Kling's", kind: entity.DesignRunKindVideo, ask: "sway",
			params: &pb_common.DesignRunParams{Video: &pb_common.DesignVideoParams{SourceMediaId: designRefMediaID, Model: "veo_3"}},
			want:   entity.DesignErrorCodeUnknownOption},
		{name: "params.video on a cut-out", kind: entity.DesignRunKindCutout, params: videoParams(designRefMediaID),
			want: entity.DesignErrorCodeVideoForbidden},
		{name: "runblob has no key", kind: entity.DesignRunKindVideo, ask: "sway", params: videoParams(designRefMediaID),
			setup: []func(*testing.T, *designRunRig){keyless}, want: designReasonKindUnavailable,
			says: "admin → AI providers"},
	} {
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
			require.Error(t, err)
			_, md := errorReason(t, err)
			require.Equalf(t, c.want, md["reason"], "%v", err)
			if c.says != "" {
				require.Contains(t, err.Error(), c.says, "the worker's sentence reaches the person")
			}
			require.Nil(t, rig.sent, "refused before the store: nothing reserved")
		})
	}
}

// TestTheVideoPictureIsSeenByTheInputGates — params.video.source_media_id is a source the three gates
// on designRunInputMediaRefs (not a picture / display only / hidden) must see: a .glb or an .mp4 named
// as the picture is refused as input_not_a_picture. (T64, 05.10: a picture another card holds is no
// longer refused — the media library is shared.)
// MUTATION (measured red → green): drop the params.video line of designRunInputMediaRefs → the .glb row
// reaches the store.
func TestTheVideoPictureIsSeenByTheInputGates(t *testing.T) {
	refs := designRunInputMediaRefs(videoParams(77), nil)
	require.Len(t, refs, 1)
	require.Equal(t, 77, refs[0].ID)
	require.Equal(t, "params.video.source_media_id", refs[0].Where)

	t.Run("a model file as the picture", func(t *testing.T) {
		rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
		rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).Return(nil).Maybe()
		req := designStartRequest(entity.DesignRunKindVideo)
		req.Params = videoParams(designGLBMediaID)
		req.Ask = "sway"
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		require.Error(t, err)
		_, md := errorReason(t, err)
		require.Equal(t, "input_not_a_picture", md["reason"])
		require.Nil(t, rig.sent)
	})
}

// TestTheVideoTileFOLLOWS_THE_KIND_GATE — the playground offers image_to_video exactly when the video
// kind gate is open (a keyless runblob closes it); run_kinds lists `video` the same way; and the
// vocabulary agrees: DesignWorkflowOf(video) = image_to_video, the video purpose is video.generate.
// MUTATION (measured red → green): designPlaygroundWorkflows opening image_to_video regardless of
// the gate → the closed band lists the tile.
func TestTheVideoTileFOLLOWS_THE_KIND_GATE(t *testing.T) {
	band := func(closed bool) *pb_admin.GetDesignBandResponse {
		repo := mocks.NewMockRepository(t)
		d := mocks.NewMockDesign(t)
		repo.EXPECT().Design().Return(d).Maybe()
		d.EXPECT().GetBand(mock.Anything, mock.Anything, mock.Anything).Return(&entity.DesignBand{}, nil).Once()
		s := &Server{repo: repo}
		s.SetDesignGenerationEnabled(true)
		s.SetDesignKindGate(func(kind string) error {
			if closed && kind == entity.DesignRunKindVideo {
				return status.Error(codes.FailedPrecondition, "no key for runblob")
			}
			return nil
		})
		resp, err := s.GetDesignBand(designRunCtx(), &pb_admin.GetDesignBandRequest{TechCardId: 7})
		require.NoError(t, err)
		return resp
	}
	open, shut := band(false), band(true)
	require.Contains(t, open.GetPlaygroundWorkflows(), entity.DesignWorkflowImageToVideo)
	require.Contains(t, open.GetRunKinds(), entity.DesignRunKindVideo)
	require.NotContains(t, shut.GetPlaygroundWorkflows(), entity.DesignWorkflowImageToVideo,
		"a tile whose route has no key is a tile that leads to a refusal")
	require.NotContains(t, shut.GetRunKinds(), entity.DesignRunKindVideo)

	require.Equal(t, entity.DesignWorkflowImageToVideo, entity.DesignWorkflowOf(entity.DesignRunKindVideo, "", false))
	require.Equal(t, entity.AIPurposeVideoGenerate, entity.AIPurposeOfRunKind(entity.DesignRunKindVideo))
	require.Equal(t, entity.DesignPictureKindVideo, entity.DesignPictureKindOfRun(entity.DesignRunKindVideo))
}
