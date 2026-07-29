package campaigndispatch

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/mail/campaignrender"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
)

const tickTimeout = 25 * time.Second

const (
	backoffBase = 30 * time.Second
	backoffMax  = 5 * time.Minute
)

type Worker struct {
	repo     dependency.Repository
	mailer   dependency.Mailer
	renderer *campaignrender.Renderer
	c        *Config
	ctx      context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
	tracker  health.Tracker
}

func New(c *Config, repo dependency.Repository, mailer dependency.Mailer) (*Worker, error) {
	if c == nil {
		defaults := DefaultConfig()
		c = &defaults
	}
	applyDefaults(c)
	renderer, err := campaignrender.New()
	if err != nil {
		return nil, err
	}
	return &Worker{repo: repo, mailer: mailer, renderer: renderer, c: c}, nil
}

func (w *Worker) Name() string { return "campaigndispatch" }

func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

func (w *Worker) Start(ctx context.Context) error {
	if w.ctx != nil && w.stop != nil {
		return fmt.Errorf("campaign dispatch worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() { w.run(w.ctx) })
	return nil
}

func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("campaign dispatch worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(w.c.WorkerInterval)
	defer ticker.Stop()
	var failures int
	for {
		select {
		case <-ticker.C:
			if w.runOnce(ctx) {
				failures = 0
				continue
			}
			failures++
			delay := workerBackoff(failures)
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

func workerBackoff(failures int) time.Duration {
	delay := backoffBase
	for i := 1; i < failures; i++ {
		if delay >= backoffMax/2 {
			return backoffMax
		}
		delay *= 2
	}
	return delay
}

func (w *Worker) runOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "campaigndispatch")
	ctx, cancel := context.WithTimeout(ctx, tickTimeout)
	defer cancel()

	if _, err := w.repo.Campaigns().PromoteDueEmailCampaign(ctx); err != nil {
		return w.failed(ctx, "promote scheduled campaigns", err)
	}
	if _, err := w.advanceFanout(ctx); err != nil {
		return w.failed(ctx, "advance campaign fanout", err)
	}
	if err := w.drainDispatch(ctx); err != nil {
		return w.failed(ctx, "dispatch campaign batch", err)
	}
	if _, err := w.repo.Campaigns().PromoteEmailCampaignABWinners(ctx, SelectABWinner); err != nil {
		return w.failed(ctx, "promote A/B campaign winners", err)
	}
	// Winner promotion assigns held remainder rows a non-NULL variant_id, making
	// them claimable immediately in the same tick.
	if err := w.drainDispatch(ctx); err != nil {
		return w.failed(ctx, "dispatch promoted A/B campaign remainder", err)
	}
	if _, err := w.repo.Campaigns().FinalizeEmailCampaigns(ctx); err != nil {
		return w.failed(ctx, "finalize campaigns", err)
	}
	w.tracker.MarkSuccess()
	return true
}

func (w *Worker) drainDispatch(ctx context.Context) error {
	for ctx.Err() == nil {
		didWork, err := w.dispatchOne(ctx)
		if err != nil {
			return err
		}
		if !didWork {
			break
		}
	}
	return ctx.Err()
}

func (w *Worker) failed(ctx context.Context, action string, err error) bool {
	w.tracker.MarkError(err)
	slog.ErrorContext(ctx, "campaign dispatch worker failed",
		slog.String("action", action),
		slog.String("err", err.Error()))
	return false
}
