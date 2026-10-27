// Package observability reports backend failures to Sentry: recovered panics (HTTP and gRPC)
// and slog records at Error and above.
//
// Everything here is inert until Init is called with a non-empty DSN (SENTRY_DSN). Without it no
// client exists, no goroutine starts, no request leaves the process, and every exported helper
// returns its input untouched — the service behaves exactly as it did before this package.
package observability

import (
	"strings"
	"sync/atomic"
	"time"

	"github.com/getsentry/sentry-go"
)

// Config is the `sentry` config section. Both values come from env (SENTRY_DSN,
// SENTRY_ENVIRONMENT); the DSN never lives in code or in a committed config file.
type Config struct {
	DSN         string `mapstructure:"dsn"`
	Environment string `mapstructure:"environment"`
}

// DefaultEnvironment is used when SENTRY_ENVIRONMENT is unset. The backend has no other contour
// marker, so the prod app must set SENTRY_ENVIRONMENT=prod explicitly.
const DefaultEnvironment = "beta"

var enabled atomic.Bool

// Init starts the Sentry client when c.DSN is set and reports whether it did. release is the git
// sha baked into the binary (empty or "unknown" lets the SDK fall back to SENTRY_RELEASE / VCS
// build info).
func Init(c Config, release string) (bool, error) {
	dsn := strings.TrimSpace(c.DSN)
	if dsn == "" {
		return false, nil
	}
	release = strings.TrimSpace(release)
	if release == "unknown" {
		release = ""
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn:              dsn,
		Environment:      EnvironmentOf(c),
		Release:          release,
		SendDefaultPII:   false,
		AttachStacktrace: true,
		SampleRate:       1.0,
		EnableTracing:    false,
		BeforeSend:       scrubEvent,
		// The classic transport: Flush waits for every queued error event. The newer telemetry
		// buffer lost events between capture and Flush under load in tests (-race).
		DisableTelemetryBuffer: true,
		DisableMetrics:         true,
		DisableClientReports:   true,
	})
	if err != nil {
		return false, err
	}
	enabled.Store(true)
	return true, nil
}

// EnvironmentOf is the Sentry environment c selects: SENTRY_ENVIRONMENT, else DefaultEnvironment.
func EnvironmentOf(c Config) string {
	if env := strings.TrimSpace(c.Environment); env != "" {
		return env
	}
	return DefaultEnvironment
}

// Enabled reports whether Init started a client.
func Enabled() bool { return enabled.Load() }

// Disable turns reporting off again and drops the client: every helper is inert afterwards, as if
// Init had never run. Used by tests that check the disabled path after an enabled one.
func Disable() {
	enabled.Store(false)
	sentry.CurrentHub().BindClient(nil)
	limits = newLimiter()
}

// Flush waits up to timeout for queued events to be sent. A no-op when disabled.
func Flush(timeout time.Duration) bool {
	if !Enabled() {
		return true
	}
	return sentry.Flush(timeout)
}
