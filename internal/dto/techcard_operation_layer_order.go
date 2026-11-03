package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// operationLayerOrderFromPb parses TechCardOperation.layer_order (0445): the step's input keys, bottom
// to top. Shape only: a sane bound and no repeats. It is never checked against the step's inputs,
// because a stale key is the reader's to skip and must not make a later save refuse.
func operationLayerOrderFromPb(i int, raw []string) (entity.TechCardLayerOrder, error) {
	var out entity.TechCardLayerOrder
	if len(raw) > entity.TechCardOperationMaxLayerOrder {
		return nil, entity.NewFieldViolation(stepPath(i)+".layer_order", "too_many", "",
			fmt.Sprintf("a step orders at most %d inputs", entity.TechCardOperationMaxLayerOrder))
	}
	seen := make(map[string]int, len(raw))
	for j, key := range raw {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		field := fmt.Sprintf("%s.layer_order[%d]", stepPath(i), j)
		if len(key) > entity.TechCardOperationLayerOrderKeyMaxLen {
			return nil, entity.NewFieldViolation(field, "too_long", key,
				fmt.Sprintf("an input key is up to %d characters", entity.TechCardOperationLayerOrderKeyMaxLen))
		}
		if prev, dup := seen[key]; dup {
			return nil, entity.NewFieldViolation(field, "duplicate",
				fmt.Sprintf("%s.layer_order[%d]", stepPath(i), prev), "a step names each input once")
		}
		seen[key] = j
		out = append(out, key)
	}
	return out, nil
}
