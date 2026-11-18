// Package airollup copies each team's AI spend into the control plane for the platform console, and
// raises the alarm when a team spends far above its usual rate (teams wave, 02-DESIGN §10, risk 6).
//
// WHY A ROLLUP. Every AI call books its row in the spending team's OWN database (ai_usage_event; the
// ledger writes with the request ctx). The console must show every team's month at once without a
// cross-database scan per page, so every RollupInterval this worker walks the teams (active and
// suspended — a read-only team's past spend still counts), sums each team's recent days from its own
// ledger and upserts team_ai_spend_daily. The console reads one control-plane query (AISpendSince).
//
// Days are the owner's budget-zone days (team #1 design_settings.budget_timezone, read through the
// registry's team-#1 pin), the same days ai_usage_event.day_local was stamped in.
//
// One team that cannot be read is logged (WARN ai_rollup_failed) and skipped; the others are rolled
// up. Sequential, on the shared pool: one connection at a time.
//
// No per-team cap exists (owner 18.11). The spike check after each rollup is the detection: ERROR
// ai_spend_spike, once per team per day, and the operator's lever is the team's `ai` switch.
package airollup

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Name is the worker's name in logs and the health registry.
const Name = "ai-spend-rollup"

const (
	// RollupInterval — how often every team is rolled up; the console is at most this stale.
	RollupInterval = 10 * time.Minute
	// FirstRunDelay — the first rollup after boot, late enough not to crowd the boot itself.
	FirstRunDelay = 30 * time.Second
	// teamTimeout bounds one team's read + upserts.
	teamTimeout = 30 * time.Second
	// historyDays — days before today re-read every run. Today and yesterday are what moves (a call
	// near midnight, an accepted call priced later); the rest is the spike check's history, re-read so
	// the check needs no second source.
	historyDays = SpikeHistoryDays
)

// Spike thresholds (risk 6). Named so the rule reads in one place.
const (
	// SpikeRatio — today above this many times the median of the previous SpikeHistoryDays days.
	SpikeRatio = 3
	// SpikeRatioFloorUSD — the ratio rule needs at least this much spent today: three cents against a
	// one-cent median is not an alarm.
	SpikeRatioFloorUSD = 1
	// DefaultSpikeUSD — today above this many dollars is a spike whatever the history
	// (env AI_SPEND_SPIKE_USD overrides it, see the app).
	DefaultSpikeUSD = 20
	// SpikeHistoryDays — the history the median is taken over (missing days count as $0).
	SpikeHistoryDays = 7
)

// Event names (log message = event, so a log query finds them by one word).
const (
	EventSpike  = "ai_spend_spike"
	EventFailed = "ai_rollup_failed"
)

// Querier is the team database as the rollup reads it: the shared routing pool, bound to a team by
// ctx (dependency.DB satisfies it).
type Querier interface {
	SelectContext(ctx context.Context, dest any, query string, args ...any) error
}

// Sink is the control plane as the rollup writes it (*platformstore.Store satisfies it).
type Sink interface {
	UpsertAISpend(ctx context.Context, teamID int64, day time.Time, v platformstore.AISpend) error
	AISpendSince(ctx context.Context, day time.Time) (map[int64]platformstore.AISpend, error)
}

// Config is what New needs. Dir, Teams, Sink and TZ are required.
type Config struct {
	Dir   tenant.Directory
	Teams Querier
	Sink  Sink
	// TZ is the owner's budget zone (registry.BudgetTimezone); "" = entity.DefaultBudgetTimezone.
	TZ func() string
	// SpikeUSD — the absolute spike threshold; zero or less = DefaultSpikeUSD.
	SpikeUSD decimal.Decimal
	// Interval / FirstDelay — zero = RollupInterval / FirstRunDelay (tests shorten them).
	Interval, FirstDelay time.Duration
	// Now — zero = time.Now.
	Now func() time.Time
	// Log — nil = slog.Default().
	Log *slog.Logger
}

// Worker — see the package doc.
type Worker struct {
	c Config

	mu sync.Mutex
	// seen — teams already rolled up over the whole month once by this process.
	seen map[int64]bool
	// alerted — team id → the day key its spike was last reported for (once per team per day).
	alerted map[int64]string

	tracker health.Tracker
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

// New builds the worker.
func New(c Config) (*Worker, error) {
	if c.Dir == nil || c.Teams == nil || c.Sink == nil {
		return nil, fmt.Errorf("airollup: directory, team pool and control plane are required")
	}
	if c.TZ == nil {
		c.TZ = func() string { return "" }
	}
	if !c.SpikeUSD.IsPositive() {
		c.SpikeUSD = decimal.NewFromInt(DefaultSpikeUSD)
	}
	if c.Interval <= 0 {
		c.Interval = RollupInterval
	}
	if c.FirstDelay <= 0 {
		c.FirstDelay = FirstRunDelay
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Worker{c: c, seen: map[int64]bool{}, alerted: map[int64]string{}}, nil
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return Name }

// LastSuccess implements health.Reporter.
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// Start launches the loop: the first rollup after FirstDelay, then every Interval.
func (w *Worker) Start(ctx context.Context) error {
	if w.stop != nil {
		return fmt.Errorf("%s worker already started", Name)
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() { w.run(w.ctx) })
	return nil
}

// Stop cancels the loop and waits for the run in flight.
func (w *Worker) Stop() {
	if w.stop == nil {
		return
	}
	w.stop()
	w.wg.Wait()
	w.stop = nil
}

func (w *Worker) run(ctx context.Context) {
	timer := time.NewTimer(w.c.FirstDelay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			w.RunOnce(ctx)
			timer.Reset(w.c.Interval)
		}
	}
}

// dayUsage is one day of one team's ledger.
type dayUsage struct {
	Day      string          `db:"day"`
	CostUSD  decimal.Decimal `db:"cost_usd"`
	Unpriced int64           `db:"unpriced"`
	Calls    int64           `db:"calls"`
}

// usageQuery — one team's ledger by budget day. cost_usd NULL = unpriced: excluded from the sum (SUM
// skips NULL), counted on its own.
const usageQuery = `SELECT DATE_FORMAT(day_local, '%Y-%m-%d') AS day,
       COALESCE(SUM(cost_usd), 0) AS cost_usd,
       CAST(SUM(cost_usd IS NULL) AS SIGNED) AS unpriced,
       CAST(COUNT(*) AS SIGNED) AS calls
FROM ai_usage_event WHERE day_local >= ? GROUP BY day_local`

// RunOnce rolls every listed team up and checks each for a spike. Exported for the tests and for a
// manual nudge; the loop calls it on its own.
func (w *Worker) RunOnce(ctx context.Context) {
	defer saferun.Recover(ctx, Name)

	now := w.c.Now()
	tz := w.c.TZ()
	if tz == "" {
		tz = entity.DefaultBudgetTimezone
	}
	today, err := time.Parse(time.DateOnly, entity.BudgetDayKey(now, tz))
	if err != nil { // BudgetDayKey always formats a date; a parse failure is a programming error
		w.tracker.MarkError(err)
		w.c.Log.ErrorContext(ctx, "ai spend rollup: today's budget day", slog.String("err", err.Error()))
		return
	}
	monthStart := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC)
	history := today.AddDate(0, 0, -historyDays)

	// Teams already in the control plane this month need no month backfill (also after a restart).
	known, err := w.c.Sink.AISpendSince(ctx, monthStart)
	if err != nil {
		w.tracker.MarkError(err)
		w.c.Log.ErrorContext(ctx, "ai spend rollup: the control plane could not be read", slog.String("err", err.Error()))
		return
	}

	err = tenant.Each(ctx, w.c.Dir, func(tctx context.Context, t tenant.Team) error {
		if err := w.rollTeam(tctx, t, today, monthStart, history, known); err != nil {
			w.c.Log.WarnContext(ctx, EventFailed,
				slog.String("event", EventFailed),
				slog.Int64("tid", t.ID),
				slog.String("slug", t.Slug),
				slog.String("err", err.Error()))
		}
		return nil // logged above; tenant.Each must not log it again
	})
	if err != nil { // only the team list itself (or shutdown)
		w.tracker.MarkError(err)
		w.c.Log.ErrorContext(ctx, "ai spend rollup: the team list could not be read", slog.String("err", err.Error()))
		return
	}
	w.tracker.MarkSuccess()
}

func (w *Worker) rollTeam(ctx context.Context, t tenant.Team, today, monthStart, history time.Time, known map[int64]platformstore.AISpend) error {
	ctx, cancel := context.WithTimeout(ctx, teamTimeout)
	defer cancel()

	w.mu.Lock()
	_, inPlane := known[t.ID]
	full := !w.seen[t.ID] && !inPlane
	w.mu.Unlock()
	since := history
	if full && monthStart.Before(since) {
		since = monthStart
	}

	var rows []dayUsage
	if err := w.c.Teams.SelectContext(ctx, &rows, usageQuery, since.Format(time.DateOnly)); err != nil {
		return fmt.Errorf("read ledger: %w", err)
	}
	byDay := make(map[string]decimal.Decimal, len(rows))
	for _, r := range rows {
		day, err := time.Parse(time.DateOnly, r.Day)
		if err != nil {
			return fmt.Errorf("ledger day %q: %w", r.Day, err)
		}
		if err := w.c.Sink.UpsertAISpend(ctx, t.ID, day, platformstore.AISpend{
			CostUSD: r.CostUSD, Unpriced: r.Unpriced, Calls: r.Calls,
		}); err != nil {
			return fmt.Errorf("upsert %s: %w", r.Day, err)
		}
		byDay[r.Day] = r.CostUSD
	}
	w.mu.Lock()
	w.seen[t.ID] = true
	w.mu.Unlock()

	w.checkSpike(ctx, t, today, byDay)
	return nil
}

// checkSpike reports ERROR ai_spend_spike when today is above SpikeUSD, or above SpikeRatio × the
// median of the previous SpikeHistoryDays days while at least SpikeRatioFloorUSD — once per team
// per day (in memory: a restart may report the same day once more, which is acceptable).
func (w *Worker) checkSpike(ctx context.Context, t tenant.Team, today time.Time, byDay map[string]decimal.Decimal) {
	key := today.Format(time.DateOnly)
	todayUSD := byDay[key]
	median := medianOfPrevious(today, byDay)
	if !IsSpike(todayUSD, median, w.c.SpikeUSD) {
		return
	}
	w.mu.Lock()
	if w.alerted[t.ID] == key {
		w.mu.Unlock()
		return
	}
	w.alerted[t.ID] = key
	w.mu.Unlock()
	w.c.Log.ErrorContext(ctx, EventSpike,
		slog.String("event", EventSpike),
		slog.Int64("tid", t.ID),
		slog.String("slug", t.Slug),
		slog.String("day", key),
		slog.String("today_usd", todayUSD.StringFixed(2)),
		slog.String("median_usd", median.StringFixed(2)),
		slog.String("threshold_usd", w.c.SpikeUSD.StringFixed(2)),
	)
}

// IsSpike is the rule of risk 6: today > absUSD, or today ≥ SpikeRatioFloorUSD and today >
// SpikeRatio × median.
func IsSpike(today, median, absUSD decimal.Decimal) bool {
	if today.GreaterThan(absUSD) {
		return true
	}
	if today.LessThan(decimal.NewFromInt(SpikeRatioFloorUSD)) {
		return false
	}
	return today.GreaterThan(median.Mul(decimal.NewFromInt(SpikeRatio)))
}

// medianOfPrevious — the median of the SpikeHistoryDays days before today; a day without rows is $0.
func medianOfPrevious(today time.Time, byDay map[string]decimal.Decimal) decimal.Decimal {
	vals := make([]decimal.Decimal, 0, SpikeHistoryDays)
	for i := 1; i <= SpikeHistoryDays; i++ {
		vals = append(vals, byDay[today.AddDate(0, 0, -i).Format(time.DateOnly)])
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i].LessThan(vals[j]) })
	n := len(vals)
	if n%2 == 1 {
		return vals[n/2]
	}
	return vals[n/2-1].Add(vals[n/2]).Div(decimal.NewFromInt(2))
}
