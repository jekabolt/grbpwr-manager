// Package patternslot owns validation rules for pattern-file material slots.
package patternslot

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Validate rejects more than one pattern file in the same material slot. The caller supplies the
// rows of one tech card, so the card id is already implicit. A purpose is the authoritative binding;
// the legacy BOM line key is used only when the purpose is empty. Rows with neither are unbound and
// deliberately do not share a limited slot.
func Validate(patterns []entity.TechCardSizePattern) error {
	seen := make(map[string]int, len(patterns))
	for i := range patterns {
		binding, field := bindingOf(patterns[i], i)
		if binding == "" {
			continue
		}
		first, duplicate := seen[binding]
		if !duplicate {
			seen[binding] = i
			continue
		}
		return entity.NewFieldViolation(field,
			fmt.Sprintf("material slot binding %q is used by more than one pattern file", binding),
			fmt.Sprintf("patterns[%d]", first),
			"keep exactly one pattern file in this material slot, or bind one of the files to another slot")
	}
	return nil
}

func bindingOf(pattern entity.TechCardSizePattern, index int) (string, string) {
	if pattern.FabricPurpose.Valid {
		if purpose := strings.ToLower(strings.TrimSpace(pattern.FabricPurpose.String)); purpose != "" {
			return purpose, fmt.Sprintf("patterns[%d].fabric_purpose", index)
		}
	}
	if pattern.BomLineKey.Valid {
		if lineKey := strings.ToUpper(strings.TrimSpace(pattern.BomLineKey.String)); lineKey != "" {
			return lineKey, fmt.Sprintf("patterns[%d].bom_line_key", index)
		}
	}
	return "", ""
}
