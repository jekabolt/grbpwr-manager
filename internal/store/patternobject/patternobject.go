// Package patternobject implements the pattern_object_access store — per-object access
// state (revocation epoch, expiry, coarse stats) behind the tokenized pattern read path.
package patternobject

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// Store implements dependency.PatternObjects.
type Store struct {
	storeutil.Base
}

// New creates a new pattern object access store.
func New(base storeutil.Base) *Store {
	return &Store{Base: base}
}

// GetById loads one access row. sql.ErrNoRows when absent.
func (s *Store) GetById(ctx context.Context, id int64) (*entity.PatternObjectAccess, error) {
	row, err := storeutil.QueryNamedOne[entity.PatternObjectAccess](ctx, s.DB,
		`SELECT id, object_key, epoch, expires_at, revoked_at, last_access_at, access_count, created_at
		 FROM pattern_object_access WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// EnsureByKeys returns access rows for every given object key, creating missing rows
// lazily (epoch 0, no expiry). Used by the read path to mint tokens for objects uploaded
// before this table existed. Duplicate keys are collapsed; unknown keys are created in
// one statement, then everything is read back in one query.
func (s *Store) EnsureByKeys(ctx context.Context, keys []string) (map[string]entity.PatternObjectAccess, error) {
	uniq := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		if k == "" {
			continue
		}
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		uniq = append(uniq, k)
	}
	if len(uniq) == 0 {
		return map[string]entity.PatternObjectAccess{}, nil
	}
	// INSERT IGNORE keeps the epoch/policy of existing rows untouched — creation is lazy
	// and must never reset state on a concurrent ensure. A card carries at most a few
	// dozen patterns, so per-key statements are fine.
	for _, k := range uniq {
		if err := storeutil.ExecNamed(ctx, s.DB,
			`INSERT IGNORE INTO pattern_object_access (object_key) VALUES (:key)`,
			map[string]any{"key": k}); err != nil {
			return nil, fmt.Errorf("ensure pattern object access row: %w", err)
		}
	}
	list, err := storeutil.QueryListNamed[entity.PatternObjectAccess](ctx, s.DB,
		`SELECT id, object_key, epoch, expires_at, revoked_at, last_access_at, access_count, created_at
		 FROM pattern_object_access WHERE object_key IN (:keys)`, map[string]any{"keys": uniq})
	if err != nil {
		return nil, fmt.Errorf("load pattern object access rows: %w", err)
	}
	out := make(map[string]entity.PatternObjectAccess, len(list))
	for _, r := range list {
		out[r.ObjectKey] = r
	}
	return out, nil
}

// BumpEpoch invalidates every token minted for the object so far (they embed the old
// epoch). The object itself and its row survive; new tokens mint against the new epoch.
func (s *Store) BumpEpoch(ctx context.Context, id int64) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`UPDATE pattern_object_access SET epoch = epoch + 1, revoked_at = NULL WHERE id = :id`,
		map[string]any{"id": id})
	if err != nil {
		return fmt.Errorf("bump pattern object epoch: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Revoke hard-disables the object's access rows-wide (all scopes, all epochs) until
// un-revoked. Distinct from BumpEpoch: a bump rotates, revoke turns off.
func (s *Store) Revoke(ctx context.Context, id int64, at time.Time) error {
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`UPDATE pattern_object_access SET revoked_at = :at WHERE id = :id`,
		map[string]any{"id": id, "at": at})
	if err != nil {
		return fmt.Errorf("revoke pattern object access: %w", err)
	}
	if rows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// RecordAccess folds a debounced batch of access stats into the rows. Best effort — the
// authoritative audit trail is the slog line per request; these columns exist for the UI.
func (s *Store) RecordAccess(ctx context.Context, counts map[int64]int64, last map[int64]time.Time) error {
	var firstErr error
	for id, n := range counts {
		params := map[string]any{"id": id, "n": n, "last": last[id]}
		if err := storeutil.ExecNamed(ctx, s.DB,
			`UPDATE pattern_object_access SET access_count = access_count + :n, last_access_at = :last WHERE id = :id`,
			params); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("record pattern object access: %w", err)
		}
	}
	return firstErr
}

// DeleteByKeys removes access rows for objects that no longer exist (GC of orphaned
// pattern objects deletes the binary; the row would otherwise 404 forever — correct but
// dead weight, design R9).
func (s *Store) DeleteByKeys(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := storeutil.ExecNamed(ctx, s.DB,
		`DELETE FROM pattern_object_access WHERE object_key IN (:keys)`,
		map[string]any{"keys": keys}); err != nil {
		return fmt.Errorf("delete pattern object access rows: %w", err)
	}
	return nil
}

// ErrNotFound aliases sql.ErrNoRows for callers that should not import database/sql.
var ErrNotFound = errors.New("pattern object access row not found")
