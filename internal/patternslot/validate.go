// Package patternslot owns validation rules for pattern-file material slots.
package patternslot

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Validate rejects more than one pattern file on the same cloth of one tech card.
//
// Two files share a slot when their bindings resolve to the same ТКАНЬ (entity.FabricScopeIdentity,
// the identity areas, markers and the client's scopeKeyOfBinding bucket by), not merely when the two
// rows name the same word. A sheet bound to the legacy line L and a sheet bound to the purpose L was
// sorted into are two sheets on one cloth; a fusible line is its own cloth even when its sheet echoes
// the purpose beside the line key.
//
// lines are the card's roll-goods lines. nil (the transport layer, which has no BOM) falls back to
// the raw binding — purpose, else the legacy line key — which is what the identity rule reduces to
// with no lines; the store repeats the check with the card's lines. Rows naming neither are unbound
// and deliberately do not share a limited slot.
func Validate(patterns []entity.TechCardSizePattern, lines []entity.RollGoodsLine) error {
	seen := make(map[string]int, len(patterns))
	for i := range patterns {
		binding, field := bindingOf(patterns[i], i, lines)
		if binding == "" {
			continue
		}
		// Purposes are a lowercase vocabulary and line keys uppercase ULIDs, so folding case only
		// merges spellings of one key, never two different cloths.
		key := strings.ToLower(binding)
		first, duplicate := seen[key]
		if !duplicate {
			seen[key] = i
			continue
		}
		return entity.NewFieldViolation(field,
			fmt.Sprintf("material slot binding %q is used by more than one pattern file", binding),
			fmt.Sprintf("patterns[%d]", first),
			"keep exactly one pattern file in this material slot, or bind one of the files to another slot")
	}
	return nil
}

// Bound reports how many rows carry any binding at all — fewer than two cannot collide, so a caller
// may skip loading the card's lines.
func Bound(patterns []entity.TechCardSizePattern) int {
	n := 0
	for i := range patterns {
		if purpose, lineKey := normalized(patterns[i]); purpose != "" || lineKey != "" {
			n++
		}
	}
	return n
}

func normalized(pattern entity.TechCardSizePattern) (purpose, lineKey string) {
	if pattern.FabricPurpose.Valid {
		purpose = strings.ToLower(strings.TrimSpace(pattern.FabricPurpose.String))
	}
	if pattern.BomLineKey.Valid {
		lineKey = strings.ToUpper(strings.TrimSpace(pattern.BomLineKey.String))
	}
	return purpose, lineKey
}

func bindingOf(pattern entity.TechCardSizePattern, index int, lines []entity.RollGoodsLine) (string, string) {
	purpose, lineKey := normalized(pattern)
	if purpose == "" && lineKey == "" {
		return "", ""
	}
	field := fmt.Sprintf("patterns[%d].bom_line_key", index)
	if purpose != "" {
		field = fmt.Sprintf("patterns[%d].fabric_purpose", index)
	}
	return strings.TrimSpace(entity.FabricScopeIdentity(purpose, lineKey, lines)), field
}
