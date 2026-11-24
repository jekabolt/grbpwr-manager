package admin

import (
	"context"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Payments of team shops (X18-L3): Stripe Connect or the team's own keys. The storefront team keeps
// its own processors (env keys) everywhere; every other team pays, refunds and checks payments
// through its own Stripe only (shoppay.Service).

// ShopPayments is the team shops' Stripe (shoppay.Service).
type ShopPayments interface {
	// StartConnect answers the Stripe onboarding link (or the dashboard of a connected account).
	StartConnect(ctx context.Context, t tenant.Team, userID int64, operator bool, returnPath string) (string, error)
	// FinishConnect connects the account of the callback's finish token, for the user who started it.
	FinishConnect(ctx context.Context, t tenant.Team, userID int64, operator bool, token string) error
	// SetKeys checks the team's own keys with Stripe and stores them sealed.
	SetKeys(ctx context.Context, t tenant.Team, pk, sk, whsec string) error
	// Disconnect turns the team's Stripe off (idempotent).
	Disconnect(ctx context.Context, t tenant.Team) error
	// WebhookURL is where a team with own keys points its Stripe webhook ("" = not available).
	WebhookURL(teamID int64) string
	// For is the team's processor of payment method pm.
	For(ctx context.Context, t tenant.Team, pm entity.PaymentMethodName) (dependency.Invoicer, error)
}

// msgShopPaymentsNotReady matches shoppay.MsgPaymentsNotReady and frontend.MsgShopPaymentsNotReady.
const msgShopPaymentsNotReady = "shop_payments_not_ready"

func errStripeNotReady() error { return status.Error(codes.Unimplemented, "stripe_not_ready") }

// shopPayer resolves an owner's (or an entered operator's) Stripe change of a team shop.
func (s *Server) shopPayer(ctx context.Context) (peopleCtx, ShopPayments, error) {
	p, err := s.shopCaller(ctx, true)
	if err != nil {
		return peopleCtx{}, nil, err
	}
	if err := s.legacyShop(p); err != nil {
		return peopleCtx{}, nil, err
	}
	if s.shopW.Payments == nil {
		return peopleCtx{}, nil, errStripeNotReady()
	}
	return p, s.shopW.Payments, nil
}

// StartStripeConnect answers the link that starts (or, once connected, manages) Stripe Connect;
// Stripe sends the owner back through the API's callback.
func (s *Server) StartStripeConnect(ctx context.Context, req *pb_admin.StartStripeConnectRequest) (*pb_admin.StartStripeConnectResponse, error) {
	p, pay, err := s.shopPayer(ctx)
	if err != nil {
		return nil, err
	}
	url, err := pay.StartConnect(ctx, p.w.Team, p.userID, p.w.Operator, strings.TrimSpace(req.GetReturnPath()))
	if err != nil {
		return nil, shopErr(ctx, "stripe connect", err)
	}
	s.peopleAudit(ctx, p, "shop.stripe_connect_start", map[string]string{})
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.StartStripeConnectResponse{Url: url, Shop: out}, nil
}

// FinishStripeConnect connects the Stripe account the owner approved: the token comes from the admin
// address Stripe's callback sent the owner back to. Only the user who started the connect finishes it.
func (s *Server) FinishStripeConnect(ctx context.Context, req *pb_admin.FinishStripeConnectRequest) (*pb_admin.FinishStripeConnectResponse, error) {
	p, pay, err := s.shopPayer(ctx)
	if err != nil {
		return nil, err
	}
	if err := pay.FinishConnect(ctx, p.w.Team, p.userID, p.w.Operator, strings.TrimSpace(req.GetToken())); err != nil {
		return nil, shopErr(ctx, "stripe connect finish", err)
	}
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.FinishStripeConnectResponse{Shop: out}, nil
}

// SetStripeKeys stores the team's own Stripe keys (checked with Stripe first). The secret key and the
// webhook secret never come back.
func (s *Server) SetStripeKeys(ctx context.Context, req *pb_admin.SetStripeKeysRequest) (*pb_admin.SetStripeKeysResponse, error) {
	p, pay, err := s.shopPayer(ctx)
	if err != nil {
		return nil, err
	}
	if err := pay.SetKeys(ctx, p.w.Team, req.GetPublishableKey(), req.GetSecretKey(), req.GetWebhookSecret()); err != nil {
		return nil, shopErr(ctx, "stripe keys", err)
	}
	s.peopleAudit(ctx, p, "shop.stripe_keys", map[string]string{})
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.SetStripeKeysResponse{Shop: out}, nil
}

// DisconnectStripe turns the team's Stripe off: checkout stops at once.
func (s *Server) DisconnectStripe(ctx context.Context, _ *pb_admin.DisconnectStripeRequest) (*pb_admin.DisconnectStripeResponse, error) {
	p, pay, err := s.shopPayer(ctx)
	if err != nil {
		return nil, err
	}
	if err := pay.Disconnect(ctx, p.w.Team); err != nil {
		return nil, shopErr(ctx, "stripe disconnect", err)
	}
	s.peopleAudit(ctx, p, "shop.stripe_disconnect", map[string]string{})
	out, err := s.shopOf(ctx, p)
	if err != nil {
		return nil, err
	}
	return &pb_admin.DisconnectStripeResponse{Shop: out}, nil
}

// shopWebhookURL fills the address a team with own keys gives its Stripe webhook: shown whenever the
// team is not on a live Connect account, so the keys form has it before the first save.
func (s *Server) shopWebhookURL(t tenant.Team, out *pb_admin.ShopPayments, st platformstore.TeamStripe, has bool) {
	if s.shopW == nil || s.shopW.Payments == nil || out == nil {
		return
	}
	if has && st.DisconnectedAt == nil && st.Kind == platformstore.StripeKindConnect {
		return
	}
	out.WebhookUrl = s.shopW.Payments.WebhookURL(t.ID)
}

// teamPaymentHandler is the processor of a team shop's payment (its own Stripe), handled=false for
// the storefront team, which keeps the env processors. With shops on, no other team ever reaches the
// storefront's Stripe: without team payments its card payments are refused.
func (s *Server) teamPaymentHandler(ctx context.Context, pm entity.PaymentMethodName) (dependency.Invoicer, bool, error) {
	if s.shopW == nil {
		return nil, false, nil
	}
	t, ok := tenant.From(ctx)
	if !ok || t.ID <= 0 || t.ID == s.legacyPaymentTeam() {
		return nil, false, nil
	}
	if s.shopW.Payments == nil {
		if pm != entity.CARD && pm != entity.CARD_TEST {
			return nil, true, status.Errorf(codes.Unimplemented, "payment method unimplemented")
		}
		return nil, true, status.Error(codes.FailedPrecondition, msgShopPaymentsNotReady)
	}
	h, err := s.shopW.Payments.For(ctx, t, pm)
	return h, true, err
}

// legacyPaymentTeam is the team that pays through the env processors: the storefront team, or team #1
// when there is no storefront (its old orders keep their processors).
func (s *Server) legacyPaymentTeam() int64 {
	if id := s.storefrontID(); id > 0 {
		return id
	}
	return tenant.TeamOneID
}

// ShopPaymentsChanged is the team shops' Stripe hook (shoppay.Deps.Changed): the team's cached shop
// answers drop on this instance and its profile is redrawn. ctx carries the team.
func (s *Server) ShopPaymentsChanged(ctx context.Context, teamID int64) {
	if s.shopW == nil || teamID <= 0 || teamID == s.legacyPaymentTeam() {
		return
	}
	if t, ok := tenant.From(ctx); !ok || t.ID != teamID {
		if s.shopW.Invalidate != nil {
			s.shopW.Invalidate(teamID)
		}
		return
	}
	s.shopChanged(ctx, teamID)
}
