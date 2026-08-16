package auth

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/auth/jwt"
	"github.com/jekabolt/grbpwr-manager/internal/rbac"
)

// unauthorizedBody is a constant response body: which stage of verification
// failed is server-side information, exactly as in the gRPC interceptor.
const unauthorizedBody = `{"error":"unauthorized"}`

// WithAdminAuthz authenticates a plain-HTTP request the same way the admin gRPC
// interceptor authenticates a call, and puts the resulting identity into the
// request context.
//
// WHY THIS EXISTS AT ALL. The library's upload endpoint is the only piece of the
// admin API that is not a gRPC method, because a file cannot ride inside one
// message. That means UnaryAdminAuthInterceptor never sees it, and the generic
// WithAuth middleware is NOT a substitute: it verifies a token's signature and
// expiry but knows nothing about admin claims, so a perfectly valid storefront
// customer token would sail through it.
//
// The parity with the interceptor is structural, not by imitation: the same
// s.JwtAuth, the same s.jwtExpectations, the same jwt.VerifyAdminToken. Any
// future change to how admin tokens are verified lands on both at once.
//
// Only AuthMetadataKey (Grpc-Metadata-Authorization) is read, because that is the
// only header the gRPC path reads — a bare Authorization header never reaches the
// admin service, so honouring one here would create a way in that does not exist
// anywhere else.
//
// The middleware authenticates and populates; it does NOT check a section. The
// handler does that itself (see RequireAdminSection), which mirrors the
// "permission shapes the response" pattern already used for costing.
func (s *Server) WithAdminAuthz(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(AuthMetadataKey)
		if raw == "" {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		token := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		sub, super, permStrs, legacy, err := jwt.VerifyAdminToken(s.JwtAuth, token, s.jwtExpectations)
		if err != nil {
			slog.Default().WarnContext(r.Context(), "invalid admin auth token on http endpoint",
				slog.String("path", r.URL.Path), slog.String("err", err.Error()))
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		ctx := r.Context()
		if sub != "" {
			ctx = PutAdminUsername(ctx, sub)
		}
		ctx = putAdminAuthz(ctx, AdminAuthz{
			Legacy: legacy,
			Super:  super,
			Perms:  rbac.ParsePermissions(permStrs),
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeAuthError emits the constant JSON body with the given status.
func writeAuthError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(unauthorizedBody))
}
