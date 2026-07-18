package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// insertSeasonedTestStyle creates a bare tech_card with a complete, mutually-consistent sku_season
// (season_code/season_year/season) — the tech_card_season_atomic CHECK (0146) rejects a partial
// pair, so all three must be set together, as in TestRelinkDraftColorway's target style. This gives
// CreateColorway/UpdateColorway/UpdateStyle a style that can actually mint a base SKU. Registers its
// own cleanup (runs after the test's product-deleting defers/cleanups, t.Cleanup being LIFO, so the
// style_id FK child rows are gone first).
func insertSeasonedTestStyle(ctx context.Context, t *testing.T, tag, seasonCode, season string, seasonYear int) int {
	t.Helper()
	res, err := testDB.ExecContext(ctx, `INSERT INTO tech_card (style_number, name, season_code, season_year, season)
		VALUES (CONCAT(?, '-', UUID_SHORT()), ?, ?, ?, ?)`, tag, tag, seasonCode, seasonYear, season)
	require.NoError(t, err)
	id64, err := res.LastInsertId()
	require.NoError(t, err)
	id := int(id64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })
	return id
}

// commonWriteTestFixtures loads the process-wide dictionary/const cache (idempotent — every
// integration test in this package that touches products does this once) and returns a ready media
// id, the default-language id (publish preconditions require a DEFAULT-language translation) and the
// full required-currency price set. Mirrors the boilerplate in TestRelinkDraftColorway /
// TestStyleSizeChartFullReplace.
func commonWriteTestFixtures(ctx context.Context, t *testing.T, s *MYSQLStore) (mediaID, langID int, prices []entity.ColorwayPriceInsert) {
	t.Helper()
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT id FROM language WHERE is_default = 1 ORDER BY id LIMIT 1`).Scan(&langID))

	mediaID, err = s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 100, FullSizeHeight: 100,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 10, ThumbnailHeight: 10,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 50, CompressedHeight: 50,
		// BlurHash must be set (not left NULL): a later product-detail read (getProductDetails) scans
		// blur_hash into a non-nullable Go string and NULL-scan-fails otherwise — the same pre-existing
		// hand-built-media-fixture quirk documented in the sibling order_sku_snapshot_integration_test.go.
		BlurHash: sql.NullString{String: "LEHV6nWB2yk8pyo0adR*.7kCMdnj", Valid: true},
	})
	require.NoError(t, err)

	for _, c := range currency.RequiredCurrencies() {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}
	if len(prices) == 0 {
		prices = append(prices, entity.ColorwayPriceInsert{Currency: "EUR", Price: decimal.NewFromInt(10000)})
	}
	return mediaID, langID, prices
}

// newColorwayInsert builds a minimal-but-valid *entity.ColorwayInsert for CreateColorway/
// UpdateColorway — a colour dictionary code (must be seeded, e.g. BLK/WHT/RED, 0130), a default
// translation and the required price set. Season/TargetGender/TopCategoryId are set but, per
// CreateColorway/UpdateColorway's doc comments, are colourway-insert scaffolding only — neither
// writer touches style facts, so they never reach tech_card.
func newColorwayInsert(colorCode, colorName, name string, mediaID, langID int, prices []entity.ColorwayPriceInsert) *entity.ColorwayInsert {
	return &entity.ColorwayInsert{
		ProductBodyInsert: entity.ColorwayBodyInsert{
			Brand: "ACME", Color: colorName, ColorCode: colorCode, CountryOfOrigin: "IT",
			TopCategoryId: 1, TargetGender: entity.Unisex, Season: entity.SeasonSS,
		},
		ThumbnailMediaID: mediaID,
		Translations:     []entity.ColorwayTranslationInsert{{LanguageId: langID, Name: name, Description: "d"}},
		Prices:           prices,
	}
}

// TestCreateColorway is the acceptance test for the R2/R4 write-decomposition CreateColorway: it
// attaches a DRAFT, unminted colourway to an EXISTING style, writing only colourway-owned data, and
// enforces UNIQUE(style_id, color_code) (R1) and an existing style (NOT_FOUND otherwise).
func TestCreateColorway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCW1", "SS", "SS26", 2026)

	prd := newColorwayInsert("BLK", "black", "TCW1-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	var lifecycleStatus, gotStyleID int
	var sku sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status, sku, style_id FROM product WHERE id = ?`, colorwayID).
		Scan(&lifecycleStatus, &sku, &gotStyleID))
	require.Equal(t, int(entity.ColorwayStatusDraft), lifecycleStatus, "CreateColorway must mint a DRAFT, never ACTIVE")
	require.False(t, sku.Valid, "CreateColorway must write NULL sku — publish mints; NULL (not '') so two unminted drafts never collide on UNIQUE (T-E-5 finding)")
	require.Equal(t, styleID, gotStyleID)

	// UNIQUE(style_id, color_code) (R1): a duplicate colour for the same style is refused.
	dup := newColorwayInsert("BLK", "black", "TCW1-BLACK-2", mediaID, langID, prices)
	_, err = s.Products().CreateColorway(ctx, styleID, dup, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.ErrorIs(t, err, entity.ErrColorwayColorExists)

	// Mint the first colourway's SKU before creating a second one. product.sku is `VARCHAR(255) NOT
	// NULL UNIQUE` (0001) and CreateColorway inserts the literal '' (by design — "no SKU is minted"
	// until publish); a second still-unminted draft ANYWHERE in the catalogue (not just this style)
	// therefore collides on the empty string (MySQL 1062), since '' is a real, non-NULL, colliding
	// value under a UNIQUE index — unlike the legacy AddProduct, which mints inside the same
	// transaction so '' is never actually committed. This is a store-level gap in the new DRAFT
	// semantics (T-E blocker, see 90-verification.md), not a fixture issue, so it is routed around
	// here rather than "fixed": minting #1 first is a legitimate real sequence too (an admin filling
	// out colourway #1 before adding #2), and it isolates this test back onto R1's actual contract —
	// uniqueness scoped to (style_id, color_code), not "at most one unminted draft system-wide".
	var styleLV int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&styleLV))
	firstMint := newColorwayInsert("BLK", "black", "TCW1-BLACK", mediaID, langID, prices)
	_, err = s.Products().UpdateColorway(ctx, colorwayID, styleLV, firstMint, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)

	// A different colour on the same style is fine — proves the guard is scoped to (style, colour).
	second := newColorwayInsert("WHT", "white", "TCW1-WHITE", mediaID, langID, prices)
	secondID, err := s.Products().CreateColorway(ctx, styleID, second, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", secondID) }()

	// An unknown style is sql.ErrNoRows (NOT_FOUND upstream) — checked before the colour-uniqueness query.
	unknown := newColorwayInsert("BLK", "black", "TCW1-UNKNOWN", mediaID, langID, prices)
	_, err = s.Products().CreateColorway(ctx, 999999999, unknown, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestCreateColorwayPublishPreconditionsAndUpdateVersionGuard walks a CreateColorway DRAFT through
// the R6 publish preconditions and UpdateColorway's optimistic guard on the shared
// tech_card.lock_version: publish is refused before a base/variant SKU exists, UpdateColorway mints
// them and bumps the shared lock, a stale expected version is a conflict, an absent colourway is
// NOT_FOUND, and publish then succeeds once every precondition is met.
func TestCreateColorwayPublishPreconditionsAndUpdateVersionGuard(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCW2", "SS", "SS26", 2026)
	// insertSeasonedTestStyle deliberately leaves brand/top_category_id/target_gender/collection NULL
	// (all nullable, no default); this test reads the colourway back through getProductDetails (below),
	// whose productQueryResult scans those columns into non-nullable Go fields (string/int/GenderEnum),
	// so they must be populated here (pre-existing fixture gap, unrelated to this fix, only surfaced now
	// that this test performs that read). category id 1 is the seeded root category (0001_initial_setup.sql),
	// used the same way by every other test that sets TopCategoryId.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET brand = 'ACME', top_category_id = 1, target_gender = 'unisex', collection = 'core' WHERE id = ?", styleID)
	require.NoError(t, err)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	prd := newColorwayInsert("BLK", "black", "TCW2-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	// A DRAFT with no built base SKU and no variant cannot be published (R6 preconditions).
	err = s.Products().PublishColorway(ctx, colorwayID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "cannot publish")

	// Lay out a size — the mint is best-effort here and no-ops (no base SKU yet to derive from).
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	var v0 int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v0))

	// UpdateColorway under the correct (shared) version mints the base + variant SKU and bumps the lock.
	upd := newColorwayInsert("BLK", "black", "TCW2-BLACK-UPD", mediaID, langID, prices)
	newVersion, err := s.Products().UpdateColorway(ctx, colorwayID, v0, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	require.Equal(t, v0+1, newVersion)

	var sku string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product WHERE id = ?`, colorwayID).Scan(&sku))
	require.Regexp(t, `^SS26-[0-9]{5}-BLK$`, sku)
	var variantSKU sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, colorwayID, sizeID).Scan(&variantSKU))
	require.True(t, variantSKU.Valid)
	require.Regexp(t, `^SS26-[0-9]{5}-BLK-[0-9]{2}$`, variantSKU.String)

	// A stale expected version is a conflict — the shared lock already moved.
	_, err = s.Products().UpdateColorway(ctx, colorwayID, v0, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)

	// An absent colourway is sql.ErrNoRows.
	_, err = s.Products().UpdateColorway(ctx, 999999999, v0, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// Every precondition is now satisfied: publish succeeds and stamps published_at.
	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID))
	var lifecycleStatus int
	var publishedAt sql.NullTime
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status, published_at FROM product WHERE id = ?`, colorwayID).
		Scan(&lifecycleStatus, &publishedAt))
	require.Equal(t, int(entity.ColorwayStatusActive), lifecycleStatus)
	require.True(t, publishedAt.Valid)

	// Entity-level plumbing (fix: PublishColorwayResponse.colorway.published_at): the fresh read back
	// through the store must also carry published_at, not just the raw DB row.
	full, err := s.Products().GetProductByIdShowHidden(ctx, colorwayID, false)
	require.NoError(t, err)
	require.True(t, full.Product.PublishedAt.Valid, "entity.Colorway.PublishedAt must be populated from the fresh read")

	// DRAFT -> ACTIVE is a one-way trip through this command; publishing again is refused.
	require.Error(t, s.Products().PublishColorway(ctx, colorwayID))
}

// TestUpdateStyleRemintAndFrozenSiblingRefusal is the acceptance test for UpdateStyle (R4/§14.7): the
// sole writer of style-level catalogue facts, optimistically locked on the shared
// tech_card.lock_version. A season (SKU-fact) change re-mints every sibling colourway's SKU in place;
// it is refused (entity.ErrStyleFrozenSiblings) atomically when any sibling is SKU-frozen, but a
// non-SKU-fact patch still succeeds even with a frozen sibling present (skuFactsChanged gates the
// frozen check, not the whole write).
func TestUpdateStyleRemintAndFrozenSiblingRefusal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TUS1", "SS", "SS26", 2026)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	// mintedColorway creates a colourway on styleID, lays out one size and UpdateColorway's it under
	// the style's current shared lock so both the base and variant SKU are minted — ready to observe
	// UpdateStyle's re-mint.
	mintedColorway := func(colorCode, colorName, tag string) int {
		prd := newColorwayInsert(colorCode, colorName, tag, mediaID, langID, prices)
		id, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
		_, err = s.Products().CreateVariant(ctx, id, sizeID)
		require.NoError(t, err)
		var v int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v))
		upd := newColorwayInsert(colorCode, colorName, tag, mediaID, langID, prices)
		_, err = s.Products().UpdateColorway(ctx, id, v, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
		require.NoError(t, err)
		return id
	}
	readSKU := func(colorwayID int) string {
		var sku string
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product WHERE id = ?`, colorwayID).Scan(&sku))
		return sku
	}

	colorwayA := mintedColorway("BLK", "black", "TUS1-A")
	colorwayB := mintedColorway("WHT", "white", "TUS1-B")

	skuA0, skuB0 := readSKU(colorwayA), readSKU(colorwayB)
	require.True(t, strings.HasPrefix(skuA0, "SS26-"), "sku=%s", skuA0)
	require.True(t, strings.HasPrefix(skuB0, "SS26-"), "sku=%s", skuB0)

	var v0 int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v0))

	basePatch := entity.StylePatch{
		Brand: "ACME", Season: entity.SeasonSS, Collection: "core",
		TargetGender: entity.Unisex, TopCategoryId: 1,
	}

	// A season change re-mints every (unfrozen) sibling's SKU under the shared lock.
	fwPatch := basePatch
	fwPatch.Season = entity.SeasonFW
	newVersion, err := s.Products().UpdateStyle(ctx, styleID, v0, fwPatch, nil)
	require.NoError(t, err)
	require.Equal(t, v0+1, newVersion)

	skuA1, skuB1 := readSKU(colorwayA), readSKU(colorwayB)
	require.True(t, strings.HasPrefix(skuA1, "FW26-"), "colourway A must be re-minted under the new season: %s", skuA1)
	require.True(t, strings.HasPrefix(skuB1, "FW26-"), "colourway B must be re-minted under the new season: %s", skuB1)
	require.Equal(t, skuA0[4:], skuA1[4:], "only the season segment changes — model/colour are untouched")
	require.Equal(t, skuB0[4:], skuB1[4:], "only the season segment changes — model/colour are untouched")

	// A stale expected version is a conflict.
	_, err = s.Products().UpdateStyle(ctx, styleID, v0, fwPatch, nil)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)

	// Freeze colourway A (simulates order/label history: sku_locked_at set).
	_, err = testDB.ExecContext(ctx, `UPDATE product SET sku_locked_at = NOW() WHERE id = ?`, colorwayA)
	require.NoError(t, err)

	// Another season change is refused outright — a frozen sibling is never silently skipped.
	pfPatch := basePatch
	pfPatch.Season = entity.SeasonPF
	_, err = s.Products().UpdateStyle(ctx, styleID, v0+1, pfPatch, nil)
	require.ErrorIs(t, err, entity.ErrStyleFrozenSiblings)

	// Refused atomically: the shared lock did not move and no sibling's SKU changed.
	var vAfterRefusal int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&vAfterRefusal))
	require.Equal(t, v0+1, vAfterRefusal)
	require.Equal(t, skuA1, readSKU(colorwayA))
	require.Equal(t, skuB1, readSKU(colorwayB))

	// A non-SKU-fact patch (season unchanged) still succeeds despite the frozen sibling.
	collectionPatch := basePatch
	collectionPatch.Season = entity.SeasonFW // unchanged from the style's current value
	collectionPatch.Collection = "core-v2"
	newVersion2, err := s.Products().UpdateStyle(ctx, styleID, v0+1, collectionPatch, nil)
	require.NoError(t, err)
	require.Equal(t, v0+2, newVersion2)
	var collection string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT collection FROM tech_card WHERE id = ?`, styleID).Scan(&collection))
	require.Equal(t, "core-v2", collection)

	// An unknown style is sql.ErrNoRows.
	_, err = s.Products().UpdateStyle(ctx, 999999999, 0, basePatch, nil)
	require.ErrorIs(t, err, sql.ErrNoRows)
}

// TestPublishColorwayMintsSKUsWithoutPriorUpdateColorway guards a beta-seed finding: a seeder had to
// insert a synthetic UpdateColorway call between CreateVariant and Publish only to get SKUs minted.
// CreateColorway never mints (its doc comment says so explicitly — "no SKU minted until publish") and
// CreateVariant's mint is best-effort and no-ops because the base isn't built yet, so a colourway
// published straight after Create -> CreateVariant x2 -> Publish (no UpdateColorway in between) used to
// fail checkColorwayPublishPreconditions outright. PublishColorway must guarantee its own base+variant
// SKUs by minting first and only then checking the R6 preconditions.
func TestPublishColorwayMintsSKUsWithoutPriorUpdateColorway(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TPCM", "SS", "SS26", 2026)
	var sizeID1, sizeID2 int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID1))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1 OFFSET 1`).Scan(&sizeID2))

	prd := newColorwayInsert("BLK", "black", "TPCM-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID1)
	require.NoError(t, err)
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID2)
	require.NoError(t, err)

	// No UpdateColorway call anywhere above — PublishColorway alone must mint the base + variant SKUs
	// (and, as a side effect, allocate the style's model_no) before it can satisfy R6's preconditions.
	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID))

	var sku string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT sku FROM product WHERE id = ?`, colorwayID).Scan(&sku))
	require.Regexp(t, `^SS26-[0-9]{5}-BLK$`, sku)

	var variantSKU1, variantSKU2 sql.NullString
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, colorwayID, sizeID1).Scan(&variantSKU1))
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT sku FROM product_size WHERE product_id = ? AND size_id = ?`, colorwayID, sizeID2).Scan(&variantSKU2))
	require.True(t, variantSKU1.Valid)
	require.True(t, variantSKU2.Valid)
	require.Regexp(t, `^SS26-[0-9]{5}-BLK-[0-9]{2}$`, variantSKU1.String)
	require.Regexp(t, `^SS26-[0-9]{5}-BLK-[0-9]{2}$`, variantSKU2.String)
}

// TestPublishColorwayResponsePublishedAtPopulated is the acceptance test for the fix to
// PublishColorwayResponse.colorway.published_at always coming back empty: it exercises the full
// DB -> entity -> proto chain (lifecycle.go's COALESCE(published_at, NOW()) -> entity.Colorway.
// PublishedAt -> dto.ConvertToPbProductFull) that the RPC response actually carries over the wire, not
// just the raw DB row. Uses the realistic Create -> CreateVariant -> Publish sequence (no UpdateColorway
// needed now that PublishColorway mints its own base+variant SKUs).
func TestPublishColorwayResponsePublishedAtPopulated(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TPAP", "SS", "SS26", 2026)
	// insertSeasonedTestStyle deliberately leaves brand/top_category_id/target_gender/collection NULL
	// (all nullable, no default); this test reads the colourway back through getProductDetails (below),
	// whose productQueryResult scans those columns into non-nullable Go fields (string/int/GenderEnum),
	// so they must be populated here (pre-existing fixture gap, unrelated to this fix, only surfaced now
	// that this test performs that read). category id 1 is the seeded root category (0001_initial_setup.sql),
	// used the same way by every other test that sets TopCategoryId.
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET brand = 'ACME', top_category_id = 1, target_gender = 'unisex', collection = 'core' WHERE id = ?", styleID)
	require.NoError(t, err)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	prd := newColorwayInsert("BLK", "black", "TPAP-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	// No UpdateColorway call — PublishColorway alone mints the base + variant SKUs it needs.
	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID))

	full, err := s.Products().GetProductByIdShowHidden(ctx, colorwayID, false)
	require.NoError(t, err)
	require.True(t, full.Product.PublishedAt.Valid)

	pbFull, err := dto.ConvertToPbProductFull(full)
	require.NoError(t, err)
	require.NotNil(t, pbFull.Colorway.PublishedAt, "PublishColorwayResponse.colorway.published_at must be populated, not left unset")
}

// TestColorwayActivationRequiresAllRequiredCurrencies locks the moved completeness gate (the P0 fix):
//
//	(a) a DRAFT is CREATED with an INCOMPLETE currency set and SUCCEEDS — the "all required currencies"
//	    check no longer runs at create/update; a draft may be partial;
//	(b) PUBLISHING that draft (the →ACTIVE edge) FAILS with the required-currency error while a required
//	    currency is missing, even though every OTHER publish precondition is satisfied (base+variant SKU,
//	    season, model_no, colour, country, price>=1, default translation — the exact fixture that makes
//	    TestPublishColorwayResponsePublishedAtPopulated's publish succeed, minus one currency);
//	(c) once the missing currency is supplied, publish SUCCEEDS and the colourway is ACTIVE.
//
// Together these prove the completeness gate sits on the →ACTIVE edge, not on create/update.
func TestColorwayActivationRequiresAllRequiredCurrencies(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, fullPrices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCWCUR", "SS", "SS26", 2026)
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET brand = 'ACME', top_category_id = 1, target_gender = 'unisex', collection = 'core' WHERE id = ?", styleID)
	require.NoError(t, err)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	// A partial required set: every required currency EXCEPT the last (PLN today), all amounts well above
	// their minimums so ONLY completeness — never per-price validity — can be what publish rejects.
	req := currency.RequiredCurrencies()
	require.Greater(t, len(req), 1)
	missingCur := req[len(req)-1]
	var partialPrices []entity.ColorwayPriceInsert
	for _, c := range req[:len(req)-1] {
		partialPrices = append(partialPrices, entity.ColorwayPriceInsert{Currency: c, Price: decimal.NewFromInt(10000)})
	}

	// (a) CreateColorway with an INCOMPLETE currency set succeeds and lands a DRAFT (pre-fix this failed
	// with "price validation failed: missing required currencies" — the P0 that blocked all creation).
	prd := newColorwayInsert("BLK", "black", "TCWCUR-BLACK", mediaID, langID, partialPrices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, partialPrices)
	require.NoError(t, err, "creating a DRAFT with a partial currency set must succeed (gate moved off create)")
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	var lifecycle, priceCount int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&lifecycle))
	require.Equal(t, int(entity.ColorwayStatusDraft), lifecycle)
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM product_price WHERE product_id = ?`, colorwayID).Scan(&priceCount))
	require.Equal(t, len(partialPrices), priceCount, "the partial set must persist as-is on a draft")

	// Lay out a variant so publish has a sellable size; PublishColorway mints the base+variant SKU (and
	// the style model_no) itself, so no UpdateColorway is needed to satisfy the SKU preconditions.
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	// (b) The →ACTIVE edge refuses to publish while a required currency is missing.
	err = s.Products().PublishColorway(ctx, colorwayID)
	require.Error(t, err, "publish must fail while a required currency is missing")
	require.Contains(t, err.Error(), "missing required currencies")
	require.Contains(t, err.Error(), missingCur)
	// The failed edge did not flip lifecycle — still DRAFT.
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&lifecycle))
	require.Equal(t, int(entity.ColorwayStatusDraft), lifecycle)

	// Supply the missing currency (now the complete required set) on the still-DRAFT colourway. This is a
	// price-changing UpdateColorway on a DRAFT — allowed, per-price validity still applies.
	var v0 int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&v0))
	updFull := newColorwayInsert("BLK", "black", "TCWCUR-BLACK", mediaID, langID, fullPrices)
	_, err = s.Products().UpdateColorway(ctx, colorwayID, v0, updFull, []int{mediaID}, []entity.ColorwayTagInsert{}, fullPrices)
	require.NoError(t, err)

	// (c) With every required currency present, the →ACTIVE edge admits the colourway.
	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID), "publish must succeed once all required currencies are present")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&lifecycle))
	require.Equal(t, int(entity.ColorwayStatusActive), lifecycle)
}

// migrationUpBody reads a migration file and returns its Up section with sql-migrate directive and
// comment lines stripped — the executable SQL only. Used to exercise a data-migration's real body
// against seeded rows (the migration itself ran at boot on empty data, a no-op).
func migrationUpBody(t *testing.T, filename string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("sql", filename))
	require.NoError(t, err)
	up, _, found := strings.Cut(string(body), "-- +migrate Down")
	require.True(t, found, "migration %s must have a Down section marker", filename)
	up = strings.TrimPrefix(strings.TrimSpace(up), "-- +migrate Up")
	var sqlLines []string
	for _, line := range strings.Split(up, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue // comment / directive line
		}
		sqlLines = append(sqlLines, line)
	}
	return strings.TrimSpace(strings.Join(sqlLines, "\n"))
}

// TestBackfillPLNProductPricesMigration validates the 0182 data backfill against seeded rows: a legacy
// ACTIVE colourway with an EUR-but-no-PLN price gets a PLN row mirroring EUR; a DRAFT with the same
// EUR-only price is left untouched (drafts may be partial); and re-running the backfill is idempotent
// (the NOT EXISTS guard makes a rerun a no-op). It executes the migration file's real Up SQL — the
// migration ran at boot on empty data, so its effect is only observable on rows created afterwards.
func TestBackfillPLNProductPricesMigration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, _ := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCWPLN", "SS", "SS26", 2026)

	// EUR-only price set — the pre-PLN legacy shape. CreateColorway lands a DRAFT and (post-fix) accepts
	// the partial set; the per-EUR amount is well above the EUR minimum.
	eurAmount := decimal.NewFromInt(12345)
	eurOnly := []entity.ColorwayPriceInsert{{Currency: "EUR", Price: eurAmount}}

	// A legacy ACTIVE colourway: created as a DRAFT with EUR-only, then flipped straight to ACTIVE by raw
	// UPDATE to simulate a row that predates PLN (the real gate would now refuse such a publish).
	activePrd := newColorwayInsert("BLK", "black", "TCWPLN-BLACK", mediaID, langID, eurOnly)
	activeID, err := s.Products().CreateColorway(ctx, styleID, activePrd, []int{mediaID}, []entity.ColorwayTagInsert{}, eurOnly)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", activeID) }()
	_, err = testDB.ExecContext(ctx, "UPDATE product SET lifecycle_status = ? WHERE id = ?", int(entity.ColorwayStatusActive), activeID)
	require.NoError(t, err)

	// A DRAFT colourway with the same EUR-only set — must NOT be backfilled.
	draftPrd := newColorwayInsert("WHT", "white", "TCWPLN-WHITE", mediaID, langID, eurOnly)
	draftID, err := s.Products().CreateColorway(ctx, styleID, draftPrd, []int{mediaID}, []entity.ColorwayTagInsert{}, eurOnly)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", draftID) }()

	backfill := migrationUpBody(t, "0182_backfill_pln_product_prices.sql")
	require.Contains(t, backfill, "INSERT INTO product_price")

	// Run the backfill.
	_, err = testDB.ExecContext(ctx, backfill)
	require.NoError(t, err)

	// The ACTIVE colourway now has a PLN row equal to its EUR price.
	var plnCount int
	var plnPrice decimal.Decimal
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(price), 0) FROM product_price WHERE product_id = ? AND currency = 'PLN'`, activeID).
		Scan(&plnCount, &plnPrice))
	require.Equal(t, 1, plnCount, "ACTIVE colourway with EUR-but-no-PLN must get a backfilled PLN row")
	require.True(t, eurAmount.Equal(plnPrice), "backfilled PLN price must mirror the EUR amount, got %s", plnPrice)

	// The DRAFT colourway is untouched.
	var draftPLN int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_price WHERE product_id = ? AND currency = 'PLN'`, draftID).Scan(&draftPLN))
	require.Equal(t, 0, draftPLN, "DRAFT colourway must NOT be backfilled (drafts may be partial)")

	// Idempotent: a rerun (NOT EXISTS guard) adds nothing.
	_, err = testDB.ExecContext(ctx, backfill)
	require.NoError(t, err)
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM product_price WHERE product_id = ? AND currency = 'PLN'`, activeID).Scan(&plnCount))
	require.Equal(t, 1, plnCount, "re-running the backfill must be a no-op (crash-idempotent)")
}
