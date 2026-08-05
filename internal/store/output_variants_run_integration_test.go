package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestOutputVariantProductionRuns exercises phase 3 of the auxiliary colour-variant work (migration
// 0253): a run against a variant-mode aux card plans one product-less line PER COLOUR and, at
// receipt, books each colour's good units into ITS OWN warehouse bucket — own moving average, own
// material_price point — at the run's single blended unit cost.
//
// Everything here is SQL and transaction behaviour no unit test can reach: the new column's
// round trip through the keyed line diff, the per-colour booking loop inside the receipt
// transaction, the stale-registry refusals taken under the run lock, and the delete guard the FK
// backs. The scalar path of a card with NO colours is deliberately covered elsewhere
// (TestAuxiliaryProductionRun) and must stay green unchanged — that is the byte-identical claim.
//
// SAFE ONLY against a local container DSN: this suite's TestMain drops every table on cleanup
// (mysql_test.go), so a prod/beta DSN would be destructive. The guard below refuses to run unless
// the DSN targets a container.
func TestOutputVariantProductionRuns(t *testing.T) {
	if os.Getenv("CI") == "" &&
		!strings.Contains(testCfg.DSN, "127.0.0.1") &&
		!strings.Contains(testCfg.DSN, "localhost") {
		t.Skip("skipping outside CI unless the DSN targets a local container (avoids the configured prod DB)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	cfg := *testCfg
	cfg.Automigrate = true
	s, err := NewForTest(ctx, cfg)
	require.NoError(t, err)
	defer s.Close()

	// The receipt's material landing labels its price point with the base currency from the consts
	// cache, exactly as it does in production.
	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	T := s.TechCards()
	P := s.ProductionRuns()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	unique := func(tag string) string { return fmt.Sprintf("%s-%d", tag, time.Now().UnixNano()%1_000_000_000) }

	// fixture removes what a subtest created in FK order: runs (their lines RESTRICT the variant),
	// then variants (they RESTRICT the material), then cards, then buckets.
	type fixture struct {
		runs      []int
		cards     []int
		materials []int
	}
	newFixture := func(t *testing.T) *fixture {
		f := &fixture{}
		t.Cleanup(func() {
			bg := context.Background()
			for _, id := range f.runs {
				_, _ = testDB.ExecContext(bg, "DELETE FROM material_stock_movement WHERE production_run_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM production_run_receipt_line WHERE receipt_id IN (SELECT id FROM production_run_receipt WHERE run_id = ?)", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM production_run_receipt WHERE run_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM production_run_line WHERE run_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM production_run WHERE id = ?", id)
			}
			for _, id := range f.cards {
				_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card_output_variant WHERE tech_card_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM tech_card WHERE id = ?", id)
			}
			for _, id := range f.materials {
				_, _ = testDB.ExecContext(bg, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM material_price WHERE material_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM material_stock WHERE material_id = ?", id)
				_, _ = testDB.ExecContext(bg, "DELETE FROM material WHERE id = ?", id)
			}
		})
		return f
	}

	mkMaterial := func(f *fixture, name string) int {
		id, err := T.CreateMaterial(ctx, &entity.MaterialInsert{
			Name: unique(name), Section: "packaging", MaterialClass: "packaging",
			Unit: ns("pcs"), Purpose: "production", CreatedBy: "tester", UpdatedBy: "tester",
		})
		require.NoError(t, err)
		f.materials = append(f.materials, id)
		return id
	}

	mkAuxCard := func(f *fixture, name string) int {
		id, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: unique(name), Stage: entity.TechCardStageProto, StyleNumber: ns(unique("OVR")),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: entity.TechCardPurposeAuxiliary,
		})
		require.NoError(t, err)
		f.cards = append(f.cards, id)
		return id
	}

	mkVariant := func(cardID int, colour string, materialID int) int {
		id, err := T.UpsertOutputVariant(ctx, cardID,
			entity.TechCardOutputVariantInsert{ColorCode: colour, MaterialId: materialID, Active: true}, "tester")
		require.NoError(t, err)
		return id
	}
	retireVariant := func(cardID, variantID int, colour string, materialID int) {
		_, err := T.UpsertOutputVariant(ctx, cardID, entity.TechCardOutputVariantInsert{
			Id: variantID, ColorCode: colour, MaterialId: materialID, Active: false,
		}, "tester")
		require.NoError(t, err)
	}

	variantLine := func(variantID, planned, received int) entity.ProductionRunLine {
		ln := entity.ProductionRunLine{
			OutputVariantId: sql.NullInt32{Int32: int32(variantID), Valid: true},
			PlannedQty:      planned,
		}
		if received > 0 {
			ln.ReceivedQty = sql.NullInt64{Int64: int64(received), Valid: true}
		}
		return ln
	}

	// materialsCost is the one manual article every run below carries, so the receipt has an actual
	// total to divide by its good units — the blended unit cost the colours share.
	materialsCost := func(base int64) entity.ProductionRunCost {
		return entity.ProductionRunCost{
			Kind: entity.ProductionRunCostMaterials, Amount: decimal.NewFromInt(base), Currency: "EUR",
			AmountBase: decimal.NullDecimal{Decimal: decimal.NewFromInt(base), Valid: true},
		}
	}

	mkRun := func(f *fixture, cardID int, cost entity.ProductionRunCost, lines ...entity.ProductionRunLine) int {
		id, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: lines,
			Costs: []entity.ProductionRunCost{cost},
		})
		require.NoError(t, err)
		f.runs = append(f.runs, id)
		return id
	}

	// receiveAuxRun posts the FINAL receipt of an aux run the way the handler does: counts from the
	// stored rollups and nothing else. WHERE the output lands is the store's decision, taken from the
	// card's registry inside the transaction — there is deliberately no destination to pass in.
	receiveAuxRun := func(t *testing.T, runID int) (*entity.PostProductionRunReceiptResult, error) {
		t.Helper()
		run, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		lines := make([]entity.ProductionRunReceiptLineInput, 0, len(run.Lines))
		for _, ln := range run.Lines {
			good, defect := int(ln.ReceivedQty.Int64), int(ln.DefectQty.Int64)
			if good <= 0 && defect <= 0 {
				continue
			}
			lines = append(lines, entity.ProductionRunReceiptLineInput{LineKey: ln.LineKey, GoodQty: good, DefectQty: defect})
		}
		key, err := entity.MintProductionRunLineKey()
		require.NoError(t, err)
		return P.PostProductionRunReceipt(ctx, entity.PostProductionRunReceiptParams{
			RunID: runID, Lines: lines, IdempotencyKey: key,
			RequestHash:  dto.HashProductionRunReceiptPayload(runID, lines, "", false, true),
			Final:        true,
			LegacyTotals: true,
			Username:     "tester",
			BaseCurrency: "EUR",
			Aux:          true,
		})
	}

	// movementsByMaterial reads the receipt_production landings a run produced, keyed by bucket.
	type landing struct{ qty, unitBase string }
	movementsByMaterial := func(t *testing.T, runID int) map[int]landing {
		t.Helper()
		rows, err := testDB.QueryContext(ctx, `
			SELECT material_id, quantity, COALESCE(unit_cost_base, '') FROM material_stock_movement
			WHERE production_run_id = ? AND movement_type = 'receipt_production' ORDER BY material_id`, runID)
		require.NoError(t, err)
		defer rows.Close()
		out := map[int]landing{}
		for rows.Next() {
			var mid int
			var l landing
			require.NoError(t, rows.Scan(&mid, &l.qty, &l.unitBase))
			out[mid] = l
		}
		require.NoError(t, rows.Err())
		return out
	}

	// The schema itself: the column, the RESTRICTing FK and the product-XOR-colour CHECK all landed.
	// Everything below reads as a behaviour test only if these three exist.
	t.Run("migration_0253_landed_the_column_the_fk_and_the_check", func(t *testing.T) {
		var cols int
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM information_schema.COLUMNS
			WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
			  AND COLUMN_NAME = 'output_variant_id' AND IS_NULLABLE = 'YES'`).Scan(&cols))
		require.Equal(t, 1, cols, "output_variant_id must exist and be NULLable")

		for _, name := range []string{"fk_prl_output_variant", "chk_prl_variant_xor"} {
			var n int
			require.NoError(t, testDB.QueryRowContext(ctx, `
				SELECT COUNT(*) FROM information_schema.TABLE_CONSTRAINTS
				WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'production_run_line'
				  AND CONSTRAINT_NAME = ?`, name).Scan(&n))
			require.Equal(t, 1, n, "constraint %s must exist", name)
		}
		var rule string
		require.NoError(t, testDB.QueryRowContext(ctx, `
			SELECT DELETE_RULE FROM information_schema.REFERENTIAL_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = DATABASE() AND CONSTRAINT_NAME = 'fk_prl_output_variant'`).Scan(&rule))
		require.Equal(t, "RESTRICT", rule, "a colour a run planned into must not be deletable out from under it")
	})

	// (t1) The heart of phase 3: two colours, two buckets, two independent moving averages, one
	// blended unit cost.
	t.Run("a_two_colour_run_books_each_colour_into_its_own_bucket", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Kofr")
		blkMat := mkMaterial(f, "OVR blk")
		whtMat := mkMaterial(f, "OVR wht")
		blk := mkVariant(cardID, "BLK", blkMat)
		wht := mkVariant(cardID, "WHT", whtMat)

		// The white bucket already holds stock at a DIFFERENT cost, so "each colour's own moving
		// average" is a claim with teeth: the two buckets must land on different averages from the
		// same receipt.
		_, err := testDB.ExecContext(ctx, `
			INSERT INTO material_stock (material_id, on_hand, avg_unit_cost_base) VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE on_hand = VALUES(on_hand), avg_unit_cost_base = VALUES(avg_unit_cost_base)`,
			whtMat, "10.000", "5.00")
		require.NoError(t, err)

		// 200 base over 100 good units = a blended 2.00 per unit, shared by both colours.
		runID := mkRun(f, cardID, materialsCost(200),
			variantLine(blk, 60, 60), variantLine(wht, 40, 40))

		// The colours round-trip through the plan grid (the keyed line diff carries the new column).
		planned, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Len(t, planned.Lines, 2)
		gotColours := map[int32]int{}
		for _, ln := range planned.Lines {
			require.True(t, ln.OutputVariantId.Valid, "a colour line must read back with its colour")
			require.False(t, ln.ProductId.Valid, "a colour line names no product")
			require.Zero(t, ln.SizeId, "a colour line needs no size")
			gotColours[ln.OutputVariantId.Int32] = ln.PlannedQty
		}
		require.Equal(t, map[int32]int{int32(blk): 60, int32(wht): 40}, gotColours)

		res, err := receiveAuxRun(t, runID)
		require.NoError(t, err)
		require.Positive(t, res.ReceiptID)

		// TWO receipt_production movements against this run, one per colour, each for its own units.
		byMat := movementsByMaterial(t, runID)
		require.Len(t, byMat, 2, "one landing per colour, not one blended landing")
		require.True(t, decimal.RequireFromString(byMat[blkMat].qty).Equal(decimal.NewFromInt(60)),
			"black landed its own 60, got %s", byMat[blkMat].qty)
		require.True(t, decimal.RequireFromString(byMat[whtMat].qty).Equal(decimal.NewFromInt(40)),
			"white landed its own 40, got %s", byMat[whtMat].qty)
		require.True(t, decimal.RequireFromString(byMat[blkMat].unitBase).Equal(decimal.NewFromInt(2)))
		require.True(t, decimal.RequireFromString(byMat[whtMat].unitBase).Equal(decimal.NewFromInt(2)),
			"both colours carry the run's ONE blended unit cost")

		// Each bucket's on-hand and moving average moved independently.
		blkStock, err := s.MaterialStock().GetMaterialStock(ctx, blkMat)
		require.NoError(t, err)
		require.True(t, blkStock.OnHand.Equal(decimal.NewFromInt(60)), "black on-hand 60, got %s", blkStock.OnHand)
		require.True(t, blkStock.AvgUnitCostBase.Valid && blkStock.AvgUnitCostBase.Decimal.Equal(decimal.NewFromInt(2)),
			"an empty bucket takes the receipt cost as its average, got %v", blkStock.AvgUnitCostBase)

		whtStock, err := s.MaterialStock().GetMaterialStock(ctx, whtMat)
		require.NoError(t, err)
		require.True(t, whtStock.OnHand.Equal(decimal.NewFromInt(50)), "white on-hand 10 + 40, got %s", whtStock.OnHand)
		// (10 × 5 + 40 × 2) / 50 = 2.6 — the white bucket's own history, untouched by the black one.
		require.True(t, whtStock.AvgUnitCostBase.Valid &&
			whtStock.AvgUnitCostBase.Decimal.Equal(decimal.RequireFromString("2.6")),
			"white blends the receipt into ITS prior average, got %v", whtStock.AvgUnitCostBase)

		// A production_run price point per colour: each bucket's cost history is its own.
		for _, mid := range []int{blkMat, whtMat} {
			prices, err := T.ListMaterialPrices(ctx, mid)
			require.NoError(t, err)
			require.Len(t, prices, 1, "material %d", mid)
			require.Equal(t, entity.MaterialPriceSourceProductionRun, prices[0].Source)
			require.True(t, prices[0].Price.Equal(decimal.NewFromInt(2)))
		}

		// The run itself: received, one receipt valued at the blended cost, rollups per colour.
		got, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, entity.ProductionRunReceived, got.Status)
		require.True(t, got.ReceivedAt.Valid)
		require.Len(t, got.Receipts, 1)
		require.True(t, got.Receipts[0].HasBase)
		require.True(t, got.Receipts[0].UnitCostBase.Decimal.Equal(decimal.NewFromInt(2)))
		require.Len(t, got.Receipts[0].Lines, 2, "one receipt line per counted colour")
		rollups := map[int32]int64{}
		for _, ln := range got.Lines {
			rollups[ln.OutputVariantId.Int32] = ln.ReceivedQty.Int64
		}
		require.Equal(t, map[int32]int64{int32(blk): 60, int32(wht): 40}, rollups)

		// The recon panel behaves exactly as it does for a single-output aux run: several buckets
		// change nothing about it. The units check is FORCED green (an aux run's output lives in the
		// material warehouse, never in the product-stock journal, so comparing against that journal
		// would be red on every aux run forever), and the money checks are material-agnostic — they
		// count receipts and journal entries, not buckets. costs_capitalised is the one check that is
		// legitimately not green here, and for a reason that predates colours entirely: no accounting
		// worker has run in this test, so nothing has capitalised yet.
		recon := map[string]entity.ProductionRunReconCheck{}
		for _, c := range got.Recon {
			recon[c.Key] = c
		}
		require.Len(t, recon, 3)
		units := recon["units_receipts_vs_stock_journal"]
		require.True(t, units.Ok)
		require.Contains(t, units.Detail, "material warehouse")
		require.True(t, recon["money_posted_vs_entries"].Ok,
			"no receipt claims to be posted, so nothing can be missing an entry")
		costs := recon["costs_capitalised"]
		require.False(t, costs.Ok, "nothing posted yet, so the 200 is still pending capitalisation")
		require.Contains(t, costs.Detail, "posts with the next receipt or worker tick",
			"the shortfall must read as worker lag, not as a colour-related discrepancy")
		require.Equal(t, "200", costs.Expected)
	})

	// (F3) A HALF-COLOURED grid never gets planned. It is receivable nowhere — the receipt has one
	// bucket per colour and no home at all for a colourless line — so the refusal belongs at the one
	// moment the run can still be edited, not at the receipt an aux run can never unwind.
	t.Run("a_half_coloured_grid_is_refused_at_plan_time", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Mixed")
		blkMat := mkMaterial(f, "OVR mixed blk")
		blk := mkVariant(cardID, "BLK", blkMat)

		_, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{
				variantLine(blk, 50, 0),
				// The legacy colourless aux line, left on the grid beside a colour.
				{PlannedQty: 50},
			},
		})
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantMixedGrid)

		// The same refusal guards an UPDATE that introduces the mix.
		runID := mkRun(f, cardID, materialsCost(100), variantLine(blk, 50, 0))
		stored, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		ins := stored.ProductionRunInsert
		ins.Lines = append(ins.Lines, entity.ProductionRunLine{PlannedQty: 50})
		require.ErrorIs(t, P.UpdateProductionRunPreservingCosts(ctx, runID, &ins, stored.LockVersion),
			entity.ErrProductionRunLineVariantMixedGrid)
	})

	// (F1, the inverted t4) A colour retired between plan and receipt must still RECEIVE, into its
	// own bucket. Retirement stops new plans; it does not abandon a batch already on the sewing
	// floor — and an aux run has no partial, no reversal and no cancel-with-issued-material to
	// escape through, so refusing here would strand the run and its material forever.
	t.Run("a_colour_retired_between_plan_and_receipt_still_receives_into_its_bucket", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Retired mid-flight")
		blkMat := mkMaterial(f, "OVR retire blk")
		whtMat := mkMaterial(f, "OVR retire wht")
		blk := mkVariant(cardID, "BLK", blkMat)
		wht := mkVariant(cardID, "WHT", whtMat)

		runID := mkRun(f, cardID, materialsCost(200),
			variantLine(blk, 60, 60), variantLine(wht, 40, 40))

		retireVariant(cardID, wht, "WHT", whtMat)

		_, err := receiveAuxRun(t, runID)
		require.NoError(t, err, "a retirement must not strand a run that is already sewn")

		byMat := movementsByMaterial(t, runID)
		require.Len(t, byMat, 2)
		require.True(t, decimal.RequireFromString(byMat[whtMat].qty).Equal(decimal.NewFromInt(40)),
			"the retired colour's units land in the retired colour's own bucket, not in the live one")
		require.True(t, decimal.RequireFromString(byMat[blkMat].qty).Equal(decimal.NewFromInt(60)))

		got, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		require.Equal(t, entity.ProductionRunReceived, got.Status)
	})

	// (F1, the surviving refusal) A colour that is NOT this card's is the real race — a row that
	// moved out from under the run, or an id that never belonged to it. That one still refuses.
	t.Run("a_foreign_colour_on_a_line_is_a_concurrent_modification", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Ours")
		otherID := mkAuxCard(f, "Theirs")
		blkMat := mkMaterial(f, "OVR foreign ours")
		theirMat := mkMaterial(f, "OVR foreign theirs")
		blk := mkVariant(cardID, "BLK", blkMat)
		foreign := mkVariant(otherID, "BLK", theirMat)

		runID := mkRun(f, cardID, materialsCost(100), variantLine(blk, 50, 50))
		// Plan-time validation refuses a foreign colour, so the only way into this state is to write
		// around the store — which is exactly the corruption/race the receipt must not book through.
		_, err := testDB.ExecContext(ctx,
			`UPDATE production_run_line SET output_variant_id = ? WHERE run_id = ?`, foreign, runID)
		require.NoError(t, err)

		_, err = receiveAuxRun(t, runID)
		require.ErrorIs(t, err, entity.ErrProductionRunConcurrentModification)

		var movements int
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM material_stock_movement WHERE production_run_id = ?`, runID).Scan(&movements))
		require.Zero(t, movements, "a refused receipt books no stock at all")
	})

	// (F2) A colour RE-POINTED at another bucket between plan and receipt books into the bucket the
	// card produces into NOW. Re-pointing is a supported edit; booking the old bucket would move
	// stock, its moving average, its price history and the M2 journal entry somewhere no aux receipt
	// can ever unwind.
	t.Run("a_colour_re_bucketed_mid_flight_books_into_the_fresh_material", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Re-bucketed")
		oldMat := mkMaterial(f, "OVR rebucket old")
		newMat := mkMaterial(f, "OVR rebucket new")
		blk := mkVariant(cardID, "BLK", oldMat)

		runID := mkRun(f, cardID, materialsCost(100), variantLine(blk, 50, 50))

		// The operator moves the colour to a different warehouse article after the run was planned.
		_, err := T.UpsertOutputVariant(ctx, cardID, entity.TechCardOutputVariantInsert{
			Id: blk, ColorCode: "BLK", MaterialId: newMat, Active: true,
		}, "tester")
		require.NoError(t, err)

		_, err = receiveAuxRun(t, runID)
		require.NoError(t, err)

		byMat := movementsByMaterial(t, runID)
		require.Len(t, byMat, 1)
		require.Contains(t, byMat, newMat, "the receipt books the bucket the card produces into NOW")
		require.NotContains(t, byMat, oldMat, "the abandoned bucket must not receive anything")
		st, err := s.MaterialStock().GetMaterialStock(ctx, newMat)
		require.NoError(t, err)
		require.True(t, st.OnHand.Equal(decimal.NewFromInt(50)))
	})

	// (F2, legacy half) The same freshness rule for a single-output card: the run lock does not cover
	// the CARD row, so output_material_id is re-read in the transaction too.
	t.Run("a_legacy_card_re_pointed_mid_flight_books_into_the_fresh_output_material", func(t *testing.T) {
		f := newFixture(t)
		oldMat := mkMaterial(f, "OVR legacy old")
		newMat := mkMaterial(f, "OVR legacy new")
		cardID, err := T.AddTechCard(ctx, &entity.TechCardInsert{
			Name: unique("Legacy repoint"), Stage: entity.TechCardStageProto, StyleNumber: ns(unique("OVR")),
			MeasurementUnit: entity.TechCardUnitMm, ApprovalState: entity.TechCardApprovalDraft,
			Purpose: entity.TechCardPurposeAuxiliary, OutputMaterialId: sql.NullInt64{Int64: int64(oldMat), Valid: true},
		})
		require.NoError(t, err)
		f.cards = append(f.cards, cardID)

		runID := mkRun(f, cardID, materialsCost(100),
			entity.ProductionRunLine{PlannedQty: 50, ReceivedQty: sql.NullInt64{Int64: 50, Valid: true}})

		_, err = testDB.ExecContext(ctx, `UPDATE tech_card SET output_material_id = ? WHERE id = ?`, newMat, cardID)
		require.NoError(t, err)

		_, err = receiveAuxRun(t, runID)
		require.NoError(t, err)
		byMat := movementsByMaterial(t, runID)
		require.Len(t, byMat, 1)
		require.Contains(t, byMat, newMat)
	})

	// Colours registered AFTER a colourless grid was planned: the card now produces by colour and
	// the grid names no bucket at all. This is the direction that would otherwise book every colour's
	// output into output_material_id, and it is the one arm of the mixed-grid rule that plan-time
	// validation cannot reach (the grid predates the colours).
	t.Run("colours_registered_after_planning_refuse_the_colourless_grid", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Colours came later")
		blkMat := mkMaterial(f, "OVR later blk")

		runID := mkRun(f, cardID, materialsCost(100),
			entity.ProductionRunLine{PlannedQty: 50, ReceivedQty: sql.NullInt64{Int64: 50, Valid: true}})

		mkVariant(cardID, "BLK", blkMat)
		_, err := receiveAuxRun(t, runID)
		require.ErrorIs(t, err, entity.ErrProductionRunConcurrentModification)
	})

	// (F6) A Valid-but-zero colour id is "unset", not a literal 0 heading for the foreign key.
	t.Run("a_valid_but_zero_colour_id_is_stored_as_no_colour", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Zero id")

		runID, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{
				OutputVariantId: sql.NullInt32{Int32: 0, Valid: true}, PlannedQty: 10,
			}},
		})
		require.NoError(t, err, "a zero id must not reach the FK as a literal 0 (raw 1452)")
		f.runs = append(f.runs, runID)

		var stored sql.NullInt32
		require.NoError(t, testDB.QueryRowContext(ctx,
			`SELECT output_variant_id FROM production_run_line WHERE run_id = ?`, runID).Scan(&stored))
		require.False(t, stored.Valid, "0 means unset, exactly as it does for size_id")
	})

	// (t5) A colour a run planned into cannot be deleted; deactivation is the retirement that keeps
	// the history. Once the run is gone the colour is deletable again — which is what keeps the
	// purpose lock escapable.
	t.Run("a_colour_a_run_references_cannot_be_deleted_until_the_run_is", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Referenced colour")
		blkMat := mkMaterial(f, "OVR ref blk")
		blk := mkVariant(cardID, "BLK", blkMat)

		runID := mkRun(f, cardID, materialsCost(100), variantLine(blk, 50, 0))

		err := T.DeleteOutputVariant(ctx, blk)
		require.Error(t, err)
		require.ErrorIs(t, err, entity.ErrOutputVariantReferencedByRun)
		require.Contains(t, err.Error(), "1 production run line",
			"the refusal must count what blocks it, not just say 'referenced'")
		require.Contains(t, err.Error(), fmt.Sprintf("run(s) %d", runID),
			"a bare count is a dead end — the refusal must NAME the run to go and look at")
		require.Contains(t, err.Error(), "cancelled run pins it too",
			"the commonest blocker is a forgotten cancelled run; the message must say so")
		require.Contains(t, err.Error(), "deactivate it instead")

		// A CANCELLED run still pins the colour — which is exactly why the message says so.
		cancelled, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		cancelIns := cancelled.ProductionRunInsert
		cancelIns.Status = entity.ProductionRunCancelled
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, &cancelIns, cancelled.LockVersion))
		require.ErrorIs(t, T.DeleteOutputVariant(ctx, blk), entity.ErrOutputVariantReferencedByRun)

		// Deactivating always works — that is the point of the message.
		retireVariant(cardID, blk, "BLK", blkMat)
		vs, err := T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Len(t, vs, 1)
		require.False(t, vs[0].Active)

		// Deleting the run releases the line (production_run_line cascades with the run), and the
		// colour becomes deletable — no receipt was ever posted, so the run is deletable too.
		require.NoError(t, P.DeleteProductionRun(ctx, runID))
		require.NoError(t, T.DeleteOutputVariant(ctx, blk))
		vs, err = T.ListOutputVariants(ctx, cardID)
		require.NoError(t, err)
		require.Empty(t, vs)
	})

	// Plan-time linkage. A colour must be one of THIS card's, and a NEW line must name one the card
	// still makes — while a line that already referenced a colour survives its retirement, so an
	// in-flight run never freezes because someone retired a colour elsewhere.
	t.Run("plan_time_colour_linkage_and_the_retirement_grandfather", func(t *testing.T) {
		f := newFixture(t)
		cardID := mkAuxCard(f, "Planner")
		otherID := mkAuxCard(f, "Someone else")
		blkMat := mkMaterial(f, "OVR plan blk")
		whtMat := mkMaterial(f, "OVR plan wht")
		foreignMat := mkMaterial(f, "OVR plan foreign")
		blk := mkVariant(cardID, "BLK", blkMat)
		wht := mkVariant(cardID, "WHT", whtMat)
		foreign := mkVariant(otherID, "BLK", foreignMat)

		// Another card's colour: absolute refusal, no grandfathering, ever.
		_, err := P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{variantLine(foreign, 10, 0)},
		})
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantUnlinked)

		// A colour that does not exist at all reads the same way.
		_, err = P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{variantLine(999_000_000, 10, 0)},
		})
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantUnlinked)

		// Plan a real run on both colours, THEN retire one of them.
		runID := mkRun(f, cardID, materialsCost(200), variantLine(blk, 60, 0), variantLine(wht, 40, 0))
		retireVariant(cardID, wht, "WHT", whtMat)

		// A NEW run cannot plan the retired colour.
		_, err = P.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: cardID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{variantLine(wht, 10, 0)},
		})
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantRetired)

		// The EXISTING run still saves, retired colour and all: an edit of its notes must not be a
		// hostage to a retirement it did not ask for.
		stored, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		ins := stored.ProductionRunInsert
		ins.Notes = ns("still editable")
		require.NoError(t, P.UpdateProductionRunPreservingCosts(ctx, runID, &ins, stored.LockVersion))

		// But it cannot ADD a line on a colour the card no longer makes: grandfathering covers what
		// the run already references, not a fresh plan smuggled in through an update.
		reread, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		other := mkVariant(cardID, "GRY", mkMaterial(f, "OVR plan gry"))
		retireVariant(cardID, other, "GRY", 0)
		ins2 := reread.ProductionRunInsert
		ins2.Lines = append(ins2.Lines, variantLine(other, 5, 0))
		err = P.UpdateProductionRunPreservingCosts(ctx, runID, &ins2, reread.LockVersion)
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantRetired)

		// (F4) And the exemption is per LINE, not per run: the run already produces the retired WHT,
		// but that does not license a SECOND, brand-new line on the same retired colour. A run-scoped
		// exemption would have let this through.
		reread2, err := P.GetProductionRun(ctx, runID)
		require.NoError(t, err)
		ins3 := reread2.ProductionRunInsert
		ins3.Lines = append(ins3.Lines, variantLine(wht, 7, 0))
		err = P.UpdateProductionRunPreservingCosts(ctx, runID, &ins3, reread2.LockVersion)
		require.ErrorIs(t, err, entity.ErrProductionRunLineVariantRetired,
			"a fresh line on an already-referenced retired colour is still a fresh plan")
	})
}
