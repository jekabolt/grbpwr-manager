package frontend

import (
	"context"
	"encoding/base64"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) SubmitOrderReview(ctx context.Context, req *pb_frontend.SubmitOrderReviewRequest) (*pb_frontend.SubmitOrderReviewResponse, error) {
	clientIP := middleware.GetClientIP(ctx)

	// Decode email
	emailBytes, err := base64.StdEncoding.DecodeString(req.B64Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't decode email",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't decode email")
	}
	email := string(emailBytes)

	// Rate limit
	if err := s.rateLimiter.CheckSupportTicket(clientIP, email); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for order review",
			slog.String("ip", clientIP),
			slog.String("email", email),
		)
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
	}

	// Convert proto to entity
	orderReview := dto.ConvertPbOrderReviewInsertToEntity(req.OrderReview)
	if orderReview == nil {
		return nil, status.Errorf(codes.InvalidArgument, "order_review is required")
	}

	itemReviews := dto.ConvertPbOrderItemReviewInsertsToEntity(req.ItemReviews)

	// Submit review
	err = s.repo.Order().AddOrderReview(ctx, req.OrderUuid, email, orderReview, itemReviews)
	if err != nil {
		// Check if it's a validation error
		if ve, ok := err.(*entity.ValidationError); ok {
			slog.Default().WarnContext(ctx, "order review validation failed",
				slog.String("err", ve.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Error(codes.InvalidArgument, ve.Error())
		}
		slog.Default().ErrorContext(ctx, "can't submit order review",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.Internal, "can't submit order review")
	}

	slog.Default().InfoContext(ctx, "order review submitted",
		slog.String("order_uuid", req.OrderUuid),
		slog.String("email", email),
	)

	return &pb_frontend.SubmitOrderReviewResponse{}, nil
}
