package app

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/jekabolt/grbpwr-manager/internal/airollup"
	"github.com/jekabolt/grbpwr-manager/internal/designgen"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// AI for every team on the owner's keys (teams wave, 02-DESIGN §10). Everything here is teams mode
// ON only: with PLATFORM_MYSQL_DSN empty none of it is built, and the registry, the design worker,
// the sweeper and the ledger run exactly as before teams (team #1 on the root ctx, no rollup).

// envAISpendSpikeUSD — optional: the absolute daily spend (USD, per team) above which the rollup logs
// ai_spend_spike. Empty or invalid = airollup.DefaultSpikeUSD ($20).
const envAISpendSpikeUSD = "AI_SPEND_SPIKE_USD"

// aiConfigPin is the registry's team pin: every registry store call reads team #1's AI config,
// whatever team the caller's ctx carries. nil with teams mode off (the identity, as before).
func (a *App) aiConfigPin() func(context.Context) (context.Context, error) {
	if a.platform == nil {
		return nil
	}
	dir := a.platform.Directory()
	return func(ctx context.Context) (context.Context, error) {
		return tenant.Pinned(ctx, dir, tenant.TeamOneID)
	}
}

// envAITeamParallel — optional: how many teams the AI loops serve at once (default 2, at most 8).
// Small because every team in flight holds database connections.
const envAITeamParallel = "AI_TEAM_PARALLEL"

func aiTeamParallel() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(envAITeamParallel)))
	if err != nil {
		n = 0
	}
	return airollup.ClampParallel(n)
}

// wireDesignTeams makes the design worker (or, with generation off, the reserve sweeper) run per
// team. Cleanup — revive, overdue, ledger sweep, reserve release — runs for every listed team,
// suspended and `ai`-off included; only active teams with `ai` on claim and spend. Teams mode off:
// nothing is set and both keep their one-tick-on-the-root-ctx behaviour.
func (a *App) wireDesignTeams() {
	if a.platform == nil {
		return
	}
	dir := a.platform.Directory()
	par := aiTeamParallel()
	if a.dgw != nil {
		a.dgw.SetTeamLoops(designgen.TeamLoop(airollup.CleanupLoop(dir, par)),
			designgen.TeamLoop(airollup.WorkLoop(dir, a.platform.FeatureOverrides, par)), par)
	}
	if a.dgs != nil {
		a.dgs.SetTeamLoop(designgen.TeamLoop(airollup.CleanupLoop(dir, par)))
	}
}

// startAIRollup starts the per-team AI spend rollup into the control plane (teams mode on only).
func (a *App) startAIRollup(ctx context.Context) (stop func(), err error) {
	if a.platform == nil {
		return func() {}, nil
	}
	w, err := airollup.New(airollup.Config{
		Dir:   a.platform.Directory(),
		Teams: a.db.DB(),
		Sink:  a.platform,
		TZ: func() string {
			if a.aireg == nil {
				return entity.DefaultBudgetTimezone
			}
			return a.aireg.BudgetTimezone() // team #1's design_settings.budget_timezone (pinned)
		},
		SpikeUSD: aiSpendSpikeUSD(ctx),
	})
	if err != nil {
		return nil, err
	}
	if err := w.Start(ctx); err != nil {
		return nil, err
	}
	return w.Stop, nil
}

// aiSpendSpikeUSD reads AI_SPEND_SPIKE_USD; empty, unparsable or ≤ 0 = the default (WARN when set
// but unusable).
func aiSpendSpikeUSD(ctx context.Context) decimal.Decimal {
	raw := strings.TrimSpace(os.Getenv(envAISpendSpikeUSD))
	def := decimal.NewFromInt(airollup.DefaultSpikeUSD)
	if raw == "" {
		return def
	}
	v, err := decimal.NewFromString(raw)
	if err != nil || !v.IsPositive() {
		slog.Default().WarnContext(ctx, "AI_SPEND_SPIKE_USD is not a positive number; the default is used",
			slog.String("value", raw), slog.String("default_usd", def.String()))
		return def
	}
	return v
}
