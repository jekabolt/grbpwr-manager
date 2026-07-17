package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestStyleAssembly is the acceptance test for the style-assembly bill (WS7, §2.8, migration 0174): a
// full-replace write per style, read-back resolution of the component card's name + aux_subtype +
// output material, an empty upsert clearing the bill, and the two write-guards — components must be
// auxiliary cards, and (component,size) pairs must be unique.
func TestStyleAssembly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()
	T := s.TechCards()

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	mkCard := func(name, styleNo string, purpose entity.TechCardPurpose, aux sql.NullString) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: name, Stage: entity.TechCardStageProto, StyleNumber: ns(styleNo),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: purpose, AuxSubtype: aux,
		})
		require.NoError(t, err)
		return id
	}

	styleID := mkCard("WS7 Garment", "WS7-ASM-GARMENT", entity.TechCardPurposeSellable, sql.NullString{})
	compID := mkCard("WS7 Woven Brand Label", "WS7-ASM-AUX", entity.TechCardPurposeAuxiliary,
		ns(string(entity.AuxSubtypeBrandLabel)))
	sellableComp := mkCard("WS7 Not Auxiliary", "WS7-ASM-SELL", entity.TechCardPurposeSellable, sql.NullString{})

	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testDB.ExecContext(bg, "DELETE FROM style_assembly WHERE style_id = ?", styleID)
		for _, id := range []int{styleID, compID, sellableComp} {
			_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", id)
		}
	})

	// Write one assembly line, then read it back resolved.
	require.NoError(t, T.UpsertStyleAssembly(ctx, styleID, []entity.StyleAssemblyInsert{{
		ComponentTechCardId: compID,
		Qty:                 decimal.RequireFromString("2"),
		PrintNote:           ns("Woven logo, black on white"),
		PositionNote:        ns("Center back neck"),
		Active:              true,
	}}, "tester"))

	lines, err := T.ListStyleAssembly(ctx, styleID)
	require.NoError(t, err)
	require.Len(t, lines, 1)
	require.Equal(t, compID, lines[0].ComponentTechCardId)
	require.Equal(t, "WS7 Woven Brand Label", lines[0].ComponentName)
	require.True(t, lines[0].ComponentAuxSubtype.Valid)
	require.Equal(t, string(entity.AuxSubtypeBrandLabel), lines[0].ComponentAuxSubtype.String)
	require.True(t, lines[0].Qty.Equal(decimal.RequireFromString("2")))
	require.Equal(t, "Woven logo, black on white", lines[0].PrintNote.String)
	require.True(t, lines[0].Active)

	// Full-replace with an empty list clears the bill.
	require.NoError(t, T.UpsertStyleAssembly(ctx, styleID, nil, "tester"))
	lines, err = T.ListStyleAssembly(ctx, styleID)
	require.NoError(t, err)
	require.Empty(t, lines)

	// Guard 1: a non-auxiliary component is rejected (field-tagged).
	err = T.UpsertStyleAssembly(ctx, styleID, []entity.StyleAssemblyInsert{{
		ComponentTechCardId: sellableComp, Qty: decimal.RequireFromString("1"), Active: true,
	}}, "tester")
	require.Error(t, err)
	var fv1 *entity.ValidationError
	require.ErrorAs(t, err, &fv1)

	// Guard 2: duplicate (component,size) in one payload is rejected.
	err = T.UpsertStyleAssembly(ctx, styleID, []entity.StyleAssemblyInsert{
		{ComponentTechCardId: compID, Qty: decimal.RequireFromString("1"), Active: true},
		{ComponentTechCardId: compID, Qty: decimal.RequireFromString("1"), Active: true},
	}, "tester")
	require.Error(t, err)
	var fv2 *entity.ValidationError
	require.ErrorAs(t, err, &fv2)

	// The failed writes left nothing behind (full-replace deletes before insert only on the happy path).
	lines, err = T.ListStyleAssembly(ctx, styleID)
	require.NoError(t, err)
	require.Empty(t, lines)
}
