package frontend

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"log/slog"
	"net/url"
	"strconv"
	"sync"

	"github.com/go-chi/jwtauth/v5"
	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Shops of teams (X18). A storefront request carries its shop's team (tenantbind). The storefront
// team (grbpwr) keeps today's customer keys from env; every other team's customer keys are derived
// from STOREFRONT_AUTH_SHOP_MASTER and its id, and its access tokens name audience "shop:{id}", so a
// token minted at one shop is refused at another.

// ShopDeps is what the frontend needs about shops (teams mode only).
type ShopDeps struct {
	StorefrontID int64
	Master       string // STOREFRONT_AUTH_SHOP_MASTER; "" = shop accounts unavailable
	Service      *shop.Service
	// Open is the edge's answer whether a team's shop takes orders (tenantbind.Binder.ShopOpen).
	Open func(ctx context.Context, t tenant.Team) (bool, error)
	// Revalidator replaces the server's revalidator: a team shop's change redraws that shop, the
	// storefront team's the storefront (shop.Revalidator). nil = keep the one New got.
	Revalidator dependency.RevalidationService
	// Payments is the team shops' Stripe (shop_pay.go); nil = team-shop checkout is refused.
	Payments ShopPayments
}

type shopState struct {
	d     ShopDeps
	auths sync.Map // team id -> *storefrontAuthRuntime
}

// SetShops turns shops on for this server (call before serving).
func (s *Server) SetShops(d ShopDeps) {
	// A new key, so no grace for a short one: every shop's customer keys derive from it.
	if d.Master != "" && len(d.Master) < jwt.MinSymmetricSecretBytes {
		slog.Default().Error("shops: STOREFRONT_AUTH_SHOP_MASTER is shorter than 32 bytes; team shop customers cannot sign in")
		d.Master = ""
	}
	s.shops = &shopState{d: d}
	if d.Revalidator != nil {
		s.re = d.Revalidator
	}
}

// MsgShopPaymentsNotReady refuses a team shop's checkout while it has no usable Stripe (X18-L3).
const MsgShopPaymentsNotReady = "shop_payments_not_ready"

// shopAudience is the access-token audience of a shop team.
func shopAudience(teamID int64) string { return "shop:" + strconv.FormatInt(teamID, 10) }

// deriveShopAuth is base with the keys of team teamID's shop.
func deriveShopAuth(base *storefrontAuthRuntime, master string, teamID int64) (*storefrontAuthRuntime, error) {
	key := func(purpose string) ([]byte, error) { return shop.DeriveKey(master, teamID, purpose, 32) }
	access, err := key("access")
	if err != nil {
		return nil, err
	}
	login, err := key("login")
	if err != nil {
		return nil, err
	}
	refresh, err := key("refresh")
	if err != nil {
		return nil, err
	}
	rt := *base
	rt.teamID = teamID
	rt.accessJwtAuth = jwtauth.New("HS256", access, nil)
	rt.loginPepper = hex.EncodeToString(login)
	rt.refreshPepper = hex.EncodeToString(refresh)
	iss := ""
	if base.accessExpectations != nil {
		iss = base.accessExpectations.Issuer
	}
	rt.accessExpectations = &jwt.Expectations{Issuer: iss, Audience: shopAudience(teamID)}
	opts := jwt.IssueOpts{Issuer: iss, Audience: shopAudience(teamID), IncludeJti: base.accessJtiRevocationEnabled, TeamID: teamID}
	rt.accessIssueOpts = &opts
	return &rt, nil
}

// sfAuth is the customer-auth runtime of the request's shop.
func (s *Server) sfAuth(ctx context.Context) (*storefrontAuthRuntime, error) {
	sh := s.shops
	t, ok := tenant.From(ctx)
	if sh == nil || !ok || t.ID == 0 || t.ID == sh.d.StorefrontID {
		if s.storefront == nil {
			// No storefront team (team shops only): no legacy customer keys to sign with.
			return nil, status.Error(codes.FailedPrecondition, "storefront account API is not configured")
		}
		return s.storefront, nil
	}
	if s.shopBase == nil {
		return nil, status.Error(codes.FailedPrecondition, "storefront account API is not configured")
	}
	if v, ok := sh.auths.Load(t.ID); ok {
		return v.(*storefrontAuthRuntime), nil
	}
	if sh.d.Master == "" {
		return nil, status.Error(codes.Unavailable, "shop accounts are not configured")
	}
	rt, err := deriveShopAuth(s.shopBase, sh.d.Master, t.ID)
	if err != nil {
		slog.Default().ErrorContext(ctx, "derive shop auth", slog.String("err", err.Error()))
		return nil, status.Error(codes.Internal, "shop accounts unavailable")
	}
	v, _ := sh.auths.LoadOrStore(t.ID, rt)
	return v.(*storefrontAuthRuntime), nil
}

// magicLinkBase is where a login letter's link points: the storefront's configured base, or for a
// shop team the same path on its shop's main address.
func (s *Server) magicLinkBase(ctx context.Context, rt *storefrontAuthRuntime) (*url.URL, error) {
	u, err := url.Parse(rt.magicLinkBaseURL)
	if err != nil {
		return nil, fmt.Errorf("magic_link_base_url: %w", err)
	}
	if rt.teamID == 0 || s.shops == nil || s.shops.d.Service == nil {
		return u, nil
	}
	host, err := s.shops.d.Service.PrimaryHost(ctx, rt.teamID)
	if err != nil {
		return nil, err
	}
	if host == "" {
		return nil, fmt.Errorf("shop has no address")
	}
	u.Scheme, u.Host = "https", host
	return u, nil
}

// GetShopProfile tells a storefront how to draw the shop it serves (answers while closed, open=false).
func (s *Server) GetShopProfile(ctx context.Context, _ *pb_frontend.GetShopProfileRequest) (*pb_frontend.GetShopProfileResponse, error) {
	t, ok := tenant.From(ctx)
	if !ok {
		return nil, status.Error(codes.Unavailable, "shop unavailable")
	}
	out := &pb_frontend.ShopProfile{
		ShopKey:      shop.Key(t.ID),
		Name:         t.Name,
		Open:         true,
		BaseCurrency: cache.GetBaseCurrency(),
		Colors:       &pb_frontend.ShopColors{},
		Legal:        map[string]string{},
	}
	sh := s.shops
	if sh == nil || sh.d.Service == nil {
		return &pb_frontend.GetShopProfileResponse{Profile: out}, nil
	}
	open := true
	if sh.d.Open != nil {
		var err error
		if open, err = sh.d.Open(ctx, t); err != nil {
			slog.Default().ErrorContext(ctx, "shop open", slog.String("err", err.Error()))
			return nil, status.Error(codes.Unavailable, "shop unavailable")
		}
	}
	p, err := sh.d.Service.Profile(ctx, t, open)
	if err != nil {
		slog.Default().ErrorContext(ctx, "shop profile", slog.String("err", err.Error()))
		return nil, status.Error(codes.Unavailable, "shop unavailable")
	}
	out.Name, out.LegalName, out.LogoUrl, out.Email = p.Name, p.LegalEntity, p.LogoURL, p.ContactEmail
	out.Address, out.Website, out.PrimaryHost, out.Hosts, out.Open = p.LegalAddress, p.Website, p.PrimaryHost, p.Hosts, p.Open
	out.Pack = p.Pack
	if sh.d.StorefrontID > 0 && t.ID == sh.d.StorefrontID {
		// The storefront team pays through its env processors: its key comes with the profile.
		out.StripePublishableKey, out.StripeMode = p.StripePublishableKey, p.StripeMode
	} else {
		out.StripeAccountId, out.StripePublishableKey, out.StripeMode = shopStripeOf(sh.d.Payments, p)
	}
	return &pb_frontend.GetShopProfileResponse{Profile: out}, nil
}
