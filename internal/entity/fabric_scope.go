package entity

import "strings"

// THE rule that binds a DXF artefact to cloth, in one place (0267).
//
// A tech card's выкройка (tech_card_size_pattern, 0260) and its cut-piece DXF alias
// (tech_card_piece_dxf_block, 0262) both used to name a concrete BOM LINE. They now name a
// НАЗНАЧЕНИЕ (TechCardBomPurpose, 0265) — at card level everything a градалка draws is a CLASS:
// «это лекало основной ткани», «эта деталь кроится из подкладки». The concrete line matters only
// where the ARTICLE matters — the раскладка (fabric width and selvedge come off the article the
// colourway pins, 0259/0264) and the production run — and neither of those is stored here.
//
// The transition is ADDITIVE, and not out of caution: it is the only shape that works. 0265
// deliberately back-filled nothing, because section='fabric' is exactly where a карманка, a
// контраст and a сетка hide, so any guess would have labelled all three «основной материал»
// confidently and wrongly. A sheet bound to line L therefore CANNOT be migrated — L has no purpose
// to rewrite it to. Both fields coexist and resolution is always:
//
//	purpose first, else the legacy line.
//
// An unsorted card keeps working with zero operator action and migrates itself the moment somebody
// sorts it. Nothing is forced.
//
// Every consumer calls in here. Three copies of "purpose, else the line" would drift, and the day
// they drifted the divergence would be silent: a sheet would resolve to one cloth in the panel and
// another in the store, and nothing would fail.

// RollGoodsLine is one cloth line of a card as the DXF bindings see it — its stable line_key and
// its НАЗНАЧЕНИЕ ("" = not sorted yet, the state of every line predating 0265). Only the four
// roll-goods sections (see the store's rollGoodsSectionList) ever appear here: a purpose on a
// thread or a button would be data no screen renders.
type RollGoodsLine struct {
	LineKey string
	Purpose string
}

// FabricScope is one binding, resolved.
type FabricScope struct {
	// Key is the UNIQUENESS bucket the binding files under, and it is exactly what the generated
	// column tech_card_piece_dxf_block.scope_key holds: COALESCE(fabric_purpose, bom_line_key).
	// Computed by the same function on both sides on purpose — the index and Go agree by
	// construction rather than by agreement. "" means the binding names nothing at all.
	Key string
	// ByPurpose reports WHICH half of the COALESCE answered. The caller needs it for the message it
	// writes: «no BOM line with this назначение» and «no such BOM line» send an operator to two
	// different controls.
	ByPurpose bool
	// LineKeys are the roll-goods BOM lines this binding addresses, in the order the card lists
	// them. A purpose legitimately owns SEVERAL — that is the whole point of the field. EMPTY means
	// the binding dangles: the purpose has no lines sorted into it yet, or the legacy line was
	// deleted / reclassified out of roll goods. Dangling is a UI STATE («слот удалён»), not an
	// error — the store only refuses a binding that dangles the moment it is first written.
	LineKeys []string
}

// Live reports whether the binding currently resolves to at least one cloth line.
func (s FabricScope) Live() bool { return len(s.LineKeys) > 0 }

// FabricScopeKey is the pure half of the rule: the uniqueness bucket, computable without the card's
// BOM. dto uses it to reject a payload that would collapse two aliases onto one key before the
// transaction opens; the store uses it to key stored rows against payload rows; MySQL computes the
// identical value into scope_key. Values are used verbatim — purposes are a closed lowercase
// vocabulary (chk_*_fabric_purpose) and line keys are uppercased ULIDs on entry, so neither side
// needs to fold case to agree.
func FabricScopeKey(purpose, bomLineKey string) string {
	if p := strings.TrimSpace(purpose); p != "" {
		return p
	}
	return strings.TrimSpace(bomLineKey)
}

// ResolveFabricScope resolves one binding against the card's roll-goods lines.
//
// The purpose WINS even when no line carries it yet. Scope is a bucket, not a lookup: demoting to
// the legacy line whenever a purpose happens to be empty would silently re-scope every alias of a
// card the moment somebody cleared a назначение on the BOM tab, and the aliases would land in a
// different uniqueness bucket than the one they were written into.
func ResolveFabricScope(purpose, bomLineKey string, lines []RollGoodsLine) FabricScope {
	p := strings.TrimSpace(purpose)
	if p != "" {
		out := FabricScope{Key: p, ByPurpose: true}
		for _, l := range lines {
			if strings.EqualFold(strings.TrimSpace(l.Purpose), p) && strings.TrimSpace(l.LineKey) != "" {
				out.LineKeys = append(out.LineKeys, l.LineKey)
			}
		}
		return out
	}
	key := strings.TrimSpace(bomLineKey)
	out := FabricScope{Key: key}
	if key == "" {
		return out
	}
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l.LineKey), key) {
			out.LineKeys = append(out.LineKeys, l.LineKey)
			break
		}
	}
	return out
}

// PurposesOfRollGoods lists the назначения actually present on a card's cloth lines, deduplicated,
// in the order the lines appear. This is what a binding may legally name — offering the full
// eight-value vocabulary would let an operator bind a sheet to a purpose the card does not use.
func PurposesOfRollGoods(lines []RollGoodsLine) []string {
	seen := make(map[string]bool, len(lines))
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		p := strings.TrimSpace(l.Purpose)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
