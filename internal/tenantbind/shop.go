package tenantbind

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Storefront requests and team shops (X18). A storefront call names its shop by the header
// x-shop-host (a storefront's server-side calls) or, failing that, by its Origin (the browser). The
// host is looked up in shop_host; a request that names no shop keeps today's answer: the storefront
// team (TENANT_STOREFRONT_TEAM_ID, grbpwr). Teams mode off: team #1, as before.

// ShopHostHeader is the HTTP header a storefront names its shop with; the gateway forwards it as the
// metadata key ShopHostMD.
const (
	ShopHostHeader = "X-Shop-Host"
	ShopHostMD     = "x-shop-host"
)

// Refusals of a storefront request (also the JSON error text of the http edge).
const (
	MsgShopNotFound = "shop_not_found"
	MsgShopMismatch = "shop_mismatch"
	MsgShopClosed   = "shop_closed"
)

// shopProfileMethod answers even while a shop is closed (it says open=false).
const shopProfileMethod = "/frontend.FrontendService/GetShopProfile"

// shopPlane is what the binder reads about shops (platformstore.Store in prod, a fake in tests).
type shopPlane interface {
	ShopHostTeam(ctx context.Context, host string) (int64, bool, error)
	TeamShop(ctx context.Context, teamID int64) (platformstore.TeamShop, error)
}

type shopHostEntry struct {
	teamID  int64
	ok      bool
	expires time.Time
}

type shopStateEntry struct {
	ts      platformstore.TeamShop
	expires time.Time
}

// shopCacheMax bounds the host cache (negative answers included); it is reset when reached.
const shopCacheMax = 10000

// SetStorefrontOrigins hands the binder the storefront's own origins (HTTP_ALLOWED_ORIGINS): a
// request naming one of their hosts in x-shop-host is the storefront team's, not an unknown shop.
func (b *Binder) SetStorefrontOrigins(origins []string) {
	m := map[string]bool{}
	for _, o := range origins {
		if h := shop.OriginHost(o); h != "" {
			m[h] = true
		}
	}
	b.mu.Lock()
	b.storefrontHosts = m
	b.mu.Unlock()
}

// InvalidateShop drops this instance's cached shop answers (every host, and teamID's shop state;
// teamID 0 = every team). Other instances see a change within cacheTTL.
func (b *Binder) InvalidateShop(teamID int64) {
	b.cacheMu.Lock()
	b.shopHosts = map[string]shopHostEntry{}
	if teamID == 0 {
		b.shopStates = map[int64]shopStateEntry{}
	} else {
		delete(b.shopStates, teamID)
	}
	b.cacheMu.Unlock()
}

func (b *Binder) shopPlane() shopPlane {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.shops
}

func (b *Binder) hostTeam(ctx context.Context, sp shopPlane, host string) (int64, bool, error) {
	now := b.now()
	b.cacheMu.Lock()
	e, hit := b.shopHosts[host]
	b.cacheMu.Unlock()
	if hit && now.Before(e.expires) {
		return e.teamID, e.ok, nil
	}
	id, ok, err := sp.ShopHostTeam(ctx, host)
	if err != nil {
		return 0, false, err
	}
	b.cacheMu.Lock()
	if b.shopHosts == nil || len(b.shopHosts) >= shopCacheMax {
		b.shopHosts = map[string]shopHostEntry{}
	}
	b.shopHosts[host] = shopHostEntry{teamID: id, ok: ok, expires: now.Add(cacheTTL)}
	b.cacheMu.Unlock()
	return id, ok, nil
}

func (b *Binder) teamShop(ctx context.Context, sp shopPlane, teamID int64) (platformstore.TeamShop, error) {
	now := b.now()
	b.cacheMu.Lock()
	e, hit := b.shopStates[teamID]
	b.cacheMu.Unlock()
	if hit && now.Before(e.expires) {
		return e.ts, nil
	}
	ts, err := sp.TeamShop(ctx, teamID)
	if err != nil {
		return platformstore.TeamShop{}, err
	}
	b.cacheMu.Lock()
	if b.shopStates == nil {
		b.shopStates = map[int64]shopStateEntry{}
	}
	b.shopStates[teamID] = shopStateEntry{ts: ts, expires: now.Add(cacheTTL)}
	b.cacheMu.Unlock()
	return ts, nil
}

// StorefrontTeamID is the id of the storefront team (grbpwr): its shop is today's storefront.
func (b *Binder) StorefrontTeamID() int64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.storefrontID
}

// ShopTeam resolves the team of a storefront request from its x-shop-host and Origin. legacy=true:
// the request names no shop (or one of the storefront's own hosts) and runs as the storefront team.
// Refusals: NotFound shop_not_found (x-shop-host names no shop), PermissionDenied shop_mismatch
// (x-shop-host and Origin name two different shops), Unavailable (control plane).
func (b *Binder) ShopTeam(ctx context.Context, shopHost, origin string) (t tenant.Team, legacy bool, err error) {
	sp := b.shopPlane()
	if !b.TeamsMode() || sp == nil {
		t, err := b.StorefrontTeam(ctx)
		return t, true, err
	}
	b.mu.RLock()
	own := b.storefrontHosts
	b.mu.RUnlock()

	hh := shop.NormHost(shopHost)
	oh := shop.OriginHost(origin)
	var fromHeader, fromOrigin int64
	if hh != "" && !own[hh] {
		id, ok, err := b.hostTeam(ctx, sp, hh)
		if err != nil {
			return tenant.Team{}, false, status.Error(codes.Unavailable, "shop lookup unavailable")
		}
		if !ok {
			return tenant.Team{}, false, status.Error(codes.NotFound, MsgShopNotFound)
		}
		fromHeader = id
	}
	if oh != "" && !own[oh] {
		id, ok, err := b.hostTeam(ctx, sp, oh)
		if err != nil {
			return tenant.Team{}, false, status.Error(codes.Unavailable, "shop lookup unavailable")
		}
		if ok {
			fromOrigin = id // an unknown Origin (dev, the storefront's own) names no shop
		}
	}
	id := fromHeader
	switch {
	case fromHeader != 0 && fromOrigin != 0 && fromHeader != fromOrigin:
		return tenant.Team{}, false, status.Error(codes.PermissionDenied, MsgShopMismatch)
	case id == 0 && hh != "" && own[hh]:
		// x-shop-host names the storefront's own host: the storefront team, whatever the Origin says.
	case id == 0:
		id = fromOrigin
	}
	if id == 0 || id == b.StorefrontTeamID() {
		t, err := b.StorefrontTeam(ctx)
		return t, true, err
	}
	t, err = b.TeamByID(ctx, id)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return tenant.Team{}, false, status.Error(codes.NotFound, MsgShopNotFound)
		}
		return tenant.Team{}, false, status.Error(codes.Unavailable, "team lookup unavailable")
	}
	return t, false, nil
}

// ShopOpen reports whether a team's shop takes orders: the storefront team always (today's
// storefront); any other team only while it is active, its shop is on and not closed by the
// operator, and its products and orders features are on.
func (b *Binder) ShopOpen(ctx context.Context, t tenant.Team) (open bool, err error) {
	if t.ID == b.StorefrontTeamID() || !b.TeamsMode() {
		return true, nil
	}
	sp := b.shopPlane()
	if sp == nil || t.Status != tenant.StatusActive {
		return false, nil
	}
	ts, err := b.teamShop(ctx, sp, t.ID)
	if err != nil {
		return false, err
	}
	if !ts.Enabled || ts.ClosedByOperator {
		return false, nil
	}
	eff, err := b.Features(ctx, t)
	if err != nil {
		return false, err
	}
	return eff[feature.Products] && eff[feature.Orders], nil
}

// bindStorefront binds a storefront call: the shop's team, checked for schema, and (unless open is
// not required) refused with NotFound shop_closed while the shop is closed.
func (b *Binder) bindStorefront(ctx context.Context, shopHost, origin string, requireOpen bool) (context.Context, error) {
	t, legacy, err := b.ShopTeam(ctx, shopHost, origin)
	if err != nil {
		if _, ok := status.FromError(err); ok {
			return nil, err
		}
		return nil, status.Error(codes.Unavailable, "storefront team unavailable")
	}
	if b.SchemaBehind(t) {
		return nil, status.Error(codes.Unavailable, "team is being updated")
	}
	if !legacy && requireOpen {
		open, err := b.ShopOpen(ctx, t)
		if err != nil {
			return nil, status.Error(codes.Unavailable, "shop lookup unavailable")
		}
		if !open {
			return nil, status.Error(codes.NotFound, MsgShopClosed)
		}
	}
	return tenant.With(ctx, t), nil
}

// shopMD reads x-shop-host and the Origin from a call's metadata: the gateway forwards Origin as
// grpcgateway-origin, a direct gRPC client sends origin.
func shopMD(ctx context.Context) (shopHost, origin string) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", ""
	}
	first := func(k string) string {
		if v := md.Get(k); len(v) > 0 {
			return v[0]
		}
		return ""
	}
	origin = first("grpcgateway-origin")
	if origin == "" {
		origin = first("origin")
	}
	return first(ShopHostMD), origin
}

// shopHeaders reads x-shop-host (plain or as Grpc-Metadata-X-Shop-Host) and the Origin of a request.
func shopHeaders(r *http.Request) (shopHost, origin string) {
	shopHost = r.Header.Get(ShopHostHeader)
	if shopHost == "" {
		shopHost = r.Header.Get("Grpc-Metadata-" + ShopHostHeader)
	}
	return shopHost, r.Header.Get("Origin")
}

// isFrontendPath: the storefront API (not the webhooks, which stay the storefront team's).
func isFrontendPath(p string) bool {
	return p == "/api/frontend" || strings.HasPrefix(p, "/api/frontend/")
}

func (p storePlane) ShopHostTeam(ctx context.Context, host string) (int64, bool, error) {
	return p.st.ShopHostTeam(ctx, host)
}

func (p storePlane) TeamShop(ctx context.Context, teamID int64) (platformstore.TeamShop, error) {
	return p.st.TeamShop(ctx, teamID)
}
