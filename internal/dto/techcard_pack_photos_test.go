package dto

import (
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

// The pack photos (0452): the list wins, the cover is its first element, an older client's single id
// is a one-photo list, and a read always fills both forms.
func TestParsePackagingPackedPhotos(t *testing.T) {
	sheet, err := parseTechCardPackaging(&pb_common.TechCardPackaging{PackedMediaId: 99, PackedMediaIds: []int32{5, 3, 8}})
	require.NoError(t, err)
	require.Equal(t, []int{5, 3, 8}, sheet.PackedMediaIds)
	require.EqualValues(t, 5, sheet.PackedMediaId.Int32, "the list wins: its first is the cover")
	back := techCardPackagingToPb(sheet)
	require.EqualValues(t, 5, back.PackedMediaId)
	require.Equal(t, []int32{5, 3, 8}, back.PackedMediaIds)

	old, err := parseTechCardPackaging(&pb_common.TechCardPackaging{PackedMediaId: 6})
	require.NoError(t, err)
	require.Nil(t, old.PackedMediaIds, "an older client: the store decides about stored further photos")
	require.EqualValues(t, 6, old.PackedMediaId.Int32)
	oldBack := techCardPackagingToPb(old)
	require.Equal(t, []int32{6}, oldBack.PackedMediaIds)
	require.EqualValues(t, 6, oldBack.PackedMediaId)

	none, err := parseTechCardPackaging(&pb_common.TechCardPackaging{})
	require.NoError(t, err)
	require.False(t, none.PackedMediaId.Valid)
	noneBack := techCardPackagingToPb(none)
	require.Zero(t, noneBack.PackedMediaId)
	require.Nil(t, noneBack.PackedMediaIds)

	_, err = parseTechCardPackaging(&pb_common.TechCardPackaging{PackedMediaIds: []int32{4, 4}})
	requireLabelFieldViolation(t, err, "packaging.packed_media_ids[1]")
	_, err = parseTechCardPackaging(&pb_common.TechCardPackaging{PackedMediaIds: []int32{0}})
	requireLabelFieldViolation(t, err, "packaging.packed_media_ids[0]")
}

// A cover-only sheet keeps its bytes (no stale signature at deploy); a second photo and the order of
// the further photos move it; the write form of an older client hashes like its read.
func TestPackagingDigestPackedPhotos(t *testing.T) {
	cover := func(ids ...int) string {
		tc := packagingGoldCard()
		tc.Packaging.PackedMediaId = ni32(int32(ids[0]))
		tc.Packaging.PackedMediaIds = ids
		return packagingDigest(tc)
	}
	legacy := packagingGoldCard()
	legacy.Packaging.PackedMediaId = ni32(5)
	require.Equal(t, packagingDigest(legacy), cover(5), "one photo = the bytes of the cover alone")

	two := cover(5, 3)
	require.NotEqual(t, cover(5), two)
	require.NotEqual(t, two, cover(5, 3, 8))
	require.NotEqual(t, cover(5, 3, 8), cover(5, 8, 3), "the order is content")
	require.NotEqual(t, two, cover(3, 5), "a new cover is content")
}

func TestParseCareLabelOrientation(t *testing.T) {
	got, err := parseTechCardCareLabel(&pb_common.TechCardCareLabel{})
	require.NoError(t, err)
	require.Equal(t, entity.CareLabelVertical, got.Orientation, `"" is vertical`)

	got, err = parseTechCardCareLabel(&pb_common.TechCardCareLabel{Orientation: "horizontal"})
	require.NoError(t, err)
	require.Equal(t, entity.CareLabelHorizontal, got.Orientation)
	require.Equal(t, "horizontal", techCardCareLabelToPb(got).Orientation)

	_, err = parseTechCardCareLabel(&pb_common.TechCardCareLabel{Orientation: "diagonal"})
	requireLabelFieldViolation(t, err, "care_label.orientation")

	require.Equal(t, "vertical", techCardCareLabelToPb(&entity.TechCardCareLabel{}).Orientation,
		"a record without one reads vertical")
}

// Vertical (or "") keeps the pre-0452 bytes; horizontal moves the LABELS digest.
func TestLabelsDigestOrientation(t *testing.T) {
	base := labelsDigest(&entity.TechCardInsert{CareLabel: &entity.TechCardCareLabel{}})
	require.Equal(t, base, labelsDigest(&entity.TechCardInsert{
		CareLabel: &entity.TechCardCareLabel{Orientation: entity.CareLabelVertical}}))
	require.NotEqual(t, base, labelsDigest(&entity.TechCardInsert{
		CareLabel: &entity.TechCardCareLabel{Orientation: entity.CareLabelHorizontal}}))
}
