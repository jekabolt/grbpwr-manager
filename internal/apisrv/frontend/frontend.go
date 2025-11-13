package frontend

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	re                dependency.RevalidationService
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
	re dependency.RevalidationService,
) *Server {
	return &Server{
		repo:              r,
		mailer:            m,
		rates:             ra,
		usdtTron:          usdtTron,
		usdtTronTestnet:   usdtTronTestnet,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
	}
}

func (s *Server) GetHero(ctx context.Context, req *pb_frontend.GetHeroRequest) (*pb_frontend.GetHeroResponse, error) {
	// Use cached hero for default language since GetHeroRequest doesn't have language field
	heroFull := cache.GetHero()

	h, err := dto.ConvertEntityHeroFullToCommonWithTranslations(heroFull)
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
			Collections:      cache.GetCollections(),
			Genders:          cache.GetGenders(),
			Languages:        cache.GetLanguages(),
			SortFactors:      cache.GetSortFactors(),
			OrderFactors:     cache.GetOrderFactors(),
			SiteEnabled:      cache.GetSiteAvailability(),
			MaxOrderItems:    cache.GetMaxOrderItems(),
			BaseCurrency:     cache.GetBaseCurrency(),
			BigMenu:          cache.GetBigMenu(),
			Announce:         cache.GetAnnounce(),
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

	// Check for idempotency: if PaymentIntent ID is provided, check if order already exists
	if req.PaymentIntentId != "" && (pm == entity.CARD || pm == entity.CARD_TEST) {
		existingOrder, err := s.repo.Order().GetOrderByPaymentIntentId(ctx, req.PaymentIntentId)
		if err != nil {
			slog.Default().ErrorContext(ctx, "error checking for existing order",
				slog.String("err", err.Error()),
				slog.String("paymentIntentId", req.PaymentIntentId),
			)
			return nil, status.Errorf(codes.Internal, "error checking for existing order")
		}

		// If order already exists with this PaymentIntent, return it (idempotent)
		if existingOrder != nil {
			slog.Default().InfoContext(ctx, "returning existing order for PaymentIntent (idempotent)",
				slog.String("orderUUID", existingOrder.Order.UUID),
				slog.String("paymentIntentId", req.PaymentIntentId),
			)

			eos, ok := cache.GetOrderStatusById(existingOrder.Order.OrderStatusId)
			if !ok {
				slog.Default().ErrorContext(ctx, "failed to retrieve order status")
				return nil, status.Errorf(codes.Internal, "internal error")
			}

			os, ok := dto.ConvertEntityToPbOrderStatus(eos.Status.Name)
			if !ok {
				slog.Default().ErrorContext(ctx, "failed to convert order status")
				return nil, status.Errorf(codes.Internal, "internal error")
			}

			pbPi, err := dto.ConvertEntityToPbPaymentInsert(&existingOrder.Payment.PaymentInsert)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't convert entity payment insert to pb payment insert", slog.String("err", err.Error()))
				return nil, status.Errorf(codes.Internal, "can't convert entity payment insert to pb payment insert")
			}
			pbPi.PaymentMethod = req.Order.PaymentMethod

			return &pb_frontend.SubmitOrderResponse{
				OrderUuid:   existingOrder.Order.UUID,
				OrderStatus: os,
				Payment:     pbPi,
			}, nil
		}
	}

	order, sendEmail, err := s.repo.Order().CreateOrder(ctx, orderNew, receivePromo, time.Now().Add(expirationDuration))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	if sendEmail {
		err := s.mailer.QueueNewSubscriber(ctx, s.repo, orderNew.Buyer.Email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't queue new subscriber mail",
				slog.String("err", err.Error()),
			)
		}
	}

	var pi *entity.PaymentInsert

	// Handle pre-created PaymentIntent for card payments
	if req.PaymentIntentId != "" && (pm == entity.CARD || pm == entity.CARD_TEST) {
		// Update existing PaymentIntent with order details (using data we already have - no DB query!)
		err = handler.UpdatePaymentIntentWithOrderNew(ctx, req.PaymentIntentId, order.UUID, orderNew)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update payment intent with order", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't update payment intent with order")
		}

		// Associate PaymentIntent with order in database
		var orderFull *entity.OrderFull
		err = s.repo.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			orderFull, err = s.repo.Order().InsertFiatInvoice(ctx, order.UUID, req.PaymentIntentId, pme.Method, time.Now().Add(expirationDuration))
			if err != nil {
				return fmt.Errorf("can't insert fiat invoice: %w", err)
			}

			err = s.repo.Order().UpdateTotalPaymentCurrency(ctx, order.UUID, orderFull.Order.TotalPriceDecimal())
			if err != nil {
				return fmt.Errorf("can't update total payment currency: %w", err)
			}

			return nil
		})

		if err != nil {
			slog.Default().ErrorContext(ctx, "can't associate payment intent with order", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't associate payment intent with order")
		}

		pi = &orderFull.Payment.PaymentInsert

		// Start monitoring the payment
		handler.StartMonitoringPayment(ctx, order.UUID, orderFull.Payment)
	} else {
		// Legacy flow: create PaymentIntent/invoice during order submission
		pi, err = s.getInvoiceByPaymentMethod(ctx, handler, order.UUID)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get order invoice", slog.String("err", err.Error()))
			return nil, status.Errorf(codes.Internal, "can't get order invoice")
		}
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

	pids := make([]int, 0, len(orderNew.Items))
	for _, item := range orderNew.Items {
		pids = append(pids, int(item.ProductId))
	}

	// Revalidate cache asynchronously - no need to block the response
	go func() {
		revalidateCtx := context.Background() // Use background context to avoid cancellation when request completes
		if err := s.re.RevalidateAll(revalidateCtx, &dto.RevalidationData{
			Products: pids,
			Hero:     true,
		}); err != nil {
			slog.Default().ErrorContext(revalidateCtx, "async revalidation failed",
				slog.String("err", err.Error()),
				slog.String("orderUUID", order.UUID),
			)
		}
	}()

	return &pb_frontend.SubmitOrderResponse{
		OrderUuid:   order.UUID,
		OrderStatus: os,
		Payment:     pbPi,
	}, nil
}

func (s *Server) GetOrderByUUIDAndEmail(ctx context.Context, req *pb_frontend.GetOrderByUUIDAndEmailRequest) (*pb_frontend.GetOrderByUUIDAndEmailResponse, error) {
	email, err := base64.StdEncoding.DecodeString(req.B64Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't decode email",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't decode email")
	}

	o, err := s.repo.Order().GetOrderByUUIDAndEmail(ctx, req.OrderUuid, string(email))
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

	return &pb_frontend.GetOrderByUUIDAndEmailResponse{
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

	currency := req.Currency
	if currency == "" {
		slog.Default().ErrorContext(ctx, "currency is required")
		return nil, status.Errorf(codes.InvalidArgument, "currency is required")
	}

	oiv, err := s.repo.Order().ValidateOrderItemsInsert(ctx, itemsToInsert, currency)
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

	response := &pb_frontend.ValidateOrderItemsInsertResponse{
		ValidItems: pbOii,
		HasChanged: oiv.HasChanged,
		Subtotal:   &pb_decimal.Decimal{Value: oiv.SubtotalDecimal().String()},
		TotalSale:  &pb_decimal.Decimal{Value: totalSale.Round(2).String()},
		Promo:      dto.ConvertEntityPromoInsertToPb(promo.PromoCodeInsert),
	}

	// Create PaymentIntent if payment method is CARD
	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)
	if pm == entity.CARD || pm == entity.CARD_TEST {
		handler, err := s.getPaymentHandler(ctx, pm)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment handler",
				slog.String("err", err.Error()),
			)
			// Don't fail the validation, just log the error and return without client_secret
			return response, nil
		}

		// Use provided currency or fall back to base currency from cache
		currency := req.Currency
		if currency == "" {
			currency = cache.GetBaseCurrency()
		}

		// Validate currency
		currencyTicker, ok := dto.VerifyCurrencyTicker(currency)
		if !ok {
			slog.Default().ErrorContext(ctx, "invalid currency",
				slog.String("currency", currency),
			)
			return response, nil
		}

		// Create pre-order PaymentIntent
		pi, err := handler.CreatePreOrderPaymentIntent(ctx, totalSale.Round(2), currencyTicker.String(), req.Country)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't create pre-order payment intent",
				slog.String("err", err.Error()),
			)
			// Don't fail the validation, just log the error and return without client_secret
			return response, nil
		}

		response.ClientSecret = pi.ClientSecret
		response.PaymentIntentId = pi.ID
	}

	return response, nil

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

func (s *Server) CancelOrderByUser(ctx context.Context, req *pb_frontend.CancelOrderByUserRequest) (*pb_frontend.CancelOrderByUserResponse, error) {
	// Decode base64 email
	email, err := base64.StdEncoding.DecodeString(req.B64Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't decode email",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "can't decode email")
	}

	// Validate reason
	if req.Reason == "" {
		return nil, status.Errorf(codes.InvalidArgument, "reason is required")
	}

	// Cancel order by user
	orderFull, err := s.repo.Order().CancelOrderByUser(ctx, req.OrderUuid, string(email), req.Reason)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel order by user",
			slog.String("err", err.Error()),
			slog.String("orderUuid", req.OrderUuid),
			slog.String("email", string(email)),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel order: %v", err)
	}

	// Convert entity to protobuf
	pbOrder, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert order to protobuf",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't convert order")
	}

	// Send refund initiated email if order status is RefundInProgress or PendingReturn
	orderStatus, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if ok && (orderStatus.Status.Name == entity.RefundInProgress || orderStatus.Status.Name == entity.PendingReturn) {
		refundDetails := dto.OrderFullToOrderRefundInitiated(orderFull)
		err = s.mailer.SendRefundInitiated(ctx, s.repo, orderFull.Buyer.Email, refundDetails)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't send refund initiated email",
				slog.String("err", err.Error()),
				slog.String("orderUuid", req.OrderUuid),
			)
			// Don't fail the cancellation if email fails
		}
	}

	slog.Default().InfoContext(ctx, "order cancelled by user",
		slog.String("orderUuid", req.OrderUuid),
		slog.String("email", string(email)),
		slog.String("reason", req.Reason),
	)

	return &pb_frontend.CancelOrderByUserResponse{
		Order: pbOrder,
	}, nil
}

func (s *Server) SubscribeNewsletter(ctx context.Context, req *pb_frontend.SubscribeNewsletterRequest) (*pb_frontend.SubscribeNewsletterResponse, error) {
	// Subscribe the user.
	isSubscribed, err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, true)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't subscribe", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.AlreadyExists, "can't subscribe")
	}
	slog.Default().DebugContext(ctx, "isSubscribed", slog.Bool("isSubscribed", isSubscribed))

	// Send new subscriber mail.
	if !isSubscribed {
		err = s.mailer.SendNewSubscriber(ctx, s.repo, req.Email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't send new subscriber mail",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't send new subscriber mail")
		}
	}

	return &pb_frontend.SubscribeNewsletterResponse{}, nil
}

func (s *Server) UnsubscribeNewsletter(ctx context.Context, req *pb_frontend.UnsubscribeNewsletterRequest) (*pb_frontend.UnsubscribeNewsletterResponse, error) {
	_, err := s.repo.Subscribers().UpsertSubscription(ctx, req.Email, false)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't unsubscribe",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't unsubscribe")
	}
	return &pb_frontend.UnsubscribeNewsletterResponse{}, nil
}

func (s *Server) NotifyMe(ctx context.Context, req *pb_frontend.NotifyMeRequest) (*pb_frontend.NotifyMeResponse, error) {
	productId := int(req.ProductId)
	sizeId := int(req.SizeId)
	email := req.Email

	// Validate email
	if email == "" {
		return nil, status.Errorf(codes.InvalidArgument, "email is required")
	}

	// Validate product exists and is not hidden/deleted
	_, err := s.repo.Products().GetProductByIdNoHidden(ctx, productId)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "product not found")
		}
		slog.Default().ErrorContext(ctx, "can't get product",
			slog.String("err", err.Error()),
			slog.Int("productId", productId),
		)
		return nil, status.Errorf(codes.Internal, "can't get product")
	}

	// Validate size exists
	_, ok := cache.GetSizeById(sizeId)
	if !ok {
		return nil, status.Errorf(codes.InvalidArgument, "invalid size_id")
	}

	// Add to waitlist (this also ensures subscriber exists)
	err = s.repo.Products().AddToWaitlist(ctx, productId, sizeId, email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add to waitlist",
			slog.String("err", err.Error()),
			slog.String("email", email),
			slog.Int("productId", productId),
			slog.Int("sizeId", sizeId),
		)
		return nil, status.Errorf(codes.Internal, "can't add to waitlist")
	}

	slog.Default().InfoContext(ctx, "added to waitlist",
		slog.String("email", email),
		slog.Int("productId", productId),
		slog.Int("sizeId", sizeId),
	)

	return &pb_frontend.NotifyMeResponse{}, nil
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

	pbAfs := make([]*pb_common.ArchiveList, 0, len(afs))

	for _, af := range afs {
		pbAfs = append(pbAfs, dto.ConvertEntityToCommonArchiveList(&af))
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

func (s *Server) SubmitSupportTicket(ctx context.Context, req *pb_frontend.SubmitSupportTicketRequest) (*pb_frontend.SubmitSupportTicketResponse, error) {
	err := s.repo.Support().SubmitTicket(ctx, dto.ConvertPbSupportTicketInsertToEntity(req.Ticket))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create support ticket", slog.String("err", err.Error()))
		return nil, err
	}

	return &pb_frontend.SubmitSupportTicketResponse{}, nil
}
