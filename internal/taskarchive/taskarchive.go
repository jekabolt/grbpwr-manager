// Package taskarchive keeps the active DONE column bounded by soft-archiving cards whose latest
// transition into DONE is older than the configured retention period.
package taskarchive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

const (
	workerInterval         = time.Hour
	tickTimeout            = 30 * time.Second
	defaultRetentionPeriod = 14 * 24 * time.Hour
)

// Config holds the age after which a card that is still in DONE is soft-archived.
type Config struct {
	RetentionPeriod time.Duration `mapstructure:"retention_period"`
}

// DefaultConfig returns the owner-approved fourteen-day DONE retention.
func DefaultConfig() Config {
	return Config{RetentionPeriod: defaultRetentionPeriod}
}

// Worker selects eligible IDs in one read, then sends every ID through the same transactional
// ArchiveTask path used by the RPC. That preserves column compaction and any future archive-side
// effects without duplicating them in background code.
type Worker struct {
	tasks   dependency.Tasks
	c       *Config
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
	now     func() time.Time
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "taskarchive" }

// LastSuccess implements health.Reporter.
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// New creates the hourly task auto-archive worker.
func New(c *Config, tasks dependency.Tasks) *Worker {
	if c == nil {
		dc := DefaultConfig()
		c = &dc
	}
	if c.RetentionPeriod <= 0 {
		c.RetentionPeriod = DefaultConfig().RetentionPeriod
	}
	return &Worker{tasks: tasks, c: c, now: time.Now}
}

// Start starts the worker.
func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("task archive worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() {
		w.worker(w.ctx)
	})
	return nil
}

// Stop cancels the loop and waits for any in-flight DB writes before App closes the pool.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("task archive worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) worker(ctx context.Context) {
	ticker := time.NewTicker(workerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			w.runOnce(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "taskarchive")

	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	cutoff := w.now().UTC().Add(-w.c.RetentionPeriod)
	ids, err := w.tasks.ListTaskIdsToAutoArchive(ctx, cutoff)
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "can't list tasks eligible for auto-archive",
			slog.String("err", err.Error()))
		return false
	}

	ok := true
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			w.tracker.MarkError(err)
			return false
		}
		if err := w.tasks.ArchiveTask(ctx, id); err != nil {
			// Another app instance may have selected and archived the same card between this worker's
			// read and write. ArchiveTask reports that normal race as sql.ErrNoRows.
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			ok = false
			w.tracker.MarkError(err)
			slog.Default().ErrorContext(ctx, "can't auto-archive DONE task",
				slog.Int("task_id", id), slog.String("err", err.Error()))
			continue
		}
		slog.Default().InfoContext(ctx, "auto-archived DONE task", slog.Int("task_id", id))
	}
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}
