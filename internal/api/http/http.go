package httpapi

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	chi "github.com/go-chi/chi/v5"
	"github.com/go-chi/cors"
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
		"/": "static/swagger/index.html",
	}
)

// Config is the configuration for the http server
type Config struct {
	Port           string   `mapstructure:"port"`
	Address        string   `mapstructure:"address"`
	AllowedOrigins []string `mapstructure:"allowed_origins"`
	CommitHash     string   `mapstructure:"commit_hash"`
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
	// Collect all allowed origins (exact and wildcard patterns)
	origins := make([]string, 0, len(allowedOrigins)+3)

	// Add localhost and vercel patterns for development
	origins = append(origins, "http://localhost*")
	origins = append(origins, "http://127.0.0.1*")
	origins = append(origins, "https://*.vercel.app")
	origins = append(origins, "https://*.github.io")

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

	// health check endpoint
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
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
