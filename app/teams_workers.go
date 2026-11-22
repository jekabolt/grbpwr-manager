package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

const (
	teamWorkerParallel   = 4
	taskBoardHealDelay   = 5 * time.Minute
	taskBoardHealTimeout = 30 * time.Second
)

type teamWorkerScope uint8

const (
	activeTeams teamWorkerScope = iota
	activeAndSuspendedTeams
)

// teamWorkerLoop is the seam the individual workers use: one invocation is one worker tick over
// every applicable team. The worker still owns its interval, timeout, backoff and health tracker.
type teamWorkerLoop func(context.Context, func(context.Context) bool) bool

func (a *App) initWorkerTeams() error {
	if a.platform != nil {
		a.workerTeams = a.platform.Directory()
		return nil
	}
	dir, err := tenant.NewStaticDirectory(a.teamOne)
	if err != nil {
		return fmt.Errorf("background teams: %w", err)
	}
	a.workerTeams = dir
	return nil
}

func (a *App) workerTeamLoop(name string, scope teamWorkerScope) teamWorkerLoop {
	return func(ctx context.Context, tick func(context.Context) bool) bool {
		if !sharesBackgroundSlots(name) {
			return runTeamWorkerTick(ctx, a.workerTeams, name, scope, teamWorkerParallel, tick)
		}
		slots := a.backgroundSlots()
		return runTeamWorkerTick(ctx, a.workerTeams, name, scope, teamWorkerParallel, func(teamCtx context.Context) bool {
			// One shared bound across every team worker (X15): ticks of all workers together.
			select {
			case slots <- struct{}{}:
			case <-teamCtx.Done():
				return false
			}
			defer func() { <-slots }()
			return tick(teamCtx)
		})
	}
}

// sharesBackgroundSlots: archivecleanup is bucket listing (up to its 5-minute tick timeout on a
// slow bucket) with one small UPDATE; in the shared slots it could hold both for minutes (X15).
func sharesBackgroundSlots(name string) bool {
	return name != "archivecleanup"
}

// runTeamWorkerTick binds each team to its own context and isolates its result. ListActive excludes
// pending/provisioning/deleted teams; the status check is repeated here so a test double or future
// directory implementation cannot accidentally route work to one of them. A fixed semaphore bounds
// database pressure without serialising healthy teams behind a slow one.
func runTeamWorkerTick(
	ctx context.Context,
	dir tenant.Directory,
	name string,
	scope teamWorkerScope,
	parallel int,
	tick func(context.Context) bool,
) bool {
	if err := ctx.Err(); err != nil {
		return false
	}
	teams, err := dir.ListActive(ctx)
	if err != nil {
		slog.Default().ErrorContext(ctx, "background worker: can't list teams",
			slog.String("worker", name), slog.String("err", err.Error()))
		return false
	}
	if parallel < 1 {
		parallel = 1
	}

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var failed atomic.Bool
	for _, team := range teams {
		if team.Status != tenant.StatusActive &&
			!(scope == activeAndSuspendedTeams && team.Status == tenant.StatusSuspended) {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			failed.Store(true)
			wg.Wait()
			return false
		}

		wg.Add(1)
		go func(team tenant.Team) {
			defer wg.Done()
			defer func() { <-sem }()
			teamCtx := tenant.With(ctx, team)
			ok := false
			defer func() {
				if recovered := recover(); recovered != nil {
					failed.Store(true)
					slog.Default().ErrorContext(teamCtx, "background worker: team tick panicked; continuing",
						slog.String("worker", name), slog.Int64("tid", team.ID),
						slog.String("slug", team.Slug), slog.Any("panic", recovered),
						slog.String("stack", string(debug.Stack())))
					return
				}
				if !ok {
					failed.Store(true)
					if ctx.Err() == nil {
						slog.Default().ErrorContext(teamCtx, "background worker: team tick failed; continuing",
							slog.String("worker", name), slog.Int64("tid", team.ID),
							slog.String("slug", team.Slug))
					}
				}
			}()
			ok = tick(teamCtx)
		}(team)
	}
	wg.Wait()
	return !failed.Load() && ctx.Err() == nil
}

// startDelayedTaskBoardHeal preserves the second, post-deploy-overlap pass while routing it to
// every active database. The startup repair in store.New remains team #1-only because newly
// provisioned databases are born on the current schema and cannot contain pre-0420 drift.
func (a *App) startDelayedTaskBoardHeal(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		timer := time.NewTimer(taskBoardHealDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		a.workerTeamLoop("taskboardheal", activeTeams)(ctx, func(teamCtx context.Context) bool {
			tickCtx, tickCancel := context.WithTimeout(teamCtx, taskBoardHealTimeout)
			defer tickCancel()
			n, err := a.db.Tasks().HealTaskBoardIds(tickCtx)
			if err != nil {
				slog.Default().ErrorContext(teamCtx, "can't heal task board ids",
					slog.String("pass", "delayed"), slog.String("err", err.Error()))
				return false
			}
			if n > 0 {
				slog.Default().WarnContext(teamCtx, "healed task board ids",
					slog.String("pass", "delayed"), slog.Int64("rows", n))
			}
			return true
		})
	}()
	return func() {
		cancel()
		wg.Wait()
	}
}
