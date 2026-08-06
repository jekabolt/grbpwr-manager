package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetStyleCutList projects a style's cut-pieces into a production cut-list: how many of each piece a
// garment takes and, per colourway, the fabric (and optional fusing) BOM line it is cut from. A
// read-only projection over GetTechCardById — no marker/CAD export, no mutable table (§2.5 / Q6).
// Gated read (SectionProducts).
//
// THE MIRROR FOLD IS GONE. total_per_garment used to be pieces_per_garment × 2 whenever
// piece.mirrored was set (Q6). The flag was never used in practice, and it double-counted against
// the way patterns actually arrive: a graded DXF draws a left/right pair as TWO blocks, so the
// matching dialog already creates two pieces (or one piece with the instance count baked in), and a
// mirror flag on top of that produced four. The admin stopped offering the checkbox and stopped
// sending the field in the same change; total_per_garment is now simply pieces_per_garment.
//
// The stored column is NOT read here any more but is still surfaced on the response (Mirrored below)
// and still written by the save path from whatever the payload carries — which, from this admin, is
// now always false. Existing `mirrored = 1` rows therefore stop affecting any number immediately,
// and are cleared card-by-card as cards are saved.
func (s *Server) GetStyleCutList(ctx context.Context, req *pb_admin.GetStyleCutListRequest) (*pb_admin.GetStyleCutListResponse, error) {
	tcID := int(req.GetTechCardId())
	if tcID <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	card, err := s.repo.TechCards().GetTechCardById(ctx, tcID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "cut-list: can't load tech card", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't load tech card")
	}

	// The fabric names come from the style's BOM lines, addressed by the real bom_item_id the piece
	// material carries (S2/S3) — so a reordered/edited BOM never mis-labels the cut-list.
	bomNameByID := make(map[int64]string, len(card.BomItems))
	for i := range card.BomItems {
		bomNameByID[int64(card.BomItems[i].Id)] = card.BomItems[i].Name
	}

	pieces := make([]*pb_admin.StyleCutListPiece, 0, len(card.Pieces))
	for i := range card.Pieces {
		p := &card.Pieces[i]
		perGarment := p.PiecesPerGarment
		total := perGarment
		fabrics := make([]*pb_admin.StyleCutListFabric, 0, len(p.Materials))
		for j := range p.Materials {
			m := &p.Materials[j]
			f := &pb_admin.StyleCutListFabric{ColorwayId: int64(m.ColorwayID)}
			if m.BomItemId.Valid {
				f.BomItemId = m.BomItemId.Int64
				f.FabricName = bomNameByID[m.BomItemId.Int64]
			}
			if m.FusingBomItemId.Valid {
				f.FusingBomItemId = m.FusingBomItemId.Int64
				f.FusingName = bomNameByID[m.FusingBomItemId.Int64]
			}
			fabrics = append(fabrics, f)
		}
		pieces = append(pieces, &pb_admin.StyleCutListPiece{
			PieceId:          int32(p.Id),
			Name:             p.Name,
			PiecesPerGarment: int32(perGarment),
			Mirrored:         p.Mirrored,
			TotalPerGarment:  int32(total),
			Grainline:        p.Grainline,
			Fused:            p.Fused,
			Fabrics:          fabrics,
		})
	}

	return &pb_admin.GetStyleCutListResponse{
		TechCardId:  int32(tcID),
		StyleNumber: card.StyleNumber.String, // "" when NULL (idea-stage draft)
		Name:        card.Name,
		Pieces:      pieces,
	}, nil
}
