package frontend

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

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

	if entity.OrderStatusExposesOrderReview(os.Status.Name) {
		review, err := s.repo.Order().GetOrderReviewByUUID(ctx, o.Order.UUID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				slog.Default().ErrorContext(ctx, "can't get order review by uuid",
					slog.String("err", err.Error()),
					slog.String("order_uuid", o.Order.UUID),
				)
				return nil, status.Errorf(codes.Internal, "can't get order review")
			}
		} else {
			o.OrderReview = review
		}
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

	// Fetch order first to determine the ORIGINAL status before any changes.
	// This drives the switch logic; the actual atomic status transition happens
	// inside CancelOrderByUser (which uses SELECT … FOR UPDATE).
	orderFull, err := s.repo.Order().GetOrderByUUIDAndEmail(ctx, req.OrderUuid, email)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Error(codes.NotFound, "order not found")
		}
		slog.Default().ErrorContext(ctx, "can't get order",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.Internal, "can't get order")
	}

	os, ok := cache.GetOrderStatusById(orderFull.Order.OrderStatusId)
	if !ok {
		return nil, status.Error(codes.Internal, "can't get order status by id")
	}
	originalStatus := os.Status.Name

	switch originalStatus {
	// --- Terminal / already-in-progress: reject ---
	case entity.Cancelled, entity.Refunded, entity.PartiallyRefunded, entity.RefundInProgress, entity.PendingReturn:
		return nil, status.Errorf(codes.FailedPrecondition,
			"order cannot be cancelled in status: %s", originalStatus)

	// --- No payment yet: cancel directly ---
	case entity.Placed, entity.AwaitingPayment:
		// CancelOrderByUser atomically sets status to Cancelled + restores stock
		if _, err := s.repo.Order().CancelOrderByUser(ctx, req.OrderUuid, email, req.Reason); err != nil {
			slog.Default().ErrorContext(ctx, "can't cancel order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't cancel order")
		}
		s.reservationMgr.Release(ctx, req.OrderUuid)

		slog.Default().InfoContext(ctx, "order cancelled by user (no payment yet)",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("email", email),
			slog.String("reason", req.Reason),
		)
		return &pb_frontend.CancelOrderByUserResponse{Order: nil}, nil

	// --- Paid but not shipped: refund via Stripe ---
	case entity.Confirmed:
		// CancelOrderByUser atomically sets status to RefundInProgress (prevents double refund)
		orderFull, err = s.repo.Order().CancelOrderByUser(ctx, req.OrderUuid, email, req.Reason)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't initiate refund for confirmed order",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't cancel order")
		}

		pm, ok := cache.GetPaymentMethodById(orderFull.Payment.PaymentMethodID)
		if !ok {
			return nil, status.Error(codes.Internal, "can't get payment method by id")
		}

		if pm.Method.Name == entity.CARD || pm.Method.Name == entity.CARD_TEST {
			pHandler, err := s.getPaymentHandler(ctx, pm.Method.Name)
			if err != nil {
				return nil, status.Error(codes.Internal, "can't get payment handler")
			}

			if err := pHandler.Refund(ctx, orderFull.Payment, req.OrderUuid, nil, orderFull.Order.Currency); err != nil {
				slog.Default().ErrorContext(ctx, "stripe refund failed",
					slog.String("err", err.Error()),
					slog.String("order_uuid", req.OrderUuid),
				)
				return nil, status.Errorf(codes.Internal, "can't refund payment")
			}

			// RefundOrder transitions RefundInProgress → Refunded, restores stock, records refunded items
			if err := s.repo.Order().RefundOrder(ctx, req.OrderUuid, nil, req.Reason, true); err != nil {
				slog.Default().ErrorContext(ctx, "can't finalize refund in DB",
					slog.String("err", err.Error()),
					slog.String("order_uuid", req.OrderUuid),
				)
				return nil, status.Errorf(codes.Internal, "can't refund order")
			}
		} else {
			slog.Default().InfoContext(ctx, "confirmed order cancellation requested; refund not auto-handled for payment method",
				slog.String("order_uuid", req.OrderUuid),
				slog.String("payment_method", string(pm.Method.Name)),
			)
		}

		// Refresh to get final Refunded status
		orderFull, err = s.repo.Order().GetOrderByUUIDAndEmail(ctx, req.OrderUuid, email)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "can't refresh order")
		}

		// Send "refund initiated" email
		refundDetails := dto.OrderFullToOrderRefundInitiated(orderFull)
		if err := s.mailer.SendRefundInitiated(ctx, s.repo, orderFull.Buyer.Email, refundDetails); err != nil {
			slog.Default().ErrorContext(ctx, "can't send refund initiated email",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
		}

	// --- Already shipped/delivered: pending return ---
	case entity.Shipped, entity.Delivered:
		eligible, reason := isOrderEligibleForReturn(orderFull, originalStatus)
		if !eligible {
			return nil, status.Error(codes.FailedPrecondition, reason)
		}

		// CancelOrderByUser atomically sets status to PendingReturn
		orderFull, err = s.repo.Order().CancelOrderByUser(ctx, req.OrderUuid, email, req.Reason)
		if err != nil {
			slog.Default().ErrorContext(ctx, "can't set pending return",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
			return nil, status.Errorf(codes.Internal, "can't set order to pending return")
		}

		slog.Default().InfoContext(ctx, "order set to pending return by user",
			slog.String("order_uuid", req.OrderUuid),
			slog.String("email", email),
			slog.String("reason", req.Reason),
		)

		// Send "pending return" email (waiting for parcel back)
		pendingDetails := dto.OrderFullToOrderPendingReturn(orderFull)
		if err := s.mailer.SendPendingReturn(ctx, s.repo, orderFull.Buyer.Email, pendingDetails); err != nil {
			slog.Default().ErrorContext(ctx, "can't send pending return email",
				slog.String("err", err.Error()),
				slog.String("order_uuid", req.OrderUuid),
			)
		}

	default:
		return nil, status.Errorf(codes.FailedPrecondition,
			"order can't be cancelled in status: %s", originalStatus)
	}

	pbOrder, err := dto.ConvertEntityOrderFullToPbOrderFull(orderFull)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't convert order to protobuf",
			slog.String("err", err.Error()),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Error(codes.Internal, "can't convert order")
	}

	slog.Default().InfoContext(ctx, "order cancel/return processed by user",
		slog.String("order_uuid", req.OrderUuid),
		slog.String("email", email),
		slog.String("reason", req.Reason),
		slog.String("original_status", string(originalStatus)),
	)

	return &pb_frontend.CancelOrderByUserResponse{Order: pbOrder}, nil
}
