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

// TestMaterialWarehouse exercises new-flow NF-01: the moving-average valuation across
// receipts/issues/write-offs/adjustments, the negative-stock guard, FX-folded receipts, the
// purchase price-history side effect, and the warehouse list valuation. Throwaway harness; cleans
// its rows.
func TestMaterialWarehouse(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
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

	dec := func(i int64) decimal.Decimal { return decimal.NewFromInt(i) }
	nd := func(str string) decimal.NullDecimal { return decimal.NullDecimal{Decimal: decimal.RequireFromString(str), Valid: true} }
	date := func(d string) sql.NullTime { return sql.NullTime{Time: time.Date(2026, 1, atoiDay(d), 0, 0, 0, 0, time.UTC), Valid: true} }

	// USD → 0.9 EUR so a foreign-currency receipt folds to base.
	require.NoError(t, s.TechCards().UpsertCostingFxRates(ctx, []entity.CostingFxRate{
		{Currency: "USD", RateToBase: decimal.RequireFromString("0.9"), ValidFrom: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
	}))

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name:     "NF Warehouse Fabric",
		Section:  "fabric",
		Unit:     sql.NullString{String: "m", Valid: true},
		MinStock: nd("5"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(ctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(ctx, "DELETE FROM material WHERE id = ?", matID) // cascades stock + price
	})

	ms := s.MaterialStock()
	get := func() *entity.MaterialStock {
		st, err := ms.GetMaterialStock(ctx, matID)
		require.NoError(t, err)
		return st
	}

	// 1) receipt 10 @ 12 EUR → on-hand 10, avg 12.
	_, err = ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: dec(10), UnitCost: nd("12"), Currency: "EUR", OccurredAt: date("01")})
	require.NoError(t, err)
	st := get()
	require.True(t, st.OnHand.Equal(dec(10)), "on-hand 10, got %s", st.OnHand)
	require.True(t, st.AvgUnitCostBase.Valid && st.AvgUnitCostBase.Decimal.Equal(dec(12)), "avg 12, got %v", st.AvgUnitCostBase)

	// 2) receipt 10 @ 18 EUR → on-hand 20, avg (10·12 + 10·18)/20 = 15.
	_, err = ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: dec(10), UnitCost: nd("18"), Currency: "EUR", OccurredAt: date("02")})
	require.NoError(t, err)
	st = get()
	require.True(t, st.OnHand.Equal(dec(20)), "on-hand 20, got %s", st.OnHand)
	require.True(t, st.AvgUnitCostBase.Decimal.Equal(dec(15)), "avg 15, got %s", st.AvgUnitCostBase.Decimal)

	// two purchase price-history points were appended (distinct effective dates).
	prices, err := s.TechCards().ListMaterialPrices(ctx, matID)
	require.NoError(t, err)
	require.Len(t, prices, 2)
	for _, p := range prices {
		require.Equal(t, entity.MaterialPriceSourcePurchase, p.Source)
	}

	// 3) issue 5 to a sample → on-hand 15, avg unchanged; the movement freezes the average as its cost.
	mv, err := ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(5), SampleId: sql.NullInt32{Int32: 1, Valid: true}})
	require.NoError(t, err)
	require.Equal(t, entity.MaterialMovementIssueSample, mv.MovementType)
	require.True(t, mv.UnitCostBase.Valid && mv.UnitCostBase.Decimal.Equal(dec(15)), "issue cost = frozen avg 15, got %v", mv.UnitCostBase)
	require.True(t, get().OnHand.Equal(dec(15)))

	// 4) issue more than on-hand → guarded.
	_, err = ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(100), SampleId: sql.NullInt32{Int32: 1, Valid: true}})
	require.ErrorIs(t, err, entity.ErrInsufficientMaterialStock)
	require.True(t, get().OnHand.Equal(dec(15)), "guarded issue left on-hand unchanged")

	// 5) write-off 3 → on-hand 12, avg unchanged.
	_, err = ms.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{MaterialId: matID, Mode: entity.MaterialAdjustModeWriteoff, Quantity: dec(3), Reason: entity.MaterialAdjustReasonDamage})
	require.NoError(t, err)
	st = get()
	require.True(t, st.OnHand.Equal(dec(12)))
	require.True(t, st.AvgUnitCostBase.Decimal.Equal(dec(15)), "write-off does not change the average")

	// 6) stock count set to 20 → on-hand 20, movement quantity is the magnitude of the change (8).
	mv, err = ms.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{MaterialId: matID, Mode: entity.MaterialAdjustModeSet, Quantity: dec(20), Reason: entity.MaterialAdjustReasonStockCount})
	require.NoError(t, err)
	require.Equal(t, entity.MaterialMovementAdjustment, mv.MovementType)
	require.True(t, mv.Quantity.Equal(dec(8)), "count magnitude 8, got %s", mv.Quantity)
	require.True(t, get().OnHand.Equal(dec(20)))

	// 7) return 2 from the sample → on-hand 22.
	_, err = ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(2), SampleId: sql.NullInt32{Int32: 1, Valid: true}, IsReturn: true})
	require.NoError(t, err)
	require.True(t, get().OnHand.Equal(dec(22)))

	// 8) foreign-currency receipt: 10 @ 10 USD → base 9 each; new avg (22·15 + 10·9)/32 = 13.125.
	_, err = ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: dec(10), UnitCost: nd("10"), Currency: "USD", OccurredAt: date("03")})
	require.NoError(t, err)
	st = get()
	require.True(t, st.OnHand.Equal(dec(32)), "on-hand 32, got %s", st.OnHand)
	require.True(t, st.AvgUnitCostBase.Decimal.Equal(decimal.RequireFromString("13.125")), "avg 13.125, got %s", st.AvgUnitCostBase.Decimal)

	// 9) uncosted receipt: 5 units, no price → on-hand 37, average unchanged (still 13.125).
	_, err = ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: dec(5), OccurredAt: date("04")})
	require.NoError(t, err)
	st = get()
	require.True(t, st.OnHand.Equal(dec(37)))
	require.True(t, st.AvgUnitCostBase.Decimal.Equal(decimal.RequireFromString("13.125")), "uncosted receipt does not move the average")

	// 10) warehouse list: the material shows on-hand 37, value 37·13.125 = 485.625 (rounded 485.63), not below min.
	rows, err := ms.ListMaterialStock(ctx, entity.MaterialStockFilter{})
	require.NoError(t, err)
	var found bool
	for _, r := range rows {
		if r.Material.Id != matID {
			continue
		}
		found = true
		require.True(t, r.OnHand.Equal(dec(37)))
		require.True(t, r.StockValueBase.Valid && r.StockValueBase.Decimal.Equal(decimal.RequireFromString("485.63")), "value 485.63, got %v", r.StockValueBase)
		require.False(t, r.BelowMinStock)
	}
	require.True(t, found, "material present in warehouse list")

	// the movement ledger recorded every change (2 receipts + issue + write-off + set + return + fx receipt + uncosted = 8).
	movements, total, err := ms.ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{MaterialId: matID})
	require.NoError(t, err)
	require.Equal(t, 8, total)
	require.Len(t, movements, 8)
}

// TestMaterialCatalogV2 exercises new-flow NF-02: a material can now be created in the widened
// section set (`other`/`decoration`/`trim` — the realigned CHECK), the internal code is unique among
// non-archived materials, and the unit of measure is locked once the material has stock movements.
func TestMaterialCatalogV2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	str := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	tc := s.TechCards()
	var ids []int
	t.Cleanup(func() {
		for _, id := range ids {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(ctx, "DELETE FROM material WHERE id = ?", id)
		}
	})

	// section `other` was previously rejected by the material.section CHECK; the realignment allows it.
	otherID, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Misc", Section: "other", Unit: str("pcs"), Code: str("NF-CODE-1")})
	require.NoError(t, err, "widened section `other` must be accepted")
	ids = append(ids, otherID)

	// duplicate code is rejected.
	_, err = tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Dup", Section: "trim", Code: str("NF-CODE-1")})
	require.ErrorIs(t, err, entity.ErrMaterialCodeTaken)

	// a distinct code is fine; unit can change while there are no movements.
	m2, err := tc.CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Fabric2", Section: "fabric", Unit: str("m"), Code: str("NF-CODE-2")})
	require.NoError(t, err)
	ids = append(ids, m2)
	require.NoError(t, tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2", Section: "fabric", Unit: str("cm"), Code: str("NF-CODE-2")}))

	// after a stock movement, the unit of measure is locked.
	_, err = s.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m2, Quantity: decimal.NewFromInt(3)})
	require.NoError(t, err)
	err = tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2", Section: "fabric", Unit: str("m"), Code: str("NF-CODE-2")})
	require.ErrorIs(t, err, entity.ErrMaterialUnitLocked)
	// same unit (no change) still updates fine.
	require.NoError(t, tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2 renamed", Section: "fabric", Unit: str("cm"), Code: str("NF-CODE-2")}))
}

// TestTechCardIdeaStage exercises new-flow NF-03 at the store level: an `idea` draft persists with
// a NULL style_number, round-trips as stage=idea, and two drafts share a season (the
// UNIQUE(style_number, season) key permits multiple NULLs).
func TestTechCardIdeaStage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

	draft := func(name string) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			// no StyleNumber (NULL) — an idea draft
			Name:            name,
			Season:          sql.NullString{String: "NF-IDEA-SEASON", Valid: true},
			Stage:           entity.TechCardStageIdea,
			ApprovalState:   entity.TechCardApprovalDraft,
			TargetGender:    sql.NullString{String: "unisex", Valid: true},
			MeasurementUnit: entity.TechCardUnitMm,
		}
	}

	id1, err := s.TechCards().AddTechCard(ctx, draft("NF Idea One"))
	require.NoError(t, err, "idea draft without a style number must persist")
	id2, err := s.TechCards().AddTechCard(ctx, draft("NF Idea Two"))
	require.NoError(t, err, "a second idea draft in the same season must coexist (NULL style_number)")
	t.Cleanup(func() {
		for _, id := range []int{id1, id2} {
			_, _ = testDB.ExecContext(ctx, "DELETE FROM tech_card WHERE id = ?", id)
		}
	})

	got, err := s.TechCards().GetTechCardById(ctx, id1)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardStageIdea, got.Stage)
	require.False(t, got.StyleNumber.Valid, "idea draft style_number is NULL")
	require.Equal(t, "NF Idea One", got.Name)
}

// atoiDay converts a 2-char day string ("01".."31") to its int day for building a deterministic
// date; avoids strconv import noise in the table above.
func atoiDay(d string) int {
	return int(d[0]-'0')*10 + int(d[1]-'0')
}
