package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestCreateColorwayDraftWithoutThumbnail is the P0 acceptance test for the colourway-create FK fix.
// A DRAFT colourway with NO thumbnail must:
//  1. be creatable — insertProduct now binds thumbnail_id = SQL NULL (not 0), so the nullable media FK
//     product_ibfk_4 (thumbnail_id -> media.id) is satisfied instead of aborting the whole INSERT;
//  2. read back cleanly with an absent (empty) thumbnail — the admin detail read LEFT JOINs media and
//     scans the nullable columns, so the draft is returned rather than dropped or NULL-scan-failing;
//  3. be REFUSED activation until a thumbnail is set — the new →ACTIVE gate (checkColorwayHasThumbnail);
//  4. activate once a valid thumbnail is set.
func TestCreateColorwayDraftWithoutThumbnail(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, fullPrices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCTN1", "SS", "SS26", 2026)
	// Populate the style-level display columns getProductDetails scans into non-nullable Go fields
	// (brand/target_gender/collection/top_category_id). insertSeasonedTestStyle only seeds the season;
	// unrelated to the thumbnail path, but a real style always carries these — set them so the admin
	// read exercises the thumbnail-NULL path rather than tripping on an unrelated NULL scan.
	_, err = testDB.ExecContext(ctx,
		`UPDATE tech_card SET brand = 'ACME', target_gender = 'unisex', collection = '', top_category_id = 1 WHERE id = ?`, styleID)
	require.NoError(t, err)
	var sizeID int
	require.NoError(t, testDB.QueryRowContext(ctx,
		`SELECT id FROM size WHERE sku_system = 'apparel' ORDER BY sku_ord LIMIT 1`).Scan(&sizeID))

	// 1) CREATE a DRAFT with NO thumbnail (ThumbnailMediaID = 0). Before the fix this bound
	//    thumbnail_id = 0 and aborted on FK product_ibfk_4; now it binds NULL and succeeds.
	prd := newColorwayInsert("BLK", "black", "TCTN1-BLK", mediaID, langID, fullPrices)
	prd.ThumbnailMediaID = 0
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, fullPrices)
	require.NoError(t, err, "creating a DRAFT colourway with no thumbnail must succeed (no FK error)")
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", colorwayID)
	})

	// thumbnail_id must persist as SQL NULL, never 0.
	var thumb sql.NullInt32
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT thumbnail_id FROM product WHERE id = ?`, colorwayID).Scan(&thumb))
	require.False(t, thumb.Valid, "a no-thumbnail DRAFT must store thumbnail_id NULL, not 0")

	// 2) READ path handles a NULL thumbnail gracefully: the admin detail read returns the draft with an
	//    empty/absent thumbnail (Id 0), not an error and not a dropped row.
	full, err := s.Products().GetProductByIdShowHidden(ctx, colorwayID, false)
	require.NoError(t, err, "admin read of a no-thumbnail draft must succeed (LEFT JOIN media, nullable scan)")
	require.NotNil(t, full.Product)
	require.Equal(t, 0, full.Product.ProductDisplay.Thumbnail.Id, "an absent thumbnail reads back as an empty MediaFull")

	// A variant is required so the OTHER publish preconditions pass and the publish reaches the thumbnail gate.
	_, err = s.Products().CreateVariant(ctx, colorwayID, sizeID)
	require.NoError(t, err)

	// 3) ACTIVATION is REFUSED while there is no thumbnail — the new →ACTIVE completeness gate.
	err = s.Products().PublishColorway(ctx, colorwayID)
	require.Error(t, err, "publishing a colourway with no thumbnail must be refused")
	require.Contains(t, err.Error(), "thumbnail", "the refusal must name the missing thumbnail")
	var st uint8
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&st))
	require.Equal(t, uint8(entity.ColorwayStatusDraft), st, "a rejected publish must leave the colourway DRAFT")

	// 4) Set a valid thumbnail via UpdateColorway (newColorwayInsert sets ThumbnailMediaID = mediaID),
	//    then publish SUCCEEDS and the colourway goes ACTIVE.
	var lockV int
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, styleID).Scan(&lockV))
	upd := newColorwayInsert("BLK", "black", "TCTN1-BLK", mediaID, langID, fullPrices)
	_, err = s.Products().UpdateColorway(ctx, colorwayID, lockV, upd, []int{mediaID}, []entity.ColorwayTagInsert{}, fullPrices)
	require.NoError(t, err)

	require.NoError(t, s.Products().PublishColorway(ctx, colorwayID), "publish must succeed once a valid thumbnail is set")
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lifecycle_status FROM product WHERE id = ?`, colorwayID).Scan(&st))
	require.Equal(t, uint8(entity.ColorwayStatusActive), st, "a complete colourway with a thumbnail must publish to ACTIVE")
}
