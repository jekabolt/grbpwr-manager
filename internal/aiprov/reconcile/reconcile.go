// Package reconcile fetches «their number»: what each provider's OWN cost API says we spent per day,
// written into ai_provider_cost_daily beside the ledger our calls write (B-28, D-06, D-17). The admin
// spend report shows the two side by side; the gap between them is how a missing ledger row or a wrong
// table price becomes visible.
//
// Four providers have something to read: OpenAI (/v1/organization/costs), Anthropic (cost_report) and
// fal (/v1/models/usage) with the reconciliation key an admin stores in the panel (kind admin), and
// OpenRouter (/api/v1/key usage_daily) with its ordinary API key. Google, meshy, apibost, runblob and
// recraft-direct have no cost API here: their line is the ledger alone.
//
// THE KEY RULE IS DELIBERATE: OpenAI, Anthropic and fal read Registry.AdminKey(provider), which is
// not gated by the provider's enabled switch because old spend still needs reconciling; OpenRouter
// reads Registry.KeyFunc(openrouter)(), the enabled-gated key that actually pays for its calls.
//
// ⚠ THIS WORKER WRITES ai_provider_cost_daily AND NOTHING ELSE. It never touches the ledger
// (ai_usage_event): their number sits beside ours, it never corrects it. A fetch that fails, answers
// a shape this package does not know, or says «there is more» writes NOTHING for that provider — a
// partial or misread number presented as theirs is exactly the lie the column exists to catch.
//
// ⚠ ONE GET PER PROVIDER PER TICK, TO A CONSTANT URL, REDIRECTS REFUSED. The hosts are
// aiprov/endpoints' constants; no row, env variable or argument names another one (a host that can be
// edited is a place a key can be sent). net/http drops Authorization on a cross-host redirect but
// forwards x-api-key, so no redirect is followed at all. Tests reach in-memory httptest handlers
// through the http.Client they pass (WithHTTPClient), never through a knob here.
//
// ⚠ NO KEY MATERIAL IN ANY LOG LINE OR ERROR. The key is read once per fetch and goes into one
// request header; errors name the provider and the HTTP status, never the key, and an error body is
// never read at all (a provider refusing a key may quote it back).
//
// DAYS ARE THE PROVIDER'S (D-17). Every API here buckets by UTC day, and a row keeps that day with
// bucket_tz 'UTC'; nothing is shifted into the organisation's zone. OpenRouter has no per-day history:
// usage_daily is the running total of the CURRENT UTC day, so its row for today is re-written every
// tick and the day's final number is its last snapshot before midnight UTC — with the default hourly
// tick, up to an hour of each OpenRouter day is never seen.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// Name is the worker's name in /statusz and in the panic log.
const Name = "ai-reconcile"

const (
	// defaultInterval — one fetch per provider an hour. The admin-key APIs answer yesterday and
	// today, so an hour is only how soon today's partial number moves; for OpenRouter it is also how
	// much of each day's end is never seen (see the package comment).
	defaultInterval = time.Hour

	// tickTimeout bounds one tick — every provider's fetch and write — so a stuck request can
	// neither block the loop nor stall graceful shutdown. Four fetches of fetchTimeout each fit.
	tickTimeout = 60 * time.Second

	// fetchTimeout bounds ONE provider's request, so a slow provider cannot spend the others' share
	// of the tick.
	fetchTimeout = 15 * time.Second
)

// Backoff bounds for consecutive failed ticks (base * 2^(n-1), capped at backoffMax); a clean tick
// resets it. The fxsync shape.
const (
	backoffBase = 1 * time.Minute
	backoffMax  = 30 * time.Minute
)

// ErrNotRunning — RunNow on a worker that is not started, or already stopped: nothing may write once
// Stop has begun (the database closes right after it).
var ErrNotRunning = errors.New("reconcile: the worker is not running")

// Config configures the reconciliation worker (config: AI_RECONCILE_ENABLED, AI_RECONCILE_INTERVAL).
type Config struct {
	// Enabled gates the worker entirely; ON by default. Every fetch is a free read of a cost report,
	// and a provider with no key is never asked. When false the worker is never constructed, and a
	// saved admin key triggers no run either.
	Enabled bool `mapstructure:"enabled"`
	// Interval between ticks; <= 0 is the default hour.
	Interval time.Duration `mapstructure:"interval"`
}

// DefaultConfig — on, hourly.
func DefaultConfig() Config {
	return Config{Enabled: true, Interval: defaultInterval}
}

// CostStore writes the providers' own daily numbers (dependency.AI satisfies it). It is the only
// store this package may reach.
type CostStore interface {
	UpsertCostDaily(ctx context.Context, rows []entity.AICostDaily) error
}

// KeySource answers the keys (registry.Registry satisfies it). AdminKey is NOT gated by the provider's
// enabled switch — switching a provider off stops new spend, not the need to reconcile the spend
// already made. KeyFunc IS gated (and falls back to env): OpenRouter's number is read with the key
// that pays for its calls, and a provider switched off in the panel is not asked for anything with it.
type KeySource interface {
	AdminKey(providerKey string) string
	KeyFunc(providerKey string) func() string
}

// Option adjusts a Worker (tests: the clock, the HTTP client, the loop's timers).
type Option func(*Worker)

// WithClock sets "now" — the day the fetches ask about and the fetched_at they stamp.
func WithClock(now func() time.Time) Option {
	return func(w *Worker) {
		if now != nil {
			w.now = now
		}
	}
}

// WithHTTPClient sets the client every fetch uses. Its transport and pool are kept; redirects are
// refused on a copy, whatever the caller's client does.
func WithHTTPClient(c *http.Client) Option {
	return func(w *Worker) { w.client = httpClient(c) }
}

// Worker periodically reconciles every provider with a cost API and a key.
type Worker struct {
	c      Config
	store  CostStore
	keys   KeySource
	now    func() time.Time
	client *http.Client

	// newTicker / newTimer are the loop's clocks; tests replace them to drive ticks and observe the
	// backoff without waiting for either.
	newTicker func(time.Duration) (<-chan time.Time, func())
	newTimer  func(time.Duration) (<-chan time.Time, func() bool)

	// mu guards ctx/stop and the admission of RunNow: a run is admitted only while the worker is
	// running, and counted in wg under the same lock, so Stop's Wait sees every run that got in.
	mu   sync.Mutex
	ctx  context.Context
	stop context.CancelFunc
	wg   sync.WaitGroup

	// fetchMu lets ONE provider fetch-and-write happen at a time. A tick and an after-save RunNow of
	// the same provider would otherwise race, and the older answer could land last.
	fetchMu sync.Mutex

	tracker health.Tracker
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return Name }

// LastSuccess implements health.Reporter: the last tick in which every provider it asked answered and
// was written (zero until the first).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// LastError is the last failure recorded (a fetch or a write), "" after a clean tick.
func (w *Worker) LastError() string { return w.tracker.LastError() }

// New builds the worker. keys is read on every fetch, so a key saved in the panel is used by the next
// one without a restart.
func New(c Config, store CostStore, keys KeySource, opts ...Option) *Worker {
	if c.Interval <= 0 {
		c.Interval = defaultInterval
	}
	w := &Worker{
		c:      c,
		store:  store,
		keys:   keys,
		now:    time.Now,
		client: defaultClient,
		newTicker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
		newTimer: func(d time.Duration) (<-chan time.Time, func() bool) {
			t := time.NewTimer(d)
			return t.C, t.Stop
		},
	}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Start launches the loop: one tick at once, then one per Interval.
func (w *Worker) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop != nil {
		return fmt.Errorf("reconcile worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	runCtx := w.ctx
	w.wg.Go(func() {
		w.run(runCtx)
	})
	return nil
}

// Stop cancels the loop and every RunNow in flight, and waits for all of them to return — the
// database closes right after it (app.Stop), and no write may land on a closed pool.
func (w *Worker) Stop() error {
	w.mu.Lock()
	if w.stop == nil {
		w.mu.Unlock()
		return fmt.Errorf("reconcile worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.mu.Unlock()
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	tick, stopTicker := w.newTicker(w.c.Interval)
	defer stopTicker()

	// Once at startup: a fresh boot does not wait a full interval for today's number.
	w.runOnce(ctx)

	var consecutiveFailures int
	for {
		select {
		case <-tick:
			if w.runOnce(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(consecutiveFailures)
			slog.Default().WarnContext(ctx, "reconcile: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			wait, stopWait := w.newTimer(delay)
			select {
			case <-wait:
			case <-ctx.Done():
				stopWait()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// backoffDelay returns base * 2^(n-1), capped at backoffMax.
func backoffDelay(consecutiveFailures int) time.Duration {
	delay := backoffBase
	for i := 1; i < consecutiveFailures; i++ {
		delay *= 2
		if delay >= backoffMax {
			return backoffMax
		}
	}
	return delay
}

// runOnce reconciles every provider in turn and reports whether all of them that had a key answered
// and were written. One provider failing does not stop the others: each one's number is its own.
func (w *Worker) runOnce(ctx context.Context) (ok bool) {
	defer saferun.Recover(ctx, Name)
	// A tick that panics (recovered and logged by saferun above) is a FAILED tick: the named result
	// would otherwise keep the `true` set before the loop, and the loop would neither back off nor
	// show the failure in /statusz. Registered after Recover, so it runs first, while the panic is
	// still unwinding.
	done := false
	defer func() {
		if !done {
			ok = false
			w.tracker.MarkError(errRunPanicked)
		}
	}()

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	ok = true
	for _, a := range adapters {
		if err := w.reconcile(ctx, a); err != nil {
			ok = false
		}
	}
	if ok {
		w.tracker.MarkSuccess()
	}
	done = true
	return ok
}

// RunNow reconciles one provider at once — the run after an admin saves its reconciliation key, so
// «their number» does not wait for the next tick. It is refused (ErrNotRunning) unless the worker is
// running, and Stop cancels it and waits for it. A provider with no cost API here is an error; a
// provider with no key is nothing to do.
func (w *Worker) RunNow(ctx context.Context, provider string) (err error) {
	a, ok := adapterOf(provider)
	if !ok {
		return fmt.Errorf("reconcile: %s has no cost API to reconcile against", providerName(provider))
	}
	ctx, release, err := w.admit(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer saferun.Recover(ctx, Name)
	// What the caller gets if reconcile panics: the assignment below never completes, and the
	// recovered panic returns this instead of a nil that would read as «done».
	err = errRunPanicked

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	if err = w.reconcile(ctx, a); err != nil {
		return err
	}
	slog.Default().InfoContext(ctx, "reconcile: on-demand run done", slog.String("provider", a.provider))
	return nil
}

// errRunPanicked — a RunNow whose reconcile panicked (logged with its stack by saferun).
var errRunPanicked = errors.New("reconcile: the run panicked")

// admit lets a RunNow in while the worker is running: counted in wg and cancelled with the loop.
func (w *Worker) admit(ctx context.Context) (context.Context, func(), error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stop == nil {
		return nil, nil, ErrNotRunning
	}
	w.wg.Add(1)
	ctx, cancel := context.WithCancel(ctx)
	unwatch := context.AfterFunc(w.ctx, cancel)
	return ctx, func() {
		unwatch()
		cancel()
		w.wg.Done()
	}, nil
}

// reconcile fetches one provider and writes its days. No key = nothing to ask, not a failure. A
// failed fetch or write logs `reconcile: <provider> fetch failed` / `… upsert failed`, marks the
// tracker, and — for a fetch — writes nothing.
func (w *Worker) reconcile(ctx context.Context, a adapter) error {
	key := w.keyOf(a)
	if key == "" {
		return nil
	}

	w.fetchMu.Lock()
	defer w.fetchMu.Unlock()

	days, err := w.fetch(ctx, a, key)
	if errors.Is(err, errAcrossMidnight) {
		slog.Default().InfoContext(ctx, "reconcile: "+a.provider+" snapshot skipped: the request straddled a UTC midnight",
			slog.String("provider", a.provider))
		return nil
	}
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "reconcile: "+a.provider+" fetch failed",
			slog.String("provider", a.provider), slog.String("err", err.Error()))
		return err
	}
	if len(days) == 0 {
		slog.Default().DebugContext(ctx, "reconcile: no provider day to write", slog.String("provider", a.provider))
		return nil
	}

	fetched := w.now().UTC()
	rows := make([]entity.AICostDaily, 0, len(days))
	for _, d := range days {
		rows = append(rows, entity.AICostDaily{
			ProviderKey: a.provider,
			Day:         d.day,
			// DECIMAL(12,6): rounded here, not by the server, so the stored number is the one
			// this line decided.
			AmountUSD: d.usd.Round(6),
			Currency:  "USD",
			BucketTZ:  entity.AICostBucketUTC,
			FetchedAt: fetched,
		})
	}
	if err := w.store.UpsertCostDaily(ctx, rows); err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "reconcile: "+a.provider+" upsert failed",
			slog.String("provider", a.provider), slog.String("err", err.Error()))
		return err
	}
	slog.Default().DebugContext(ctx, "reconcile: provider days written",
		slog.String("provider", a.provider), slog.Int("days", len(rows)))
	return nil
}

// keyOf reads the key the adapter's API takes: the reconciliation key (not gated by enabled) or the
// API key (gated by enabled, env fallback) — see KeySource.
func (w *Worker) keyOf(a adapter) string {
	if w.keys == nil {
		return ""
	}
	if a.kind == entity.AIKeyAdmin {
		return w.keys.AdminKey(a.provider)
	}
	if f := w.keys.KeyFunc(a.provider); f != nil {
		return f()
	}
	return ""
}

// providerName echoes only known vocabulary into a message.
func providerName(p string) string {
	if entity.IsAIProviderKey(p) {
		return p
	}
	return "unknown provider"
}
