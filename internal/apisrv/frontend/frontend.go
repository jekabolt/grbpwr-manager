package frontend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	gerr "github.com/jekabolt/grbpwr-manager/internal/errors"
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
	repo   dependency.Repository
	mailer dependency.Mailer
	rates  dependency.RatesService
}

// New creates a new server with frontend handlers.
func New(r dependency.Repository, m dependency.Mailer, ra dependency.RatesService) *Server {
	return &Server{
		repo:   r,
		mailer: m,
		rates:  ra,
	}
}

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	hero, err := s.repo.Hero().GetHero(ctx)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get hero",
			slog.String("err", err.Error()),
		)
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "can't get hero")
		}
	}
	h, err := dto.ConvertEntityHeroFullToCommon(hero)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert entity hero to pb hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity hero to pb hero")
	}

	return &pb_frontend.GetHeroResponse{
		Hero:       h,
		Dictionary: dto.ConvertToCommonDictionary(s.repo.Cache().GetDict()),
		Rates:      dto.CurrencyRateToPb(s.rates.GetRates()),
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

func (s *Server) GetOrderByUUID(ctx context.Context, req *pb_frontend.GetOrderByUUIDRequest) (*pb_frontend.GetOrderByUUIDResponse, error) {
	o, err := s.repo.Order().GetOrderByUUID(ctx, req.Uuid)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get order by uuid",
			slog.String("err", err.Error()),
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, gerr.OrderNotFound
		}
		return nil, status.Errorf(codes.Internal, "can't get order by uuid")
	}

	oPb, err := dto.ConvertEntityOrderFullToPbOrderFull(o)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert entity order full to pb order full",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order full to pb order full")
	}

	return &pb_frontend.GetOrderByUUIDResponse{
		Order: oPb,
	}, nil
}

func (s *Server) OrderPaymentDone(ctx context.Context, req *pb_frontend.OrderPaymentDoneRequest) (*pb_frontend.OrderPaymentDoneResponse, error) {
	pi, err := dto.ConvertToEntityPaymentInsert(req.Payment)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't convert payment to entity payment insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't convert payment to entity payment insert")
	}

	if !pi.IsTransactionDone {
		slog.Default().ErrorCtx(ctx, "payment transaction is not done")
		return nil, status.Errorf(codes.InvalidArgument, "payment transaction is not done")
	}
	if pi.TransactionAmount.IsZero() {
		slog.Default().ErrorCtx(ctx, "payment transaction amount is zero")
		return nil, status.Errorf(codes.InvalidArgument, "payment transaction amount is zero")
	}

	err = s.repo.Order().OrderPaymentDone(ctx, req.OrderUuid, pi)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't mark order as paid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't mark order as paid")
	}

	o, err := s.repo.Order().GetOrderByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get order by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order by uuid")
	}

	s.mailer.SendOrderConfirmation(ctx, o.Buyer.Email, &dto.OrderConfirmed{
		Name:            fmt.Sprintf("%s %s", o.Buyer.FirstName, o.Buyer.LastName),
		OrderUUID:       req.OrderUuid,
		OrderDate:       o.Order.Placed,
		TotalAmount:     o.Order.TotalPrice.String(),
		PaymentMethod:   req.Payment.PaymentMethod.String(),
		PaymentCurrency: dto.ConvertPaymentMethodToCurrency(req.Payment.PaymentMethod),
	})

	return &pb_frontend.OrderPaymentDoneResponse{}, nil
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
	// Subscribe the user.
	err := s.repo.Subscribers().Subscribe(ctx, req.Email, req.Name)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't subscribe", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't subscribe")
	}

	// Send new subscriber mail.
	sr, err := s.mailer.SendNewSubscriber(ctx, req.Email)
	if sr == nil {
		slog.Default().ErrorCtx(ctx, "send new subscriber mail returned nil sr", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.InvalidArgument, "send new subscriber mail error")
	}

	// Update sr based on the error from SendNewSubscriber.
	if err != nil {
		sr.Sent = false
	} else {
		sr.Sent = true
	}

	err = s.repo.Mail().AddMail(ctx, sr)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't add mail to the database", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't add mail to the database")
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

func (s *Server) GetArchivesPaged(ctx context.Context, req *pb_frontend.GetArchivesPagedRequest) (*pb_frontend.GetArchivesPagedResponse, error) {
	afs, err := s.repo.Archive().GetArchivesPaged(ctx,
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get archives paged",
			slog.String("err", err.Error()),
		)
		return nil, err
	}

	pbAfs := make([]*pb_common.ArchiveFull, len(afs))

	for _, af := range afs {
		pbAfs = append(pbAfs, dto.ConvertArchiveFullEntityToPb(&af))
	}

	return &pb_frontend.GetArchivesPagedResponse{
		Archives: pbAfs,
	}, nil

}
func (s *Server) GetArchiveById(ctx context.Context, req *pb_frontend.GetArchiveByIdRequest) (*pb_frontend.GetArchiveByIdResponse, error) {
	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorCtx(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_frontend.GetArchiveByIdResponse{
		Archive: dto.ConvertArchiveFullEntityToPb(af),
	}, nil
}
