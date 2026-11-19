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

// preflight runs a CORS preflight through mw and returns the Access-Control-Allow-Origin it got.
func preflight(t *testing.T, mw func(http.Handler) http.Handler, origin string) string {
	t.Helper()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodOptions, "/api/admin/x", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "Grpc-Metadata-X-Team")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Header().Get("Access-Control-Allow-Origin")
}

// Teams §14: team hosts reach the API by HTTP_ALLOWED_ORIGIN_REGEX, on top of the static list.
func TestCORSOriginRegex(t *testing.T) {
	const re = `^https://([a-z0-9-]{1,40}\.)?beta\.lekalo\.studio$`
	mw, err := buildCORS([]string{"https://admin.beta.grbpwr.com"}, false, re)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range []string{
		"https://beta.lekalo.studio",
		"https://acme.beta.lekalo.studio",
		"https://ACME.beta.lekalo.studio",
		"https://admin.beta.grbpwr.com", // the static list still applies
	} {
		if got := preflight(t, mw, o); got != o {
			t.Errorf("%s refused (Allow-Origin %q)", o, got)
		}
	}
	for _, o := range []string{
		"https://lekalo.studio.evil.com",
		"https://acme.beta.lekalo.studio.evil.com",
		"https://a.b.beta.lekalo.studio",
		"http://acme.beta.lekalo.studio",
		"https://evil.com",
	} {
		if got := preflight(t, mw, o); got != "" {
			t.Errorf("%s allowed (Allow-Origin %q)", o, got)
		}
	}
}

func TestCORSOriginRegexValidation(t *testing.T) {
	for _, bad := range []string{
		`https://([a-z]+\.)?lekalo\.studio$`, // no ^
		`^https://([a-z]+\.)?lekalo\.studio`, // no $
		`^https://([a-z+\.)?lekalo\.studio$`, // does not compile
	} {
		if _, err := buildCORS(nil, false, bad); err == nil {
			t.Errorf("regex %q accepted", bad)
		}
	}
	if re, err := compileOriginRegex(""); err != nil || re != nil {
		t.Fatalf("empty regex: %v %v", re, err)
	}
}

// Without the regex the list behaves exactly as before (mode OFF, envs unset).
func TestCORSListUnchangedWithoutRegex(t *testing.T) {
	list := []string{"https://admin.grbpwr.com", "https://*.vercel.app"}
	for _, mw := range []func(http.Handler) http.Handler{corsMiddleware(list, false), mustCORS(t, list, "")} {
		for o, want := range map[string]bool{
			"https://admin.grbpwr.com":     true,
			"https://preview-1.vercel.app": true,
			"https://acme.lekalo.studio":   false,
			"https://evil.com":             false,
		} {
			if got := preflight(t, mw, o) == o; got != want {
				t.Errorf("%s: allowed=%v, want %v", o, got, want)
			}
		}
	}
	// An empty list without a regex fails closed.
	if got := preflight(t, mustCORS(t, nil, ""), "https://admin.grbpwr.com"); got != "" {
		t.Errorf("empty list allowed an origin: %q", got)
	}
}

func mustCORS(t *testing.T, list []string, re string) func(http.Handler) http.Handler {
	t.Helper()
	mw, err := buildCORS(list, false, re)
	if err != nil {
		t.Fatal(err)
	}
	return mw
}
