package admin

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetOrderByUUID(ctx context.Context, req *pb_admin.GetOrderByUUIDRequest) (*pb_admin.GetOrderByUUIDResponse, error) {
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

	return &pb_admin.GetOrderByUUIDResponse{
		Order: oPb,
	}, nil
}

func (s *Server) SetTrackingNumber(ctx context.Context, req *pb_admin.SetTrackingNumberRequest) (*pb_admin.SetTrackingNumberResponse, error) {
	if req.TrackingCode == "" {
		slog.Default().ErrorContext(ctx, "tracking code is empty")
		return nil, status.Errorf(codes.InvalidArgument, "tracking code is empty")
	}

	_, err := s.repo.Order().SetTrackingNumber(ctx, req.OrderUuid, req.TrackingCode)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't update tracking number info",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't update shipping info")
	}

	// Get full order details for email
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order full by uuid",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order details")
	}

	shipmentDetails := dto.OrderFullToOrderShipment(orderFull)
	err = s.mailer.SendOrderShipped(ctx, s.repo, orderFull.Buyer.Email, shipmentDetails)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't send order shipped email",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't send order shipped email")
	}

	return &pb_admin.SetTrackingNumberResponse{}, nil
}

func (s *Server) ListOrders(ctx context.Context, req *pb_admin.ListOrdersRequest) (*pb_admin.ListOrdersResponse, error) {

	if req.Status < 0 {
		slog.Default().ErrorContext(ctx, "status is invalid")
		return nil, status.Errorf(codes.InvalidArgument, "status is invalid")
	}

	if req.PaymentMethod < 0 {
		slog.Default().ErrorContext(ctx, "payment method is invalid")
		return nil, status.Errorf(codes.InvalidArgument, "payment method is invalid")
	}

	orders, err := s.repo.Order().GetOrdersByStatusAndPaymentTypePaged(ctx,
		req.Email,
		req.OrderUuid,
		int(req.Status),
		cache.GetPaymentMethodIdByPbId(req.PaymentMethod),
		int(req.OrderId),
		int(req.Limit),
		int(req.Offset),
		dto.ConvertPBCommonOrderFactorToEntity(req.OrderFactor),
	)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get orders by status",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get orders by status")
	}

	ordersPb := make([]*pb_common.Order, 0, len(orders))
	for _, order := range orders {
		o, err := dto.ConvertEntityOrderToPbCommonOrder(order)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't convert entity order to pb common order",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't convert entity order to pb common order")
		}
		ordersPb = append(ordersPb, o)
	}
	return &pb_admin.ListOrdersResponse{
		Orders: ordersPb,
	}, nil
}

func (s *Server) RefundOrder(ctx context.Context, req *pb_admin.RefundOrderRequest) (*pb_admin.RefundOrderResponse, error) {
	orderFull, err := s.repo.Order().GetOrderFullByUUID(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get order for refund",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't get order")
	}

	orderStatus, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		slog.Default().ErrorContext(ctx, "can't get order status by id",
			slog.String("orderUuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.Internal, "can't get order status by id")
	}

	allowed := orderStatus.Status.Name == entity.RefundInProgress || orderStatus.Status.Name == entity.PendingReturn ||
		orderStatus.Status.Name == entity.Delivered || orderStatus.Status.Name == entity.Confirmed || orderStatus.Status.Name == entity.PartiallyRefunded
	if !allowed {
		return nil, status.Errorf(codes.InvalidArgument, "order status must be refund_in_progress, pending_return, delivered, confirmed or partially_refunded, got %s", orderStatus.Status.Name)
	}

	// Confirmed orders support only full refund
	if orderStatus.Status.Name == entity.Confirmed && len(req.OrderItemIds) > 0 {
		return nil, status.Errorf(codes.InvalidArgument, "confirmed orders support only full refund")
	}

	// Determine refund_shipping:
	// - For confirmed (not yet shipped) orders doing full refund: always include shipping
	// - For other statuses: use the request flag
	refundShipping := req.RefundShipping
	if orderStatus.Status.Name == entity.Confirmed && len(req.OrderItemIds) == 0 {
		// Full refund of not-yet-shipped order: always include shipping fee
		refundShipping = true
	}

	// Stripe refund for Stripe payment methods (CARD / CARD_TEST)
	pm, ok := cache.GetPaymentMethodById(orderFull.Payment.PaymentMethodID)
	if ok && (pm.Method.Name == entity.CARD || pm.Method.Name == entity.CARD_TEST) {
		handler, err := s.getPaymentHandler(ctx, pm.Method.Name)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't get payment handler for refund",
				slog.String("err", err.Error()),
			)
			return nil, status.Errorf(codes.Internal, "can't get payment handler")
		}

		// Calculate refund amount for Stripe
		var refundAmount *decimal.Decimal
		if orderStatus.Status.Name == entity.Confirmed && len(req.OrderItemIds) == 0 {
			// Full refund for Confirmed: nil = full refund on Stripe (includes everything)
			refundAmount = nil
		} else if len(req.OrderItemIds) == 0 {
			// Full refund for other statuses: calculate total items + optional shipping
			amount := calculateFullRefundAmount(orderFull, refundShipping)
			refundAmount = &amount
		} else {
			// Partial refund: calculate from specified items + optional shipping
			amount := calculateRefundAmount(orderFull.OrderItems, req.OrderItemIds, orderFull.Order.Currency)
			if refundShipping && !orderFull.Shipment.FreeShipping {
				amount = amount.Add(orderFull.Shipment.CostDecimal(orderFull.Order.Currency))
			}
			refundAmount = &amount
		}

		if err := handler.Refund(ctx, orderFull.Payment, req.OrderUuid, refundAmount, orderFull.Order.Currency); err != nil {
			slog.Default().ErrorContext(ctx, "stripe refund failed",
				slog.String("err", err.Error()),
				slog.String("orderUuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "stripe refund failed: %v", err)
		}
	}

	err = s.repo.Order().RefundOrder(ctx, req.OrderUuid, req.OrderItemIds, req.Reason, refundShipping)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't refund order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't refund order")
	}
	return &pb_admin.RefundOrderResponse{}, nil
}

func (s *Server) DeliveredOrder(ctx context.Context, req *pb_admin.DeliveredOrderRequest) (*pb_admin.DeliveredOrderResponse, error) {
	err := s.repo.Order().DeliveredOrder(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't mark order as delivered",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't mark order as delivered")
	}
	return &pb_admin.DeliveredOrderResponse{}, nil
}

func (s *Server) CancelOrder(ctx context.Context, req *pb_admin.CancelOrderRequest) (*pb_admin.CancelOrderResponse, error) {
	err := s.repo.Order().CancelOrder(ctx, req.OrderUuid)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't cancel order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't cancel order")
	}
	if s.reservationMgr != nil {
		s.reservationMgr.Release(ctx, req.OrderUuid)
	}
	return &pb_admin.CancelOrderResponse{}, nil
}

func (s *Server) AddOrderComment(ctx context.Context, req *pb_admin.AddOrderCommentRequest) (*pb_admin.AddOrderCommentResponse, error) {
	// Validate comment
	if req.Comment == "" {
		return nil, status.Errorf(codes.InvalidArgument, "comment is required")
	}

	err := s.repo.Order().AddOrderComment(ctx, req.OrderUuid, req.Comment)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't add order comment",
			slog.String("err", err.Error()),
			slog.String("orderUuid", req.OrderUuid),
			slog.String("comment", req.Comment),
		)
		return nil, status.Errorf(codes.Internal, "can't add order comment: %v", err)
	}

	slog.Default().InfoContext(ctx, "order comment added",
		slog.String("orderUuid", req.OrderUuid),
		slog.String("comment", req.Comment),
	)

	return &pb_admin.AddOrderCommentResponse{}, nil
}

func (s *Server) CreateCustomOrder(ctx context.Context, req *pb_admin.CreateCustomOrderRequest) (*pb_admin.CreateCustomOrderResponse, error) {
	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)
	if pm != entity.BANK_INVOICE && pm != entity.CASH {
		return nil, status.Errorf(codes.InvalidArgument, "payment method must be bank_invoice or cash for custom orders")
	}
	orderNew, err := dto.ConvertCreateCustomOrderRequestToEntity(req)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid request: %v", err)
	}
	if _, err := v.ValidateStruct(orderNew); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "validation failed: %v", err)
	}
	order, err := s.repo.Order().CreateCustomOrder(ctx, orderNew)
	if err != nil {
		var validationErr *entity.ValidationError
		if errors.As(err, &validationErr) {
			return nil, status.Errorf(codes.InvalidArgument, "%s", validationErr.Message)
		}
		slog.Default().ErrorContext(ctx, "can't create custom order", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't create custom order: %v", err)
	}
	orderPb, err := dto.ConvertEntityOrderToPbCommonOrder(*order)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert order to proto", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't convert order: %v", err)
	}
	return &pb_admin.CreateCustomOrderResponse{Order: orderPb}, nil
}

// calculateRefundAmount calculates the total refund amount based on the specified order item IDs.
// Each occurrence of an ID in orderItemIds represents 1 unit to refund.
// Uses currency-aware rounding (0 for KRW/JPY, 2 for EUR/USD).
func calculateRefundAmount(orderItems []entity.OrderItem, orderItemIds []int32, currency string) decimal.Decimal {
	itemByID := make(map[int]entity.OrderItem)
	for _, item := range orderItems {
		itemByID[item.Id] = item
	}

	var total decimal.Decimal
	for _, id := range orderItemIds {
		item, ok := itemByID[int(id)]
		if ok {
			// Each occurrence = 1 unit, use ProductPriceWithSale for the refund amount
			total = total.Add(item.ProductPriceWithSale)
		}
	}
	return dto.RoundForCurrency(total, currency)
}

// calculateFullRefundAmount calculates the total refund amount for a full refund (all items + optional shipping).
// Used when doing a full refund on non-confirmed orders where we need an explicit amount for Stripe.
func calculateFullRefundAmount(orderFull *entity.OrderFull, includeShipping bool) decimal.Decimal {
	var total decimal.Decimal
	for _, item := range orderFull.OrderItems {
		total = total.Add(item.ProductPriceWithSale.Mul(item.Quantity))
	}
	if includeShipping && !orderFull.Shipment.FreeShipping {
		total = total.Add(orderFull.Shipment.CostDecimal(orderFull.Order.Currency))
	}
	return dto.RoundForCurrency(total, orderFull.Order.Currency)
}
