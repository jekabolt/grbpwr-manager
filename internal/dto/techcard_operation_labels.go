package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// operationLabelKeysFromPb parses the set of labels a step attaches or hangs. Membership in the
// payload is checked only after old-client carry and server minting have produced the final labels.
func operationLabelKeysFromPb(i int, raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]bool, len(raw))
	for j, value := range raw {
		key := normalizeGarmentLabelLineKey(value)
		if key == "" {
			continue
		}
		if !entity.IsValidProductionRunLineKey(key) {
			return nil, entity.NewFieldViolation(fmt.Sprintf("%s.label_keys[%d]", stepPath(i), j),
				"invalid", value, "a garment label line_key (26-character ULID)")
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, key)
		if len(out) > maxLabelEntries {
			return nil, entity.NewFieldViolation(stepPath(i)+".label_keys", "too_many", "",
				fmt.Sprintf("a card has at most %d labels", maxLabelEntries))
		}
	}
	return out, nil
}
