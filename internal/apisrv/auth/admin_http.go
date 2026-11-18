package auth

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jekabolt/grbpwr-manager/internal/feature"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
// The parity with the interceptor is structural, not by imitation: both run adminChain (edge.go)
// — the same JWT verification, team, status gate, Origin lock, session, account state and
// feature gate (feature.ForRoute on the chi route pattern). On a suspended team only GET, HEAD
// and OPTIONS pass (403 team_read_only); a switched-off feature answers 403 feature_off.
//
// Only AuthMetadataKey (Grpc-Metadata-Authorization) is read, because that is the
// only header the gRPC path reads — a bare Authorization header never reaches the
// admin service, so honouring one here would create a way in that does not exist
// anywhere else.
//
// The middleware does NOT check a section. The handler does that itself (see
// RequireAdminSection), which mirrors the "permission shapes the response" pattern already used
// for costing.
func (s *Server) WithAdminAuthz(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.Header.Get(AuthMetadataKey)
		token := strings.TrimSpace(strings.TrimPrefix(raw, "Bearer "))
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized)
			return
		}
		pattern := routePattern(r)
		ctx, err := s.adminChain(r.Context(), adminCall{
			token:    token,
			origin:   r.Header.Get("Origin"),
			host:     r.Host,
			xteam:    httpXTeam(r),
			method:   r.Method + " " + pattern,
			readOnly: r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions,
			features: func() ([]feature.Key, bool) { return feature.ForRoute(r.Method, pattern) },
		})
		if err != nil {
			writeChainError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// routePattern is the chi route pattern of the request (feature.ForRoute keys on it), or the path
// when chi has none.
func routePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		if p := rc.RoutePattern(); p != "" {
			return p
		}
	}
	return r.URL.Path
}

// writeChainError maps an adminChain status to the HTTP answer. 401 and 503 keep the constant
// body; the two 403s name the reason the admin client shows.
func writeChainError(w http.ResponseWriter, err error) {
	switch st, _ := status.FromError(err); st.Code() {
	case codes.Unavailable:
		writeAuthError(w, http.StatusServiceUnavailable)
	case codes.FailedPrecondition:
		writeJSONError(w, http.StatusForbidden, msgTeamReadOnly)
	case codes.PermissionDenied:
		if st.Message() == msgFeatureOff {
			writeJSONError(w, http.StatusForbidden, msgFeatureOff)
		} else {
			writeJSONError(w, http.StatusForbidden, "forbidden")
		}
	default:
		writeAuthError(w, http.StatusUnauthorized)
	}
}

// writeAuthError emits the constant JSON body with the given status.
func writeAuthError(w http.ResponseWriter, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(unauthorizedBody))
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":"` + msg + `"}`))
}

// httpXTeam is the team a plain HTTP request names in its X-Team header (the gateway spelling
// Grpc-Metadata-X-Team included).
func httpXTeam(r *http.Request) string {
	for _, h := range []string{"X-Team", "Grpc-Metadata-X-Team"} {
		if v := strings.TrimSpace(r.Header.Get(h)); v != "" {
			return v
		}
	}
	return ""
}
