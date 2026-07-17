package store

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"sync"
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
	nd := func(str string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(str), Valid: true}
	}
	date := func(d string) sql.NullTime {
		return sql.NullTime{Time: time.Date(2026, 1, atoiDay(d), 0, 0, 0, 0, time.UTC), Valid: true}
	}

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
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID) // cascades stock + price
	})

	// a tech card + sample so issue_sample movements satisfy the sample_id FK (added in 0108).
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Warehouse Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-WH-1", Valid: true},
		TargetGender:    sql.NullString{String: "unisex", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) }) // cascades sample
	smID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{TechCardId: tcID, Purpose: "proto", Status: "planned", FabricSource: "sample"})
	require.NoError(t, err)
	sampleRef := sql.NullInt32{Int32: int32(smID), Valid: true}

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
	mv, err := ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(5), SampleId: sampleRef})
	require.NoError(t, err)
	require.Equal(t, entity.MaterialMovementIssueSample, mv.MovementType)
	require.True(t, mv.UnitCostBase.Valid && mv.UnitCostBase.Decimal.Equal(dec(15)), "issue cost = frozen avg 15, got %v", mv.UnitCostBase)
	require.True(t, get().OnHand.Equal(dec(15)))

	// 4) issue more than on-hand → guarded.
	_, err = ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(100), SampleId: sampleRef})
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
	_, err = ms.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: dec(2), SampleId: sampleRef, IsReturn: true})
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
		cctx := context.Background()
		for _, id := range ids {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
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
	require.NoError(t, tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2", Section: "fabric", Unit: str("cm"), Code: str("NF-CODE-2")}, 0))

	// after a stock movement, the unit of measure is locked. The optimistic-lock version is now 1
	// (the update above bumped it); pass it so the unit check — not a conflict — is what rejects.
	_, err = s.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: m2, Quantity: decimal.NewFromInt(3)})
	require.NoError(t, err)
	err = tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2", Section: "fabric", Unit: str("m"), Code: str("NF-CODE-2")}, 1)
	require.ErrorIs(t, err, entity.ErrMaterialUnitLocked)
	// same unit (no change) still updates fine (the rejected update did not bump the version).
	require.NoError(t, tc.UpdateMaterial(ctx, m2, &entity.MaterialInsert{Name: "NF Fabric2 renamed", Section: "fabric", Unit: str("cm"), Code: str("NF-CODE-2")}, 1))
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
		cctx := context.Background()
		for _, id := range []int{id1, id2} {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", id)
		}
	})

	got, err := s.TechCards().GetTechCardById(ctx, id1)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardStageIdea, got.Stage)
	require.False(t, got.StyleNumber.Valid, "idea draft style_number is NULL")
	require.Equal(t, "NF Idea One", got.Name)
}

// TestSampleCost exercises new-flow NF-04: a sample gets an auto per-card number, its cost is
// composed from material issues (NF-01) plus dev-expense rows tied to it, and it cannot be deleted
// while it has material movements.
func TestSampleCost(t *testing.T) {
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

	// a tech card to hang the sample off.
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Sample Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-SMP-1", Valid: true},
		TargetGender:    sql.NullString{String: "unisex", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)

	// a material with stock at avg 10.
	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Sample Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	_, err = s.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
		MaterialId: matID, Quantity: decimal.NewFromInt(20), UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(10), Valid: true}, Currency: "EUR",
	})
	require.NoError(t, err)

	// the sample (number auto-assigned).
	smID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{
		TechCardId: tcID, Purpose: entity.SamplePurposeProto, Status: entity.SampleStatusInSewing,
		FabricSource: entity.SampleFabricSample, SizeId: sql.NullInt32{Int32: 1, Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card_dev_expense WHERE tech_card_id = ?", tcID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM sample WHERE tech_card_id = ?", tcID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
	})

	sm, err := s.Samples().GetSampleById(ctx, smID)
	require.NoError(t, err)
	require.Equal(t, 1, sm.Number, "first sample of the card is number 1")

	// issue 2.5 m to the sample → materials cost 2.5 × 10 = 25.
	_, err = s.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.RequireFromString("2.5"), SampleId: sql.NullInt32{Int32: int32(smID), Valid: true},
	})
	require.NoError(t, err)

	// a manual dev-expense of 20 (base) tied to the sample.
	_, err = s.TechCards().AddTechCardDevExpense(ctx, entity.TechCardDevExpense{
		TechCardId: tcID, Kind: "labour", Amount: decimal.NewFromInt(20), Currency: "EUR",
		AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(20), Valid: true},
		SampleId:   sql.NullInt32{Int32: int32(smID), Valid: true},
	})
	require.NoError(t, err)

	sm, err = s.Samples().GetSampleById(ctx, smID)
	require.NoError(t, err)
	require.NotNil(t, sm.Cost)
	require.True(t, sm.Cost.MaterialsBase.Equal(decimal.NewFromInt(25)), "materials 2.5×10=25, got %s", sm.Cost.MaterialsBase)
	require.True(t, sm.Cost.ManualBase.Equal(decimal.NewFromInt(20)), "manual 20, got %s", sm.Cost.ManualBase)
	require.True(t, sm.Cost.TotalBase.Equal(decimal.NewFromInt(45)), "total 45, got %s", sm.Cost.TotalBase)
	require.False(t, sm.Cost.HasUncosted)

	// a returned 0.5 m reduces the materials cost by 0.5×10=5 → 20.
	_, err = s.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{
		MaterialId: matID, Quantity: decimal.RequireFromString("0.5"), SampleId: sql.NullInt32{Int32: int32(smID), Valid: true}, IsReturn: true,
	})
	require.NoError(t, err)
	sm, err = s.Samples().GetSampleById(ctx, smID)
	require.NoError(t, err)
	require.True(t, sm.Cost.MaterialsBase.Equal(decimal.NewFromInt(20)), "materials after return 25−5=20, got %s", sm.Cost.MaterialsBase)

	// gap-06: the issue movement denormalises the owning style (tech_card_id) so the journal can be
	// filtered by style without joining through the sample.
	movements, _, err := s.MaterialStock().ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{SampleId: smID})
	require.NoError(t, err)
	require.NotEmpty(t, movements)
	for _, mv := range movements {
		require.True(t, mv.TechCardId.Valid, "movement carries owning tech card")
		require.Equal(t, int32(tcID), mv.TechCardId.Int32, "movement tech_card_id = sample's tech card")
	}

	// the sample cannot be deleted while it has material movements.
	require.ErrorIs(t, s.Samples().DeleteSample(ctx, smID), entity.ErrSampleHasMovements)
}

// atoiDay converts a 2-char day string ("01".."31") to its int day for building a deterministic
// date; avoids strconv import noise in the table above.
func atoiDay(d string) int {
	return int(d[0]-'0')*10 + int(d[1]-'0')
}

// TestTechCardPieces exercises new-flow NF-05: structural cut-pieces + the per-colourway fabric
// matrix round-trip through AddTechCard/GetTechCard, the positional colorway_index resolution
// surviving a full-replace colourway reorder, and the store-level bounds guard.
func TestTechCardPieces(t *testing.T) {
	t.Skip("PR6 R1 merge: colourways/product_ids left the tech-card write payload (colourways are products now); this integration test's setup is redesigned in track T-E")
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

	ni := func(i int32) sql.NullInt32 { return sql.NullInt32{Int32: i, Valid: true} }
	unit := sql.NullString{String: "m", Valid: true}

	// colorwayNames lets the piece assertions verify the colorway_index → colorway mapping.
	build := func(styleNum string, colorwayNames []string) *entity.TechCardInsert {
		return &entity.TechCardInsert{
			Name: "NF Pieces Style", Stage: entity.TechCardStageProto,
			StyleNumber:     sql.NullString{String: styleNum, Valid: true},
			TargetGender:    sql.NullString{String: "unisex", Valid: true},
			MeasurementUnit: entity.TechCardUnitMm,
			ApprovalState:   entity.TechCardApprovalDraft,
			BomItems: []entity.TechCardBomItem{
				{Section: "fabric", Name: "Main fabric", Unit: unit},
				{Section: "interlining", Name: "Fusing", Unit: unit},
			},
			Pieces: []entity.TechCardPiece{
				{Name: "Front", PiecesPerGarment: 1, Grainline: "lengthwise", Materials: []entity.TechCardPieceMaterial{
					{ColorwayID: 0, BomItemIndex: ni(0)},
					{ColorwayID: 1, BomItemIndex: ni(0)},
				}},
				{Name: "Collar", PiecesPerGarment: 2, Mirrored: true, Grainline: "bias", Fused: true, Materials: []entity.TechCardPieceMaterial{
					{ColorwayID: 0, BomItemIndex: ni(0), FusingBomItemIndex: ni(1)},
				}},
			},
		}
	}

	tcID, err := s.TechCards().AddTechCard(ctx, build("NF-PC-1", []string{"Black", "Navy"}))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	// round-trip: pieces come back in order with their per-colourway fabric mapping.
	card, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, card.Pieces, 2)

	front := card.Pieces[0]
	require.Equal(t, "Front", front.Name)
	require.Equal(t, 1, front.PiecesPerGarment)
	require.Len(t, front.Materials, 2)
	require.Equal(t, 0, front.Materials[0].ColorwayID)
	require.Equal(t, 1, front.Materials[1].ColorwayID)
	require.True(t, front.Materials[0].BomItemIndex.Valid && front.Materials[0].BomItemIndex.Int32 == 0)

	collar := card.Pieces[1]
	require.Equal(t, "Collar", collar.Name)
	require.True(t, collar.Mirrored && collar.Fused)
	require.Equal(t, "bias", collar.Grainline)
	require.Len(t, collar.Materials, 1)
	require.True(t, collar.Materials[0].FusingBomItemIndex.Valid && collar.Materials[0].FusingBomItemIndex.Int32 == 1)

	// full-replace with colourways REORDERED (Navy first). The piece materials still target the
	// same positional colorway_index, so after reorder Front[0] now maps to Navy — the store must
	// re-resolve index → the freshly-inserted colorway_id, never leak the old ids.
	require.NoError(t, s.TechCards().UpdateTechCard(ctx, tcID, build("NF-PC-1", []string{"Navy", "Black"}), card.LockVersion))
	card2, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Len(t, card2.Pieces, 2)
	require.Equal(t, "Navy", card2.Colorways[0].Name)
	require.Equal(t, 0, card2.Pieces[0].Materials[0].ColorwayID, "index preserved through reorder")
	// the piece_material row must point at the NEW colorway_id (Navy is now index 0); a stale id
	// would either FK-fail the update or resolve to a wrong/absent index here.
	require.Equal(t, 1, card2.Pieces[0].Materials[1].ColorwayID)

	// store-level guard: an out-of-range colorway_index is rejected (defence in depth under dto).
	bad := build("NF-PC-2", []string{"Black", "Navy"})
	bad.Pieces[0].Materials[0].ColorwayID = 5
	_, err = s.TechCards().AddTechCard(ctx, bad)
	require.Error(t, err, "out-of-range colorway_index must fail")
}

// TestProductionRunMaterialFlow exercises NF-06 §6.3: material issued to a run loads onto the run
// as movements (feeding materials-from-stock actuals) and blocks deletion of the run.
func TestProductionRunMaterialFlow(t *testing.T) {
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

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Run Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-RUN-1", Valid: true},
		TargetGender:    sql.NullString{String: "unisex", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	P := s.ProductionRuns()
	// planning lines carry no product_id yet (colourways not published) — the FK allows NULL.
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}, {SizeId: 2, PlannedQty: 8}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Run Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	// receive 100 @ 4 EUR (avg 4), issue 21 to the run, return 1.
	_, err = s.MaterialStock().ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(100), UnitCost: nd("4"), Currency: "EUR"})
	require.NoError(t, err)
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}
	_, err = s.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(21), ProductionRunId: runRef})
	require.NoError(t, err)
	_, err = s.MaterialStock().IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(1), ProductionRunId: runRef, IsReturn: true})
	require.NoError(t, err)

	// the run now carries the two movements; net issued = 20 @ avg 4 → materials-from-stock 80.
	got, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Len(t, got.MaterialMovements, 2)
	var netIssued decimal.Decimal
	for _, m := range got.MaterialMovements {
		switch m.MovementType {
		case entity.MaterialMovementIssueProduction:
			netIssued = netIssued.Add(m.Quantity)
		case entity.MaterialMovementReturnProduction:
			netIssued = netIssued.Sub(m.Quantity)
		}
	}
	require.True(t, netIssued.Equal(decimal.NewFromInt(20)), "21 issued − 1 returned")

	// a run with stock movements cannot be deleted.
	err = P.DeleteProductionRun(ctx, runID)
	require.ErrorIs(t, err, entity.ErrProductionRunHasMovements)
}

// TestAuxiliaryProductionRun exercises NF-07: an auxiliary card's run output is received into the
// material warehouse (receipt_production moving the average + a production_run price point), not
// into product stock; a second receive is refused.
func TestAuxiliaryProductionRun(t *testing.T) {
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

	// output material the dust bag lands in (packaging section).
	outMatID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Dust Bag Stock", Section: "packaging", Unit: sql.NullString{String: "pc", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", outMatID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", outMatID)
	})

	// auxiliary tech card that outputs into that material.
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Dust Bag", Stage: entity.TechCardStageProto,
		StyleNumber:      sql.NullString{String: "NF-AUX-1", Valid: true},
		TargetGender:     sql.NullString{String: "unisex", Valid: true},
		MeasurementUnit:  entity.TechCardUnitMm,
		ApprovalState:    entity.TechCardApprovalDraft,
		Purpose:          entity.TechCardPurposeAuxiliary,
		OutputMaterialId: sql.NullInt64{Int64: int64(outMatID), Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	// read-back: purpose + output material round-trip.
	card, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	require.Equal(t, entity.TechCardPurposeAuxiliary, card.Purpose)
	require.EqualValues(t, outMatID, card.OutputMaterialId.Int64)

	// a run producing 100 units (one OS line, no product) with a 200-base manual cost article — the
	// store derives the receipt's unit cost from the run's ACTUALS under the lock (g25-07): 200/100=2.
	P := s.ProductionRuns()
	runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 100, ReceivedQty: sql.NullInt64{Int64: 100, Valid: true}}},
		Costs: []entity.ProductionRunCost{{
			Kind: entity.ProductionRunCostMaterials, Amount: decimal.NewFromInt(200), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(200), Valid: true},
		}},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
	})

	require.NoError(t, P.ReceiveAuxiliaryProductionRun(ctx, runID, outMatID, "tester"))

	// the material warehouse now holds 100 @ avg 2.
	st, err := s.MaterialStock().GetMaterialStock(ctx, outMatID)
	require.NoError(t, err)
	require.True(t, st.OnHand.Equal(decimal.NewFromInt(100)), "on-hand 100, got %s", st.OnHand)
	require.True(t, st.AvgUnitCostBase.Valid && st.AvgUnitCostBase.Decimal.Equal(decimal.NewFromInt(2)), "avg 2, got %v", st.AvgUnitCostBase)

	// a production_run price point was appended (cost history of our own output).
	prices, err := s.TechCards().ListMaterialPrices(ctx, outMatID)
	require.NoError(t, err)
	require.Len(t, prices, 1)
	require.Equal(t, entity.MaterialPriceSourceProductionRun, prices[0].Source)

	// the run is received; a second receive is refused.
	got, err := P.GetProductionRun(ctx, runID)
	require.NoError(t, err)
	require.Equal(t, entity.ProductionRunReceived, got.Status)
	require.ErrorIs(t, P.ReceiveAuxiliaryProductionRun(ctx, runID, outMatID, "tester"), entity.ErrProductionRunAlreadyReceived)
}

// TestMaterialIssueConcurrentShortage exercises NF-01's negative-stock guard under contention
// (nf01-07 gap #1): two goroutines race to issue the last unit of a material. The row-level
// FOR UPDATE in readStockForUpdate must serialise them so exactly one issue succeeds and the other
// fails cleanly on the shortage — on-hand never goes negative and no phantom stock is minted.
func TestMaterialIssueConcurrentShortage(t *testing.T) {
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

	// a tech card + sample to satisfy the issue's sample_id FK.
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Race Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-RACE-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })
	smID, err := s.Samples().AddSample(ctx, &entity.SampleInsert{TechCardId: tcID, Purpose: "proto", Status: entity.SampleStatusInSewing, FabricSource: "sample"})
	require.NoError(t, err)
	sampleRef := sql.NullInt32{Int32: int32(smID), Valid: true}

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF Race Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	MS := s.MaterialStock()
	// exactly ONE unit on hand.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(1), UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(7), Valid: true}, Currency: "EUR"})
	require.NoError(t, err)

	// two goroutines each try to issue the whole unit, released together by a barrier.
	const racers = 2
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	done.Add(racers)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		go func(idx int) {
			defer done.Done()
			start.Wait()
			_, e := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
				MaterialId: matID, Quantity: decimal.NewFromInt(1), SampleId: sampleRef,
			})
			errs[idx] = e
		}(i)
	}
	start.Done() // release both at once
	done.Wait()

	var ok, shortage int
	for _, e := range errs {
		switch {
		case e == nil:
			ok++
		case errors.Is(e, entity.ErrInsufficientMaterialStock):
			shortage++
		default:
			t.Fatalf("unexpected issue error: %v", e)
		}
	}
	require.Equal(t, 1, ok, "exactly one issue succeeds")
	require.Equal(t, 1, shortage, "the loser fails on the shortage guard, not with a phantom issue")

	st, err := MS.GetMaterialStock(ctx, matID)
	require.NoError(t, err)
	require.True(t, st.OnHand.Equal(decimal.Zero), "on-hand settles at 0, never negative, got %s", st.OnHand)
}

// TestMaterialReceiptNoFXRate exercises NF-01's FX-fold branch (nf01-07 gap #2): a PRICED receipt
// in a currency that has no costing FX rate leaves unit_cost_base NULL, so the moving average is
// frozen (not moved) while on-hand still grows — distinct from an unpriced receipt (no unit_cost at
// all), which also freezes the average but for a different reason. The frozen movement is annotated.
func TestMaterialReceiptNoFXRate(t *testing.T) {
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

	nd := func(str string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(str), Valid: true}
	}

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF NoFX Fabric", Section: "fabric", Unit: sql.NullString{String: "m", Valid: true}})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	MS := s.MaterialStock()
	// baseline: 10 @ 12 EUR → on-hand 10, avg 12.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(10), UnitCost: nd("12"), Currency: "EUR"})
	require.NoError(t, err)

	// priced receipt in ZZZ — a currency with no costing FX rate: on-hand grows, base cost unknown.
	mv, err := MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(5), UnitCost: nd("100"), Currency: "ZZZ"})
	require.NoError(t, err)
	require.True(t, mv.UnitCost.Valid, "the foreign unit cost is still recorded")
	require.False(t, mv.UnitCostBase.Valid, "no FX rate → no base cost on the movement")
	require.True(t, mv.Comment.Valid && strings.Contains(mv.Comment.String, "no FX rate"), "frozen receipt is annotated, got %q", mv.Comment.String)

	st, err := MS.GetMaterialStock(ctx, matID)
	require.NoError(t, err)
	require.True(t, st.OnHand.Equal(decimal.NewFromInt(15)), "on-hand grows to 15, got %s", st.OnHand)
	require.True(t, st.AvgUnitCostBase.Valid && st.AvgUnitCostBase.Decimal.Equal(decimal.NewFromInt(12)),
		"average frozen at 12 (the unratable receipt can't move it), got %v", st.AvgUnitCostBase)

	// contrast: an UNPRICED receipt (no unit_cost at all) also freezes the average, but its movement
	// carries neither a unit cost nor the no-FX note.
	mv2, err := MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(3)})
	require.NoError(t, err)
	require.False(t, mv2.UnitCost.Valid, "unpriced receipt has no unit cost")
	require.False(t, mv2.UnitCostBase.Valid)
	require.False(t, mv2.Comment.Valid && strings.Contains(mv2.Comment.String, "no FX rate"),
		"an unpriced receipt is not a frozen-FX case")
	st, err = MS.GetMaterialStock(ctx, matID)
	require.NoError(t, err)
	require.True(t, st.OnHand.Equal(decimal.NewFromInt(18)))
	require.True(t, st.AvgUnitCostBase.Decimal.Equal(decimal.NewFromInt(12)), "still frozen at 12")
}

// TestBomSectionDrift guards NF-02's section vocabulary against drift (nf01-07 gap #3): the Go
// TechCardBomSection enum must stay identical to the REGEXP allow-list in BOTH CHECK constraints of
// migration 0106 (chk_material_section, chk_bom_item_section). A section added to one side but not
// the other would silently reject valid inserts (or admit invalid ones) at runtime. The second half
// inserts a card whose BOM uses the two widened sections (decoration/other) to prove the live CHECK
// accepts them end-to-end.
func TestBomSectionDrift(t *testing.T) {
	// --- Go enum vs the two migration CHECK regexes (no DB needed) ---
	content, err := fs.ReadFile("sql/0106_new_flow_material_catalog_v2.sql")
	require.NoError(t, err)

	goSet := map[string]bool{}
	for sec := range entity.ValidTechCardBomSections {
		goSet[string(sec)] = true
	}

	// pull each `REGEXP '^(a|b|c)$'` allow-list out of the migration (SQL doubles the quotes).
	re := regexp.MustCompile(`REGEXP ''\^\(([^)]+)\)\$''`)
	matches := re.FindAllStringSubmatch(string(content), -1)
	require.Len(t, matches, 2, "migration 0106 defines exactly the two realigned section CHECKs")
	for _, m := range matches {
		checkSet := map[string]bool{}
		for _, v := range strings.Split(m[1], "|") {
			checkSet[v] = true
		}
		require.Equal(t, goSet, checkSet,
			"CHECK regex %q drifted from the Go TechCardBomSection enum", m[1])
	}

	// --- live CHECK accepts the widened sections end-to-end ---
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

	unit := sql.NullString{String: "m", Valid: true}
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF Section Style", Stage: entity.TechCardStageProto,
		StyleNumber:     sql.NullString{String: "NF-SEC-1", Valid: true},
		MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState:   entity.TechCardApprovalDraft,
		BomItems: []entity.TechCardBomItem{
			{Section: entity.BomSectionDecoration, Name: "Embroidery", Unit: unit},
			{Section: entity.BomSectionOther, Name: "Misc", Unit: unit},
		},
	})
	require.NoError(t, err, "widened BOM sections (decoration/other) must pass the live CHECK")
	t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", tcID) })

	card, err := s.TechCards().GetTechCardById(ctx, tcID)
	require.NoError(t, err)
	got := map[entity.TechCardBomSection]bool{}
	for _, b := range card.BomItems {
		got[b.Section] = true
	}
	require.True(t, got[entity.BomSectionDecoration], "decoration BOM line round-trips")
	require.True(t, got[entity.BomSectionOther], "other BOM line round-trips")
}

// TestMaterialReturnCostedPricing (g25-14): a return is priced at the average of the COSTED
// outstanding issues only — an uncosted issue contributes to the return CAP but must not dilute the
// return price toward zero (the units that left with a known cost come back at that cost).
func TestMaterialReturnCostedPricing(t *testing.T) {
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

	nd := func(v string) decimal.NullDecimal {
		return decimal.NullDecimal{Decimal: decimal.RequireFromString(v), Valid: true}
	}
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	MS := s.MaterialStock()

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{Name: "NF RetPrice Fabric", Section: "fabric", Unit: ns("m")})
	require.NoError(t, err)
	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "NF RetPrice Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("NF-RETP-1"), MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	runID, err := s.ProductionRuns().CreateProductionRun(ctx, &entity.ProductionRunInsert{
		TechCardId: tcID, Status: entity.ProductionRunInProgress,
		Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 20}},
	})
	require.NoError(t, err)
	runRef := sql.NullInt32{Int32: int32(runID), Valid: true}
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", runID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM tech_card WHERE id = ?", tcID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	// UNPRICED receipt → no average; issuing 5 books an UNCOSTED issue movement.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(10)})
	require.NoError(t, err)
	uncosted, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(5), ProductionRunId: runRef})
	require.NoError(t, err)
	require.False(t, uncosted.UnitCostBase.Valid, "no average yet → uncosted issue")

	// priced receipt 10 @ 4 sets the average to 4; issue 5 more books a COSTED issue @ 4.
	_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{MaterialId: matID, Quantity: decimal.NewFromInt(10), UnitCost: nd("4"), Currency: "EUR"})
	require.NoError(t, err)
	costed, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(5), ProductionRunId: runRef})
	require.NoError(t, err)
	require.True(t, costed.UnitCostBase.Valid && costed.UnitCostBase.Decimal.Equal(decimal.NewFromInt(4)))

	// outstanding: qty 10 (5 uncosted + 5 costed), costed value 20. A blended val/qty would price the
	// return at 2; the costed-only rule prices it at 4 — what the costed units actually left at.
	ret, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(3), ProductionRunId: runRef, IsReturn: true})
	require.NoError(t, err)
	require.True(t, ret.UnitCostBase.Valid && ret.UnitCostBase.Decimal.Equal(decimal.NewFromInt(4)),
		"return priced at the costed outstanding average (4), got %v", ret.UnitCostBase)

	// the cap still counts UNCOSTED issues too: 10 out, 3 returned → 7 remain; returning 8 is refused.
	_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(8), ProductionRunId: runRef, IsReturn: true})
	require.ErrorIs(t, err, entity.ErrExcessiveMaterialReturn)

	// a return NOT covered by costed issues (4 back while only 2 costed remain out) can't be honestly
	// priced — it books uncosted rather than minting value at the costed average.
	ret2, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{MaterialId: matID, Quantity: decimal.NewFromInt(4), ProductionRunId: runRef, IsReturn: true})
	require.NoError(t, err)
	require.False(t, ret2.UnitCostBase.Valid, "an uncovered return books uncosted (conservative)")
}
