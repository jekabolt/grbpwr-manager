package entity

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DictionaryNamespace identifies a controlled dictionary for cache-revision tracking. Each namespace
// has one row in dictionary_revision; a mutation bumps its revision in the same transaction so every
// instance can detect a stale dictionary cache before a dictionary-dependent write (R9).
type DictionaryNamespace string

const (
	DictNamespaceColor       DictionaryNamespace = "color"
	DictNamespaceCollection  DictionaryNamespace = "collection"
	DictNamespaceTag         DictionaryNamespace = "tag"
	DictNamespaceCountry     DictionaryNamespace = "country"
	DictNamespaceSize        DictionaryNamespace = "size"
	DictNamespaceMeasurement DictionaryNamespace = "measurement"
	// DictNamespaceCategorySizeSystem covers the category -> size-system mapping (S10/WS5, migration
	// 0175). Seeded once with revision=1; no CRUD path bumps it yet (the table is migration-seeded,
	// not admin-editable in WS5) -- the namespace row exists so a future editable mapping does not
	// need a follow-up migration to register it.
	DictNamespaceCategorySizeSystem DictionaryNamespace = "category_size_system"
	// DictNamespaceFiber covers the controlled fibre vocabulary (S17/P0.4): the dictionary that
	// material_composition / bom_item_composition / style_composition reference by code (FK RESTRICT).
	// Its dictionary_revision row is seeded by migration 0180 so CreateFiber/ArchiveFiber can bump it.
	DictNamespaceFiber DictionaryNamespace = "fiber"
)

// CollectionDict is a controlled collection dictionary entry (R9). Code is a stable unique slug; an
// in-use code is archived, never renamed or deleted. tech_card.collection_id references it.
// It is distinct from entity.Collection, which is the storefront projection (name + gender counts).
type CollectionDict struct {
	ID           int            `db:"id"`
	Code         string         `db:"code"`
	Name         string         `db:"name"`
	Translations sql.NullString `db:"translations"`
	ArchivedAt   sql.NullTime   `db:"archived_at"`
}

// TagDict is a controlled tag dictionary entry (R9). product_tag.tag_id references it.
type TagDict struct {
	ID           int            `db:"id"`
	Code         string         `db:"code"`
	Name         string         `db:"name"`
	Translations sql.NullString `db:"translations"`
	ArchivedAt   sql.NullTime   `db:"archived_at"`
}

// Fiber is a controlled fibre-vocabulary entry (R9/S17): the dictionary that material_composition,
// bom_item_composition and style_composition reference by Code (FK RESTRICT). Code is the stable,
// upper-cased key; Name is display data; an in-use fibre is archived (archived_at set), never deleted.
type Fiber struct {
	Code       string       `db:"code"`
	Name       string       `db:"name"`
	ArchivedAt sql.NullTime `db:"archived_at"`
}

// Country is a controlled ISO 3166-1 alpha-2 dictionary entry (R9). The dictionary is CLOSED: the admin
// API may only toggle Active; arbitrary creation is forbidden. product.country_code references Code.
type Country struct {
	Code         string         `db:"code"`
	DisplayName  string         `db:"display_name"`
	Translations sql.NullString `db:"translations"`
	Active       bool           `db:"active"`
}

// DictionaryRevision is the current cache-invalidation counter for one namespace.
type DictionaryRevision struct {
	Namespace string `db:"namespace"`
	Revision  int64  `db:"revision"`
}

// ErrDictionaryVersionConflict is returned when a mutation's expected_version does not match the
// current dictionary_revision (optimistic concurrency, R9).
var ErrDictionaryVersionConflict = errors.New("dictionary revision conflict")

// ErrDictionaryCodeInUse is returned when a rename or delete is attempted on a dictionary code that is
// referenced by catalog rows. R9 requires archive-not-delete for in-use codes.
var ErrDictionaryCodeInUse = errors.New("dictionary code is in use: archive instead of rename/delete")

var colorCodeRe = regexp.MustCompile(`^[A-Z0-9]{3}$`)

// NormalizeColorCode upper-cases and trims a colour code so validation and storage compare canonically.
func NormalizeColorCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// ValidateColorCode enforces the immutable colour-code contract (R9): exactly three characters drawn
// from [A-Z0-9], upper-case. Pass the already-normalised value (see NormalizeColorCode).
func ValidateColorCode(code string) error {
	if !colorCodeRe.MatchString(code) {
		return fmt.Errorf("invalid colour code %q: must match [A-Z0-9]{3}", code)
	}
	return nil
}

var fiberCodeRe = regexp.MustCompile(`^[A-Z0-9]{1,8}$`)

// ValidateFiberCode enforces the fibre-code contract (S17): 1-8 upper-case alphanumerics, matching
// the fiber.code VARCHAR(8) key. Pass the already-normalised value (ToUpper+TrimSpace).
func ValidateFiberCode(code string) error {
	if !fiberCodeRe.MatchString(code) {
		return fmt.Errorf("invalid fibre code %q: must be 1-8 characters from [A-Z0-9]", code)
	}
	return nil
}

var dictNonAlnumRe = regexp.MustCompile(`[^0-9A-Za-z]+`)

// NormalizeDictSlug derives a stable dictionary code from a free-text name: runs of non-alphanumerics
// collapse to '_', capped at 64 chars, upper-cased, surrounding '_' trimmed. The operation order is
// byte-compatible with the 0151 migration backfill slug, so a code created by the API matches the code
// the migration derived for the same legacy value (they resolve to the same dictionary row).
func NormalizeDictSlug(name string) string {
	s := dictNonAlnumRe.ReplaceAllString(strings.TrimSpace(name), "_")
	if len(s) > 64 {
		s = s[:64]
	}
	s = strings.ToUpper(s)
	return strings.Trim(s, "_")
}

// CheckExpectedRevision implements the optimistic-concurrency check for dictionary mutations. An
// expected value of 0 opts out (unconditional write); any other value must equal the current revision
// or the caller must reject the mutation with ErrDictionaryVersionConflict.
func CheckExpectedRevision(expected, current int64) error {
	if expected != 0 && expected != current {
		return fmt.Errorf("%w: expected %d, current %d", ErrDictionaryVersionConflict, expected, current)
	}
	return nil
}
