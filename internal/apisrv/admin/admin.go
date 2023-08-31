package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"golang.org/x/exp/slog"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo   dependency.Repository
	bucket dependency.FileStore
}

// New creates a new server with admin handlers.
func New(r dependency.Repository, b dependency.FileStore) *Server {
	return &Server{
		repo:   r,
		bucket: b,
	}
}

// UploadContentImage
func (s *Server) UploadContentImage(ctx context.Context, req *pb_admin.UploadContentImageRequest) (*pb_common.Media, error) {
	m, err := s.bucket.UploadContentImage(ctx, req.RawB64Image, req.Folder, req.ImageName)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't upload content image",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return m, err
}

// UploadContentVideo
func (s *Server) UploadContentVideo(ctx context.Context, req *pb_admin.UploadContentVideoRequest) (*pb_common.Media, error) {
	return s.bucket.UploadContentVideo(ctx, req.GetRaw(), req.Folder, req.VideoName, req.ContentType)
}

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	//TODO:
	return nil, s.bucket.DeleteFromBucket(ctx, req.ObjectKeys)
}

// ListObjects
func (s *Server) ListObjects(ctx context.Context, req *pb_admin.ListObjectsRequest) (*pb_admin.ListObjectsResponse, error) {
	list, err := s.bucket.ListObjects(ctx)
	if err != nil {
		return nil, err
	}
	return &pb_admin.ListObjectsResponse{
		Entities: list,
	}, err
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
