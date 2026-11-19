package tenantbind

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// readyz mimics the readiness handler over the strict store: no team in ctx → ErrNoTenant → 503.
func readyz(pingErr error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := tenant.From(r.Context()); !ok {
			http.Error(w, tenant.ErrNoTenant.Error(), http.StatusServiceUnavailable)
			return
		}
		if pingErr != nil {
			http.Error(w, pingErr.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// Fix-round item 1: in teams mode /readyz (no token, no team host) pings team #1's database, the
// boot team; a down database still answers 503. Mode OFF is unchanged.
func TestReadyzInTeamsMode(t *testing.T) {
	tb, _ := teamsBinder(t)
	for name, b := range map[string]*Binder{"teams mode": tb, "mode off": newBinder(t)} {
		for _, c := range []struct {
			path    string
			pingErr error
			want    int
		}{
			{"/readyz", nil, http.StatusOK},
			{"/readyz", errors.New("db down"), http.StatusServiceUnavailable},
			{"/health", nil, http.StatusOK},
		} {
			req := httptest.NewRequest(http.MethodGet, c.path, nil)
			req.Host = "api.lekalo.studio"
			rec := httptest.NewRecorder()
			b.Middleware(readyz(c.pingErr)).ServeHTTP(rec, req)
			if rec.Code != c.want {
				t.Errorf("%s %s ping=%v: %d, want %d (%s)", name, c.path, c.pingErr, rec.Code, c.want, rec.Body.String())
			}
		}
	}
}
