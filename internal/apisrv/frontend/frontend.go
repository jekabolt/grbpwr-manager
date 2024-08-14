package frontend

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	gerr "github.com/jekabolt/grbpwr-manager/internal/errors"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/shopspring/decimal"
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
			return nil, status.Errorf(codes.NotFound, "can't get hero")
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

func (s *Server) GetProduct(ctx context.Context, req *pb_frontend.GetProductRequest) (*pb_frontend.GetProductResponse, error) {

	pf, err := s.repo.Products().GetProductByIdShowHidden(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get product by full name",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get product by full name")
	}

	pbPrd, err := dto.ConvertToPbProductFull(pf)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert dto product to proto product",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert dto product to proto product")
	}

	return &pb_frontend.GetProductResponse{
		Product: pbPrd,
	}, nil
}

func (s *Server) GetProductsPaged(ctx context.Context, req *pb_frontend.GetProductsPagedRequest) (*pb_frontend.GetProductsPagedResponse, error) {
	sfs := make([]entity.SortFactor, 0, len(req.SortFactors))
	for _, sf := range req.SortFactors {
		sfs = append(sfs, dto.ConvertPBCommonSortFactorToEntity(sf))
	}

	// remove duplicates
	sfs = slices.Compact(sfs)

	of := dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor)

	fc := dto.ConvertPBCommonFilterConditionsToEntity(req.FilterConditions)

	prds, count, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, false)
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
		Total:    int32(count),
	}, nil
}

func (s *Server) SubmitOrder(ctx context.Context, req *pb_frontend.SubmitOrderRequest) (*pb_frontend.SubmitOrderResponse, error) {
	orderNew, receivePromo := dto.ConvertCommonOrderNewToEntity(req.Order)

	_, err := v.ValidateStruct(orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation order create request failed: %v", err).Error())
	}

	order, sendEmail, err := s.repo.Order().CreateOrder(ctx, orderNew, receivePromo)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	if sendEmail {
		err := s.mailer.SendNewSubscriber(ctx, s.repo, orderNew.Buyer.Email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't send new subscriber mail",
				slog.String("err", err.Error()),
			)
		}
	}

	pm := dto.ConvertPbPaymentMethodToEntity(req.Order.PaymentMethod)

	pme, ok := s.repo.Cache().GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "internal error")
	}
	if !pme.Allowed {
		slog.Default().ErrorContext(ctx, "payment method not allowed")
		return nil, status.Errorf(codes.PermissionDenied, "payment method not allowed")
	}

	invoice, err := s.getInvoiceByPaymentMethod(ctx, pm, order.UUID)
	if err != nil {
		return nil, err
	}

	eos, ok := s.repo.Cache().GetOrderStatusById(order.OrderStatusID)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve order status")
		return nil, status.Errorf(codes.Internal, "internal error")
	}

	os, ok := dto.ConvertEntityToPbOrderStatus(eos.Name)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to convert order status")
		return nil, status.Errorf(codes.Internal, "internal error")
	}

	pbPi, err := dto.ConvertEntityToPbPaymentInsert(invoice.Payment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
	}
	pbPi.PaymentMethod = req.Order.PaymentMethod

	return &pb_frontend.SubmitOrderResponse{
		OrderUuid:   order.UUID,
		OrderStatus: os,
		ExpiredAt:   timestamppb.New(invoice.ExpiredAt),
		Payment:     pbPi,
	}, nil
}

func (s *Server) GetOrderByUUID(ctx context.Context, req *pb_frontend.GetOrderByUUIDRequest) (*pb_frontend.GetOrderByUUIDResponse, error) {
	o, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
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

	oiv, err := s.repo.Order().ValidateOrderItemsInsert(ctx, itemsToInsert)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't validate order items insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order items insert")
	}

	pbOii := make([]*pb_common.OrderItem, 0, len(oiv.ValidItems))
	for _, i := range oiv.ValidItems {
		pbOii = append(pbOii, dto.ConvertEntityOrderItemToPb(&i))
	}

	shipmentCarrier, scOk := s.repo.Cache().GetShipmentCarrierById(int(req.ShipmentCarrierId))
	if scOk && !shipmentCarrier.Allowed {
		slog.Default().ErrorContext(ctx, "shipment carrier not allowed",
			slog.Any("shipmentCarrier", shipmentCarrier),
		)
		return nil, status.Errorf(codes.PermissionDenied, "shipment carrier not allowed")
	}
	if scOk && shipmentCarrier.Allowed {
		oiv.Subtotal = oiv.Subtotal.Add(shipmentCarrier.Price)
	}

	promo, ok := s.repo.Cache().GetPromoByName(req.PromoCode)
	if ok && promo.Allowed && promo.FreeShipping && scOk {
		oiv.Subtotal = oiv.Subtotal.Sub(shipmentCarrier.Price)
	}

	totalSale := oiv.Subtotal

	if ok && promo.Allowed {
		if !promo.Discount.Equals(decimal.Zero) {
			totalSale = totalSale.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}
	}

	return &pb_frontend.ValidateOrderItemsInsertResponse{
		ValidItems: pbOii,
		HasChanged: oiv.HasChanged,
		Subtotal:   &pb_decimal.Decimal{Value: oiv.Subtotal.String()},
		TotalSale:  &pb_decimal.Decimal{Value: totalSale.String()},
		Promo:      dto.ConvertEntityPromoInsertToPb(promo.PromoCodeInsert),
	}, nil

}
func (s *Server) ValidateOrderByUUID(ctx context.Context, req *pb_frontend.ValidateOrderByUUIDRequest) (*pb_frontend.ValidateOrderByUUIDResponse, error) {
	orderFull, err := s.repo.Order().ValidateOrderByUUID(ctx, req.OrderUuid)
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

	pme, ok := s.repo.Cache().GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "internal error")
	}
	if !pme.Allowed {
		slog.Default().ErrorContext(ctx, "payment method not allowed")
		return nil, status.Errorf(codes.PermissionDenied, "payment method not allowed")
	}

	invoice, err := s.getInvoiceByPaymentMethod(ctx, pm, req.OrderUuid)
	if err != nil {
		return nil, err
	}

	pbPi, err := dto.ConvertEntityToPbPaymentInsert(invoice.Payment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
	}
	pbPi.PaymentMethod = req.PaymentMethod

	return &pb_frontend.GetOrderInvoiceResponse{
		Payment:   pbPi,
		ExpiredAt: timestamppb.New(invoice.ExpiredAt),
	}, nil
}

type InvoiceDetails struct {
	Payment   *entity.PaymentInsert
	ExpiredAt time.Time
}

func (s *Server) getInvoiceByPaymentMethod(ctx context.Context, pm entity.PaymentMethodName, orderUuid string) (*InvoiceDetails, error) {
	var handler dependency.CryptoInvoice
	switch pm {
	case entity.USDT_TRON:
		handler = s.usdtTron
	case entity.USDT_TRON_TEST:
		handler = s.usdtTronTestnet
	default:
		slog.Default().ErrorContext(ctx, "payment method unimplemented")
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}

	pi, expire, err := handler.GetOrderInvoice(ctx, orderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order invoice", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get order invoice")
	}

	return &InvoiceDetails{
		Payment:   pi,
		ExpiredAt: expire,
	}, nil
}

func (s *Server) CancelOrderInvoice(ctx context.Context, req *pb_frontend.CancelOrderInvoiceRequest) (*pb_frontend.CancelOrderInvoiceResponse, error) {
	payment, err := s.repo.Order().ExpireOrderPayment(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't expire order payment",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't expire order payment")
	}
	pme, _ := s.repo.Cache().GetPaymentMethodById(payment.PaymentMethodID)

	slog.Default().DebugContext(ctx, "cancel order invoice",
		slog.Any("paymentMethod", pme),
	)

	switch pme.Name {
	case entity.USDT_TRON:
		err = s.usdtTron.CancelMonitorPayment(req.OrderUuid)
	case entity.USDT_TRON_TEST:
		err = s.usdtTronTestnet.CancelMonitorPayment(req.OrderUuid)
	default:
		slog.Default().ErrorContext(ctx, "payment method unimplemented")
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel monitor payment",
			slog.String("err", err.Error()),
			slog.Any("paymentMethod", pme.Name),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel monitor payment")
	}

	return &pb_frontend.CancelOrderInvoiceResponse{}, nil
}

func (s *Server) CheckCryptoPayment(ctx context.Context, req *pb_frontend.CheckCryptoPaymentRequest) (*pb_frontend.CheckCryptoPaymentResponse, error) {

	payment, order, err := s.repo.Order().CheckPaymentPendingByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't check payment pending by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't check payment pending by uuid")
	}

	pm, ok := s.repo.Cache().GetPaymentMethodById(payment.PaymentMethodID)
	if !ok {
		slog.Default().ErrorContext(ctx, "can't get payment method by id",
			slog.Any("paymentMethodId", payment.PaymentMethodID),
		)
		return nil, status.Errorf(codes.Internal, "can't get payment method by id")
	}

	// TODO:
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

	payment, err = checker.CheckForTransactions(ctx, order.UUID, payment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't check for transactions",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't check for transactions")
	}

	pbPayment, err := dto.ConvertEntityToPbPayment(payment)
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

func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	// Subscribe the user.
	err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, true)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't subscribe", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.AlreadyExists, "can't subscribe")
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
	err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, false)
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

	pbAfs := make([]*pb_common.ArchiveFull, 0, len(afs))

	for _, af := range afs {
		pbAfs = append(pbAfs, dto.ConvertArchiveFullEntityToPb(&af))
	}

	return &pb_frontend.GetArchivesPagedResponse{
		Archives: pbAfs,
	}, nil

}
