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
		if !f.StyleSeasonCode.Valid || !f.StyleSeasonYear.Valid {
			return seg, fmt.Errorf("styled product %d has no complete sku_season", f.ID)
		}
		if err := validateSKUSeason(entity.SeasonEnum(f.StyleSeasonCode.String), int(f.StyleSeasonYear.Int32)); err != nil {
			return seg, fmt.Errorf("styled product %d: %w", f.ID, err)
		}
		seg.Season = entity.SeasonEnum(f.StyleSeasonCode.String)
		seg.Year = int(f.StyleSeasonYear.Int32)
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
	if err := validateSKUSeason(seg.Season, seg.Year); err != nil {
		return seg, fmt.Errorf("product %d: %w", f.ID, err)
	}
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

func validateSKUSeason(code entity.SeasonEnum, year int) error {
	if !entity.IsValidSeason(code) {
		return fmt.Errorf("season code %q is not canonical", code)
	}
	if year < 2000 || year > 2099 {
		return fmt.Errorf("season year %d must be between 2000 and 2099", year)
	}
	return nil
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

const (
	baseSKUInvariantPattern    = `^(SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3}$`
	variantSKUInvariantPattern = `^(SS|FW|PF|RC)[0-9]{2}-[0-9]{5}-[A-Z0-9]{3}-[0-9]{2}$`
)

type skuInvariantViolation struct {
	Kind      string        `db:"kind"`
	ProductID int           `db:"product_id"`
	VariantID sql.NullInt64 `db:"variant_id"`
}

type skuBackfillReadinessError struct {
	FailedProductIDs    []int
	ViolationProductIDs []int
	ViolationVariantIDs []int64
	ViolationCount      int
}

func (e *skuBackfillReadinessError) Error() string {
	return fmt.Sprintf(
		"sku backfill/readiness failed: mint_failed_product_ids=%v invariant_violations=%d violation_product_ids=%v violation_variant_ids=%v",
		e.FailedProductIDs, e.ViolationCount, e.ViolationProductIDs, e.ViolationVariantIDs,
	)
}

func appendUnique[T comparable](values []T, value T) []T {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

// findSKUInvariantViolations is the readiness postcondition. It covers every product, including
// deleted and frozen rows: a frozen identity is immutable, but it is not allowed to be malformed.
// The exact variant equality check is stronger than shape validation and catches a SKU attached to
// the wrong size ordinal or base product.
func findSKUInvariantViolations(ctx context.Context, db dependency.DB) ([]skuInvariantViolation, error) {
	return storeutil.QueryListNamed[skuInvariantViolation](ctx, db, `
		SELECT v.kind, v.product_id, v.variant_id
		FROM (
			SELECT 'invalid_base' AS kind, p.id AS product_id, NULL AS variant_id
			FROM product p
			WHERE p.sku = '' OR NOT REGEXP_LIKE(p.sku, :base_pattern, 'c')

			UNION ALL

			SELECT 'duplicate_base' AS kind, p.id AS product_id, NULL AS variant_id
			FROM product p
			JOIN (
				SELECT sku FROM product
				WHERE sku <> ''
				GROUP BY sku HAVING COUNT(*) > 1
			) duplicate ON duplicate.sku = p.sku

			UNION ALL

			SELECT 'invalid_variant' AS kind, ps.product_id, ps.id AS variant_id
			FROM product_size ps
			JOIN product p ON p.id = ps.product_id
			JOIN size s ON s.id = ps.size_id
			WHERE ps.sku IS NULL
			   OR ps.sku = ''
			   OR NOT REGEXP_LIKE(ps.sku, :variant_pattern, 'c')
			   OR BINARY ps.sku <> BINARY CONCAT(p.sku, '-', LPAD(s.sku_ord, 2, '0'))

			UNION ALL

			SELECT 'duplicate_variant' AS kind, ps.product_id, ps.id AS variant_id
			FROM product_size ps
			JOIN (
				SELECT sku FROM product_size
				WHERE sku IS NOT NULL AND sku <> ''
				GROUP BY sku HAVING COUNT(*) > 1
			) duplicate ON duplicate.sku = ps.sku
		) v
		ORDER BY v.kind, v.product_id, v.variant_id`, map[string]any{
		"base_pattern":    baseSKUInvariantPattern,
		"variant_pattern": variantSKUInvariantPattern,
	})
}

// BackfillSKUs mints canonical SKUs for every non-frozen product that needs repair, then verifies
// the complete catalog before readiness. Deleted products are included. Frozen products are never
// reminted; if their identity violates the invariant the postcondition fails and boot is blocked.
// Per-product mint failures are aggregated so one bad row does not hide the rest of the report.
func (s *Store) BackfillSKUs(ctx context.Context) error {
	ids, err := storeutil.QueryListNamed[struct {
		ID int `db:"id"`
	}](ctx, s.DB, `
		SELECT p.id FROM product p
		WHERE p.sku_locked_at IS NULL
		  AND (
		        p.sku = ''
		     OR NOT REGEXP_LIKE(p.sku, :base_pattern, 'c')
		     OR EXISTS (
		          SELECT 1
		          FROM product_size ps
		          JOIN size s ON s.id = ps.size_id
		          WHERE ps.product_id = p.id
		            AND (
		                 ps.sku IS NULL
		              OR ps.sku = ''
		              OR NOT REGEXP_LIKE(ps.sku, :variant_pattern, 'c')
		              OR BINARY ps.sku <> BINARY CONCAT(p.sku, '-', LPAD(s.sku_ord, 2, '0'))
		            )
		     )
		  )
		ORDER BY p.id`, map[string]any{
		"base_pattern":    baseSKUInvariantPattern,
		"variant_pattern": variantSKUInvariantPattern,
	})
	if err != nil {
		return fmt.Errorf("backfill: list products needing SKUs: %w", err)
	}
	minted := 0
	failedProductIDs := make([]int, 0)
	for _, r := range ids {
		id := r.ID
		if err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
			return MintProductSKUs(ctx, rep.DB(), id)
		}); err != nil {
			failedProductIDs = append(failedProductIDs, id)
			slog.ErrorContext(ctx, "sku backfill: could not mint product",
				slog.Int("product_id", id), slog.String("err", err.Error()))
			continue
		}
		minted++
	}

	violations, err := findSKUInvariantViolations(ctx, s.DB)
	if err != nil {
		return fmt.Errorf("backfill: verify SKU readiness invariants: %w", err)
	}
	violationProductIDs := make([]int, 0)
	violationVariantIDs := make([]int64, 0)
	for _, violation := range violations {
		violationProductIDs = appendUnique(violationProductIDs, violation.ProductID)
		if violation.VariantID.Valid {
			violationVariantIDs = appendUnique(violationVariantIDs, violation.VariantID.Int64)
		}
		slog.ErrorContext(ctx, "sku readiness invariant violation",
			slog.String("kind", violation.Kind),
			slog.Int("product_id", violation.ProductID),
			slog.Int64("variant_id", violation.VariantID.Int64))
	}

	slog.InfoContext(ctx, "sku backfill readiness check complete",
		slog.Int("candidates", len(ids)),
		slog.Int("minted", minted),
		slog.Int("mint_failures", len(failedProductIDs)),
		slog.Int("invariant_violations", len(violations)))
	if len(failedProductIDs) > 0 || len(violations) > 0 {
		return &skuBackfillReadinessError{
			FailedProductIDs:    failedProductIDs,
			ViolationProductIDs: violationProductIDs,
			ViolationVariantIDs: violationVariantIDs,
			ViolationCount:      len(violations),
		}
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

type sizeOrdinalFact struct {
	SizeID    int            `db:"size_id"`
	SKUOrd    int            `db:"sku_ord"`
	SKUSystem string         `db:"sku_system"`
	SKU       sql.NullString `db:"sku"`
}

// validateSizeOrdinalFacts enforces the complete product-level size contract before any SKU is
// written. DB constraints protect each dictionary row; this additionally rejects a colourway that
// mixes size systems, because the same two-digit ordinal has meaning only inside one system.
func validateSizeOrdinalFacts(productID int, facts []sizeOrdinalFact) error {
	var productSystem entity.SizeSKUSystem
	ordSizeID := make(map[int]int, len(facts))
	for _, fact := range facts {
		system := entity.SizeSKUSystem(fact.SKUSystem)
		if !entity.IsValidSizeSKUSystem(system) {
			return fmt.Errorf("cannot mint variant sku: size %d has invalid SKU system %q", fact.SizeID, fact.SKUSystem)
		}
		if fact.SKUOrd < 1 || fact.SKUOrd > 99 {
			return fmt.Errorf("cannot mint variant sku: size %d has invalid SKU ordinal %d (must be 1-99)", fact.SizeID, fact.SKUOrd)
		}
		if productSystem == "" {
			productSystem = system
		} else if system != productSystem {
			return fmt.Errorf("cannot mint variant sku: product %d mixes size SKU systems %q and %q", productID, productSystem, system)
		}
		if previousSizeID, exists := ordSizeID[fact.SKUOrd]; exists {
			return fmt.Errorf("cannot mint variant sku: product %d sizes %d and %d share SKU ordinal %d", productID, previousSizeID, fact.SizeID, fact.SKUOrd)
		}
		ordSizeID[fact.SKUOrd] = fact.SizeID
	}
	return nil
}

// loadValidatedSizeOrdinalFacts reads ordinal facts from the transaction's DB connection rather
// than the process cache. That makes a size dictionary row and its constraints the authoritative
// source even during cache refresh, and validates the single-system invariant over the whole product.
func loadValidatedSizeOrdinalFacts(ctx context.Context, db dependency.DB, productID int) ([]sizeOrdinalFact, error) {
	facts, err := storeutil.QueryListNamed[sizeOrdinalFact](ctx, db, `
		SELECT ps.size_id, s.sku_ord, s.sku_system, ps.sku
		FROM product_size ps
		JOIN size s ON s.id = ps.size_id
		WHERE ps.product_id = :id
		ORDER BY ps.size_id`, map[string]any{"id": productID})
	if err != nil {
		return nil, fmt.Errorf("list product size SKU facts: %w", err)
	}
	if err := validateSizeOrdinalFacts(productID, facts); err != nil {
		return nil, err
	}
	return facts, nil
}

// mintVariantSKUs sets product_size.sku for every size row after validating the full size contract.
func mintVariantSKUs(ctx context.Context, db dependency.DB, productID int, base string) error {
	facts, err := loadValidatedSizeOrdinalFacts(ctx, db, productID)
	if err != nil {
		return err
	}
	for _, fact := range facts {
		variant := BuildVariantSKU(base, fact.SKUOrd)
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"sku": variant, "pid": productID, "sid": fact.SizeID}); err != nil {
			return fmt.Errorf("set product_size.sku (size %d): %w", fact.SizeID, err)
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
		return fmt.Errorf("cannot mint frozen variant sku: product %d has no frozen base sku", productID)
	}
	facts, err := loadValidatedSizeOrdinalFacts(ctx, db, productID)
	if err != nil {
		return err
	}
	for _, fact := range facts {
		if fact.SKU.Valid && fact.SKU.String != "" {
			continue
		}
		variant := BuildVariantSKU(baseRow.SKU, fact.SKUOrd)
		if err := storeutil.ExecNamed(ctx, db,
			`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
			map[string]any{"sku": variant, "pid": productID, "sid": fact.SizeID}); err != nil {
			return fmt.Errorf("set frozen variant sku (size %d): %w", fact.SizeID, err)
		}
	}
	return nil
}

// ensureVariantSKU guarantees a single variant row (product_id, size_id) has a SKU, minting it from the
// product's base when absent. It NEVER rewrites an existing SKU (so a frozen variant's identity is
// preserved), and it HARD-FAILS rather than leave a SKU-less variant when it cannot mint — no base SKU
// yet, or the size has no ordinal. Stock paths that can materialise a new variant (admin stock edit,
// production receive) call this right after the upsert so no successful path leaves a NULL/empty SKU.
func ensureVariantSKU(ctx context.Context, db dependency.DB, productID, sizeID int) error {
	facts, err := loadValidatedSizeOrdinalFacts(ctx, db, productID)
	if err != nil {
		return err
	}
	var target *sizeOrdinalFact
	for i := range facts {
		if facts[i].SizeID == sizeID {
			target = &facts[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("cannot mint variant sku: product %d has no size %d", productID, sizeID)
	}
	if target.SKU.Valid && target.SKU.String != "" {
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
	variant := BuildVariantSKU(baseRow.SKU, target.SKUOrd)
	if err := storeutil.ExecNamed(ctx, db,
		`UPDATE product_size SET sku = :sku WHERE product_id = :pid AND size_id = :sid`,
		map[string]any{"sku": variant, "pid": productID, "sid": sizeID}); err != nil {
		return fmt.Errorf("set variant sku (product %d size %d): %w", productID, sizeID, err)
	}
	return nil
}

// disambiguateBase enforces uniqueness without inventing another wire format. Structural guards make
// a collision unexpected; if one occurs, mint fails and the transaction rolls back so the upstream
// model/color facts can be fixed. Numeric emergency suffixes are forbidden because they break the
// fixed-length URL, analytics and readiness contract.
func disambiguateBase(ctx context.Context, db dependency.DB, base string, productID int) (string, error) {
	taken, err := baseSKUTakenByOther(ctx, db, base, productID)
	if err != nil {
		return "", err
	}
	if taken {
		return "", fmt.Errorf("sku: canonical base %q collides with another product; fix model/color facts", base)
	}
	return base, nil
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
