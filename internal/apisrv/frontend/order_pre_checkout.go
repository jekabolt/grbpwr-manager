package frontend

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
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
			return nil, status.Error(codes.InvalidArgument, err.Error())
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

	freeShipping := false

	// Complimentary shipping: waive shipping when subtotal meets threshold (threshold=0 means disabled for that currency)
	complimentaryPrices := cache.GetComplimentaryShippingPrices()
	if threshold, ok := complimentaryPrices[strings.ToUpper(currency)]; ok && threshold.GreaterThan(decimal.Zero) {
		if oiv.SubtotalDecimal().GreaterThanOrEqual(threshold) {
			freeShipping = true
		}
	}

	effectiveShipmentPrice := shipmentPrice
	if freeShipping {
		effectiveShipmentPrice = decimal.Zero
	}

	// Apply promo discount if present
	promo, promoOk := cache.GetPromoByCode(req.PromoCode)
	if promoOk {
		decimalPlaces := dto.DecimalPlacesForCurrency(currency)
		promoTotal, promoFreeShipping := promo.CalculateTotalWithPromo(totalSale, effectiveShipmentPrice, decimalPlaces)
		totalSale = promoTotal
		if promoFreeShipping {
			freeShipping = true
			effectiveShipmentPrice = decimal.Zero
		}
	} else {
		// No promo — just add shipping
		totalSale = totalSale.Add(effectiveShipmentPrice)
	}

	response := &pb_frontend.ValidateOrderItemsInsertResponse{
		ValidItems:      pbOii,
		HasChanged:      oiv.HasChanged,
		Subtotal:        &pb_decimal.Decimal{Value: dto.RoundForCurrency(oiv.SubtotalDecimal(), currency).String()},
		TotalSale:       &pb_decimal.Decimal{Value: dto.RoundForCurrency(totalSale, currency).String()},
		ItemAdjustments: dto.ConvertEntityOrderItemAdjustmentsToPb(oiv.ItemAdjustments),
		FreeShipping:    freeShipping,
		ShippingPrice:   &pb_decimal.Decimal{Value: dto.RoundForCurrency(effectiveShipmentPrice, currency).String()},
	}

	if promoOk {
		response.Promo = dto.ConvertEntityPromoInsertToPb(promo.PromoCodeInsert)
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
			return nil, status.Errorf(codes.Internal, "payment unavailable")
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
			return nil, status.Errorf(codes.Internal, "failed to create payment intent")
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

// validateOrderItemsWithReservation validates order items while accounting for stock reservations
func (s *Server) validateOrderItemsWithReservation(ctx context.Context, items []entity.OrderItemInsert, currency string, sessionID string) (*entity.OrderItemValidation, error) {
	// First, get the standard validation
	oiv, err := s.repo.Order().ValidateOrderItemsInsert(ctx, items, currency)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to validate order items: %v", err)
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
