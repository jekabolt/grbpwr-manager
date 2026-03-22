# Add a new background worker

Existing workers to reference as patterns:
- `internal/ordercleanup/` — periodic order expiration
- `internal/stripereconcile/` — orphaned PaymentIntent cleanup
- `internal/analytics/ga4sync/` — GA4/BigQuery data sync
- `internal/mail/worker.go` — email queue processor

Steps:
1. Create `internal/<workername>/` package with a struct holding dependencies and config
2. Implement `Start(ctx context.Context) error` that launches a goroutine with a ticker
3. Implement `Stop() error` or accept context cancellation for graceful shutdown
4. Use `log/slog` for structured logging with relevant context fields
5. Add config fields to `config/cfg.go` (interval, limits, etc.)
6. Wire in `app/app.go` — construct in startup, call Start, defer Stop
7. If the worker needs a new interface method, add to `internal/dependency/dependency.go` and run `make generate-mocks`

Use `time.NewTicker` for periodic work. Respect context cancellation. Log errors, never panic.
