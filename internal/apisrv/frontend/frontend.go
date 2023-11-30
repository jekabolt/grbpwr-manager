package frontend

import (
	"context"
	"fmt"
	"slices"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"golang.org/x/exp/slog"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	hero, err := s.repo.Hero().GetHero(ctx)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get hero")
	}
	h, err := dto.ConvertEntityHeroFullToCommon(hero)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert entity hero to pb hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity hero to pb hero")
	}
	return &pb_frontend.GetHeroResponse{
		Hero: h,
	}, nil
}
func (s *Server) GetProductByName(ctx context.Context, req *pb_frontend.GetProductByNameRequest) (*pb_frontend.GetProductByNameResponse, error) {
	pf, err := s.repo.Products().GetProductByNameNoHidden(ctx, req.Name)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get product by id")
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert dto product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
	}

	return &pb_frontend.GetProductByNameResponse{
		Product: pbPrd,
	}, nil
}
func (s *Server) GetProductsPaged(ctx context.Context, req *pb_frontend.GetProductsPagedRequest) (*pb_frontend.GetProductsPagedResponse, error) {
	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// Validate parameters
	if req.Limit <= 0 || req.Offset <= 0 {
		req.Limit, req.Offset = 15, 0
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

	prds, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, false)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_common.Product, 0, len(prds))
	for _, prd := range prds {
		pbPrd, err := dto.ConvertEntityProductToCommon(&prd)
		if err != nil {
			slog.Default().ErrorCtx(ctx, "can't convert dto product to proto product",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
		}
		prdsPb = append(prdsPb, pbPrd)
	}

	return &pb_frontend.GetProductsPagedResponse{
		Products: prdsPb,
	}, nil
}
func (s *Server) SubmitOrder(ctx context.Context, req *pb_frontend.SubmitOrderRequest) (*pb_frontend.SubmitOrderResponse, error) {
	orderNew := dto.ConvertCommonOrderNewToEntity(req.Order)

	_, err := v.ValidateStruct(orderNew)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation order create request failed: %v", err).Error())
	}

	order, err := s.repo.Order().CreateOrder(ctx, orderNew)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	o, err := dto.ConvertEntityOrderToPbCommonOrder(order)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}

	return &pb_frontend.SubmitOrderResponse{
		Order: o,
	}, nil
}
func (s *Server) ApplyPromoCode(ctx context.Context, req *pb_frontend.ApplyPromoCodeRequest) (*pb_frontend.ApplyPromoCodeResponse, error) {
	newAmt, err := s.repo.Order().ApplyPromoCode(ctx, int(req.OrderId), req.PromoCode)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't apply promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't apply promo code")
	}
	return &pb_frontend.ApplyPromoCodeResponse{
		Total: &pb_decimal.Decimal{
			Value: newAmt.String(),
		},
	}, nil
}
func (s *Server) UpdateOrderItems(ctx context.Context, req *pb_frontend.UpdateOrderItemsRequest) (*pb_frontend.UpdateOrderItemsResponse, error) {
	itemsToInsert := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		oii, err := dto.ConvertPbOrderItemInsertToEntity(i)
		if err != nil {
			slog.Default().ErrorCtx(ctx, "can't convert pb order item to entity order item",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert pb order item to entity order item")
		}
		itemsToInsert = append(itemsToInsert, *oii)
	}

	newTotal, err := s.repo.Order().UpdateOrderItems(ctx, int(req.OrderId), itemsToInsert)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't update order items",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update order items")
	}
	return &pb_frontend.UpdateOrderItemsResponse{
		Total: &pb_decimal.Decimal{
			Value: newTotal.String(),
		},
	}, nil
}
func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	err := s.repo.Subscribers().Subscribe(ctx, req.Email, req.Name)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't subscribe",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't subscribe")
	}
	return &pb_frontend.SubscribeNewsletterResponse{}, nil
}
func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*pb_frontend.UnsubscribeNewsletterResponse, error) {
	err := s.repo.Subscribers().Unsubscribe(ctx, req.Email)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't unsubscribe",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't unsubscribe")
	}
	return &pb_frontend.UnsubscribeNewsletterResponse{}, nil
}
