package store

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
)

// EnableTeamCache registers this store as the per-team cache loader and turns on the cache's strict
// mode when the pool is in teams mode (a ctx without a team then reads zero values, never team #1's).
// With teams mode off it changes nothing a request can see: team #1's snapshot is filled by InitConsts
// at boot and never lazily reloaded.
func (ms *MYSQLStore) EnableTeamCache() {
	if ms.router != nil {
		cache.SetStrict(ms.router.Strict())
	}
	cache.SetLoader(ms.LoadTeamCache)
}

// LoadTeamCache reads the cached data of the team in ctx from that team's own database: the dictionary
// revisions first (so a change landing mid-load reads as stale on the next freshness check), then the
// dictionary payload; the hero and the house margin target are best effort, as at boot, and fall back
// to the built-in defaults.
func (ms *MYSQLStore) LoadTeamCache(ctx context.Context) (cache.TeamData, error) {
	revs, err := ms.dictionaryStore.GetDictionaryRevisions(ctx)
	if err != nil {
		return cache.TeamData{}, fmt.Errorf("dictionary revisions: %w", err)
	}
	di, err := ms.settingsStore.GetDictionaryInfo(ctx)
	if err != nil {
		return cache.TeamData{}, fmt.Errorf("dictionary info: %w", err)
	}
	out := cache.TeamData{Dict: di, Revisions: revs}
	if hf, err := ms.Hero().GetHero(ctx); err != nil {
		slog.Default().WarnContext(ctx, "team cache: can't load hero; using an empty one", slog.String("err", err.Error()))
	} else {
		out.Hero = hf
	}
	if t, err := ms.Metrics().GetAlertThresholds(ctx); err != nil {
		slog.Default().WarnContext(ctx, "team cache: can't load house target margin; using the built-in default", slog.String("err", err.Error()))
	} else {
		out.TargetMarginPct = t.TargetMarginPct
	}
	return out, nil
}
