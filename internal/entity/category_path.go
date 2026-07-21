package entity

import "database/sql"

// Category tree levels, matching the category_level seed order in 0001_initial_setup.sql
// (top_category, sub_category, type). The tree is seed-only -- there is no CRUD RPC that adds a
// level -- so these ids are stable.
const (
	CategoryLevelTop  = 1
	CategoryLevelSub  = 2
	CategoryLevelType = 3
)

// CategoryNode is one node of the category tree as read for derivation: just the identity and the
// level, which is all DeriveStyleCategoryPath needs to classify it.
type CategoryNode struct {
	ID      int `db:"id"`
	LevelID int `db:"level_id"`
}

// DeriveStyleCategoryPath turns the ancestor chain of the single category_id a tech card carries
// (the picked node first, then its parents walking up category.parent_id) into the top/sub/type
// triple the style taxonomy needs.
//
// Classification is by level_id, NEVER by position in the chain. That distinction is load-bearing
// because the tree is not uniformly three deep: `dresses` (0001_initial_setup.sql, "Dresses types
// (no sub-category)") hangs its level-3 types DIRECTLY off the level-1 top category. Picking the
// dress type `mini` must therefore derive {top: dresses, sub: NULL, type: mini} -- a positional
// walk would wrongly produce {top: dresses, sub: mini}, filing a dress under a sub-category that
// does not exist and breaking size-system resolution and storefront filters for the whole category.
//
// ok is false when the chain contains no top-level node at all (an orphaned or cyclic parent_id).
// Callers must treat that as a hard error rather than writing the partial path: a NULL
// top_category_id reads downstream as "no category assigned", which ResolveSizeSystemPolicy answers
// with Unrestricted -- silently disabling validation instead of reporting the broken tree.
func DeriveStyleCategoryPath(chain []CategoryNode) (StyleCategoryPath, bool) {
	var path StyleCategoryPath
	for _, n := range chain {
		// First writer per level wins: the chain is ordered most-specific first, so a malformed tree
		// with two nodes at the same level resolves to the one nearest the picked node.
		switch n.LevelID {
		case CategoryLevelTop:
			if !path.TopCategoryID.Valid {
				path.TopCategoryID = sql.NullInt32{Int32: int32(n.ID), Valid: true}
			}
		case CategoryLevelSub:
			if !path.SubCategoryID.Valid {
				path.SubCategoryID = sql.NullInt32{Int32: int32(n.ID), Valid: true}
			}
		case CategoryLevelType:
			if !path.TypeID.Valid {
				path.TypeID = sql.NullInt32{Int32: int32(n.ID), Valid: true}
			}
		}
	}
	if !path.TopCategoryID.Valid {
		return StyleCategoryPath{}, false
	}
	return path, true
}
