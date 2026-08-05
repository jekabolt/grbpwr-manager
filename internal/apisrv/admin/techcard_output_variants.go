package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	authsrv "github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Colour variants of an AUXILIARY card's warehouse output (migration 0252). Reads travel on
// GetTechCard (TechCard.output_variants) and ListTechCards (the two summary fields on the list
// item), so only the two writes need RPCs of their own.

// UpsertTechCardOutputVariant creates or updates ONE colour of an auxiliary card's output — the
// warehouse bucket the card produces into in that colour. Single-row on purpose: a variant owns
// stock and becomes the FK target of a run line, so the assembly bill's full-replace shape would
// re-mint identities that history depends on.
func (s *Server) UpsertTechCardOutputVariant(ctx context.Context, req *pb_admin.UpsertTechCardOutputVariantRequest) (*pb_admin.UpsertTechCardOutputVariantResponse, error) {
	if req.GetTechCardId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "tech_card_id is required")
	}
	ins, err := dto.ConvertPbTechCardOutputVariantToEntity(req.GetVariant())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid colour variant: %v", err)
	}
	id, err := s.repo.TechCards().UpsertOutputVariant(ctx, int(req.GetTechCardId()), ins,
		authsrv.GetAdminUsername(ctx))
	if err != nil {
		return nil, s.techCardOutputVariantError(ctx, "upsert", int(req.GetTechCardId()), err)
	}
	return &pb_admin.UpsertTechCardOutputVariantResponse{Id: int32(id)}, nil
}

// DeleteTechCardOutputVariant removes a colour variant outright. Deactivating it is the normal
// retirement; deleting is how a card stops being auxiliary at all, since any variant row pins the
// purpose. The warehouse bucket survives with its stock and moving average — only the card's claim
// on it goes, so the material stays archivable by hand in the catalog.
func (s *Server) DeleteTechCardOutputVariant(ctx context.Context, req *pb_admin.DeleteTechCardOutputVariantRequest) (*pb_admin.DeleteTechCardOutputVariantResponse, error) {
	if req.GetId() <= 0 {
		return nil, status.Error(codes.InvalidArgument, "id is required")
	}
	if err := s.repo.TechCards().DeleteOutputVariant(ctx, int(req.GetId())); err != nil {
		return nil, s.techCardOutputVariantError(ctx, "delete", 0, err)
	}
	return &pb_admin.DeleteTechCardOutputVariantResponse{}, nil
}

// techCardOutputVariantError maps the store's typed refusals to gRPC statuses, following the
// tech-card update path: a malformed payload is InvalidArgument, a well-formed request the current
// state blocks is FailedPrecondition, and only an unrecognised error is Internal. Every
// FailedPrecondition returns err.Error() rather than the bare sentinel, because the store appends
// the fact that actually blocks (which card holds the bucket, which unit the card is measured in) —
// that detail is the only actionable half of the message.
func (s *Server) techCardOutputVariantError(ctx context.Context, op string, techCardID int, err error) error {
	var ve *entity.ValidationError
	switch {
	case errors.As(err, &ve):
		return apierr.Invalid(ve)
	case errors.Is(err, entity.ErrOutputVariantNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, sql.ErrNoRows):
		return status.Error(codes.NotFound, "tech card not found")
	case errors.Is(err, entity.ErrTechCardNotAuxiliary),
		errors.Is(err, entity.ErrOutputVariantMaterialClaimed),
		errors.Is(err, entity.ErrOutputVariantReferencedByRun),
		errors.Is(err, entity.ErrOutputVariantUnitMismatch):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, entity.ErrTechCardReleased):
		return status.Error(codes.FailedPrecondition, entity.ErrTechCardReleased.Error())
	case s.repo.IsErrUniqueViolation(err):
		// The colour and bucket guards above are checked inside the same transaction, so this is the
		// racing-writer backstop (uniq_tcov_card_color / uniq_tcov_material), not a validation gap.
		return status.Error(codes.FailedPrecondition,
			"this colour, or this material, is already registered on a card; reload and retry")
	case s.repo.IsErrForeignKeyViolation(err):
		return status.Error(codes.InvalidArgument, "colour variant references a missing tech card, colour or material")
	}
	slog.Default().ErrorContext(ctx, "tech card colour variant write failed",
		slog.String("op", op), slog.Int("tech_card_id", techCardID), slog.String("err", err.Error()))
	return status.Error(codes.Internal, "can't save the colour variant; try again")
}
