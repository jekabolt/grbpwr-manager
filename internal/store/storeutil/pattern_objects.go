package storeutil

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/patternrefs"
)

// ResolvePatternName applies the presence-gated write semantics of a pattern's display
// name across a full-replace save, shared by the tech-card and fitting stores. The
// payload's null state is proto presence, not emptiness — Valid=false means the field was
// absent (a stale client that predates it), so the row being replaced keeps its stored
// name (prior; the zero value when the row is new). A present name is written as given,
// with present-empty normalised to NULL (an explicit clear).
func ResolvePatternName(payload, prior sql.NullString) sql.NullString {
	return ResolveNullableOnPresence(payload, prior)
}

// ResolveNullableOnPresence is the generic presence-gated resolver behind ResolvePatternName:
// absent (Valid=false) keeps the prior value, present-empty clears to NULL, present writes as
// given. Referenced directly by callers whose field is NOT a display name (e.g. the pattern
// fabric binding) so a future name-specific tweak (trimming, caps) cannot silently rewrite
// binding semantics.
func ResolveNullableOnPresence(payload, prior sql.NullString) sql.NullString {
	if !payload.Valid {
		return prior
	}
	if payload.String == "" {
		return sql.NullString{}
	}
	return payload
}

// UnreferencedPatternObjectURLs returns candidate pattern URLs whose canonical object key no tech
// card or fitting row references after the caller's mutation. It is intended to run inside the same
// write transaction that removed the references, so rolled-back mutations never produce cleanup
// candidates. The caller performs the external object deletion only after commit.
//
// Stored URLs may use either the Spaces origin host or the CDN host. Both encode the same object key
// in their path, so raw-URL comparison is unsafe: removing one form must not delete an object still
// referenced through the other. Configured-host ownership is enforced at the bucket deletion
// boundary, which owns that configuration; this layer only recognizes the dedicated pattern path.
func UnreferencedPatternObjectURLs(ctx context.Context, db dependency.DB, candidates []string) ([]string, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	refs, err := QueryListNamed[struct {
		URL string `db:"url"`
	}](ctx, db, `
		SELECT url FROM tech_card_size_pattern
		UNION
		SELECT url FROM fitting_pattern
		UNION
		SELECT url FROM tech_card_size_pattern_history`, nil)
	if err != nil {
		return nil, fmt.Errorf("check remaining pattern object references: %w", err)
	}
	referencedURLs := make([]string, 0, len(refs))
	for _, ref := range refs {
		referencedURLs = append(referencedURLs, ref.URL)
	}

	snapshots, err := QueryListNamed[struct {
		Snapshot string `db:"snapshot"`
	}](ctx, db, `
		SELECT CAST(snapshot AS CHAR) AS snapshot FROM tech_card_snapshot
		UNION ALL
		SELECT snapshot FROM tech_card_release`, nil)
	if err != nil {
		return nil, fmt.Errorf("check pattern references in snapshots: %w", err)
	}
	blobs := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		blobs = append(blobs, snapshot.Snapshot)
	}
	snapshotURLs, err := patternrefs.URLsFromSnapshots(blobs)
	if err != nil {
		// A malformed historical blob must leak candidates, never delete an object it may reference.
		slog.Default().WarnContext(ctx, "pattern GC skipped because a card snapshot is unreadable",
			slog.String("err", err.Error()))
		return nil, nil
	}
	referencedURLs = append(referencedURLs, snapshotURLs...)
	return patternrefs.UnreferencedObjectURLs(candidates, referencedURLs), nil
}

// PatternObjectKey extracts the canonical S3 key from a syntactically valid pattern URL. It does
// not decide whether the host belongs to this deployment because storeutil deliberately has no
// bucket configuration; Bucket.DeleteObjects performs that ownership check before any side effect.
func PatternObjectKey(raw string) (string, bool) {
	return patternrefs.ObjectKey(raw)
}
