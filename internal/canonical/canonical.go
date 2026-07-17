// Package canonical selects the canonical translation of a multilingual entity deterministically, so
// derived public artifacts (pretty URLs, SKU-facing names, cache keys) are stable regardless of the
// order the translations arrive in or which languages happen to exist.
package canonical

import "github.com/jekabolt/grbpwr-manager/internal/entity"

// IsDefaultFunc returns a predicate reporting whether a language id is a default language, built from
// the supplied language set (typically cache.GetLanguages()). When the set is empty or has no default,
// the predicate is always false and Select falls back to the smallest language id.
func IsDefaultFunc(langs []entity.Language) func(int) bool {
	def := make(map[int]bool, len(langs))
	for _, l := range langs {
		if l.IsDefault {
			def[l.Id] = true
		}
	}
	return func(id int) bool { return def[id] }
}

// Select returns the canonical element of items (and true), or the zero value (and false) when items
// is empty. The canonical element is:
//   - the element whose language is default; if several are (erroneously) marked default, the one with
//     the smallest language id among them;
//   - otherwise the element with the smallest language id overall.
//
// The result never depends on the order of items — the same set of translations always yields the same
// canonical choice.
func Select[T any](items []T, langID func(T) int, isDefault func(int) bool) (T, bool) {
	var zero T
	bestDefault, bestAny := -1, -1
	for i := range items {
		id := langID(items[i])
		if bestAny == -1 || id < langID(items[bestAny]) {
			bestAny = i
		}
		if isDefault != nil && isDefault(id) {
			if bestDefault == -1 || id < langID(items[bestDefault]) {
				bestDefault = i
			}
		}
	}
	if bestDefault != -1 {
		return items[bestDefault], true
	}
	if bestAny != -1 {
		return items[bestAny], true
	}
	return zero, false
}

// ProductName applies the shared canonical-language policy to product translations.
func ProductName(items []entity.ColorwayTranslationInsert, langs []entity.Language) (string, bool) {
	tr, ok := Select(items,
		func(t entity.ColorwayTranslationInsert) int { return t.LanguageId },
		IsDefaultFunc(langs))
	if !ok {
		return "", false
	}
	return tr.Name, true
}

// ArchiveHeading applies the same policy to archive translations.
func ArchiveHeading(items []entity.ArchiveTranslation, langs []entity.Language) (string, bool) {
	tr, ok := Select(items,
		func(t entity.ArchiveTranslation) int { return t.LanguageId },
		IsDefaultFunc(langs))
	if !ok {
		return "", false
	}
	return tr.Heading, true
}
