package admin

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// fabric_direction became optional so a tab holding an older bundle cannot erase it. That fixed the
// write and broke the DIGEST: the field sits in materialsProjection, whose invariant is that it
// hashes only what survives the store round-trip unchanged, and an omitted field arrives as an empty
// NullString while the column keeps its value. A MATERIALS approval made from exactly the client the
// optionality exists for would then read «changed since sign-off» the instant it was made — and
// forever, because re-approving from the same client hashes the same absence.
//
// purpose/purpose_note/is_sample dodged this by staying out of the projection. Direction cannot: it
// is a cutting fact the approval is about, and since Ф1 it decides whether a раскладка can be saved
// at all.
func TestCarryOmittedFabricDirectionKeepsTheMaterialsDigestStable(t *testing.T) {
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: v != ""} }
	const (
		shell  = "01DGSTLINESHELL000000000A1"
		lining = "01DGSTLINELINING00000000L1"
	)
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{
			{LineKey: shell, Section: entity.BomSectionFabric, Name: "Вельвет", FabricDirection: ns("one_way")},
			{LineKey: lining, Section: entity.BomSectionLining, Name: "Купра", FabricDirection: ns("any")},
		},
	}}
	// What the STORED card fingerprints to — the value a fresh approval must match.
	want := dto.TechCardSectionDigests(&stored.TechCardInsert)[entity.SignoffMaterials]

	// A stale tab: same content, but it does not speak the field at all.
	staleTab := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{
		{LineKey: shell, Section: entity.BomSectionFabric, Name: "Вельвет", FabricDirectionOmitted: true},
		{LineKey: lining, Section: entity.BomSectionLining, Name: "Купра", FabricDirectionOmitted: true},
	}}
	require.NotEqual(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffMaterials],
		"guard: without the carry the digests must differ, or this test proves nothing")

	carryOmittedFabricDirectionFrom(stored, staleTab)
	require.Equal(t, want, dto.TechCardSectionDigests(staleTab)[entity.SignoffMaterials],
		"an approval from a tab that cannot speak the field must not be born stale")

	// The carry is not a blanket copy: a tab that DID speak still owns the value, including when it
	// deliberately clears one. Otherwise the field would become unclearable.
	current := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{
		{LineKey: shell, Section: entity.BomSectionFabric, Name: "Вельвет", FabricDirection: ns("two_way")},
		{LineKey: lining, Section: entity.BomSectionLining, Name: "Купра"},
	}}
	carryOmittedFabricDirectionFrom(stored, current)
	require.Equal(t, "two_way", current.BomItems[0].FabricDirection.String)
	require.False(t, current.BomItems[1].FabricDirection.Valid, "an explicit clear survives the carry")

	// A line the stored card does not have (a new row) is left alone rather than matched by position.
	fresh := &entity.TechCardInsert{BomItems: []entity.TechCardBomItem{
		{LineKey: "01DGSTLINENEW00000000000N1", Section: entity.BomSectionFabric, Name: "Новая", FabricDirectionOmitted: true},
	}}
	carryOmittedFabricDirectionFrom(stored, fresh)
	require.False(t, fresh.BomItems[0].FabricDirection.Valid, "a new line has nothing stored to carry")
}
