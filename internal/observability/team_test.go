package observability_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/jekabolt/grbpwr-manager/internal/observability"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestInstallOf(t *testing.T) {
	require.Equal(t, "beta", observability.InstallOf("lekalo-beta"))
	require.Equal(t, "beta", observability.InstallOf("beta"))
	require.Equal(t, "prod", observability.InstallOf("lekalo-prod"))
	require.Equal(t, "prod", observability.InstallOf(" PROD "))
	require.Equal(t, "staging", observability.InstallOf("staging"))
	require.Equal(t, "beta", observability.InstallOf(""))
}

func TestEventsCarryTeamAndInstall(t *testing.T) {
	rc := newReceiver(t)
	observability.Disable()
	t.Cleanup(observability.Disable)
	_, err := observability.Init(observability.Config{DSN: rc.dsn(), Environment: "lekalo-beta"}, "abc123")
	require.NoError(t, err)

	log := slog.New(observability.NewSlogHandler(slog.NewJSONHandler(&bytes.Buffer{}, nil)))
	acme := tenant.Team{ID: 7, Slug: "acme", Name: "Acme"}
	log.ErrorContext(tenant.With(context.Background(), acme), "ctx team line")
	log.Error(observability.MsgTeamStuckAfterMigration, slog.String("team", "zeta"))
	log.Error("no team line", slog.String("slug", "Not A Slug!"))

	// A panic recovered OUTSIDE the team binder still carries the team: the team interceptor tags
	// the shared per-call hub.
	outer := sentry.SetHubOnContext(context.Background(), sentry.CurrentHub().Clone())
	inner := tenant.With(outer, acme)
	_, _ = observability.TeamUnaryInterceptor()(inner, nil, &grpc.UnaryServerInfo{}, func(context.Context, any) (any, error) {
		return nil, nil
	})
	observability.CapturePanic(outer, "boom", "route-panic")
	require.True(t, observability.Flush(5*time.Second))

	_, events := rc.snapshot()
	require.Len(t, events, 4)
	tags := map[string]map[string]any{}
	for _, ev := range events {
		key, _ := ev["message"].(string)
		tg := ev["tags"].(map[string]any)
		if r, ok := tg["route"].(string); ok {
			key = r
		}
		tags[key] = tg
	}
	require.Equal(t, "acme", tags["ctx team line"]["team"])
	require.Equal(t, "zeta", tags[observability.MsgTeamStuckAfterMigration]["team"])
	require.NotContains(t, tags["no team line"], "team")
	require.Equal(t, "acme", tags["route-panic"]["team"])
	for k, tg := range tags {
		require.Equal(t, "beta", tg["install"], k)
	}
}
