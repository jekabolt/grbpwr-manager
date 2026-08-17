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
//
// The read is UNBOUNDED by design: no LIMIT, no cursor. That is right for a worklist whose totals
// have to be exact and whose whole job is to reach zero, and it is safe at a portfolio measured in
// hundreds of cards — but it grows monotonically, so the caller that matters most (the release
// go/no-go) is given counts_only, which answers the same question in constant size. If the row-bearing
// form ever starts costing real money the fix is a cursor over cards, NOT a truncated total: a
// silently short answer to «is the campaign finished» is the failure this whole report exists to
// prevent.
func (s *Server) ListTechCardFabricDirectionGaps(ctx context.Context, req *pb_admin.ListTechCardFabricDirectionGapsRequest) (*pb_admin.ListTechCardFabricDirectionGapsResponse, error) {
	if req.GetTechCardId() < 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id must not be negative")
	}
	cards, err := s.repo.TechCards().ListFabricDirectionGaps(ctx, int(req.GetTechCardId()))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list fabric direction gaps",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the fabric direction report")
	}
	return dto.FabricDirectionGapReportToPb(
		entity.BuildFabricDirectionGapReport(cards, req.GetIncludeInactive()),
		req.GetCountsOnly()), nil
}
