package frontend

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/stockreserve"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
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
	storefront        *storefrontAuthRuntime
}

// New creates a new server with frontend handlers.
func New(
	r dependency.Repository,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr *stockreserve.Manager,
	storefrontCfg *storefront.Config,
) (*Server, error) {
	// Set reservation manager on stripe processors if they support it
	if sp, ok := stripePayment.(interface {
		SetReservationManager(dependency.StockReservationManager)
	}); ok {
		sp.SetReservationManager(reservationMgr)
	}
	if spt, ok := stripePaymentTest.(interface {
		SetReservationManager(dependency.StockReservationManager)
	}); ok {
		spt.SetReservationManager(reservationMgr)
	}

	sa, err := newStorefrontAuthRuntime(storefrontCfg)
	if err != nil {
		return nil, fmt.Errorf("storefront auth: %w", err)
	}

	return &Server{
		repo:              r,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		rateLimiter:       ratelimit.NewMultiKeyLimiter(),
		reservationMgr:    reservationMgr,
		storefront:        sa,
	}, nil
}
