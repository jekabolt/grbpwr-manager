package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalDevWriteGuard(t *testing.T) {
	tests := []struct {
		name                   string
		method                 string
		origin                 string
		referer                string
		blockLocalOriginWrites bool
		wantStatus             int
		wantReached            bool
	}{
		{
			name:                   "guard on rejects localhost POST",
			method:                 http.MethodPost,
			origin:                 "http://localhost:3000",
			blockLocalOriginWrites: true,
			wantStatus:             http.StatusForbidden,
			wantReached:            false,
		},
		{
			name:                   "guard on passes localhost GET",
			method:                 http.MethodGet,
			origin:                 "http://localhost:3000",
			blockLocalOriginWrites: true,
			wantStatus:             http.StatusNoContent,
			wantReached:            true,
		},
		{
			name:                   "guard on passes POST without source headers",
			method:                 http.MethodPost,
			blockLocalOriginWrites: true,
			wantStatus:             http.StatusNoContent,
			wantReached:            true,
		},
		{
			name:                   "guard on passes grbpwr.com POST",
			method:                 http.MethodPost,
			origin:                 "https://grbpwr.com",
			blockLocalOriginWrites: true,
			wantStatus:             http.StatusNoContent,
			wantReached:            true,
		},
		{
			name:                   "guard on rejects localhost Referer POST",
			method:                 http.MethodPost,
			referer:                "https://admin.localhost:5173/products/1",
			blockLocalOriginWrites: true,
			wantStatus:             http.StatusForbidden,
			wantReached:            false,
		},
		{
			name:        "guard off passes localhost POST",
			method:      http.MethodPost,
			origin:      "http://localhost:3000",
			wantStatus:  http.StatusNoContent,
			wantReached: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reached := false
			next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				reached = true
				w.WriteHeader(http.StatusNoContent)
			})
			var handler http.Handler = next
			if tc.blockLocalOriginWrites {
				handler = localDevWriteGuard(handler)
			}
			// Beta keeps credentialed localhost CORS disabled while disabling only
			// the independent write fence.
			handler = corsMiddleware([]string{"https://grbpwr.com"}, false)(handler)

			req := httptest.NewRequest(tc.method, "/api/frontend/test", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			if tc.referer != "" {
				req.Header.Set("Referer", tc.referer)
			}
			resp := httptest.NewRecorder()

			handler.ServeHTTP(resp, req)

			if resp.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", resp.Code, tc.wantStatus, resp.Body.String())
			}
			if reached != tc.wantReached {
				t.Fatalf("next handler reached = %t, want %t", reached, tc.wantReached)
			}
			if tc.wantStatus == http.StatusForbidden {
				if got := resp.Header().Get("Content-Type"); got != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", got)
				}
				if !json.Valid(resp.Body.Bytes()) {
					t.Errorf("forbidden response is not valid JSON: %q", resp.Body.String())
				}
			}
		})
	}
}

func TestLocalDevWriteGuardHostCoverage(t *testing.T) {
	tests := []struct {
		method string
		origin string
	}{
		{http.MethodPut, "http://127.0.0.1:8080"},
		{http.MethodPatch, "https://[::1]:8443"},
		{http.MethodDelete, "https://shop.localhost:3000"},
	}
	for _, test := range tests {
		t.Run(test.origin, func(t *testing.T) {
			reached := false
			handler := localDevWriteGuard(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				reached = true
			}))
			req := httptest.NewRequest(test.method, "/api/admin/test", nil)
			req.Header.Set("Origin", test.origin)
			resp := httptest.NewRecorder()

			handler.ServeHTTP(resp, req)

			if resp.Code != http.StatusForbidden {
				t.Errorf("status = %d, want %d", resp.Code, http.StatusForbidden)
			}
			if reached {
				t.Error("next handler was reached")
			}
		})
	}
}
