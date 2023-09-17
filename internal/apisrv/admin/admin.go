package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/form"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"golang.org/x/exp/slog"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
	r := form.UploadContentImageRequest{
		UploadContentImageRequest: req,
	}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

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
	r := form.UploadContentVideoRequest{
		UploadContentVideoRequest: req,
	}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

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

	slog.Default().DebugCtx(ctx, "DeleteFromBucket request",
		slog.Any("req", req.ObjectKeys),
	)

	r := form.DeleteFromBucketRequest{
		DeleteFromBucketRequest: req,
	}
	resp := &pb_admin.DeleteFromBucketResponse{}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return resp, err
	}
	err := s.bucket.DeleteFromBucket(ctx, req.ObjectKeys)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete object from bucket",
			slog.String("err", err.Error()),
		)
		return resp, err
	}
	return resp, err
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
	r := form.AddProductRequest{
		AddProductRequest: req,
	}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	sizes := dto.ConvertProtoSize(req.GetAvailableSizes())
	prices, err := dto.ConvertProtoPrice(req.GetPrice())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert proto price to dto price",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	media := dto.ConvertProtoMediaArray(req.GetProductMedia())

	err = s.repo.Products().AddProduct(ctx,
		req.GetName(),
		req.GetDescription(),
		req.GetPreorder(),
		sizes,
		prices,
		media,
		req.Categories,
	)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't create a product",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return nil, nil
}

func (s *Server) UpdateName(context.Context, *pb_admin.UpdateNameRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateName not implemented")
}
func (s *Server) UpdateDescription(context.Context, *pb_admin.UpdateDescriptionRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateDescription not implemented")
}
func (s *Server) UpdatePreorder(context.Context, *pb_admin.UpdatePreorderRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdatePreorder not implemented")
}
func (s *Server) UpdatePrice(context.Context, *pb_admin.UpdatePriceRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdatePrice not implemented")
}
func (s *Server) UpdateSizes(context.Context, *pb_admin.UpdateSizesRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateSizes not implemented")
}
func (s *Server) UpdateCategories(context.Context, *pb_admin.UpdateCategoriesRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateCategories not implemented")
}
func (s *Server) UpdateMedia(context.Context, *pb_admin.UpdateMediaRequest) (*emptypb.Empty, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdateMedia not implemented")
}

// DeleteProduct
func (s *Server) DeleteProduct(ctx context.Context, req *pb_admin.DeleteProductRequest) (*pb_admin.DeleteProductResponse, error) {
	r := form.DeleteProductRequest{
		DeleteProductRequest: req,
	}

	resp := &pb_admin.DeleteProductResponse{}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return resp, err
	}

	err := s.repo.Products().DeleteProductByID(ctx, req.GetProductId())
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't delete a product",
			slog.String("err", err.Error()),
		)
		return resp, err
	}

	return resp, nil
}

// GetOrdersByStatus
func (s *Server) GetOrdersByStatus(ctx context.Context, req *pb_admin.GetOrdersByStatusRequest) (*pb_admin.GetOrdersByStatusResponse, error) {
	r := form.GetOrdersByStatusRequest{
		GetOrdersByStatusRequest: req,
	}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	status, err := dto.ConvertProtoOrderStatus(&req.Status)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert proto order status to dto order status",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	orders, err := s.repo.Order().GetOrderByStatus(ctx, status)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't create a product",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	ordersProto := &pb_admin.GetOrdersByStatusResponse{}
	for _, order := range orders {
		opb, err := order.ConvertToProtoOrder()
		if err != nil {
			slog.Default().ErrorCtx(ctx, "can't convert dto order to proto order",
				slog.String("err", err.Error()),
			)
			return nil, err
		}
		ordersProto.Orders = append(ordersProto.Orders, opb)
	}
	return ordersProto, nil
}

// HideProduct
func (s *Server) HideProduct(ctx context.Context, req *pb_admin.HideProductRequest) (*emptypb.Empty, error) {
	r := form.HideProductRequest{
		HideProductRequest: req,
	}
	if err := r.Validate(); err != nil {
		slog.Default().ErrorCtx(ctx, "validation request failed",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	err := s.repo.Products().HideProductByID(ctx, req.ProductId, req.Hide)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't hide a product",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

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
