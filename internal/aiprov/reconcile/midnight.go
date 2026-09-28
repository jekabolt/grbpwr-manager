package reconcile

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// ───────────── a cumulative counter's days: the diff at LOCAL midnights (D-17, B-30) ─────────────
//
// OpenRouter answers one number, the key's spend since it was issued. A day is the difference of two
// readings, and D-17 fixes which two: the ones taken right after two consecutive local midnights
// (design_settings.budget_timezone — the zone the ledger counts its days in, so the two columns of the
// report are days of the same calendar). The reading at a midnight closes the day before it and is
// kept as the BASE of the day it opens (ai_provider_usage_snapshot: a deploy is a restart, and a base
// held in memory would be lost with its day). Between midnights the hourly tick re-writes today's
// running partial against that base.
//
// WHO READS WHEN IS NOT WHAT DECIDES. Every reading — the hourly tick's, the midnight timer's, an
// after-save RunNow's — is judged by settle from the base and the instant the request was built, the
// same way. The midnight timer only makes sure a reading exists right after each midnight; if an
// hourly tick happens to be the first reading inside the window, it closes the day, and the timer's
// reading after it is today's first partial. So the two clocks can meet on fetchMu in either order and
// the result is the same.

const (
	// midnightGrace — how long after a local midnight the closing reading is taken. The spend of the
	// day's last seconds reaches the counter on the PROVIDER's clock; half a minute lets it land before
	// the reading that closes the day. The price is the new day's first half minute counted in the old
	// day: cents at most, and still counted exactly once.
	midnightGrace = 30 * time.Second

	// midnightWindow — how late a closing reading may still be: it closes the day only when built in
	// [midnight + grace, midnight + grace + window). The midnight timer can wait on fetchMu behind one
	// provider of an hourly tick (fetchTimeout and a write) and then spend fetchTimeout on its own
	// request; five minutes covers both with room. A later reading carries minutes of the new day, so it
	// closes nothing (the missed-midnight rule): the day before keeps its last hourly partial.
	midnightWindow = 5 * time.Minute
)

// errNoSnapshotStore — a cumulative provider with nowhere to keep its base (a Worker built without
// one): a failure, never a silent skip, because the provider's line would quietly stop.
var errNoSnapshotStore = errors.New("reconcile: no store for the base of a cumulative counter")

// reading is one answer of a cumulative counter, with everything its verdict depends on.
type reading struct {
	usage      decimal.Decimal // the counter, rounded to the base column's six places
	at         time.Time       // when the request was built: the instant the reading is dated at
	zone       string          // the budget zone's name, as loaded
	loc        *time.Location
	keyChanged bool // the key differs from the one this process last read the counter with
}

// verdict is what one reading means for the days.
type verdict struct {
	row    *dayAmount // a day to write: today's partial, or the day a midnight reading closes
	rebase bool       // the reading becomes the base of today
	today  string     // the reading's local day
	level  slog.Level // how loudly the verdict is logged
	why    string     // the log line's text after `reconcile: <provider> `
}

// settle judges one reading against the base (nil = none yet). The order of the cases is the order of
// trust: a base that cannot be diffed against at all is replaced before its day is even compared.
func settle(base *entity.AIUsageSnapshot, r reading) verdict {
	local := r.at.In(r.loc)
	y, m, d := local.Date()
	// Calendar arithmetic in the zone, never ±24 h: the day a clock moves is 23 or 25 hours long.
	midnight := time.Date(y, m, d, 0, 0, 0, 0, r.loc)
	today := local.Format(dayLayout)
	yesterday := time.Date(y, m, d-1, 0, 0, 0, 0, r.loc).Format(dayLayout)
	v := verdict{today: today, level: slog.LevelWarn}
	switch {
	case base == nil:
		// The first reading ever: nothing to diff against. A base, not a number.
		v.rebase, v.level, v.why = true, slog.LevelInfo, "usage base taken; its first day is counted from the next reading"
	case strings.TrimSpace(base.BucketTZ) != r.zone:
		// The base was read at another zone's midnight: a difference against it spans no day of ours.
		v.rebase, v.why = true, "budget zone changed since the base was read; a new base"
	case r.keyChanged:
		// Another key is another counter: its history is not today's spend.
		v.rebase, v.why = true, "key changed; a new base"
	case r.usage.LessThan(base.UsageUSD):
		// A counter of spend only grows; one that went down restarted (a rotated key).
		v.rebase, v.why = true, "usage restarted; a new base"
	case base.Day == today:
		// Today's running partial, re-written by every reading until midnight closes it.
		v.row, v.level = &dayAmount{day: today, usd: r.usage.Sub(base.UsageUSD)}, slog.LevelDebug
		v.why = "today's partial written"
	case base.Day == yesterday && r.at.Before(midnight.Add(midnightGrace)):
		// After midnight, inside the grace: the closing reading is still to come (the midnight timer's,
		// or the next reading inside the window). Nothing is written and the base stays.
		v.level, v.why = slog.LevelDebug, "reading inside the midnight grace; the closing reading comes after it"
	case base.Day == yesterday && r.at.Before(midnight.Add(midnightGrace+midnightWindow)):
		// THE MIDNIGHT READING: exactly the day before — reading minus the base taken at its own midnight.
		v.row, v.rebase = &dayAmount{day: yesterday, usd: r.usage.Sub(base.UsageUSD)}, true
		v.level, v.why = slog.LevelInfo, "day closed at local midnight; a new base"
	case base.Day > today:
		// A base from a day that has not come yet: the clock moved back. Nothing sane to diff against.
		v.rebase, v.why = true, "base is dated after today (the clock moved back); a new base"
	default:
		// The midnight reading never happened (a restart, an outage, a failed fetch across midnight). NOT
		// split: a later reading carries hours of the new day, and a guess would be presented as their
		// number. The base's day keeps its last hourly partial; today counts from this reading.
		v.rebase, v.why = true, "midnight reading missed; "+base.Day+" keeps its last partial, a new base"
	}
	return v
}

// reconcileCumulative takes one reading of a cumulative counter and applies settle's verdict. Called
// with fetchMu held (reconcile).
func (w *Worker) reconcileCumulative(ctx context.Context, a adapter, key string) error {
	if w.snapshots == nil {
		return w.failed(ctx, a, "snapshot read", errNoSnapshotStore)
	}
	zone, loc, ok := w.zone(ctx)
	if !ok {
		// A day of a zone that does not load cannot be named: as if the provider had no key.
		return nil
	}
	body, win, err := w.fetch(ctx, a, key)
	if err != nil {
		return w.failed(ctx, a, "fetch", err)
	}
	usage, err := a.usage(body)
	if err != nil {
		return w.failed(ctx, a, "fetch", notUnderstood(a, err))
	}
	base, err := w.snapshots.GetUsageSnapshot(ctx, a.provider)
	if err != nil {
		return w.failed(ctx, a, "snapshot read", err)
	}
	fp := sha256.Sum256([]byte(key))
	last, known := w.keyPrints[a.provider]
	v := settle(base, reading{
		// Rounded once, here: the base stored, the base diffed against and the day written all agree
		// to the column's six places.
		usage: usage.Round(6), at: win.at, zone: zone, loc: loc,
		keyChanged: known && last != fp,
	})
	attrs := []any{slog.String("provider", a.provider), slog.String("day", v.today), slog.String("zone", zone)}
	if v.row != nil {
		attrs = append(attrs, slog.String("written_day", v.row.day), slog.String("usd", v.row.usd.String()))
	}
	slog.Default().Log(ctx, v.level, "reconcile: "+a.provider+" "+v.why, attrs...)

	var errs []error
	if v.row != nil {
		row := entity.AICostDaily{
			ProviderKey: a.provider, Day: v.row.day, AmountUSD: v.row.usd.Round(6), Currency: "USD",
			BucketTZ: zone, FetchedAt: w.now().UTC(),
		}
		if err := w.store.UpsertCostDaily(ctx, []entity.AICostDaily{row}); err != nil {
			errs = append(errs, w.failed(ctx, a, "upsert", err))
		}
	}
	if v.rebase {
		// Written even when the closing row failed: the midnight reading is today's true base either
		// way. Without it the next hour would find yesterday's base, take the missed-midnight rule, and
		// today would lose everything spent before that hour.
		sn := entity.AIUsageSnapshot{ProviderKey: a.provider, UsageUSD: usage.Round(6), Day: v.today, BucketTZ: zone, TakenAt: win.at.UTC()}
		if err := w.snapshots.PutUsageSnapshot(ctx, sn); err != nil {
			errs = append(errs, w.failed(ctx, a, "snapshot write", err))
		}
	}
	if len(errs) > 0 {
		// The key is not remembered: a base the new key's reading failed to become must be retried as
		// a key change at the next reading, not diffed against.
		return errors.Join(errs...)
	}
	w.keyPrints[a.provider] = fp
	return nil
}

// zone is the organisation's budget zone as the key source knows it now, loaded. "" (the registry
// before its first Reload) is entity.DefaultBudgetTimezone — the zone aiprov.Ledger stamps day_local in
// for the same blank, so their days and ours stay the same days. A name that does not load answers
// ok=false and is logged once per name: nothing is read or written until it loads.
func (w *Worker) zone(ctx context.Context) (string, *time.Location, bool) {
	name := ""
	if w.keys != nil {
		name = strings.TrimSpace(w.keys.BudgetTimezone())
	}
	if name == "" {
		name = entity.DefaultBudgetTimezone
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		if w.once("zone", name) {
			slog.Default().ErrorContext(ctx, "reconcile: the budget zone does not load; openrouter's days are not read until it does",
				slog.String("zone", safeWord(name)))
		}
		return "", nil, false
	}
	w.once("zone", "") // a zone that loads re-arms the line for the next one that does not
	return name, loc, true
}

// nextMidnight is when the next closing reading is due: the first local midnight + grace strictly
// after now. A CALENDAR computation in loc, never now + 24 h: the day a clock moves is 23 or 25 hours
// long, and a timer counted in fixed hours would read the counter an hour off midnight all season.
func nextMidnight(now time.Time, loc *time.Location) time.Time {
	y, m, d := now.In(loc).Date()
	due := time.Date(y, m, d, 0, 0, 0, 0, loc).Add(midnightGrace)
	if !due.After(now) {
		due = time.Date(y, m, d+1, 0, 0, 0, 0, loc).Add(midnightGrace)
	}
	return due
}

// runMidnights is the second clock: ONE timer, armed for the next local midnight + grace and re-armed
// after every reading, computed afresh from the clock and the zone each time (a zone edited in design
// settings moves the next midnight). It never runs inside the hourly runOnce — a backoff there must
// not delay the reading that closes a day — and meets it only on fetchMu, through reconcile.
//
// NO BACKOFF HERE, AND NO RETRY: a failed closing reading is read again by the next HOURLY tick, and
// settle decides what that reading is — the closing one when it still falls inside the window, else
// the missed-midnight rule (the day keeps its last partial).
func (w *Worker) runMidnights(ctx context.Context) {
	for {
		// No zone to count midnights in: look again in an hour (the line saying why is zone's).
		delay := w.c.Interval
		if _, loc, ok := w.zone(ctx); ok {
			now := w.now()
			delay = nextMidnight(now, loc).Sub(now)
		}
		fire, stop := w.newMidnightTimer(delay)
		select {
		case <-fire:
			w.midnightOnce(ctx)
		case <-ctx.Done():
			stop()
			return
		}
	}
}

// midnightOnce takes the closing reading of every cumulative provider with a key. Its failures are
// logged and marked by reconcile; it never marks success — LastSuccess is the hourly tick's word on
// every provider, and a clean midnight must not clear another provider's error.
func (w *Worker) midnightOnce(ctx context.Context) {
	defer saferun.Recover(ctx, Name)
	// A panic is a failure in /statusz, as in runOnce (registered after Recover, so it runs first).
	done := false
	defer func() {
		if !done {
			w.tracker.MarkError(errRunPanicked)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()
	for _, a := range adapters {
		if a.cumulative {
			_ = w.reconcile(ctx, a)
		}
	}
	done = true
}
