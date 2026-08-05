package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/apierr"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// CreateVariant adds a new variant (size) to a colourway at zero stock (R2). The size is immutable and
// the variant SKU is minted from the colourway's base. An unknown colourway is NOT_FOUND, an archived
// one is FAILED_PRECONDITION, and a duplicate size is ALREADY_EXISTS.
func (s *Server) CreateVariant(ctx context.Context, req *pb_admin.CreateVariantRequest) (*pb_admin.CreateVariantResponse, error) {
	if req.ColorwayId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "colorway_id is required")
	}
	if req.SizeId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "size_id is required")
	}
	v, err := s.repo.Products().CreateVariant(ctx, int(req.ColorwayId), int(req.SizeId))
	if err != nil {
		var ve *entity.ValidationError
		switch {
		case errors.As(err, &ve):
			// S10/WS5: size system not permitted for the owning style's category.
			return nil, apierr.Invalid(ve)
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "colourway %d not found", req.ColorwayId)
		case errors.Is(err, entity.ErrVariantExists):
			return nil, status.Errorf(codes.AlreadyExists, "colourway %d already has size %d", req.ColorwayId, req.SizeId)
		case errors.Is(err, entity.ErrColorwayArchived):
			return nil, status.Errorf(codes.FailedPrecondition, "colourway %d is archived", req.ColorwayId)
		default:
			slog.Default().ErrorContext(ctx, "can't create variant", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't create variant: %v", err)
		}
	}
	s.afterColorwayLifecycleChange(ctx, v.ProductId)
	return &pb_admin.CreateVariantResponse{Variant: dto.ConvertEntityVariantToPb(v)}, nil
}

// UpdateVariant patches a variant's mutable state (R2). Only the lifecycle status is writable — size_id
// and the variant SKU are immutable. An empty update_mask applies the patch as given.
func (s *Server) UpdateVariant(ctx context.Context, req *pb_admin.UpdateVariantRequest) (*pb_admin.UpdateVariantResponse, error) {
	if req.VariantId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "variant_id is required")
	}
	if req.Patch == nil {
		return nil, status.Error(codes.InvalidArgument, "patch is required")
	}
	target := entity.VariantStatus(req.Patch.GetStatus())
	if !target.Valid() {
		return nil, status.Error(codes.InvalidArgument, "patch.status must be ACTIVE or ARCHIVED")
	}
	v, err := s.setVariantStatus(ctx, int(req.VariantId), target)
	if err != nil {
		return nil, err
	}
	return &pb_admin.UpdateVariantResponse{Variant: v}, nil
}

// ArchiveVariant retires a variant (R2: archive-not-delete). Its id stays valid for the frozen
// order/stock references; it drops off the storefront and rejects stock writes.
func (s *Server) ArchiveVariant(ctx context.Context, req *pb_admin.ArchiveVariantRequest) (*pb_admin.ArchiveVariantResponse, error) {
	if req.VariantId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "variant_id is required")
	}
	if _, err := s.setVariantStatus(ctx, int(req.VariantId), entity.VariantStatusArchived); err != nil {
		return nil, err
	}
	return &pb_admin.ArchiveVariantResponse{}, nil
}

// setVariantStatus is the shared body of UpdateVariant/ArchiveVariant: it applies the status through
// the store's optimistic guard and maps store errors to gRPC. A refreshed storefront revalidation is
// triggered on success (a variant's availability changed).
func (s *Server) setVariantStatus(ctx context.Context, variantID int, target entity.VariantStatus) (*pb_common.Variant, error) {
	v, err := s.repo.Products().SetVariantStatus(ctx, variantID, target)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "variant %d not found", variantID)
		}
		slog.Default().ErrorContext(ctx, "can't set variant status",
			slog.Int("variant_id", variantID), slog.String("target", target.String()), slog.String("err", err.Error()))
		return nil, status.Errorf(codes.FailedPrecondition, "cannot set variant %d status: %v", variantID, err)
	}
	s.afterColorwayLifecycleChange(ctx, v.ProductId)
	return dto.ConvertEntityVariantToPb(v), nil
}

// ListVariantSeconds returns a colourway's B-grade (factory seconds) variants with their manual
// price lists (0251). B rows never appear in GetProduct or storefront reads — this is the admin's
// only window into seconds stock; an empty prices list marks the variant unsellable (fail-closed).
func (s *Server) ListVariantSeconds(ctx context.Context, req *pb_admin.ListVariantSecondsRequest) (*pb_admin.ListVariantSecondsResponse, error) {
	if req.ColorwayId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "colorway_id is required")
	}
	rows, err := s.repo.Products().ListVariantSeconds(ctx, int(req.ColorwayId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't list seconds variants",
			slog.Int64("colorway_id", req.ColorwayId), slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "can't list seconds variants")
	}
	out := make([]*pb_admin.SecondsVariant, 0, len(rows))
	for _, r := range rows {
		out = append(out, &pb_admin.SecondsVariant{
			Variant: dto.ConvertEntityVariantToPb(r.Variant),
			Prices:  dto.ConvertEntityPriceInsertsToPb(r.Prices),
		})
	}
	return &pb_admin.ListVariantSecondsResponse{Seconds: out}, nil
}

// SetVariantPrice replaces the manual price set of a B-grade variant (0251, owner decision:
// seconds are priced by hand). Full replace; an empty list clears the prices and the variant stops
// selling (fail-closed). The store validates the per-price catalogue rules, duplicate currencies
// and base-currency presence; grade 'A' targets are FAILED_PRECONDITION (A prices live on the
// colourway).
func (s *Server) SetVariantPrice(ctx context.Context, req *pb_admin.SetVariantPriceRequest) (*pb_admin.SetVariantPriceResponse, error) {
	if req.VariantId <= 0 {
		return nil, status.Error(codes.InvalidArgument, "variant_id is required")
	}
	prices := dto.ConvertPbPriceInsertsToEntity(req.Prices)
	if len(prices) != len(req.Prices) {
		return nil, status.Error(codes.InvalidArgument, "every price needs a currency and a parseable decimal amount")
	}
	err := s.repo.Products().SetVariantPrice(ctx, int(req.VariantId), prices)
	if err != nil {
		var ve *entity.ValidationError
		switch {
		case errors.As(err, &ve):
			return nil, apierr.Invalid(ve)
		case errors.Is(err, sql.ErrNoRows):
			return nil, status.Errorf(codes.NotFound, "variant %d not found", req.VariantId)
		case errors.Is(err, entity.ErrVariantPriceNotSeconds):
			return nil, status.Errorf(codes.FailedPrecondition, "variant %d is not a B-grade (seconds) variant — A prices live on the colourway", req.VariantId)
		default:
			slog.Default().ErrorContext(ctx, "can't set variant price",
				slog.Int64("variant_id", req.VariantId), slog.String("err", err.Error()))
			return nil, status.Error(codes.Internal, "can't set variant price")
		}
	}
	return &pb_admin.SetVariantPriceResponse{}, nil
}
