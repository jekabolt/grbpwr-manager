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
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements handlers for frontend requests.
type Server struct {
	pb_frontend.UnimplementedFrontendServiceServer
	repo              dependency.Repository
	rates             dependency.RatesService
	mailer            dependency.Mailer
	usdtTron          dependency.Invoicer
	usdtTronTestnet   dependency.Invoicer
	stripePayment     dependency.Invoicer
	stripePaymentTest dependency.Invoicer
}

// New creates a new server with frontend handlers.
func New(
	r dependency.Repository,
	m dependency.Mailer,
	ra dependency.RatesService,
	usdtTron dependency.Invoicer,
	usdtTronTestnet dependency.Invoicer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
) *Server {
	return &Server{
		repo:              r,
		mailer:            m,
		rates:             ra,
		usdtTron:          usdtTron,
		usdtTronTestnet:   usdtTronTestnet,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
	}
}

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {

	h, err := dto.ConvertEntityHeroFullToCommon(cache.GetHero())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity hero to pb hero",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert entity hero to pb hero")
	}

	return &pb_frontend.GetHeroResponse{
		Hero: h,
		Dictionary: dto.ConvertToCommonDictionary(dto.Dict{
			Categories:       cache.GetCategories(),
			Measurements:     cache.GetMeasurements(),
			OrderStatuses:    cache.GetOrderStatuses(),
			PaymentMethods:   cache.GetPaymentMethods(),
			ShipmentCarriers: cache.GetShipmentCarriers(),
			Sizes:            cache.GetSizes(),
			Genders:          cache.GetGenders(),
			SortFactors:      cache.GetSortFactors(),
			OrderFactors:     cache.GetOrderFactors(),
			SiteEnabled:      cache.GetSiteAvailability(),
			MaxOrderItems:    cache.GetMaxOrderItems(),
			BaseCurrency:     cache.GetBaseCurrency(),
		}),
		Rates: dto.CurrencyRateToPb(s.rates.GetRates()),
	}, nil
}

func (s *Server) GetProduct(ctx context.Context, req *pb_frontend.GetProductRequest) (*pb_frontend.GetProductResponse, error) {

	pf, err := s.repo.Products().GetProductByIdNoHidden(ctx, int(req.Id))
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

	pm := dto.ConvertPbPaymentMethodToEntity(req.Order.PaymentMethod)

	pme, ok := cache.GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "internal error")
	}
	if !pme.Method.Allowed {
		slog.Default().ErrorContext(ctx, "payment method not allowed")
		return nil, status.Errorf(codes.PermissionDenied, "payment method not allowed")
	}
	handler, err := s.getPaymentHandler(ctx, pm)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment handler",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get payment handler")
	}

	expirationDuration := handler.ExpirationDuration()

	order, sendEmail, err := s.repo.Order().CreateOrder(ctx, orderNew, receivePromo, time.Now().Add(expirationDuration))
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

	pi, err := s.getInvoiceByPaymentMethod(ctx, handler, order.UUID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order invoice", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get order invoice")
	}

	eos, ok := cache.GetOrderStatusById(order.OrderStatusId)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve order status")
		return nil, status.Errorf(codes.Internal, "internal error")
	}

	os, ok := dto.ConvertEntityToPbOrderStatus(eos.Status.Name)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to convert order status")
		return nil, status.Errorf(codes.Internal, "internal error")
	}

	pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
	}
	pbPi.PaymentMethod = req.Order.PaymentMethod

	return &pb_frontend.SubmitOrderResponse{
		OrderUuid:   order.UUID,
		OrderStatus: os,
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
			return nil, status.Errorf(codes.NotFound, "order not found")
		}
		return nil, status.Errorf(codes.Internal, "can't get order by uuid")
	}

	os, ok := cache.GetOrderStatusById(o.Order.OrderStatusId)
	if !ok {
		return nil, status.Errorf(codes.Internal, "can't get order status by id")
	}

	if os.Status.Name == entity.AwaitingPayment {
		pm, ok := cache.GetPaymentMethodById(o.Payment.PaymentMethodID)
		if !ok {
			slog.Default().ErrorContext(ctx, "can't get payment method by id",
				slog.Any("paymentMethodId", o.Payment.PaymentMethodID),
			)
			return nil, status.Errorf(codes.Internal, "can't get payment method by id")
		}

		handler, err := s.getPaymentHandler(ctx, pm.Method.Name)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment handler",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get payment handler")
		}

		payment, err := handler.CheckForTransactions(ctx, o.Order.UUID, o.Payment)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't check for transactions",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't check for transactions")
		}

		o.Payment = *payment
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

	shipmentCarrier, scOk := cache.GetShipmentCarrierById(int(req.ShipmentCarrierId))
	if scOk && !shipmentCarrier.Allowed {
		slog.Default().ErrorContext(ctx, "shipment carrier not allowed",
			slog.Any("shipmentCarrier", shipmentCarrier),
		)
		return nil, status.Errorf(codes.PermissionDenied, "shipment carrier not allowed")
	}
	if scOk && shipmentCarrier.Allowed {
		oiv.Subtotal = oiv.SubtotalDecimal().Add(shipmentCarrier.PriceDecimal()).Round(2)
	}

	promo, ok := cache.GetPromoByCode(req.PromoCode)
	if ok && promo.Allowed && promo.FreeShipping && scOk {
		oiv.Subtotal = oiv.SubtotalDecimal().Sub(shipmentCarrier.PriceDecimal()).Round(2)
	}

	totalSale := oiv.SubtotalDecimal()
	if ok && promo.IsAllowed() {
		if !promo.Discount.Equals(decimal.Zero) {
			totalSale = totalSale.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}
	}

	return &pb_frontend.ValidateOrderItemsInsertResponse{
		ValidItems: pbOii,
		HasChanged: oiv.HasChanged,
		Subtotal:   &pb_decimal.Decimal{Value: oiv.SubtotalDecimal().String()},
		TotalSale:  &pb_decimal.Decimal{Value: totalSale.Round(2).String()},
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

	pme, ok := cache.GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "internal error")
	}
	if !pme.Method.Allowed {
		slog.Default().ErrorContext(ctx, "payment method not allowed")
		return nil, status.Errorf(codes.PermissionDenied, "payment method not allowed")
	}

	handler, err := s.getPaymentHandler(ctx, pm)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment handler",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get payment handler")
	}

	pi, err := s.getInvoiceByPaymentMethod(ctx, handler, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order invoice", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get order invoice")
	}

	pbPi, err := dto.ConvertEntityToPbPaymentInsert(pi)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
	}
	pbPi.PaymentMethod = req.PaymentMethod

	return &pb_frontend.GetOrderInvoiceResponse{
		Payment: pbPi,
	}, nil
}

func (s *Server) getInvoiceByPaymentMethod(ctx context.Context, handler dependency.Invoicer, orderUuid string) (*entity.PaymentInsert, error) {

	pi, err := handler.GetOrderInvoice(ctx, orderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order invoice", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get order invoice")
	}

	return pi, nil
}

func (s *Server) getPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	switch pm {
	case entity.USDT_TRON:
		return s.usdtTron, nil
	case entity.USDT_TRON_TEST:
		return s.usdtTronTestnet, nil
	case entity.CARD:
		return s.stripePayment, nil
	case entity.CARD_TEST:
		return s.stripePaymentTest, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}

func (s *Server) CancelOrderInvoice(ctx context.Context, req *pb_frontend.CancelOrderInvoiceRequest) (*pb_frontend.CancelOrderInvoiceResponse, error) {
	payment, err := s.repo.Order().ExpireOrderPayment(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't expire order payment",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't expire order payment")
	}
	pme, _ := cache.GetPaymentMethodById(payment.PaymentMethodID)

	slog.Default().DebugContext(ctx, "cancel order invoice",
		slog.Any("paymentMethod", pme),
	)

	switch pme.Method.Name {
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
			slog.Any("paymentMethod", pme.Method.Name),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel monitor payment")
	}

	return &pb_frontend.CancelOrderInvoiceResponse{}, nil
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
	afs, count, err := s.repo.Archive().GetArchivesPaged(ctx,
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
		Total:    int32(count),
	}, nil

}

func (s *Server) GetArchive(ctx context.Context, req *pb_frontend.GetArchiveRequest) (*pb_frontend.GetArchiveResponse, error) {
	af, err := s.repo.Archive().GetArchiveById(ctx, int(req.Id))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get archive by slug", slog.String("err", err.Error()))
		return nil, err
	}

	pbAf := dto.ConvertArchiveFullEntityToPb(af)

	return &pb_frontend.GetArchiveResponse{
		Archive: pbAf,
	}, nil
}
