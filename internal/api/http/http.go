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
}

// StripeWebhookHandler handles inbound Stripe webhook events (signature-verified).
type StripeWebhookHandler interface {
	HandleStripeEvent(w http.ResponseWriter, r *http.Request)
}

// Server is the http server
type Server struct {
	hs                   *http.Server
	gs                   *grpc.Server
	c                    *Config
	done                 chan struct{}
	healthChecker        HealthChecker
	webhookHandler       WebhookHandler
	stripeWebhookHandler StripeWebhookHandler
	healthRegistry       *health.Registry
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

// SetWebhookHandler registers the webhook handler for Resend and list-unsubscribe routes.
func (s *Server) SetWebhookHandler(h WebhookHandler) {
	s.webhookHandler = h
}

// SetStripeWebhookHandler registers the handler for Stripe webhook events.
func (s *Server) SetStripeWebhookHandler(h StripeWebhookHandler) {
	s.stripeWebhookHandler = h
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
// (grpcMaxRecvMsgSize) because it carries base64 media uploads.
const maxJSONBodyBytes = 4 << 20 // 4 MB

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

	return cors.Handler(cors.Options{
		AllowedOrigins:   origins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "PATCH"},
		AllowedHeaders:   []string{"Content-Type", "Authorization", "X-Requested-With", "Accept", "Grpc-Metadata-Authorization", "Origin"},
		ExposedHeaders:   []string{"Content-Length", "X-Request-Id"},
		AllowCredentials: true,
		MaxAge:           3600,
	})
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
		// Admin carries base64 media uploads, so it gets the larger gRPC-recv cap;
		// frontend/auth JSON is bounded tightly.
		r.With(limitBody(grpcMaxRecvMsgSize)).Mount("/admin", auth.WithAuth(adminHandler))
		r.With(limitBody(maxJSONBodyBytes)).Mount("/frontend", frontendHandler)
		r.With(limitBody(maxJSONBodyBytes)).Mount("/auth", authHandler)
	})

	// Webhook routes — no CORS, no auth. Must accept POST from external services.
	if s.webhookHandler != nil {
		r.Post("/api/webhooks/resend", s.webhookHandler.HandleResendEvent)
		r.Post("/api/webhooks/list-unsubscribe/{email_b64}", s.webhookHandler.HandleListUnsubscribe)
	}
	if s.stripeWebhookHandler != nil {
		r.Post("/api/webhooks/stripe", s.stripeWebhookHandler.HandleStripeEvent)
	}

	r.Mount("/", http.FileServer(http.FS(fs)))

	return r, nil
}

func (s *Server) adminJSONGateway(ctx context.Context) (http.Handler, error) {
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
