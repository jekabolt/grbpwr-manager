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

// GetStyleCutList is the first real consumer of the piece.mirrored flag (Q6). It projects a style's
// cut-pieces into a production cut-list: each piece's quantity expanded for a mirrored pair
// (pieces_per_garment × 2 when mirrored) and, per colourway, the fabric (and optional fusing) BOM line
// it is cut from. A read-only projection over GetTechCardById — no marker/CAD export, no mutable
// table (§2.5 / Q6). Gated read (SectionProducts).
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
		if p.Mirrored {
			total *= 2 // Q6: mirrored = cut as a left+right pair
		}
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
