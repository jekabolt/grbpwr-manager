package entity

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// CategorySizeSystem is one row of the category -> size-system mapping table (S10/WS5,
// migration 0175): which SizeSKUSystem values are permitted for a category-tree node. A row targets
// EITHER a broad category node (CategoryID set) OR a specific leaf type (TypeID set) -- never both
// unset (mirrors the DB's chk_category_size_system_target). See ResolveSizeSystemPolicy for how a
// style's top/sub/type category triple resolves against a slice of these rows.
type CategorySizeSystem struct {
	ID         int           `db:"id"`
	CategoryID sql.NullInt32 `db:"category_id"`
	TypeID     sql.NullInt32 `db:"type_id"`
	SkuSystem  SizeSKUSystem `db:"size_system"`
}

// StyleCategoryPath is the category triple a style (tech_card) carries: top/sub/type. All three are
// nullable in the DB and a style genuinely can have no category yet (it is not required to create
// one). The triple is normally DERIVED from the single tech_card.category_id the tech-card UI
// writes, by classifying that node and its ancestors by level (DeriveStyleCategoryPath); UpdateStyle
// writes it directly on the product/legacy route and derives category_id back from it, so either
// writer leaves the row self-consistent. A NULL sub with a SET type is a legitimate shape, not a
// bug: `dresses` hangs its types directly off the top category with no sub-category level.
type StyleCategoryPath struct {
	TopCategoryID sql.NullInt32 `db:"top_category_id"`
	SubCategoryID sql.NullInt32 `db:"sub_category_id"`
	TypeID        sql.NullInt32 `db:"type_id"`
}

// MostSpecificID returns the deepest category node the path sets (type > sub-category > top-category)
// for display purposes (e.g. naming "the category" in a validation error); ok is false when the style
// has no category assigned at all.
func (p StyleCategoryPath) MostSpecificID() (id int, ok bool) {
	switch {
	case p.TypeID.Valid:
		return int(p.TypeID.Int32), true
	case p.SubCategoryID.Valid:
		return int(p.SubCategoryID.Int32), true
	case p.TopCategoryID.Valid:
		return int(p.TopCategoryID.Int32), true
	default:
		return 0, false
	}
}

// SizeSystemPolicy is the resolved outcome of ResolveSizeSystemPolicy: what a style's size writes are
// permitted to use. Exactly one of Unrestricted, OSFallback, or a non-empty Systems applies.
type SizeSystemPolicy struct {
	// Unrestricted is true when the style has no category assigned yet (TopCategoryID NULL): there is
	// nothing to validate against, so every size is allowed.
	Unrestricted bool
	// OSFallback is true when the style HAS a category, but that category (at every level of its
	// path) matches zero category_size_system rows -- a "category without a grid" (e.g. bags,
	// objects), which is restricted to the single one-size 'os' entry rather than left unrestricted.
	OSFallback bool
	// Systems is the set of permitted SizeSKUSystem values, populated when neither of the above holds.
	Systems map[SizeSKUSystem]bool
}

// Allows reports whether sz may be used given the policy.
func (p SizeSystemPolicy) Allows(sz Size) bool {
	switch {
	case p.Unrestricted:
		return true
	case p.OSFallback:
		return strings.EqualFold(sz.Name, "os")
	default:
		return p.Systems[sz.SkuSystem]
	}
}

// AllowedLabel renders the permitted set for a human-readable error message.
func (p SizeSystemPolicy) AllowedLabel() string {
	if p.OSFallback {
		return "os (one-size)"
	}
	names := make([]string, 0, len(p.Systems))
	for s := range p.Systems {
		names = append(names, string(s))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// ResolveSizeSystemPolicy determines which SizeSKUSystem values a style's size writes may use, given
// its category path and the full category_size_system table (S10/WS5). Resolution walks
// most-specific-first: a rule keyed on the style's type_id wins outright over rules keyed on its
// sub/top category; only when no type-level rule matches does a sub_category_id rule apply, then
// top_category_id. See the fallback rules on CategorySizeSystem/SizeSystemPolicy for the two
// degenerate cases (no category yet vs. a category with an intentionally empty grid).
func ResolveSizeSystemPolicy(path StyleCategoryPath, rules []CategorySizeSystem) SizeSystemPolicy {
	if !path.TopCategoryID.Valid {
		return SizeSystemPolicy{Unrestricted: true}
	}
	if path.TypeID.Valid {
		if systems := matchingSizeSystems(rules, func(r CategorySizeSystem) bool {
			return r.TypeID.Valid && r.TypeID.Int32 == path.TypeID.Int32
		}); len(systems) > 0 {
			return SizeSystemPolicy{Systems: systems}
		}
	}
	if path.SubCategoryID.Valid {
		if systems := matchingSizeSystems(rules, func(r CategorySizeSystem) bool {
			return r.CategoryID.Valid && r.CategoryID.Int32 == path.SubCategoryID.Int32
		}); len(systems) > 0 {
			return SizeSystemPolicy{Systems: systems}
		}
	}
	if systems := matchingSizeSystems(rules, func(r CategorySizeSystem) bool {
		return r.CategoryID.Valid && r.CategoryID.Int32 == path.TopCategoryID.Int32
	}); len(systems) > 0 {
		return SizeSystemPolicy{Systems: systems}
	}
	// A category is set, but nothing on its path is mapped: OS-fallback, not "anything goes".
	return SizeSystemPolicy{OSFallback: true}
}

func matchingSizeSystems(rules []CategorySizeSystem, match func(CategorySizeSystem) bool) map[SizeSKUSystem]bool {
	var out map[SizeSKUSystem]bool
	for _, r := range rules {
		if !match(r) {
			continue
		}
		if out == nil {
			out = make(map[SizeSKUSystem]bool)
		}
		out[r.SkuSystem] = true
	}
	return out
}

// ValidateSizeAgainstCategory checks one size against the size-system policy resolved from a style's
// category path (S10/WS5). It returns a field-tagged *ValidationError naming the offending size
// system, the category and the allowed set when sz is not permitted, or nil otherwise (including the
// Unrestricted case: a style with no category yet has nothing to validate against). categoryLabel is
// a human-readable name for the style's most-specific set category (e.g. "blazer" or "shoes"); pass
// "" when unknown -- the message falls back to "this style".
func ValidateSizeAgainstCategory(field string, path StyleCategoryPath, categoryLabel string, rules []CategorySizeSystem, sz Size) error {
	policy := ResolveSizeSystemPolicy(path, rules)
	if policy.Allows(sz) {
		return nil
	}
	label := categoryLabel
	if label == "" {
		label = "this style"
	}
	return NewFieldViolation(field,
		fmt.Sprintf("size system %s not allowed for category %s, allowed: %s", sz.SkuSystem, label, policy.AllowedLabel()),
		"", "choose a size from an allowed system for this category")
}
