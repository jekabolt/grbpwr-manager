package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo dependency.Repository
}

// New creates a new server with admin handlers.
func New(r dependency.Repository) *Server {
	return &Server{
		repo: r,
	}
}

// AddProduct
func (s *Server) AddProduct(ctx context.Context, req *pb_admin.AddProductRequest) (*pb_admin.AddProductResponse, error) {
	return nil, nil
}

// DeleteProduct
func (s *Server) DeleteProduct(ctx context.Context, req *pb_admin.DeleteProductRequest) (*pb_admin.DeleteProductResponse, error) {
	return nil, nil
}

// GetOrdersByStatus
func (s *Server) GetOrdersByStatus(ctx context.Context, req *pb_admin.GetOrdersByStatusRequest) (*pb_admin.GetOrdersByStatusResponse, error) {
	return nil, nil
}

// HideProduct
func (s *Server) HideProduct(ctx context.Context, req *pb_admin.HideProductRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// RefundOrder
func (s *Server) RefundOrder(ctx context.Context, req *pb_admin.RefundOrderRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// SetHero
func (s *Server) SetHero(ctx context.Context, req *pb_admin.SetHeroRequest) (*pb_admin.SetHeroResponse, error) {
	return nil, nil
}

// SetSaleByID
func (s *Server) SetSaleByID(ctx context.Context, req *pb_admin.SetSaleByIDRequest) (*emptypb.Empty, error) {
	return nil, nil
}

// UpdateShippingInfo
func (s *Server) UpdateShippingInfo(ctx context.Context, req *pb_admin.UpdateShippingInfoRequest) (*emptypb.Empty, error) {
	return nil, nil
}
