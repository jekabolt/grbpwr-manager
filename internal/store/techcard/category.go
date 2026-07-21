package techcard

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// categoryAncestorsQuery reads the picked category node and its ancestors, most-specific first, by
// walking category.parent_id upward. The depth bound is a cycle guard, not a shape assumption: the
// tree is three levels, so 5 is slack while still terminating if parent_id is ever made circular.
const categoryAncestorsQuery = `
	WITH RECURSIVE anc AS (
		SELECT id, level_id, parent_id, 0 AS depth FROM category WHERE id = :category_id
		UNION ALL
		SELECT c.id, c.level_id, c.parent_id, a.depth + 1
		FROM category c JOIN anc a ON c.id = a.parent_id
		WHERE a.depth < 5
	)
	SELECT id, level_id FROM anc ORDER BY depth`

// syncStyleCategoryTriple derives a style's top/sub/type category triple from the single category_id
// the tech-card UI writes, and stores it on the row. category_id is the source of truth for a
// style's taxonomy; the triple is its normalized projection, which size-system resolution
// (entity.ResolveSizeSystemPolicy), storefront filters and category metrics all read.
//
// The tree is read inside the caller's transaction rather than from the dictionary cache: a cache
// miss would derive an empty triple SILENTLY, which is exactly the "category disappeared" failure
// this whole change exists to fix. Reading it here also sees the same snapshot as the write.
//
// THE CLOBBER RULE: when category_id is unset this is a no-op — the triple is left exactly as it
// was, never cleared. Every style created before the derivation existed has a triple backfilled
// from its products (0139) and category_id NULL, so clearing on unset would destroy correct
// category data on the FIRST tech-card save of every such style. The wire cannot tell "field
// omitted" from "field explicitly cleared" anyway (the proto field is a bare int32 documented as
// `0 = unset`, and the client renders it FROM the tech card), so the conservative reading is the
// only safe one. The way to change a style's category is to pick a different one, not to clear it.
//
// "Unset" here means the sql.NullInt32 is invalid. entity.TechCardInsert.CategoryId is nullable; the
// wire's 0 becomes Valid:false at the dto boundary (nullInt32FromPb, internal/dto/model.go). The
// Int32 <= 0 arm below is defence in depth for a store-level caller that builds the entity directly
// and sets Valid:true with a zero or negative id, which no category row can match.
//
// When category_id IS set, all three columns are written from the derivation — including NULLing
// sub/type for a top-only pick. That is not data loss, it is the edit: narrowing a style from
// {tops, tshirts, crop} to just {tops} must drop the levels the owner removed.
func syncStyleCategoryTriple(ctx context.Context, db dependency.DB, id int, categoryID sql.NullInt32) error {
	if !categoryID.Valid || categoryID.Int32 <= 0 {
		return nil
	}
	chain, err := storeutil.QueryListNamed[entity.CategoryNode](ctx, db, categoryAncestorsQuery,
		map[string]any{"category_id": categoryID.Int32})
	if err != nil {
		return fmt.Errorf("load ancestors of category %d: %w", categoryID.Int32, err)
	}
	path, ok := entity.DeriveStyleCategoryPath(chain)
	if !ok {
		// The category exists (the header write's FK already proved that) but its chain reaches no
		// top-level node, so the tree itself is broken. Refuse rather than write a headless path: a
		// NULL top_category_id reads downstream as "no category assigned" and turns size validation
		// off entirely, hiding the breakage instead of surfacing it.
		return entity.NewFieldViolation("category_id", "category_tree_has_no_top_category", "",
			"this category is not attached to a top-level category; pick another category or fix the category tree")
	}
	return storeutil.ExecNamed(ctx, db, `
		UPDATE tech_card SET
			top_category_id = :top_category_id,
			sub_category_id = :sub_category_id,
			type_id = :type_id
		WHERE id = :id`,
		map[string]any{
			"id":              id,
			"top_category_id": path.TopCategoryID,
			"sub_category_id": path.SubCategoryID,
			"type_id":         path.TypeID,
		})
}
