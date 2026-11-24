package frontend

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/ratelimit"
	"github.com/jekabolt/grbpwr-manager/internal/stockreserve"
	"github.com/jekabolt/grbpwr-manager/internal/storefront"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// clampPagination bounds a client-supplied limit/offset for public list
// endpoints so a huge limit can't force MySQL to materialize the whole table.
func clampPagination(limit, offset, def, max int) (int, int) {
	if limit <= 0 {
		limit = def
	}
	if limit > max {
		limit = max
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
}

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
	storefront        *storefrontAuthRuntime // the storefront team's customer auth; nil = team shops only
	shopBase          *storefrontAuthRuntime // what team shops derive their keys over
	shops             *shopState             // nil = teams mode off (X18)
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
	setReservations(stripePayment, stripePaymentTest, reservationMgr)

	sa, err := newStorefrontAuthRuntime(storefrontCfg)
	if err != nil {
		return nil, fmt.Errorf("storefront auth: %w", err)
	}
	return newServer(r, m, stripePayment, stripePaymentTest, re, reservationMgr, sa, sa), nil
}

// NewTeamShops is New for an installation without a storefront team (TENANT_STOREFRONT_TEAM_ID=none):
// it needs no legacy STOREFRONT_AUTH_* keys and never fails. Customer accounts work only at team shops
// (keys from STOREFRONT_AUTH_SHOP_MASTER, SetShops); a call for no shop is refused FailedPrecondition.
func NewTeamShops(
	r dependency.Repository,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr *stockreserve.Manager,
	storefrontCfg *storefront.Config,
) *Server {
	setReservations(stripePayment, stripePaymentTest, reservationMgr)
	return newServer(r, m, stripePayment, stripePaymentTest, re, reservationMgr, nil, newShopBaseRuntime(storefrontCfg))
}

func newServer(
	r dependency.Repository,
	m dependency.Mailer,
	stripePayment dependency.Invoicer,
	stripePaymentTest dependency.Invoicer,
	re dependency.RevalidationService,
	reservationMgr *stockreserve.Manager,
	legacy, shopBase *storefrontAuthRuntime,
) *Server {
	return &Server{
		repo:              r,
		mailer:            m,
		stripePayment:     stripePayment,
		stripePaymentTest: stripePaymentTest,
		re:                re,
		rateLimiter:       ratelimit.NewMultiKeyLimiter(),
		reservationMgr:    reservationMgr,
		storefront:        legacy,
		shopBase:          shopBase,
	}
}

// StopRateLimiter terminates the frontend rate-limiter cleanup goroutines. Called
// from App.Stop so the limiters follow the same lifecycle discipline as the other
// background components (idempotent).
func (s *Server) StopRateLimiter() {
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
}

// setReservations hands the reservation manager to the Stripe processors that take one.
func setReservations(stripePayment, stripePaymentTest dependency.Invoicer, reservationMgr *stockreserve.Manager) {
	for _, p := range []dependency.Invoicer{stripePayment, stripePaymentTest} {
		if sp, ok := p.(interface {
			SetReservationManager(dependency.StockReservationManager)
		}); ok {
			sp.SetReservationManager(reservationMgr)
		}
	}
}
