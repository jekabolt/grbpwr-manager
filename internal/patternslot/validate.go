// Package patternslot owns validation rules for pattern-file material slots.
package patternslot

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// Validate rejects more than one pattern file on the same cloth of one tech card that has nothing
// stored yet — a create, a clone, an archive import. It is ValidateAgainst with an empty stored set.
func Validate(patterns []entity.TechCardSizePattern, lines []entity.RollGoodsLine) error {
	return ValidateAgainst(nil, nil, patterns, lines)
}

// ValidateAgainst rejects a save that puts a pattern file into a material slot beside a file it did
// not already share that slot with in the stored set. One file per slot is the rule; a slot that
// ALREADY holds two files (stored before the rule, or by an older client) stays writable — otherwise
// every edit of that card, however unrelated, would be refused and the card could never be repaired.
// Removing one of the two saves; adding a third, or a second file to any other slot, is refused.
// Picking which of two stored files survives is the operator's call, never made here.
//
// Two files share a slot when their bindings resolve to the same ТКАНЬ (entity.FabricScopeIdentity,
// the identity areas, markers and the client's scopeKeyOfBinding bucket by), not merely when the two
// rows name the same word. A sheet bound to the legacy line L and a sheet bound to the purpose L was
// sorted into are two sheets on one cloth; a fusible line is its own cloth even when its sheet echoes
// the purpose beside the line key.
//
// stored/storedLines are the card's pattern rows and roll-goods lines before this save; patterns/lines
// are what the save would commit. A sheet is matched to its stored self by line_key, else by
// (size, url) — the same keys the store's upsert-diff matches by. Rows naming neither purpose nor
// line are unbound and deliberately do not share a limited slot.
func ValidateAgainst(stored []entity.TechCardSizePattern, storedLines []entity.RollGoodsLine,
	patterns []entity.TechCardSizePattern, lines []entity.RollGoodsLine) error {
	together := storedSlotMates(stored, storedLines)
	slots := make(map[string][]int, len(patterns))
	for i := range patterns {
		binding, field := bindingOf(patterns[i], i, lines)
		if binding == "" {
			continue
		}
		// Purposes are a lowercase vocabulary and line keys uppercase ULIDs, so folding case only
		// merges spellings of one key, never two different cloths.
		key := strings.ToLower(binding)
		for _, mate := range slots[key] {
			if together.shared(patterns[mate], patterns[i]) {
				continue
			}
			label := slotLabel(binding, lines)
			return entity.NewFieldViolation(field,
				fmt.Sprintf("material slot %s already holds a pattern file", label),
				patternName(patterns[mate], mate),
				"keep exactly one pattern file in this material slot, or bind the new file to another slot")
		}
		slots[key] = append(slots[key], i)
	}
	return nil
}

// slotMates remembers which stored sheets already shared one slot: sheet key → slot group ids.
type slotMates map[string][]int

func storedSlotMates(stored []entity.TechCardSizePattern, lines []entity.RollGoodsLine) slotMates {
	bySlot := make(map[string][]int, len(stored))
	order := make([]string, 0, len(stored))
	for i := range stored {
		binding, _ := bindingOf(stored[i], i, lines)
		if binding == "" {
			continue
		}
		key := strings.ToLower(binding)
		if _, ok := bySlot[key]; !ok {
			order = append(order, key)
		}
		bySlot[key] = append(bySlot[key], i)
	}
	mates := slotMates{}
	for group, key := range order {
		rows := bySlot[key]
		if len(rows) < 2 {
			continue
		}
		for _, i := range rows {
			for _, k := range sheetKeys(stored[i]) {
				mates[k] = append(mates[k], group)
			}
		}
	}
	return mates
}

// shared reports whether a and b were already in one slot of the stored set.
func (m slotMates) shared(a, b entity.TechCardSizePattern) bool {
	if len(m) == 0 {
		return false
	}
	groups := make(map[int]bool)
	for _, k := range sheetKeys(a) {
		for _, g := range m[k] {
			groups[g] = true
		}
	}
	for _, k := range sheetKeys(b) {
		for _, g := range m[k] {
			if groups[g] {
				return true
			}
		}
	}
	return false
}

func sheetKeys(p entity.TechCardSizePattern) []string {
	keys := make([]string, 0, 2)
	if lk := strings.ToUpper(strings.TrimSpace(p.LineKey)); lk != "" {
		keys = append(keys, "line:"+lk)
	}
	if url := strings.TrimSpace(p.URL); url != "" {
		keys = append(keys, fmt.Sprintf("pair:%d|%s", p.SizeId, url))
	}
	return keys
}

// slotLabel names a slot the way the operator sees it: the purpose word, or the BOM line's material
// name. A line key is an internal id and is never printed.
func slotLabel(identity string, lines []entity.RollGoodsLine) string {
	identity = strings.TrimSpace(identity)
	if strings.ToLower(identity) == identity {
		return fmt.Sprintf("%q", identity)
	}
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l.LineKey), identity) {
			if name := strings.TrimSpace(l.Name); name != "" {
				return fmt.Sprintf("%q", name)
			}
			return "of an unnamed fabric line"
		}
	}
	return "of a fabric line no longer on the BOM"
}

func patternName(p entity.TechCardSizePattern, index int) string {
	for _, candidate := range []string{p.Name.String, p.Filename.String} {
		if name := strings.TrimSpace(candidate); name != "" {
			return name
		}
	}
	return fmt.Sprintf("patterns[%d]", index)
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
