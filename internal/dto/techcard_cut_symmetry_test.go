package dto

import (
	"database/sql"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func pcs(v pb_common.TechCardPieceCutSymmetry) *pb_common.TechCardPieceCutSymmetry { return &v }

// ПРИСУТСТВИЕ, НЕ ЗНАЧЕНИЕ. `optional` exists so a tab holding an older bundle cannot erase a marking
// it cannot speak, and the whole mechanism rests on the parser telling ABSENT apart from EXPLICITLY
// UNKNOWN. Getting this wrong in either direction is silent: collapse them and every stale save wipes
// the card; separate them wrongly and the field becomes unclearable.
func TestParsePieceCutSymmetryPresence(t *testing.T) {
	t.Run("absent means omitted, not cleared", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "полочка", PiecesPerGarment: 2},
		}))
		require.NoError(t, err)
		require.True(t, got.Pieces[0].CutSymmetryOmitted, "a piece with no cut_symmetry field must be marked omitted")
		require.False(t, got.Pieces[0].CutSymmetry.Valid, "omitted must not invent a value")
	})

	t.Run("explicit UNKNOWN clears", func(t *testing.T) {
		got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "полочка", PiecesPerGarment: 2,
				CutSymmetry: pcs(pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN)},
		}))
		require.NoError(t, err)
		require.False(t, got.Pieces[0].CutSymmetryOmitted,
			"an explicitly sent UNKNOWN is a deliberate act and must NOT read as omitted, or the field can never be cleared")
		require.False(t, got.Pieces[0].CutSymmetry.Valid, "explicit UNKNOWN stores NULL — «не размечено»")
	})

	t.Run("each value round-trips", func(t *testing.T) {
		for pb, want := range map[pb_common.TechCardPieceCutSymmetry]entity.TechCardPieceCutSymmetry{
			pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_IDENTICAL: entity.PieceCutSymmetryIdentical,
			pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED:  entity.PieceCutSymmetryMirrored,
			pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_FOLD:      entity.PieceCutSymmetryFold,
		} {
			got, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
				{Name: "полочка", PiecesPerGarment: 2, CutSymmetry: pcs(pb)},
			}))
			require.NoError(t, err, pb.String())
			require.False(t, got.Pieces[0].CutSymmetryOmitted)
			require.Equal(t, string(want), got.Pieces[0].CutSymmetry.String)
		}
	})
}

// The unresolvable pair is refused in WORDS, in the parser, before MySQL refuses it with a number.
// chk_tcp_mirrored_needs_even_count spans two columns, so its 3819 names pieces_per_garment even when
// the operator only touched the dropdown.
func TestParsePieceCutSymmetryRejectsOddMirrored(t *testing.T) {
	for _, n := range []int32{1, 3, 5} {
		_, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "полочка", PiecesPerGarment: n,
				CutSymmetry: pcs(pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED)},
		}))
		require.Error(t, err, "mirrored with pieces_per_garment=%d must be refused", n)
		var ve *entity.ValidationError
		require.ErrorAs(t, err, &ve, "the refusal must be a field violation, not a bare 500")
		require.Contains(t, ve.Error(), "cut_symmetry")
	}

	// A ZERO count is the proto3 default and the parser turns it into 1 — which is still odd, so the
	// refusal must survive that coercion rather than be dodged by it.
	_, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
		{Name: "полочка",
			CutSymmetry: pcs(pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED)},
	}))
	require.Error(t, err, "an unset count defaults to 1, which is still not a pair")

	// fold and identical carry no evenness rule: a cuff cut on the fold is legitimately ×2, and a back
	// on the fold legitimately ×1.
	for _, sym := range []pb_common.TechCardPieceCutSymmetry{
		pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_FOLD,
		pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_IDENTICAL,
	} {
		for _, n := range []int32{1, 2, 3} {
			_, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
				{Name: "полочка", PiecesPerGarment: n, CutSymmetry: pcs(sym)},
			}))
			require.NoError(t, err, "%s ×%d is legal", sym, n)
		}
	}
}

// A read must always SPEAK the field, even when the column is NULL. The optionality exists so a
// client cannot erase what it cannot say — not so the server can fall silent. If the emit returned
// nil for an unmarked piece the field would vanish from the JSON of nearly every piece today, and a
// client faithfully round-tripping what it read would come back looking exactly like the stale tab
// this whole design is defending against.
func TestEmitPieceCutSymmetryIsAlwaysPresent(t *testing.T) {
	card := &entity.TechCard{TechCardInsert: entity.TechCardInsert{
		Pieces: []entity.TechCardPiece{
			{LineKey: "01UNMARKED000000000000000", Name: "спинка", PiecesPerGarment: 1},
			{LineKey: "01MARKED00000000000000000", Name: "полочка", PiecesPerGarment: 2,
				CutSymmetry: sql.NullString{String: string(entity.PieceCutSymmetryMirrored), Valid: true}},
		},
	}}
	out := ConvertEntityTechCardToPb(card, CostingFx{})
	require.Len(t, out.GetTechCard().GetPieces(), 2)

	require.NotNil(t, out.GetTechCard().GetPieces()[0].CutSymmetry, "an unmarked piece must still carry the field")
	require.Equal(t, pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_UNKNOWN,
		out.GetTechCard().GetPieces()[0].GetCutSymmetry())

	require.NotNil(t, out.GetTechCard().GetPieces()[1].CutSymmetry)
	require.Equal(t, pb_common.TechCardPieceCutSymmetry_TECH_CARD_PIECE_CUT_SYMMETRY_MIRRORED,
		out.GetTechCard().GetPieces()[1].GetCutSymmetry())
}

// The wire round trip must be a fixed point: read a card, send it straight back, and nothing about
// the marking may move. This is what makes «always emit» safe for the new client.
func TestPieceCutSymmetryWireRoundTripIsAFixedPoint(t *testing.T) {
	for _, stored := range []sql.NullString{
		{},
		{String: string(entity.PieceCutSymmetryIdentical), Valid: true},
		{String: string(entity.PieceCutSymmetryMirrored), Valid: true},
		{String: string(entity.PieceCutSymmetryFold), Valid: true},
	} {
		emitted := PieceCutSymmetryToPb(stored)
		back, err := ConvertPbTechCardInsertToEntity(baseTechCardWithPieces([]*pb_common.TechCardPiece{
			{Name: "полочка", PiecesPerGarment: 2, CutSymmetry: &emitted},
		}))
		require.NoError(t, err)
		require.Equal(t, stored.Valid, back.Pieces[0].CutSymmetry.Valid, "validity moved for %+v", stored)
		require.Equal(t, stored.String, back.Pieces[0].CutSymmetry.String, "value moved for %+v", stored)
		require.False(t, back.Pieces[0].CutSymmetryOmitted, "a client that speaks the field is never the stale tab")
	}
}
