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
	Section TechCardBomSection
}

// FabricScope is one binding, resolved.
type FabricScope struct {
	// Key is the binding key used by write validation/diffing. It equals the stored COALESCE bucket
	// for ordinary roll goods; a binding that names an INTERLINING line resolves to that line even
	// when an old client also echoed its purpose. Cross-table readers use FabricScopeIdentity.
	// "" means the binding names nothing at all.
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

// EffectiveFabricScopeKey is the fabric identity a BOM line uses today. INTERLINING is physical
// material, not a purpose class: every fusible line owns its sheets, aliases and measured areas.
// All other roll-goods sections retain the purpose-first rule introduced by 0267.
//
// FabricScopeKey deliberately remains the raw stored-row bucket (the DB's COALESCE). Do not use
// this helper to key a row without a BOM line: changing that would disagree with the generated
// scope_key column and its unique indexes.
func EffectiveFabricScopeKey(line RollGoodsLine) string {
	if line.Section == BomSectionInterlining {
		return strings.TrimSpace(line.LineKey)
	}
	return FabricScopeKey(line.Purpose, line.LineKey)
}

// LegacyInterliningPurposeScope returns the purpose bucket an interlining line may read for
// compatibility. It is intentionally available only when that purpose identifies exactly one
// interlining line on the card. Callers must prefer the line scope whenever it contains data;
// this function supplies a fallback, never a second scope to merge.
func LegacyInterliningPurposeScope(line RollGoodsLine, lines []RollGoodsLine) (string, bool) {
	if line.Section != BomSectionInterlining {
		return "", false
	}
	purpose := strings.TrimSpace(line.Purpose)
	key := strings.TrimSpace(line.LineKey)
	if purpose == "" || key == "" {
		return "", false
	}
	match := ""
	for _, candidate := range lines {
		if candidate.Section != BomSectionInterlining ||
			!strings.EqualFold(strings.TrimSpace(candidate.Purpose), purpose) ||
			strings.TrimSpace(candidate.LineKey) == "" {
			continue
		}
		if match != "" {
			return "", false
		}
		match = candidate.LineKey
	}
	if !strings.EqualFold(strings.TrimSpace(match), key) {
		return "", false
	}
	return purpose, true
}

// FabricScopeReadKeys lists a line's effective scope followed by its optional legacy purpose
// fallback. A reader checks the first key for data before trying the second; it must never merge
// both. For non-interlining lines the list always contains the unchanged purpose-or-line key.
func FabricScopeReadKeys(line RollGoodsLine, lines []RollGoodsLine) []string {
	key := EffectiveFabricScopeKey(line)
	if key == "" {
		return nil
	}
	out := []string{key}
	if legacy, ok := LegacyInterliningPurposeScope(line, lines); ok && !strings.EqualFold(legacy, key) {
		out = append(out, legacy)
	}
	return out
}

// ResolveFabricScope resolves one binding against the card's roll-goods lines.
//
// Purpose wins for every ordinary roll-goods line even when no line carries it yet. A named
// INTERLINING line is the one exception: each fusible is its own scope. A purpose-only legacy
// interlining binding remains readable only when exactly one such line carries that purpose.
func ResolveFabricScope(purpose, bomLineKey string, lines []RollGoodsLine) FabricScope {
	p := strings.TrimSpace(purpose)
	key := strings.TrimSpace(bomLineKey)
	// A named interlining line always wins over a purpose carried beside it. Old clients commonly
	// round-trip both fields; folding such a row back into the shared purpose would recreate the
	// collision this rule removes.
	if key != "" {
		for _, l := range lines {
			if l.Section == BomSectionInterlining && strings.EqualFold(strings.TrimSpace(l.LineKey), key) {
				return FabricScope{Key: l.LineKey, LineKeys: []string{l.LineKey}}
			}
		}
	}
	if p != "" {
		out := FabricScope{Key: p, ByPurpose: true}
		legacyInterlining := ""
		legacyInterliningCount := 0
		for _, l := range lines {
			if !strings.EqualFold(strings.TrimSpace(l.Purpose), p) || strings.TrimSpace(l.LineKey) == "" {
				continue
			}
			if l.Section == BomSectionInterlining {
				legacyInterlining = l.LineKey
				legacyInterliningCount++
				continue
			}
			out.LineKeys = append(out.LineKeys, l.LineKey)
		}
		// A purpose-only interlining binding is a legacy read fallback. It remains live only when it
		// identifies one line; N same-purpose fusibles must never be merged or guessed between.
		if legacyInterliningCount == 1 {
			out.LineKeys = append(out.LineKeys, legacyInterlining)
		}
		return out
	}
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

// FabricScopeIdentity resolves a stored binding to the ТКАНЬ КАРТОЧКИ it addresses — the key every
// cross-table reader must bucket by, and a DIFFERENT question from the one FabricScopeKey answers.
//
// ДВА КЛЮЧА, КОТОРЫЕ ДОЛГО ВЫГЛЯДЕЛИ ОДНИМ, И ИМЕННО ЭТО СЛОМАЛО ЗАМЕР ПЛОЩАДЕЙ.
//
//   - FabricScopeKey — ВЕДРО УНИКАЛЬНОСТИ одной записи: то, что назвала сама запись, ничего не зная
//     о BOM. Его материализует scope_key (0267), по нему строится уникальный индекс, по нему стор
//     сшивает сохранённые строки с присланными. Оно НЕ ИМЕЕТ ПРАВА ДВИГАТЬСЯ от правки BOM — иначе
//     запись оказалась бы в другом ведре, чем то, в которое её положили.
//   - FabricScopeIdentity — ЛИЧНОСТЬ ТКАНИ: под каким ключом эту запись видит всё остальное. Она
//     обязана двигаться вместе с BOM, потому что деньги считаются от СТРОКИ: оценка по площади берёт
//     скоуп как EffectiveFabricScopeKey(строка) (dto.slotAreaEstimate), и клиент группирует ровно
//     так же (bom-purpose.ts scopeKeyOfBinding).
//
// ЖИВОЙ РАЗРЫВ, РАДИ КОТОРОГО ЭТО ЗАВЕДЕНО. Переход 0267 аддитивен: запись, привязанная к строке L,
// переехать на назначение не может (у L назначения тогда не было), и она НЕ ПЕРЕЕЗЖАЕТ сама потом —
// чтение отдаёт обе половины как есть, клиент их возвращает, и связь блока навсегда остаётся
// line-scoped. А строка L назначение получает. С этого момента ведро связи — L, а личность её ткани
// — «main», и доказательство полноты замера, сверявшее одно с другим, отвечало «блоки не привязаны»
// на карточке, где привязаны все девять.
//
// Правило то же самое, НЕ вторая его копия: разрешение делает ResolveFabricScope, а ключ считает
// FabricScopeKey — обе половины остаются в одной функции каждая. Ново здесь только «найди эту
// строку в BOM карточки», то есть поиск, а не правило. Тем же составом это уже собрано в
// MarkerFabricScope, которая для раскладки достаёт назначение НАЗВАННОЙ строки и разрешает уже его;
// здесь та же композиция названа именем, чтобы её звали, а не переписывали.
//
// НАЗНАЧЕНИЕ, КОТОРОГО НЕ НЕСЁТ НИ ОДНА СТРОКА, ОСТАЁТСЯ СОБОЙ. Клиентский scopeKeyOfBinding и
// печатный viewerGroups в этом случае отвечают «без привязки», и это верно для ЭКРАНА (панель
// показывает такой лист неприкреплённым). Здесь так делать нельзя: пустой ключ склеил бы все
// повисшие записи карточки в одно ведро, а хранимое ведро сдвинулось бы от очистки чужого поля —
// ровно то, что запрещает ResolveFabricScope. Читателю это ничем не грозит: у повисшего назначения
// нет строки BOM, значит нет и слота, который бы его спросил.
func FabricScopeIdentity(purpose, bomLineKey string, lines []RollGoodsLine) string {
	sc := ResolveFabricScope(purpose, bomLineKey, lines)
	if sc.ByPurpose || sc.Key == "" {
		// Пустой ключ — запись, не назвавшая НИЧЕГО (лист, залитый до 0260). Искать её среди строк
		// нельзя: uniq_bom_line_key до сих пор допускает несколько NULL, и строка ткани с пустым
		// line_key совпала бы с ней по равенству пустых строк, забрав неприкреплённый лист в чужое
		// назначение. Тот же guard, что в ResolveFabricScope, и по той же причине.
		return sc.Key
	}
	for _, l := range lines {
		if strings.EqualFold(strings.TrimSpace(l.LineKey), sc.Key) {
			// Личность СТРОКИ, посчитанная той же функцией, которой её считают читатели строки
			// (dto.slotAreaEstimate). Написание берётся со строки, а не с записи: ключи строк
			// заглавные на входе, но у записи мог остаться другой регистр, и два ведра под одну
			// ткань — это тот же разрыв, только тише.
			return EffectiveFabricScopeKey(l)
		}
	}
	// Строки нет вовсе (удалена или переклассифицирована из рулонных) — ключ остаётся тем, что
	// назвала запись. «Слот удалён» — состояние интерфейса, а не повод переименовать ведро.
	return sc.Key
}

// PurposesOfRollGoods lists the назначения actually present on a card's cloth lines, deduplicated,
// in the order the lines appear. This is what a binding may legally name — offering the full
// eight-value vocabulary would let an operator bind a sheet to a purpose the card does not use.
func PurposesOfRollGoods(lines []RollGoodsLine) []string {
	seen := make(map[string]bool, len(lines))
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l.Section == BomSectionInterlining {
			continue
		}
		p := strings.TrimSpace(l.Purpose)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}
