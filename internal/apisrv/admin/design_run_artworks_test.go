package admin

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// ─────────────────────────── ARTWORK PLACEMENTS → RENDER SNAPSHOT (70-ROUND7 B7) ───────────────────────────

func artworkQuadAnnotation(pts ...[2]string) *pb_common.TechCardAnnotation {
	a := &pb_common.TechCardAnnotation{Kind: pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_POLYGON}
	for _, p := range pts {
		a.Points = append(a.Points, &pb_common.TechCardAnnotationPoint{
			X: &pb_decimal.Decimal{Value: p[0]}, Y: &pb_decimal.Decimal{Value: p[1]}})
	}
	return a
}

func artworkQuadJSON(t *testing.T, a *pb_common.TechCardAnnotation) entity.RawJSON {
	t.Helper()
	raw, err := designMarshalJSON(a)
	require.NoError(t, err)
	return entity.RawJSON(raw)
}

// TestSetDesignAssetPlacementAcceptsAQuad — the placement verb takes a 4-corner POLYGON (TL, TR, BR,
// BL) through the card's one annotation validator; DIM stays a 2-point shape.
func TestSetDesignAssetPlacementAcceptsAQuad(t *testing.T) {
	rig := newDesignAssetRig(t)
	rig.expectPlacement(&entity.DesignAssetPlacement{Id: 8, AssetId: 12, PictureId: 91}, nil)
	quad := artworkQuadAnnotation([2]string{"0.34", "0.22"}, [2]string{"0.48", "0.25"},
		[2]string{"0.47", "0.39"}, [2]string{"0.33", "0.36"})
	_, err := rig.srv.SetDesignAssetPlacement(designRunCtx(), &pb_admin.SetDesignAssetPlacementRequest{
		TechCardId: designRunCardID, AssetId: 12, PictureId: 91, Annotation: quad, Note: "embroidery",
	})
	require.NoError(t, err)
	require.NotNil(t, rig.sentPlacement)
	corners, ok := designArtworkQuad(rig.sentPlacement.Annotation)
	require.True(t, ok)
	require.Equal(t, []designArtworkCorn{{0.34, 0.22}, {0.48, 0.25}, {0.47, 0.39}, {0.33, 0.36}}, corners)

	dim := artworkQuadAnnotation([2]string{"0.1", "0.1"}, [2]string{"0.2", "0.1"},
		[2]string{"0.2", "0.2"}, [2]string{"0.1", "0.2"})
	dim.Kind = pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM
	_, err = designAssetAnnotationJSON(dim)
	require.Error(t, err, "DIM is two points; a quad is a POLYGON")
}

// TestDesignArtworkQuadReadsPolygonAndLegacyBox — 4-point POLYGON as is; 2-point DIM box expands to
// TL, TR, BR, BL; anything else is not an artwork place.
func TestDesignArtworkQuadReadsPolygonAndLegacyBox(t *testing.T) {
	box := artworkQuadAnnotation([2]string{"0.5", "0.4"}, [2]string{"0.2", "0.1"})
	box.Kind = pb_common.TechCardAnnotationKind_TECH_CARD_ANNOTATION_KIND_DIM
	got, ok := designArtworkQuad(artworkQuadJSON(t, box))
	require.True(t, ok)
	require.Equal(t, []designArtworkCorn{{0.2, 0.1}, {0.5, 0.1}, {0.5, 0.4}, {0.2, 0.4}}, got)

	tri := artworkQuadAnnotation([2]string{"0.1", "0.1"}, [2]string{"0.2", "0.1"}, [2]string{"0.2", "0.2"})
	_, ok = designArtworkQuad(artworkQuadJSON(t, tri))
	require.False(t, ok)
	_, ok = designArtworkQuad(nil)
	require.False(t, ok)
}

// artworkBand: front flat (picture 501, media 1) and back flat (picture 502, media 2); one artwork
// asset (40, media 30) bound to colourway 13 on DECORATION line 904, placed on the front; a button
// asset (41) bound on hardware line 905 and placed too; the same artwork's binding on colourway 14.
func artworkBand(t *testing.T) (*entity.TechCard, *entity.DesignBand) {
	t.Helper()
	card := &entity.TechCard{}
	card.BomItems = []entity.TechCardBomItem{
		{Id: 904, Section: entity.BomSectionDecoration, Name: "chest embroidery",
			Spec: sql.NullString{String: "embroidery", Valid: true}},
		{Id: 905, Section: entity.BomSectionHardware, Name: "button"},
	}
	quad := artworkQuadJSON(t, artworkQuadAnnotation([2]string{"0.34", "0.22"}, [2]string{"0.48", "0.25"},
		[2]string{"0.47", "0.39"}, [2]string{"0.33", "0.36"}))
	band := &entity.DesignBand{
		Bench: []entity.DesignBenchSlot{
			{Id: 1, ViewKey: "back", Kind: entity.DesignPictureKindFlat, PictureId: sql.NullInt32{Int32: 502, Valid: true},
				Picture: &entity.DesignPicture{Id: 502, MediaId: 2}},
			{Id: 2, ViewKey: "front", Kind: entity.DesignPictureKindFlat, PictureId: sql.NullInt32{Int32: 501, Valid: true},
				Picture: &entity.DesignPicture{Id: 501, MediaId: 1}},
		},
		Assets: []entity.DesignAsset{
			{Id: 40, Kind: entity.DesignAssetKindHardware, Name: "chest embroidery · ROSSO",
				MediaId: sql.NullInt32{Int32: 30, Valid: true}},
			{Id: 41, Kind: entity.DesignAssetKindHardware, Name: "button",
				MediaId: sql.NullInt32{Int32: 31, Valid: true}},
		},
		AssetBindings: []entity.DesignAssetBinding{
			{ColorwayId: 13, BomItemId: 904, AssetId: 40},
			{ColorwayId: 13, BomItemId: 905, AssetId: 41},
		},
		AssetPlacements: []entity.DesignAssetPlacement{
			{Id: 2, AssetId: 41, PictureId: 501, Annotation: quad},
			{Id: 1, AssetId: 40, PictureId: 501, Annotation: quad, Note: sql.NullString{String: "embroidery · cut", Valid: true}},
		},
	}
	return card, band
}

func artworkInputs(medias ...int32) *pb_common.DesignInputSnapshot {
	in := &pb_common.DesignInputSnapshot{}
	views := map[int32]string{1: "front", 2: "back"}
	for _, m := range medias {
		in.Slots = append(in.Slots, &pb_common.DesignInputSlot{ViewKey: views[m], MediaId: m})
	}
	return in
}

// TestDesignFreezeArtworksTakesTheColourwaysDecorationOnSentFlats — the freeze reads ONLY placements
// of assets bound to the run's colourway on DECORATION lines, on flats the run actually sends.
func TestDesignFreezeArtworksTakesTheColourwaysDecorationOnSentFlats(t *testing.T) {
	card, band := artworkBand(t)
	params := &pb_common.DesignRunParams{ColorwayId: 13}

	got := designFreezeArtworks(entity.DesignRunKindRender, params, card, band, artworkInputs(1, 2))
	require.Equal(t, []designFrozenArtwork{{
		AssetID: 40, BomItemID: 904, Name: "chest embroidery", MediaID: 30, View: "front", PictureID: 501,
		FlatMediaID: 1, Corners: []designArtworkCorn{{0.34, 0.22}, {0.48, 0.25}, {0.47, 0.39}, {0.33, 0.36}},
		Note: "embroidery",
	}}, got, "the button on a hardware line is not an artwork")

	require.Empty(t, designFreezeArtworks(entity.DesignRunKindRender, params, card, band, artworkInputs(2)),
		"the front flat is not sent: its artwork has nowhere to point")
	require.Empty(t, designFreezeArtworks(entity.DesignRunKindRender, &pb_common.DesignRunParams{ColorwayId: 14},
		card, band, artworkInputs(1, 2)), "another colourway's binding")
	require.Empty(t, designFreezeArtworks(entity.DesignRunKindRender, &pb_common.DesignRunParams{}, card, band,
		artworkInputs(1, 2)), "colourway 0 binds nothing")
	require.Empty(t, designFreezeArtworks(entity.DesignRunKindThreed, params, card, band, artworkInputs(1, 2)))
}

// TestDesignSpliceArtworksWritesTheKeyDesigngenReads — the spliced snapshot keeps every proto key,
// adds `artworks`, still parses with the band's DiscardUnknown reader, and an empty list is a no-op.
func TestDesignSpliceArtworksWritesTheKeyDesigngenReads(t *testing.T) {
	card, band := artworkBand(t)
	inputs := artworkInputs(1, 2)
	raw, err := designMarshalJSON(inputs)
	require.NoError(t, err)

	same, err := designSpliceArtworks(raw, nil)
	require.NoError(t, err)
	require.Equal(t, raw, same)

	arts := designFreezeArtworks(entity.DesignRunKindRender, &pb_common.DesignRunParams{ColorwayId: 13}, card, band, inputs)
	spliced, err := designSpliceArtworks(raw, arts)
	require.NoError(t, err)

	var obj map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(spliced, &obj))
	require.Contains(t, obj, "slots")
	require.Contains(t, obj, "artworks")
	back := &pb_common.DesignInputSnapshot{}
	require.NoError(t, designUnmarshalJSON(spliced, back))
	require.Len(t, back.GetSlots(), 2)

	// A rerun carries the parent's frozen copy.
	require.Equal(t, arts, designParentArtworks(&entity.DesignRun{Inputs: entity.RawJSON(spliced)}))
	require.Empty(t, designParentArtworks(&entity.DesignRun{Inputs: entity.RawJSON(raw)}))
}
