package frontend

import (
	"context"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) GetOrderInvoice(ctx context.Context, req *pb_frontend.GetOrderInvoiceRequest) (*pb_frontend.GetOrderInvoiceResponse, error) {
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
