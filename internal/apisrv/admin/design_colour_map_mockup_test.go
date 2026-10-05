package admin

import (
	"context"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── T13: THE CLOTH MOCKUP OF A COLOUR MAP AT THE RUN DOOR ───────────────────────────────────────
//
// A mockup travels to the provider right after its map, so it is held to the map's rules: card-owned
// media, one picture one role, and counted in the reserve.

// TestTheDoorRefusesAMockupThatIsAlsoAMap — the mockup is its own picture: never the map it belongs
// to, never another map, never the mockup of a second map; negative ids are refused; 0 is «none».
func TestTheDoorRefusesAMockupThatIsAlsoAMap(t *testing.T) {
	withBack := func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps = append(c.ColourMaps, &pb_common.DesignColourMap{
			MediaId: 21, View: entity.DesignViewBack, BaseMediaId: 2,
			Palette: []*pb_common.DesignColourSwatch{{Hex: "#3a7bd5", Px: 10}},
		})
	}
	// POSITIVE CONTROLS: no mockup at all, and one mockup per map on its own picture.
	require.NoError(t, designRefuseMalformedColourMaps(designProbeMapRecipe(nil)))
	require.NoError(t, designRefuseMalformedColourMaps(designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		withBack(c)
		c.ColourMaps[0].MockupMediaId = 30
		c.ColourMaps[1].MockupMediaId = 31
	})))

	for _, tc := range []struct {
		name string
		mut  func(*pb_common.DesignColourRecipe)
		want string
	}{
		{"the mockup is its own map", func(c *pb_common.DesignColourRecipe) {
			c.ColourMaps[0].MockupMediaId = 20
		}, "is the picture of colour_maps.0"},
		{"the mockup is another map", func(c *pb_common.DesignColourRecipe) {
			withBack(c)
			c.ColourMaps[0].MockupMediaId = 21
		}, "is the picture of colour_maps.1"},
		{"one mockup on two maps", func(c *pb_common.DesignColourRecipe) {
			withBack(c)
			c.ColourMaps[0].MockupMediaId = 30
			c.ColourMaps[1].MockupMediaId = 30
		}, "already the mockup of colour_maps.0"},
		{"a negative mockup", func(c *pb_common.DesignColourRecipe) {
			c.ColourMaps[0].MockupMediaId = -4
		}, "a mockup is a picture"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := designRefuseMalformedColourMaps(designProbeMapRecipe(tc.mut))
			require.Error(t, err)
			require.Equal(t, codes.InvalidArgument, status.Code(err))
			require.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestTheDoorRefusesAMockupThatIsAlsoAnInput — a plate, a reference or a cloth named as a mockup.
func TestTheDoorRefusesAMockupThatIsAlsoAnInput(t *testing.T) {
	inputs := &pb_common.DesignInputSnapshot{
		Slots: []*pb_common.DesignInputSlot{{ViewKey: entity.DesignViewFront, MediaId: 1}},
		Refs:  []*pb_common.DesignInputRef{{MediaId: 7, Role: "mood"}},
	}
	mockup := func(id int32) *pb_common.DesignRunParams {
		return designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) { c.ColourMaps[0].MockupMediaId = id })
	}
	require.NoError(t, designRefuseColourMapAlsoAnInput(mockup(30), inputs))
	require.NoError(t, designRefuseColourMapAlsoAnInput(mockup(0), inputs))

	for id, where := range map[int32]string{1: "the bench plate on front", 7: "«mood»", 9: "params.colour.fabrics.0.media_id"} {
		err := designRefuseColourMapAlsoAnInput(mockup(id), inputs)
		require.Error(t, err)
		require.Equal(t, codes.InvalidArgument, status.Code(err))
		require.Contains(t, err.Error(), where)
		require.Contains(t, err.Error(), "mockup_media_id")
		_, md := errorReason(t, err)
		require.Equal(t, "colour_map_mockup_is_also_an_input", md["reason"])
	}
}

// TestAMockupIsCountedAsAnImageOfTheCall — the reserve prices every picture the call carries.
func TestAMockupIsCountedAsAnImageOfTheCall(t *testing.T) {
	in := &pb_common.DesignInputSnapshot{}
	plain := designImageCallImages(entity.DesignRunKindRender, designProbeMapRecipe(nil), in, 0)
	with := designImageCallImages(entity.DesignRunKindRender, designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
		c.ColourMaps[0].MockupMediaId = 30
	}), in, 0)
	require.Equal(t, plain+1, with)
}

// TestAMockupHeldByAnotherCardReachesTheStore — T64 (05.10): владелец — медиатека общая,
// foreign_media больше не отказ. A mockup another card holds passes like any other and is frozen
// into params, where the worker reads it.
func TestAMockupHeldByAnotherCardReachesTheStore(t *testing.T) {
	run := func(t *testing.T, mockupID int32) (*designRunRig, error) {
		rig := newDesignRunRig(t, designMoodCard(), designBandWith(true))
		rig.design.EXPECT().AssertMediaNotForeign(mock.Anything, designRunCardID, mock.Anything).
			RunAndReturn(func(_ context.Context, _ int, ids []int) error {
				for _, id := range ids {
					if id == 666 {
						return entity.ErrDesignForeignMedia
					}
				}
				return nil
			}).Maybe()
		req := designStartRequest(entity.DesignRunKindRender)
		req.Params = designProbeMapRecipe(func(c *pb_common.DesignColourRecipe) {
			c.ColourMaps[0].MockupMediaId = mockupID
		})
		req.Params.Layout = designLayoutOne
		_, err := rig.srv.StartDesignRun(designRunCtx(), req)
		return rig, err
	}

	rig, err := run(t, 30)
	require.NoError(t, err)
	require.NotNil(t, rig.sent)
	require.Contains(t, string(rig.sent.Params), `"mockup_media_id":30`)

	rig, err = run(t, 666)
	require.NoError(t, err)
	require.NotNil(t, rig.sent, "a library picture held by another card is not refused")
	require.Contains(t, string(rig.sent.Params), `"mockup_media_id":666`)
}
