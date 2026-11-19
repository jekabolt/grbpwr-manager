package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/middleware"
)

func TestTeamLogoHandlerLimitsEachIP(t *testing.T) {
	s := &Server{rateLimiter: newAuthRateLimiter()}
	t.Cleanup(s.rateLimiter.stop)
	h := s.TeamLogoHandler() // no platform store: allowed requests reach the common 404
	request := func(ip string) int {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, "/api/auth/team-logo/acme", nil)
		r = r.WithContext(context.WithValue(r.Context(), middleware.ClientIPKey, ip))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < teamLogoMaxPerIP; i++ {
		if code := request("192.0.2.1"); code != http.StatusNotFound {
			t.Fatalf("request %d = %d, want route response", i+1, code)
		}
	}
	if code := request("192.0.2.1"); code != http.StatusTooManyRequests {
		t.Fatalf("request above per-IP limit = %d, want 429", code)
	}
	if code := request("192.0.2.2"); code != http.StatusNotFound {
		t.Fatalf("other IP = %d, want an independent bucket", code)
	}
}
