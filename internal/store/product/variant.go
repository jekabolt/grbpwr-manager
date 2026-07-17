package product

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// CreateVariant adds a new variant (a size) to a colourway at zero stock and mints its variant SKU
// (R2). The size is immutable and the variant is created ACTIVE. It is rejected if the colourway does
// not exist (sql.ErrNoRows), is archived (ErrColorwayArchived), or already carries that size
// (ErrVariantExists). The SKU mint is best-effort: a DRAFT colourway whose base SKU is not yet built
// keeps a NULL variant SKU (publish preconditions enforce a valid SKU before it can go live), so a
// variant can still be laid out on a not-fully-specified draft.
func (s *Store) CreateVariant(ctx context.Context, colorwayID, sizeID int) (entity.Variant, error) {
	sz, ok := cache.GetSizeById(sizeID)
	if !ok {
		return entity.Variant{}, fmt.Errorf("unknown size id %d", sizeID)
	}
	var out entity.Variant
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		st, err := loadColorwayLifecycle(ctx, rep.DB(), colorwayID)
		if err != nil {
			return err // wrapped sql.ErrNoRows when the colourway is absent -> NOT_FOUND upstream
		}
		if st == entity.ColorwayStatusArchived {
			return fmt.Errorf("colourway %d: %w", colorwayID, entity.ErrColorwayArchived)
		}
		// S10/WS5: the size must belong to a system permitted for the OWNING STYLE's category (not
		// just any size in the dictionary) -- server-side, not just a UI filter.
		path, err := loadColorwayStyleCategoryPath(ctx, rep.DB(), colorwayID)
		if err != nil {
			return fmt.Errorf("load style category for colourway %d: %w", colorwayID, err)
		}
		if verr := entity.ValidateSizeAgainstCategory("size_id", path, cache.CategoryLabel(path), cache.GetCategorySizeSystems(), sz); verr != nil {
			return verr
		}
		exists, err := storeutil.QueryNamedOne[struct {
			N int `db:"n"`
		}](ctx, rep.DB(), `SELECT COUNT(*) AS n FROM product_size WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"pid": colorwayID, "sid": sz.Id})
		if err != nil {
			return fmt.Errorf("check existing variant (colourway %d size %d): %w", colorwayID, sz.Id, err)
		}
		if exists.N > 0 {
			return fmt.Errorf("colourway %d size %d: %w", colorwayID, sz.Id, entity.ErrVariantExists)
		}
		id, err := storeutil.ExecNamedLastId(ctx, rep.DB(),
			`INSERT INTO product_size (product_id, size_id, quantity, status) VALUES (:pid, :sid, 0, :status)`,
			map[string]any{"pid": colorwayID, "sid": sz.Id, "status": uint8(entity.VariantStatusActive)})
		if err != nil {
			return fmt.Errorf("insert variant (colourway %d size %d): %w", colorwayID, sz.Id, err)
		}
		// Best-effort mint: a draft without a built base SKU keeps a NULL variant SKU until publish.
		if err := ensureVariantSKU(ctx, rep.DB(), colorwayID, sz.Id); err != nil {
			slog.Default().WarnContext(ctx, "created variant without a minted SKU (base not built yet)",
				slog.Int("colorway_id", colorwayID), slog.Int("size_id", sz.Id), slog.String("err", err.Error()))
		}
		out, err = getVariantByID(ctx, rep.DB(), id)
		return err
	})
	return out, err
}

// SetVariantStatus applies a lifecycle status to a variant under an optimistic guard on its current
// value (R2). It returns sql.ErrNoRows when the variant is absent (NOT_FOUND upstream) and rejects a
// concurrent change (RowsAffected != 1) rather than silently losing it. size_id/SKU are never touched.
func (s *Store) SetVariantStatus(ctx context.Context, variantID int, target entity.VariantStatus) (entity.Variant, error) {
	if !target.Valid() {
		return entity.Variant{}, fmt.Errorf("invalid variant status %d", uint8(target))
	}
	var out entity.Variant
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		cur, err := getVariantByID(ctx, rep.DB(), variantID)
		if err != nil {
			return err // wrapped sql.ErrNoRows -> NOT_FOUND upstream
		}
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE product_size SET status = :next WHERE id = :id AND status = :cur`,
			map[string]any{"next": uint8(target), "cur": cur.Status, "id": variantID})
		if err != nil {
			return fmt.Errorf("set variant %d status: %w", variantID, err)
		}
		if rows != 1 {
			return fmt.Errorf("variant %d status changed concurrently; retry", variantID)
		}
		out, err = getVariantByID(ctx, rep.DB(), variantID)
		return err
	})
	return out, err
}

// getVariantByID reads a single variant on the given connection (composes into a transaction).
func getVariantByID(ctx context.Context, db dependency.DB, variantID int) (entity.Variant, error) {
	return storeutil.QueryNamedOne[entity.Variant](ctx, db,
		`SELECT id, quantity, product_id, size_id, sku, status FROM product_size WHERE id = :id`,
		map[string]any{"id": variantID})
}

// loadColorwayStyleCategoryPath loads the category triple (top/sub/type) of the STYLE that owns a
// colourway (S10/WS5): CreateVariant validates the requested size against this, not the colourway's
// own row (a colourway carries no category -- category is a style fact, R4/§14.7).
func loadColorwayStyleCategoryPath(ctx context.Context, db dependency.DB, colorwayID int) (entity.StyleCategoryPath, error) {
	row, err := storeutil.QueryNamedOne[entity.StyleCategoryPath](ctx, db, `
		SELECT sty.top_category_id AS top_category_id, sty.sub_category_id AS sub_category_id, sty.type_id AS type_id
		FROM product p
		JOIN tech_card sty ON sty.id = p.style_id
		WHERE p.id = :id`, map[string]any{"id": colorwayID})
	if err != nil {
		return entity.StyleCategoryPath{}, fmt.Errorf("load colourway %d style category: %w", colorwayID, err)
	}
	return row, nil
}
