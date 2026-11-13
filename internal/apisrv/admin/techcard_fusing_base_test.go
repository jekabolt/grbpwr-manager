package admin

import (
	"database/sql"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/techcardarchive"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/stretchr/testify/require"
)

func fusingBaseZoneEntityPiece(key string, sizeID int32, signature string) entity.TechCardPiece {
	p := entity.TechCardPiece{
		LineKey: key, Name: key, PiecesPerGarment: 1, Grainline: "lengthwise", Fused: true,
		FusingMode: sql.NullString{String: string(entity.PieceFusingModeZone), Valid: true},
		FusingZone: &entity.TechCardPieceFusingZone{DxfPieceKey: "FUSE-" + key},
	}
	if sizeID != 0 {
		p.FusingBaseSizeId = sql.NullInt32{Int32: sizeID, Valid: true}
		p.FusingBaseSignature = sql.NullString{String: signature, Valid: true}
	}
	return p
}

// The API carry gives an omitted key the stored one, lets an explicit key or clear win, and still
// drops a carried key when the same save unfuses the piece.
func TestCarryOmittedPieceFusingDetailsFromFusingBase(t *testing.T) {
	stored := &entity.TechCard{TechCardInsert: entity.TechCardInsert{Pieces: []entity.TechCardPiece{
		fusingBaseZoneEntityPiece("OMIT", 4, "stored"),
		fusingBaseZoneEntityPiece("NEW", 4, "stored"),
		fusingBaseZoneEntityPiece("CLEAR", 4, "stored"),
		fusingBaseZoneEntityPiece("UNFUSE", 4, "stored"),
	}}}
	omitted := fusingBaseZoneEntityPiece("OMIT", 0, "")
	omitted.FusingBaseOmitted = true
	replaced := fusingBaseZoneEntityPiece("NEW", 5, "fresh")
	cleared := fusingBaseZoneEntityPiece("CLEAR", 0, "")
	unfused := fusingBaseZoneEntityPiece("UNFUSE", 0, "")
	unfused.FusingBaseOmitted = true
	unfused.Fused = false
	unfused.FusingMode = sql.NullString{}
	unfused.FusingZone = nil
	incoming := &entity.TechCardInsert{Pieces: []entity.TechCardPiece{omitted, replaced, cleared, unfused}}

	carryOmittedPieceFusingDetailsFrom(stored, incoming)

	key := func(p entity.TechCardPiece) [2]any {
		if !p.FusingBaseSizeId.Valid {
			return [2]any{nil, nil}
		}
		return [2]any{p.FusingBaseSizeId.Int32, p.FusingBaseSignature.String}
	}
	require.Equal(t, [2]any{int32(4), "stored"}, key(incoming.Pieces[0]), "omitted keeps the stored key")
	require.Equal(t, [2]any{int32(5), "fresh"}, key(incoming.Pieces[1]), "an explicit key replaces it")
	require.Equal(t, [2]any{nil, nil}, key(incoming.Pieces[2]), "an explicit clear wins over the stored key")
	require.Equal(t, [2]any{nil, nil}, key(incoming.Pieces[3]), "unfusing clears even an omitted key")
}

// The revision journal's changed sections apply the same carry: a tab that predates the key is not
// a pieces change, a clear of a key that was never stored is not either, and a new key is.
func TestChangedSectionsFusingBase(t *testing.T) {
	piece := func(base *pb_common.TechCardPieceFusingBase) *pb_common.TechCardPiece {
		return &pb_common.TechCardPiece{LineKey: "BODY", Name: "Body", FusingBase: base}
	}
	card := func(p *pb_common.TechCardPiece) *pb_common.TechCardInsert {
		return &pb_common.TechCardInsert{Name: "c", Pieces: []*pb_common.TechCardPiece{p}}
	}
	stored := card(piece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "sig"}))

	require.Empty(t, techCardChangedSections(stored, card(piece(nil))), "an older tab omits the key")
	require.Empty(t, techCardChangedSections(card(piece(nil)), card(piece(&pb_common.TechCardPieceFusingBase{}))),
		"clearing a key that was never stored changes nothing")
	require.True(t, reflect.DeepEqual([]string{"pieces"}, techCardChangedSections(stored,
		card(piece(&pb_common.TechCardPieceFusingBase{BaseSizeId: 4, ContourSignature: "other"})))),
		"a new key is a pieces change")
}

// The archive remaps the key's base size by name. A size the target lacks would leave half a key,
// which the write refuses, so the whole key is cleared and the import goes on.
func TestResolveImportFusingBaseSize(t *testing.T) {
	insert := &pb_common.TechCardInsert{Pieces: []*pb_common.TechCardPiece{
		{LineKey: "KNOWN", FusingBase: &pb_common.TechCardPieceFusingBase{BaseSizeId: 3, ContourSignature: "sig"}},
		{LineKey: "LOST", FusingBase: &pb_common.TechCardPieceFusingBase{BaseSizeId: 9, ContourSignature: "sig"}},
		{LineKey: "NONE"},
	}}
	r := &tcimpResolver{
		out:                &resolvedTechCardImport{Insert: insert, Counters: techcardarchive.NewCounters()},
		sizeMapping:        map[int64]int64{3: 30},
		sizeNameBySourceID: map[int64]string{3: "s", 9: "xxl"},
		sizeMissed:         map[int64]bool{},
		holeSeen:           map[string]bool{},
	}

	r.resolveSizes()

	pieces := insert.GetPieces()
	require.EqualValues(t, 30, pieces[0].GetFusingBase().GetBaseSizeId())
	require.Equal(t, "sig", pieces[0].GetFusingBase().GetContourSignature())
	require.NotNil(t, pieces[1].GetFusingBase(), "a lost size is an explicit clear, not an omission")
	require.Zero(t, pieces[1].GetFusingBase().GetBaseSizeId())
	require.Empty(t, pieces[1].GetFusingBase().GetContourSignature())
	require.Nil(t, pieces[2].GetFusingBase())
	require.Len(t, tcimpHoles(r.out, techcardarchive.ReasonSizeUnknown), 1)
}
