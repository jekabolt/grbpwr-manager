// Package patternobject implements the pattern_object_access store — per-object access
// state (revocation epoch, expiry, coarse stats) behind the tokenized pattern read path.
package patternobject

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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
		`SELECT id, object_key, filename, epoch, expires_at, revoked_at, last_access_at, access_count, created_at
		 FROM pattern_object_access WHERE id = :id`, map[string]any{"id": id})
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// maxObjectKeyBytes mirrors object_key VARCHAR(512). A longer key cannot round-trip: the
// INSERT would truncate (strict mode downgrades that to a warning under INSERT IGNORE) and
// the read-back would then miss it, leaving a junk row behind.
const maxObjectKeyBytes = 512

// maxFilenameBytes mirrors filename VARCHAR(255); an over-long name is dropped rather
// than truncated (the disposition falls back to the key basename).
const maxFilenameBytes = 255

// EnsureByKeys returns access rows for every given object key, creating missing rows
// lazily (epoch 0, no expiry). Used by the read path to mint tokens for objects uploaded
// before this table existed. Duplicate keys are collapsed.
//
// This runs on every card/fitting READ, so it is SELECT-first: the steady state (all rows
// exist) costs exactly one query and writes nothing — an unconditional per-key INSERT
// would put a binlog write behind every admin read. Only the misses are inserted, in one
// multi-row INSERT IGNORE, which also keeps the concurrency guarantee: the unique key on
// object_key means a racing ensure cannot double-insert or reset epoch/policy.
func (s *Store) EnsureByKeys(ctx context.Context, refs []entity.PatternObjectRef) (map[string]entity.PatternObjectAccess, error) {
	uniq := make([]entity.PatternObjectRef, 0, len(refs))
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if ref.Key == "" {
			continue
		}
		if len(ref.Key) > maxObjectKeyBytes {
			slog.Default().WarnContext(ctx, "pattern object key too long for access row",
				slog.Int("len", len(ref.Key)), slog.Int("max", maxObjectKeyBytes))
			continue
		}
		if _, dup := seen[ref.Key]; dup {
			continue
		}
		seen[ref.Key] = struct{}{}
		if len(ref.Filename) > maxFilenameBytes {
			ref.Filename = ""
		}
		uniq = append(uniq, ref)
	}
	if len(uniq) == 0 {
		return map[string]entity.PatternObjectAccess{}, nil
	}
	keys := make([]string, 0, len(uniq))
	for _, ref := range uniq {
		keys = append(keys, ref.Key)
	}
	out, err := s.loadByKeys(ctx, keys)
	if err != nil {
		return nil, err
	}
	missing := make([]entity.PatternObjectRef, 0, len(uniq))
	backfill := make([]entity.PatternObjectRef, 0)
	for _, ref := range uniq {
		row, ok := out[ref.Key]
		if !ok {
			missing = append(missing, ref)
			continue
		}
		// Legacy rows predate the column; fill it once, when a caller supplies the name.
		if !row.Filename.Valid && ref.Filename != "" {
			backfill = append(backfill, ref)
		}
	}
	for _, ref := range backfill {
		if err := storeutil.ExecNamed(ctx, s.DB,
			`UPDATE pattern_object_access SET filename = :filename
			 WHERE object_key = :key AND filename IS NULL`,
			map[string]any{"filename": ref.Filename, "key": ref.Key}); err != nil {
			return nil, fmt.Errorf("backfill pattern object filename: %w", err)
		}
		row := out[ref.Key]
		row.Filename = sql.NullString{String: ref.Filename, Valid: true}
		out[ref.Key] = row
	}
	if len(missing) == 0 {
		return out, nil
	}
	// INSERT IGNORE, not a plain insert: between the SELECT above and this statement a
	// concurrent ensure may have created the same key, and that is a no-op here rather
	// than a duplicate-key error (the unique key is what actually guarantees one row).
	args := make([]any, 0, len(missing)*2)
	missingKeys := make([]string, 0, len(missing))
	for _, ref := range missing {
		var name any
		if ref.Filename != "" {
			name = ref.Filename
		}
		args = append(args, ref.Key, name)
		missingKeys = append(missingKeys, ref.Key)
	}
	q := `INSERT IGNORE INTO pattern_object_access (object_key, filename) VALUES ` +
		strings.TrimSuffix(strings.Repeat("(?,?),", len(missing)), ",")
	if _, err := s.DB.ExecContext(ctx, q, args...); err != nil {
		return nil, fmt.Errorf("ensure pattern object access rows: %w", err)
	}
	created, err := s.loadByKeys(ctx, missingKeys)
	if err != nil {
		return nil, err
	}
	for k, r := range created {
		out[k] = r
	}
	return out, nil
}

func (s *Store) loadByKeys(ctx context.Context, keys []string) (map[string]entity.PatternObjectAccess, error) {
	list, err := storeutil.QueryListNamed[entity.PatternObjectAccess](ctx, s.DB,
		`SELECT id, object_key, filename, epoch, expires_at, revoked_at, last_access_at, access_count, created_at
		 FROM pattern_object_access WHERE object_key IN (:keys)`, map[string]any{"keys": keys})
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
