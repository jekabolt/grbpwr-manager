// Package shopdomains re-checks the custom domains of team shops that are not live yet (X18-L5):
// every minute for the first hour after a domain is attached, then every 30 minutes, and after
// 7 days a domain still not live is marked failed (the «check» button in the shop screen retries).
// A domain that turns live (or changes state) drops this instance's caches and redraws the shop.
package shopdomains

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/health"
	"github.com/jekabolt/grbpwr-manager/internal/platformstore"
	"github.com/jekabolt/grbpwr-manager/internal/saferun"
	"github.com/jekabolt/grbpwr-manager/internal/shop"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

const (
	// Tick is how often the worker looks for due domains.
	Tick = time.Minute
	// FastFor is how long after attach a domain is checked every FastEvery.
	FastFor   = time.Hour
	FastEvery = time.Minute
	// SlowEvery is the check interval after FastFor.
	SlowEvery = 30 * time.Minute
	// GiveUpAfter is the age after which a domain still not live is marked failed.
	GiveUpAfter = 7 * 24 * time.Hour

	teamTimeout = 2 * time.Minute
	backoffBase = time.Minute
	backoffMax  = 30 * time.Minute
)

// Store is what the worker reads and writes in the control plane (platformstore.Store).
type Store interface {
	ShopHosts(ctx context.Context, teamID int64) ([]platformstore.ShopHost, error)
	SetShopHostStatus(ctx context.Context, teamID int64, host, status string, records []platformstore.ShopRecord, now time.Time) error
}

// Worker re-checks pending custom domains.
type Worker struct {
	st Store
	dp shop.DomainProvider
	// teams runs one tick over every active team (app.workerTeamLoop: the X15 background slots).
	teams func(context.Context, func(context.Context) bool) bool
	// changed runs after a domain's state changed (caches, shop redraw); may be nil.
	changed func(ctx context.Context, teamID int64)
	now     func() time.Time

	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	tracker health.Tracker
}

// New builds the worker. teams must visit each team with its tenant in the context.
func New(st Store, dp shop.DomainProvider, teams func(context.Context, func(context.Context) bool) bool, changed func(context.Context, int64)) *Worker {
	return &Worker{st: st, dp: dp, teams: teams, changed: changed, now: time.Now}
}

// Name implements health.Reporter.
func (w *Worker) Name() string { return "shopdomains" }

// LastSuccess implements health.Reporter.
func (w *Worker) LastSuccess() time.Time { return w.tracker.LastSuccess() }

// Start launches the worker goroutine.
func (w *Worker) Start(ctx context.Context) error {
	if w.stop != nil {
		return fmt.Errorf("shopdomains worker already started")
	}
	w.ctx, w.stop = context.WithCancel(ctx)
	w.wg.Go(func() { w.run(w.ctx) })
	return nil
}

// Stop signals the worker to exit and waits for it.
func (w *Worker) Stop() error {
	if w.stop == nil {
		return fmt.Errorf("shopdomains worker already stopped or not started")
	}
	w.stop()
	w.stop = nil
	w.wg.Wait()
	return nil
}

func (w *Worker) run(ctx context.Context) {
	ticker := time.NewTicker(Tick)
	defer ticker.Stop()
	w.RunOnce(ctx)
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.RunOnce(ctx) {
				failures = 0
				continue
			}
			failures++
			delay := backoff(failures)
			slog.Default().WarnContext(ctx, "shopdomains: backing off after failed tick",
				slog.Int("consecutive_failures", failures), slog.Duration("delay", delay))
			t := time.NewTimer(delay)
			select {
			case <-t.C:
			case <-ctx.Done():
				t.Stop()
				return
			}
		}
	}
}

func backoff(n int) time.Duration {
	d := backoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= backoffMax {
			return backoffMax
		}
	}
	return d
}

// RunOnce is one tick over every team; false when any team failed.
func (w *Worker) RunOnce(ctx context.Context) bool {
	defer saferun.Recover(ctx, "shopdomains")
	if w.teams == nil {
		return false
	}
	ok := w.teams(ctx, w.team)
	if ok {
		w.tracker.MarkSuccess()
	}
	return ok
}

// Due reports whether a pending host should be checked now, and whether it is past giving up.
func Due(h platformstore.ShopHost, now time.Time) (due, giveUp bool) {
	age := now.Sub(h.CreatedAt)
	every := SlowEvery
	if age < FastFor {
		every = FastEvery
	}
	if h.CheckedAt != nil && now.Sub(*h.CheckedAt) < every-time.Second {
		return false, false
	}
	return true, age >= GiveUpAfter
}

func pending(h platformstore.ShopHost) bool {
	return h.Kind == platformstore.ShopHostCustom &&
		(h.Status == platformstore.ShopHostRecordsNeeded || h.Status == platformstore.ShopHostVerifying)
}

// team checks the due pending domains of the team in ctx.
func (w *Worker) team(ctx context.Context) bool {
	t, ok := tenant.From(ctx)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, teamTimeout)
	defer cancel()
	hosts, err := w.st.ShopHosts(ctx, t.ID)
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "shopdomains: list hosts", slog.String("err", err.Error()))
		return false
	}
	good := true
	for _, h := range hosts {
		if !pending(h) {
			continue
		}
		due, giveUp := Due(h, w.now())
		if !due {
			continue
		}
		if !w.check(ctx, t.ID, h, giveUp) {
			good = false
		}
	}
	return good
}

func (w *Worker) check(ctx context.Context, teamID int64, h platformstore.ShopHost, giveUp bool) bool {
	state, err := w.dp.Check(ctx, h.Host)
	if err != nil {
		w.tracker.MarkError(err)
		slog.Default().WarnContext(ctx, "shopdomains: provider check", slog.String("host", h.Host), slog.String("err", err.Error()))
		return false
	}
	status := state.Status
	switch status {
	case platformstore.ShopHostRecordsNeeded, platformstore.ShopHostVerifying, platformstore.ShopHostLive:
	default:
		status = platformstore.ShopHostFailed
	}
	if giveUp && status != platformstore.ShopHostLive {
		status = platformstore.ShopHostFailed
	}
	recs := make([]platformstore.ShopRecord, 0, len(state.Records))
	for _, r := range state.Records {
		recs = append(recs, platformstore.ShopRecord{Type: r.Type, Name: r.Name, Value: r.Value, Found: r.Found})
	}
	if err := w.st.SetShopHostStatus(ctx, teamID, h.Host, status, recs, w.now()); errors.Is(err, platformstore.ErrShopHostNotFound) {
		return true // removed while the check ran
	} else if err != nil {
		w.tracker.MarkError(err)
		slog.Default().ErrorContext(ctx, "shopdomains: store status", slog.String("host", h.Host), slog.String("err", err.Error()))
		return false
	}
	if status != h.Status {
		slog.Default().InfoContext(ctx, "shopdomains: domain state changed",
			slog.String("host", h.Host), slog.String("from", h.Status), slog.String("to", status))
		if w.changed != nil {
			w.changed(ctx, teamID)
		}
	}
	return true
}
