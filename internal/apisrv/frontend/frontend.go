package frontend

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements handlers for frontend requests.
type Server struct {
	pb_frontend.UnimplementedFrontendServiceServer
	repo dependency.Repository
}

// New creates a new server with frontend handlers.
func New(r dependency.Repository) *Server {
	return &Server{
		repo: r,
	}
}

// AcquireOrder
func (s *Server) AcquireOrder(ctx context.Context, req *pb_frontend.AcquireOrderRequest) (*pb_frontend.AcquireOrderResponse, error) {
	return nil, nil
}

// ApplyPromoCode
func (s *Server) ApplyPromoCode(ctx context.Context, req *pb_frontend.ApplyPromoCodeRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// GetHero
func (s *Server) GetHero(ctx context.Context, req *emptypb.Empty) (*pb_frontend.GetHeroResponse, error) {
	return nil, nil
}

// GetOrdersByEmail
func (s *Server) GetOrdersByEmail(ctx context.Context, req *pb_frontend.GetOrdersByEmailRequest) (*pb_frontend.GetOrdersByEmailResponse, error) {
	return nil, nil
}

// GetProductById
func (s *Server) GetProductById(ctx context.Context, req *pb_frontend.GetProductByIdRequest) (*pb_frontend.GetProductByIdResponse, error) {
	return nil, nil
}

// GetProductsPaged
func (s *Server) GetProductsPaged(ctx context.Context, req *pb_frontend.GetProductsPagedRequest) (*pb_frontend.GetProductsPagedResponse, error) {
	return nil, nil
}

// SubmitOrder
func (s *Server) SubmitOrder(ctx context.Context, req *pb_frontend.SubmitOrderRequest) (*pb_frontend.SubmitOrderResponse, error) {
	return nil, nil
}

// SubscribeNewsletter
func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// UnsubscribeNewsletter
func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// UpdateOrder
func (s *Server) UpdateOrder(ctx context.Context, req *pb_frontend.UpdateOrderRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// UpdateOrderCurrency
func (s *Server) UpdateOrderCurrency(ctx context.Context, req *pb_frontend.UpdateOrderCurrencyRequest) (*emptypb.Empty, error) {
	return nil, nil
}
