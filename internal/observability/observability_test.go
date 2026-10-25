package observability_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/stretchr/testify/require"
)

// receiver is a stand-in Sentry ingest: it keeps the event payloads of every envelope it gets.
type receiver struct {
	srv    *httptest.Server
	mu     sync.Mutex
	posts  int
	events []map[string]any
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	rc := &receiver{}
	rc.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		_, _ = body.ReadFrom(r.Body)
		rc.mu.Lock()
		defer rc.mu.Unlock()
		rc.posts++
		sc := bufio.NewScanner(&body)
		sc.Buffer(make([]byte, 1<<20), 8<<20)
		sc.Scan() // envelope header
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
				rc.events = append(rc.events, ev)
			}
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(rc.srv.Close)
	return rc
}

func (rc *receiver) dsn() string {
	return "http://key@" + strings.TrimPrefix(rc.srv.URL, "http://") + "/1"
}

func (rc *receiver) snapshot() (int, []map[string]any) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.posts, append([]map[string]any(nil), rc.events...)
}

func TestDisabledIsInert(t *testing.T) {
	observability.Disable()
	on, err := observability.Init(observability.Config{DSN: "  "}, "sha")
	require.NoError(t, err)
	require.False(t, on)
	require.False(t, observability.Enabled())

	inner := slog.NewJSONHandler(&bytes.Buffer{}, nil)
	require.Same(t, inner, observability.NewSlogHandler(inner))

	next := http.NewServeMux()
	require.Same(t, next, observability.HTTPMiddleware(next).(*http.ServeMux))

	ctx := context.Background()
	require.Equal(t, ctx, observability.MarkReported(ctx))
	observability.CapturePanic(ctx, "boom", "route") // must not panic or send
	observability.SetAdmin(ctx, "admin", nil)
	require.True(t, observability.Flush(time.Millisecond))
}

func TestSlogErrorReachesSentryOnce(t *testing.T) {
	rc := newReceiver(t)
	observability.Disable()
	t.Cleanup(observability.Disable)
	on, err := observability.Init(observability.Config{DSN: rc.dsn()}, "abc123")
	require.NoError(t, err)
	require.True(t, on)

	var out bytes.Buffer
	log := slog.New(observability.NewSlogHandler(slog.NewJSONHandler(&out, nil))).With("worker", "fx")
	log.Info("not an error")
	log.Error("sync failed", slog.Any("err", errors.New(`upstream 502 for ops@example.com: Get "https://maps.example.com/geo?key=k3y#f": EOF`)),
		slog.String("api_token", "s3cr3t"), slog.String("callback", "https://hooks.example.com/cb?token=t0k"))
	log.Error("sync failed", slog.String("err", "again")) // same line within the window: held back
	log.ErrorContext(observability.MarkReported(context.Background()), "already reported")
	require.True(t, observability.Flush(5*time.Second))

	// The JSON log itself is unchanged: every record still reaches the inner handler.
	require.Equal(t, 4, strings.Count(out.String(), "\n"))

	_, events := rc.snapshot()
	require.Len(t, events, 1)
	ev := events[0]
	require.Equal(t, "sync failed", ev["message"])
	require.Equal(t, "abc123", ev["release"])
	require.Equal(t, "beta", ev["environment"])
	require.Equal(t, []any{"slog", "sync failed"}, ev["fingerprint"])
	exc := ev["exception"].([]any)[0].(map[string]any)
	require.Equal(t, `upstream 502 for [email]: Get "https://maps.example.com/geo": EOF`, exc["value"])
	attrs := ev["contexts"].(map[string]any)["log"].(map[string]any)
	require.Equal(t, "https://hooks.example.com/cb", attrs["callback"])
	require.Equal(t, "fx", attrs["worker"])
	require.Equal(t, "[Filtered]", attrs["api_token"])
}

func TestGRPCFinishedCallsGroupByMethodAndCode(t *testing.T) {
	rc := newReceiver(t)
	observability.Disable()
	t.Cleanup(observability.Disable)
	_, err := observability.Init(observability.Config{DSN: rc.dsn()}, "abc123")
	require.NoError(t, err)

	log := slog.New(observability.NewSlogHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	call := func(method, code string) {
		log.Error("finished call", "grpc.service", "admin.AdminService", "grpc.method", method,
			"grpc.code", code, "grpc.error", "rpc error")
	}
	call("GetStyle", "Internal")
	call("GetStyle", "Internal") // same method and code within the window: held back
	call("GetStyle", "Unknown")
	call("SaveStyle", "Internal")
	require.True(t, observability.Flush(5*time.Second))

	_, events := rc.snapshot()
	require.Len(t, events, 3)
	got := map[string]bool{}
	for _, ev := range events {
		got[ev["message"].(string)] = true
	}
	require.Equal(t, map[string]bool{
		"finished call /admin.AdminService/GetStyle Internal":  true,
		"finished call /admin.AdminService/GetStyle Unknown":   true,
		"finished call /admin.AdminService/SaveStyle Internal": true,
	}, got)
}

func TestEnvironmentFromConfig(t *testing.T) {
	require.Equal(t, "beta", observability.EnvironmentOf(observability.Config{}))
	require.Equal(t, "prod", observability.EnvironmentOf(observability.Config{Environment: " prod "}))
}
