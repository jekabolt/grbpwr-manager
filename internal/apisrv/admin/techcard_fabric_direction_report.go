package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListTechCardFabricDirectionGaps serves the кампания Д1 worklist (Ф1.8): every roll-goods BOM line
// still missing its направление ткани, grouped by card, with the counts an owner triages by and the
// one number that says whether the campaign is finished.
//
// The layering follows the readiness read: the store COUNTS and the interpretation — which cards
// belong on a worklist, in what order — lives in one pure function (entity.BuildFabricDirectionGapReport)
// a test can argue with. Nothing here decides anything the blocker also decides.
func (s *Server) ListTechCardFabricDirectionGaps(ctx context.Context, req *pb_admin.ListTechCardFabricDirectionGapsRequest) (*pb_admin.ListTechCardFabricDirectionGapsResponse, error) {
	if req.GetTechCardId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id must not be negative")
	}
	cards, err := s.repo.TechCards().ListFabricDirectionGaps(ctx, int(req.GetTechCardId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list fabric direction gaps",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the направление ткани report")
	}
	return dto.FabricDirectionGapReportToPb(
		entity.BuildFabricDirectionGapReport(cards, req.GetIncludeInactive())), nil
}
