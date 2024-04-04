package frontend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	gerr "github.com/jekabolt/grbpwr-manager/internal/errors"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Server implements handlers for frontend requests.
type Server struct {
	pb_frontend.UnimplementedFrontendServiceServer
	repo            dependency.Repository
	rates           dependency.RatesService
	mailer          dependency.Mailer
	usdtTron        dependency.CryptoInvoice
	usdtTronTestnet dependency.CryptoInvoice
}

// New creates a new server with frontend handlers.
func New(
	r dependency.Repository,
	m dependency.Mailer,
	ra dependency.RatesService,
	usdtTron dependency.CryptoInvoice,
	usdtTronTestnet dependency.CryptoInvoice,
) *Server {
	return &Server{
		repo:            r,
		mailer:          m,
		rates:           ra,
		usdtTron:        usdtTron,
		usdtTronTestnet: usdtTronTestnet,
	}
}

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	hero, err := s.repo.Hero().GetHero(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get hero",
			slog.String("err", err.Error()),
		)
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.Internal, "can't get hero")
		}
	}
	h, err := dto.ConvertEntityHeroFullToCommon(hero)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity hero to pb hero",
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
		slog.Default().ErrorContext(ctx, "can't get product by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get product by id")
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
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
		slog.Default().ErrorContext(ctx, "can't get products paged",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get products paged")
	}

	prdsPb := make([]*pb_common.Product, 0, len(prds))
	for _, prd := range prds {
		pbPrd, err := dto.ConvertEntityProductToCommon(&prd)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
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
		slog.Default().ErrorContext(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation order create request failed: %v", err).Error())
	}

	order, err := s.repo.Order().CreateOrder(ctx, orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	o, err := dto.ConvertEntityOrderToPbCommonOrder(order)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}

	return &pb_frontend.SubmitOrderResponse{
		Order: o,
	}, nil
}

func (s *Server) GetOrderByUUID(ctx context.Context, req *pb_frontend.GetOrderByUUIDRequest) (*pb_frontend.GetOrderByUUIDResponse, error) {
	o, err := s.repo.Order().GetOrderFullByUUID(ctx, req.Uuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order by uuid",
			slog.String("err", err.Error()),
		)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, gerr.OrderNotFound
		}
		return nil, status.Errorf(codes.Internal, "can't get order by uuid")
	}

	oPb, err := dto.ConvertEntityOrderFullToPbOrderFull(o)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order full to pb order full",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order full to pb order full")
	}

	return &pb_frontend.GetOrderByUUIDResponse{
		Order: oPb,
	}, nil
}

func (s *Server) ValidateOrderItemsInsert(ctx context.Context, req *pb_frontend.ValidateOrderItemsInsertRequest) (*pb_frontend.ValidateOrderItemsInsertResponse, error) {
	itemsToInsert := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		oii, err := dto.ConvertPbOrderItemInsertToEntity(i)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert pb order item to entity order item",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert pb order item to entity order item")
		}
		itemsToInsert = append(itemsToInsert, *oii)
	}

	oii, subtotal, err := s.repo.Order().ValidateOrderItemsInsert(ctx, itemsToInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't validate order items insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order items insert")
	}

	pbOii := make([]*pb_common.OrderItemInsert, 0, len(oii))
	for _, i := range oii {
		pbOii = append(pbOii, dto.ConvertEntityOrderItemInsertToPb(&i))
	}

	return &pb_frontend.ValidateOrderItemsInsertResponse{
		Items:    pbOii,
		Subtotal: &pb_decimal.Decimal{Value: subtotal.String()},
	}, nil

}
func (s *Server) ValidateOrderByUUID(ctx context.Context, req *pb_frontend.ValidateOrderByUUIDRequest) (*pb_frontend.ValidateOrderByUUIDResponse, error) {
	orderFull, err := s.repo.Order().ValidateOrderByUUID(ctx, req.Uuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't validate order by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order by uuid")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_frontend.ValidateOrderByUUIDResponse{
		Order: of,
	}, nil
}

func (s *Server) GetOrderInvoice(ctx context.Context, req *pb_frontend.GetOrderInvoiceRequest) (*pb_frontend.GetOrderInvoiceResponse, error) {

	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)

	pme, _ := s.repo.Cache().GetPaymentMethodsByName(pm)
	if !pme.Allowed {
		slog.Default().ErrorContext(ctx, "payment method not allowed")
		return nil, status.Errorf(codes.PermissionDenied, "payment method not allowed")
	}

	switch pm {
	case entity.USDT_TRON:

		pi, expire, err := s.usdtTron.GetOrderInvoice(ctx, int(req.OrderId))
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order invoice",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get order invoice")
		}

		pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
		}

		return &pb_frontend.GetOrderInvoiceResponse{
			Payment:   pbPi,
			ExpiredAt: timestamppb.New(expire),
		}, nil

	case entity.USDT_TRON_TEST:

		pi, expire, err := s.usdtTronTestnet.GetOrderInvoice(ctx, int(req.OrderId))
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order invoice",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get order invoice")
		}

		pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
		}

		return &pb_frontend.GetOrderInvoiceResponse{
			Payment:   pbPi,
			ExpiredAt: timestamppb.New(expire),
		}, nil

	default:
		slog.Default().ErrorContext(ctx, "payment method unimplemented")
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}

}

func (s *Server) CheckCryptoPayment(ctx context.Context, req *pb_frontend.CheckCryptoPaymentRequest) (*pb_frontend.CheckCryptoPaymentResponse, error) {

	p, o, err := s.repo.Order().CheckPaymentPendingByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't check payment pending by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't check payment pending by uuid")
	}

	pm, ok := s.repo.Cache().GetPaymentMethodById(p.PaymentMethodID)
	if !ok {
		slog.Default().ErrorContext(ctx, "can't get payment method by id",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get payment method by id")
	}

	checker := s.usdtTron
	switch pm.Name {
	case entity.USDT_TRON:
		checker = s.usdtTron
	case entity.USDT_TRON_TEST:
		checker = s.usdtTronTestnet
	default:
		slog.Default().ErrorContext(ctx, "payment method is not allowed",
			slog.Any("paymentMethod", pm),
		)
		return nil, status.Errorf(codes.Unimplemented, "payment method is not allowed")
	}

	p, err = checker.CheckForTransactions(ctx, int(o.ID), p)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't check for transactions",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't check for transactions")
	}

	pbPayment, err := dto.ConvertEntityToPbPayment(p)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity payment to pb payment",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity payment to pb payment")
	}

	return &pb_frontend.CheckCryptoPaymentResponse{
		Payment: pbPayment,
	}, nil

}

func (s *Server) ApplyPromoCode(ctx context.Context, req *pb_frontend.ApplyPromoCodeRequest) (*pb_frontend.ApplyPromoCodeResponse, error) {
	orderFull, err := s.repo.Order().ApplyPromoCode(ctx, int(req.OrderId), req.PromoCode)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't apply promo code",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't apply promo code")
	}

	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_frontend.ApplyPromoCodeResponse{
		Order: of,
	}, nil
}

func (s *Server) UpdateOrderItems(ctx context.Context, req *pb_frontend.UpdateOrderItemsRequest) (*pb_frontend.UpdateOrderItemsResponse, error) {
	itemsToInsert := make([]entity.OrderItemInsert, 0, len(req.Items))
	for _, i := range req.Items {
		oii, err := dto.ConvertPbOrderItemInsertToEntity(i)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert pb order item to entity order item",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert pb order item to entity order item")
		}
		itemsToInsert = append(itemsToInsert, *oii)
	}

	orderFull, err := s.repo.Order().UpdateOrderItems(ctx, int(req.OrderId), itemsToInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update order items",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update order items")
	}
	of, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
	}
	return &pb_frontend.UpdateOrderItemsResponse{
		Order: of,
	}, nil
}

func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	// Subscribe the user.
	err := s.repo.Subscribers().Subscribe(ctx, req.Email, req.Name)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't subscribe", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't subscribe")
	}

	// Send new subscriber mail.
	// TODO: in tx
	err = s.mailer.SendNewSubscriber(ctx, s.repo, req.Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't send new subscriber mail",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't send new subscriber mail")
	}

	return &pb_frontend.SubscribeNewsletterResponse{}, nil
}

func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*pb_frontend.UnsubscribeNewsletterResponse, error) {
	err := s.repo.Subscribers().Unsubscribe(ctx, req.Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't unsubscribe",
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
		slog.Default().ErrorContext(ctx, "can't get archives paged",
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
		slog.Default().ErrorContext(ctx, "can't get archive by id",
			slog.String("err", err.Error()),
		)
		return nil, err
	}
	return &pb_frontend.GetArchiveByIdResponse{
		Archive: dto.ConvertArchiveFullEntityToPb(af),
	}, nil
}
