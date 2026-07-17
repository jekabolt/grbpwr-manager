package store

import (
	"context"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestGetTechCardByIdNullLabDipStatus guards against a 500 on GetTechCardById once a colourway is
// linked to its style: insertProduct (the INSERT behind CreateColorway) never sets
// product.lab_dip_status, and that column (added VARCHAR(16) NULL, no DEFAULT, by
// 0151_colorway_domain_merge.sql) therefore starts life as SQL NULL on every freshly-created
// colourway. enrichMaterials used to scan it straight into entity.TechCardColorway.LabDipStatus — a
// plain string-kind type, not sql.NullString — so the scan failed with "converting NULL to string is
// unsupported". This exercises the real CreateColorway -> GetTechCardById path (not a synthetic NULL
// insert) to prove the production path actually triggers it, and locks in the
// COALESCE(c.lab_dip_status, 'pending') fallback.
func TestGetTechCardByIdNullLabDipStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	mediaID, langID, prices := commonWriteTestFixtures(ctx, t, s)
	styleID := insertSeasonedTestStyle(ctx, t, "TCNL", "SS", "SS26", 2026)

	prd := newColorwayInsert("BLK", "black", "TCNL-BLACK", mediaID, langID, prices)
	colorwayID, err := s.Products().CreateColorway(ctx, styleID, prd, []int{mediaID}, []entity.ColorwayTagInsert{}, prices)
	require.NoError(t, err)
	defer func() { _, _ = testDB.ExecContext(ctx, "DELETE FROM product WHERE id = ?", colorwayID) }()

	card, err := s.TechCards().GetTechCardById(ctx, styleID)
	require.NoError(t, err)
	require.Len(t, card.Colorways, 1)
	require.Equal(t, entity.TechCardLabDipStatus("pending"), card.Colorways[0].LabDipStatus)
}
