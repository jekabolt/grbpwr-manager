package store

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestStageRegressionAllowedWhenOnlyArchivedColorways is the acceptance test for FIX 3 (P2): the
// tech-card stage-regression guard (guardTechCardStageRegression) counts a style's colourways to decide
// whether the stage may move backward, but it must NOT count ARCHIVED (soft-deleted,
// product.lifecycle_status = 4) colourways — those are retired work, not live downstream artifacts.
// With the `AND lifecycle_status <> 4` filter, a style whose only colourway is archived can regress its
// stage (proto → idea); a style with a NON-archived (e.g. DRAFT) colourway still cannot.
func TestStageRegressionAllowedWhenOnlyArchivedColorways(t *testing.T) {
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

	// mkProtoCard creates a sellable DRAFT tech card at stage `proto` (ordinal 1) so a move to `idea`
	// (ordinal 0) is a regression the guard evaluates.
	mkProtoCard := func(name, styleNo string) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(styleNo),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: entity.TechCardPurposeSellable,
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })
		return id
	}

	// addColorway inserts a colourway (product) under the style at the given lifecycle_status. It is
	// frozen (sku_locked_at set) so the re-mint UpdateTechCard runs on a successful regression is a
	// no-op — the test targets the guard, not SKU minting.
	addColorway := func(styleID int, lifecycle entity.ColorwayStatus) int {
		res, err := testDB.ExecContext(ctx, `INSERT INTO product
			(sku, color, color_code, color_hex, country_of_origin, thumbnail_id, style_id, lifecycle_status, sku_locked_at, deleted_at)
			VALUES (?, 'c', 'BLK', '#000000', 'IT', ?, ?, ?, NOW(), ?)`,
			fmt.Sprintf("SR-CW-%d-%d", styleID, lifecycle), mediaID, styleID, uint8(lifecycle),
			sql.NullTime{Time: time.Now(), Valid: lifecycle == entity.ColorwayStatusArchived})
		require.NoError(t, err)
		id64, err := res.LastInsertId()
		require.NoError(t, err)
		id := int(id64)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM product WHERE id = ?", id) })
		return id
	}

	regressToIdea := func(cardID int) error {
		var lockV int
		require.NoError(t, testDB.QueryRowContext(ctx, `SELECT lock_version FROM tech_card WHERE id = ?`, cardID).Scan(&lockV))
		return T.UpdateTechCard(ctx, cardID, &entity.TechCardInsert{
			Name: "regressed", Stage: entity.TechCardStageIdea, StyleNumber: ns(fmt.Sprintf("SR-%d", cardID)),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: entity.TechCardPurposeSellable,
		}, lockV)
	}

	// --- Positive: only an ARCHIVED colourway -> regression allowed ---
	archivedOnly := mkProtoCard("Archived-only style", "SR-ARCH-ONLY")
	addColorway(archivedOnly, entity.ColorwayStatusArchived)
	require.NoError(t, regressToIdea(archivedOnly),
		"a style whose only colourway is archived must be allowed to regress its stage")
	var stage string
	require.NoError(t, testDB.QueryRowContext(ctx, `SELECT stage FROM tech_card WHERE id = ?`, archivedOnly).Scan(&stage))
	require.Equal(t, string(entity.TechCardStageIdea), stage, "the stage must actually have regressed to idea")

	// --- Negative control: a non-archived (DRAFT) colourway -> regression still blocked ---
	withDraft := mkProtoCard("Draft-colourway style", "SR-DRAFT")
	addColorway(withDraft, entity.ColorwayStatusDraft)
	err = regressToIdea(withDraft)
	require.Error(t, err, "a live/non-archived colourway must still block stage regression")
	var ve *entity.ValidationError
	require.ErrorAs(t, err, &ve, "the block must be a field-tagged ValidationError")
	require.Equal(t, "stage", ve.Field)
	require.Contains(t, ve.Error(), "colourway")
}
