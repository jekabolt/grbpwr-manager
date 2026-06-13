package httpapi

import (
	"context"
	"embed"
	"fmt"
	"net/http"
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
	"google.golang.org/grpc/status"

	grpcSlog "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"
	grpcRecovery "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
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
	CommitHash     string   `mapstructure:"commit_hash"`
}

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

func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	// Collect all allowed origins (exact and wildcard patterns)
	origins := make([]string, 0, len(allowedOrigins)+3)

	// Add localhost and vercel patterns for development
	origins = append(origins, "http://localhost*")
	origins = append(origins, "http://127.0.0.1*")
	origins = append(origins, "https://*.vercel.app")
	origins = append(origins, "https://*.github.io")
	origins = append(origins, "https://admin.grbpwr.com")

	// Add configured origins (they may contain wildcards)
	origins = append(origins, allowedOrigins...)

	// chi/cors handles wildcard patterns like "https://*.vercel.app"
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
		r.Use(corsMiddleware(s.c.AllowedOrigins))
		r.Mount("/admin", auth.WithAuth(adminHandler))
		r.Mount("/frontend", frontendHandler)
		r.Mount("/auth", authHandler)
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
		grpc.MaxRecvMsgSize(50*1024*1024), // 50MB
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
