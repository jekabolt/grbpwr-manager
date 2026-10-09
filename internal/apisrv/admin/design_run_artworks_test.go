package admin

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// manyArtworks — n frozen artworks with distinct pictures (media 300+i) on the front flat.
func manyArtworks(n int) []designFrozenArtwork {
	out := make([]designFrozenArtwork, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, designFrozenArtwork{AssetID: 40 + i, MediaID: 300 + i, View: "front", Name: "print"})
	}
	return out
}

// TestDesignFreezeArtworksDoesNotTruncate — a fifth placed artwork is frozen, not silently dropped:
// the door refuses it in words.
func TestDesignFreezeArtworksDoesNotTruncate(t *testing.T) {
	card, band := artworkBand(t)
	quad := band.AssetPlacements[1].Annotation
	for i := 0; i < 4; i++ {
		band.AssetPlacements = append(band.AssetPlacements,
			entity.DesignAssetPlacement{Id: 10 + i, AssetId: 40, PictureId: 501, Annotation: quad})
	}
	got := designFreezeArtworks(entity.DesignRunKindRender, &pb_common.DesignRunParams{ColorwayId: 13},
		card, band, artworkInputs(1, 2))
	require.Len(t, got, 5)
}

// TestDesignRefuseRenderArtworksTooMany — more than four placed artworks is InvalidArgument
// `too_many_artworks` before the reserve; four pass; other kinds and no artworks are untouched.
func TestDesignRefuseRenderArtworksTooMany(t *testing.T) {
	s := engineServer()
	p := &pb_common.DesignRunParams{ColorwayId: 13}
	in := artworkInputs(1, 2)

	err := s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, manyArtworks(5))
	require.Equal(t, designErrorCodeTooManyArtworks, ffReason(t, err))
	require.Equal(t, "too_many_artworks", designErrorCodeTooManyArtworks)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Contains(t, err.Error(), "at most 4 placed artworks per render · remove one on PARTS")

	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, manyArtworks(4)))
	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, nil))
	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindFlat, p, in, manyArtworks(5)))
}

// renderAtCeiling — a render whose plates and references fill the engine's ceiling exactly.
func renderAtCeiling(t *testing.T) (*Server, designgen.Engine, *pb_common.DesignRunParams, *pb_common.DesignInputSnapshot) {
	t.Helper()
	s := engineServer()
	engine, ok := designgen.FindEngine(s.designEngineTable(), "")
	require.True(t, ok)
	require.Greater(t, engine.MaxRefs, 2)
	in := artworkInputs(1, 2)
	for i := 0; i < engine.MaxRefs-2; i++ {
		in.Refs = append(in.Refs, &pb_common.DesignInputRef{MediaId: int32(500 + i)})
	}
	return s, engine, &pb_common.DesignRunParams{ColorwayId: 13}, in
}

// TestDesignRefuseRenderArtworksOverTheEngineCeiling — artwork pictures count against the engine's
// reference ceiling at the door (deduplicated), so an over-ceiling render is refused before the reserve.
func TestDesignRefuseRenderArtworksOverTheEngineCeiling(t *testing.T) {
	s, engine, p, in := renderAtCeiling(t)
	require.Equal(t, engine.MaxRefs, designImageCallImagesWithArtworks(entity.DesignRunKindRender, p, in, nil, 0))

	over := manyArtworks(1)
	require.Equal(t, engine.MaxRefs+1, designImageCallRequiredImages(entity.DesignRunKindRender, p, in, over))
	err := s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, over)
	require.Equal(t, "too_many_pictures", ffReason(t, err))

	// An artwork whose picture already travels (here: the front plate) adds nothing it must carry —
	// its front guide (T27) is optional and does not move the door.
	dup := []designFrozenArtwork{{AssetID: 40, MediaID: 1, View: "front"}}
	require.Equal(t, engine.MaxRefs, designImageCallRequiredImages(entity.DesignRunKindRender, p, in, dup))
	require.Equal(t, engine.MaxRefs+1, designImageCallImagesWithArtworks(entity.DesignRunKindRender, p, in, dup, 0))
	require.Equal(t, engine.MaxRefs, designImageCallImagesWithArtworks(entity.DesignRunKindRender, p, in, dup, engine.MaxRefs))
	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, dup))
}

// TestDesignArtworkGuidesAreOptionalAtTheDoor — T27: one placement guide per side carrying artworks
// is counted for the reserve (within the ceiling) and never refused: the worker drops guides
// side_r, side_l, back, front under the ceiling before anything else.
func TestDesignArtworkGuidesAreOptionalAtTheDoor(t *testing.T) {
	arts := []designFrozenArtwork{
		{MediaID: 30, View: "front"}, {MediaID: 31, View: "front"},
		{MediaID: 32, View: "back"}, {MediaID: 33, View: "side_r"}, {MediaID: 34, View: "detail"},
	}
	require.Equal(t, 3, designArtworkGuideCount(arts))
	require.Equal(t, 0, designArtworkGuideCount(nil))

	p := &pb_common.DesignRunParams{ColorwayId: 13}
	in := artworkInputs(1, 2)
	req := designImageCallRequiredImages(entity.DesignRunKindRender, p, in, arts)
	require.Equal(t, req+3, designImageCallImagesWithArtworks(entity.DesignRunKindRender, p, in, arts, 0))
	require.Equal(t, req+1, designImageCallImagesWithArtworks(entity.DesignRunKindRender, p, in, arts, req+1))
	// Other kinds carry no guides.
	require.Equal(t, designImageCallRequiredImages(entity.DesignRunKindFlat, p, in, nil),
		designImageCallImagesWithArtworks(entity.DesignRunKindFlat, p, in, nil, 0))
}

// TestDesignEstimateCountsArtworkPictures — the reserve prices every distinct artwork picture, and
// each side's placement guide, as an input image of each call.
func TestDesignEstimateCountsArtworkPictures(t *testing.T) {
	s := engineServer()
	engine, ok := designgen.FindEngine(s.designEngineTable(), "")
	require.True(t, ok)
	p := &pb_common.DesignRunParams{ColorwayId: 13, Image: &pb_common.DesignImageOptions{Model: engine.Slug, Quality: "high"}}
	in := artworkInputs(1, 2)

	plain := s.designEstimateForRunWithArtworks(entity.DesignRunKindRender, 1, p, in, nil)
	require.True(t, plain.Valid)
	require.Equal(t, plain, s.designEstimateForRun(entity.DesignRunKindRender, 1, p, in))

	// Two front artworks: two pictures plus ONE front placement guide (T27).
	two := s.designEstimateForRunWithArtworks(entity.DesignRunKindRender, 1, p, in, manyArtworks(2))
	require.True(t, two.Decimal.Equal(plain.Decimal.Add(engine.InputUSD.Mul(decimal.NewFromInt(3)))),
		"%s vs %s", two.Decimal, plain.Decimal)

	dup := s.designEstimateForRunWithArtworks(entity.DesignRunKindRender, 1, p, in,
		[]designFrozenArtwork{{MediaID: 1, View: "front"}})
	require.True(t, dup.Decimal.Equal(plain.Decimal.Add(engine.InputUSD)),
		"a picture already sent is not priced twice; only its guide is")
}

// TestDesignArtworkMediaRefsJoinTheMediaDoors — the format / display-only / hidden doors see the
// artwork pictures, deduplicated, named by where they came from.
func TestDesignArtworkMediaRefsJoinTheMediaDoors(t *testing.T) {
	refs := designRunInputMediaRefs(&pb_common.DesignRunParams{}, artworkInputs(1, 2))
	got := designArtworkMediaRefs(refs, []designFrozenArtwork{
		{MediaID: 30, View: "front", Name: "chest embroidery"},
		{MediaID: 1, View: "front"},
		{MediaID: 30, View: "back"},
	})
	require.Len(t, got, 3)
	require.Equal(t, 30, got[2].ID)
	require.Equal(t, "the artwork «chest embroidery» placed on the front flat", got[2].Where)
	require.Equal(t, refs, designArtworkMediaRefs(refs, nil))
}

// TestDesignRefuseRenderOverTheCeilingWithoutArtworks — T24, the beta run 72 shape: 4 bench plates
// + 3 references + 4 colour maps + 4 cloth mockups + 2 cloths = 17 against GPT Image 2's 16. No
// artworks, and still refused at the door (InvalidArgument, too_many_pictures) — before, only a
// render with artworks was counted and this one failed in the worker after the reserve.
func TestDesignRefuseRenderOverTheCeilingWithoutArtworks(t *testing.T) {
	s := engineServer()
	engine, ok := designgen.FindEngine(s.designEngineTable(), "")
	require.True(t, ok)
	require.Equal(t, 16, engine.MaxRefs)

	in := &pb_common.DesignInputSnapshot{}
	views := []string{entity.DesignViewFront, entity.DesignViewBack, entity.DesignViewSideL, entity.DesignViewSideR}
	for i, v := range views {
		in.Slots = append(in.Slots, &pb_common.DesignInputSlot{ViewKey: v, MediaId: int32(100 + i)})
	}
	for i := 0; i < 3; i++ {
		in.Refs = append(in.Refs, &pb_common.DesignInputRef{MediaId: int32(200 + i)})
	}
	c := &pb_common.DesignColourRecipe{Fabrics: []*pb_common.DesignFabricUse{{MediaId: 400}, {MediaId: 401}}}
	for i, v := range views {
		c.ColourMaps = append(c.ColourMaps, &pb_common.DesignColourMap{
			MediaId: int32(300 + i), View: v, MockupMediaId: int32(310 + i)})
	}
	p := &pb_common.DesignRunParams{ColorwayId: 13, Colour: c}
	require.Equal(t, 17, designImageCallImages(entity.DesignRunKindRender, p, in, 0))

	err := s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, nil)
	require.Equal(t, codes.InvalidArgument, status.Code(err))
	require.Equal(t, "too_many_pictures", ffReason(t, err))
	require.Contains(t, err.Error(), "this render would send 17 pictures and "+engine.Label+" takes at most 16")

	// One mockup fewer fits exactly: the door is a ceiling, not a guess.
	c.ColourMaps[3].MockupMediaId = 0
	require.Equal(t, 16, designImageCallImages(entity.DesignRunKindRender, p, in, 0))
	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindRender, p, in, nil))
	// Other kinds are not this door's.
	c.ColourMaps[3].MockupMediaId = 313
	require.NoError(t, s.designRefuseRenderArtworks(entity.DesignRunKindFlat, p, in, nil))
}
