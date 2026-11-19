package cache

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/tenant"
)

// Versioned dictionary invalidation (R9). Each controlled dictionary namespace has a monotonic
// revision in the DB (dictionary_revision, bumped in the same tx as every mutation). This cache holds
// the revisions it last loaded; before a dictionary-dependent write, and on a background poll (<=5s),
// EnsureDictionaryFresh compares the DB revisions to the cached ones and reloads the in-memory
// dictionary when they differ — so a colour/collection/tag/country change made on one instance becomes
// visible on every instance without a process restart or a broadcast.

// The revisions are per team (teamSnap.revisions): each team's dictionary is fresh against its own
// database.

// DefaultDictionaryPollInterval is the background freshness bound required by R9 (<=5s).
const DefaultDictionaryPollInterval = 5 * time.Second

// DictionaryRevisionSource reads the authoritative per-namespace revisions (implemented by
// dependency.Dictionary, i.e. store.Dictionary()).
type DictionaryRevisionSource interface {
	GetDictionaryRevisions(ctx context.Context) (map[entity.DictionaryNamespace]int64, error)
}

// DictionaryInfoSource reloads the full dictionary payload (implemented by dependency.Cache, i.e.
// store.Cache()).
type DictionaryInfoSource interface {
	GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error)
}

// SetDictionaryRevisions records the revisions currently reflected by the team's in-memory dictionary.
func SetDictionaryRevisions(ctx context.Context, revs map[entity.DictionaryNamespace]int64) {
	s, _, err := registry.getForWrite(ctx)
	if err != nil {
		writeSkipped(ctx, "SetDictionaryRevisions", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revisions = copyRevisions(revs)
}

// GetDictionaryRevisions returns a copy of the revisions the team's cache currently reflects.
func GetDictionaryRevisions(ctx context.Context) map[entity.DictionaryNamespace]int64 {
	s := registry.get(ctx)
	if s == nil {
		return map[entity.DictionaryNamespace]int64{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyRevisions(s.revisions)
}

// DictionaryRevisionsStale reports whether any namespace in dbRevs differs from what the team's cache
// holds. A namespace the cache has never seen (zero value) counts as stale, so the first check after
// boot forces a baseline load. With no team (teams mode) it reports stale.
func DictionaryRevisionsStale(ctx context.Context, dbRevs map[entity.DictionaryNamespace]int64) bool {
	s := registry.get(ctx)
	if s == nil {
		return true
	}
	return s.revisionsStale(dbRevs)
}

func (s *teamSnap) revisionsStale(dbRevs map[entity.DictionaryNamespace]int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for ns, rev := range dbRevs {
		if s.revisions[ns] != rev {
			return true
		}
	}
	return false
}

// EnsureDictionaryFresh reloads the in-memory dictionary of the team in ctx iff that team's DB
// revisions have moved past the cached ones. It returns whether a reload happened. Call it before a
// dictionary-dependent write (resolving a colourway's dictionary family and its name, minting a SKU
// colour token that must not read as another family's code, saving a product with a
// collection/tag/country) and from the background poller. It is safe to call concurrently; a
// redundant reload is harmless. A team that is not cached is loaded first.
func EnsureDictionaryFresh(ctx context.Context, revSrc DictionaryRevisionSource, infoSrc DictionaryInfoSource) (bool, error) {
	s, tid, err := registry.getForWrite(ctx)
	if err != nil {
		return false, fmt.Errorf("dictionary cache: %w", err)
	}
	dbRevs, err := revSrc.GetDictionaryRevisions(ctx)
	if err != nil {
		return false, fmt.Errorf("read dictionary revisions: %w", err)
	}
	if !s.revisionsStale(dbRevs) {
		return false, nil
	}
	dInfo, err := infoSrc.GetDictionaryInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("reload dictionary: %w", err)
	}
	s.mu.Lock()
	s.setDictLocked(dInfo)
	if dInfo.ModelPhotoTechCardID > 0 {
		s.modelPhotoTechCardID = dInfo.ModelPhotoTechCardID
	}
	s.revisions = copyRevisions(dbRevs)
	s.mu.Unlock()
	publishDefinitions(tid, dInfo)
	return true, nil
}

// PollDictionaryRevisions runs EnsureDictionaryFresh every interval until ctx is cancelled, giving the
// <=5s cross-instance freshness bound (R9), for the team in ctx and for each extra team (teams mode:
// the storefront team; loaded right away, not on the first tick). Every tick also drops idle team
// snapshots. interval <= 0 uses DefaultDictionaryPollInterval.
func PollDictionaryRevisions(ctx context.Context, revSrc DictionaryRevisionSource, infoSrc DictionaryInfoSource, interval time.Duration, extra ...tenant.Team) {
	if interval <= 0 {
		interval = DefaultDictionaryPollInterval
	}
	pollExtra := func() {
		for _, t := range extra {
			if _, err := EnsureDictionaryFresh(tenant.With(ctx, t), revSrc, infoSrc); err != nil {
				slog.Default().ErrorContext(ctx, "dictionary revision poll failed",
					slog.Int64("tid", t.ID), slog.String("err", err.Error()))
			}
		}
	}
	pollExtra()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := EnsureDictionaryFresh(ctx, revSrc, infoSrc); err != nil {
				slog.Default().Error("dictionary revision poll failed", slog.String("err", err.Error()))
			}
			pollExtra()
			Sweep()
		}
	}
}
