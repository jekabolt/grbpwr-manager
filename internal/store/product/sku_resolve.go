package product

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// productSKUFacts is everything MintProductSKUs needs to build a product's SKU. Styled fields come
// from the linked style (colourway + tech_card); when Styled is false the standalone fields apply.
type productSKUFacts struct {
	ID        int           `db:"id"`
	Season    string        `db:"season"`     // standalone season enum (SS/FW/PF/RC)
	CreatedYr int           `db:"created_yr"` // YEAR(created_at) — standalone year source
	ColorCode string        `db:"color_code"` // required standalone dictionary code
	ModelNo   sql.NullInt32 `db:"model_no"`   // standalone model number
	LockedAt  sql.NullTime  `db:"sku_locked_at"`

	// styled overrides (all NULL when the product realises no colourway)
	StyleSeasonCode sql.NullString `db:"style_season_code"`
	StyleSeasonYear sql.NullInt32  `db:"style_season_year"`
	StyleModelNo    sql.NullInt32  `db:"style_model_no"`
	CwColorCode     sql.NullString `db:"cw_color_code"`
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
		       p.color_code AS color_code,
		       p.model_no AS model_no,
		       p.sku_locked_at AS sku_locked_at,
		       tc.season_code AS style_season_code,
		       tc.season_year AS style_season_year,
		       tc.model_no    AS style_model_no,
		       cw.color_code  AS cw_color_code,
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
// exists (allocating one from the shared counter when missing) and requiring the FK-backed colour
// dictionary code. It may write model_no back, so it takes db and runs inside the caller's tx.
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
		if !f.CwColorCode.Valid {
			return seg, fmt.Errorf("styled product %d has no canonical color_code", f.ID)
		}
		if err := validateColorCode(f.CwColorCode.String); err != nil {
			return seg, fmt.Errorf("styled product %d: %w", f.ID, err)
		}
		seg.ColorCode = f.CwColorCode.String
		return seg, nil
	}

	seg.Season = entity.SeasonEnum(f.Season)
	seg.Year = f.CreatedYr
	modelNo, err := ensureProductModelNo(ctx, db, f)
	if err != nil {
		return seg, err
	}
	seg.ModelNo = modelNo
	if err := validateColorCode(f.ColorCode); err != nil {
		return seg, fmt.Errorf("product %d: %w", f.ID, err)
	}
	seg.ColorCode = f.ColorCode
	return seg, nil
}

func validateColorCode(code string) error {
	if len(code) != 3 || code != strings.ToUpper(code) || strings.TrimSpace(code) != code {
		return fmt.Errorf("color_code %q is not canonical", code)
	}
	if _, ok := cache.GetColorByCode(code); !ok {
		return fmt.Errorf("color_code %q is not in the dictionary", code)
	}
	return nil
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
		// Frozen: the base SKU and every existing variant SKU are immutable and must never be rebuilt.
		// But a size added to a frozen colourway still needs a variant SKU — derive it from the already
		// frozen base and mint ONLY the missing ones, leaving base and existing variants untouched.
		return mintMissingVariantSKUs(ctx, db, productID)
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
		ord, err := requireSizeOrdinal(r.SizeID)
		if err != nil {
			return err
		}
		variant := BuildVariantSKU(base, ord)
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"sku": variant, "pid": productID, "sid": r.SizeID}); err != nil {
			return fmt.Errorf("set product_size.sku (size %d): %w", r.SizeID, err)
		}
	}
	return nil
}

// mintMissingVariantSKUs sets product_size.sku for a FROZEN product's sizes that still lack one,
// deriving each from the product's already-frozen base SKU. The base and every existing variant SKU
// are left untouched — this exists only so a size added to a frozen colourway (or one left NULL by an
// older bug) gets a valid variant SKU without rebuilding the frozen identity.
func mintMissingVariantSKUs(ctx context.Context, db dependency.DB, productID int) error {
	baseRow, err := storeutil.QueryNamedOne[struct {
		SKU string `db:"sku"`
	}](ctx, db, `SELECT sku FROM product WHERE id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("load frozen base sku for product %d: %w", productID, err)
	}
	if baseRow.SKU == "" {
		// No base to derive from (should not happen once frozen) — nothing safe to mint.
		return nil
	}
	sizeRows, err := storeutil.QueryListNamed[struct {
		SizeID int `db:"size_id"`
	}](ctx, db, `SELECT size_id FROM product_size WHERE product_id = :id AND (sku IS NULL OR sku = '')`,
		map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("list frozen variants needing sku: %w", err)
	}
	for _, r := range sizeRows {
		ord, err := requireSizeOrdinal(r.SizeID)
		if err != nil {
			return err
		}
		variant := BuildVariantSKU(baseRow.SKU, ord)
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"sku": variant, "pid": productID, "sid": r.SizeID}); err != nil {
			return fmt.Errorf("set frozen variant sku (size %d): %w", r.SizeID, err)
		}
	}
	return nil
}

// requireSizeOrdinal returns a size's SKU ordinal, FAILING (rather than emitting a SKU-less variant)
// when the size is unknown to the cache, has no ordinal (0), or is outside the two-digit range the
// variant segment allows. Every mint path routes through it so an invalid ordinal rolls the whole
// operation back instead of silently committing a NULL variant SKU (problem 017).
func requireSizeOrdinal(sizeID int) (int, error) {
	sz, ok := cache.GetSizeById(sizeID)
	if !ok {
		return 0, fmt.Errorf("cannot mint variant sku: size %d is not in the size dictionary", sizeID)
	}
	if sz.SkuOrd < 1 || sz.SkuOrd > 99 {
		return 0, fmt.Errorf("cannot mint variant sku: size %d has an invalid SKU ordinal %d (must be 1-99)", sizeID, sz.SkuOrd)
	}
	return sz.SkuOrd, nil
}

// ensureVariantSKU guarantees a single variant row (product_id, size_id) has a SKU, minting it from the
// product's base when absent. It NEVER rewrites an existing SKU (so a frozen variant's identity is
// preserved), and it HARD-FAILS rather than leave a SKU-less variant when it cannot mint — no base SKU
// yet, or the size has no ordinal. Stock paths that can materialise a new variant (admin stock edit,
// production receive) call this right after the upsert so no successful path leaves a NULL/empty SKU.
func ensureVariantSKU(ctx context.Context, db dependency.DB, productID, sizeID int) error {
	cur, err := storeutil.QueryNamedOne[struct {
		SKU sql.NullString `db:"sku"`
	}](ctx, db, `SELECT sku FROM product_size WHERE product_id = :pid AND size_id = :sid`,
		map[string]any{"pid": productID, "sid": sizeID})
	if err != nil {
		return fmt.Errorf("load variant sku (product %d size %d): %w", productID, sizeID, err)
	}
	if cur.SKU.Valid && cur.SKU.String != "" {
		return nil // identity already set — a stock quantity change must never touch it
	}
	baseRow, err := storeutil.QueryNamedOne[struct {
		SKU string `db:"sku"`
	}](ctx, db, `SELECT sku FROM product WHERE id = :id`, map[string]any{"id": productID})
	if err != nil {
		return fmt.Errorf("load base sku for product %d: %w", productID, err)
	}
	if baseRow.SKU == "" {
		return fmt.Errorf("cannot mint variant sku: product %d has no base sku", productID)
	}
	ord, err := requireSizeOrdinal(sizeID)
	if err != nil {
		return err
	}
	variant := BuildVariantSKU(baseRow.SKU, ord)
	if err := storeutil.ExecNamed(ctx, db,
		`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
		map[string]any{"sku": variant, "pid": productID, "sid": sizeID}); err != nil {
		return fmt.Errorf("set variant sku (product %d size %d): %w", productID, sizeID, err)
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
