package admin

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetOrderReviewsPaged(ctx context.Context, req *pb_admin.GetOrderReviewsPagedRequest) (*pb_admin.GetOrderReviewsPagedResponse, error) {
	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	reviews, total, err := s.repo.Order().GetOrderReviewsPaged(ctx, int(req.Limit), int(req.Offset), of)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order reviews paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order reviews")
	}

	return &pb_admin.GetOrderReviewsPagedResponse{
		Reviews: dto.ConvertEntityOrderReviewFullsToPb(reviews),
		Total:   int32(total),
	}, nil
}

func (s *Server) DeleteOrderReview(ctx context.Context, req *pb_admin.DeleteOrderReviewRequest) (*pb_admin.DeleteOrderReviewResponse, error) {
	err := s.repo.Order().DeleteOrderReview(ctx, int(req.OrderId))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't delete order review",
			slog.String("err", err.Error()),
			slog.Int("order_id", int(req.OrderId)),
		)
		return nil, status.Errorf(codes.Internal, "can't delete order review")
	}

	slog.Default().InfoContext(ctx, "order review deleted",
		slog.Int("order_id", int(req.OrderId)),
	)

	return &pb_admin.DeleteOrderReviewResponse{}, nil
}

func (s *Server) GetProductReviewsPaged(ctx context.Context, req *pb_admin.GetProductReviewsPagedRequest) (*pb_admin.GetProductReviewsPagedResponse, error) {
	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	reviews, total, err := s.repo.Order().GetProductReviewsPaged(ctx, int(req.ProductId), int(req.Limit), int(req.Offset), of)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get product reviews paged",
			slog.String("err", err.Error()),
			slog.Int("product_id", int(req.ProductId)),
		)
		return nil, status.Errorf(codes.Internal, "can't get product reviews")
	}

	pbReviews := make([]*pb_common.OrderItemReview, 0, len(reviews))
	for i := range reviews {
		pbReviews = append(pbReviews, dto.ConvertEntityOrderItemReviewToPb(&reviews[i]))
	}

	return &pb_admin.GetProductReviewsPagedResponse{
		Reviews: pbReviews,
		Total:   int32(total),
	}, nil
}
