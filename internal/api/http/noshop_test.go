package httpapi

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	authjwt "github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

// noShopRouter is the production router with every optional handler set. Port 1: nothing listens
// there, so a gateway that is mounted fails its loopback dial instead of reaching a local server.
func noShopRouter(t *testing.T, noShop bool, as *auth.Server, reg *health.Registry) http.Handler {
	t.Helper()
	s := New(&Config{Address: "127.0.0.1", Port: "1"})
	s.SetWebhookHandler(nopWebhooks{})
	s.SetStripeWebhookHandler(nopWebhooks{})
	s.SetAftershipWebhookHandler(nopWebhooks{})
	if reg == nil {
		reg = &health.Registry{}
	}
	s.SetHealthRegistry(reg)
	s.SetNoShop(noShop)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h, err := s.setupHTTPAPI(ctx, as)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// newTestAuth is a real auth server (mode OFF) whose admin lookup always finds admin #1.
func newTestAuth(t *testing.T) *auth.Server {
	t.Helper()
	ar := mocks.NewMockAdmin(t)
	ar.EXPECT().GetAdminByUsername(mock.Anything, mock.Anything).Return(&entity.Admin{Id: 1}, nil).Maybe()
	as, err := auth.New(&auth.Config{
		JWTSecret:                "statusz-test-secret-of-32-bytes!",
		MasterPassword:           "master-password",
		PasswordHasherSaltSize:   16,
		PasswordHasherIterations: 100000,
		JWTTTL:                   "60m",
	}, ar)
	if err != nil {
		t.Fatal(err)
	}
	return as
}

func serve(h http.Handler, method, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set(auth.AuthMetadataKey, "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestNoShopStorefrontRoutesAnswer503 — no shop: the storefront gateway and every shop webhook
// answer 503 with the fixed body; the admin gateway is still mounted and reaches its auth.
func TestNoShopStorefrontRoutesAnswer503(t *testing.T) {
	h := noShopRouter(t, true, newTestAuth(t), nil)
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/frontend/hero"},
		{http.MethodPost, "/api/frontend/order/submit"},
		{http.MethodPost, "/api/webhooks/stripe"},
		{http.MethodPost, "/api/webhooks/aftership"},
		{http.MethodPost, "/api/webhooks/resend"},
		{http.MethodPost, "/api/webhooks/list-unsubscribe/e/t"},
	} {
		rec := serve(h, c.method, c.path, "")
		if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != noShopBody {
			t.Errorf("%s %s: %d %q, want 503 %q", c.method, c.path, rec.Code, rec.Body.String(), noShopBody)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Errorf("%s %s: Content-Type %q", c.method, c.path, ct)
		}
	}
	rec := serve(h, http.MethodPost, "/api/admin/product/get", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("admin route without a token: %d %q, want the auth 401", rec.Code, rec.Body.String())
	}

	// The walk sees one webhook catch-all and no shop handler behind it.
	seen := map[string]bool{}
	_ = chi.Walk(h.(chi.Routes), func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[route] = true
		return nil
	})
	if !seen["/api/webhooks/*"] || seen["/api/webhooks/stripe"] || !seen["/api/admin/*"] || !seen["/api/frontend/*"] {
		t.Fatalf("no-shop routes: %v", seen)
	}
}

// TestShopMountsTheStorefront — with a shop (mode OFF or a storefront team id) the routes are
// today's: the webhooks reach their handlers and the frontend gateway is the real one.
func TestShopMountsTheStorefront(t *testing.T) {
	h := noShopRouter(t, false, newTestAuth(t), nil)
	if rec := serve(h, http.MethodPost, "/api/webhooks/stripe", ""); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("stripe webhook with a shop: %d %q, want the handler's empty 200", rec.Code, rec.Body.String())
	}
	if rec := serve(h, http.MethodGet, "/api/frontend/hero", ""); rec.Body.String() == noShopBody {
		t.Fatal("the frontend gateway answered the no-shop body with a shop")
	}
}

// TestNoShopGRPC — FrontendService is not registered; a call to it answers Unavailable with the
// fixed message before the tenant and auth interceptors see it. Other unknown services keep
// grpc-go's Unimplemented; registered services still run.
func TestNoShopGRPC(t *testing.T) {
	s := New(&Config{})
	s.SetNoShop(true)
	refuseAll := func(any, grpc.ServerStream, *grpc.StreamServerInfo, grpc.StreamHandler) error {
		return status.Error(codes.PermissionDenied, "auth")
	}
	gs := grpc.NewServer(grpc.ChainStreamInterceptor(s.noShopStreamInterceptor(), refuseAll), s.unknownServiceOption())
	lis := bufconn.Listen(1 << 20)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = pb_frontend.NewFrontendServiceClient(conn).GetHero(ctx, &pb_frontend.GetHeroRequest{})
	if st := status.Convert(err); st.Code() != codes.Unavailable || st.Message() != noShopMessage {
		t.Fatalf("FrontendService call: %v, want Unavailable %q", err, noShopMessage)
	}
	err = conn.Invoke(ctx, "/x.Other/Do", &pb_frontend.GetHeroRequest{}, &pb_frontend.GetHeroResponse{})
	if st := status.Convert(err); st.Code() != codes.PermissionDenied {
		t.Fatalf("another unknown service: %v, want it to pass on to the next interceptor", err)
	}

	// The handler itself, for the unknown service that gets past the interceptors.
	if st := status.Convert(noShopStream(nil, nil, &grpc.StreamServerInfo{FullMethod: "/admin.AdminService/X"},
		func(any, grpc.ServerStream) error { return nil })); st.Code() != codes.OK {
		t.Fatalf("admin call stopped: %v", st)
	}

	// With a shop the interceptor passes FrontendService through and no unknown handler is set.
	s.SetNoShop(false)
	called := false
	_ = s.noShopStreamInterceptor()(nil, nil, &grpc.StreamServerInfo{FullMethod: frontendMethodPrefix() + "GetHero"},
		func(any, grpc.ServerStream) error { called = true; return nil })
	if !called {
		t.Fatal("with a shop a FrontendService call was stopped")
	}
	if _, ok := s.unknownServiceOption().(grpc.EmptyServerOption); !ok {
		t.Fatal("with a shop the unknown-service handler is installed")
	}
}

// TestStatuszAnalyticsOff — review item 7: GET /statusz answers 200 to an admin with GA4 and
// BigQuery off, even when the clients reach the registry as typed nils (the old wiring).
func TestStatuszAnalyticsOff(t *testing.T) {
	as := newTestAuth(t)
	var nilBQ *bq.Client
	var nilGA4 *ga4.Client
	reg := &health.Registry{Breakers: []health.BreakerReporter{
		{BreakerName: "ga4", StateFunc: func() circuitbreaker.State { return nilGA4.CircuitBreakerState() }},
		{BreakerName: "bigquery", StateFunc: func() circuitbreaker.State { return nilBQ.CircuitBreakerState() }},
	}, Problems: []string{"shops: STOREFRONT_AUTH_SHOP_MASTER is empty"}}
	h := noShopRouter(t, true, as, reg)
	tok, err := authjwt.NewAdminToken(as.JwtAuth, time.Hour, "u", true, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(h, http.MethodGet, "/statusz", tok)
	body, _ := io.ReadAll(rec.Body)
	if rec.Code != http.StatusOK || !strings.Contains(string(body), `"workers"`) {
		t.Fatalf("/statusz for an admin with analytics off: %d %s", rec.Code, body)
	}
	if !strings.Contains(string(body), `"problems":["shops: STOREFRONT_AUTH_SHOP_MASTER is empty"]`) {
		t.Fatalf("/statusz must list boot problems: %s", body)
	}
	if rec := serve(h, http.MethodGet, "/statusz", ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("/statusz without a token: %d", rec.Code)
	}
}
