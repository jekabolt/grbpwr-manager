package admin

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4mp"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server implements handlers for admin.
type Server struct {
	pb_admin.UnimplementedAdminServiceServer
	repo              dependency.Repository
	bucket            dependency.FileStore
	mailer            dependency.Mailer
	stripePayment     dependency.Invoicer
	stripePaymentTest dependency.Invoicer
	re                dependency.RevalidationService
	reservationMgr    dependency.StockReservationManager
	ga4mp             *ga4mp.Client
}

// New creates a new server with admin handlers.
func New(
	r dependency.Repository,
	b dependency.FileStore,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr dependency.StockReservationManager,
	ga4mpClient *ga4mp.Client,
) *Server {
	return &Server{
		repo:              r,
		bucket:            b,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		reservationMgr:    reservationMgr,
		ga4mp:             ga4mpClient,
	}
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
