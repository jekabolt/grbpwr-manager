package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// This file is the acceptance suite for WS5/S10: category -> permitted size-system(s) mapping
// (migration 0175, internal/entity.ResolveSizeSystemPolicy). It exercises the two real write paths
// server-side validation was added to -- CreateVariant (a colourway's SKU per size) and
// AddTechCard/UpdateTechCard's SizeIds (the STYLE's own size range, tech_card_size, the literal
// size-picker gap the S10 bug report named) -- against a REAL category+size_system read from the DB,
// through the SAME dictionary cache the admin picker's GetDictionary RPC serves. Requires a live
// MySQL (TestMain, see mysql_test.go) — not runnable in this sandbox, but must compile.

// categoryIDByName resolves a top-level (level_id=1) category id by NAME (stable seed content,
// 0001_initial_setup.sql), never assumed insertion-order id.
func categoryIDByName(ctx context.Context, t *testing.T, name string) int {
	t.Helper()
	var id int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM category WHERE name = ? AND level_id = 1`, name).Scan(&id))
	return id
}

// assignStyleCategory sets a style's top_category_id via UpdateStyle (R4/§14.7, the sole writer) under
// its current shared lock, keeping season identical to insertSeasonedTestStyle's "SS"/2026 so the
// SKU-fact re-mint path (irrelevant here) never triggers. Returns the lock_version AFTER the update.
func assignStyleCategory(ctx context.Context, t *testing.T, s *MYSQLStore, styleID, categoryID int) int {
	t.Helper()
	var v0 int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v0))
	patch := entity.StylePatch{
		Brand: "ACME", Season: entity.SeasonSS, Collection: "core",
		TargetGender: entity.Unisex, TopCategoryId: categoryID,
	}
	v1, err := s.Products().UpdateStyle(ctx, styleID, v0, patch)
	require.NoError(t, err)
	return v1
}

// requireCategorySizeSystemViolation asserts err is a field-tagged *entity.ValidationError (the S10
// contract: apierr.Invalid at the RPC layer turns this into an InvalidArgument + BadRequest
// FieldViolation) naming field and containing want in its Reason.
func requireCategorySizeSystemViolation(t *testing.T, err error, field, want string) {
	t.Helper()
	require.Error(t, err)
	var ve *entity.ValidationError
	require.True(t, errors.As(err, &ve), "expected a field-tagged *entity.ValidationError, got %T: %v", err, err)
	require.Equal(t, field, ve.Field)
	require.Contains(t, ve.Reason, want)
}

// TestCategorySizeSystem_CreateVariant is the acceptance test for CreateVariant's S10 guard: a
// colourway's requested size must belong to a system permitted for the OWNING STYLE's category, not
// just any size in the dictionary.
func TestCategorySizeSystem_CreateVariant(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)

	var apparelSizeID, shoeSizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' AND name = 'm'`).Scan(&apparelSizeID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'shoe' ORDER BY sku_ord LIMIT 1`).Scan(&shoeSizeID))
	shoesCategoryID := categoryIDByName(ctx, t, "shoes")

	styleID := insertSeasonedTestStyle(ctx, t, "WS5CV", "SS", "SS26", 2026)
	assignStyleCategory(ctx, t, s, styleID, shoesCategoryID)

	prd := newColorwayInsert("BLK", "black", "WS5CV-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	// An apparel size is rejected: "shoes" only permits the shoe system.
	_, err = s.Products().CreateVariant(ctx, colorwayID, apparelSizeID)
	requireCategorySizeSystemViolation(t, err, "size_id", "size system apparel not allowed for category shoes")
	require.Contains(t, err.Error(), "allowed: shoe")

	// The correct system succeeds.
	_, err = s.Products().CreateVariant(ctx, colorwayID, shoeSizeID)
	require.NoError(t, err)

	// A colourway with NO variant survived from the rejected call (it must not have partially applied).
	var n int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_size WHERE product_id = ?`, colorwayID).Scan(&n))
	require.Equal(t, 1, n, "only the accepted (shoe) variant should exist")
}

// TestCategorySizeSystem_StyleSizeRange is the acceptance test for the STYLE's own size range
// (tech_card_size, AddTechCard/UpdateTechCard's SizeIds) -- the literal size-picker gap S10 reported:
// the admin picker offered every size regardless of the style's category, with no backend guard.
func TestCategorySizeSystem_StyleSizeRange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	// The mapping rides the same dictionary payload as categories/sizes (S10/WS5 scope item 3).
	require.NotEmpty(t, di.CategorySizeSystems, "category_size_system seed (0175) must be non-empty")

	var apparelSizeID, shoeSizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' AND name = 'm'`).Scan(&apparelSizeID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'shoe' ORDER BY sku_ord LIMIT 1`).Scan(&shoeSizeID))
	shoesCategoryID := categoryIDByName(ctx, t, "shoes")

	tc := &entity.TechCardInsert{
		StyleNumber:     sql.NullString{String: "WS5SR", Valid: true},
		Name:            "ws5 size range",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
		SizeIds:         []int{apparelSizeID},
	}
	id, err := s.TechCards().AddTechCard(ctx, tc)
	require.NoError(t, err, "a style with no category assigned yet is unrestricted -- nothing to validate against")
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })

	var sizeRangeCount int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_size WHERE tech_card_id = ?`, id).Scan(&sizeRangeCount))
	require.Equal(t, 1, sizeRangeCount)

	v1 := assignStyleCategory(ctx, t, s, id, shoesCategoryID)
	// UpdateStyle never touches tech_card_size (it writes catalogue facts only) -- the range survives.
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_size WHERE tech_card_id = ?`, id).Scan(&sizeRangeCount))
	require.Equal(t, 1, sizeRangeCount)

	// UpdateTechCard re-submitting the now-mismatched apparel size range is rejected; the reject rolls
	// back the full-replace atomically (the existing size range is untouched, not left half-deleted).
	tc.SizeIds = []int{apparelSizeID}
	err = s.TechCards().UpdateTechCard(ctx, id, tc, v1)
	requireCategorySizeSystemViolation(t, err, "size_ids[0]", "size system apparel not allowed for category shoes")
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_size WHERE tech_card_id = ?`, id).Scan(&sizeRangeCount))
	require.Equal(t, 1, sizeRangeCount, "a rejected full-replace must not partially apply")

	// The correct system succeeds.
	tc.SizeIds = []int{shoeSizeID}
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, id, tc, v1))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tech_card_size WHERE tech_card_id = ? AND size_id = ?`, id, shoeSizeID).Scan(&sizeRangeCount))
	require.Equal(t, 1, sizeRangeCount)
}

// TestCategorySizeSystem_OSFallback covers the "category without a grid" fallback (bags/objects,
// intentionally seeded with zero category_size_system rows, 0175): CreateVariant restricts such a
// style to the single one-size 'os' entry, not "anything goes".
func TestCategorySizeSystem_OSFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)

	var osSizeID, apparelSizeID int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM size WHERE name = 'os'`).Scan(&osSizeID))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' AND name = 'm'`).Scan(&apparelSizeID))
	bagsCategoryID := categoryIDByName(ctx, t, "bags")

	styleID := insertSeasonedTestStyle(ctx, t, "WS5OS", "SS", "SS26", 2026)
	assignStyleCategory(ctx, t, s, styleID, bagsCategoryID)

	prd := newColorwayInsert("BLK", "black", "WS5OS-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	// bags has zero category_size_system rows -> OS-fallback: 'os' is allowed, 'm' is not.
	_, err = s.Products().CreateVariant(ctx, colorwayID, osSizeID)
	require.NoError(t, err)

	_, err = s.Products().CreateVariant(ctx, colorwayID, apparelSizeID)
	requireCategorySizeSystemViolation(t, err, "size_id", "allowed: os (one-size)")
}
