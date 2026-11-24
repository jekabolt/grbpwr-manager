package frontend

import (
	"context"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ShopPayments is the team shops' Stripe (shoppay.Service, X18-L3): a team's processor charges on
// its connected account or with its own keys.
type ShopPayments interface {
	For(ctx context.Context, t tenant.Team, pm entity.PaymentMethodName) (dependency.Invoicer, error)
	// ConnectPublishableKey is the platform key a connected team's Stripe.js loads with ("" = off).
	ConnectPublishableKey() string
}

// shopStripeOf is what a shop's storefront loads Stripe.js with: the team's own publishable key, or
// the platform's with the connected account. Nothing while the shop cannot take a payment here.
func shopStripeOf(pay ShopPayments, p shop.Profile) (account, publishable, mode string) {
	if pay == nil || p.StripeMode == "" {
		return "", "", ""
	}
	if p.StripeAccountID != "" {
		publishable = pay.ConnectPublishableKey()
		if publishable == "" {
			return "", "", ""
		}
		return p.StripeAccountID, publishable, p.StripeMode
	}
	if p.StripePublishableKey == "" {
		return "", "", ""
	}
	return "", p.StripePublishableKey, p.StripeMode
}

// teamPaymentHandler is the processor of a team shop's payment, handled=false for the storefront
// team (its env processors). A team's money never lands on the storefront's Stripe: without team
// payments wired its checkout is refused.
func (s *Server) teamPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, bool, error) {
	sh := s.shops
	if sh == nil {
		return nil, false, nil
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID == 0 || t.ID == sh.d.StorefrontID {
		return nil, false, nil
	}
	if sh.d.Payments == nil {
		return nil, true, status.Error(codes.FailedPrecondition, MsgShopPaymentsNotReady)
	}
	h, err := sh.d.Payments.For(ctx, t, pm)
	return h, true, err
}
