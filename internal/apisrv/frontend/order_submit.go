package frontend

import (
	"context"
	"errors"
	"log/slog"
	"time"

	v "github.com/asaskevich/govalidator"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/payment/stripe"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/store"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// existingOrderResponse builds the idempotent SubmitOrder response for an order
// that already exists for the request's PaymentIntent (a retry, or the winner of
// a concurrent-submit race). It syncs the PaymentIntent amount to the order total
// (in case the order was updated) and returns the order's current status.
func (s *Server) existingOrderResponse(ctx context.Context, handler dependency.Invoicer, req *pb_frontend.SubmitOrderRequest, existingOrder *entity.OrderFull) (*pb_frontend.SubmitOrderResponse, error) {
	// Ensure PaymentIntent amount matches order total (order may have been updated on ErrOrderItemsUpdated)
	if err := s.ensurePaymentIntentAmountMatchesOrder(ctx, handler, req.PaymentIntentId, existingOrder); err != nil {
		slog.Default().ErrorContext(ctx, "can't sync payment intent amount on idempotent retry", slog.String("err", err.Error()))
		return nil, status.Errorf(codes.Internal, "can't sync payment intent amount")
	}

	eos, ok := cache.GetOrderStatusById(existingOrder.Order.OrderStatusId)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve order status")
		return nil, status.Errorf(codes.Internal, "order status not found")
	}

	os, ok := dto.ConvertEntityToPbOrderStatus(eos.Status.Name)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to convert order status")
		return nil, status.Errorf(codes.Internal, "invalid order status")
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

func (s *Server) SubmitOrder(ctx context.Context, req *pb_frontend.SubmitOrderRequest) (*pb_frontend.SubmitOrderResponse, error) {
	if req.GetOrder() == nil {
		return nil, status.Errorf(codes.InvalidArgument, "order is required")
	}
	slog.Default().InfoContext(ctx, "SubmitOrder started",
		slog.String("payment_intent_id", req.PaymentIntentId),
		slog.String("payment_method", string(dto.ConvertPbPaymentMethodToEntity(req.Order.PaymentMethod))),
	)
	// Extract client identifiers for rate limiting
	clientIP := middleware.GetClientIP(ctx)
	clientSession := middleware.GetClientSession(ctx)

	orderNew, receivePromo := dto.ConvertCommonOrderNewToEntity(req.Order)
	orderNew.GAClientID = req.GaClientId

	_, err := v.ValidateStruct(orderNew)
	if err != nil {
		slog.Default().ErrorContext(ctx, "validation order create request failed",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.InvalidArgument, "validation failed")
	}

	// RATE LIMIT CHECK: Prevent cart bombing and order spam
	if err := s.rateLimiter.CheckOrderCreation(clientIP, orderNew.Buyer.Email); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for order creation",
			slog.String("ip", clientIP),
			slog.String("email", orderNew.Buyer.Email),
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
	}

	pm := dto.ConvertPbPaymentMethodToEntity(req.Order.PaymentMethod)

	pme, ok := cache.GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "payment method not configured")
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
			return s.existingOrderResponse(ctx, handler, req, existingOrder)
		}
	}

	order, sendEmail, err := s.repo.Order().CreateOrder(ctx, orderNew, receivePromo, time.Now().UTC().Add(expirationDuration))
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't create order",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "can't create order")
	}

	// COMMIT RESERVATION: Convert cart reservation to order reservation
	s.reservationMgr.Commit(ctx, clientSession, order.UUID)

	// Soft-reserve the order's packaging materials (PLM rework §2.8, S22). Best-effort: packaging must
	// never block a sale — an oversell is surfaced via available, not refused here, and the reservation
	// is released on cancel (cancelOrder) or closed on ship (consume). A failure is logged, not returned.
	if rerr := s.repo.MaterialStock().ReservePackagingForOrder(ctx, order.Id, ""); rerr != nil {
		slog.Default().WarnContext(ctx, "packaging reserve failed on submit",
			slog.String("order_uuid", order.UUID), slog.String("err", rerr.Error()))
	}

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
		// A concurrent SubmitOrder already claimed this PaymentIntent (UNIQUE on
		// payment.client_secret). Drop our duplicate order and return the order
		// that won the race, idempotently.
		if errors.Is(err, store.ErrPaymentIntentAlreadyAssociated) {
			slog.Default().InfoContext(ctx, "payment intent already associated with another order, returning winner",
				slog.String("order_uuid", order.UUID),
				slog.String("payment_intent_id", req.PaymentIntentId),
			)
			if cancelErr := s.repo.Order().CancelOrder(ctx, order.UUID); cancelErr != nil {
				slog.Default().ErrorContext(ctx, "failed to cancel duplicate order after associate race",
					slog.String("err", cancelErr.Error()),
					slog.String("order_uuid", order.UUID),
				)
			}
			s.reservationMgr.Release(ctx, order.UUID)

			existingOrder, lookupErr := s.repo.Order().GetOrderByPaymentIntentId(ctx, req.PaymentIntentId)
			if lookupErr != nil || existingOrder == nil {
				slog.Default().ErrorContext(ctx, "can't load winning order after associate race",
					slog.String("payment_intent_id", req.PaymentIntentId),
				)
				return nil, status.Errorf(codes.Aborted, "order is being processed, please retry")
			}
			return s.existingOrderResponse(ctx, handler, req, existingOrder)
		}

		slog.Default().ErrorContext(ctx, "can't associate payment intent with order",
			slog.String("err", err.Error()),
			slog.String("order_uuid", order.UUID),
			slog.String("payment_intent_id", req.PaymentIntentId),
		)
		if cancelErr := s.repo.Order().CancelOrder(ctx, order.UUID); cancelErr != nil {
			slog.Default().ErrorContext(ctx, "failed to cancel order after associate failure",
				slog.String("err", cancelErr.Error()),
				slog.String("order_uuid", order.UUID),
			)
		}
		s.reservationMgr.Release(ctx, order.UUID)
		return nil, status.Errorf(codes.Internal, "can't associate payment intent with order")
	}

	// The client may have already confirmed the card payment before SubmitOrder
	// finished (the client_secret is handed out by ValidateOrderItemsInsert). A
	// succeeded PaymentIntent can no longer have its shipping/amount updated, so
	// skip those Stripe mutations to avoid failing the request (and cancelling a
	// paid order). The payment is reconciled below via CheckForTransactions.
	paymentAlreadySucceeded := false
	if existingPi, piErr := handler.GetPaymentIntentByID(ctx, req.PaymentIntentId); piErr == nil {
		paymentAlreadySucceeded = stripe.PaymentIntentSucceeded(existingPi)
	}

	// Update existing PaymentIntent with order details (using data we already have - no DB query!)
	if !paymentAlreadySucceeded {
		err = handler.UpdatePaymentIntentWithOrderNew(ctx, req.PaymentIntentId, order.UUID, orderNew)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't update payment intent with order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", order.UUID),
			)
			if cancelErr := s.repo.Order().CancelOrder(ctx, order.UUID); cancelErr != nil {
				slog.Default().ErrorContext(ctx, "failed to cancel order after update failure",
					slog.String("err", cancelErr.Error()),
					slog.String("order_uuid", order.UUID),
				)
			}
			s.reservationMgr.Release(ctx, order.UUID)
			return nil, status.Errorf(codes.Internal, "can't update payment intent with order")
		}
	}

	var orderFull *entity.OrderFull
	orderFull, err = s.repo.Order().InsertFiatInvoice(ctx, order.UUID, req.PaymentIntentId, pme.Method, time.Now().UTC().Add(expirationDuration))
	if err != nil {
		if errors.Is(err, store.ErrOrderItemsUpdated) {
			// Order items were updated (stock/price changed). Update PaymentIntent amount and retry.
			orderFull, err = s.retryInsertFiatInvoiceAfterItemsUpdated(ctx, order.UUID, req.PaymentIntentId, pme.Method, expirationDuration, handler)
			if err != nil {
				slog.Default().ErrorContext(ctx, "can't complete payment after items update", slog.String("err", err.Error()))
				return nil, status.Errorf(codes.Internal, "can't complete payment after items update")
			}
		} else {
			slog.Default().ErrorContext(ctx, "InsertFiatInvoice failed, cancelling order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", order.UUID),
			)
			if cancelErr := s.repo.Order().CancelOrder(ctx, order.UUID); cancelErr != nil {
				slog.Default().ErrorContext(ctx, "failed to cancel orphan order", slog.String("orderUUID", order.UUID), slog.String("err", cancelErr.Error()))
			}
			s.reservationMgr.Release(ctx, order.UUID)
			return nil, status.Errorf(codes.Internal, "can't associate payment intent with order")
		}
	}

	// start monitoring immediately after InsertFiatInvoice succeeds
	// to prevent orphaned orders if subsequent operations fail.
	//
	// Detach the request context: the gRPC request ctx is cancelled the moment
	// SubmitOrder returns, which would kill the monitor goroutine instantly.
	// context.WithoutCancel preserves request values/trace while stopping
	// cancellation from propagating. The monitor's own lifecycle (and shutdown)
	// is governed by the processor's parent context, not this ctx.
	handler.StartMonitoringPayment(context.WithoutCancel(ctx), order.UUID, orderFull.Payment)

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

	// A succeeded PaymentIntent (customer paid during submit) can't have its
	// amount updated — and must not be touched. Skip the sync and reconcile below.
	if stripe.PaymentIntentSucceeded(stripePi) {
		paymentAlreadySucceeded = true
	} else {
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
				return nil, status.Errorf(codes.Internal, "payment amount mismatch")
			}

			slog.Default().InfoContext(ctx, "PaymentIntent amount updated successfully",
				slog.String("payment_intent_id", req.PaymentIntentId),
				slog.String("new_amount", orderTotal.String()),
			)
		}
	}

	// If the customer already paid, reconcile immediately so the order is
	// confirmed (status, email, analytics, reservation release) and the response
	// reflects it, instead of waiting for the expiration-time monitor.
	if paymentAlreadySucceeded {
		slog.Default().InfoContext(ctx, "payment already succeeded during submit, reconciling",
			slog.String("order_uuid", order.UUID),
			slog.String("payment_intent_id", req.PaymentIntentId),
		)
		if _, cerr := handler.CheckForTransactions(ctx, order.UUID, orderFull.Payment); cerr != nil {
			slog.Default().ErrorContext(ctx, "can't reconcile already-succeeded payment",
				slog.String("err", cerr.Error()),
				slog.String("order_uuid", order.UUID),
			)
		}
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
		return nil, status.Errorf(codes.Internal, "order status not found")
	}

	os, ok := dto.ConvertEntityToPbOrderStatus(eos.Status.Name)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to convert order status")
		return nil, status.Errorf(codes.Internal, "invalid order status")
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
		revalidateCtx := context.Background()
		// Best-effort background side effect: a panic in the revalidation path must
		// be logged with a stack and swallowed, never crash the whole process.
		defer saferun.Recover(revalidateCtx, "order-revalidate")
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
