package storeutil

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

const patternObjectPathSegment = "tech-card-patterns"

// ResolvePatternName applies the presence-gated write semantics of a pattern's display
// name across a full-replace save, shared by the tech-card and fitting stores. The
// payload's null state is proto presence, not emptiness — Valid=false means the field was
// absent (a stale client that predates it), so the row being replaced keeps its stored
// name (prior; the zero value when the row is new). A present name is written as given,
// with present-empty normalised to NULL (an explicit clear).
func ResolvePatternName(payload, prior sql.NullString) sql.NullString {
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
	candidateByKey := make(map[string]string, len(candidates))
	candidateKeys := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		key, ok := PatternObjectKey(raw)
		if !ok {
			continue
		}
		if _, seen := candidateByKey[key]; seen {
			continue
		}
		candidateByKey[key] = raw
		candidateKeys = append(candidateKeys, key)
	}
	if len(candidateKeys) == 0 {
		return nil, nil
	}

	refs, err := QueryListNamed[struct {
		URL string `db:"url"`
	}](ctx, db, `
		SELECT url FROM tech_card_size_pattern
		UNION
		SELECT url FROM fitting_pattern`, nil)
	if err != nil {
		return nil, fmt.Errorf("check remaining pattern object references: %w", err)
	}
	referencedKeys := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if key, ok := PatternObjectKey(ref.URL); ok {
			referencedKeys[key] = struct{}{}
		}
	}

	orphaned := make([]string, 0, len(candidateKeys))
	for _, key := range candidateKeys {
		if _, ok := referencedKeys[key]; !ok {
			orphaned = append(orphaned, candidateByKey[key])
		}
	}
	return orphaned, nil
}

// PatternObjectKey extracts the canonical S3 key from a syntactically valid pattern URL. It does
// not decide whether the host belongs to this deployment because storeutil deliberately has no
// bucket configuration; Bucket.DeleteObjects performs that ownership check before any side effect.
func PatternObjectKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	key := strings.Trim(u.Path, "/")
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		if segment == patternObjectPathSegment && i < len(segments)-1 {
			return key, true
		}
	}
	return "", false
}
