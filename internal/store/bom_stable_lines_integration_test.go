package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestBomStableLinesUpsertDiff is the acceptance test for the BOM stable-lines root fix (WS3 / S2-S3,
// migration 0159): BOM lines are reconciled by line_key so a line's id survives edits (which is what
// lets downstream references be real FKs), editing/reordering does not churn ids, and a removed line
// is deleted. A full-replace would recreate ids on every save; this proves it no longer does.
func TestBomStableLinesUpsertDiff(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	bom := func(key, name string) entity.TechCardBomItem {
		return entity.TechCardBomItem{LineKey: key, Section: entity.TechCardBomSection("fabric"), Name: name}
	}
	card := func(items ...entity.TechCardBomItem) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "BOM Stable Style", Stage: entity.TechCardStageProto, StyleNumber: ns("BOM-STABLE-1"),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft, BomItems: items,
		}
	}

	tcID, err := T.AddTechCard(ctx, card(bom("K1", "Fabric A"), bom("K2", "Lining B")))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	// Read back: line_keys round-trip and each line has a stable id.
	c1, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c1.BomItems, 2)
	idByKey := map[string]int{}
	for _, b := range c1.BomItems {
		require.NotEmpty(t, b.LineKey, "line_key must round-trip")
		idByKey[b.LineKey] = b.Id
	}
	require.Contains(t, idByKey, "K1")
	require.Contains(t, idByKey, "K2")

	// Update: round-trip both keys, edit K1's name -> ids stay stable (upsert-diff, not full-replace).
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card(bom("K1", "Fabric A2"), bom("K2", "Lining B")), c1.LockVersion))
	c2, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c2.BomItems, 2)
	for _, b := range c2.BomItems {
		require.Equal(t, idByKey[b.LineKey], b.Id, "id must be stable across update for line_key %s", b.LineKey)
		if b.LineKey == "K1" {
			require.Equal(t, "Fabric A2", b.Name, "edit must persist in place")
		}
	}

	// Drop K2 (submit only K1) -> it is deleted; K1's id is unchanged.
	require.NoError(t, T.UpdateTechCard(ctx, tcID, card(bom("K1", "Fabric A2")), c2.LockVersion))
	c3, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, c3.BomItems, 1)
	require.Equal(t, "K1", c3.BomItems[0].LineKey)
	require.Equal(t, idByKey["K1"], c3.BomItems[0].Id, "surviving line keeps its id")
}
