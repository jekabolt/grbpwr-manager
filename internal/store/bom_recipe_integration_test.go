package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestUpdateColorwayRecipeRoundTrip is the contract test for the restored recipe write-path
// (WS3 / S2-S3; closes the A3.4 "silent no-op" — ColorwayDevelopmentInsert.usages was accepted on the
// wire but never written). A colourway recipe written via UpdateColorwayRecipe persists and reads
// back, referencing the style BOM by stable line_key resolved to a real bom_item_id FK. A stale
// shared version is rejected.
func TestUpdateColorwayRecipeRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Recipe Style", Stage: entity.TechCardStageProto, StyleNumber: ns("RCP-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "RK1", Section: entity.TechCardBomSection("fabric"), Name: "Main Fabric"},
			{LineKey: "RK2", Section: entity.TechCardBomSection("thread"), Name: "Thread"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	// A colourway is a product under the style (post-R1 merge).
	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("RCP-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, err := res.LastInsertId()
	require.NoError(t, err)
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	// Write a recipe referencing the fabric BOM line by its stable line_key.
	newVer, err := T.UpdateColorwayRecipe(ctx, cwID, card.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: "RK1", Placement: ns("outer"), Color: ns("black"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
	})
	require.NoError(t, err)
	require.Equal(t, card.LockVersion+1, newVer, "recipe write bumps the shared lock")

	// Read back: the recipe persisted and resolved to a real bom_item_id (was a silent no-op before).
	card2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	var found *entity.TechCardColorwayUsage
	for i := range card2.Colorways {
		if card2.Colorways[i].Id == cwID {
			require.Len(t, card2.Colorways[i].Usages, 1)
			found = &card2.Colorways[i].Usages[0]
		}
	}
	require.NotNil(t, found, "recipe usage must read back")
	require.True(t, found.BomItemId.Valid, "usage resolved line_key -> real bom_item_id")
	require.Equal(t, "outer", found.Placement.String)

	// A stale shared version is rejected (optimistic lock).
	_, err = T.UpdateColorwayRecipe(ctx, cwID, card.LockVersion, nil)
	require.ErrorIs(t, err, entity.ErrTechCardConflict)
}

// TestColorwayRecipeReadPath is the H1 fix's contract test. Review finding H1: `techCardUsagesToPb`
// (dto/techcard.go) had zero call sites in the whole repo (confirmed by grep) — the recipe write-path
// (UpdateColorwayRecipe, restored above) was write-only. Because the write is a full-replace (DELETE
// all usages + re-INSERT), a UI that saves without ever being able to load the current recipe first
// would silently blank whatever it didn't resubmit. This proves BOTH restored read surfaces
// (01-DOMAIN-MODEL §2.3: recipe is colourway-owned, so GetColorwayByID is the minimum that must
// return it; the tech-card constructor view must show it too) round-trip a written recipe with
// matching field values, including the derived per-line money resolved against the style's own BOM.
func TestColorwayRecipeReadPath(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	mediaID, err := s.Media().AddMedia(ctx, &entity.MediaItem{
		FullSizeMediaURL: "https://x/f.jpg", FullSizeWidth: 1, FullSizeHeight: 1,
		ThumbnailMediaURL: "https://x/t.jpg", ThumbnailWidth: 1, ThumbnailHeight: 1,
		CompressedMediaURL: "https://x/c.jpg", CompressedWidth: 1, CompressedHeight: 1,
	})
	require.NoError(t, err)

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "Recipe Read Style", Stage: entity.TechCardStageProto, StyleNumber: ns("RCPR-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "RRK1", Section: entity.TechCardBomSection("fabric"), Name: "Shell Fabric",
				UnitPrice: decimal.NewNullDecimal(decimal.RequireFromString("10.00")), Currency: ns("EUR")},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	res, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'BLK', '#000000', 'US', ?, ?)`, fmt.Sprintf("RCPR-CW-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID64, err := res.LastInsertId()
	require.NoError(t, err)
	cwID := int(cwID64)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID) })

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)

	_, err = T.UpdateColorwayRecipe(ctx, cwID, card.LockVersion, []entity.TechCardColorwayUsage{
		{BomLineKey: "RRK1", Placement: ns("outer"), Color: ns("black"), Pantone: ns("19-4005"),
			Consumption: decimal.NewNullDecimal(decimal.RequireFromString("1.5"))},
	})
	require.NoError(t, err)

	// --- surface 1: GetTechCardById's constructor view (TechCard.colorways[].usages) ---
	card2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	pbTC := dto.ConvertEntityTechCardToPb(card2, dto.CostingFx{Base: "EUR"})
	var ref *pb_common.AdminColorwayRef
	for _, c := range pbTC.Colorways {
		if int(c.ColorwayId) == cwID {
			ref = c
		}
	}
	require.NotNil(t, ref, "GetTechCardById must list the colourway")
	require.Len(t, ref.Usages, 1, "GetTechCardById's colourway ref must carry the written recipe (H1)")
	u := ref.Usages[0]
	require.Equal(t, "outer", u.Placement)
	require.Equal(t, "black", u.Color)
	require.Equal(t, "19-4005", u.Pantone)
	require.Equal(t, "1.5", u.Consumption.GetValue())
	require.Greater(t, u.BomItemId, int64(0), "bom_line_key must resolve to a real bom_item_id")
	require.Equal(t, "15", u.LineTotal.GetValue(), "1.5 consumption x 10.00 unit_price, with costing:read")

	// --- surface 2: the dedicated recipe read (store side of GetColorwayByID) ---
	recipe, err := T.GetColorwayRecipe(ctx, cwID)
	require.NoError(t, err)
	require.Len(t, recipe, 1, "GetColorwayRecipe must return the written recipe (H1, GetColorwayByID's read side)")
	require.Equal(t, "outer", recipe[0].Placement.String)
	require.True(t, recipe[0].BomItemId.Valid)
	require.Equal(t, u.BomItemId, recipe[0].BomItemId.Int64, "both read surfaces resolve the same bom_item_id")

	// A colourway with no recipe reads back empty, not an error.
	res2, err := testDB.ExecContext(ctx, `INSERT INTO product
		(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id)
		VALUES (?, 'c', 'WHT', '#ffffff', 'US', ?, ?)`, fmt.Sprintf("RCPR-CW2-%d", tcID), mediaID, tcID)
	require.NoError(t, err)
	cwID2_64, err := res2.LastInsertId()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", cwID2_64) })
	emptyRecipe, err := T.GetColorwayRecipe(ctx, int(cwID2_64))
	require.NoError(t, err)
	require.Empty(t, emptyRecipe)
}
