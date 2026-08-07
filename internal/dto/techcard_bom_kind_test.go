package dto

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

// bomKindLine builds one wire BOM line; nil pointers mean the field was NOT SENT, which is the whole
// subject of these tests and is not the same as sending an empty value.
func bomKindLine(kind *pb_common.TechCardBomKind, note *string) *pb_common.TechCardBomItem {
	return &pb_common.TechCardBomItem{
		LineKey:  "01DGSTBOMKIND00000000ZIP1",
		Section:  pb_common.TechCardBomSection_TECH_CARD_BOM_SECTION_HARDWARE,
		Name:     "молния",
		Kind:     kind,
		KindNote: note,
	}
}

// A tab holding a bundle that predates 0278 sends NEITHER field. That must mean «не трогай», never
// «очисти» — the erasure would otherwise be total (every line of the card) and invisible (the fields
// are outside the signed MATERIALS digest, and NULL is indistinguishable from "not classified yet").
func TestBomKindAbsentMeansLeaveStoredValueAlone(t *testing.T) {
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(nil, nil)})
	require.NoError(t, err)
	require.True(t, got[0].KindOmitted)
	require.True(t, got[0].KindNoteOmitted)
	require.False(t, got[0].Kind.Valid)
	require.False(t, got[0].KindNote.Valid)
}

// An explicitly sent UNSET is a deliberate act and DOES clear the column — the same distinction
// `purpose` draws. Presence, not value, is what "absent" means.
func TestBomKindExplicitUnsetClears(t *testing.T) {
	unset := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(&unset, nil)})
	require.NoError(t, err)
	require.False(t, got[0].KindOmitted, "an explicitly sent UNSET must be written, not preserved")
	require.False(t, got[0].Kind.Valid)
}

func TestBomKindPresentIsStoredAsItsEntityString(t *testing.T) {
	zipper := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(&zipper, nil)})
	require.NoError(t, err)
	require.False(t, got[0].KindOmitted)
	require.Equal(t, string(entity.BomKindZipper), got[0].Kind.String)
}

// THE COUPLING, and why one presence decision governs both columns. chk_bom_item_kind_note makes a
// note legal ONLY beside kind='other', so a save that rewrote one column and preserved the other
// could hand MySQL a row it must refuse — a raw 3819 on the card-save path, naming a column the
// operator did not touch. Sending EITHER half therefore writes BOTH.
func TestBomKindAndNoteShareOnePresenceDecision(t *testing.T) {
	other := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_OTHER
	note := "неведомая хреновина"

	// Kind alone: the note is written too — as NULL, which is what clears a stale note left over
	// from a previous `other`.
	got, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(&other, nil)})
	require.NoError(t, err)
	require.False(t, got[0].KindOmitted)
	require.False(t, got[0].KindNoteOmitted)
	require.False(t, got[0].KindNote.Valid)

	// Both halves together: the normal write.
	got, err = parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(&other, &note)})
	require.NoError(t, err)
	require.Equal(t, note, got[0].KindNote.String)
	require.Equal(t, string(entity.BomKindOther), got[0].Kind.String)

	// The note alone, with no kind, is not silently accepted against whatever is stored: kind
	// resolves to NULL and the note-only-on-other rule refuses it with a field-tagged error the
	// operator can act on, instead of MySQL refusing it with a 3819 nobody can read.
	_, err = parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(nil, &note)})
	require.Error(t, err)
}

// The note is the escape hatch of `other` and of nothing else — accepting it beside a real kind
// would let free text in through the back door and dissolve the closed list the field exists to keep.
func TestBomKindNoteIsRefusedOnARealKind(t *testing.T) {
	zipper := pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_ZIPPER
	note := "с двумя бегунками"
	_, err := parseTechCardBomItems([]*pb_common.TechCardBomItem{bomKindLine(&zipper, &note)})
	require.Error(t, err)
}

// The read path always emits PRESENCE, so a client never has to guess whether it is talking to a
// server that predates the field; "not classified yet" travels as UNSET, not as an absent field.
// A stored value this build does not recognise degrades to UNSET rather than being reported as some
// other kind — a read must not lose the row, even when it cannot name the value.
func TestBomKindReadAlwaysCarriesPresenceAndDegradesUnknowns(t *testing.T) {
	out := techCardBomItemsToPb([]entity.TechCardBomItem{
		{Kind: sql.NullString{String: string(entity.BomKindButton), Valid: true}},
		{},
		{Kind: sql.NullString{String: "grommet_v2", Valid: true}},
	})
	require.NotNil(t, out[0].Kind)
	require.Equal(t, pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_BUTTON, out[0].GetKind())
	require.NotNil(t, out[1].Kind)
	require.Equal(t, pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET, out[1].GetKind())
	require.NotNil(t, out[2].Kind)
	require.Equal(t, pb_common.TechCardBomKind_TECH_CARD_BOM_KIND_UNSET, out[2].GetKind())
}

// THE SIGN-OFF CLAIM, asserted rather than asserted-in-a-comment: classifying a BOM line moves NO
// section digest. `kind` is deliberately outside materialsProjection on the same grounds as purpose
// and price_source — it is metadata about a line that already exists and does not change what the
// card BUYS. Folding it in would declare every approved MATERIALS sign-off stale the first time an
// operator sorted an approved card, which is a wall of "changed since sign-off" that means nothing.
//
// If this test ever has to change, the field has stopped being a grouping and become an input to a
// derivation — and then it must be folded in as a POSITIONAL TAIL, appended only when filled.
func TestBomKindIsOutsideEverySectionDigest(t *testing.T) {
	base := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{
			{LineKey: "01DGSTBOMKIND00000000ZIP1", Section: entity.BomSectionHardware, Name: "молния"},
			{LineKey: "01DGSTBOMKIND00000000THR1", Section: entity.BomSectionThread, Name: "нитки"},
		},
	}
	classified := &entity.TechCardInsert{
		BomItems: []entity.TechCardBomItem{
			{LineKey: "01DGSTBOMKIND00000000ZIP1", Section: entity.BomSectionHardware, Name: "молния",
				Kind: sql.NullString{String: string(entity.BomKindZipper), Valid: true}},
			{LineKey: "01DGSTBOMKIND00000000THR1", Section: entity.BomSectionThread, Name: "нитки",
				Kind:     sql.NullString{String: string(entity.BomKindOther), Valid: true},
				KindNote: sql.NullString{String: "спец-нить поставщика", Valid: true}},
		},
	}
	require.Equal(t, TechCardSectionDigests(base), TechCardSectionDigests(classified),
		"classifying a BOM line must not stale any sign-off — least of all MATERIALS")
}
