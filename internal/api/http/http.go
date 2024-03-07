package httpapi

import (
	"context"
	"embed"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	"github.com/go-chi/chi"
	"github.com/grpc-ecosystem/grpc-gateway/runtime"

	"golang.org/x/exp/slog"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	grpcRecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
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

	// handle static swagger
	r.HandleFunc("/products-manager/api", func(w http.ResponseWriter, r *http.Request) {
		page, ok := pages[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		tpl, err := template.ParseFS(fs, page)
		if err != nil {
			slog.Default().ErrorCtx(ctx, "get swagger template error [%v]", err.Error())
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		if err := tpl.Execute(w, nil); err != nil {
			return
		}
	})

	r.Mount("/api/admin", auth.WithAuth(adminHandler))
	r.Mount("/api/frontend", frontendHandler)
	r.Mount("/api/auth", authHandler)

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
			EmitDefaults: false,
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
			EmitDefaults: false,
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
			EmitDefaults: false,
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
		grpcSlog.WithLogOnEvents(grpcSlog.StartCall, grpcSlog.FinishCall, grpcSlog.PayloadSent, grpcSlog.PayloadReceived),
		// Add any other option (check functions starting with logging.With).
	}

	s.gs = grpc.NewServer(
		grpc.ChainUnaryInterceptor(
			grpcSlog.UnaryServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
			grpcRecovery.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			grpcSlog.StreamServerInterceptor(log.InterceptorLogger(slog.Default()), opts...),
			grpcRecovery.StreamServerInterceptor(),
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
		Handler: cors(h2c.NewHandler(handler, &http2.Server{})),
	}

	go func() {
		slog.Default().InfoCtx(ctx, fmt.Sprintf("grbpwr-products-manager new listener on: http://%v", listenerAddr))
		err := s.hs.ListenAndServe()
		if err == http.ErrServerClosed {
			slog.Default().InfoCtx(ctx, "http server returned")
		} else {
			slog.Default().ErrorCtx(ctx, "http server exited with an error [%v]", err.Error())
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

// cors is a middleware that implements Cross Origin Resource Sharing.
// It adds CORS headers to each response.
// https://developer.mozilla.org/en-US/docs/Web/HTTP/CORS

func (s *Server) cors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		if origin == "" {
			// If there's no origin, it's not a CORS request, so just pass it through.
			slog.Default().InfoCtx(r.Context(), "no origin header")
			h.ServeHTTP(w, r)
			return
		}

		// Check if the origin is from localhost or in the allowed origins list
		if isOriginAllowed(origin, s.c.AllowedOrigins) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			slog.Default().InfoCtx(r.Context(), "origin not allowed",
				slog.String("origin", origin),
			)
			http.Error(w, "Origin not allowed", http.StatusForbidden)
			return
		}

		w.Header().Set("Access-Control-Allow-Methods", "GET, PUT, POST, DELETE, HEAD, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Content-Length, Accept-Encoding, Authorization, ResponseType, Grpc-Metadata-Authorization")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		h.ServeHTTP(w, r)
	})
}

func isOriginAllowed(origin string, allowedOrigins []string) bool {
	// Always allow localhost origins
	if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "https://localhost:") {
		return true
	}

	// Check if origin is in the list of allowed origins
	for _, allowedOrigin := range allowedOrigins {
		if origin == allowedOrigin {
			return true
		}
	}

	return false
}
