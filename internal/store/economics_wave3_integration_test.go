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

// TestMaterialCatalog exercises the task-10 material catalog + append-only price history against
// a real MySQL: create/get/update/archive, the current-price join (latest valid_from <= today,
// ignoring future-dated rows), and the full history read. Cleans up via ON DELETE CASCADE.
func TestMaterialCatalog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	T := s.TechCards()
	d := func(v string) decimal.Decimal { return decimal.RequireFromString(v) }
	nd := func(v string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: d(v), Valid: true} }
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	day := func(y int, m time.Month, dd int) time.Time { return time.Date(y, m, dd, 0, 0, 0, 0, time.UTC) }

	var matID int
	defer func() {
		if matID != 0 {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material WHERE id = ?", matID) // material_price cascades
		}
	}()

	// create
	matID, err = T.CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "Wool 300gsm", Section: "fabric", Supplier: ns("MillCo"), FabricWeightGsm: nd("300"),
	})
	require.NoError(t, err)
	require.Greater(t, matID, 0)

	// get — no price yet
	m, err := T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.Equal(t, "Wool 300gsm", m.Name)
	require.Equal(t, "fabric", m.Section)
	require.Nil(t, m.LatestPrice, "no price history yet")

	// price history: two effective points + one future (must be ignored by "current")
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("12.50"), Currency: "EUR", ValidFrom: day(2026, 1, 1)}))
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("14.00"), Currency: "EUR", ValidFrom: day(2026, 6, 1)}))
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("99.00"), Currency: "EUR", ValidFrom: day(2099, 1, 1)}))

	m, err = T.GetMaterial(ctx, matID)
	require.NoError(t, err)
	require.NotNil(t, m.LatestPrice)
	require.True(t, m.LatestPrice.Price.Equal(d("14.00")), "current price is the latest effective, not the future one: got %s", m.LatestPrice.Price)

	// same-day correction upserts (not duplicates)
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{MaterialId: matID, Price: d("14.25"), Currency: "EUR", ValidFrom: day(2026, 6, 1)}))
	hist, err := T.ListMaterialPrices(ctx, matID)
	require.NoError(t, err)
	require.Len(t, hist, 3, "same (date,currency) upserts rather than appends")
	require.True(t, hist[0].ValidFrom.Equal(day(2099, 1, 1)), "history is newest-first")

	// list by section (current price attached)
	list, err := T.ListMaterials(ctx, "fabric", false)
	require.NoError(t, err)
	found := false
	for _, mm := range list {
		if mm.Id == matID {
			found = true
			require.NotNil(t, mm.LatestPrice)
			require.True(t, mm.LatestPrice.Price.Equal(d("14.25")))
		}
	}
	require.True(t, found, "material present in section list")

	// update descriptive fields
	require.NoError(t, T.UpdateMaterial(ctx, matID, &entity.MaterialInsert{Name: "Wool 320gsm", Section: "fabric"}))
	m, _ = T.GetMaterial(ctx, matID)
	require.Equal(t, "Wool 320gsm", m.Name)

	// archive removes it from the default list but not the include-archived list
	require.NoError(t, T.ArchiveMaterial(ctx, matID, true))
	list, _ = T.ListMaterials(ctx, "fabric", false)
	require.False(t, containsMaterial(list, matID), "archived excluded by default")
	list, _ = T.ListMaterials(ctx, "fabric", true)
	require.True(t, containsMaterial(list, matID), "archived included when requested")
}

// TestProductionRun exercises the task-09 phase-1 production-run store against a real MySQL:
// create with a plan snapshot + size grid, get, list (sizes attached), update (frozen plan cost,
// full-replaced sizes, received/defect facts), and delete (grid cascades).
func TestProductionRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber: "PRUN-001", Name: "Run Coat", Stage: entity.TechCardStageProto,
		ApprovalState: entity.TechCardApprovalDraft, MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	defer func() { _ = s.TechCards().DeleteTechCard(ctx, tcID) }()

	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId:      tcID,
		Status:          entity.ProductionRunPlanned,
		PlannedUnitCost: nd("33.00"),
		PlannedCurrency: ns("EUR"),
		Sizes: []entity.ProductionRunSize{
			{SizeId: 1, PlannedQty: 60},
			{SizeId: 2, PlannedQty: 40},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostMaterials, Amount: decimal.RequireFromString("500"), Currency: "EUR", AmountBase: nd("500")},
		},
	})
	require.NoError(t, err)
	require.Greater(t, runID, 0)

	got, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, tcID, got.TechCardId)
	require.Equal(t, entity.ProductionRunPlanned, got.Status)
	require.True(t, got.PlannedUnitCost.Decimal.Equal(decimal.RequireFromString("33.00")))
	require.Equal(t, "EUR", got.PlannedCurrency.String)
	require.Len(t, got.Sizes, 2)
	require.Equal(t, 1, got.Sizes[0].SizeId)
	require.Equal(t, 60, got.Sizes[0].PlannedQty)
	require.False(t, got.Sizes[0].ReceivedQty.Valid, "received unset until receipt")
	require.Len(t, got.Costs, 1)
	require.Equal(t, entity.ProductionRunCostMaterials, got.Costs[0].Kind)
	require.True(t, got.Costs[0].Amount.Equal(decimal.RequireFromString("500")))
	require.True(t, got.Costs[0].AmountBase.Decimal.Equal(decimal.RequireFromString("500")))

	list, total, err := P.ListProductionRuns(ctx, 0, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, list, 1)
	require.Len(t, list[0].Sizes, 2, "sizes attached to list rows")

	// status filter that excludes the run returns nothing
	_, total, err = P.ListProductionRuns(ctx, 0, 0, entity.ProductionRunListFilter{TechCardId: tcID, Status: entity.ProductionRunClosed})
	require.NoError(t, err)
	require.Equal(t, 0, total)

	// update: advance status, record facts, full-replace the grid — the plan cost stays frozen.
	require.NoError(t, P.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
		TechCardId:      tcID,
		Status:          entity.ProductionRunInProgress,
		StartedAt:       sql.NullTime{Time: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), Valid: true},
		PlannedUnitCost: nd("999.00"), // must be IGNORED by update (frozen at plan time)
		PlannedCurrency: ns("USD"),
		Notes:           ns("started"),
		Sizes: []entity.ProductionRunSize{
			{SizeId: 1, PlannedQty: 60, ReceivedQty: sql.NullInt64{Int64: 58, Valid: true}, DefectQty: sql.NullInt64{Int64: 2, Valid: true}},
		},
		Costs: []entity.ProductionRunCost{
			{Kind: entity.ProductionRunCostCMT, Amount: decimal.RequireFromString("400"), Currency: "EUR", AmountBase: nd("400")},
		},
	}))
	got, err = P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunInProgress, got.Status)
	require.True(t, got.PlannedUnitCost.Decimal.Equal(decimal.RequireFromString("33.00")), "plan cost frozen, not overwritten by update")
	require.Equal(t, "EUR", got.PlannedCurrency.String)
	require.Len(t, got.Sizes, 1, "grid full-replaced")
	require.EqualValues(t, 58, got.Sizes[0].ReceivedQty.Int64)
	require.EqualValues(t, 2, got.Sizes[0].DefectQty.Int64)
	require.Len(t, got.Costs, 1, "costs full-replaced")
	require.Equal(t, entity.ProductionRunCostCMT, got.Costs[0].Kind)

	// update of a missing run → ErrNoRows
	err = P.UpdateProductionRun(ctx, 0, &entity.ProductionRunInsert{TechCardId: tcID, Status: entity.ProductionRunPlanned})
	require.ErrorIs(t, err, sql.ErrNoRows)

	// delete: run gone, grid cascades
	require.NoError(t, P.DeleteProductionRun(ctx, runID))
	_, err = P.GetProductionRun(ctx, runID)
	require.ErrorIs(t, err, sql.ErrNoRows)
	list, _, err = P.ListProductionRuns(ctx, 0, 0, entity.ProductionRunListFilter{TechCardId: tcID})
	require.NoError(t, err)
	require.Empty(t, list)
}

func containsMaterial(list []entity.MaterialWithPrice, id int) bool {
	for _, m := range list {
		if m.Id == id {
			return true
		}
	}
	return false
}

// TestTechCardRelease exercises the task-11 immutable release snapshot store methods against a
// real MySQL: save (blob + metadata), list newest-first, get-by-id (blob round-trips), a second
// release episode, a missing-id read, and ON DELETE CASCADE when the parent card is removed.
func TestTechCardRelease(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	T := s.TechCards()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	// minimal parent card (header only — no size/media/product FKs to satisfy).
	tcID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
		StyleNumber:     "REL-001",
		Name:            "Release Coat",
		Stage:           entity.TechCardStageProto,
		ApprovalState:   entity.TechCardApprovalDraft,
		MeasurementUnit: entity.TechCardUnitMm,
	})
	require.NoError(t, err)
	defer func() { _ = T.DeleteTechCard(ctx, tcID) }()

	blob1 := `{"id":1,"name":"Release Coat v1"}`
	require.NoError(t, T.SaveTechCardRelease(ctx, entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: tcID, Version: ns("1.0"), ReleasedBy: ns("alice"),
			UnitCost: nd("12.34"), Currency: ns("EUR"),
		},
		Snapshot: blob1,
	}))

	list, err := T.ListTechCardReleases(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.Equal(t, "1.0", list[0].Version.String)
	require.Equal(t, "alice", list[0].ReleasedBy.String)
	require.True(t, list[0].UnitCost.Decimal.Equal(decimal.RequireFromString("12.34")))
	require.Equal(t, "EUR", list[0].Currency.String)
	require.False(t, list[0].CreatedAt.IsZero(), "created_at is DB-stamped")

	got, err := T.GetTechCardRelease(ctx, list[0].Id)
	require.NoError(t, err)
	require.JSONEq(t, blob1, got.Snapshot, "blob round-trips verbatim")
	require.Equal(t, tcID, got.TechCardId)

	// second release episode (re-open → re-release): newest-first, both retained.
	require.NoError(t, T.SaveTechCardRelease(ctx, entity.TechCardRelease{
		TechCardReleaseMeta: entity.TechCardReleaseMeta{
			TechCardId: tcID, Version: ns("2.0"), ReleasedBy: ns("bob"),
		},
		Snapshot: `{"id":1,"name":"Release Coat v2"}`,
	}))
	list, err = T.ListTechCardReleases(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "2.0", list[0].Version.String, "newest-first")
	require.False(t, list[0].UnitCost.Valid, "v2.0 was released without a foldable unit cost")
	require.Equal(t, "1.0", list[1].Version.String)
	require.True(t, list[1].UnitCost.Valid, "v1.0 kept its frozen unit cost")

	// missing id → sql.ErrNoRows (so the handler can map to NotFound).
	_, err = T.GetTechCardRelease(ctx, 0)
	require.ErrorIs(t, err, sql.ErrNoRows)

	// ON DELETE CASCADE: removing the card drops its releases.
	require.NoError(t, T.DeleteTechCard(ctx, tcID))
	list, err = T.ListTechCardReleases(ctx, tcID)
	require.NoError(t, err)
	require.Empty(t, list, "releases cascade with the parent card")
}
