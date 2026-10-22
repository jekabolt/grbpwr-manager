package admin

import (
	"context"
	"errors"

	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// DeleteTechCardPatternRevision removes one retained sheet revision. The store owns all decisions
// that must be atomic with the delete (ownership, lifecycle/version guards, snapshot retention,
// journal and reference accounting); object storage cleanup happens only after that transaction
// commits, then the card is read through the same response path as GetTechCard.
func (s *Server) DeleteTechCardPatternRevision(
	ctx context.Context,
	req *pb_admin.DeleteTechCardPatternRevisionRequest,
) (*pb_admin.DeleteTechCardPatternRevisionResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech card id is required")
	}
	if req.GetHistoryId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "pattern history id is required")
	}
	id := int(req.GetTechCardId())
	orphanedPatternURLs, err := s.repo.TechCards().DeleteTechCardPatternRevisionAndListOrphanedPatternURLs(
		ctx, id, req.GetHistoryId(), int(req.GetExpectedLockVersion()), authsrv.GetAdminUsername(ctx))
	if err != nil {
		if errors.Is(err, entity.ErrTechCardPatternRevisionCurrent) {
			return nil, status.Error(codes.FailedPrecondition, entity.ErrTechCardPatternRevisionCurrent.Error())
		}
		return nil, s.techCardWriteError(ctx, err)
	}
	s.deleteOrphanedPatternObjects(ctx, "tech_card_pattern_history", id, orphanedPatternURLs)
	card, err := s.readTechCardForResponse(ctx, id, "")
	if err != nil {
		return nil, err
	}
	return &pb_admin.DeleteTechCardPatternRevisionResponse{TechCard: card}, nil
}
