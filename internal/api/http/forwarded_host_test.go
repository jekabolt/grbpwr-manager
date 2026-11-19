package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc/metadata"
)

// Fix-round item 2: the grpc-gateway prefers a client X-Forwarded-Host over r.Host when it
// annotates x-forwarded-host. The root middleware drops the client header, so the annotation the
// admin chain trusts on the loopback hop is the Host the request really came to.
func TestClientForwardedHostDoesNotReachTheGateway(t *testing.T) {
	var got []string
	h := stripForwardedHost(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := runtime.AnnotateContext(context.Background(), runtime.NewServeMux(), r, "/admin.AdminService/ListOrders")
		if err != nil {
			t.Fatal(err)
		}
		md, _ := metadata.FromOutgoingContext(ctx)
		got = md.Get("x-forwarded-host")
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/admin/orders", nil)
	req.Host = "grbpwr.lekalo.studio"
	req.Header.Set("X-Forwarded-Host", "acme.lekalo.studio")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if len(got) != 1 || got[0] != "grbpwr.lekalo.studio" {
		t.Fatalf("gateway annotated x-forwarded-host %v, want the request Host", got)
	}
}
