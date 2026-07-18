package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestProductionRunActualWastagePersists covers the "wastage% -> production run" column (migration
// 0187): the run's actual_wastage_percent round-trips through create/read, stays NULL (fall back to
// the BOM estimate) when unset, is mutable via update (unlike the frozen planned-cost snapshot), and
// is clearable back to NULL.
func TestProductionRunActualWastagePersists(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
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

	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	line := []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}}
	PR := s.ProductionRuns()

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Wastage Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-WASTE-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)

	// run WITH an actual cutting wastage set (8.50%).
	setID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		ActualWastagePercent: nd("8.50"),
		Lines:                line,
	})
	require.NoError(t, err)

	// run WITHOUT one → NULL, i.e. the cost calc falls back to the BOM line's estimate.
	nullID, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress, Lines: line,
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		cctx := context.Background()
		for _, id := range []int{setID, nullID} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	// round-trip: the set value comes back (2-dp column, trailing zero stripped); the unset one is NULL.
	setRun, err := PR.GetProductionRun(ctx, setID)
	require.NoError(t, err)
	require.True(t, setRun.ActualWastagePercent.Valid, "actual_wastage_percent persists when set")
	require.Equal(t, "8.5", setRun.ActualWastagePercent.Decimal.String())

	nullRun, err := PR.GetProductionRun(ctx, nullID)
	require.NoError(t, err)
	require.False(t, nullRun.ActualWastagePercent.Valid, "unset actual_wastage_percent stays NULL (BOM-estimate fallback)")

	// mutable via update — the frozen planned-cost snapshot is not re-taken, but this actual is.
	require.NoError(t, PR.UpdateProductionRun(ctx, nullID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		ActualWastagePercent: nd("12.00"), Lines: line,
	}, 0))
	updated, err := PR.GetProductionRun(ctx, nullID)
	require.NoError(t, err)
	require.True(t, updated.ActualWastagePercent.Valid)
	require.Equal(t, "12", updated.ActualWastagePercent.Decimal.String(), "update sets the actual wastage")

	// and clearable back to NULL via update (fall back to the BOM estimate again).
	require.NoError(t, PR.UpdateProductionRun(ctx, setID, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress, Lines: line,
	}, 0))
	cleared, err := PR.GetProductionRun(ctx, setID)
	require.NoError(t, err)
	require.False(t, cleared.ActualWastagePercent.Valid, "update can clear the actual wastage back to NULL")
}
