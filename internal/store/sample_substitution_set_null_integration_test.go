package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/require"
)

// TestSampleSubstitutionSurvivesBomLineDelete is the acceptance test for P4-flyover M3
// (04-MAZE-FLYOVER.md): sample_substitution.bom_item_id was ON DELETE RESTRICT (0170), so a BOM line
// ever referenced by a dev-time substitution — even from a long-closed round — could not be removed
// from the BOM. Migration 0178 changes it to ON DELETE SET NULL: deleting the BOM line (via
// UpdateTechCard's upsert-diff, omitting the line from the payload) must now succeed, and the
// substitution must survive with bom_item_id NULLed while its original_material_id snapshot (Q2: the
// historical fact) stays intact.
func TestSampleSubstitutionSurvivesBomLineDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	matID, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "M3 Snapshot Fabric", Section: "fabric", CreatedBy: "tester", UpdatedBy: "tester",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM material WHERE id = ?", matID) })

	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		Name: "M3 Substitution Style", Stage: entity.TechCardStageProto, StyleNumber: ns("M3-SUBST-1"),
		MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{LineKey: "M3BOM1", Section: entity.BomSectionFabric, Name: "Main Fabric"},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testDB.ExecContext(bg, "DELETE FROM sample_substitution WHERE original_material_id = ?", matID)
		_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	card, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, card.BomItems, 1)
	bomItemID := card.BomItems[0].Id
	require.NotZero(t, bomItemID)

	sampleID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusPlanned,
		FabricSource: entity.SampleFabricSample, CreatedBy: "tester", UpdatedBy: "tester",
	})
	require.NoError(t, err)

	subID, err := s.Samples().AddSampleSubstitution(ctx, &entity.SampleSubstitutionInsert{
		SampleId:           sampleID,
		BomItemId:          sql.NullInt32{Int32: int32(bomItemID), Valid: true},
		OriginalMaterialId: sql.NullInt32{Int32: int32(matID), Valid: true},
		Reason:             sql.NullString{String: "out of stock", Valid: true},
		CreatedBy:          "tester",
	})
	require.NoError(t, err)

	// Before 0178 this delete would 1451 on fk_subst_bom_item (RESTRICT). UpdateTechCard's upsert-diff
	// omits the BOM line from the payload, which is exactly the "BOM row deleted while a substitution
	// still references it" scenario M3 targets.
	err = T.UpdateTechCard(ctx, tcID, &entity.TechCardInsert{
		Name: card.Name, Stage: card.Stage, StyleNumber: card.StyleNumber,
		MeasurementUnit: card.MeasurementUnit, ApprovalState: card.ApprovalState,
		UpdatedBy: "tester",
		BomItems:  nil, // the line the substitution references is dropped
	}, card.LockVersion)
	require.NoError(t, err, "deleting a BOM row with a live substitution must succeed now (SET NULL, not RESTRICT)")

	// The BOM line is really gone.
	cardAfter, err := T.GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Empty(t, cardAfter.BomItems)

	// The substitution survives with bom_item_id NULLed and its material snapshot intact.
	subs, err := s.Samples().ListSampleSubstitutions(ctx, sampleID)
	require.NoError(t, err)
	require.Len(t, subs, 1)
	require.Equal(t, subID, subs[0].Id)
	require.False(t, subs[0].BomItemId.Valid, "bom_item_id must be NULLed, not blocking the delete")
	require.True(t, subs[0].OriginalMaterialId.Valid, "original_material_id snapshot must survive")
	require.Equal(t, int32(matID), subs[0].OriginalMaterialId.Int32)
	require.Equal(t, "out of stock", subs[0].Reason.String)
}
