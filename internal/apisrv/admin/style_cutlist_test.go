package admin

import (
	"context"
	"database/sql"
	"testing"

	mocks "github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetStyleCutList is the acceptance test for the mirror consumer (Q6): a mirrored piece expands to
// twice its per-garment count in the cut-list, a non-mirrored piece does not, and each piece resolves
// its fabric (and fusing) BOM line name per colourway from the real bom_item_id.
func TestGetStyleCutList(t *testing.T) {
	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)

	nid := func(v int64) sql.NullInt64 { return sql.NullInt64{Int64: v, Valid: true} }
	card := &entity.TechCard{Id: 7}
	card.StyleNumber = sql.NullString{String: "S-1", Valid: true}
	card.Name = "The Coat"
	card.BomItems = []entity.TechCardBomItem{
		{Id: 100, Name: "Shell Wool"},
		{Id: 200, Name: "Canvas Fusing"},
	}
	card.Pieces = []entity.TechCardPiece{
		{Id: 1, Name: "Front", PiecesPerGarment: 1, Mirrored: true, Grainline: "lengthwise",
			Materials: []entity.TechCardPieceMaterial{{ColorwayID: 9, BomItemId: nid(100), FusingBomItemId: nid(200)}}},
		{Id: 2, Name: "Back", PiecesPerGarment: 1, Mirrored: false, Grainline: "lengthwise",
			Materials: []entity.TechCardPieceMaterial{{ColorwayID: 9, BomItemId: nid(100)}}},
		{Id: 3, Name: "Pocket", PiecesPerGarment: 2, Mirrored: true, Grainline: "crosswise"},
	}
	tc.EXPECT().GetTechCardById(mock.Anything, 7).Return(card, nil)

	s := &Server{repo: repo}
	resp, err := s.GetStyleCutList(context.Background(), &pb_admin.GetStyleCutListRequest{TechCardId: 7})
	require.NoError(t, err)
	require.Equal(t, int32(7), resp.TechCardId)
	require.Equal(t, "S-1", resp.StyleNumber)
	require.Len(t, resp.Pieces, 3)

	front := resp.Pieces[0]
	require.True(t, front.Mirrored)
	require.Equal(t, int32(2), front.TotalPerGarment, "mirrored piece cut ×2 (a left+right pair)")
	require.Len(t, front.Fabrics, 1)
	require.Equal(t, int64(100), front.Fabrics[0].BomItemId)
	require.Equal(t, "Shell Wool", front.Fabrics[0].FabricName)
	require.Equal(t, int64(200), front.Fabrics[0].FusingBomItemId)
	require.Equal(t, "Canvas Fusing", front.Fabrics[0].FusingName)

	require.False(t, resp.Pieces[1].Mirrored)
	require.Equal(t, int32(1), resp.Pieces[1].TotalPerGarment, "non-mirrored piece is not doubled")

	require.Equal(t, int32(4), resp.Pieces[2].TotalPerGarment, "pieces_per_garment 2 × mirrored = 4")
	require.Empty(t, resp.Pieces[2].Fabrics)
}

// TestGetStyleCutListValidation covers the bad-request and not-found paths.
func TestGetStyleCutListValidation(t *testing.T) {
	s := &Server{repo: mocks.NewMockRepository(t)}
	_, err := s.GetStyleCutList(context.Background(), &pb_admin.GetStyleCutListRequest{TechCardId: 0})
	require.Equal(t, codes.InvalidArgument, status.Code(err))

	repo := mocks.NewMockRepository(t)
	tc := mocks.NewMockTechCards(t)
	repo.EXPECT().TechCards().Return(tc)
	tc.EXPECT().GetTechCardById(mock.Anything, 5).Return(nil, sql.ErrNoRows)
	s2 := &Server{repo: repo}
	_, err = s2.GetStyleCutList(context.Background(), &pb_admin.GetStyleCutListRequest{TechCardId: 5})
	require.Equal(t, codes.NotFound, status.Code(err))
}
