package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// 0452: a bundle that knows neither packed_media_ids nor orientation saves the card as it read it; the
// history chip must not call that a packaging or labels change.
func TestTechCardChangedSectionsOlderBundlePackPhotosAndOrientation(t *testing.T) {
	before := snapshotDiffCard()
	before.Packaging = &pb_common.TechCardPackaging{PackedMediaId: 5, PackedMediaIds: []int32{5, 3, 8}}
	before.CareLabel = &pb_common.TechCardCareLabel{QrPreset: "storefront", Orientation: "vertical"}

	older := proto.Clone(before).(*pb_common.TechCardInsert)
	older.Packaging.PackedMediaIds = nil
	older.CareLabel.Orientation = ""
	require.Empty(t, techCardChangedSections(before, older))

	// A cover-only card read before 0452 against a bundle that knows the list.
	legacy := snapshotDiffCard()
	legacy.Packaging = &pb_common.TechCardPackaging{PackedMediaId: 5}
	current := proto.Clone(legacy).(*pb_common.TechCardInsert)
	current.Packaging.PackedMediaIds = []int32{5}
	require.Empty(t, techCardChangedSections(legacy, current))

	for name, edit := range map[string]func(*pb_common.TechCardInsert){
		"older bundle, another cover": func(a *pb_common.TechCardInsert) {
			a.Packaging.PackedMediaIds, a.Packaging.PackedMediaId = nil, 3
		},
		"reorder":      func(a *pb_common.TechCardInsert) { a.Packaging.PackedMediaIds = []int32{5, 8, 3} },
		"one photo":    func(a *pb_common.TechCardInsert) { a.Packaging.PackedMediaIds = []int32{5} },
		"no photos":    func(a *pb_common.TechCardInsert) { a.Packaging.PackedMediaIds, a.Packaging.PackedMediaId = nil, 0 },
		"horizontal":   func(a *pb_common.TechCardInsert) { a.CareLabel.Orientation = "horizontal" },
		"stale cover":  func(a *pb_common.TechCardInsert) { a.Packaging.PackedMediaId = 3 },
		"further only": func(a *pb_common.TechCardInsert) { a.Packaging.PackedMediaIds = []int32{3, 8} },
	} {
		after := proto.Clone(before).(*pb_common.TechCardInsert)
		edit(after)
		got := techCardChangedSections(before, after)
		if name == "stale cover" {
			// The list wins over a cover that disagrees with it: nothing the save writes changes.
			require.Empty(t, got, name)
			continue
		}
		require.NotEmpty(t, got, name)
	}
}

// The older-bundle rule the update handler applies before the sign-off restamp (and the store again
// in the transaction): the stored cover alone keeps the stored photos, anything else is left alone.
func TestCarryStoredPackedPhotos(t *testing.T) {
	cover := func(id int32) *entity.TechCardPackaging {
		return &entity.TechCardPackaging{PackedMediaId: sql.NullInt32{Int32: id, Valid: true}}
	}
	p := cover(5)
	p.CarryStoredPackedPhotos([]int{5, 3, 8})
	require.Equal(t, []int{5, 3, 8}, p.PackedMediaIds)

	p = cover(3)
	p.CarryStoredPackedPhotos([]int{5, 3, 8})
	require.Nil(t, p.PackedMediaIds, "another cover is a one-photo list")

	p = cover(5)
	p.CarryStoredPackedPhotos([]int{5})
	require.Nil(t, p.PackedMediaIds, "nothing further to keep")

	p = &entity.TechCardPackaging{PackedMediaId: sql.NullInt32{Int32: 5, Valid: true}, PackedMediaIds: []int{5}}
	p.CarryStoredPackedPhotos([]int{5, 3, 8})
	require.Equal(t, []int{5}, p.PackedMediaIds, "a list is what the client means")

	p = &entity.TechCardPackaging{}
	p.CarryStoredPackedPhotos([]int{5, 3})
	require.Nil(t, p.PackedMediaIds, "no photo at all clears them")

	var none *entity.TechCardPackaging
	none.CarryStoredPackedPhotos([]int{5, 3}) // an absent sheet is preserved by the store, not here

	stored := []int{5, 3, 8}
	p = cover(5)
	p.CarryStoredPackedPhotos(stored)
	p.PackedMediaIds[1] = 99
	require.Equal(t, []int{5, 3, 8}, stored, "the carried list is a copy")
}
