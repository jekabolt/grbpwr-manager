// Package patternrefs contains the pure reference accounting used before pattern-object GC.
package patternrefs

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const objectPathSegment = "tech-card-patterns"

// ObjectKey extracts the canonical object-store key from either the origin or CDN spelling of a
// managed pattern URL. Host ownership is intentionally left to the bucket deletion boundary.
func ObjectKey(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	key := strings.Trim(u.Path, "/")
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		if segment == objectPathSegment && i < len(segments)-1 {
			return key, true
		}
	}
	return "", false
}

// ObjectURLReferenced reports whether candidate and one of refs name the same object. Managed
// origin/CDN spellings compare by canonical object key; non-managed URLs compare literally so an
// old row with an unusual URL still fails closed instead of being treated as disposable.
func ObjectURLReferenced(candidate string, refs []string) bool {
	candidateKey, managed := ObjectKey(candidate)
	for _, ref := range refs {
		if managed {
			if refKey, ok := ObjectKey(ref); ok && refKey == candidateKey {
				return true
			}
			continue
		}
		if ref == candidate {
			return true
		}
	}
	return false
}

// SnapshotCanBeDeleted is the retention rule for a snapshot reached from a deleted history row.
// Restore-generated snapshots are durable undo points even after their last history link disappears;
// every other snapshot is removable only after no history row references it.
func SnapshotCanBeDeleted(reason string, remainingHistoryRefs int) bool {
	return remainingHistoryRefs == 0 && !strings.HasPrefix(strings.TrimSpace(reason), "restore:")
}

// UnreferencedObjectURLs returns managed candidates whose canonical keys do not occur in refs.
// Candidate order is stable and duplicate origin/CDN spellings collapse to the first one.
func UnreferencedObjectURLs(candidates, refs []string) []string {
	referenced := make(map[string]struct{}, len(refs))
	for _, raw := range refs {
		if key, ok := ObjectKey(raw); ok {
			referenced[key] = struct{}{}
		}
	}
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, len(candidates))
	for _, raw := range candidates {
		key, ok := ObjectKey(raw)
		if !ok {
			continue
		}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		if _, live := referenced[key]; !live {
			out = append(out, raw)
		}
	}
	return out
}

type snapshotPattern struct {
	URL     string            `json:"url"`
	History []snapshotPattern `json:"history"`
}

type snapshotInsert struct {
	Patterns []snapshotPattern `json:"patterns"`
}

type snapshotDocument struct {
	TechCard                   *snapshotInsert   `json:"techCard"`
	TechCardSnake              *snapshotInsert   `json:"tech_card"`
	Patterns                   []snapshotPattern `json:"patterns"`
	RemovedPatternHistory      []snapshotPattern `json:"removedPatternHistory"`
	RemovedPatternHistorySnake []snapshotPattern `json:"removed_pattern_history"`
}

// URLsFromSnapshots extracts pattern URLs from the common.TechCard proto-JSON shape used by both
// release snapshots and pre-update card snapshots. The top-level patterns fallback admits an older
// TechCardInsert-only blob without making the GC parser depend on generated protobuf packages.
func URLsFromSnapshots(blobs []string) ([]string, error) {
	var out []string
	for i, blob := range blobs {
		var doc snapshotDocument
		if err := json.Unmarshal([]byte(blob), &doc); err != nil {
			return nil, fmt.Errorf("snapshot %d: %w", i, err)
		}
		patterns := doc.Patterns
		if doc.TechCard != nil {
			patterns = append(patterns, doc.TechCard.Patterns...)
		}
		if doc.TechCardSnake != nil {
			patterns = append(patterns, doc.TechCardSnake.Patterns...)
		}
		patterns = append(patterns, doc.RemovedPatternHistory...)
		patterns = append(patterns, doc.RemovedPatternHistorySnake...)
		out = appendSnapshotPatternURLs(out, patterns)
	}
	return out, nil
}

func appendSnapshotPatternURLs(out []string, patterns []snapshotPattern) []string {
	for _, pattern := range patterns {
		if pattern.URL != "" {
			out = append(out, pattern.URL)
		}
		out = appendSnapshotPatternURLs(out, pattern.History)
	}
	return out
}
