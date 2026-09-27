package dto

import (
	"errors"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// T45 (27.09) — the wire half of the colourway palette: reading development.colours /
// development.name_i18n with their mask semantics, the development-only update mask, the family
// proposal and the read projections. The dictionary is the one main_test.go seeds (BLK, WHT, OFW,
// NAV, and UNK without a hex).

func requireFieldViolation(t *testing.T, err error, field, reason string) {
	t.Helper()
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "want a *entity.ValidationError, got %T %v", err, err)
	require.Equal(t, field, ve.Field, ve.Message)
	require.Equal(t, reason, ve.Reason, ve.Message)
}

func mask(paths ...string) *fieldmaskpb.FieldMask { return &fieldmaskpb.FieldMask{Paths: paths} }

func TestColorwayDevelopmentPatchReadsThePalette(t *testing.T) {
	dev := &pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{
		{Label: " bone ", Hex: "#efe9dc", Pantone: "11-0602", PantoneSystem: "tcx"},
		{Pantone: "19-4005 TCX"},
	}}
	want := []entity.ColorwayColour{
		{Label: "bone", Hex: "#EFE9DC", Pantone: "11-0602", PantoneSystem: "TCX"},
		{Pantone: "19-4005 TCX"},
	}
	// The palette editor's write: the mask names the palette, nothing else is touched.
	patch, err := ColorwayDevelopmentPatchFromPb(dev, mask("development.colours"))
	require.NoError(t, err)
	require.Equal(t, want, patch.Colours, "validated and canonicalised whole")
	require.False(t, patch.HasScalars(), "a masked palette write is not a scalar write")

	// No mask = every development leaf the request carries (pre-T45 semantics), the palette included.
	patch, err = ColorwayDevelopmentPatchFromPb(dev, nil)
	require.NoError(t, err)
	require.Equal(t, want, patch.Colours)
}

func TestColorwayDevelopmentPatchRefusesABadPalette(t *testing.T) {
	_, err := ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{
		{Label: "ok"}, {Label: "bad", Hex: "black"},
	}}, nil)
	requireFieldViolation(t, err, "development.colours[1].hex", "invalid_hex")

	// A nil element reads as an empty colour and is refused with its index, not a panic.
	_, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{Colours: []*pb_common.ColorwayColour{nil}}, nil)
	requireFieldViolation(t, err, "development.colours[0]", "empty_colour")
}

// SPARSE: an empty list is «leave the palette», so a client that never heard of colours cannot wipe
// it — UNLESS the mask names development.colours explicitly, which is a statement about the value.
// MUTATION: treat an unnamed empty list as «clear» — the lab-dip panel's save wipes every palette.
func TestColorwayDevelopmentPatchPaletteMaskSemantics(t *testing.T) {
	patch, err := ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{}, nil)
	require.NoError(t, err)
	require.Nil(t, patch.Colours, "no mask and an empty list = leave the stored palette")
	require.Nil(t, patch.NameI18n, "no mask and an empty map = leave the translations")
	patch, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{}, mask("development.colours", "development.labDipRound"))
	require.Error(t, err, "named explicitly, an empty palette is a statement, and it is refused")
	patch, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{}, mask("merchandising"))
	require.NoError(t, err)
	require.Nil(t, patch, "a mask that selects nothing under development = no development write")

	for _, path := range []string{"development.colours", "Development.Colours"} {
		_, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{}, mask(path))
		requireFieldViolation(t, err, "development.colours", "palette_size")
	}

	// A mask that selects only the lab-dip leaves ignores a palette that rides along.
	status := pb_common.TechCardLabDipStatus_TECH_CARD_LAB_DIP_STATUS_SUBMITTED
	patch, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{
		LabDipStatus: status,
		Colours:      []*pb_common.ColorwayColour{{Label: "x"}},
	}, mask("development.labDipStatus"))
	require.NoError(t, err)
	require.NotNil(t, patch)
	require.Nil(t, patch.Colours, "the mask did not select development.colours")
	require.NotNil(t, patch.LabDipStatus)
}

func TestColorwayDevelopmentPatchReadsNameTranslations(t *testing.T) {
	patch, err := ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{
		NameI18N: map[int32]string{1: " Nuit ", 2: ""},
	}, mask("development.nameI18n"))
	require.NoError(t, err)
	require.Equal(t, map[int]string{1: "Nuit", 2: ""}, patch.NameI18n, `"" survives as «delete that language»`)

	_, err = ColorwayDevelopmentPatchFromPb(&pb_common.ColorwayDevelopmentInsert{
		NameI18N: map[int32]string{0: "x"},
	}, nil)
	requireFieldViolation(t, err, "development.name_i18n[0]", "invalid_language")
}

func TestUpdateMaskIsDevelopmentOnly(t *testing.T) {
	cases := []struct {
		paths []string
		want  bool
	}{
		{nil, false}, // no mask: the pre-T45 full merchandising replace
		{[]string{"development.labDipStatus", "development.labDipRound"}, true},
		{[]string{"development"}, true},
		{[]string{"development.colours", " Development.name_i18n "}, true},
		{[]string{"merchandising", "development.colours"}, false},
		{[]string{"costPrice"}, false},
		{[]string{"developmentx.colours"}, false},
	}
	for _, tc := range cases {
		var m *fieldmaskpb.FieldMask
		if tc.paths != nil {
			m = mask(tc.paths...)
		}
		require.Equal(t, tc.want, UpdateMaskIsDevelopmentOnly(m), "%v", tc.paths)
	}
}

// ─── the family (owner's decision 4) ───

func TestColorwayBodyAcceptsAnEmptyFamily(t *testing.T) {
	got, err := convertMerchInsertToEntity(&pb_common.ColorwayMerchandisingInsert{SkuColorToken: " bkw "}, "")
	require.NoError(t, err, "an empty family is proposed (create) or kept (update), never refused here")
	require.Empty(t, got.ColorCode)
	require.Equal(t, "BKW", got.SkuColorToken, "carried upper-cased for the store's echo guard")
}

func TestResolveColorwayFamily(t *testing.T) {
	named := &entity.ColorwayInsert{ProductBodyInsert: entity.ColorwayBodyInsert{ColorCode: "WHT", Color: "white"}}
	require.NoError(t, ResolveColorwayFamily(named, "#000000", true))
	require.Equal(t, "WHT", named.ProductBodyInsert.ColorCode, "a family the request named is kept")

	for _, tc := range []struct{ hex, code, name string }{
		{"#2B2C30", "BLK", "black"}, // 19-4005 TCX: a fabric black is BLK, not NAV
		{"#1F2A44", "NAV", "navy"},
		{"#EFE9DC", "OFW", "off-white"},
	} {
		prd := &entity.ColorwayInsert{}
		require.NoError(t, ResolveColorwayFamily(prd, tc.hex, true))
		require.Equal(t, tc.code, prd.ProductBodyInsert.ColorCode, tc.hex)
		require.Equal(t, tc.name, prd.ProductBodyInsert.Color, "the dictionary name travels with the code")
	}

	prd := &entity.ColorwayInsert{}
	requireFieldViolation(t, ResolveColorwayFamily(prd, "", true), "merchandising.color_code", "family_required")
	require.NoError(t, ResolveColorwayFamily(prd, "", false), "update: empty = keep the stored family")
	require.Empty(t, prd.ProductBodyInsert.ColorCode)
}

func TestColorwayMainHex(t *testing.T) {
	devHex := " #111111 "
	require.Equal(t, "#222222", ColorwayMainHex(&entity.ColorwayDevelopmentPatch{
		DevHex: &devHex, Colours: []entity.ColorwayColour{{Label: "a", Hex: "#222222"}},
	}), "the palette's main colour wins")
	require.Equal(t, "#111111", ColorwayMainHex(&entity.ColorwayDevelopmentPatch{DevHex: &devHex}))
	require.Equal(t, "", ColorwayMainHex(nil))
}

// ─── wire helpers and read projections ───

func TestColorwayColoursRoundTrip(t *testing.T) {
	in := []entity.ColorwayColour{{Label: "bone", Hex: "#EFE9DC", Pantone: "11-0602", PantoneSystem: "TCX"}, {Pantone: "Black 6 C"}}
	require.Equal(t, in, ColorwayColoursFromPb(ColorwayColoursToPb(in)))
	require.Nil(t, ColorwayColoursToPb(nil), "a legacy colourway has no palette on the wire")
	require.Nil(t, ColorwayNameI18nToPb(nil))
	require.Equal(t, map[int32]string{3: "Nuit"}, ColorwayNameI18nToPb(map[int]string{3: "Nuit"}))
}

func TestColorwayPaletteSlotAssignmentsFromPb(t *testing.T) {
	got := ColorwayPaletteSlotAssignmentsFromPb([]*pb_admin.ColorwayPaletteSlotAssignment{
		{BomLineKey: "BOM-1", ColourPosition: 2}, nil,
	})
	require.Equal(t, []entity.ColorwayPaletteSlotAssignment{{BomLineKey: "BOM-1", ColourPosition: 2}, {}}, got,
		"a nil element becomes an empty key that validation refuses by index")
}

func TestTechCardColorwayRefCarriesTheT45Facts(t *testing.T) {
	tc := &entity.TechCard{}
	tc.Colorways = []entity.TechCardColorway{{
		Id: 7, ColorCode: "BLK", SkuColorToken: "BKW",
		Colours:  []entity.ColorwayColour{{Label: "black"}, {Label: "white", Hex: "#FFFFFF"}},
		NameI18n: map[int]string{2: "noir et blanc"},
	}}
	refs := techCardColorwayRefsToPb(tc, nil, CostingFx{})
	require.Len(t, refs, 1)
	require.Equal(t, "BLK", refs[0].GetColorCode(), "the family")
	require.Equal(t, "BKW", refs[0].GetSkuColorToken(), "the SKU segment")
	require.Len(t, refs[0].GetColours(), 2)
	require.Equal(t, "white", refs[0].GetColours()[1].GetLabel())
	require.Equal(t, "noir et blanc", refs[0].GetNameI18N()[2])
}
