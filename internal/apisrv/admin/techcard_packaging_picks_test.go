package admin

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func picksStored() *entity.TechCard {
	return &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		PackagingItems: []entity.TechCardPackagingItem{{Key: "polybag", MaterialId: sql.NullInt32{Int32: 7, Valid: true}}},
	}}
}

// A stale bundle cannot tell «this card has none» from «I never heard of them», so its full replace is
// refused. Anything that shows it knows the fields passes, and a card with nothing to lose stays writable.
func TestPackagingPicksStoredGate(t *testing.T) {
	stale := &pb_common.TechCardInsert{LabelsAware: true}
	err := packagingPicksStoredGate(stale, picksStored())
	wantFailedPrecondition(t, err, "stale full replace over a stored pick")
	require.True(t, strings.Contains(err.Error(), "hard-refresh"), "the refusal must say how to recover: %v", err)

	for name, stored := range map[string]*entity.TechCard{
		"per order": {TechCardInsert: entity.TechCardInsert{PackagingItems: []entity.TechCardPackagingItem{{Key: "a", PerOrder: true}}}},
		"colourway": {TechCardInsert: entity.TechCardInsert{PackagingItems: []entity.TechCardPackagingItem{{Key: "a", ColorwayIds: []int{1}}}}},
	} {
		wantFailedPrecondition(t, packagingPicksStoredGate(stale, stored), name)
	}

	sheet := &entity.TechCard{TechCardInsert: entity.TechCardInsert{Packaging: &entity.TechCardPackaging{
		PackedSize: sql.NullString{String: "30x20", Valid: true},
	}}}
	wantFailedPrecondition(t, packagingPicksStoredGate(&pb_common.TechCardInsert{Packaging: &pb_common.TechCardPackaging{Notes: "n"}}, sheet),
		"stale sheet over stored packed facts")

	for name, c := range map[string]struct {
		pb     *pb_common.TechCardInsert
		stored *entity.TechCard
	}{
		"aware may clear":          {&pb_common.TechCardInsert{LabelsAware: true, PackagingPicksAware: true}, picksStored()},
		"payload knows the fields": {&pb_common.TechCardInsert{LabelsAware: true, PackagingItems: []*pb_common.TechCardPackagingItem{{Key: "a", PerOrder: true}}}, picksStored()},
		"items are not replaced":   {&pb_common.TechCardInsert{}, picksStored()},
		"nothing stored":           {stale, &entity.TechCard{TechCardInsert: entity.TechCardInsert{PackagingItems: []entity.TechCardPackagingItem{{Key: "a"}}}}},
		"sheet is not replaced":    {&pb_common.TechCardInsert{}, sheet},
		"no stored card":           {stale, nil},
	} {
		require.NoError(t, packagingPicksStoredGate(c.pb, c.stored), name)
	}
}

// The name and thumbnail are a read of the pick, and the colourways are a set: neither is a change. A
// different pick is.
func TestTechCardChangedSectionsPackagingPicks(t *testing.T) {
	before := snapshotDiffCard()
	before.LabelsAware = true
	before.PackagingItems = []*pb_common.TechCardPackagingItem{{Key: "polybag", MaterialId: 7, ColorwayIds: []int32{3, 9}}}

	after := proto.Clone(before).(*pb_common.TechCardInsert)
	after.PackagingItems[0].PickedName = "Polybag"
	after.PackagingItems[0].PickedThumbnail = &pb_common.MediaFull{Id: 4}
	after.PackagingItems[0].ColorwayIds = []int32{9, 3}
	require.Empty(t, techCardChangedSections(before, after))

	for name, edit := range map[string]func(*pb_common.TechCardPackagingItem){
		"material":  func(it *pb_common.TechCardPackagingItem) { it.MaterialId = 8 },
		"aux":       func(it *pb_common.TechCardPackagingItem) { it.MaterialId, it.AuxTechCardId = 0, 8 },
		"per order": func(it *pb_common.TechCardPackagingItem) { it.PerOrder = true },
		"colourway": func(it *pb_common.TechCardPackagingItem) { it.ColorwayIds = []int32{3} },
	} {
		changed := proto.Clone(before).(*pb_common.TechCardInsert)
		edit(changed.PackagingItems[0])
		require.NotEmpty(t, techCardChangedSections(before, changed), name)
	}
}

// One dead reference must not cost the whole restore: only that reference goes.
func TestPruneDeadPackagingReferences(t *testing.T) {
	in := &pb_common.TechCardInsert{PackagingItems: []*pb_common.TechCardPackagingItem{
		{Key: "live", MaterialId: 1, ColorwayIds: []int32{10, 20}},
		{Key: "dead material", MaterialId: 2, ColorwayIds: []int32{10, 30}},
		{Key: "live aux", AuxTechCardId: 3},
		{Key: "dead aux", AuxTechCardId: 4, ColorwayIds: []int32{30}},
		nil,
		{Key: "words"},
	}}
	pruneDeadPackagingReferences(t.Context(), 5, in, entity.PackagingPickTargets{
		Materials: map[int]bool{1: true},
		AuxCards:  map[int]bool{3: true},
		Colorways: map[int]bool{10: true, 20: true},
	})
	items := in.GetPackagingItems()
	require.EqualValues(t, 1, items[0].GetMaterialId())
	require.Equal(t, []int32{10, 20}, items[0].GetColorwayIds())
	require.Zero(t, items[1].GetMaterialId())
	require.Equal(t, []int32{10}, items[1].GetColorwayIds())
	require.EqualValues(t, 3, items[2].GetAuxTechCardId())
	require.Zero(t, items[3].GetAuxTechCardId())
	require.Empty(t, items[3].GetColorwayIds(), "the last colourway gone reads as all of them, and is logged")
	require.Equal(t, "words", items[5].GetKey())
}

// A snapshot with nothing to check must not touch the store: the strict mock fails on an unexpected call.
func TestDropDeadPackagingReferencesAsksOnlyWhenThereIsSomethingToAsk(t *testing.T) {
	rig := newRestoreRig(t)
	require.NoError(t, rig.srv.dropDeadPackagingReferences(t.Context(), restoreCardID,
		&pb_common.TechCardInsert{PackagingItems: []*pb_common.TechCardPackagingItem{{Key: "words"}}}))

	rig.cards.EXPECT().PackagingPickTargets(mock.Anything, restoreCardID, []int{1}, []int(nil)).
		Return(entity.PackagingPickTargets{}, nil)
	in := &pb_common.TechCardInsert{PackagingItems: []*pb_common.TechCardPackagingItem{{Key: "a", MaterialId: 1}}}
	require.NoError(t, rig.srv.dropDeadPackagingReferences(t.Context(), restoreCardID, in))
	require.Zero(t, in.GetPackagingItems()[0].GetMaterialId())
}

func TestResolveImportPackagingPickMaterial(t *testing.T) {
	passports := []techcardarchive.MaterialPassport{
		{Ref: 8400, Code: "PK-BAG", Name: "polybag", Unit: "pcs"},
		{Ref: 8401, Code: "F-WOOL", Name: "wool", Unit: "pcs"},
		{Ref: 8402, Code: "PK-GONE", Name: "gone", Unit: "pcs"},
	}
	catalog := func() []entity.MaterialWithPrice {
		bag := tcimpCatalogRow(2001, "PK-BAG", "pcs")
		bag.MaterialClass = string(entity.MaterialClassPackaging)
		wool := tcimpCatalogRow(2002, "F-WOOL", "pcs")
		wool.MaterialClass = string(entity.MaterialClassFabric)
		return []entity.MaterialWithPrice{bag, wool}
	}
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.PackagingItems = []*pb_common.TechCardPackagingItem{
		{Key: "matched", MaterialId: 8400, PerOrder: true, PickedName: "source name", PickedThumbnail: &pb_common.MediaFull{Id: 9}},
		{Key: "wrong class", MaterialId: 8401},
		{Key: "no article here", MaterialId: 8402},
		{Key: "no passport", MaterialId: 8499},
		{Key: "both", MaterialId: 8400, AuxTechCardId: 77},
		{Key: "words"},
	}
	a.with(techcardarchive.FileMaterialsIndex, tcimpJSON(t, passports))
	cards.EXPECT().ListMaterials(mock.Anything, "", true).Return(catalog(), nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err, "a pick that does not resolve drops alone, the import goes on")
	require.True(t, res.Insert.GetPackagingPicksAware())

	items := res.Insert.GetPackagingItems()
	require.Len(t, items, 6, "every item arrives, with its pick or without")
	require.EqualValues(t, 2001, items[0].GetMaterialId(), "the source's id must not survive")
	require.True(t, items[0].GetPerOrder())
	require.Empty(t, items[0].GetPickedName(), "a read of the source base")
	require.Nil(t, items[0].GetPickedThumbnail())
	for _, i := range []int{1, 2, 3} {
		require.Zero(t, items[i].GetMaterialId(), items[i].GetKey())
	}
	require.EqualValues(t, 2001, items[4].GetMaterialId())
	require.Zero(t, items[4].GetAuxTechCardId(), "both picks in a hand-made archive: the material stays")

	wrongOrAbsent := tcimpHoles(res, techcardarchive.ReasonMaterialNotFound)
	refs := make([]string, 0, len(wrongOrAbsent))
	for _, h := range wrongOrAbsent {
		require.Equal(t, techcardarchive.EntityMaterial, h.Entity)
		refs = append(refs, h.Ref)
	}
	require.ElementsMatch(t, []string{"packaging_items[1]", "packaging_items[2]", "packaging_items[3]"}, refs)
}

func TestResolveImportPackagingPickAuxCard(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.PackagingItems = []*pb_common.TechCardPackagingItem{
		{Key: "matched", AuxTechCardId: 900},
		{Key: "not here", AuxTechCardId: 901},
		{Key: "no number in the manifest", AuxTechCardId: 902},
	}
	a.manifest.IDMaps.AuxTechCards = map[string]string{"900": "GRB-AUX-0012", "901": "GRB-AUX-NOPE"}
	cards.EXPECT().ListTechCards(mock.Anything, 100, 0, entity.Ascending, mock.Anything).
		Return([]entity.TechCard{tcimpAuxCard(55, "GRB-AUX-0012")}, 1, nil)

	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	items := res.Insert.GetPackagingItems()
	require.EqualValues(t, 55, items[0].GetAuxTechCardId())
	require.Zero(t, items[1].GetAuxTechCardId())
	require.Zero(t, items[2].GetAuxTechCardId())

	holes := tcimpHoles(res, techcardarchive.ReasonAssemblyComponentNotFound)
	refs := make([]string, 0, len(holes))
	for _, h := range holes {
		require.Equal(t, techcardarchive.EntityAssembly, h.Entity)
		require.Equal(t, techcardarchive.StatusDegraded, h.Status)
		refs = append(refs, h.Ref)
	}
	require.ElementsMatch(t, []string{"packaging_items[1]", "packaging_items[2]"}, refs)
}

// Colourways are products and do not travel: the list empties, which reads «all colourways», so the
// report has to say so. The import itself never fails over it.
func TestResolveImportPackagingColourwaysDropWithAReportLine(t *testing.T) {
	s, _, _, _ := tcimpServer(t)
	a := tcimpNewArchive()
	a.insert.PackagingItems = []*pb_common.TechCardPackagingItem{
		{Key: "polybag", ColorwayIds: []int32{3, 9}},
		{Key: "tissue"},
	}
	res, err := s.resolveTechCardImport(t.Context(), a.open(t))
	require.NoError(t, err)
	require.Empty(t, res.Insert.GetPackagingItems()[0].GetColorwayIds())

	holes := tcimpHoles(res, techcardarchive.ReasonColorwaysNotApplied)
	require.Len(t, holes, 1)
	require.Equal(t, techcardarchive.EntityColorway, holes[0].Entity)
	require.Equal(t, "packaging_items[0]", holes[0].Ref)
	require.Contains(t, holes[0].Detail, "polybag")
}

func TestSanitizeCardForArchiveClearsThePickRead(t *testing.T) {
	pb := &pb_common.TechCard{TechCard: &pb_common.TechCardInsert{
		PackagingItems: []*pb_common.TechCardPackagingItem{{
			Key: "polybag", MaterialId: 7, ColorwayIds: []int32{3},
			PickedName: "Polybag", PickedThumbnail: &pb_common.MediaFull{Id: 4},
		}},
	}}
	sanitizeCardForArchive(pb)
	it := pb.GetTechCard().GetPackagingItems()[0]
	require.Empty(t, it.GetPickedName())
	require.Nil(t, it.GetPickedThumbnail())
	require.EqualValues(t, 7, it.GetMaterialId(), "the id travels: the far side matches it")
	require.Equal(t, []int32{3}, it.GetColorwayIds())
}

func TestCollectArchiveAuxPicksNamesTheCardsByNumber(t *testing.T) {
	s, _, cards, _ := tcimpServer(t)
	aux := tcimpAuxCard(900, "GRB-AUX-0012")
	cards.EXPECT().GetTechCardById(mock.Anything, 900).Return(&aux, nil).Once()
	cards.EXPECT().GetTechCardById(mock.Anything, 901).Return(nil, sql.ErrNoRows).Once()

	card := &entity.TechCard{}
	card.PackagingItems = []entity.TechCardPackagingItem{
		{Key: "a", AuxTechCardId: sql.NullInt32{Int32: 900, Valid: true}},
		{Key: "b", AuxTechCardId: sql.NullInt32{Int32: 900, Valid: true}},
		{Key: "c", AuxTechCardId: sql.NullInt32{Int32: 901, Valid: true}},
		{Key: "words"},
	}
	got, holes, err := s.collectArchiveAuxPicks(t.Context(), card)
	require.NoError(t, err)
	require.Equal(t, map[string]string{"900": "GRB-AUX-0012"}, got)
	require.Len(t, holes, 1)
	require.Equal(t, "packaging_items[2]", holes[0].Ref)
	require.Equal(t, techcardarchive.ReasonAssemblyComponentNotFound, holes[0].Reason)
}

func TestArchiveMediaSlotsCarryThePackedPhoto(t *testing.T) {
	card := &entity.TechCard{}
	card.Packaging = &entity.TechCardPackaging{PackedMediaId: sql.NullInt32{Int32: 42, Valid: true}}
	var ids []int
	for _, slot := range archiveCardMediaSlots(card) {
		ids = append(ids, slot.id)
	}
	require.Contains(t, ids, 42)
}
