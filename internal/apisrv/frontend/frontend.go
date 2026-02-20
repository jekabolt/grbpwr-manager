package frontend

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"sort"
	"time"

	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/stockreserve"
	"github.com/jekabolt/grbpwr-manager/internal/store"
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
	mailer            dependency.Mailer
	stripePayment     dependency.Invoicer
	stripePaymentTest dependency.Invoicer
	re                dependency.RevalidationService
	rateLimiter       *ratelimit.MultiKeyLimiter
	reservationMgr    *stockreserve.Manager
}

// New creates a new server with frontend handlers.
func New(
	r dependency.Repository,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
) *Server {
	reservationMgr := stockreserve.NewDefaultManager()
	
	// Set reservation manager on stripe processors if they support it
	if sp, ok := stripePayment.(interface{ SetReservationManager(dependency.StockReservationManager) }); ok {
		sp.SetReservationManager(reservationMgr)
	}
	if spt, ok := stripePaymentTest.(interface{ SetReservationManager(dependency.StockReservationManager) }); ok {
		spt.SetReservationManager(reservationMgr)
	}
	
	return &Server{
		repo:              r,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		rateLimiter:       ratelimit.NewMultiKeyLimiter(),
		reservationMgr:    reservationMgr,
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
			Categories:             cache.GetCategories(),
			Measurements:           cache.GetMeasurements(),
			OrderStatuses:          cache.GetOrderStatuses(),
			PaymentMethods:         cache.GetPaymentMethods(),
			ShipmentCarriers:       cache.GetShipmentCarriers(),
			Sizes:                  cache.GetSizes(),
			Collections:            cache.GetCollections(),
			Genders:                cache.GetGenders(),
			Languages:              cache.GetLanguages(),
			SortFactors:            cache.GetSortFactors(),
			OrderFactors:           cache.GetOrderFactors(),
			SiteEnabled:            cache.GetSiteAvailability(),
			MaxOrderItems:          cache.GetMaxOrderItems(),
			BaseCurrency:           cache.GetBaseCurrency(),
			BigMenu:                cache.GetBigMenu(),
			Announce:               cache.GetAnnounce(),
			OrderExpirationSeconds: cache.GetOrderExpirationSeconds(),
		}),
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

	// Validate: price sorting requires currency to be specified
	var priceSortRequested bool
	for _, sf := range sfs {
		if sf == entity.Price {
			priceSortRequested = true
			break
		}
	}

	if priceSortRequested && (fc == nil || fc.Currency == "") {
		slog.Default().WarnContext(ctx, "price sorting requires currency",
			slog.String("err", "price sorting requires currency to be specified in filter conditions"),
		)
		return nil, status.Errorf(codes.InvalidArgument, "price sorting requires currency to be specified in filter conditions")
	}

	prds, count, err := s.repo.Products().GetProductsPaged(ctx, int(req.Limit), int(req.Offset), sfs, of, fc, false)
	if err != nil {
		// Check if it's a validation error (should return 4xx, not 5xx)
		if err.Error() == "price sorting requires currency to be specified in filter conditions" {
			slog.Default().WarnContext(ctx, "price sorting requires currency",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.InvalidArgument, err.Error())
		}
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
	slog.Default().InfoContext(ctx, "SubmitOrder started",
		slog.String("payment_intent_id", req.PaymentIntentId),
		slog.String("payment_method", string(dto.ConvertPbPaymentMethodToEntity(req.Order.PaymentMethod))),
	)
	// Extract client identifiers for rate limiting
	clientIP := middleware.GetClientIP(ctx)
	clientSession := middleware.GetClientSession(ctx)

	orderNew, receivePromo := dto.ConvertCommonOrderNewToEntity(req.Order)

	_, err := v.ValidateStruct(orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, fmt.Errorf("validation order create request failed: %v", err).Error())
	}

	// RATE LIMIT CHECK: Prevent cart bombing and order spam
	if err := s.rateLimiter.CheckOrderCreation(clientIP, orderNew.Buyer.Email); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for order creation",
			slog.String("ip", clientIP),
			slog.String("email", orderNew.Buyer.Email),
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.ResourceExhausted, err.Error())
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

	// Enforce PaymentIntent flow for card: prevents duplicate orders on retry (idempotency)
	if (pm == entity.CARD || pm == entity.CARD_TEST) && req.PaymentIntentId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "payment_intent_id required for card payments; call ValidateOrderItemsInsert first")
	}

	handler, err := s.getPaymentHandler(ctx, pm)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment handler",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get payment handler")
	}

	expirationDuration := s.getOrderExpirationDuration(handler)

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

			// Ensure PaymentIntent amount matches order total (order may have been updated on ErrOrderItemsUpdated)
			if err := s.ensurePaymentIntentAmountMatchesOrder(ctx, handler, req.PaymentIntentId, existingOrder); err != nil {
				slog.Default().ErrorContext(ctx, "can't sync payment intent amount on idempotent retry", slog.String("err", err.Error()))
				return nil, status.Errorf(codes.Internal, "can't sync payment intent amount: %v", err)
			}

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

	// COMMIT RESERVATION: Convert cart reservation to order reservation
	s.reservationMgr.Commit(ctx, clientSession, order.UUID)

	if sendEmail {
		err := s.mailer.QueueNewSubscriber(ctx, s.repo, orderNew.Buyer.Email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't queue new subscriber mail",
				slog.String("err", err.Error()),
			)
		}
	}

	// Associate PaymentIntent with order BEFORE InsertFiatInvoice so retries find the order
	// when InsertFiatInvoice returns ErrOrderItemsUpdated (prevents duplicate orders)
	if err = s.repo.Order().AssociatePaymentIntentWithOrder(ctx, order.UUID, req.PaymentIntentId); err != nil {
		slog.Default().ErrorContext(ctx, "can't associate payment intent with order",
			slog.String("err", err.Error()),
			slog.String("order_uuid", order.UUID),
			slog.String("payment_intent_id", req.PaymentIntentId),
		)
		_ = s.repo.Order().CancelOrder(ctx, order.UUID)
		s.reservationMgr.Release(ctx, order.UUID) // Release reservation on failure
		return nil, status.Errorf(codes.Internal, "can't associate payment intent with order")
	}

	// Update existing PaymentIntent with order details (using data we already have - no DB query!)
	err = handler.UpdatePaymentIntentWithOrderNew(ctx, req.PaymentIntentId, order.UUID, orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update payment intent with order",
			slog.String("err", err.Error()),
			slog.String("order_uuid", order.UUID),
		)
		_ = s.repo.Order().CancelOrder(ctx, order.UUID)
		return nil, status.Errorf(codes.Internal, "can't update payment intent with order")
	}

	var orderFull *entity.OrderFull
	orderFull, err = s.repo.Order().InsertFiatInvoice(ctx, order.UUID, req.PaymentIntentId, pme.Method, time.Now().Add(expirationDuration))
	if err != nil {
		if errors.Is(err, store.ErrOrderItemsUpdated) {
			// Order items were updated (stock/price changed). Update PaymentIntent amount and retry.
			orderFull, err = s.retryInsertFiatInvoiceAfterItemsUpdated(ctx, order.UUID, req.PaymentIntentId, pme.Method, expirationDuration, handler)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't complete payment after items update", slog.String("err", err.Error()))
				return nil, status.Errorf(codes.Internal, "can't complete payment after items update: %v", err)
			}
		} else {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice failed, cancelling order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", order.UUID),
			)
			if cancelErr := s.repo.Order().CancelOrder(ctx, order.UUID); cancelErr != nil {
				slog.Default().ErrorContext(ctx, "failed to cancel orphan order", slog.String("orderUUID", order.UUID), slog.String("err", cancelErr.Error()))
			}
			return nil, status.Errorf(codes.Internal, "can't associate payment intent with order")
		}
	}

	// start monitoring immediately after InsertFiatInvoice succeeds
	// to prevent orphaned orders if subsequent operations fail
	handler.StartMonitoringPayment(ctx, order.UUID, orderFull.Payment)

	err = s.repo.Order().UpdateTotalPaymentCurrency(ctx, order.UUID, orderFull.Order.TotalPriceDecimal())
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update total payment currency", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't update total payment currency")
	}

	// Validate and update PaymentIntent amount to match final order total (including delivery)
	stripePi, err := handler.GetPaymentIntentByID(ctx, req.PaymentIntentId)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment intent for validation", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get payment intent for validation")
	}

	// Convert PaymentIntent amount from smallest currency unit to decimal
	// For zero-decimal currencies (like JPY, KRW), amount is already in decimal
	// For other currencies, convert from cents
	piAmount := stripe.AmountFromSmallestUnit(stripePi.Amount, string(stripePi.Currency))
	orderTotal := orderFull.Order.TotalPriceDecimal()

	// Check if amounts match (with small tolerance for rounding)
	if !piAmount.Equal(orderTotal) {
		slog.Default().InfoContext(ctx, "PaymentIntent amount mismatch, updating",
			slog.String("payment_intent_id", req.PaymentIntentId),
			slog.String("pi_amount", piAmount.String()),
			slog.String("order_total", orderTotal.String()),
		)

		// Update PaymentIntent amount to match order total
		err = handler.UpdatePaymentIntentAmount(ctx, req.PaymentIntentId, orderTotal, orderFull.Order.Currency)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update payment intent amount",
				slog.String("err", err.Error()),
				slog.String("payment_intent_id", req.PaymentIntentId),
				slog.String("expected_amount", orderTotal.String()),
			)
			return nil, status.Errorf(codes.Internal, "payment amount mismatch: expected %s but PaymentIntent has %s", orderTotal.String(), piAmount.String())
		}

		slog.Default().InfoContext(ctx, "PaymentIntent amount updated successfully",
			slog.String("payment_intent_id", req.PaymentIntentId),
			slog.String("new_amount", orderTotal.String()),
		)
	}

	pi := &orderFull.Payment.PaymentInsert

	// Fetch order from DB for response (status may have changed to AwaitingPayment after InsertFiatInvoice)
	orderForResponse, err := s.repo.Order().GetOrderByUUID(ctx, order.UUID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order for response", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't get order for response")
	}

	eos, ok := cache.GetOrderStatusById(orderForResponse.OrderStatusId)
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
				slog.String("orderUUID", orderForResponse.UUID),
			)
		}
	}()

	return &pb_frontend.SubmitOrderResponse{
		OrderUuid:   orderForResponse.UUID,
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
	// Extract client identifiers for rate limiting and stock reservation
	clientIP := middleware.GetClientIP(ctx)
	clientSession := middleware.GetClientSession(ctx)

	// RATE LIMIT CHECK: Prevent validation spam
	if err := s.rateLimiter.CheckValidation(clientIP); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for validation",
			slog.String("ip", clientIP),
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.ResourceExhausted, err.Error())
	}

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

	// Validate with stock reservation awareness
	oiv, err := s.validateOrderItemsWithReservation(ctx, itemsToInsert, currency, clientSession)
	if err != nil {
		// Check if it's a validation error (should return 4xx, not 5xx)
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			slog.Default().WarnContext(ctx, "validation failed for order items",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.InvalidArgument, err.Error())
		}
		slog.Default().ErrorContext(ctx, "can't validate order items insert",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't validate order items insert")
	}
	totalSale := oiv.SubtotalDecimal()

	pbOii := make([]*pb_common.OrderItem, 0, len(oiv.ValidItems))
	for _, i := range oiv.ValidItems {
		pbOii = append(pbOii, dto.ConvertEntityOrderItemToPb(&i, currency))
	}

	shipmentCarrier, scOk := cache.GetShipmentCarrierById(int(req.ShipmentCarrierId))
	if scOk && !shipmentCarrier.Allowed {
		slog.Default().ErrorContext(ctx, "shipment carrier not allowed",
			slog.Any("shipmentCarrier", shipmentCarrier),
		)
		return nil, status.Errorf(codes.PermissionDenied, "shipment carrier not allowed")
	}
	// Geo restriction: if carrier has allowed regions and we have a country, verify the region
	if scOk && req.Country != "" && len(shipmentCarrier.AllowedRegions) > 0 {
		region, ok := entity.CountryToRegion(req.Country)
		if !ok {
			return nil, status.Errorf(codes.InvalidArgument, "shipping country %s could not be mapped to a region", req.Country)
		}
		if !shipmentCarrier.AvailableForRegion(region) {
			return nil, status.Errorf(codes.PermissionDenied, "shipment carrier does not serve region %s", region)
		}
	}

	var shipmentPrice decimal.Decimal
	if scOk && shipmentCarrier.Allowed {
		shipmentPrice, err = shipmentCarrier.PriceDecimal(currency)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get shipment carrier price",
				slog.String("currency", currency),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get shipment carrier price for currency %s", currency)
		}
	}

	totalSale = totalSale.Add(shipmentPrice)
	promo, ok := cache.GetPromoByCode(req.PromoCode)
	if ok && promo.Allowed && promo.FreeShipping && scOk {
		totalSale = totalSale.Sub(shipmentPrice)
	}

	if ok && promo.IsAllowed() {
		if !promo.Discount.Equals(decimal.Zero) {
			totalSale = totalSale.Mul(decimal.NewFromInt(100).Sub(promo.Discount).Div(decimal.NewFromInt(100)))
		}
	}

	response := &pb_frontend.ValidateOrderItemsInsertResponse{
		ValidItems:      pbOii,
		HasChanged:      oiv.HasChanged,
		Subtotal:        &pb_decimal.Decimal{Value: dto.RoundForCurrency(oiv.SubtotalDecimal(), currency).String()},
		TotalSale:       &pb_decimal.Decimal{Value: dto.RoundForCurrency(totalSale, currency).String()},
		Promo:           dto.ConvertEntityPromoInsertToPb(promo.PromoCodeInsert),
		ItemAdjustments: dto.ConvertEntityOrderItemAdjustmentsToPb(oiv.ItemAdjustments),
	}

	// Create PaymentIntent if payment method is CARD
	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)
	if pm == entity.CARD || pm == entity.CARD_TEST {
		handler, err := s.getPaymentHandler(ctx, pm)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment handler for validate-items",
				slog.String("payment_method", string(pm)),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "payment unavailable: %s", err.Error())
		}

		// Use provided currency or fall back to base currency from cache
		currency := req.Currency
		if currency == "" {
			currency = cache.GetBaseCurrency()
		}

		// Validate total meets Stripe minimum before creating PaymentIntent
		roundedTotal := dto.RoundForCurrency(totalSale, currency)
		if err := dto.ValidatePriceMeetsMinimum(roundedTotal, currency); err != nil {
			slog.Default().WarnContext(ctx, "total below currency minimum, card payment unavailable",
				slog.String("currency", currency),
				slog.String("total", roundedTotal.String()),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.InvalidArgument, "total below currency minimum for card payment: %s", err.Error())
		}

		// Cart fingerprint for session matching (same cart + same client = same session)
		cartFingerprint := cartFingerprintForPreOrder(roundedTotal, currency, req.Country, req.PromoCode, req.ShipmentCarrierId, itemsToInsert, clientSession)
		pi, rotatedKey, err := handler.GetOrCreatePreOrderPaymentIntent(ctx, req.IdempotencyKey, roundedTotal, currency, req.Country, cartFingerprint)
		if err != nil {
			if errors.Is(err, stripe.ErrPaymentAlreadyCompleted) {
				return nil, status.Errorf(codes.InvalidArgument, "Payment already completed for this session. Please clear your checkout and start a new order.")
			}
			slog.Default().ErrorContext(ctx, "can't get or create pre-order payment intent",
				slog.String("payment_method", string(pm)),
				slog.String("currency", currency),
				slog.String("total", roundedTotal.String()),
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "failed to create payment intent: %s", err.Error())
		}

		if pi.ClientSecret == "" {
			slog.Default().ErrorContext(ctx, "Stripe returned PaymentIntent without ClientSecret")
			return nil, status.Errorf(codes.Internal, "payment unavailable: missing client secret")
		}

		response.ClientSecret = pi.ClientSecret
		response.PaymentIntentId = pi.ID
		if rotatedKey != "" {
			response.IdempotencyKey = rotatedKey // New session or rotated (expired)
		} else {
			response.IdempotencyKey = req.IdempotencyKey // Same valid session
		}
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
		return nil, err
	}
	return pi, nil
}

func (s *Server) getPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, error) {
	switch pm {
	case entity.CARD:
		return s.stripePayment, nil
	case entity.CARD_TEST:
		return s.stripePaymentTest, nil
	default:
		return nil, status.Errorf(codes.Unimplemented, "payment method unimplemented")
	}
}

// cartFingerprintForPreOrder returns a deterministic hash of cart contents and client identity for session matching.
// Same cart + same client = same fingerprint. Includes clientSession so different clients with identical carts get different sessions.
func cartFingerprintForPreOrder(amount decimal.Decimal, currency, country, promoCode string, shipmentCarrierId int32, items []entity.OrderItemInsert, clientSession string) string {
	sorted := make([]entity.OrderItemInsert, len(items))
	copy(sorted, items)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ProductId != sorted[j].ProductId {
			return sorted[i].ProductId < sorted[j].ProductId
		}
		return sorted[i].SizeId < sorted[j].SizeId
	})
	data := fmt.Sprintf("%s|%s|%s|%s|%d|%s", amount.String(), currency, country, promoCode, shipmentCarrierId, clientSession)
	for _, i := range sorted {
		data += fmt.Sprintf("|%d:%d:%s", i.ProductId, i.SizeId, i.Quantity.String())
	}
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

// ensurePaymentIntentAmountMatchesOrder verifies the PaymentIntent amount matches the order total.
// If not (e.g. order was updated on ErrOrderItemsUpdated), updates the PaymentIntent before the client pays.
func (s *Server) ensurePaymentIntentAmountMatchesOrder(ctx context.Context, handler dependency.Invoicer, paymentIntentId string, orderFull *entity.OrderFull) error {
	stripePi, err := handler.GetPaymentIntentByID(ctx, paymentIntentId)
	if err != nil {
		return fmt.Errorf("get payment intent: %w", err)
	}
	piAmount := stripe.AmountFromSmallestUnit(stripePi.Amount, string(stripePi.Currency))
	orderTotal := orderFull.Order.TotalPriceDecimal()
	if piAmount.Equal(orderTotal) {
		return nil
	}
	slog.Default().InfoContext(ctx, "PaymentIntent amount mismatch on retry, updating",
		slog.String("payment_intent_id", paymentIntentId),
		slog.String("pi_amount", piAmount.String()),
		slog.String("order_total", orderTotal.String()),
	)
	return handler.UpdatePaymentIntentAmount(ctx, paymentIntentId, orderTotal, orderFull.Order.Currency)
}

// retryInsertFiatInvoiceAfterItemsUpdated is called when InsertFiatInvoice returns ErrOrderItemsUpdated.
// Order items were updated in DB (stock/price changed). We update the PaymentIntent amount to match
// the new order total, then retry InsertFiatInvoice (items now match, so it should succeed).
func (s *Server) retryInsertFiatInvoiceAfterItemsUpdated(ctx context.Context, orderUUID string, paymentIntentId string, pm entity.PaymentMethod, expirationDuration time.Duration, handler dependency.Invoicer) (*entity.OrderFull, error) {
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, orderUUID)
	if err != nil {
		return nil, fmt.Errorf("get updated order: %w", err)
	}

	orderTotal := orderFull.Order.TotalPriceDecimal()
	if err = handler.UpdatePaymentIntentAmount(ctx, paymentIntentId, orderTotal, orderFull.Order.Currency); err != nil {
		return nil, fmt.Errorf("update payment intent amount: %w", err)
	}

	orderFull, err = s.repo.Order().InsertFiatInvoice(ctx, orderUUID, paymentIntentId, pm, time.Now().Add(expirationDuration))
	if err != nil {
		return nil, fmt.Errorf("retry insert fiat invoice: %w", err)
	}
	return orderFull, nil
}

// getOrderExpirationDuration returns configurable expiration from settings, or handler default when not set.
func (s *Server) getOrderExpirationDuration(handler dependency.Invoicer) time.Duration {
	if sec := cache.GetOrderExpirationSeconds(); sec > 0 {
		return time.Duration(sec) * time.Second
	}
	return handler.ExpirationDuration()
}

func (s *Server) CancelOrderInvoice(ctx context.Context, req *pb_frontend.CancelOrderInvoiceRequest) (*pb_frontend.CancelOrderInvoiceResponse, error) {
	payment, err := s.repo.Order().ExpireOrderPayment(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't expire order payment",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't expire order payment")
	}

	// If payment is nil, the order was not in AwaitingPayment status, nothing to cancel
	if payment == nil {
		return &pb_frontend.CancelOrderInvoiceResponse{}, nil
	}

	// RELEASE RESERVATION: Free stock when order invoice is cancelled
	s.reservationMgr.Release(ctx, req.OrderUuid)

	pme, _ := cache.GetPaymentMethodById(payment.PaymentMethodID)

	handler, err := s.getPaymentHandler(ctx, pme.Method.Name)
	if err != nil {
		return nil, err
	}
	if err = handler.CancelMonitorPayment(req.OrderUuid); err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel monitor payment",
			slog.String("err", err.Error()),
			slog.Any("paymentMethod", pme.Method.Name),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel monitor payment")
	}
	return &pb_frontend.CancelOrderInvoiceResponse{}, nil
}

// isOrderEligibleForReturn checks if an order is eligible for return based on delivery date and order age
// Returns (eligible bool, reason string)
func isOrderEligibleForReturn(orderFull *entity.OrderFull, statusName entity.OrderStatusName) (bool, string) {
	const (
		maxDaysSinceDelivery = 14
		maxDaysSincePlaced   = 90
	)

	now := time.Now()

	// // Check if order was placed more than 60 days ago
	// daysSincePlaced := now.Sub(orderFull.Order.Placed).Hours() / 24
	// if daysSincePlaced > maxDaysSincePlaced {
	// 	return false, "order was placed more than 60 days ago and is no longer eligible for return"
	// }

	// If order is delivered, check if delivered more than 14 days ago
	if statusName == entity.Delivered {
		// Find when order was delivered from status history
		var deliveredAt time.Time
		for _, history := range orderFull.StatusHistory {
			if history.StatusName == entity.Delivered {
				deliveredAt = history.ChangedAt
				break
			}
		}

		if !deliveredAt.IsZero() {
			daysSinceDelivery := now.Sub(deliveredAt).Hours() / 24
			if daysSinceDelivery > maxDaysSinceDelivery {
				return false, "order was delivered more than 14 days ago and is no longer eligible for return"
			}
		}
	}

	return true, ""
}

func (s *Server) CancelOrderByUser(ctx context.Context, req *pb_frontend.CancelOrderByUserRequest) (*pb_frontend.CancelOrderByUserResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if req.OrderUuid == "" {
		return nil, status.Error(codes.InvalidArgument, "orderUuid is required")
	}
	if req.B64Email == "" {
		return nil, status.Error(codes.InvalidArgument, "email is required")
	}
	if req.Reason == "" {
		return nil, status.Error(codes.InvalidArgument, "reason is required")
	}

	emailBytes, err := base64.StdEncoding.DecodeString(req.B64Email)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't decode email",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Error(codes.InvalidArgument, "can't decode email")
	}
	email := string(emailBytes)

	orderFull, err := s.repo.Order().CancelOrderByUser(ctx, req.OrderUuid, email, req.Reason)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel order by user",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
			slog.String("email", email),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel order: %v", err)
	}

	// RELEASE RESERVATION: Free stock when order is cancelled
	s.reservationMgr.Release(ctx, req.OrderUuid)

	os, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		slog.Default().ErrorContext(ctx, "can't get order status by id",
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Error(codes.Internal, "can't get order status by id")
	}
	statusName := os.Status.Name

	sendRefundInitiatedEmail := false

	switch statusName {
	case entity.RefundInProgress, entity.PendingReturn, entity.Refunded, entity.PartiallyRefunded, entity.Cancelled:
		slog.Default().InfoContext(ctx, "order already in terminal/refund flow state",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("status", string(statusName)),
		)
		return nil, status.Error(codes.AlreadyExists, "order already in refund progress, pending return, refunded, partially refunded, or cancelled")

	case entity.Delivered, entity.Shipped:
		// Check if order is eligible for return based on time constraints
		eligible, reason := isOrderEligibleForReturn(orderFull, statusName)
		if !eligible {
			slog.Default().InfoContext(ctx, "order not eligible for return",
				slog.String("order_uuid", req.OrderUuid),
				slog.String("status", string(statusName)),
				slog.String("reason", reason),
			)
			return nil, status.Error(codes.FailedPrecondition, reason)
		}

		// Order is eligible - set status to PendingReturn
		if err := s.repo.Order().SetOrderStatusToPendingReturn(ctx, req.OrderUuid, "user"); err != nil {
			slog.Default().ErrorContext(ctx, "can't set order status to pending return",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't set order to pending return: %v", err)
		}

		// Refresh order to get updated status
		orderFull, err = s.repo.Order().GetOrderByUUIDAndEmail(ctx, req.OrderUuid, email)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't refresh order after status update",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't refresh order: %v", err)
		}

		sendRefundInitiatedEmail = true
		slog.Default().InfoContext(ctx, "order set to pending return by user",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("email", email),
			slog.String("reason", req.Reason),
		)

	case entity.Placed, entity.AwaitingPayment:
		if err := s.repo.Order().CancelOrder(ctx, req.OrderUuid); err != nil {
			slog.Default().ErrorContext(ctx, "can't cancel order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't cancel order: %v", err)
		}
		slog.Default().InfoContext(ctx, "order cancelled by user (no payment yet)",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("email", email),
			slog.String("reason", req.Reason),
		)
		return &pb_frontend.CancelOrderByUserResponse{Order: nil}, nil

	case entity.Confirmed:
		pm, ok := cache.GetPaymentMethodById(orderFull.Payment.PaymentMethodID)
		if !ok {
			slog.Default().ErrorContext(ctx, "can't get payment method by id",
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Error(codes.Internal, "can't get payment method by id")
		}

		switch pm.Method.Name {
		case entity.CARD, entity.CARD_TEST:
			pHandler, err := s.getPaymentHandler(ctx, pm.Method.Name)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't get payment handler",
					slog.String("err", err.Error()),
					slog.String("order_uuid", req.OrderUuid),
				)
				return nil, status.Error(codes.Internal, "can't get payment handler")
			}

			// Full refund for RefundInProgress (amount = nil)
			if err := pHandler.Refund(ctx, orderFull.Payment, req.OrderUuid, nil, orderFull.Order.Currency); err != nil {
				slog.Default().ErrorContext(ctx, "can't refund payment",
					slog.String("err", err.Error()),
					slog.String("order_uuid", req.OrderUuid),
				)
				return nil, status.Errorf(codes.Internal, "can't refund payment: %v", err)
			}

			if err := s.repo.Order().RefundOrder(ctx, req.OrderUuid, nil, req.Reason); err != nil {
				slog.Default().ErrorContext(ctx, "can't refund order",
					slog.String("err", err.Error()),
					slog.String("order_uuid", req.OrderUuid),
				)
				return nil, status.Errorf(codes.Internal, "can't refund order: %v", err)
			}

			sendRefundInitiatedEmail = true

		default:
			// Keep order cancellation request recorded; actual refund handling depends on payment method implementation.
			slog.InfoContext(ctx, "confirmed order cancellation requested; refund not auto-handled for payment method",
				slog.String("order_uuid", req.OrderUuid),
				slog.String("payment_method", string(pm.Method.Name)),
			)
		}

	default:
		return nil, status.Error(codes.FailedPrecondition, "order can't be cancelled in status: "+string(statusName))
	}

	if !sendRefundInitiatedEmail && (statusName == entity.RefundInProgress || statusName == entity.PendingReturn) {
		sendRefundInitiatedEmail = true
	}

	if sendRefundInitiatedEmail {
		refundDetails := dto.OrderFullToOrderRefundInitiated(orderFull)
		if err := s.mailer.SendRefundInitiated(ctx, s.repo, orderFull.Buyer.Email, refundDetails); err != nil {
			slog.Default().ErrorContext(ctx, "can't send refund initiated email",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
		}
	}

	pbOrder, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert order to protobuf",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Error(codes.Internal, "can't convert order")
	}

	slog.Default().InfoContext(ctx, "order cancelled by user",
		slog.String("order_uuid", req.OrderUuid),
		slog.String("email", email),
		slog.String("reason", req.Reason),
		slog.String("status", string(statusName)),
	)

	return &pb_frontend.CancelOrderByUserResponse{Order: pbOrder}, nil
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

// validateOrderItemsWithReservation validates order items while accounting for stock reservations
func (s *Server) validateOrderItemsWithReservation(ctx context.Context, items []entity.OrderItemInsert, currency string, sessionID string) (*entity.OrderItemValidation, error) {
	// First, get the standard validation
	oiv, err := s.repo.Order().ValidateOrderItemsInsert(ctx, items, currency)
	if err != nil {
		return nil, err
	}

	// Now apply stock reservation logic - check available stock minus other reservations
	adjustedItems := make([]entity.OrderItem, 0, len(oiv.ValidItems))
	additionalAdjustments := make([]entity.OrderItemAdjustment, 0)

	for _, item := range oiv.ValidItems {
		// Get current stock from database
		currentStock, exists, err := s.repo.Products().GetProductSizeStock(ctx, item.ProductId, item.SizeId)
		if err != nil || !exists {
			// If we can't get stock, skip this item
			additionalAdjustments = append(additionalAdjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: item.Quantity,
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}

		// Calculate available stock (total - reservations, excluding current session)
		availableStock := s.reservationMgr.GetAvailableStock(currentStock, item.ProductId, item.SizeId, sessionID)

		if availableStock.LessThanOrEqual(decimal.Zero) {
			// No stock available after accounting for reservations
			additionalAdjustments = append(additionalAdjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: item.Quantity,
				AdjustedQuantity:  decimal.Zero,
				Reason:            entity.AdjustmentReasonOutOfStock,
			})
			continue
		}

		if item.Quantity.GreaterThan(availableStock) {
			// Reduce quantity to available stock
			additionalAdjustments = append(additionalAdjustments, entity.OrderItemAdjustment{
				ProductId:         item.ProductId,
				SizeId:            item.SizeId,
				RequestedQuantity: item.Quantity,
				AdjustedQuantity:  availableStock,
				Reason:            entity.AdjustmentReasonQuantityReduced,
			})
			item.Quantity = availableStock
		}

		// Reserve the stock for this session
		if err := s.reservationMgr.Reserve(ctx, sessionID, item.ProductId, item.SizeId, item.Quantity); err != nil {
			slog.Default().WarnContext(ctx, "failed to reserve stock",
				slog.String("session_id", sessionID),
				slog.Int("product_id", item.ProductId),
				slog.Int("size_id", item.SizeId),
				slog.String("err", err.Error()),
			)
		}

		adjustedItems = append(adjustedItems, item)
	}

	// If we had additional adjustments, recalculate subtotal
	if len(additionalAdjustments) > 0 {
		oiv.ValidItems = adjustedItems
		oiv.ItemAdjustments = append(oiv.ItemAdjustments, additionalAdjustments...)
		oiv.HasChanged = true

		// Recalculate subtotal
		subtotal := decimal.Zero
		for _, item := range adjustedItems {
			itemTotal := item.ProductPriceWithSale.Mul(item.Quantity)
			subtotal = subtotal.Add(itemTotal)
		}
		oiv.Subtotal = subtotal
	}

	return oiv, nil
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
