package storeutil

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
)

const patternObjectPathSegment = "tech-card-patterns"

// UnreferencedPatternObjectURLs returns candidate pattern URLs that no tech card or fitting row
// references after the caller's mutation. It is intended to run inside the same write transaction
// that removed the references, so rolled-back mutations never produce cleanup candidates. The caller
// performs the external object deletion only after commit.
//
// Only URLs whose path contains the bucket-owned tech-card-patterns segment are eligible. Pattern
// fields historically accepted arbitrary URLs, and object cleanup must never turn one of those into
// a deletion request against an unrelated bucket key.
func UnreferencedPatternObjectURLs(ctx context.Context, db dependency.DB, candidates []string) ([]string, error) {
	eligible := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, raw := range candidates {
		if !isManagedPatternObjectURL(raw) {
			continue
		}
		if _, ok := seen[raw]; ok {
			continue
		}
		seen[raw] = struct{}{}
		eligible = append(eligible, raw)
	}
	if len(eligible) == 0 {
		return nil, nil
	}

	refs, err := QueryListNamed[struct {
		URL string `db:"url"`
	}](ctx, db, `
		SELECT DISTINCT url FROM (
			SELECT url FROM tech_card_size_pattern WHERE url IN (:urls)
			UNION ALL
			SELECT url FROM fitting_pattern WHERE url IN (:urls)
		) pattern_refs`, map[string]any{"urls": eligible})
	if err != nil {
		return nil, fmt.Errorf("check remaining pattern object references: %w", err)
	}
	referenced := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		referenced[ref.URL] = struct{}{}
	}

	orphaned := make([]string, 0, len(eligible))
	for _, raw := range eligible {
		if _, ok := referenced[raw]; !ok {
			orphaned = append(orphaned, raw)
		}
	}
	return orphaned, nil
}

func isManagedPatternObjectURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return false
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, segment := range segments {
		if segment == patternObjectPathSegment && i < len(segments)-1 {
			return true
		}
	}
	return false
}
