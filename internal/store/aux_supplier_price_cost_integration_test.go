package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// TestAuxSupplierPriceCost is the money path of a bought aux card: its supplier price × the plan is
// a cost article from the first save (1.80 × 500 → 900), it follows the plan while untouched, an
// operator edit takes it over for good, no price means no article, and the receipt values the good
// units off it (900 ÷ 450 good → 2.00 into the output material's stock and moving average).
func TestAuxSupplierPriceCost(t *testing.T) {
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
	base := cache.GetBaseCurrency()
	require.Equal(t, "EUR", base, "the fixture prices in the base currency")

	dec := decimal.RequireFromString
	T, P := s.TechCards(), s.ProductionRuns()

	material := func(name string) int {
		t.Helper()
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{Name: name, Section: "packaging",
			Unit: sql.NullString{String: "pc", Valid: true}})
		require.NoError(t, err)
		t.Cleanup(func() {
			cctx := context.Background()
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material_price WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", id)
		})
		return id
	}
	boughtCard := func(style string, output int) int {
		t.Helper()
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: style, Stage: entity.TechCardStageProto,
			StyleNumber:      sql.NullString{String: style, Valid: true},
			MeasurementUnit:  entity.TechCardUnitMm,
			ApprovalState:    entity.TechCardApprovalDraft,
			Purpose:          entity.TechCardPurposeAuxiliary,
			AuxMadeBy:        sql.NullString{String: entity.AuxMadeBySupplier, Valid: true},
			OutputMaterialId: sql.NullInt64{Int64: int64(output), Valid: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = testDB.ExecContext(context.Background(), "DELETE FROM tech_card WHERE id = ?", id) })
		return id
	}
	createRun := func(card, qty int) int {
		t.Helper()
		id, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: card, Status: entity.ProductionRunPlanned, Actor: "tester",
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: qty}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			cctx := context.Background()
			_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run_receipt WHERE run_id = ?", id)
			_, _ = testDB.ExecContext(cctx, "DELETE FROM production_run WHERE id = ?", id)
		})
		return id
	}
	get := func(id int) *entity.ProductionRun {
		t.Helper()
		run, err := P.GetProductionRun(ctx, id)
		require.NoError(t, err)
		return run
	}
	seeds := func(id int) int {
		t.Helper()
		var n int
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM production_run_event WHERE run_id = ? AND event_type = ?",
			id, entity.ProductionRunEventSupplierPriceSeeded).Scan(&n))
		return n
	}
	requireArticle := func(run *entity.ProductionRun, amount string) {
		t.Helper()
		require.Len(t, run.Costs, 1)
		c := run.Costs[0]
		require.Equal(t, entity.ProductionRunCostMaterials, c.Kind)
		require.Equal(t, entity.SupplierPriceCostDescription, c.Description.String)
		require.Equal(t, "EUR", c.Currency)
		require.True(t, c.Amount.Equal(dec(amount)), "amount %s, want %s", c.Amount, amount)
		require.True(t, c.AmountBase.Valid, "folded to base in the same save")
		require.True(t, c.AmountBase.Decimal.Equal(dec(amount)), "base %s, want %s", c.AmountBase.Decimal, amount)
	}
	setPlan := func(run *entity.ProductionRun, qty int) *entity.ProductionRunInsert {
		ins := run.ProductionRunInsert
		ins.Lines = append([]entity.ProductionRunLine(nil), run.Lines...)
		ins.Costs = append([]entity.ProductionRunCost(nil), run.Costs...)
		ins.Lines[0].PlannedQty = qty
		ins.Actor = "tester"
		return &ins
	}
	fx := dto.CostingFx{Base: base}

	bag := material("AUXSP bag")
	require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{
		MaterialId: bag, Price: dec("1.80"), Currency: "EUR",
		ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: entity.MaterialPriceSourceManual,
	}))
	card := boughtCard("AUXSP-BAG", bag)

	t.Run("seeded at create, follows the plan, received at total ÷ good", func(t *testing.T) {
		runID := createRun(card, 500)
		run := get(runID)
		requireArticle(run, "900")
		require.Equal(t, 1, seeds(runID))

		// Cost-blind save (no costing:write) moves the plan → the untouched article follows it.
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, setPlan(run, 600), entity.NoLockVersion()))
		run = get(runID)
		requireArticle(run, "1080")
		require.Equal(t, 2, seeds(runID))

		// A costs save that round-trips the article untouched behaves the same; an unrelated save
		// with the plan unchanged records nothing.
		require.NoError(t, P.UpdateProductionRun(ctx, runID, setPlan(run, 500), entity.NoLockVersion(), fx))
		run = get(runID)
		requireArticle(run, "900")
		require.Equal(t, 3, seeds(runID))
		require.NoError(t, P.UpdateProductionRun(ctx, runID, setPlan(run, 500), entity.NoLockVersion(), fx))
		requireArticle(get(runID), "900")
		require.Equal(t, 3, seeds(runID))

		// 450 good of 500 → 900 ÷ 450 = 2.00 per unit into the bag's stock.
		run = get(runID)
		key, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		lines := []entity.ProductionRunReceiptLineInput{{LineKey: run.Lines[0].LineKey, GoodQty: 450}}
		_, err = P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
			RunID: runID, Lines: lines, IdempotencyKey: key,
			RequestHash: dto.HashProductionRunReceiptPayload(runID, lines, "", false, true),
			Final:       true, Username: "tester", BaseCurrency: base, Aux: true,
		})
		require.NoError(t, err)

		run = get(runID)
		require.Equal(t, entity.ProductionRunReceived, run.Status)
		unit := run.ActualUnitCostBase()
		require.True(t, unit.Valid)
		require.True(t, unit.Decimal.Equal(dec("2")), "unit cost %s", unit.Decimal)
		var receiptUnit decimal.NullDecimal
		require.NoError(t, testDB.QueryRowContext(ctx,
			"SELECT unit_cost_base FROM production_run_receipt WHERE run_id = ?", runID).Scan(&receiptUnit))
		require.True(t, receiptUnit.Valid && receiptUnit.Decimal.Equal(dec("2")), "receipt froze %v", receiptUnit)

		st, err := s.MaterialStock().GetMaterialStock(ctx, bag)
		require.NoError(t, err)
		require.True(t, st.OnHand.Equal(dec("450")), "on hand %s", st.OnHand)
		require.True(t, st.AvgUnitCostBase.Valid && st.AvgUnitCostBase.Decimal.Equal(dec("2")),
			"moving average %v", st.AvgUnitCostBase)

		// A received run is a fact: its costs are not re-seeded (the update is refused outright).
		require.ErrorIs(t, P.UpdateProductionRun(ctx, runID, setPlan(run, 700), entity.NoLockVersion(), fx),
			entity.ErrProductionRunReceivedImmutable)
		requireArticle(get(runID), "900")
	})

	t.Run("an edited article is the operator's: never re-seeded", func(t *testing.T) {
		runID := createRun(card, 500)
		run := get(runID)
		requireArticle(run, "900")

		// The operator corrects it to the invoice in the same save that moves the plan.
		ins := setPlan(run, 600)
		ins.Costs[0].Amount = dec("912.40")
		ins.Costs[0].AmountBase = decimal.NullDecimal{}
		require.NoError(t, P.UpdateProductionRun(ctx, runID, ins, entity.NoLockVersion(), fx))
		run = get(runID)
		requireArticle(run, "912.40")

		// Later plan moves on either path leave it alone, and nothing is recorded.
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, setPlan(run, 700), entity.NoLockVersion()))
		require.NoError(t, P.UpdateProductionRun(ctx, runID, setPlan(get(runID), 800), entity.NoLockVersion(), fx))
		requireArticle(get(runID), "912.40")
		require.Equal(t, 1, seeds(runID))

		// Deleted by the operator → stays deleted after a plan change.
		run = get(runID)
		ins = setPlan(run, 900)
		ins.Costs = nil
		require.NoError(t, P.UpdateProductionRun(ctx, runID, ins, entity.NoLockVersion(), fx))
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, setPlan(get(runID), 950), entity.NoLockVersion()))
		require.Empty(t, get(runID).Costs)
		require.Equal(t, 1, seeds(runID))
	})

	t.Run("no price → no article", func(t *testing.T) {
		unpriced := material("AUXSP unpriced")
		// A production_run price is the run's own output, never a supplier price.
		require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{
			MaterialId: unpriced, Price: dec("3"), Currency: "EUR",
			ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: entity.MaterialPriceSourceProductionRun,
		}))
		runID := createRun(boughtCard("AUXSP-NOPRICE", unpriced), 500)
		run := get(runID)
		require.Empty(t, run.Costs)
		require.Equal(t, 0, seeds(runID))

		// The price arrives later → the next save seeds it.
		require.NoError(t, T.AddMaterialPrice(ctx, entity.MaterialPrice{
			MaterialId: unpriced, Price: dec("1.50"), Currency: "EUR",
			ValidFrom: time.Now().UTC().AddDate(0, 0, -1), Source: entity.MaterialPriceSourcePurchase,
		}))
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, setPlan(run, 500), entity.NoLockVersion()))
		requireArticle(get(runID), "750")
	})

	t.Run("a typed materials article is not counted twice", func(t *testing.T) {
		runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: card, Status: entity.ProductionRunPlanned, Actor: "tester",
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 500}},
			Costs: []entity.ProductionRunCost{{Kind: entity.ProductionRunCostMaterials, Amount: dec("880"), Currency: "EUR",
				AmountBase: decimal.NullDecimal{Decimal: dec("880"), Valid: true}}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", runID)
		})
		run := get(runID)
		require.Len(t, run.Costs, 1)
		require.True(t, run.Costs[0].Amount.Equal(dec("880")))
		require.Equal(t, 0, seeds(runID))
	})
}
