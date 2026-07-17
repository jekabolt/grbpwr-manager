package cache

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Versioned dictionary invalidation (R9). Each controlled dictionary namespace has a monotonic
// revision in the DB (dictionary_revision, bumped in the same tx as every mutation). This cache holds
// the revisions it last loaded; before a dictionary-dependent write, and on a background poll (<=5s),
// EnsureDictionaryFresh compares the DB revisions to the cached ones and reloads the in-memory
// dictionary when they differ — so a colour/collection/tag/country change made on one instance becomes
// visible on every instance without a process restart or a broadcast.

var (
	dictRevMu           sync.RWMutex
	cachedDictRevisions = map[entity.DictionaryNamespace]int64{}
)

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

// SetDictionaryRevisions records the revisions currently reflected by the in-memory dictionary.
func SetDictionaryRevisions(revs map[entity.DictionaryNamespace]int64) {
	dictRevMu.Lock()
	defer dictRevMu.Unlock()
	cachedDictRevisions = make(map[entity.DictionaryNamespace]int64, len(revs))
	for ns, r := range revs {
		cachedDictRevisions[ns] = r
	}
}

// GetDictionaryRevisions returns a copy of the revisions the cache currently reflects.
func GetDictionaryRevisions() map[entity.DictionaryNamespace]int64 {
	dictRevMu.RLock()
	defer dictRevMu.RUnlock()
	out := make(map[entity.DictionaryNamespace]int64, len(cachedDictRevisions))
	for ns, r := range cachedDictRevisions {
		out[ns] = r
	}
	return out
}

// DictionaryRevisionsStale reports whether any namespace in dbRevs differs from what the cache holds.
// A namespace the cache has never seen (zero value) counts as stale, so the first check after boot
// forces a baseline load.
func DictionaryRevisionsStale(dbRevs map[entity.DictionaryNamespace]int64) bool {
	dictRevMu.RLock()
	defer dictRevMu.RUnlock()
	for ns, rev := range dbRevs {
		if cachedDictRevisions[ns] != rev {
			return true
		}
	}
	return false
}

// EnsureDictionaryFresh reloads the in-memory dictionary iff the DB revisions have moved past the
// cached ones. It returns whether a reload happened. Call it before a dictionary-dependent write
// (minting a SKU from color_code, saving a product with a collection/tag/country) and from the
// background poller. It is safe to call concurrently; a redundant reload is harmless.
func EnsureDictionaryFresh(ctx context.Context, revSrc DictionaryRevisionSource, infoSrc DictionaryInfoSource) (bool, error) {
	dbRevs, err := revSrc.GetDictionaryRevisions(ctx)
	if err != nil {
		return false, fmt.Errorf("read dictionary revisions: %w", err)
	}
	if !DictionaryRevisionsStale(dbRevs) {
		return false, nil
	}
	dInfo, err := infoSrc.GetDictionaryInfo(ctx)
	if err != nil {
		return false, fmt.Errorf("reload dictionary: %w", err)
	}
	RefreshDictionary(dInfo)
	SetDictionaryRevisions(dbRevs)
	return true, nil
}

// PollDictionaryRevisions runs EnsureDictionaryFresh every interval until ctx is cancelled, giving the
// <=5s cross-instance freshness bound (R9). interval <= 0 uses DefaultDictionaryPollInterval.
func PollDictionaryRevisions(ctx context.Context, revSrc DictionaryRevisionSource, infoSrc DictionaryInfoSource, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultDictionaryPollInterval
	}
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
		}
	}
}
