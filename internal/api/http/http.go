package httpapi

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"net/http"
	goruntime "runtime"
	"runtime/debug"
	"strings"
	"text/template"
	"time"

	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"log/slog"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protojson"

	grpcSlog "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	grpcRecovery "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/log"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

var (
	//go:embed static
	fs embed.FS

	pages = map[string]string{
		"/": "static/swagger/index.html",
	}
)

// HealthChecker defines an interface for checking application health
type HealthChecker interface {
	CheckHealth(ctx context.Context) error
}

// DatabaseHealthChecker implements HealthChecker for database health checks
type DatabaseHealthChecker struct {
	pingFunc func(ctx context.Context) error
}

// NewDatabaseHealthChecker creates a new database health checker
func NewDatabaseHealthChecker(pingFunc func(ctx context.Context) error) *DatabaseHealthChecker {
	return &DatabaseHealthChecker{
		pingFunc: pingFunc,
	}
}

// CheckHealth checks database connectivity
func (d *DatabaseHealthChecker) CheckHealth(ctx context.Context) error {
	if d.pingFunc == nil {
		return fmt.Errorf("ping function not set")
	}
	return d.pingFunc(ctx)
}

// Config is the configuration for the http server
type Config struct {
	Port           string   `mapstructure:"port"`
	Address        string   `mapstructure:"address"`
	AllowedOrigins []string `mapstructure:"allowed_origins"`
	// AllowDevOrigins, when true, additionally permits localhost/127.0.0.1 CORS
	// origins. It must stay false (unset) in prod/beta since CORS allows
	// credentials; enable it only for local development.
	AllowDevOrigins bool   `mapstructure:"allow_dev_origins"`
	CommitHash      string `mapstructure:"commit_hash"`
}

// HTTP server timeouts used to harden against slow-loris style DoS without
// breaking long-lived h2c gRPC streams / SSE or large media uploads.
const (
	// serverReadHeaderTimeout caps how long a client may take to send the
	// request headers before the connection is dropped.
	serverReadHeaderTimeout = 10 * time.Second
	// serverIdleTimeout reaps idle keep-alive connections.
	serverIdleTimeout = 120 * time.Second
	// serverMaxHeaderBytes bounds the size of request headers (1 MiB).
	serverMaxHeaderBytes = 1 << 20
)

// gRPC server limits and keepalive parameters.
//
// IMPORTANT: the grpc-gateway REST/JSON gateway connects to THIS gRPC server
// over loopback via grpc.Dial (see *JSONGateway funcs: insecure credentials, no
// client-side keepalive). The gateway's client connection is therefore long
// lived and mostly carries short unary RPCs (often with no active streams). The
// values below are chosen so neither that loopback connection nor long-lived
// frontend gRPC streams get throttled or force-closed:
//   - MaxConnectionAge / MaxConnectionAgeGrace are intentionally NOT set: a
//     bounded age would periodically GOAWAY+tear down the gateway's own loopback
//     connection (and any in-flight long stream) for no security benefit on a
//     trusted loopback peer.
//   - MaxConnectionIdle is generous (15m); when it fires the server sends GOAWAY
//     and grpc-go transparently reconnects on the next call, so it only reaps
//     genuinely idle/half-open peers.
//   - Enforcement uses PermitWithoutStream:true and a modest MinTime so a
//     well-behaved client that pings without active streams is never dropped.
const (
	// grpcMaxRecvMsgSize / grpcMaxSendMsgSize cap inbound/outbound message size.
	// Send must be set explicitly: the grpc-go default send cap is ~4MiB, which
	// would silently truncate large responses while recv allows 50MiB.
	grpcMaxRecvMsgSize = 50 * 1024 * 1024 // 50 MiB
	grpcMaxSendMsgSize = 50 * 1024 * 1024 // 50 MiB

	// grpcMaxConcurrentStreams bounds per-connection HTTP/2 stream fan-out so a
	// single client connection cannot exhaust server resources.
	grpcMaxConcurrentStreams = 1000

	// grpcKeepaliveMaxConnectionIdle reaps connections idle (no outstanding RPCs)
	// for this long by sending a GOAWAY; clients transparently reconnect.
	grpcKeepaliveMaxConnectionIdle = 15 * time.Minute
	// grpcKeepaliveTime is how long the server waits with no activity before
	// sending a keepalive ping to detect half-open connections.
	grpcKeepaliveTime = 1 * time.Minute
	// grpcKeepaliveTimeout is how long the server waits for a ping ack before
	// closing the connection.
	grpcKeepaliveTimeout = 20 * time.Second

	// grpcKeepaliveMinTime is the minimum interval a client must wait between
	// keepalive pings; paired with PermitWithoutStream:true so compliant clients
	// (including the loopback gateway) are never disconnected for pinging.
	grpcKeepaliveMinTime = 30 * time.Second
)

// WebhookHandler handles inbound webhook HTTP requests.
type WebhookHandler interface {
	HandleResendEvent(w http.ResponseWriter, r *http.Request)
	HandleListUnsubscribe(w http.ResponseWriter, r *http.Request)
	HandleCampaignListUnsubscribe(w http.ResponseWriter, r *http.Request)
}

// StripeWebhookHandler handles inbound Stripe webhook events (signature-verified).
type StripeWebhookHandler interface {
	HandleStripeEvent(w http.ResponseWriter, r *http.Request)
}

// AftershipWebhookHandler handles inbound AfterShip tracking webhook events (signature-verified).
type AftershipWebhookHandler interface {
	HandleAftershipEvent(w http.ResponseWriter, r *http.Request)
}

// Server is the http server
type Server struct {
	hs                      *http.Server
	gs                      *grpc.Server
	c                       *Config
	done                    chan struct{}
	healthChecker           HealthChecker
	webhookHandler          WebhookHandler
	patternAccessHandler    http.Handler
	patternViewerHandler    http.Handler
	runPackHandler          http.Handler
	fileUploadHandler       http.Handler
	filePreviewHandler      http.Handler
	fileLinkHandler         http.Handler
	stripeWebhookHandler    StripeWebhookHandler
	aftershipWebhookHandler AftershipWebhookHandler
	healthRegistry          *health.Registry
}

// New creates a new server
func New(config *Config) *Server {
	return &Server{
		c:    config,
		done: make(chan struct{}),
	}
}

// SetHealthChecker sets an optional health checker for readiness probes
func (s *Server) SetHealthChecker(checker HealthChecker) {
	s.healthChecker = checker
}

// SetPatternAccessHandler registers the tokenized pattern read endpoint (/api/p/{token}).
// Token-guarded, deliberately outside admin auth — <object>/<iframe>/QR consumers cannot
// send headers; mounted inside /api so CORS applies to the ?mode=json variant.
func (s *Server) SetPatternAccessHandler(h http.Handler) {
	s.patternAccessHandler = h
}

// SetFileUploadHandler registers the files-library multipart upload endpoint
// (POST /api/files/upload). Unlike the token-guarded pattern endpoints, this one
// IS authenticated — the caller is expected to pass it already wrapped in the
// admin authorization middleware. It lives outside the gateway mount because a
// file cannot ride inside a single gRPC message.
func (s *Server) SetFileUploadHandler(h http.Handler) {
	s.fileUploadHandler = h
}

// SetFilePreviewHandler registers the preview-replacement endpoint
// (POST /api/files/{id}/preview). Same posture as the upload above — already
// wrapped in admin authorization by the caller — but a far smaller body cap: it
// carries a thumbnail, not a file.
func (s *Server) SetFilePreviewHandler(h http.Handler) {
	s.filePreviewHandler = h
}

// SetPatternViewerHandler registers the card-level pattern viewer manifest endpoint
// (/api/pv/{token}) — the JSON the public viewer page behind printed tech-pack QR codes
// fetches. Same posture as /api/p: the token is the credential, no auth wrapper, and the
// SPA fetches cross-origin so it must sit inside the CORS'd /api group.
func (s *Server) SetPatternViewerHandler(h http.Handler) {
	s.patternViewerHandler = h
}

// SetRunPackHandler registers the public production-run pack manifest endpoint
// (/api/rp/{token}) — the JSON the cutting floor's phone fetches from the QR printed on a
// batch order. Same posture as /api/p and /api/pv: the token is the credential, no auth
// wrapper, inside the CORS'd /api group.
func (s *Server) SetRunPackHandler(h http.Handler) {
	s.runPackHandler = h
}

// SetFileLinkHandler registers the public library-file link endpoint (/api/f/{token}, Ф7) —
// the url a person OUTSIDE the company opens. Same posture as /api/p, /api/pv and /api/rp: the
// token is the credential, no auth wrapper, inside the CORS'd /api group.
//
// ОТ ДВУХ СОСЕДЕЙ ВЫШЕ ОН ОТЛИЧАЕТСЯ ОДНИМ: у файла есть уровень доступа, и маршрут обязан
// проверять его на строке файла, а не только поколение токена. Живой токен на файле, который
// вернули в `team`, обязан быть мёртв — см. internal/fileaccess.
func (s *Server) SetFileLinkHandler(h http.Handler) {
	s.fileLinkHandler = h
}

// SetWebhookHandler registers the webhook handler for Resend and list-unsubscribe routes.
func (s *Server) SetWebhookHandler(h WebhookHandler) {
	s.webhookHandler = h
}

// SetStripeWebhookHandler registers the handler for Stripe webhook events.
func (s *Server) SetStripeWebhookHandler(h StripeWebhookHandler) {
	s.stripeWebhookHandler = h
}

// SetAftershipWebhookHandler registers the handler for AfterShip tracking webhook events.
func (s *Server) SetAftershipWebhookHandler(h AftershipWebhookHandler) {
	s.aftershipWebhookHandler = h
}

// SetHealthRegistry registers the operational-state registry surfaced by the
// admin-gated GET /statusz endpoint (DB pool, per-worker liveness, breakers,
// runtime). Optional: when unset, /statusz is not mounted.
func (s *Server) SetHealthRegistry(r *health.Registry) {
	s.healthRegistry = r
}

// Done returns a channel that is closed when gRPC server exits
func (s *Server) Done() <-chan struct{} {
	return s.done
}

// Shutdown gracefully drains in-flight requests and stops the listener. It must
// be called before the database is closed so handlers do not race against a
// closed connection pool. The drain is bounded by ctx.
//
// gRPC is served over h2c through the HTTP server (s.gs.ServeHTTP), not via a
// dedicated gRPC listener, so draining the HTTP server is what finishes in-flight
// unary RPCs and closes the connections. We therefore drain s.hs first, then hard
// Stop the gRPC server to release its resources — GracefulStop would block on
// keep-alive HTTP/2 connections that only close once s.hs has shut down.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}

	var err error
	if s.hs != nil {
		// Returns http.ErrServerClosed from ListenAndServe, which closes s.done.
		err = s.hs.Shutdown(ctx)
	}
	if s.gs != nil {
		s.gs.Stop()
	}
	return err
}

// corsDevOrigins are localhost/loopback origins permitted ONLY when dev origins
// are explicitly enabled (allowDevOrigins). They must never be present in prod,
// since AllowCredentials is true.
var corsDevOrigins = []string{
	"http://localhost*",
	"http://127.0.0.1*",
}

// corsMiddleware builds the CORS handler. Because AllowCredentials is true, the
// set of allowed origins is an EXPLICIT allowlist of the real prod/beta
// frontends supplied via HTTP_ALLOWED_ORIGINS (e.g. https://grbpwr.com,
// https://admin.grbpwr.com and their beta.* counterparts). The previous broad
// credentialed wildcards (https://*.vercel.app, https://*.github.io) are removed:
// they let any attacker-controlled *.vercel.app / *.github.io deployment make
// credentialed cross-origin calls. Localhost dev origins are gated behind
// allowDevOrigins (env-driven; off in prod) so they never widen the prod surface.
// maxJSONBodyBytes caps frontend/auth JSON request bodies. The grpc-gateway
// marshaler buffers the whole body into memory before the loopback gRPC hop, so an
// unbounded body is a memory-exhaustion / JSON-bomb vector. Admin is capped higher
// (maxAdminJSONBodyBytes) because it carries base64 media uploads.
const maxJSONBodyBytes = 4 << 20 // 4 MB

// maxAdminJSONBodyBytes caps admin REST/JSON request bodies. Admin media uploads carry
// raw bytes base64-encoded in the JSON body (proto `bytes` over grpc-gateway), which
// inflates the largest raw payload — video, maxVideoPayloadBytes = 50 MiB — by ~4/3 to
// ~66.7 MiB on the wire. The cap must therefore sit above that expanded size, so it is
// deliberately larger than grpcMaxRecvMsgSize (which bounds the post-base64-decode gRPC
// hop, not the JSON body). Reusing grpcMaxRecvMsgSize here would silently reject any
// video over ~37.5 MiB (= 50 MiB × 3/4) before it reached the handler.
const maxAdminJSONBodyBytes = 72 << 20 // 72 MiB (base64 of a 50 MiB video ≈ 66.7 MiB + JSON envelope)

// maxFileUploadBodyBytes caps a files-library upload. It is NOT related to the
// admin-JSON cap above: that one exists because base64 inflates a payload inside
// a gRPC message, whereas this is a raw multipart stream that never expands.
//
// 95 MiB is chosen to sit just under the request-body ceiling the platform in
// front of the app is believed to impose (~100 MB on DigitalOcean App Platform —
// NOT verified, and worth verifying with a real large file before relying on it).
// Being the smaller of the two limits matters: our own limit produces a clean 413
// with a message, whereas the platform's produces a truncated body, which reaches
// the handler as an unexpected EOF and needs its own log line to be told apart
// from a person closing the tab.
//
// Files above this go through presigned PUT straight to the bucket — which needs
// a CORS policy on the bucket that only its owner can set, hence not in this pass.
const maxFileUploadBodyBytes = 95 << 20 // 95 MiB

// maxFilePreviewBodyBytes caps a preview replacement. Deliberately NOT the upload
// cap: this body carries one thumbnail (bounded at 2 MiB by the handler itself)
// and nothing else, so the 95 MiB ceiling would be 47× more room than the
// endpoint can ever legitimately use — and the extra room is the whole cost of an
// unauthenticated flood. 4 MiB is the 2 MiB payload plus multipart headroom.
const maxFilePreviewBodyBytes = 4 << 20 // 4 MiB

// limitBody caps the request body via http.MaxBytesReader, so an oversized body is
// rejected instead of being fully buffered by the JSON gateway.
func limitBody(max int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, max)
			next.ServeHTTP(w, r)
		})
	}
}

// recoverMiddleware recovers panics in non-gRPC HTTP handlers, logs the stack via
// slog, and returns 500 instead of letting net/http drop the connection silently.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Default().ErrorContext(r.Context(), "recovered panic in http handler",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())),
				)
				w.WriteHeader(http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func corsMiddleware(allowedOrigins []string, allowDevOrigins bool) func(http.Handler) http.Handler {
	origins := make([]string, 0, len(allowedOrigins)+len(corsDevOrigins))

	// Configured prod/beta frontends (HTTP_ALLOWED_ORIGINS) are the source of truth.
	origins = append(origins, allowedOrigins...)

	if allowDevOrigins {
		origins = append(origins, corsDevOrigins...)
	}

	opts := cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Requested-With", "Accept", "Grpc-Metadata-Authorization", "Origin"},
		ExposedHeaders:   []string{"Content-Length", "X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           3600,
	}
	if len(origins) == 0 {
		// Fail closed. go-chi treats an empty AllowedOrigins as "allow all", which
		// with AllowCredentials=true degrades a strict credentialed allowlist to
		// allow-all when HTTP_ALLOWED_ORIGINS is empty/typo'd and dev origins are off.
		// Reject every cross-origin request instead.
		opts.AllowedOrigins = nil
		opts.AllowOriginFunc = func(_ *http.Request, _ string) bool { return false }
	}
	return cors.Handler(opts)
}

func (s *Server) setupHTTPAPI(ctx context.Context, auth *auth.Server) (http.Handler, error) {

	r := chi.NewRouter()
	// App-level panic recovery for the non-gRPC HTTP surface (webhooks, /statusz,
	// swagger, fileserver, REST gateway). The gRPC interceptor chain covers only the
	// gRPC path; without this a panic in a chi handler drops the connection with no
	// slog stack and no clean 500. Placed at the root so it wraps every route.
	r.Use(recoverMiddleware)

	adminHandler, err := s.adminJSONGateway(ctx)
	if err != nil {
		return nil, err
	}
	frontendHandler, err := s.frontendJSONGateway(ctx)
	if err != nil {
		return nil, err
	}
	authHandler, err := s.authJSONGateway(ctx)
	if err != nil {
		return nil, err
	}

	// Liveness probe - indicates the container is running
	// Simple check that the server is alive and responding
	r.HandleFunc("/livez", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			slog.Default().Error("failed to write livez response", slog.String("err", err.Error()))
		}
	})

	// Readiness probe - indicates the container is ready to accept traffic
	// Can check dependencies like database connectivity
	r.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()

		if s.healthChecker != nil {
			if err := s.healthChecker.CheckHealth(ctx); err != nil {
				slog.Default().WarnContext(ctx, "readiness check failed",
					slog.String("error", err.Error()),
				)
				w.Header().Set("Content-Type", "text/plain")
				w.WriteHeader(http.StatusServiceUnavailable)
				if _, err := w.Write([]byte(fmt.Sprintf("NOT READY: %v", err))); err != nil {
					slog.Default().Error("failed to write readyz error response", slog.String("err", err.Error()))
				}
				return
			}
		}

		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			slog.Default().Error("failed to write readyz response", slog.String("err", err.Error()))
		}
	})

	// Health check endpoint - backward compatibility
	// Alias to liveness check for simple health monitoring
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("OK")); err != nil {
			slog.Default().Error("failed to write health response", slog.String("err", err.Error()))
		}
	})

	// Operational status endpoint. Unlike /livez and /readyz this exposes internal
	// state (DB pool, per-worker liveness, circuit-breaker state, runtime), so it
	// is gated behind the same admin JWT auth the admin REST surface uses
	// (auth.WithAuth). It is read-only and never affects readiness — a stale
	// worker shows up here but does NOT make /readyz fail (which would trigger
	// restart loops). Only mounted when a health registry has been registered.
	if s.healthRegistry != nil {
		r.Method(http.MethodGet, "/statusz", auth.WithAuth(http.HandlerFunc(s.handleStatusz)))
	}

	// handle static swagger at root
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Only serve swagger for root path, not for other paths
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		page, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		tpl, err := template.ParseFS(fs, page)
		if err != nil {
			slog.Default().ErrorContext(ctx, "get swagger template error",
				slog.String("error", err.Error()),
			)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := tpl.Execute(w, nil); err != nil {
			slog.Default().ErrorContext(ctx, "failed to execute swagger template",
				slog.String("err", err.Error()),
			)
			return
		}
	})

	// Apply CORS middleware only to API routes
	r.Route("/api", func(r chi.Router) {
		r.Use(corsMiddleware(s.c.AllowedOrigins, s.c.AllowDevOrigins))
		// Admin carries base64-encoded media uploads, so it gets a cap sized for the
		// base64-expanded video payload (see maxAdminJSONBodyBytes); frontend/auth JSON
		// is bounded tightly.
		r.With(limitBody(maxAdminJSONBodyBytes)).Mount("/admin", auth.WithAuth(adminHandler))
		r.With(limitBody(maxJSONBodyBytes)).Mount("/frontend", frontendHandler)
		r.With(limitBody(maxJSONBodyBytes)).Mount("/auth", authHandler)
		// Tokenized pattern reads: the token IS the credential (rate-limited, audited,
		// epoch-revocable server-side), so no auth wrapper — QR codes and <object>
		// embeds cannot send Authorization headers.
		if s.patternAccessHandler != nil {
			r.Method(http.MethodGet, "/p/{token}", s.patternAccessHandler)
			// HEAD too, or chi answers it 405 — the one response that would not be the
			// uniform 404 every other rejection returns.
			r.Method(http.MethodHead, "/p/{token}", s.patternAccessHandler)
		}
		// Card-level viewer manifest (/api/pv/{token}, scope 'c') — same token-guarded
		// posture as /p above, including the HEAD mount for the uniform-404 property.
		if s.patternViewerHandler != nil {
			r.Method(http.MethodGet, "/pv/{token}", s.patternViewerHandler)
			r.Method(http.MethodHead, "/pv/{token}", s.patternViewerHandler)
		}
		// Run pack manifest (/api/rp/{token}, scope 'r') — the batch order the cutting floor
		// opens from paper. Same token-guarded posture as /p and /pv above, HEAD mount
		// included so a probe cannot tell a wrong method from a wrong token.
		if s.runPackHandler != nil {
			r.Method(http.MethodGet, "/rp/{token}", s.runPackHandler)
			r.Method(http.MethodHead, "/rp/{token}", s.runPackHandler)
		}
		// Public library-file link (/api/f/{token}, scope 'f'). ДВУХБУКВЕННЫХ СОСЕДЕЙ НЕ
		// ШАДОУИТ: /p, /pv, /rp и /f — четыре разных литеральных сегмента, chi разбирает их
		// как дерево, а не как список префиксов. HEAD монтируется вместе с GET, иначе chi
		// ответил бы на него 405 — единственный ответ, который отличался бы от одинакового
		// 404 всех прочих отказов и тем самым подтверждал бы существование пути.
		if s.fileLinkHandler != nil {
			r.Method(http.MethodGet, "/f/{token}", s.fileLinkHandler)
			r.Method(http.MethodHead, "/f/{token}", s.fileLinkHandler)
		}
		// Files-library upload. Its own body cap, deliberately NOT the admin-JSON one:
		// that limit is sized for base64-expanded media inside a gRPC message, and this
		// is a raw stream with entirely different economics. The handler arrives already
		// wrapped in admin authorization (app wiring) — the interceptor that guards every
		// gRPC method has no reach here.
		if s.fileUploadHandler != nil {
			r.With(limitBody(maxFileUploadBodyBytes)).
				Method(http.MethodPost, "/files/upload", s.fileUploadHandler)
		}
		// Preview replacement ("построить превью заново"). Same authorization
		// wrapping as the upload, its own body cap: one thumbnail has nothing to do
		// with the 95 MiB a file may need.
		if s.filePreviewHandler != nil {
			r.With(limitBody(maxFilePreviewBodyBytes)).
				Method(http.MethodPost, "/files/{id}/preview", s.filePreviewHandler)
		}
	})

	// Webhook routes — no CORS, no auth. Must accept POST from external services.
	if s.webhookHandler != nil {
		r.Post("/api/webhooks/resend", s.webhookHandler.HandleResendEvent)
		r.Post("/api/webhooks/list-unsubscribe/{email_b64}/{token}", s.webhookHandler.HandleListUnsubscribe)
		r.Post("/api/webhooks/list-unsubscribe/{topic}/{email_b64}/{token}", s.webhookHandler.HandleCampaignListUnsubscribe)
	}
	if s.stripeWebhookHandler != nil {
		r.Post("/api/webhooks/stripe", s.stripeWebhookHandler.HandleStripeEvent)
	}
	if s.aftershipWebhookHandler != nil {
		r.Post("/api/webhooks/aftership", s.aftershipWebhookHandler.HandleAftershipEvent)
	}

	r.Mount("/", http.FileServer(http.FS(fs)))

	return r, nil
}

// newAdminJSONMarshaler — JSON-маршалер admin-гейтвея. ЕДИНСТВЕННОЕ место, где он собирается:
// его берёт и мукс (newAdminServeMux ниже), и тест marshaler_test.go — не копией опций, а этим же
// конструктором, иначе тест доказывал бы строгость выдуманной конфигурации, а не работающей.
//
// ЗАЧЕМ. По умолчанию grpc-gateway v2.21.0 ставит на мукс protojson с DiscardUnknown: true
// (runtime/marshaler_registry.go, defaultMarshaler). Это значит, что незнакомое ПОЛЕ и незнакомое
// ИМЯ ЧЛЕНА ENUM в теле запроса выбрасываются БЕЗ ОШИБКИ: сервер отвечает 200, а присланного
// значения в сообщении просто нет. Так и случился инцидент Ж7 — владелец получил
// «operation_type: required» на заполненном поле, а 32 поля шага тех-карты уехали в никуда молча:
// строка операции пишется полной заменой, и то, что транспорт съел, стёрлось и в базе. Обмен на
// громкий 400 с ИМЕНЕМ ВИНОВНИКА в теле — это и есть смысл правки: «сломаться можно, исчезнуть
// нельзя».
//
// ПОЧЕМУ ТОЛЬКО ADMIN. У витрины и у auth свои отдельные муксы (frontendJSONGateway /
// authJSONGateway ниже) — они этой строгости НЕ получают намеренно: публичный клиент витрины
// обновляется не нами и не синхронно, и 400 на лишнем поле там был бы отказом в обслуживании, а не
// защитой данных. Терять на витрине нечего: она не пишет карточек.
//
// ПОЧЕМУ EmitUnpopulated ОСТАЁТСЯ true. Это дефолт того же маршалера, и на нём стоит контракт
// admin-клиента: незаполненное вложенное сообщение приходит ЯВНЫМ null, и zod-схемы клиента
// разбирают именно эту форму. Меняется РОВНО ОДИН бит — DiscardUnknown; остальные опции
// воспроизводят дефолт v2.21.0 дословно.
//
// ГРАНИЦА СОВМЕСТИМОСТИ (инвентарь на 2026-08-21). Бандл admin-клиента СТАРШЕ последнего снятия
// члена enum / поля теперь получает 400 вместо тихой порчи — это принятое поведение, а не
// регрессия. Инвентарь по proto/admin/admin/admin.proto + proto/common/common/*.proto: 15
// зарезервированных ИМЁН членов enum (TECH_CARD_PRESS_ACTION_OPEN, TECH_CARD_TOPSTITCH_MODE_WIDTH,
// TECH_CARD_REINFORCEMENT_FUSIBLE_PATCH, TECH_CARD_REINFORCEMENT_FABRIC_STAY,
// TECH_CARD_MACHINE_TYPE_HARDWARE_ATTACH, TECH_CARD_PEEL_MODE_NONE, TECH_CARD_HOLE_PREP_PRONG_PIERCE,
// TECH_CARD_THREAD_TENSION_OTHER, TECH_CARD_PRESS_TOWARD_SIDE, TECH_CARD_PIECE_FUSING_MODE_SEAM_ALLOWANCE,
// TECH_CARD_INSPECT_COVERAGE_FIRST_OUTPUT, TECH_CARD_CLEANING_KIND_CHALK_REMOVAL,
// TECH_CARD_CLEANING_KIND_ADHESIVE_REMOVAL, TECH_CARD_ZIPPER_APPLICATION_SEPARATING_CF,
// TECH_CARD_ZIPPER_APPLICATION_IN_SEAM_POCKET) и 112 пар (сообщение, зарезервированное ИМЯ поля).
// Клиент ветки feat/operation-kinds-ui не эмитит НИ ОДНОГО из них: сгенерированные типы
// src/api/proto-http/{admin,common}/index.ts не содержат ни одного зарезервированного ключа в
// соответствующем сообщении, снятые члены встречаются ровно трижды — двумя комментариями в
// генерённых файлах, картой ЧТЕНИЯ RETIRED_REINFORCEMENT (operation-options.ts:867-870, на провод
// из формы уходит уже канонизованный PATCH) и фикстурой печатного стенда print-smoke.tsx, который
// ничего не сохраняет. Перед прод-деплоем бека это надо ПЕРЕПРОВЕРИТЬ на прод-бандле клиента
// (он может быть старше беты) — см. Ф8-п.6 плана PHASE-STOP-LOSS. ВАЖНО: списки reserved — не вся
// граница. Поля и члены, снятые с одним лишь reserved-НОМЕРОМ (без имени), получают тот же 400, но
// в инвентарь и растяжку не попадают — их поимённый список у retiredFieldNamePairs в
// marshaler_test.go («СЛЕПОЕ ПЯТНО РАСТЯЖКИ»); прод-проверку вести по объединению обоих списков.
func newAdminJSONMarshaler() *runtime.HTTPBodyMarshaler {
	return &runtime.HTTPBodyMarshaler{
		Marshaler: &runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{
				EmitUnpopulated: true, // дефолт v2.21.0; контракт явного null у admin-клиента
			},
			UnmarshalOptions: protojson.UnmarshalOptions{
				DiscardUnknown: false, // ЕДИНСТВЕННОЕ отличие от дефолта: незнакомое = 400, не тишина
			},
		},
	}
}

// newAdminServeMux собирает мукс admin-гейтвея. Вынесен из adminJSONGateway, чтобы тест поднимал
// РОВНО ТОТ мукс, который живёт в проде (со всеми его опциями), и удаление WithMarshalerOption
// красило тест, а не проходило мимо него.
func newAdminServeMux() *runtime.ServeMux {
	return runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch key {
			case "Grpc-Metadata-Authorization":
				return key, true
			default:
				return runtime.DefaultHeaderMatcher(key)
			}
		}),
		runtime.WithMarshalerOption(runtime.MIMEWildcard, newAdminJSONMarshaler()),
	)
}

func (s *Server) adminJSONGateway(ctx context.Context) (http.Handler, error) {
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := newAdminServeMux()

	err := pb_admin.RegisterAdminServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}
	return mux, nil
}

func (s *Server) frontendJSONGateway(ctx context.Context) (http.Handler, error) {
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch key {
			case "Grpc-Metadata-Authorization":
				return key, true
			default:
				return runtime.DefaultHeaderMatcher(key)
			}
		}),
	)
	err := pb_frontend.RegisterFrontendServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}
	return mux, nil
}

func (s *Server) authJSONGateway(ctx context.Context) (http.Handler, error) {
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := runtime.NewServeMux(
		runtime.WithIncomingHeaderMatcher(func(key string) (string, bool) {
			switch key {
			case "Grpc-Metadata-Authorization":
				return key, true
			default:
				return runtime.DefaultHeaderMatcher(key)
			}
		}),
	)

	err := pb_auth.RegisterAuthServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}

	return mux, nil
}

// statuszResponse is the JSON shape of GET /statusz. It carries no secrets —
// only operational counters and timestamps.
type statuszResponse struct {
	Now     string          `json:"now"`
	Commit  string          `json:"commit,omitempty"`
	DB      *statuszDB      `json:"db,omitempty"`
	Workers []statuszWorker `json:"workers"`
	// Stale lists the names of workers whose last success is older than
	// staleThreshold (or that have never run); a convenience summary.
	Stale    []string         `json:"stale,omitempty"`
	Breakers []statuszBreaker `json:"breakers,omitempty"`
	Runtime  statuszRuntime   `json:"runtime"`
}

type statuszDB struct {
	OpenConnections    int    `json:"open_connections"`
	InUse              int    `json:"in_use"`
	Idle               int    `json:"idle"`
	WaitCount          int64  `json:"wait_count"`
	WaitDuration       string `json:"wait_duration"`
	MaxOpenConnections int    `json:"max_open_connections"`
}

type statuszWorker struct {
	Name string `json:"name"`
	// LastSuccess is RFC3339, or "never" if the worker has not had a successful
	// tick yet.
	LastSuccess string `json:"last_success"`
	// SecondsSinceLastSuccess is null when the worker has never succeeded, so a
	// never-run worker is not reported as infinitely stale.
	SecondsSinceLastSuccess *int64 `json:"seconds_since_last_success"`
}

type statuszBreaker struct {
	Name  string `json:"name"`
	State string `json:"state"`
}

type statuszRuntime struct {
	NumGoroutine int    `json:"num_goroutine"`
	NumCPU       int    `json:"num_cpu"`
	HeapAllocMiB uint64 `json:"heap_alloc_mib"`
	SysMiB       uint64 `json:"sys_mib"`
	NumGC        uint32 `json:"num_gc"`
}

// statuszStaleThreshold is the age past which a worker's last success is flagged
// in the Stale summary. It is generous so a worker with a long interval (e.g.
// ga4sync / tier management run hourly+) is not flagged between normal ticks.
const statuszStaleThreshold = 6 * time.Hour

// handleStatusz renders the operational status JSON. It is registered behind
// admin auth, so it is never world-readable.
func (s *Server) handleStatusz(w http.ResponseWriter, r *http.Request) {
	reg := s.healthRegistry
	now := time.Now()

	resp := statuszResponse{
		Now:     now.UTC().Format(time.RFC3339),
		Commit:  s.c.CommitHash,
		Workers: make([]statuszWorker, 0, len(reg.Workers)),
	}

	if reg.DB != nil {
		st := reg.DB.Stats()
		resp.DB = &statuszDB{
			OpenConnections:    st.OpenConnections,
			InUse:              st.InUse,
			Idle:               st.Idle,
			WaitCount:          st.WaitCount,
			WaitDuration:       st.WaitDuration.String(),
			MaxOpenConnections: st.MaxOpenConnections,
		}
	}

	for _, wk := range reg.Workers {
		ws := statuszWorker{Name: wk.Name()}
		last := wk.LastSuccess()
		if last.IsZero() {
			ws.LastSuccess = "never"
			resp.Stale = append(resp.Stale, wk.Name())
		} else {
			ws.LastSuccess = last.UTC().Format(time.RFC3339)
			secs := int64(now.Sub(last).Seconds())
			ws.SecondsSinceLastSuccess = &secs
			if now.Sub(last) > statuszStaleThreshold {
				resp.Stale = append(resp.Stale, wk.Name())
			}
		}
		resp.Workers = append(resp.Workers, ws)
	}

	for _, b := range reg.Breakers {
		if b.StateFunc == nil {
			continue
		}
		resp.Breakers = append(resp.Breakers, statuszBreaker{
			Name:  b.BreakerName,
			State: b.StateFunc().String(),
		})
	}

	var ms goruntime.MemStats
	goruntime.ReadMemStats(&ms)
	const miB = 1024 * 1024
	resp.Runtime = statuszRuntime{
		NumGoroutine: goruntime.NumGoroutine(),
		NumCPU:       goruntime.NumCPU(),
		HeapAllocMiB: ms.HeapAlloc / miB,
		SysMiB:       ms.Sys / miB,
		NumGC:        ms.NumGC,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Default().ErrorContext(r.Context(), "failed to encode statusz response",
			slog.String("err", err.Error()),
		)
	}
}

// panicRecoveryHandler logs a recovered panic with its stack trace and returns a
// generic Internal error, so a single malformed request cannot crash the process
// and panic internals are never leaked to the caller.
func panicRecoveryHandler(ctx context.Context, p any) error {
	slog.Default().ErrorContext(ctx, "recovered from panic in gRPC handler",
		slog.Any("panic", p),
		slog.String("stack", string(debug.Stack())),
	)
	return status.Error(codes.Internal, "internal server error")
}

// Start starts the server
func (s *Server) Start(ctx context.Context,
	adminServer *admin.Server,
	frontendServer *frontend.Server,
	authServer *auth.Server,
) error {

	opts := []grpcSlog.Option{
		grpcSlog.WithLogOnEvents(grpcSlog.StartCall, grpcSlog.FinishCall),
		// Add any other option (check functions starting with logging.With).
	}

	// Recovery must be the outermost interceptor so it catches panics from the
	// logging/auth interceptors and the handlers alike; grpc-go runs each call in
	// its own goroutine with no recover, so an unhandled panic kills the process.
	recoveryOpts := []grpcRecovery.Option{
		grpcRecovery.WithRecoveryHandlerContext(panicRecoveryHandler),
	}

	s.gs = grpc.NewServer(
		grpc.MaxRecvMsgSize(grpcMaxRecvMsgSize),
		// Send limit matched to recv: the grpc-go default send cap (~4MiB) would
		// otherwise silently truncate large responses.
		grpc.MaxSendMsgSize(grpcMaxSendMsgSize),
		// Bound per-connection stream fan-out.
		grpc.MaxConcurrentStreams(grpcMaxConcurrentStreams),
		// Reap idle/half-open HTTP/2 connections. MaxConnectionAge is deliberately
		// unset so the loopback gateway client and long-lived frontend streams are
		// never periodically force-closed; see the const block for rationale.
		grpc.KeepaliveParams(keepalive.ServerParameters{
			MaxConnectionIdle: grpcKeepaliveMaxConnectionIdle,
			Time:              grpcKeepaliveTime,
			Timeout:           grpcKeepaliveTimeout,
		}),
		// PermitWithoutStream keeps the (mostly stream-less) loopback gateway
		// connection alive; MinTime stays modest so compliant clients aren't dropped.
		grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{
			MinTime:             grpcKeepaliveMinTime,
			PermitWithoutStream: true,
		}),
		grpc.ChainUnaryInterceptor(
			grpcRecovery.UnaryServerInterceptor(recoveryOpts...),
			grpcSlog.UnaryServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
			authServer.UnaryAdminAuthInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			grpcRecovery.StreamServerInterceptor(recoveryOpts...),
			grpcSlog.StreamServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
		),
	)
	pb_admin.RegisterAdminServiceServer(s.gs, adminServer)
	pb_frontend.RegisterFrontendServiceServer(s.gs, frontendServer)
	pb_auth.RegisterAuthServiceServer(s.gs, authServer)

	var clientHTTPHandler http.Handler
	handler := middleware.ClientIdentifier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			s.gs.ServeHTTP(w, r)
		} else {
			if clientHTTPHandler == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			clientHTTPHandler.ServeHTTP(w, r)
		}
	}))

	ctx, cancel := context.WithCancel(ctx)
	hsDone := make(chan struct{})

	go func() {
		<-hsDone
		close(s.done)
	}()

	listenerAddr := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)
	s.hs = &http.Server{
		Addr:    listenerAddr,
		Handler: h2c.NewHandler(handler, &http2.Server{}),
		// Slow-loris hardening. ReadHeaderTimeout caps how long a client may take
		// to send request headers; IdleTimeout reaps idle keep-alive connections;
		// MaxHeaderBytes bounds header size.
		// ReadTimeout and WriteTimeout are intentionally omitted: this server
		// multiplexes h2c gRPC (long-lived streams / SSE) and large media uploads
		// to the bucket on the same port. A WriteTimeout would kill long-lived
		// streaming responses, and a ReadTimeout would abort slow but legitimate
		// large upload / streaming request bodies. The per-connection slow-loris
		// risk is instead bounded by ReadHeaderTimeout + IdleTimeout.
		// TODO: make configurable via httpapi.Config.
		ReadHeaderTimeout: serverReadHeaderTimeout,
		IdleTimeout:       serverIdleTimeout,
		MaxHeaderBytes:    serverMaxHeaderBytes,
	}

	go func() {
		commitInfo := ""
		if s.c.CommitHash != "" {
			commitInfo = fmt.Sprintf(" commit: %s", s.c.CommitHash)
		}
		slog.Default().InfoContext(ctx, fmt.Sprintf("grbpwr-products-manager new listener on: http://%v%s", listenerAddr, commitInfo))
		err := s.hs.ListenAndServe()
		if err == http.ErrServerClosed {
			slog.Default().InfoContext(ctx, "http server returned")
		} else {
			slog.Default().ErrorContext(ctx, "http server exited with an error",
				slog.String("error", err.Error()),
			)
		}
		cancel()
		close(hsDone)
	}()

	clientHTTPHandler, err := s.setupHTTPAPI(ctx, authServer)
	if err != nil {
		cancel()
		return err
	}

	return nil
}
