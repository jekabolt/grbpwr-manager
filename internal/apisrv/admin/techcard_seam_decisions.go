package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// AddTechCardSeamDecisions appends what the technologist did with the seam guesses (accept / own /
// skip / refuse), ≤ 50 per call, stamped with the caller. A log beside the seams: it never writes
// tech_card_seam, never moves lock_version, and is accepted on a released card (a note about the
// review, not card content). Malformed input is refused field-addressed before the store.
func (s *Server) AddTechCardSeamDecisions(ctx context.Context, req *pb_admin.AddTechCardSeamDecisionsRequest) (*pb_admin.AddTechCardSeamDecisionsResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	in, err := dto.TechCardSeamDecisionsWriteFromPb(int(req.GetTechCardId()), req.GetDecisions(), authsrv.GetAdminUsername(ctx))
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}
	written, err := s.repo.TechCards().AddTechCardSeamDecisions(ctx, in)
	if err != nil {
		if st, ok := apierr.Status(err); ok {
			return nil, st
		}
		// The existence read finds no card, or (a delete racing the insert) the only FK fails.
		if errors.Is(err, sql.ErrNoRows) || s.repo.IsErrForeignKeyViolation(err) {
			return nil, status.Error(codes.NotFound, "tech card not found")
		}
		slog.Default().ErrorContext(ctx, "can't add tech card seam decisions",
			slog.Int("tech_card_id", int(req.GetTechCardId())), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't add tech card seam decisions")
	}
	return &pb_admin.AddTechCardSeamDecisionsResponse{Written: int32(written)}, nil
}
