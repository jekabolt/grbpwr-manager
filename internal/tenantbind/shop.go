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

// SetAllowedOrigins hands the binder the allowed origins (HTTP_ALLOWED_ORIGINS: dev, the console,
// CORS-only pages). Unless one is a storefront host (SetStorefrontHosts), a request naming its host,
// in x-shop-host or by its Origin, names no shop: it is never looked up and never names a team.
func (b *Binder) SetAllowedOrigins(origins []string) {
	m := map[string]bool{}
	for _, o := range origins {
		if h := shop.OriginHost(o); h != "" {
			m[h] = true
		}
	}
	b.mu.Lock()
	b.corsHosts = m
	b.mu.Unlock()
}

// SetStorefrontHosts hands the binder the storefront team's own shop hosts (TENANT_STOREFRONT_HOSTS:
// grbpwr.com, www.grbpwr.com …; empty without a storefront team). A request naming one, in
// x-shop-host or by its Origin, names the storefront team, so it never lets the other side pick
// another shop.
func (b *Binder) SetStorefrontHosts(hosts []string) {
	m := map[string]bool{}
	for _, h := range hosts {
		if n := shop.NormHost(h); shop.ValidHost(n) {
			m[n] = true
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
	v, err, _ := b.shopFlight.Do(host, func() (any, error) {
		id, ok, err := sp.ShopHostTeam(context.WithoutCancel(ctx), host)
		if err != nil {
			return nil, err
		}
		e := shopHostEntry{teamID: id, ok: ok, expires: now.Add(cacheTTL)}
		b.cacheMu.Lock()
		if b.shopHosts == nil || len(b.shopHosts) >= shopCacheMax {
			b.shopHosts = map[string]shopHostEntry{}
		}
		b.shopHosts[host] = e
		b.cacheMu.Unlock()
		return e, nil
	})
	if err != nil {
		return 0, false, err
	}
	e = v.(shopHostEntry)
	return e.teamID, e.ok, nil
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

// ShopTeam resolves the team of a storefront request from its x-shop-host and Origin. Each side
// names a team: a storefront host (SetStorefrontHosts) the storefront team, an allowed origin
// (SetAllowedOrigins) none, any other host the team of its shop_host row (an unknown Origin names
// none). When both sides name a team they must name the same one. legacy=true: the request names no
// shop or the storefront team's, and runs as the storefront team.
// Refusals: NotFound shop_not_found (x-shop-host names no shop), PermissionDenied shop_mismatch
// (x-shop-host and Origin name two different teams), Unavailable (control plane).
func (b *Binder) ShopTeam(ctx context.Context, shopHost, origin string) (t tenant.Team, legacy bool, err error) {
	sp := b.shopPlane()
	if !b.TeamsMode() || sp == nil {
		t, err := b.StorefrontTeam(ctx)
		return t, true, err
	}
	b.mu.RLock()
	own, cors := b.storefrontHosts, b.corsHosts
	b.mu.RUnlock()
	sf := b.StorefrontTeamID()

	hh := shop.NormHost(shopHost)
	oh := shop.OriginHost(origin)
	// Only a well-formed host name reaches the control plane: a malformed x-shop-host names no shop,
	// a malformed Origin names none either.
	if hh != "" && !shop.ValidHost(hh) {
		return tenant.Team{}, false, status.Error(codes.NotFound, MsgShopNotFound)
	}
	if oh != "" && !shop.ValidHost(oh) {
		oh = ""
	}
	var fromHeader, fromOrigin int64
	switch {
	case hh == "" || (cors[hh] && !own[hh]):
		// no header, or an allowed origin's host: names no shop
	case own[hh] && sf > 0:
		fromHeader = sf
	default:
		id, ok, err := b.hostTeam(ctx, sp, hh)
		if err != nil {
			return tenant.Team{}, false, status.Error(codes.Unavailable, "shop lookup unavailable")
		}
		if !ok {
			return tenant.Team{}, false, status.Error(codes.NotFound, MsgShopNotFound)
		}
		fromHeader = id
	}
	switch {
	case oh == "" || (cors[oh] && !own[oh]):
	case own[oh] && sf > 0:
		fromOrigin = sf
	default:
		id, ok, err := b.hostTeam(ctx, sp, oh)
		if err != nil {
			return tenant.Team{}, false, status.Error(codes.Unavailable, "shop lookup unavailable")
		}
		if ok {
			fromOrigin = id // an unknown Origin (a preview, a page elsewhere) names no shop
		}
	}
	if fromHeader != 0 && fromOrigin != 0 && fromHeader != fromOrigin {
		return tenant.Team{}, false, status.Error(codes.PermissionDenied, MsgShopMismatch)
	}
	id := fromHeader
	if id == 0 {
		id = fromOrigin
	}
	if id == 0 && sf <= 0 {
		// No storefront team (TENANT_STOREFRONT_TEAM_ID=none): a request that names no team shop
		// has no shop to run as.
		return tenant.Team{}, false, status.Error(codes.NotFound, MsgShopNotFound)
	}
	if id == 0 || id == sf {
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
