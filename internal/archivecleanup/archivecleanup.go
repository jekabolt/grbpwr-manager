// Package archivecleanup expires tech-card ZIP archives (Ф5.1).
//
// TWO HALVES OF ONE FACT. The export writes a private object under techcard-archives/ and hands
// out a link that dies in ten minutes; the import writes one under techcard-imports/ and leaves a
// tech_card_import row that a human is expected to come back and commit. After the retention
// window (bucket.ArchiveRetention — seven days, an owner decision) the bytes go, and the row that
// still claims it could become a card has to stop claiming it. Both halves run on the same tick
// and off the same cutoff, because a row that says "uploaded" after its object is deleted is an
// operator pressing commit onto a 404. "The same cutoff" is literal and structural: runCleanup
// reads the clock exactly once and passes that INSTANT to both the bucket listing and the expiry
// UPDATE. Neither takes a duration, so neither can quietly restart the countdown from its own
// "now" — which is precisely what the listing used to do.
//
// AGE IS THE ONLY CRITERION, AND THAT IS DELIBERATE. This worker never looks at an import row to
// decide whether to delete an object. It is tempting — a row that is neither committed nor failed
// "looks abandoned" — and it is exactly the wrong instinct, twice over:
//
//   - The commit handler, when its transaction fails ambiguously, KNOWINGLY leaves the files it
//     placed rather than risk deleting the bytes of a commit that actually landed. Those files are
//     NOT this worker's to reclaim — they are media/pattern objects in segments this worker must
//     never touch; the commit logs their keys and the minted media rows read as unused in the media
//     library. What this worker does own is the import's ARCHIVE OBJECT, which it removes by age
//     alone — including after a successful commit — needing to know nothing about why it is there.
//   - A row can be in a state this package has never heard of. Statuses are held in Go (0336
//     declined a CHECK) and the import pipeline is still growing; deleting bytes on the strength
//     of a status that turns out to mean "in flight" would destroy an import mid-commit.
//
// So: an object younger than the window is never touched, whatever any row says about it, and an
// object older than the window always goes, whatever any row says about it.
package archivecleanup

import (
	"context"
	"fmt"
	"sync"
	"time"

	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/bucket"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

// tickTimeout bounds one cleanup tick.
//
// It is minutes, not the thirty seconds the DB-only cleanup workers use, because most of this tick
// is NETWORK: a listing of two bucket segments plus one HTTPS DELETE per aged-out object. A week
// of exports is a few hundred objects, and thirty seconds would cut the sweep off mid-way every
// tick — harmless (the survivors are simply older next hour, and the whole tick is idempotent by
// construction) but it would mean the backlog never actually drains.
const tickTimeout = 5 * time.Minute

// Backoff bounds for consecutive-failure backoff. On a failed tick an extra delay
// (base * 2^(n-1), capped at backoffMax) is waited before the next iteration so a persistently
// failing dependency (the bucket or the DB) isn't hammered every WorkerInterval. A successful tick
// resets the backoff. Same shape as ordercleanup/storefrontcleanup.
const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

// cleanableSegments — the ONLY folders this worker is allowed to name.
//
// The bucket refuses anything else (isCleanableSegment guards ListObjectsOlderThan), so a fourth
// name here would be an error rather than a catastrophe; the list is spelled out anyway so that
// "which folders does the sweeper touch" is answerable by reading this file, and so that a test
// can assert the answer without a bucket. The values are the bucket's own aliases: one definition
// of each folder name, shared with the export, the import and the presign guard.
var cleanableSegments = []string{bucket.ArchiveSegment, bucket.ImportSegment}

// Config holds configuration for the archive cleanup worker.
//
// The retention period is deliberately NOT here. It is bucket.ArchiveRetention — one number, an
// owner decision, already read by the object half and the row half — and a knob would be a second
// place for it to be true. The interval is the only thing worth varying, and only downward, for
// tests.
type Config struct {
	WorkerInterval time.Duration `mapstructure:"worker_interval"`
}

// DefaultConfig returns default configuration values.
func DefaultConfig() Config {
	return Config{
		WorkerInterval: 1 * time.Hour,
	}
}

// Worker deletes expired tech-card archive/import objects from the bucket and marks the import
// rows whose archive is gone as expired.
type Worker struct {
	repo    dependency.Repository
	files   dependency.FileStore
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker

	// now is the tick's clock, a field ONLY so a test can fail the promise this package opens
	// with: that one tick reads the clock ONCE and hands the same instant to both halves.
	//
	// Comparing the instants the two halves received cannot prove it, and that is a measured fact
	// rather than a worry: on darwin time.Now() has microsecond granularity, so two consecutive
	// reads return the IDENTICAL wall instant (20 of 20 pairs, checked). An equality assertion
	// therefore stays green on a worker that reads the clock twice — exactly the defect this
	// field exists to catch — while on a nanosecond-resolution kernel the same assertion would
	// start failing at random. A clock that visibly moves on every read makes the question
	// answerable on any platform: count the reads.
	now func() time.Time
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "archivecleanup" }

// LastSuccess implements health.Reporter (zero time until the first clean tick).
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New creates a new archive cleanup worker. A nil config means DefaultConfig.
func New(c *Config, repo dependency.Repository, files dependency.FileStore) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.WorkerInterval == 0 {
		c.WorkerInterval = DefaultConfig().WorkerInterval
	}
	return &Worker{
		repo:  repo,
		files: files,
		c:     c,
		now:   time.Now,
	}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("archive cleanup worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.worker(w.ctx)
	})
	return nil
}

// Stop signals the worker to stop and WAITS for its goroutine to exit.
//
// The wait is the whole point, not politeness: this worker's tick writes to the database (the
// expiry UPDATE), and app.Stop closes the connection pool a few lines after calling this. Return
// before the goroutine is gone and shutdown races an in-flight UPDATE against a closed pool. Same
// contract, and the same reason, as ordercleanup.Stop and storefrontcleanup.Stop.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("archive cleanup worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()

	// consecutiveFailures drives the extra backoff delay applied after a failed tick.
	// Reset to 0 on the first successful tick.
	var consecutiveFailures int

	for {
		select {
		case <-ticker.C:
			if w.runCleanup(ctx) {
				consecutiveFailures = 0
				continue
			}
			consecutiveFailures++
			delay := backoffDelay(consecutiveFailures)
			slog.Default().WarnContext(ctx, "archive cleanup: backing off after failed tick",
				slog.Int("consecutive_failures", consecutiveFailures),
				slog.Duration("delay", delay),
			)
			// Wait the extra backoff on top of the ticker interval, but stay responsive to
			// shutdown — never time.Sleep blindly.
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// backoffDelay returns the extra inter-iteration delay for the given number of consecutive
// failures: base * 2^(n-1), capped at backoffMax.
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

// runCleanup performs a single cleanup tick and reports whether it fully succeeded.
//
// The halves do not gate each other. A bucket that is refusing listings must not stop the rows
// from expiring, and a database that is refusing writes must not stop the bytes from going: each
// half is a truth about the same window, and delaying one because the other failed would only
// widen the interval during which they disagree. Both feed one verdict, so a partial tick never
// reads as a healthy one on /statusz.
func (w *Worker) runCleanup(ctx context.Context) bool {
	defer saferun.Recover(ctx, "archivecleanup")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	// ONE instant for the whole tick, and this line is the only place in the sweep that reads a
	// clock. Both halves take an absolute cutoff — the bucket listing and the expiry UPDATE — so
	// "the same boundary said two ways" is now something the code enforces rather than something
	// this file asserts. It used to hand the bucket an AGE, and the bucket stamped its own "now"
	// inside the call: two starting instants for one tick, and a package doc promising one.
	cutoff := w.now().UTC().Add(-bucket.ArchiveRetention)

	ok := true
	if !w.removeExpiredObjects(ctx, cutoff) {
		ok = false
	}
	if !w.expireStaleImportRows(ctx, cutoff) {
		ok = false
	}

	// Record success only when both halves completed without error, so staleness on /statusz
	// reflects real failures.
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}

// removeExpiredObjects deletes every object in the feature's two segments older than the retention
// window, and reports whether it got through both segments cleanly.
//
// Listing and removing are two calls on purpose: the selection is decided by ONE argument (the
// tick's cutoff) that can be inspected without deleting anything. A failure in one segment does not
// skip the other — they age out independently and one bad listing is no reason to keep the other
// segment's week-old bytes.
func (w *Worker) removeExpiredObjects(ctx context.Context, cutoff time.Time) bool {
	ok := true
	for _, segment := range cleanableSegments {
		// The tick's cutoff, the same instant the row half gets: this is the single knob that keeps
		// a not-yet-committed import uploaded THIS MORNING out of the selection. Anything younger
		// than the window is invisible to this call, whatever state its row is in.
		keys, err := w.files.ListObjectsOlderThan(ctx, segment, cutoff)
		if err != nil {
			ok = false
			w.tracker.MarkError(err)
			slog.Default().ErrorContext(ctx, "archive cleanup: can't list expired objects",
				slog.String("segment", segment), slog.String("err", err.Error()))
			continue
		}
		if len(keys) == 0 {
			continue
		}
		if err := w.files.RemoveObjectsByKeys(ctx, keys...); err != nil {
			ok = false
			w.tracker.MarkError(err)
			// Best-effort by contract: RemoveObjectsByKeys reports the FIRST failure and keeps
			// going, so some of these keys are gone and some are not. Whatever survived is simply
			// older next hour — the tick is idempotent, and nothing here needs to be undone.
			slog.Default().ErrorContext(ctx, "archive cleanup: can't remove expired objects",
				slog.String("segment", segment), slog.Int("count", len(keys)),
				slog.String("err", err.Error()))
			continue
		}
		slog.Default().InfoContext(ctx, "archive cleanup: expired objects removed",
			slog.String("segment", segment), slog.Int("count", len(keys)))
	}
	return ok
}

// expireStaleImportRows marks the uncommitted uploads whose archive has aged out, and reports
// whether the UPDATE went through.
func (w *Worker) expireStaleImportRows(ctx context.Context, olderThan time.Time) bool {
	n, err := w.repo.TechCards().ExpireStaleTechCardImports(ctx, olderThan)
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "archive cleanup: can't expire stale import rows",
			slog.String("err", err.Error()))
		return false
	}
	if n > 0 {
		slog.Default().InfoContext(ctx, "archive cleanup: stale import rows expired",
			slog.Int64("count", n))
	}
	return true
}
