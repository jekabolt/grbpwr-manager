package httpapi

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	chi "github.com/go-chi/chi/v5"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"

	"log/slog"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcSlog "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/logging"

	"github.com/jekabolt/grbpwr-manager/internal/apisrv/admin"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/auth"
	"github.com/jekabolt/grbpwr-manager/internal/apisrv/frontend"
	"github.com/jekabolt/grbpwr-manager/log"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_auth "github.com/jekabolt/grbpwr-manager/proto/gen/auth"
	pb_frontend "github.com/jekabolt/grbpwr-manager/proto/gen/frontend"
)

var (
	//go:embed static
	fs embed.FS

	pages = map[string]string{
		"/products-manager/api": "static/swagger/index.html",
	}
)

// Config is the configuration for the http server
type Config struct {
	Port           string   `mapstructure:"port"`
	Address        string   `mapstructure:"address"`
	AllowedOrigins []string `mapstructure:"allowed_origins"`
}

// Server is the http server
type Server struct {
	hs   *http.Server
	gs   *grpc.Server
	c    *Config
	done chan struct{}
}

// New creates a new server
func New(config *Config) *Server {
	return &Server{
		c:    config,
		done: make(chan struct{}),
	}
}

// Done returns a channel that is closed when gRPC server exits
func (s *Server) Done() <-chan struct{} {
	return s.done
}

func corsMiddleware(allowedOrigins []string) func(http.Handler) http.Handler {
	// Separate exact matches and wildcard patterns
	exactOrigins := make(map[string]bool, len(allowedOrigins))
	wildcardPatterns := make([]string, 0)

	for _, origin := range allowedOrigins {
		if strings.Contains(origin, "*") {
			wildcardPatterns = append(wildcardPatterns, origin)
		} else {
			exactOrigins[origin] = true
		}
	}

	// Helper function to check if origin matches a wildcard pattern
	matchesWildcard := func(origin, pattern string) bool {
		// Convert wildcard pattern to regex-like matching
		// e.g., "https://*.vercel.app" matches "https://anything.vercel.app"
		if !strings.Contains(pattern, "*") {
			return false
		}

		// Handle pattern like "https://*.vercel.app"
		if strings.HasPrefix(pattern, "https://*.") {
			suffix := strings.TrimPrefix(pattern, "https://*")
			if strings.HasPrefix(origin, "https://") && strings.HasSuffix(origin, suffix) {
				// Ensure there's at least one character between "https://" and the suffix
				middle := strings.TrimPrefix(strings.TrimSuffix(origin, suffix), "https://")
				return len(middle) > 0 && !strings.Contains(middle, "/")
			}
		}

		// Handle pattern like "http://*" or other patterns if needed
		if strings.HasPrefix(pattern, "http://*") {
			return strings.HasPrefix(origin, "http://localhost") || strings.HasPrefix(origin, "http://127.0.0.1")
		}

		return false
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Allow requests without Origin header (non-browser clients like curl)
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			var allowed bool

			// Check exact match against configured origins
			if exactOrigins[origin] {
				allowed = true
			}

			// Check wildcard patterns
			if !allowed {
				for _, pattern := range wildcardPatterns {
					if matchesWildcard(origin, pattern) {
						allowed = true
						break
					}
				}
			}

			// Check hardcoded patterns (localhost, vercel)
			if !allowed {
				if strings.HasPrefix(origin, "http://localhost") ||
					strings.HasPrefix(origin, "http://127.0.0.1") ||
					(strings.HasPrefix(origin, "https://") && strings.HasSuffix(origin, ".vercel.app")) {
					allowed = true
				}
			}

			if allowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Max-Age", "3600")

				// Handle preflight requests
				if r.Method == http.MethodOptions {
					w.WriteHeader(http.StatusOK)
					return
				}

				next.ServeHTTP(w, r)
				return
			}

			// Reject disallowed origin
			http.Error(w, "Origin not allowed", http.StatusForbidden)
		})
	}
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

	// health check endpoint
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	// handle static swagger
	r.HandleFunc("/products-manager/api", func(w http.ResponseWriter, r *http.Request) {
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

	r.Mount("/", http.FileServer(http.FS(fs)))

	return r, nil
}

func (s *Server) adminJSONGateway(ctx context.Context) (http.Handler, error) {
	// dial options for the grpc-gateway
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := runtime.NewServeMux(runtime.WithMarshalerOption(
		runtime.MIMEWildcard,
		&runtime.JSONPb{
			EnumsAsInts:  false,
			EmitDefaults: true,
		},
	))

	err := pb_admin.RegisterAdminServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}
	return mux, nil
}

func (s *Server) frontendJSONGateway(ctx context.Context) (http.Handler, error) {
	// dial options for the grpc-gateway
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := runtime.NewServeMux(runtime.WithMarshalerOption(
		runtime.MIMEWildcard,
		&runtime.JSONPb{
			EnumsAsInts:  false,
			EmitDefaults: true,
		},
	))
	err := pb_frontend.RegisterFrontendServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}
	return mux, nil
}

func (s *Server) authJSONGateway(ctx context.Context) (http.Handler, error) {
	// dial options for the grpc-gateway
	grpcDialOpts := []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}

	apiEndpoint := fmt.Sprintf("%s:%s", s.c.Address, s.c.Port)

	mux := runtime.NewServeMux(runtime.WithMarshalerOption(
		runtime.MIMEWildcard,
		&runtime.JSONPb{
			EnumsAsInts:  false,
			EmitDefaults: true,
		},
	))

	err := pb_auth.RegisterAuthServiceHandlerFromEndpoint(ctx, mux, apiEndpoint, grpcDialOpts)
	if err != nil {
		return nil, err
	}

	return mux, nil
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

	s.gs = grpc.NewServer(
		grpc.MaxRecvMsgSize(20*1024*1024), // 20MB
		grpc.ChainUnaryInterceptor(
			grpcSlog.UnaryServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
			// grpcRecovery.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			grpcSlog.StreamServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
			// grpcRecovery.StreamServerInterceptor(),
		),
	)
	pb_admin.RegisterAdminServiceServer(s.gs, adminServer)
	pb_frontend.RegisterFrontendServiceServer(s.gs, frontendServer)
	pb_auth.RegisterAuthServiceServer(s.gs, authServer)

	var clientHTTPHandler http.Handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.Contains(r.Header.Get("Content-Type"), "application/grpc") {
			s.gs.ServeHTTP(w, r)
		} else {
			if clientHTTPHandler == nil {
				w.WriteHeader(http.StatusNotImplemented)
				return
			}
			clientHTTPHandler.ServeHTTP(w, r)
		}
	})

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
		slog.Default().InfoContext(ctx, fmt.Sprintf("grbpwr-products-manager new listener on: http://%v", listenerAddr))
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
