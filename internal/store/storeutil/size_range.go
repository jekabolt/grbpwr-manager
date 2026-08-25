package storeutil

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TechCardSizeRange is a style's declared size range (tech_card_size), loaded once so a write that
// references many sizes checks them all against a single query.
//
// Every size-scoped child of a style — chart cells, grade base, assembly lines, per-size consumption,
// fitting sizes — hangs off size(id), the GLOBAL dictionary, not off tech_card_size. The FK therefore
// only proves the size exists somewhere in the world, never that this style makes it, which is how an
// XL cell lands on an XS/S style: a phantom column in the grid, an assembly line no one can attach,
// a consumption norm no run will ever apply.
//
// An UNDECLARED range (no tech_card_size rows) accepts any size. A style is routinely authored before
// its grid is picked, and refusing sizes then would block early work; this is the same rule the sample
// writer already uses (store/sample: `grid.Total > 0 && grid.Match == 0`).
type TechCardSizeRange struct {
	styleID int
	ids     map[int]bool
}

// LoadTechCardSizeRange reads a style's declared size range on the given db (tx or pool). Call it
// inside the write transaction so a concurrent size-range change cannot slip between check and write.
func LoadTechCardSizeRange(ctx context.Context, db dependency.DB, styleID int) (TechCardSizeRange, error) {
	rows, err := QueryListNamed[struct {
		SizeID int `db:"size_id"`
	}](ctx, db, `SELECT size_id FROM tech_card_size WHERE tech_card_id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return TechCardSizeRange{}, fmt.Errorf("load style %d size range: %w", styleID, err)
	}
	ids := make(map[int]bool, len(rows))
	for _, r := range rows {
		ids[r.SizeID] = true
	}
	return TechCardSizeRange{styleID: styleID, ids: ids}, nil
}

// NewTechCardSizeRange builds a range from ids already in hand, without a statement.
//
// It exists because the DECISIONS a range drives — which chart cells a card can hold, which assembly
// lines it can attach — are worth testing on their own, and the loader above is the only other way
// to obtain one, which puts a MySQL behind every such test. A non-positive id is dropped rather than
// stored: 0 means «not size-scoped» everywhere in this contract, and a range that contained it would
// answer Has(0) with true for the wrong reason.
func NewTechCardSizeRange(styleID int, sizeIDs ...int) TechCardSizeRange {
	ids := make(map[int]bool, len(sizeIDs))
	for _, id := range sizeIDs {
		if id > 0 {
			ids[id] = true
		}
	}
	return TechCardSizeRange{styleID: styleID, ids: ids}
}

// Declared reports whether the style has picked a size range at all.
func (r TechCardSizeRange) Declared() bool { return len(r.ids) > 0 }

// Has reports membership. An undeclared range contains everything (see the type comment).
func (r TechCardSizeRange) Has(sizeID int) bool { return !r.Declared() || r.ids[sizeID] }

// Require returns a field-tagged violation when sizeID falls outside a DECLARED range, and nil
// otherwise. A non-positive id means "not size-scoped" (an all-sizes assembly line, an unset
// optional) and is always accepted — presence is the caller's rule, not this one's.
func (r TechCardSizeRange) Require(field string, sizeID int) error {
	if sizeID <= 0 || r.Has(sizeID) {
		return nil
	}
	return entity.NewFieldViolation(field, "size_not_in_style_range",
		fmt.Sprintf("size %d", sizeID),
		fmt.Sprintf("add the size to style %d's size range first, or pick one of the sizes it already makes", r.styleID))
}
