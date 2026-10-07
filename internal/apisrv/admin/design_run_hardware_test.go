package admin

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// ─────────────────────────── R9 Ф1 · HARDWARE PAINTED ON PARTS AT THE DOOR ───────────────────────────
//
// A painted button travels as a fabric use of `kind: hardware` with its shelf asset id and its
// picture, and NO map label. The door needs no new rule for it: these tests pin that it passes the
// shelf check, that its picture counts against the engine's ceiling, and that a hardware use with
// no map label is no malformed colour map.

func hardwareRenderParams() *pb_common.DesignRunParams {
	return &pb_common.DesignRunParams{Colour: &pb_common.DesignColourRecipe{
		FabricMediaId: 9,
		ColourMaps:    []*pb_common.DesignColourMap{{MediaId: 20, View: "front", MockupMediaId: 30, Palette: []*pb_common.DesignColourSwatch{{Hex: "#3a7bd5", Px: 10}}}},
		Fabrics: []*pb_common.DesignFabricUse{
			{AssetId: 7, MediaId: 9, MapHex: "#3a7bd5", Kind: entity.DesignAssetKindFabric},
			{AssetId: 73, MediaId: 40, Name: "FRONT BUTTON", Parts: "2 on the front", Kind: entity.DesignAssetKindHardware},
		},
	}}
}

// TestR9HardwareAssetPassesTheShelfDoor — a hardware shelf row of the card is a shelf row.
func TestR9HardwareAssetPassesTheShelfDoor(t *testing.T) {
	assets := []entity.DesignAsset{
		{Id: 7, TechCardId: 41, Kind: entity.DesignAssetKindFabric},
		{Id: 73, TechCardId: 41, Kind: entity.DesignAssetKindHardware},
	}
	require.NoError(t, designRefuseForeignClothAssets(41, hardwareRenderParams(), assets))
	// …and another card's button is still refused.
	require.Error(t, designRefuseForeignClothAssets(41, hardwareRenderParams(), assets[:1]))
	require.NoError(t, designRefuseMalformedColourMaps(hardwareRenderParams()))
}

// TestR9HardwarePictureCountsAgainstTheCeiling — the button's picture is one more image of the call.
func TestR9HardwarePictureCountsAgainstTheCeiling(t *testing.T) {
	in := &pb_common.DesignInputSnapshot{Slots: []*pb_common.DesignInputSlot{{ViewKey: "front", MediaId: 1}}}
	with := designImageCallRequiredImages(entity.DesignRunKindRender, hardwareRenderParams(), in, nil)
	p := hardwareRenderParams()
	p.Colour.Fabrics = p.Colour.Fabrics[:1]
	without := designImageCallRequiredImages(entity.DesignRunKindRender, p, in, nil)
	require.Equal(t, without+1, with)
	// plate 1 · cloth 9 · map 20 · mockup 30 · button 40
	require.Equal(t, 5, with)
}
