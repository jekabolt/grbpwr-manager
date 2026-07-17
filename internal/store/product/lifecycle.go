package product

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// PublishColorway, HideColorway, UnhideColorway and ArchiveColorway are the ONLY ways a colourway's
// lifecycle_status changes (contract decision R6) — a colourway save never touches it. Each validates
// the edge through the entity state machine (entity.NextColorwayStatus) and applies it under an
// optimistic guard on the current value, so a concurrent transition is rejected (RowsAffected != 1)
// rather than silently lost. Publish additionally enforces the sellable-colourway preconditions.
func (s *Store) PublishColorway(ctx context.Context, colorwayID int) error {
	return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionPublish)
}

// HideColorway takes an ACTIVE colourway off the storefront (ACTIVE -> HIDDEN); it stays admin-visible.
func (s *Store) HideColorway(ctx context.Context, colorwayID int) error {
	return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionHide)
}

// UnhideColorway returns a HIDDEN colourway to the storefront (HIDDEN -> ACTIVE).
func (s *Store) UnhideColorway(ctx context.Context, colorwayID int) error {
	return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionUnhide)
}

// ArchiveColorway retires a colourway (ACTIVE|HIDDEN -> ARCHIVED) and stamps the archival audit time.
// It does not check order references — the storefront/admin layer decides whether an
// archived-with-orders colourway is allowed; the SKU stays frozen and readable.
func (s *Store) ArchiveColorway(ctx context.Context, colorwayID int) error {
	return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionArchive)
}

// TransitionColorwayToHidden moves a colourway to HIDDEN via the single legal edge from its current
// state: hide (ACTIVE -> HIDDEN) when it is live, or restore/unarchive (ARCHIVED -> HIDDEN, clearing the
// deleted_at tombstone) when it is archived. It is the admin store entry point for the "hide / unarchive"
// action wired to TransitionColorwayStatus targeting HIDDEN. Any other source state is rejected by the
// entity state machine (e.g. DRAFT can only publish; HIDDEN has no self-edge), fail-closed. The current
// status is a hint for edge selection only — transitionColorwayLifecycle re-reads it under the optimistic
// guard, so a concurrent change is rejected rather than mis-applied.
func (s *Store) TransitionColorwayToHidden(ctx context.Context, colorwayID int) error {
	cur, err := loadColorwayLifecycle(ctx, s.DB, colorwayID)
	if err != nil {
		return err
	}
	if cur == entity.ColorwayStatusArchived {
		return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionRestore)
	}
	return s.transitionColorwayLifecycle(ctx, colorwayID, entity.ColorwayTransitionHide)
}

func loadColorwayLifecycle(ctx context.Context, db dependency.DB, colorwayID int) (entity.ColorwayStatus, error) {
	row, err := storeutil.QueryNamedOne[struct {
		Status uint8 `db:"lifecycle_status"`
	}](ctx, db, `SELECT lifecycle_status FROM product WHERE id = :id`, map[string]any{"id": colorwayID})
	if err != nil {
		return entity.ColorwayStatusUnknown, fmt.Errorf("load colourway %d lifecycle: %w", colorwayID, err)
	}
	return entity.ColorwayStatus(row.Status), nil
}

func (s *Store) transitionColorwayLifecycle(ctx context.Context, colorwayID int, t entity.ColorwayTransition) error {
	cur, err := loadColorwayLifecycle(ctx, s.DB, colorwayID)
	if err != nil {
		return err
	}
	next, err := entity.NextColorwayStatus(cur, t)
	if err != nil {
		return fmt.Errorf("colourway %d: %w", colorwayID, err)
	}
	if t == entity.ColorwayTransitionPublish {
		// Publish must guarantee its own base+variant SKUs: CreateColorway never mints and
		// CreateVariant's mint is best-effort (the base isn't built yet at that point), so a
		// colourway published straight after Create+CreateVariant (no UpdateColorway in between)
		// would otherwise fail the "valid SKU" preconditions below. Mint FIRST so the precondition
		// check that follows sees the freshly-minted state (R6).
		if err := MintProductSKUs(ctx, s.DB, colorwayID); err != nil {
			return fmt.Errorf("mint colourway %d SKUs before publish: %w", colorwayID, err)
		}
		if err := checkColorwayPublishPreconditions(ctx, s.DB, colorwayID); err != nil {
			return err
		}
	}
	// The →ACTIVE edge — publish (DRAFT->ACTIVE) or unhide (HIDDEN->ACTIVE) — is the SINGLE point that
	// enforces required-currency completeness. The create/update write path deliberately lets a DRAFT
	// carry partial prices; a colourway must never become publicly sellable without a valid price in
	// every required currency. Per-price validity of those prices was already checked when they were
	// written, so only completeness is (re-)verified here, against the persisted price rows.
	if next == entity.ColorwayStatusActive {
		if err := checkColorwayRequiredCurrencies(ctx, s.DB, colorwayID); err != nil {
			return err
		}
	}
	// Side effects on the audit stamps: publish records first publication; archive records retirement;
	// restore (unarchive-to-hidden) clears the archival tombstone so the row is no longer soft-deleted.
	extra := ""
	switch t {
	case entity.ColorwayTransitionPublish:
		extra = ", published_at = COALESCE(published_at, NOW())"
	case entity.ColorwayTransitionArchive:
		extra = ", deleted_at = COALESCE(deleted_at, NOW())"
	case entity.ColorwayTransitionRestore:
		extra = ", deleted_at = NULL"
	}
	rows, err := storeutil.ExecNamedRows(ctx, s.DB,
		`UPDATE product SET lifecycle_status = :next`+extra+` WHERE id = :id AND lifecycle_status = :cur`,
		map[string]any{"next": uint8(next), "cur": uint8(cur), "id": colorwayID})
	if err != nil {
		return fmt.Errorf("apply %s to colourway %d: %w", t, colorwayID, err)
	}
	if rows != 1 {
		return fmt.Errorf("colourway %d lifecycle changed concurrently (expected %s); retry", colorwayID, cur)
	}
	return nil
}

// publishReadiness aggregates the sellable-colourway signals a DRAFT must satisfy before it can go
// ACTIVE (R6). It is one query so the whole checklist is evaluated together.
type publishReadiness struct {
	BaseSKUValid        bool           `db:"base_sku_valid"`
	ColorCode           string         `db:"color_code"`
	CountryOfOrigin     string         `db:"country_of_origin"`
	SeasonCode          sql.NullString `db:"season_code"`
	SeasonYear          sql.NullInt32  `db:"season_year"`
	ModelNo             sql.NullInt32  `db:"model_no"`
	ValidVariants       int            `db:"valid_variants"`
	PriceCount          int            `db:"price_count"`
	DefaultTranslations int            `db:"default_translations"`
}

// checkColorwayPublishPreconditions enforces R6's DRAFT->ACTIVE rules: the colourway must be a fully
// identified, sellable unit — a built base SKU, at least one variant with a valid SKU, a complete
// sellable style (sku_season + model_no), a dictionary colour, a country, at least one price and a
// default-language translation. All misses are collected so the operator sees the whole checklist.
//
// Deliberately NOT gated here (documented deviation from the R6 checklist): customs (hs_code /
// customs_description) is optional by design — it is only required for cross-border shipments and is
// enforced at label build time (0127), so requiring it at publish would wrongly block EU-only
// colourways. Media presence is guaranteed structurally (product.thumbnail_id is NOT NULL).
func checkColorwayPublishPreconditions(ctx context.Context, db dependency.DB, colorwayID int) error {
	r, err := storeutil.QueryNamedOne[publishReadiness](ctx, db, `
		SELECT
		  REGEXP_LIKE(COALESCE(p.sku, ''), :base_pattern, 'c') AS base_sku_valid,
		  p.color_code       AS color_code,
		  p.country_of_origin AS country_of_origin,
		  sty.season_code    AS season_code,
		  sty.season_year    AS season_year,
		  sty.model_no       AS model_no,
		  (SELECT COUNT(*) FROM product_size ps
		     WHERE ps.product_id = p.id AND ps.sku IS NOT NULL
		       AND REGEXP_LIKE(ps.sku, :variant_pattern, 'c')) AS valid_variants,
		  (SELECT COUNT(*) FROM product_price pp WHERE pp.product_id = p.id) AS price_count,
		  (SELECT COUNT(*) FROM product_translation pt JOIN language l ON l.id = pt.language_id
		     WHERE pt.product_id = p.id AND l.is_default = TRUE) AS default_translations
		FROM product p JOIN tech_card sty ON sty.id = p.style_id
		WHERE p.id = :id`,
		map[string]any{
			"id":              colorwayID,
			"base_pattern":    baseSKUInvariantPattern,
			"variant_pattern": variantSKUInvariantPattern,
		})
	if err != nil {
		return fmt.Errorf("load publish preconditions for colourway %d: %w", colorwayID, err)
	}

	var missing []string
	if !r.BaseSKUValid {
		missing = append(missing, "base SKU is not built")
	}
	if r.ValidVariants < 1 {
		missing = append(missing, "no variant has a valid SKU")
	}
	if !r.SeasonCode.Valid || !r.SeasonYear.Valid {
		missing = append(missing, "style has no complete sku_season")
	}
	if !r.ModelNo.Valid {
		missing = append(missing, "style has no model number")
	}
	if err := validateColorCode(r.ColorCode); err != nil {
		missing = append(missing, "colour code is not a valid dictionary code")
	}
	if strings.TrimSpace(r.CountryOfOrigin) == "" {
		missing = append(missing, "country of origin is empty")
	}
	if r.PriceCount < 1 {
		missing = append(missing, "no price is set")
	}
	if r.DefaultTranslations < 1 {
		missing = append(missing, "no default-language translation")
	}
	if len(missing) > 0 {
		return fmt.Errorf("cannot publish colourway %d: %s", colorwayID, strings.Join(missing, "; "))
	}
	return nil
}

// checkColorwayRequiredCurrencies verifies a colourway's PERSISTED prices cover every required
// currency (currency.MissingRequired). It is the completeness gate on the →ACTIVE edge
// (transitionColorwayLifecycle): publish (DRAFT->ACTIVE) and unhide (HIDDEN->ACTIVE) both route through
// it, so a colourway can never go live missing a required currency. Amount-level validity (positive,
// above minimum) was enforced at write time and is not re-checked here.
func checkColorwayRequiredCurrencies(ctx context.Context, db dependency.DB, colorwayID int) error {
	rows, err := storeutil.QueryListNamed[struct {
		Currency string `db:"currency"`
	}](ctx, db, `SELECT currency FROM product_price WHERE product_id = :id`, map[string]any{"id": colorwayID})
	if err != nil {
		return fmt.Errorf("load colourway %d prices: %w", colorwayID, err)
	}
	provided := make(map[string]bool, len(rows))
	for _, r := range rows {
		provided[strings.ToUpper(r.Currency)] = true
	}
	if missing := currency.MissingRequired(provided); len(missing) > 0 {
		return fmt.Errorf("cannot activate colourway %d: missing required currencies: %s", colorwayID, strings.Join(missing, ", "))
	}
	return nil
}
