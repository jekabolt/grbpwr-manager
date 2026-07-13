package frontend

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetOrderInvoice(ctx context.Context, req *pb_frontend.GetOrderInvoiceRequest) (*pb_frontend.GetOrderInvoiceResponse, error) {
	// RATE LIMIT CHECK: this endpoint operates on req.OrderUuid alone and returns
	// the payment client_secret, so without a limit the ORD-+7-char reference is
	// brute-forceable over time. Key on client IP only; the order UUID is unique
	// per guess, so keying a limiter on it never fills (audit p04-03).
	//
	// TODO(security): close the IDOR. This endpoint has no ownership binding: the
	// caller only proves knowledge of order_uuid, not that the order is theirs
	// (unlike GetOrderByUUIDAndEmail, which matches a buyer email). The guest
	// checkout flow reaches this endpoint without a storefront session, so we
	// cannot make the storefront access token (s.storefrontEmailFromAccess) a hard
	// requirement here without breaking guest payments. Fully fixing this needs a
	// proto change (add an email/token field to GetOrderInvoiceRequest, matched
	// against the order's buyer email) plus coordinated frontend changes to pass
	// it. Until then, rate limiting is the mitigation.
	clientIP := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckOrderInvoiceIP(clientIP); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for get order invoice",
			slog.String("ip", clientIP),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
	}

	pm := dto.ConvertPbPaymentMethodToEntity(req.PaymentMethod)

	pme, ok := cache.GetPaymentMethodByName(pm)
	if !ok {
		slog.Default().ErrorContext(ctx, "failed to retrieve payment method")
		return nil, status.Errorf(codes.Internal, "payment method not configured")
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

func (s *Server) CancelOrderInvoice(ctx context.Context, req *pb_frontend.CancelOrderInvoiceRequest) (*pb_frontend.CancelOrderInvoiceResponse, error) {
	// RATE LIMIT CHECK: this endpoint operates on req.OrderUuid alone and cancels
	// payment monitoring / releases reserved stock, so without a limit the
	// ORD-+7-char reference is brute-forceable into a denial-of-service against
	// other buyers' pending orders. Key on client IP only; the order UUID is unique
	// per guess, so keying a limiter on it never fills (audit p04-03).
	//
	// TODO(security): close the IDOR. Like GetOrderInvoice, this endpoint has no
	// ownership binding — the caller only proves knowledge of order_uuid, not that
	// the order is theirs (unlike CancelOrderByUser, which matches a buyer email).
	// The guest checkout flow reaches this endpoint without a storefront session,
	// so the storefront access token (s.storefrontEmailFromAccess) cannot be made
	// a hard requirement here without breaking guest payments. Fully fixing this
	// needs a proto change (add an email/token field to CancelOrderInvoiceRequest,
	// matched against the order's buyer email) plus coordinated frontend changes.
	// Until then, rate limiting is the mitigation.
	clientIP := middleware.GetClientIP(ctx)
	if err := s.rateLimiter.CheckOrderInvoiceIP(clientIP); err != nil {
		slog.Default().WarnContext(ctx, "rate limit exceeded for cancel order invoice",
			slog.String("ip", clientIP),
			slog.String("order_uuid", req.OrderUuid),
		)
		return nil, status.Errorf(codes.ResourceExhausted, "rate limit exceeded")
	}

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

	pme, ok := cache.GetPaymentMethodById(payment.PaymentMethodID)
	if !ok {
		slog.Default().ErrorContext(ctx, "payment method not found in cache",
			slog.Int("payment_method_id", payment.PaymentMethodID),
		)
		return nil, status.Errorf(codes.Internal, "payment method not found")
	}

	handler, err := s.getPaymentHandler(ctx, pme.Method.Name)
	if err != nil {
		slog.Default().ErrorContext(ctx, "can't get payment handler for cancel",
			slog.String("err", err.Error()),
		)
		return nil, status.Errorf(codes.Internal, "failed to get payment handler")
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

func (s *Server) getInvoiceByPaymentMethod(ctx context.Context, handler dependency.Invoicer, orderUuid string) (*entity.PaymentInsert, error) {
	pi, err := handler.GetOrderInvoice(ctx, orderUuid)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get invoice: %v", err)
	}
	return pi, nil
}
