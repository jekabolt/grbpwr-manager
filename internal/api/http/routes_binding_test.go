package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/platformauth/oauthflow"
	"github.com/jekabolt/grbpwr-manager/internal/tenantbind"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
	"google.golang.org/grpc"
)

// G5 (teams §6): every registered gRPC method and every chi route has exactly one binding class.
// An unknown class is refused in teams mode, so a new route or RPC without one fails here first.

func TestEveryGRPCMethodHasABindingClass(t *testing.T) {
	descs := []grpc.ServiceDesc{
		pb_admin.AdminService_ServiceDesc,
		pb_admin.PlatformService_ServiceDesc,
		pb_frontend.FrontendService_ServiceDesc,
		pb_auth.AuthService_ServiceDesc,
	}
	want := map[string]tenantbind.Class{
		"admin.AdminService":       tenantbind.ClassClaim,
		"admin.PlatformService":    tenantbind.ClassPlatform,
		"frontend.FrontendService": tenantbind.ClassStorefront,
	}
	n := 0
	for _, d := range descs {
		names := make([]string, 0, len(d.Methods)+len(d.Streams))
		for _, m := range d.Methods {
			names = append(names, m.MethodName)
		}
		for _, s := range d.Streams {
			names = append(names, s.StreamName)
		}
		for _, name := range names {
			full := "/" + d.ServiceName + "/" + name
			c := tenantbind.ClassifyMethod(full)
			if c == tenantbind.ClassUnknown {
				t.Errorf("%s has no binding class", full)
				continue
			}
			if w, ok := want[d.ServiceName]; ok && c != w {
				t.Errorf("%s: class %q, want %q", full, c, w)
			}
			if d.ServiceName == "auth.AuthService" && c != tenantbind.ClassLegacyAuth && c != tenantbind.ClassXTeam {
				t.Errorf("%s: class %q, want legacy-auth or x-team", full, c)
			}
			n++
		}
	}
	if n < 100 {
		t.Fatalf("walked only %d methods: the service descriptors are not the real ones", n)
	}
	// The four old AuthService RPCs stay team #1.
	for _, m := range []string{"Login", "Create", "Delete", "ChangePassword"} {
		if c := tenantbind.ClassifyMethod(tenantbind.AuthServicePrefix + m); c != tenantbind.ClassLegacyAuth {
			t.Errorf("AuthService/%s: class %q, want legacy-auth", m, c)
		}
	}
	if c := tenantbind.ClassifyMethod("/x.Other/Do"); c != tenantbind.ClassUnknown {
		t.Errorf("an unregistered service classified %q", c)
	}
}

type nopWebhooks struct{}

func (nopWebhooks) HandleResendEvent(http.ResponseWriter, *http.Request)             {}
func (nopWebhooks) HandleListUnsubscribe(http.ResponseWriter, *http.Request)         {}
func (nopWebhooks) HandleCampaignListUnsubscribe(http.ResponseWriter, *http.Request) {}
func (nopWebhooks) HandleStripeEvent(http.ResponseWriter, *http.Request)             {}
func (nopWebhooks) HandleAftershipEvent(http.ResponseWriter, *http.Request)          {}

func TestEveryChiRouteHasABindingClass(t *testing.T) {
	nop := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	s := New(&Config{Address: "127.0.0.1", Port: "8081"})
	// Every optional handler set, so every route the production router can mount is walked.
	s.SetPatternAccessHandler(nop)
	s.SetPatternViewerHandler(nop)
	s.SetRunPackHandler(nop)
	s.SetFileLinkHandler(nop)
	s.SetTeamLogoHandler(nop)
	oauthH := oauthflow.New(oauthflow.Deps{})
	defer oauthH.Close()
	s.SetOAuthHandler(oauthH)
	s.SetFileUploadHandler(nop)
	s.SetFilePreviewHandler(nop)
	s.SetTechCardArchiveUploadHandler(nop)
	s.SetFileProxyHandler(nop)
	s.SetWebhookHandler(nopWebhooks{})
	s.SetStripeWebhookHandler(nopWebhooks{})
	s.SetAftershipWebhookHandler(nopWebhooks{})
	s.SetHealthRegistry(&health.Registry{})
	s.platformOn = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, err := s.setupHTTPAPI(ctx, &auth.Server{})
	if err != nil {
		t.Fatal(err)
	}
	router, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("setupHTTPAPI returned %T, not a chi router", h)
	}

	seen := map[string]bool{}
	err = chi.Walk(router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		seen[route] = true
		if c := tenantbind.ClassifyPath(route); c == tenantbind.ClassUnknown {
			t.Errorf("%s %s has no binding class", method, route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The mounts and routes that must be there (a walk over an empty router would pass vacuously).
	for _, r := range []string{
		"/livez", "/readyz", "/statusz",
		"/api/admin/*", "/api/frontend/*", "/api/auth/*", "/api/platform/*",
		"/api/p/{token}", "/api/pv/{token}", "/api/rp/{token}", "/api/f/{token}",
		"/api/files/upload", "/api/files/{id}/preview", "/api/techcard-archive/upload",
		"/api/files/proxy", "/api/webhooks/stripe", "/api/auth/team-logo/{slug}",
		"/api/auth/oauth/{provider}/start", "/api/auth/oauth/google/callback", "/api/auth/oauth/apple/callback",
	} {
		if !seen[r] {
			t.Errorf("route %s not walked", r)
		}
	}

	expect := map[string]tenantbind.Class{
		"/api/admin/*":                     tenantbind.ClassClaim,
		"/api/files/{id}/preview":          tenantbind.ClassClaim,
		"/api/files/proxy":                 tenantbind.ClassClaim,
		"/statusz":                         tenantbind.ClassClaim,
		"/api/platform/*":                  tenantbind.ClassPlatform,
		"/api/frontend/*":                  tenantbind.ClassStorefront,
		"/api/webhooks/stripe":             tenantbind.ClassStorefront,
		"/api/p/{token}":                   tenantbind.ClassToken,
		"/api/auth/*":                      tenantbind.ClassXTeam,
		"/api/auth/team-logo/{slug}":       tenantbind.ClassXTeam,
		"/api/auth/oauth/{provider}/start": tenantbind.ClassXTeam,
		"/api/auth/oauth/google/callback":  tenantbind.ClassXTeam,
		"/api/auth/oauth/apple/callback":   tenantbind.ClassXTeam,
		"/livez":                           tenantbind.ClassHealth,
	}
	for r, want := range expect {
		if got := tenantbind.ClassifyPath(r); got != want {
			t.Errorf("%s: class %q, want %q", r, got, want)
		}
	}
	if got := tenantbind.ClassifyPath("/api/nothing"); got != tenantbind.ClassUnknown {
		t.Errorf("an unmounted /api path classified %q", got)
	}
	if got := tenantbind.ClassifyPath("/api/pvx"); got != tenantbind.ClassUnknown {
		t.Errorf("/api/pvx classified %q: prefixes must stop at a segment boundary", got)
	}
}
