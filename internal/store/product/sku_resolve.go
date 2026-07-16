package product

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// productSKUFacts is everything MintProductSKUs needs to build a product's SKU. Styled fields come
// from the linked style (colourway + tech_card); when Styled is false the standalone fields apply.
type productSKUFacts struct {
	ID        int            `db:"id"`
	Season    string         `db:"season"`     // standalone season enum (SS/FW/PF/RC)
	CreatedYr int            `db:"created_yr"` // YEAR(created_at) — standalone year source
	Color     string         `db:"color"`      // standalone free-text colour name
	ColorCode sql.NullString `db:"color_code"` // standalone dictionary code
	ModelNo   sql.NullInt32  `db:"model_no"`   // standalone model number
	LockedAt  sql.NullTime   `db:"sku_locked_at"`

	// styled overrides (all NULL when the product realises no colourway)
	StyleSeasonCode sql.NullString `db:"style_season_code"`
	StyleSeasonYear sql.NullInt32  `db:"style_season_year"`
	StyleModelNo    sql.NullInt32  `db:"style_model_no"`
	CwColorCode     sql.NullString `db:"cw_color_code"`
	CwName          sql.NullString `db:"cw_name"`
	Styled          bool           `db:"styled"`
}

// loadProductSKUFacts reads the facts for one product, LEFT JOINing the (at most one) colourway that
// realises it and its style. Uses MIN(...) so a product accidentally linked to more than one
// colourway still yields one deterministic row (the structural UNIQUE guard makes multi-link
// unexpected, but the query must not fan out).
func loadProductSKUFacts(ctx context.Context, db dependency.DB, productID int) (*productSKUFacts, error) {
	const q = `
		SELECT p.id AS id,
		       sty.season_code AS season,
		       YEAR(p.created_at) AS created_yr,
		       p.color AS color,
		       p.color_code AS color_code,
		       p.model_no AS model_no,
		       p.sku_locked_at AS sku_locked_at,
		       tc.season_code AS style_season_code,
		       tc.season_year AS style_season_year,
		       tc.model_no    AS style_model_no,
		       cw.color_code  AS cw_color_code,
		       cw.name        AS cw_name,
		       (cw.id IS NOT NULL) AS styled
		FROM product p
		JOIN tech_card sty ON sty.id = p.style_id
		LEFT JOIN tech_card_colorway cw ON cw.id = (
		    SELECT MIN(cw2.id) FROM tech_card_colorway cw2 WHERE cw2.product_id = p.id)
		LEFT JOIN tech_card tc ON tc.id = cw.tech_card_id
		WHERE p.id = :id`
	f, err := storeutil.QueryNamedOne[productSKUFacts](ctx, db, q, map[string]any{"id": productID})
	if err != nil {
		return nil, fmt.Errorf("load sku facts for product %d: %w", productID, err)
	}
	return &f, nil
}

// resolveSegments turns the raw facts into the generator's SKUSegments, ensuring a model number
// exists (allocating one from the shared counter when missing) and resolving the colour code from
// the dictionary, falling back to the free-text name mapper. It may write model_no back, so it takes
// db and runs inside the caller's tx.
func resolveSegments(ctx context.Context, db dependency.DB, f *productSKUFacts) (SKUSegments, error) {
	var seg SKUSegments
	if f.Styled {
		seg.Season = entity.SeasonEnum(f.StyleSeasonCode.String)
		if f.StyleSeasonYear.Valid {
			seg.Year = int(f.StyleSeasonYear.Int32)
		}
		modelNo, err := ensureStyleModelNo(ctx, db, f)
		if err != nil {
			return seg, err
		}
		seg.ModelNo = modelNo
		seg.ColorCode = resolveColorCode(f.CwColorCode, f.CwName.String)
		seg.ColorName = f.CwName.String
		return seg, nil
	}

	seg.Season = entity.SeasonEnum(f.Season)
	seg.Year = f.CreatedYr
	modelNo, err := ensureProductModelNo(ctx, db, f)
	if err != nil {
		return seg, err
	}
	seg.ModelNo = modelNo
	seg.ColorCode = resolveColorCode(f.ColorCode, f.Color)
	seg.ColorName = f.Color
	return seg, nil
}

// resolveColorCode returns the dictionary code to use for the colour segment: the explicit code if
// set, else the free-text name mapped to a dictionary code, else "" (colorSegment then translits the
// name or falls back to UNK — never writing a non-dictionary code to color_code).
func resolveColorCode(code sql.NullString, name string) string {
	if code.Valid && code.String != "" {
		return code.String
	}
	if mapped, ok := cache.MapColorNameToCode(name); ok {
		return mapped
	}
	return ""
}

// ensureStyleModelNo returns the style's model number, allocating and persisting one on the tech_card
// if it has none yet (self-healing so minting never emits 00000).
func ensureStyleModelNo(ctx context.Context, db dependency.DB, f *productSKUFacts) (int, error) {
	if f.StyleModelNo.Valid {
		return int(f.StyleModelNo.Int32), nil
	}
	// need the style id — re-derive from the colourway link
	var styleID int
	row, err := storeutil.QueryNamedOne[struct {
		TechCardID int `db:"tech_card_id"`
	}](ctx, db, `SELECT tc.id AS tech_card_id FROM tech_card_colorway cw JOIN tech_card tc ON tc.id = cw.tech_card_id WHERE cw.product_id = :id ORDER BY cw.id LIMIT 1`,
		map[string]any{"id": f.ID})
	if err != nil {
		return 0, fmt.Errorf("resolve style id for product %d: %w", f.ID, err)
	}
	styleID = row.TechCardID
	n, err := storeutil.AllocateModelNo(ctx, db, "tech_card", styleID)
	if err != nil {
		return 0, err
	}
	if err := storeutil.ExecNamed(ctx, db, `UPDATE tech_card SET model_no = :n WHERE id = :id AND model_no IS NULL`,
		map[string]any{"n": n, "id": styleID}); err != nil {
		return 0, fmt.Errorf("persist style model_no: %w", err)
	}
	return n, nil
}

// ensureProductModelNo returns the standalone product's model number, allocating and persisting one
// if missing.
func ensureProductModelNo(ctx context.Context, db dependency.DB, f *productSKUFacts) (int, error) {
	if f.ModelNo.Valid {
		return int(f.ModelNo.Int32), nil
	}
	n, err := storeutil.AllocateModelNo(ctx, db, "product", f.ID)
	if err != nil {
		return 0, err
	}
	if err := storeutil.ExecNamed(ctx, db, `UPDATE product SET model_no = :n WHERE id = :id AND model_no IS NULL`,
		map[string]any{"n": n, "id": f.ID}); err != nil {
		return 0, fmt.Errorf("persist product model_no: %w", err)
	}
	return n, nil
}

// BackfillSKUs mints new-format SKUs for every non-frozen product that still needs one: base SKU
// empty or in the old format, or any variant SKU missing. It reuses MintProductSKUs so backfilled
// values are identical to runtime-generated ones (same translit, fallbacks, style-vs-standalone).
// Deleted products are INCLUDED. It is idempotent and self-limiting — once a product is converted it
// no longer matches the predicate — so it is safe to run on every boot. It never fails boot: a single
// product that cannot be minted is logged and skipped. Frozen products (sku_locked_at) are excluded.
func (s *Store) BackfillSKUs(ctx context.Context) error {
	ids, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, s.DB, `
		SELECT id FROM product
		WHERE sku_locked_at IS NULL
		  AND (
		        sku = ''
		     OR sku NOT REGEXP '^[A-Z]{2}[0-9]{2}-[0-9]{5}-'
		     OR EXISTS (SELECT 1 FROM product_size ps WHERE ps.product_id = product.id AND ps.sku IS NULL)
		  )`, map[string]any{})
	if err != nil {
		return fmt.Errorf("backfill: list products needing SKUs: %w", err)
	}
	minted := 0
	for _, r := range ids {
		id := r.ID
		if err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
			return MintProductSKUs(ctx, rep.DB(), id)
		}); err != nil {
			slog.WarnContext(ctx, "sku backfill: could not mint product; skipping",
				slog.Int("product_id", id), slog.String("err", err.Error()))
			continue
		}
		minted++
	}
	if len(ids) > 0 {
		slog.InfoContext(ctx, "sku backfill complete", slog.Int("candidates", len(ids)), slog.Int("minted", minted))
	}
	return nil
}

// MintProductSKUs (re)generates the base SKU and every per-size variant SKU for a product, unless the
// product's SKU is frozen (sku_locked_at set), in which case it is a no-op. It is idempotent: the
// same facts yield the same SKUs. Runs inside the caller's transaction.
func MintProductSKUs(ctx context.Context, db dependency.DB, productID int) error {
	f, err := loadProductSKUFacts(ctx, db, productID)
	if err != nil {
		return err
	}
	if f.LockedAt.Valid {
		return nil // frozen: never rebuild
	}

	seg, err := resolveSegments(ctx, db, f)
	if err != nil {
		return err
	}
	base := BuildBaseSKU(seg)
	base, err = disambiguateBase(ctx, db, base, productID)
	if err != nil {
		return err
	}
	if err := storeutil.ExecNamed(ctx, db, `UPDATE product SET sku = :sku WHERE id = :id`,
		map[string]any{"sku": base, "id": productID}); err != nil {
		return fmt.Errorf("set product.sku: %w", err)
	}
	return mintVariantSKUs(ctx, db, productID, base)
}

// mintVariantSKUs sets product_size.sku for every size row of the product. A size whose ordinal is 0
// (unseeded system) is skipped with a warning rather than emitting a bogus "-00" segment.
func mintVariantSKUs(ctx context.Context, db dependency.DB, productID int, base string) error {
	sizeRows, err := storeutil.QueryListNamed[struct {
		SizeID int `db:"size_id"`
	}](ctx, db, `SELECT size_id FROM product_size WHERE product_id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("list product sizes: %w", err)
	}
	for _, r := range sizeRows {
		sz, ok := cache.GetSizeById(r.SizeID)
		if !ok || sz.SkuOrd == 0 {
			slog.WarnContext(ctx, "sku: skipping variant for size without ordinal",
				slog.Int("product_id", productID), slog.Int("size_id", r.SizeID))
			continue
		}
		variant := BuildVariantSKU(base, sz.SkuOrd)
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"sku": variant, "pid": productID, "sid": r.SizeID}); err != nil {
			return fmt.Errorf("set product_size.sku (size %d): %w", r.SizeID, err)
		}
	}
	return nil
}

// disambiguateBase guarantees base SKU uniqueness. Structural guards (unique model_no + the
// UNIQUE(tech_card_id, color_code) colourway constraint) make a real collision unexpected, so a clash
// is logged and resolved with a numeric suffix as an emergency last resort (it breaks the fixed
// length — see the generator doc).
func disambiguateBase(ctx context.Context, db dependency.DB, base string, productID int) (string, error) {
	candidate := base
	for suffix := 2; suffix < 100; suffix++ {
		taken, err := baseSKUTakenByOther(ctx, db, candidate, productID)
		if err != nil {
			return "", err
		}
		if !taken {
			if candidate != base {
				slog.WarnContext(ctx, "sku: base collision resolved with emergency suffix",
					slog.String("base", base), slog.String("resolved", candidate), slog.Int("product_id", productID))
			}
			return candidate, nil
		}
		candidate = fmt.Sprintf("%s%d", base, suffix)
	}
	return "", fmt.Errorf("sku: could not disambiguate base %q after 98 attempts", base)
}

func baseSKUTakenByOther(ctx context.Context, db dependency.DB, sku string, productID int) (bool, error) {
	row, err := storeutil.QueryNamedOne[struct {
		N int `db:"n"`
	}](ctx, db, `SELECT COUNT(*) AS n FROM product WHERE sku = :sku AND id != :id`,
		map[string]any{"sku": sku, "id": productID})
	if err != nil {
		return false, fmt.Errorf("check base sku uniqueness: %w", err)
	}
	return row.N > 0, nil
}
