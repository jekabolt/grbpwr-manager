package frontend

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) NotifyMe(ctx context.Context, req *pb_frontend.NotifyMeRequest) (*pb_frontend.NotifyMeResponse, error) {
	productId := int(req.ProductId)
	sizeId := int(req.SizeId)

	email := normalizeEmail(req.Email)
	if email == "" || !v.IsEmail(email) {
		return nil, status.Errorf(codes.InvalidArgument, "valid email is required")
	}
	ip := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckSubscribe(ip, email); err != nil {
		return nil, status.Error(codes.ResourceExhausted, err.Error())
	}

	// Validate product exists and is not hidden/deleted
	_, err := s.repo.Products().GetProductByIdNoHidden(ctx, productId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product",
			slog.String("err", err.Error()),
			slog.Int("productId", productId),
		)
		return nil, status.Errorf(codes.Internal, "can't get product")
	}

	// Validate size exists
	_, ok := cache.GetSizeById(sizeId)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "invalid size_id")
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
