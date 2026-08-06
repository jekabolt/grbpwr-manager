package admin

import (
	"context"
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

// «Дом настроек цеха» (Ф2.5, 0272). Four phases of the production-cutting plan each said a value
// "should come from a workshop setting" and none of them built a place for one; this is the place.
// Первый жилец is the cutting table length — a property of the ЦЕХ that the nesting modal has been
// making the operator retype on every раскладка. Ф3.2 (припуск), Ф4.8 (высота стопки) and
// 08-cut-out (минимальный зазор) move in as further typed fields on the same two RPCs.

// GetWorkshopSettings returns the shop-floor configuration. Never NotFound: a workshop that has
// configured nothing yet is a legitimate state and reads as an all-absent settings object.
func (s *Server) GetWorkshopSettings(ctx context.Context, req *pb_admin.GetWorkshopSettingsRequest) (*pb_admin.GetWorkshopSettingsResponse, error) {
	settings, err := s.repo.Workshop().GetSettings(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get workshop settings", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't read the workshop settings; try again")
	}
	return &pb_admin.GetWorkshopSettingsResponse{Settings: dto.WorkshopSettingsToPb(settings)}, nil
}

// UpdateWorkshopSettings patches the shop-floor configuration and returns the result.
//
// Partial by construction, on PRESENCE rather than value: a setting the request does not carry is
// left alone. That is not a nicety — the admin is an SPA whose tabs outlive deploys, so a bundle
// that predates a new setting sends nothing for it, and a "write whatever arrived" rule would have
// that bundle silently clear settings it has never heard of. Clearing is possible, but only by
// saying so explicitly (an empty decimal); see UpdateWorkshopSettingsRequest in the proto.
func (s *Server) UpdateWorkshopSettings(ctx context.Context, req *pb_admin.UpdateWorkshopSettingsRequest) (*pb_admin.UpdateWorkshopSettingsResponse, error) {
	patch, err := dto.WorkshopSettingsPatchFromPb(req)
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		return nil, status.Errorf(codes.InvalidArgument, "invalid workshop settings: %v", err)
	}
	settings, err := s.repo.Workshop().UpdateSettings(ctx, patch, authsrv.GetAdminUsername(ctx))
	if err != nil {
		var ve *entity.ValidationError
		if errors.As(err, &ve) {
			return nil, apierr.Invalid(ve)
		}
		slog.Default().ErrorContext(ctx, "can't update workshop settings", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't save the workshop settings; try again")
	}
	return &pb_admin.UpdateWorkshopSettingsResponse{Settings: dto.WorkshopSettingsToPb(settings)}, nil
}
