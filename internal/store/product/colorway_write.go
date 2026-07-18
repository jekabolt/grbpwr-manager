package product

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// CreateColorway creates a new DRAFT colourway attached to an EXISTING style (R2/R4 write
// decomposition — the North Star replacement for the coupled AddProduct/UpsertColorway). It writes
// ONLY colourway-owned data: the product row (DRAFT, no SKU minted until publish), its merch
// translations, media, tags and prices. It NEVER synthesises a style, NEVER writes style facts (those
// are UpdateStyle's, R4/§14.7), creates NO variants (CreateVariant) and touches NO size chart
// (UpdateStyleSizeChart). The style must exist (sql.ErrNoRows otherwise -> NOT_FOUND) and the
// (style_id, color_code) pair must be free (entity.ErrColorwayColorExists on a duplicate, UNIQUE R1).
func (s *Store) CreateColorway(ctx context.Context, styleID int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert) (int, error) {
	// R9: verify the in-memory dictionary is current before this dictionary-dependent write (the color
	// name/SKU segment resolves color_code, the label reads country).
	if _, err := cache.EnsureDictionaryFresh(ctx, s.repFunc().Dictionary(), s.repFunc().Cache()); err != nil {
		return 0, fmt.Errorf("can't refresh dictionary before colourway create: %w", err)
	}
	// CreateColorway always mints a DRAFT (below); a DRAFT may carry zero or partial prices, so only the
	// always-on per-price checks run here. Required-currency COMPLETENESS is enforced later, on the
	// →ACTIVE edge (PublishColorway / unhide, lifecycle.go) — a price that IS supplied is still validated.
	if err := validateColorwayPrices(prices); err != nil {
		return 0, fmt.Errorf("price validation failed: %w", err)
	}
	var colorwayID int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// The style must already exist; a colourway attaches to it (R4 — no synthetic style from the
		// colourway payload, unlike the legacy AddProduct).
		style, err := storeutil.QueryNamedOne[struct {
			N int `db:"n"`
		}](ctx, rep.DB(), `SELECT COUNT(*) AS n FROM tech_card WHERE id = :id`, map[string]any{"id": styleID})
		if err != nil {
			return fmt.Errorf("check style %d: %w", styleID, err)
		}
		if style.N == 0 {
			return fmt.Errorf("style %d not found: %w", styleID, sql.ErrNoRows)
		}
		// UNIQUE(style_id, color_code) (R1): reject a duplicate colour with a clean precondition error
		// rather than surfacing a raw driver constraint violation.
		dup, err := storeutil.QueryNamedOne[struct {
			N int `db:"n"`
		}](ctx, rep.DB(), `SELECT COUNT(*) AS n FROM product WHERE style_id = :sid AND color_code = :cc`,
			map[string]any{"sid": styleID, "cc": prd.ProductBodyInsert.ColorCode})
		if err != nil {
			return fmt.Errorf("check colour uniqueness (style %d): %w", styleID, err)
		}
		if dup.N > 0 {
			return entity.ErrColorwayColorExists
		}
		// Default an unset/negative sale percentage to 0 (matches the legacy create path).
		if !prd.ProductBodyInsert.SalePercentage.Valid || prd.ProductBodyInsert.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.ProductBodyInsert.SalePercentage = decimal.NullDecimal{Valid: true, Decimal: decimal.NewFromFloat(0)}
		}
		// DRAFT: no SKU is minted (base SKU is built at publish), no variants, no chart, no style facts.
		colorwayID, err = insertProduct(ctx, rep.DB(), prd, styleID, int(entity.ColorwayStatusDraft))
		if err != nil {
			return fmt.Errorf("can't insert colourway: %w", err)
		}
		if err := insertProductTranslations(ctx, rep.DB(), colorwayID, prd.Translations); err != nil {
			return fmt.Errorf("can't insert colourway translations: %w", err)
		}
		if err := insertMedia(ctx, rep.DB(), mediaIDs, colorwayID); err != nil {
			return fmt.Errorf("can't insert colourway media: %w", err)
		}
		if _, err := insertTags(ctx, rep.DB(), tags, colorwayID); err != nil {
			return fmt.Errorf("can't insert colourway tags: %w", err)
		}
		if err := insertProductPrices(ctx, rep.DB(), colorwayID, prices); err != nil {
			return fmt.Errorf("can't insert colourway prices: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return colorwayID, nil
}

// UpdateColorway patches a colourway's own fields under an optimistic guard on its style's shared
// tech_card.lock_version (R2/R4). It updates ONLY colourway-owned data (the merch row, translations,
// media, tags, prices) and re-mints the SKU so a colour change is reflected while the SKU is unlocked
// (a no-op on a frozen colourway). It never touches style facts, variants, stock or the size chart. A
// stale expected version (or a concurrent bump) is entity.ErrTechCardConflict (ABORTED); an absent
// colourway is sql.ErrNoRows (NOT_FOUND). Lifecycle is never changed here (R6). Returns the new
// shared lock_version.
func (s *Store) UpdateColorway(ctx context.Context, colorwayID, expectedVersion int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert) (int, error) {
	if _, err := cache.EnsureDictionaryFresh(ctx, s.repFunc().Dictionary(), s.repFunc().Cache()); err != nil {
		return 0, fmt.Errorf("can't refresh dictionary before colourway update: %w", err)
	}
	// Sparse update (update_mask presence semantics): an empty translations/media/tags/prices slice
	// means "leave unchanged", so an admin panel loading and re-sending the full colourway is the norm
	// and a partial payload never wipes data. The scalar merch row is always full-replaced.
	// Per-price validity is always enforced on any supplied price. The required-currency COMPLETENESS
	// gate, however, is skipped for a DRAFT (it may hold partial prices) and applied only to an
	// already-published colourway — decided inside the tx below against the persisted lifecycle_status,
	// so a live/hidden colourway can never have its complete price set replaced with a partial one.
	if len(prices) > 0 {
		if err := validateColorwayPrices(prices); err != nil {
			return 0, fmt.Errorf("price validation failed: %w", err)
		}
	}
	var newLockVersion int
	err := s.txFunc(ctx, func(ctx context.Context, rep dependency.Repository) error {
		// The colourway's optimistic version is its style's shared tech_card.lock_version (R2/R4).
		cur, err := storeutil.QueryNamedOne[struct {
			StyleID         int   `db:"style_id"`
			LockVersion     int   `db:"lock_version"`
			LifecycleStatus uint8 `db:"lifecycle_status"`
		}](ctx, rep.DB(),
			`SELECT p.style_id, p.lifecycle_status, t.lock_version FROM product p JOIN tech_card t ON t.id = p.style_id WHERE p.id = :id`,
			map[string]any{"id": colorwayID})
		if err != nil {
			return err // sql.ErrNoRows -> NOT_FOUND upstream
		}
		if cur.LockVersion != expectedVersion {
			return entity.ErrTechCardConflict
		}
		if !prd.ProductBodyInsert.SalePercentage.Valid || prd.ProductBodyInsert.SalePercentage.Decimal.LessThan(decimal.Zero) {
			prd.ProductBodyInsert.SalePercentage = decimal.NullDecimal{Valid: true, Decimal: decimal.NewFromFloat(0)}
		}
		// Colourway-owned columns only — never style facts, variants or the chart.
		if err := updateColorwayRow(ctx, rep.DB(), prd, colorwayID); err != nil {
			return fmt.Errorf("can't update colourway %d: %w", colorwayID, err)
		}
		if len(prd.Translations) > 0 {
			if err := insertProductTranslations(ctx, rep.DB(), colorwayID, prd.Translations); err != nil {
				return fmt.Errorf("can't update colourway %d translations: %w", colorwayID, err)
			}
		}
		if len(mediaIDs) > 0 {
			if err := updateProductMedia(ctx, rep.DB(), colorwayID, mediaIDs); err != nil {
				return fmt.Errorf("can't update colourway %d media: %w", colorwayID, err)
			}
		}
		if len(tags) > 0 {
			if err := updateProductTags(ctx, rep.DB(), colorwayID, tags); err != nil {
				return fmt.Errorf("can't update colourway %d tags: %w", colorwayID, err)
			}
		}
		if len(prices) > 0 {
			// A published (non-DRAFT) colourway's price set must stay complete: refuse a partial
			// replacement that would drop a required currency from a live/hidden colourway. A DRAFT is
			// exempt — its completeness is enforced when it is published (→ACTIVE edge).
			if entity.ColorwayStatus(cur.LifecycleStatus) != entity.ColorwayStatusDraft {
				if err := validateRequiredCurrenciesPresent(prices); err != nil {
					return fmt.Errorf("price validation failed: %w", err)
				}
			}
			if err := upsertProductPrices(ctx, rep.DB(), colorwayID, prices); err != nil {
				return fmt.Errorf("can't update colourway %d prices: %w", colorwayID, err)
			}
		}
		// Re-mint so a colour change is reflected in the base/variant SKUs while unlocked (no-op frozen).
		if err := MintProductSKUs(ctx, rep.DB(), colorwayID); err != nil {
			return fmt.Errorf("can't re-mint colourway %d SKUs: %w", colorwayID, err)
		}
		// Bump the shared optimistic lock under the guard, so a concurrent style/colourway edit holding
		// the old version is rejected (a colourway edit is a mutation of the style aggregate).
		rows, err := storeutil.ExecNamedRows(ctx, rep.DB(),
			`UPDATE tech_card SET lock_version = lock_version + 1 WHERE id = :id AND lock_version = :expected`,
			map[string]any{"id": cur.StyleID, "expected": expectedVersion})
		if err != nil {
			return fmt.Errorf("bump colourway %d lock: %w", colorwayID, err)
		}
		if rows == 0 {
			return entity.ErrTechCardConflict
		}
		newLockVersion = expectedVersion + 1
		return nil
	})
	if err != nil {
		return 0, err
	}
	return newLockVersion, nil
}
