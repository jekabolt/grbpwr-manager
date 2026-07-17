package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) NotifyMe(ctx context.Context, req *pb_frontend.NotifyMeRequest) (*pb_frontend.NotifyMeResponse, error) {
	email := normalizeEmail(req.Email)
	if email == "" || !v.IsEmail(email) {
		return nil, status.Errorf(codes.InvalidArgument, "valid email is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckSubscribe(ip, email); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	// R2/R3/p013: the storefront supplies the public variant SKU; resolve it to the internal variant
	// (product_id, size_id) the waitlist keys on. Unknown SKU -> NOT_FOUND.
	if req.VariantSku == "" {
		return nil, status.Errorf(codes.InvalidArgument, "variant_sku is required")
	}
	variant, err := s.repo.Products().GetVariantBySKU(ctx, req.VariantSku)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "variant not found")
		}
		slog.Default().ErrorContext(ctx, "can't resolve variant", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't resolve variant")
	}
	productId := variant.ProductId
	sizeId := variant.SizeId

	// Validate the colourway exists and is not hidden/deleted
	if _, err := s.repo.Products().GetProductByIdNoHidden(ctx, productId); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product",
			slog.String("err", err.Error()),
			slog.Int("productId", productId),
		)
		return nil, status.Errorf(codes.Internal, "can't get product")
	}

	// Add to waitlist (this also ensures subscriber exists)
	err = s.repo.Products().AddToWaitlist(ctx, productId, sizeId, email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add to waitlist",
			slog.String("err", err.Error()),
			slog.String("email", email),
			slog.Int("productId", productId),
			slog.Int("sizeId", sizeId),
		)
		return nil, status.Errorf(codes.Internal, "can't add to waitlist")
	}

	slog.Default().InfoContext(ctx, "added to waitlist",
		slog.String("email", email),
		slog.Int("productId", productId),
		slog.Int("sizeId", sizeId),
	)

	return &pb_frontend.NotifyMeResponse{}, nil
}
