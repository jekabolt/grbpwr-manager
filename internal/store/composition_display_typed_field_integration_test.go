package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestColorwayCompositionDisplay_TypedFieldNotJSONOverload is the M1 fix's contract test. Review
// finding M1: styleCompositionSelect (internal/store/product/query.go) used to COALESCE the structured
// style_composition rows into the SAME `composition` string as a JSON array the moment any existed for
// a style — a data-triggered (not version-gated) overload of composition's wire shape, reachable from
// the public storefront read paths (getProductDetails/buildQuery/GetProductsByTag/GetProductsByIds/
// GetLowStockProducts) and the admin colourway read. This proves the fix: `composition` stays legacy
// plain text in BOTH cases (no structural data, and after structural data appears), and the new typed
// `composition_entries` carries the structured rows exclusively — with data and without.
func TestColorwayCompositionDisplay_TypedFieldNotJSONOverload(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	P := s.Products()

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
		// The colourway/product read scans blur_hash into a non-nullable Go string (unlike the
		// entity's own sql.NullString), so it must be non-NULL in the DB.
		BlurHash: sql.NullString{String: "", Valid: true},
	})
	require.NoError(t, err)

	// seedSpineStyle (mysql_test.go) sets brand/target_gender/top_category_id directly by raw SQL — the
	// colourway/product read (getProductDetails) scans these style fields into non-nullable Go types, so
	// they must be non-NULL. It registers its own cleanup; style_composition rows cascade-delete with
	// the tech_card row (FOREIGN KEY ... ON DELETE CASCADE), so no separate cleanup is needed for them.
	tcID := seedSpineStyle(ctx, t, "M1-DISP")

	// tech_card.composition is a native MySQL JSON column (migration 0139) — legacy free text is stored
	// as a JSON string scalar (the only shape a plain "100% Cotton" entry can take in a JSON column).
	// The WIRE value, however, is the plain text: the read path JSON_UNQUOTEs the scalar
	// (styleCompositionSelect) and the write path JSON_QUOTEs it back (styleFieldsSet) — writing wire
	// text raw into the column is invalid JSON and fails with MySQL 3140 (found live by the beta A–L
	// acceptance run, step B.8). The M1 bug this test pins was COALESCE-ing a DIFFERENT JSON shape (an
	// array of {fiber_code,name,percent} objects) into this same field once structural data existed.
	const legacyCompositionStored = `"100% Cotton"` // on-disk JSON scalar
	const legacyComposition = `100% Cotton`         // wire/plain form every read must return
	// collection/season_code/season_year are non-nullable on the colourway/product read's scan struct
	// (unlike GetTechCardById's `SELECT *`, which tolerates NULL via sql.Null* fields) — seedSpineStyle
	// leaves them unset, so getProductDetails would fail to scan the row without these.
	_, err = testDB.ExecContext(ctx,
		"UPDATE tech_card SET composition = ?, collection = '', season_code = 'SS', season_year = 2026, season = 'SS26' WHERE id = ?",
		legacyCompositionStored, tcID)
	require.NoError(t, err)

	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?, 2)`, fmt.Sprintf("M1-DISP-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, err := res.LastInsertId()
	require.NoError(t, err)
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	// --- without structural data: composition is the plain legacy text; composition_entries is empty. ---
	pf, err := P.GetProductByIdShowHidden(ctx, cwID, false)
	require.NoError(t, err)
	require.True(t, pf.Product.ProductDisplay.ProductBody.ProductBodyInsert.Composition.Valid)
	require.Equal(t, legacyComposition, pf.Product.ProductDisplay.ProductBody.ProductBodyInsert.Composition.String)
	require.Empty(t, pf.Product.ProductDisplay.ProductBody.ProductBodyInsert.CompositionEntries)

	sf := dto.StorefrontColorwayFromFull(pf)
	require.Equal(t, legacyComposition, sf.Display.Composition, "storefront composition is legacy plain text with no structural data")
	require.Empty(t, sf.Display.CompositionEntries)

	// Also exercise the paged/list projection (GetProductsByIds — the flagged PUBLIC storefront path;
	// buildQuery/GetProductsByTag/GetLowStockProducts share the exact same SQL constants).
	byIds, err := P.GetProductsByIds(ctx, []int{cwID})
	require.NoError(t, err)
	require.Len(t, byIds, 1)
	require.Equal(t, legacyComposition, byIds[0].ProductDisplay.ProductBody.ProductBodyInsert.Composition.String)
	require.Empty(t, byIds[0].ProductDisplay.ProductBody.ProductBodyInsert.CompositionEntries)

	// --- the store WRITE path: plain wire text must round-trip byte-identical through
	// UpdateStyle (JSON_QUOTE on write) and the reads above (JSON_UNQUOTE) — the beta A–L run proved
	// writing it raw 500s (MySQL 3140: invalid JSON for column tech_card.composition). ---
	var lockVer int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT lock_version FROM tech_card WHERE id = ?", tcID).Scan(&lockVer))
	const apiComposition = "70% cotton, 30% viscose"
	_, err = P.UpdateStyle(ctx, tcID, lockVer, entity.StylePatch{
		TopCategoryId: 1, Season: entity.SeasonEnum("SS"), TargetGender: entity.GenderEnum("unisex"),
		Composition: sql.NullString{String: apiComposition, Valid: true},
	}, nil)
	require.NoError(t, err, "UpdateStyle must accept plain wire text for composition (JSON_QUOTE, not raw)")
	pfAPI, err := P.GetProductByIdShowHidden(ctx, cwID, false)
	require.NoError(t, err)
	require.Equal(t, apiComposition, pfAPI.Product.ProductDisplay.ProductBody.ProductBodyInsert.Composition.String,
		"plain wire text round-trips byte-identical through store write+read")
	// restore the raw legacy scalar so the structural-data half below still exercises the stored form
	_, err = testDB.ExecContext(ctx, "UPDATE tech_card SET composition = ? WHERE id = ?", legacyCompositionStored, tcID)
	require.NoError(t, err)

	// --- with structural data: composition MUST STILL be the plain legacy text (not JSON); the
	// structured rows go ONLY into composition_entries. ---
	_, err = testDB.ExecContext(ctx,
		"INSERT INTO style_composition (tech_card_id, fiber_code, percent, source) VALUES (?, 'COT', 60.00, 'manual'), (?, 'POL', 40.00, 'manual')",
		tcID, tcID)
	require.NoError(t, err)

	pf2, err := P.GetProductByIdShowHidden(ctx, cwID, false)
	require.NoError(t, err)
	require.Equal(t, legacyComposition, pf2.Product.ProductDisplay.ProductBody.ProductBodyInsert.Composition.String,
		"composition must stay legacy plain text even after style_composition gains rows (M1: JSON-in-string overload removed)")
	entries := pf2.Product.ProductDisplay.ProductBody.ProductBodyInsert.CompositionEntries
	require.Len(t, entries, 2)
	byCode := map[string]entity.CompositionEntry{}
	for _, e := range entries {
		byCode[e.FiberCode] = e
	}
	require.Equal(t, "60", byCode["COT"].Percent.String())
	require.Equal(t, "40", byCode["POL"].Percent.String())

	byIds2, err := P.GetProductsByIds(ctx, []int{cwID})
	require.NoError(t, err)
	require.Len(t, byIds2, 1)
	require.Equal(t, legacyComposition, byIds2[0].ProductDisplay.ProductBody.ProductBodyInsert.Composition.String,
		"list path composition must also stay legacy plain text (M1)")
	require.Len(t, byIds2[0].ProductDisplay.ProductBody.ProductBodyInsert.CompositionEntries, 2)

	sf2 := dto.StorefrontColorwayFromFull(pf2)
	require.Equal(t, legacyComposition, sf2.Display.Composition, "storefront composition string is STILL legacy plain text (M1)")
	require.Len(t, sf2.Display.CompositionEntries, 2, "storefront composition_entries carries the structured rows (M1)")
	var gotCOT bool
	for _, ce := range sf2.Display.CompositionEntries {
		if ce.FiberCode == "COT" {
			gotCOT = true
			require.Equal(t, "60", ce.Percent.GetValue())
		}
	}
	require.True(t, gotCOT)
}
