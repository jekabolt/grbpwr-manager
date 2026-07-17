package techcard

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/jekabolt/grbpwr-manager/internal/stylenumber"
)

// SuggestStyleNumber proposes the next free style number for a season (Q1): {SEASON}{YY}-{SEQ},
// where SEQ is one past the highest numeric suffix already used under that season prefix. The
// proposal is advisory — the caller may accept it (source=generated) or override it (source=manual);
// the global UNIQUE(style_number) index is the authority on collisions. The scan reads every article
// sharing the season prefix so it also steps over a manual number that happens to occupy the slot
// (almost always a no-op). Returns an error for an invalid season (unknown code / out-of-range year).
func (s *Store) SuggestStyleNumber(ctx context.Context, seasonCode string, seasonYear int) (string, error) {
	prefix, err := stylenumber.Prefix(seasonCode, seasonYear)
	if err != nil {
		return "", err
	}
	rows, err := storeutil.QueryListNamed[struct {
		StyleNumber string `db:"style_number"`
	}](ctx, s.DB,
		`SELECT style_number FROM tech_card WHERE style_number LIKE :prefix`,
		map[string]any{"prefix": escapeLike(prefix) + "%"})
	if err != nil {
		return "", fmt.Errorf("suggest style number: scan season prefix %q: %w", prefix, err)
	}
	existing := make(map[string]struct{}, len(rows))
	maxSeq := 0
	for _, r := range rows {
		existing[r.StyleNumber] = struct{}{}
		// Count only pure-numeric suffixes as generated sequences; manual suffixes (e.g. "-JKT") do
		// not advance the sequence but are still in `existing` so a probe cannot collide with one.
		if n, err := strconv.Atoi(strings.TrimPrefix(r.StyleNumber, prefix)); err == nil && n > maxSeq {
			maxSeq = n
		}
	}
	for seq := maxSeq + 1; ; seq++ {
		candidate, err := stylenumber.Generate(seasonCode, seasonYear, seq)
		if err != nil {
			return "", err
		}
		if _, taken := existing[candidate]; !taken {
			return candidate, nil
		}
	}
}
