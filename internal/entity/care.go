package entity

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// careProfessionalCategory is the one care category that holds TWO picks (one dry-clean, one
// wet-clean) instead of one. Everything else is exactly one symbol per category, so the uniqueness
// rule is keyed on category + sub-category rather than category alone.
const careProfessionalCategory = "Professional Care"

// CareSymbol is one entry of the controlled ISO 3758 care vocabulary (care_symbol) — the dictionary
// that a style's stored care codes resolve against, exactly as `fiber` backs the composition model.
// Code is the stable key; it is what is stored on the style, what the label generator consumes and
// what prints on the sewn tag. An in-use symbol is archived (archived_at set), never deleted.
type CareSymbol struct {
	Code        string         `db:"code"`
	Category    string         `db:"category"`
	SubCategory sql.NullString `db:"sub_category"`
	Name        string         `db:"name"`
	ShortProse  string         `db:"short_prose"`
	SortOrder   int            `db:"sort_order"`
	ArchivedAt  sql.NullTime   `db:"archived_at"`
	// Translations is the customer-facing wording per language id. Only short_prose is translated;
	// a missing language falls back to the English columns above.
	Translations map[int]CareTranslation
}

// CareTranslation is one language's wording for a care symbol. Name is optional — the admin picker
// is English-only, so most rows carry prose alone and fall back to CareSymbol.Name.
type CareTranslation struct {
	Name       string
	ShortProse string
}

// CareEntry is one resolved care symbol on the wire — the TYPED projection of the stored code
// string, and the direct analogue of CompositionEntry. Same contract: care_instructions (the raw
// comma-joined column) is never overloaded with it; a client renders CareEntries when present and
// falls back to the plain string otherwise, which is what keeps pre-ISO free-text rows readable.
type CareEntry struct {
	Code        string
	Category    string
	SubCategory string
	Name        string
	ShortProse  string
}

// CareIndex is the dictionary in the shape the read and write paths actually need it: keyed by
// code, so resolving a style's care costs no query. Build it once per dictionary load
// (BuildCareIndex) and hand it around; the zero value resolves nothing and rejects everything,
// which is the correct behaviour before the dictionary has loaded.
type CareIndex struct {
	byCode map[string]CareSymbol
}

// BuildCareIndex indexes a care dictionary by code. Archived symbols are included: a style that
// already references one must keep rendering it, and archiving only means "do not offer this in the
// picker any more". Rejecting a newly-typed archived code is the write path's job (Normalize).
func BuildCareIndex(symbols []CareSymbol) CareIndex {
	byCode := make(map[string]CareSymbol, len(symbols))
	for _, s := range symbols {
		byCode[s.Code] = s
	}
	return CareIndex{byCode: byCode}
}

// Len reports how many symbols the index holds. Callers use it to tell "dictionary not loaded" from
// "dictionary loaded and this code is genuinely unknown".
func (ix CareIndex) Len() int { return len(ix.byCode) }

// SplitCareCodes splits a stored care value into its codes. Empty, whitespace-only and free text
// alike simply yield whatever comma-separated tokens are there — parsing is separate from deciding
// whether the tokens mean anything.
func SplitCareCodes(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// careSlotKey is the uniqueness key for a pick: Professional Care is scoped to its sub-category
// (a garment may carry one dry-clean AND one wet-clean symbol), everything else to the category.
func careSlotKey(s CareSymbol) string {
	if s.Category == careProfessionalCategory && s.SubCategory.Valid {
		return s.Category + "-" + s.SubCategory.String
	}
	return s.Category
}

// Normalize validates a care value on the WRITE path and returns it canonicalised.
//
// It rejects what a picker cannot produce: an unknown code, an archived one, the same code twice,
// or two symbols competing for the same slot (two wash temperatures). On success the codes come
// back joined in dictionary order, so the same selection always stores the same string regardless
// of the order it was clicked in — which is what makes the column comparable and the printed label
// stable.
//
// An EMPTY value is valid and means "no care specified"; it returns "" with no error.
//
// Legacy free text is deliberately NOT accepted here. Reads tolerate it (Resolve just returns no
// entries), but the moment someone edits care they have to pick real symbols — otherwise the column
// never converges and the storefront can never rely on it. Because care is only written when the
// field mask names it, this cannot block a save that was not touching care in the first place.
func (ix CareIndex) Normalize(raw string) (string, error) {
	codes := SplitCareCodes(raw)
	if len(codes) == 0 {
		return "", nil
	}
	if ix.Len() == 0 {
		return "", NewFieldViolation("care_instructions", "care dictionary is unavailable", "",
			"retry once the dictionary has loaded")
	}

	picked := make([]CareSymbol, 0, len(codes))
	seenCode := make(map[string]bool, len(codes))
	seenSlot := make(map[string]string, len(codes))
	for _, code := range codes {
		up := strings.ToUpper(code)
		sym, ok := ix.byCode[up]
		if !ok {
			return "", NewFieldViolation("care_instructions", "unknown_care_code", code,
				"pick symbols from the care dictionary; free-text care is no longer accepted")
		}
		if sym.ArchivedAt.Valid {
			return "", NewFieldViolation("care_instructions", "archived_care_code", code,
				"this symbol has been retired; pick a current one")
		}
		if seenCode[up] {
			return "", NewFieldViolation("care_instructions", "duplicate_care_code", code,
				"list each symbol at most once")
		}
		seenCode[up] = true
		slot := careSlotKey(sym)
		if other, clash := seenSlot[slot]; clash {
			return "", NewFieldViolation("care_instructions", "conflicting_care_codes",
				fmt.Sprintf("%s and %s", other, up),
				fmt.Sprintf("%s allows one symbol; drop one of them", strings.ToLower(slot)))
		}
		seenSlot[slot] = up
		picked = append(picked, sym)
	}

	sort.Slice(picked, func(i, j int) bool { return picked[i].SortOrder < picked[j].SortOrder })
	out := make([]string, len(picked))
	for i, s := range picked {
		out[i] = s.Code
	}
	return strings.Join(out, ","), nil
}

// Resolve projects a stored care value into typed entries for the READ path, in dictionary order.
//
// It is deliberately tolerant where Normalize is strict: a code the dictionary does not know is
// skipped rather than raising, because rows written before the vocabulary existed hold prose
// ("Machine wash cold at 30, do not tumble dry") and a read must never fail on historical data. A
// value that resolves to nothing returns nil, which is the signal for a client to fall back to
// rendering the raw string.
//
// languageID selects the customer-facing wording; 0 (or a language with no row) keeps the English
// columns. A translation supplies prose only unless it also carries a name.
func (ix CareIndex) Resolve(raw string, languageID int) []CareEntry {
	codes := SplitCareCodes(raw)
	if len(codes) == 0 || ix.Len() == 0 {
		return nil
	}
	picked := make([]CareSymbol, 0, len(codes))
	seen := make(map[string]bool, len(codes))
	for _, code := range codes {
		up := strings.ToUpper(strings.TrimSpace(code))
		sym, ok := ix.byCode[up]
		if !ok || seen[up] {
			continue
		}
		seen[up] = true
		picked = append(picked, sym)
	}
	if len(picked) == 0 {
		return nil
	}
	sort.Slice(picked, func(i, j int) bool { return picked[i].SortOrder < picked[j].SortOrder })

	out := make([]CareEntry, 0, len(picked))
	for _, s := range picked {
		e := CareEntry{
			Code:        s.Code,
			Category:    s.Category,
			SubCategory: s.SubCategory.String,
			Name:        s.Name,
			ShortProse:  s.ShortProse,
		}
		if t, ok := s.Translations[languageID]; ok && languageID != 0 {
			if t.ShortProse != "" {
				e.ShortProse = t.ShortProse
			}
			if t.Name != "" {
				e.Name = t.Name
			}
		}
		out = append(out, e)
	}
	return out
}

// CareProse renders resolved entries as the sentence a customer reads:
// "machine wash 30°, do not bleach, do not tumble dry, iron low". Kept next to Resolve so the
// storefront and any server-side label render the same words from the same order.
func CareProse(entries []CareEntry) string {
	if len(entries) == 0 {
		return ""
	}
	parts := make([]string, len(entries))
	for i, e := range entries {
		parts[i] = e.ShortProse
	}
	return strings.Join(parts, ", ")
}
