package product

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// styleFieldFragments maps a normalized style-fact field name to its SQL SET assignment, keyed to the
// same bind names as stylePatchParams. The order here is the canonical write order (matches
// styleFieldsSet) so a masked UPDATE is deterministic. Used to honor UpdateStyle's field mask: only the
// requested fields are written, the rest keep their stored value — so a partial editor (the tech card's
// fit/composition/care, the colourway card's model-wears) never clobbers a fact it doesn't own.
var styleFieldFragments = []struct{ key, frag string }{
	{"brand", "brand = :brand"},
	{"season", "season_code = :seasonCode, season_year = COALESCE(season_year, YEAR(CURRENT_DATE)), season = CONCAT(:seasonCode, LPAD(MOD(COALESCE(season_year, YEAR(CURRENT_DATE)), 100), 2, '0'))"},
	{"collection", "collection = :collection"},
	{"targetgender", "target_gender = :targetGender"},
	{"fit", "fit = :fit"},
	{"composition", "composition = JSON_QUOTE(:composition)"},
	{"careinstructions", "care_instructions = :careInstructions"},
	{"modelwearsheightcm", "model_wears_height_cm = :modelWearsHeightCm"},
	{"modelwearssizeid", "model_wears_size_id = :modelWearsSizeId"},
	{"topcategoryid", "top_category_id = :topCategoryId"},
	{"subcategoryid", "sub_category_id = :subCategoryId"},
	{"typeid", "type_id = :typeId"},
}

// styleCategoryIDFragment derives tech_card.category_id back from the top/sub/type triple whenever a
// writer touches any level of that triple, taking the most specific level supplied.
//
// THE INVARIANT: a style's taxonomy has two representations on the row — the single category_id the
// tech-card UI edits, and the top/sub/type triple that size-system resolution, storefront filters
// and metrics read. EVERY writer of either representation derives the other, so whichever ran last
// leaves the row self-consistent and the two cannot drift apart:
//   - Add/UpdateTechCard write category_id and DERIVE the triple from it (syncStyleCategoryTriple).
//   - UpdateStyle writes the triple and derives category_id from it — masked paths append this
//     fragment in styleSetColumns, the unmasked path gets it via styleFieldsSet.
//   - writeStyleFields (colourway create) and updateProduct (colourway edit) write the triple and
//     get the derivation from styleFieldsSet.
//
// All three must have it. A colourway saved from a stale form re-writes the OLD triple; if that path
// alone skipped the derivation, category_id would keep the newer tech-card value and the row would
// be permanently inconsistent with nothing able to notice.
//
// The trailing `category_id` in the COALESCE is the same "never un-set a category" rule the
// tech-card side applies: if every level of the incoming triple is unset, keep the stored
// category_id rather than blanking it. validateStyleCategoryMask guarantees this fragment only ever
// runs with ALL THREE levels being written, so the binds and the columns agree by construction and
// the fallback is reached only for a wholly empty triple — which top_category_id's FK refuses
// anyway. It is kept as a belt-and-braces guard, so the expression is correct on its own terms
// rather than only in combination with a constraint declared somewhere else.
//
// Bind types make the NULLIFs work (verified against stylePatchParams): topCategoryId is a plain int
// that is 0 when unset, so NULLIF(0, 0) is NULL; subCategoryId and typeId are sql.NullInt32 that
// bind SQL NULL when unset, and NULLIF(NULL, 0) is likewise NULL.
const styleCategoryIDFragment = `category_id = COALESCE(NULLIF(:typeId, 0), NULLIF(:subCategoryId, 0), NULLIF(:topCategoryId, 0), category_id)`

// styleCategoryMaskKeys are the three normalized mask keys that together name ONE path through the
// category tree. They are not independent facts and may only be masked as a set — see
// validateStyleCategoryMask.
var styleCategoryMaskKeys = [...]string{"topcategoryid", "subcategoryid", "typeid"}

// validateStyleCategoryMask rejects a field mask naming SOME but not ALL of the category levels.
//
// The triple is a path through a tree, constrained by parent(type) = sub and parent(sub) = top — not
// three independent columns. A partial mask lets a caller write a path that violates those
// constraints, and NO derivation can repair it, because the levels that would have to change are
// exactly the ones the mask excludes from the write. Concretely: a style at
// {top tops, sub tshirts, type crop} re-pointed to sub `shirts` under a mask naming only
// subCategoryId leaves type `crop` in place — `crop` is a child of `tshirts`, not of `shirts`, so the
// row now describes a path that does not exist in the tree. Whatever category_id we then derive is
// wrong in one direction or the other, and the next UpdateTechCard re-derives the triple from it and
// silently reverts the edit.
//
// So this is refused loudly instead. Blast radius is nil: the admin client's category mask paths are
// dead code and internal/betaseed sends no mask at all, so every live caller either masks no category
// level or does a full replace. A caller that genuinely wants to move one level must send all three,
// which forces it to state the resulting path explicitly — or go through the tech card, which edits
// category_id and derives the whole triple from it.
func validateStyleCategoryMask(fields []string) error {
	if len(fields) == 0 {
		return nil // full replace: every level is written, so the path is stated in full
	}
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[normalizeStyleField(f)] = true
	}
	named := make([]string, 0, len(styleCategoryMaskKeys))
	for _, k := range styleCategoryMaskKeys {
		if want[k] {
			named = append(named, k)
		}
	}
	if len(named) == 0 || len(named) == len(styleCategoryMaskKeys) {
		return nil
	}
	return entity.NewFieldViolation("update_mask",
		fmt.Sprintf("partial category mask (%s)", strings.Join(named, ", ")), "",
		"a style's top/sub/type categories are one path through the category tree and must be updated together; "+
			"name all three in the mask, or edit the category on the tech card instead")
}

// normalizeStyleField folds a field-mask path (snake_case from canonical FieldMask, or camelCase as the
// admin client sends it) to the lowercase, underscore-free key used in styleFieldFragments — so
// "target_gender", "targetGender" and "targetgender" all match.
func normalizeStyleField(p string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(p), "_", ""))
}

// styleSetColumns builds the column-assignment part of the UPDATE for the requested normalized fields
// (nil/empty ⇒ all fields, matching the legacy full-replace). Returns the fragment WITHOUT the trailing
// lock-version bump (the caller always appends that) and whether the season column is among the written
// fields (the SKU-fact re-mint guard keys off this). An empty string means "no data columns" — the
// caller still bumps the lock, so a mask naming only unknown fields is a touch, not a silent full write.
func styleSetColumns(fields []string) (columns string, seasonWritten bool) {
	if len(fields) == 0 {
		// Legacy full-replace: the patch carries the whole triple, and styleFieldsSet already ends
		// with styleCategoryIDFragment, so category_id is derived here too. Do NOT re-append it —
		// that would assign the same column twice in one SET.
		return styleFieldsSet, true
	}
	want := make(map[string]bool, len(fields))
	for _, f := range fields {
		want[normalizeStyleField(f)] = true
	}
	frags := make([]string, 0, len(styleFieldFragments)+1)
	categoryWritten := false
	for _, o := range styleFieldFragments {
		if want[o.key] {
			frags = append(frags, o.frag)
			switch o.key {
			case "season":
				seasonWritten = true
			case "topcategoryid", "subcategoryid", "typeid":
				categoryWritten = true
			}
		}
	}
	// Emitted ONCE if any level of the triple is being written — not per field, or the same column
	// would be assigned three times in one SET. A mask that touches no category level (a fit-only or
	// model-wears-only save) leaves category_id alone entirely.
	if categoryWritten {
		frags = append(frags, styleCategoryIDFragment)
	}
	return strings.Join(frags, ", "), seasonWritten
}

// stylePatchParams maps a StylePatch onto the shared styleFieldsSet SQL bind names — the same set the
// legacy writeStyleFields wrote, now owned solely by UpdateStyle (R4/§14.7). season_year is preserved
// (COALESCE in styleFieldsSet). category_id is not a member of the patch, but it IS written whenever
// a category level is — derived from the triple by styleCategoryIDFragment, which binds these same
// topCategoryId/subCategoryId/typeId names.
func stylePatchParams(p entity.StylePatch) map[string]any {
	return map[string]any{
		"brand":              p.Brand,
		"seasonCode":         string(p.Season),
		"collection":         p.Collection,
		"targetGender":       string(p.TargetGender),
		"fit":                p.Fit,
		"composition":        p.Composition,
		"careInstructions":   p.CareInstructions,
		"modelWearsHeightCm": p.ModelWearsHeightCm,
		"modelWearsSizeId":   p.ModelWearsSizeId,
		"topCategoryId":      p.TopCategoryId,
		"subCategoryId":      p.SubCategoryId,
		"typeId":             p.TypeId,
	}
}

// UpdateStyle is the SOLE writer of a style's catalogue facts (brand/sku_season/collection/
// target_gender/fit/composition/care/model-wears/categories) — R4/§14.7. It is optimistically locked
// on the shared tech_card.lock_version (entity.ErrTechCardConflict on a stale value or a concurrent
// bump -> ABORTED; sql.ErrNoRows when the style is absent -> NOT_FOUND). A change to a SKU fact (the
// season code) re-mints EVERY unfrozen sibling colourway in the same tx; if ANY sibling is SKU-frozen
// (sku_locked_at set, has order/label history) the whole change is refused with
// entity.ErrStyleFrozenSiblings (FAILED_PRECONDITION) — the official path is CloneStyleForSeason. PLM
// facts (BOM/POM/ops/lifecycle) remain UpdateTechCard's; no fact is written by both. Returns the new
// shared lock_version.
func (s *Store) UpdateStyle(ctx context.Context, styleID, expectedLockVersion int, patch entity.StylePatch, fields []string) (int, error) {
	// The category levels are one tree path and may only be masked as a set; a partial category mask is
	// unsatisfiable rather than merely unsupported (see validateStyleCategoryMask). Checked before the
	// tx opens — it is a pure statement about the request.
	if err := validateStyleCategoryMask(fields); err != nil {
		return 0, err
	}
	// Honor the field mask: only the named facts are written, the rest keep their stored value (nil/empty
	// ⇒ legacy full-replace). This lets a partial editor — the tech card's fit/composition/care, the
	// colourway card's model-wears — save just what it owns without clobbering facts owned elsewhere.
	setColumns, seasonWritten := styleSetColumns(fields)
	var newLockVersion int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := storeutil.QueryNamedOne[struct {
			LockVersion int            `db:"lock_version"`
			SeasonCode  sql.NullString `db:"season_code"`
		}](ctx, rep.DB(),
			`SELECT lock_version, season_code FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if cur.LockVersion != expectedLockVersion {
			return entity.ErrTechCardConflict
		}
		// The season is the only SKU fact UpdateStyle can change; when it moves (and is actually being
		// written this call), the frozen policy applies. A mask that doesn't touch season never re-mints.
		skuFactsChanged := seasonWritten && cur.SeasonCode.String != string(patch.Season)
		if skuFactsChanged {
			frozen, err := storeutil.QueryNamedOne[struct {
				N int `db:"n"`
			}](ctx, rep.DB(),
				`SELECT COUNT(*) AS n FROM product WHERE style_id = :id AND sku_locked_at IS NOT NULL`,
				map[string]any{"id": styleID})
			if err != nil {
				return fmt.Errorf("check frozen siblings of style %d: %w", styleID, err)
			}
			if frozen.N > 0 {
				return entity.ErrStyleFrozenSiblings
			}
		}
		params := stylePatchParams(patch)
		params["styleId"] = styleID
		params["expected"] = expectedLockVersion
		setBody := "lock_version = lock_version + 1"
		if setColumns != "" {
			setBody = setColumns + ", lock_version = lock_version + 1"
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET `+setBody+` WHERE id = :styleId AND lock_version = :expected`,
			params)
		if err != nil {
			return fmt.Errorf("update style %d: %w", styleID, err)
		}
		// The row provably exists (loaded above), so 0 rows means the lock moved under us.
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		if skuFactsChanged {
			// Re-mint every sibling — we proved above that none is frozen (MintProductSKUs is a no-op on
			// a frozen product anyway, but the guard already refused the change if any sibling was frozen).
			ids, err := captureStyleColorwayIDs(ctx, rep.DB(), styleID)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if err := MintProductSKUs(ctx, rep.DB(), id); err != nil {
					return fmt.Errorf("re-mint colourway %d after style %d SKU change: %w", id, styleID, err)
				}
			}
		}
		// P4-flyover M1 (04-MAZE-FLYOVER.md): re-derive the structural composition (S17/WS3-Ф5) from
		// the style's shell-fabric BOM every save; a manual override already on file is preserved
		// (entity.ReconcileStyleComposition). A field-tagged error here (a fabric's own composition
		// does not sum to 100) aborts the whole style save, same as any other validation failure.
		if err := ReconcileStyleCompositionTx(ctx, rep.DB(), styleID); err != nil {
			return err
		}
		newLockVersion = expectedLockVersion + 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newLockVersion, nil
}

// captureStyleColorwayIDs returns the ids of every colourway (product) of a style (R1: a colourway is
// a product with product.style_id = the style).
func captureStyleColorwayIDs(ctx context.Context, db dependency.DB, styleID int) ([]int, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, db, `SELECT id FROM product WHERE style_id = :id`, map[string]any{"id": styleID})
	if err != nil {
		return nil, fmt.Errorf("capture colourways of style %d: %w", styleID, err)
	}
	ids := make([]int, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return ids, nil
}
