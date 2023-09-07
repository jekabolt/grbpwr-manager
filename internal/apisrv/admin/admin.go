package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
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
	media, err := s.bucket.UploadContentVideo(ctx, req.GetRaw(), req.Folder, req.VideoName, req.ContentType)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't upload content video",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return media, nil
}

// DeleteFromBucket
func (s *Server) DeleteFromBucket(ctx context.Context, req *pb_admin.DeleteFromBucketRequest) (*pb_admin.DeleteFromBucketResponse, error) {
	err := s.bucket.DeleteFromBucket(ctx, req.ObjectKeys)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return nil, err
}

// ListObjects
func (s *Server) ListObjects(ctx context.Context, req *pb_admin.ListObjectsRequest) (*pb_admin.ListObjectsResponse, error) {
	list, err := s.bucket.ListObjects(ctx)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't list objects from bucket",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_admin.ListObjectsResponse{
		Entities: list,
	}, err
}

// AddProduct
func (s *Server) AddProduct(ctx context.Context, req *pb_admin.AddProductRequest) (*pb_admin.AddProductResponse, error) {
	prd, err := dto.ValidateConvertProtoProduct(req.GetProduct())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	err = s.repo.Products().AddProduct(ctx, prd)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't create a product",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
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
