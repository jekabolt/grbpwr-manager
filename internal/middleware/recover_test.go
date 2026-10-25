package middleware_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	grpcRecovery "github.com/grpc-ecosystem/go-grpc-middleware/v2/interceptors/recovery"
	"github.com/jekabolt/grbpwr-manager/internal/middleware"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type sentryReceiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	events []map[string]any
}

func newSentryReceiver(t *testing.T) *sentryReceiver {
	t.Helper()
	rc := &sentryReceiver{}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		sc := bufio.NewScanner(&body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		sc.Scan()
		for sc.Scan() {
			var item struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(sc.Bytes(), &item)
			if !sc.Scan() {
				break
			}
			if item.Type == "event" {
				var ev map[string]any
				_ = json.Unmarshal(sc.Bytes(), &ev)
				rc.mu.Lock()
				rc.events = append(rc.events, ev)
				rc.mu.Unlock()
			}
		}
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *sentryReceiver) dsn() string {
	return "http://key@" + strings.TrimPrefix(rc.srv.URL, "http://") + "/1"
}

func (rc *sentryReceiver) all() []map[string]any {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return append([]map[string]any(nil), rc.events...)
}

// captureLog swaps the default logger for one that writes JSON into a buffer (through the
// Sentry handler when it is on), the way cmd/run.go builds it.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	var buf bytes.Buffer
	slog.SetDefault(slog.New(observability.NewSlogHandler(slog.NewJSONHandler(&buf, nil))))
	return &buf
}

// httpStack is the production order of setupHTTPAPI: Sentry hub (only when on), then Recover.
func httpStack() http.Handler {
	r := chi.NewRouter()
	if observability.Enabled() {
		r.Use(observability.HTTPMiddleware)
	}
	r.Use(middleware.Recover)
	r.Get("/boom/{id}", func(http.ResponseWriter, *http.Request) { panic("http boom") })
	return r
}

type panickingHealth struct {
	healthpb.UnimplementedHealthServer
}

func (panickingHealth) Check(context.Context, *healthpb.HealthCheckRequest) (*healthpb.HealthCheckResponse, error) {
	var m map[string]int
	m["x"] = 1 // nil map write: a runtime.Error panic
	return nil, nil
}

// grpcClient serves panickingHealth behind the production interceptor order of Server.Start, with
// an auth stand-in that names the admin the way UnaryAdminAuthInterceptor does.
func grpcClient(t *testing.T) healthpb.HealthClient {
	t.Helper()
	adminID := 7
	fakeAuth := func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		observability.SetAdmin(ctx, "alice", &adminID)
		return h(ctx, req)
	}
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.ChainUnaryInterceptor(
		observability.UnaryServerInterceptor(),
		grpcRecovery.UnaryServerInterceptor(grpcRecovery.WithRecoveryHandlerContext(middleware.GRPCPanicHandler)),
		fakeAuth,
	))
	healthpb.RegisterHealthServer(gs, panickingHealth{})
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	return healthpb.NewHealthClient(conn)
}

func TestRecoverWithoutSentry(t *testing.T) {
	observability.Disable()
	logs := captureLog(t)

	rec := httptest.NewRecorder()
	httpStack().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/boom/1", nil))
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Contains(t, logs.String(), "recovered panic in http handler")

	_, err := grpcClient(t).Check(context.Background(), &healthpb.HealthCheckRequest{})
	require.Equal(t, codes.Internal, status.Code(err))
	require.Equal(t, "internal server error", status.Convert(err).Message())
	require.Contains(t, logs.String(), "recovered from panic in gRPC handler")
}

func TestRecoverReportsToSentry(t *testing.T) {
	rc := newSentryReceiver(t)
	observability.Disable()
	t.Cleanup(observability.Disable)
	_, err := observability.Init(observability.Config{DSN: rc.dsn(), Environment: "beta"}, "deadbeef")
	require.NoError(t, err)
	logs := captureLog(t)

	// HTTP: same response as without Sentry, one exception event, the log line not sent twice.
	req := httptest.NewRequest(http.MethodGet, "/boom/42", nil)
	req.Header.Set("X-Request-Id", "req-http-1")
	rec := httptest.NewRecorder()
	httpStack().ServeHTTP(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
	require.Empty(t, rec.Body.String())
	require.Empty(t, rec.Header().Get("X-Request-Id"), "the response must not change")

	// gRPC: the request id arrives as gateway metadata; the admin is set by the auth step.
	ctx := metadata.AppendToOutgoingContext(context.Background(), "x-request-id", "req-grpc-1")
	_, err = grpcClient(t).Check(ctx, &healthpb.HealthCheckRequest{})
	require.Equal(t, codes.Internal, status.Code(err))

	require.True(t, observability.Flush(5*time.Second))
	require.Contains(t, logs.String(), "recovered panic in http handler")
	require.Contains(t, logs.String(), "recovered from panic in gRPC handler")

	events := rc.all()
	require.Len(t, events, 2, "one event per panic; the recover log lines are not re-sent")
	byRoute := map[string]map[string]any{}
	for _, ev := range events {
		require.NotEmpty(t, ev["exception"])
		tags := ev["tags"].(map[string]any)
		byRoute[tags["route"].(string)] = ev
	}

	httpEv := byRoute["GET /boom/{id}"]
	require.NotNil(t, httpEv, "routes: %v", byRoute)
	require.Equal(t, "req-http-1", httpEv["tags"].(map[string]any)["request_id"])
	require.Equal(t, "deadbeef", httpEv["release"])
	require.Equal(t, "beta", httpEv["environment"])
	require.Equal(t, "fatal", httpEv["level"])

	grpcEv := byRoute["/grpc.health.v1.Health/Check"]
	require.NotNil(t, grpcEv, "routes: %v", byRoute)
	gt := grpcEv["tags"].(map[string]any)
	require.Equal(t, "req-grpc-1", gt["request_id"])
	require.Equal(t, "7", gt["admin_id"])
	require.Equal(t, "alice", grpcEv["user"].(map[string]any)["username"])
	exc := grpcEv["exception"].([]any)[0].(map[string]any)
	require.Contains(t, exc["value"], "nil map")
}
