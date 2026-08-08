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

// TestRunFabricReservation is the Ф5б.4 / Ф5б.6 acceptance suite: fabric held by a PRODUCTION RUN,
// written into the SAME ledger that holds packaging for orders (0286).
//
// The probe that decides whether the whole design is right is the first one. openReservedQty sums a
// material's open claims by material_id and never asks who owns them, so a run's hold must start
// depressing available(material) with NO edit to that reader. The suite proves it end to end: every
// availability assertion below goes through MaterialAvailable → openReservedQty, unmodified, and it
// sees run-owned holds. Had that reader needed teaching, one number would have become two
// half-answers and the first caller to forget the second half would sell cloth already held.
//
// Probes are §6 of tmp/production-cutting/spec-f5b-fact-and-reserve.md: 2 (two runs, one cloth),
// 3 (an abandoned run holds, and its age is visible), 4 (norm → lays correction moves available by
// exactly the difference), 5 (cancelling closes run claims and leaves order claims alone), 10
// (deleting a run cascades its claims away and leaves order claims alone), plus the issue-time
// consume and the per-lot read that only the recut check asks for.
//
// Each probe owns its OWN material, so a failure names one mechanism instead of a shared balance.
//
// SAFE ONLY against a local container DSN — see the guard and mysql_test.go / project memory.
func TestRunFabricReservation(t *testing.T) {
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

	di, err := s.Cache().GetDictionaryInfo(ctx)
	require.NoError(t, err)
	hf, err := s.Hero().GetHero(ctx)
	require.NoError(t, err)
	require.NoError(t, cache.InitConsts(ctx, di, hf))

	MS := s.MaterialStock()
	PR := s.ProductionRuns()
	ns := func(v string) sql.NullString { return sql.NullString{String: v, Valid: true} }
	ni := func(v int) sql.NullInt32 { return sql.NullInt32{Int32: int32(v), Valid: true} }
	dec := func(v int64) decimal.Decimal { return decimal.NewFromInt(v) }

	tcID, err := s.TechCards().AddTechCard(ctx, &entity.TechCardInsert{
		Name: "F5B Reservation Style", Stage: entity.TechCardStageProto,
		StyleNumber: ns("F5B-RES-1"), MeasurementUnit: entity.TechCardUnitMm,
		ApprovalState: entity.TechCardApprovalDraft,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.TechCards().DeleteTechCard(context.Background(), tcID) })

	// newFabric creates a fabric article and puts `onHand` metres on the shelf.
	newFabric := func(t *testing.T, name string, onHand int64) int {
		t.Helper()
		id, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
			Name: name, Section: "fabric", Unit: ns("m"),
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			c := context.Background()
			_, _ = testDB.ExecContext(c, "DELETE FROM material_stock_movement WHERE material_id = ?", id)
			_, _ = testDB.ExecContext(c, "DELETE FROM material WHERE id = ?", id)
		})
		if onHand > 0 {
			_, err = MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
				MaterialId: id, Quantity: dec(onHand),
				UnitCost: decimal.NullDecimal{Decimal: dec(5), Valid: true}, Currency: "EUR",
			})
			require.NoError(t, err)
		}
		return id
	}

	newRun := func(t *testing.T) int {
		t.Helper()
		id, err := PR.CreateProductionRun(ctx, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunInProgress,
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
		})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM production_run WHERE id = ?", id)
		})
		return id
	}

	newOrderClaim := func(t *testing.T, materialID int, qty int64) int {
		t.Helper()
		orderID := seedOrder(ctx, t)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM customer_order WHERE id = ?", orderID)
		})
		_, err := testDB.ExecContext(ctx, `
			INSERT INTO material_reservation_ledger (material_id, order_id, qty, event, claim_key, created_by)
			VALUES (?, ?, ?, 'reserve', ?, 'tester')`,
			materialID, orderID, qty, entity.PackagingClaimKey(orderID, materialID))
		require.NoError(t, err)
		return orderID
	}

	// hold is the requirement map a caller hands the writer: material → outstanding quantity.
	hold := func(materialID int, qty int64) map[int]entity.RunMaterialRequirement {
		return map[int]entity.RunMaterialRequirement{materialID: {Qty: dec(qty)}}
	}

	// available reads the soft availability through the UNTOUCHED reader chain
	// MaterialAvailable → openReservedQty.
	available := func(t *testing.T, materialID int) entity.MaterialAvailability {
		t.Helper()
		av, err := MS.MaterialAvailable(ctx, materialID)
		require.NoError(t, err)
		return av
	}

	// ledgerEvents returns every (claim_key, event, qty) row of a run, so the tests can assert on the
	// SHAPE of the append-only trail — a correction must be a release plus a new-generation reserve,
	// never an in-place rewrite.
	type ledgerRow struct {
		claimKey string
		event    string
		qty      decimal.Decimal
	}
	ledgerEvents := func(t *testing.T, runID int) []ledgerRow {
		t.Helper()
		rows, err := testDB.QueryContext(ctx, `
			SELECT claim_key, event, qty FROM material_reservation_ledger
			WHERE run_id = ? ORDER BY id`, runID)
		require.NoError(t, err)
		defer rows.Close()
		out := []ledgerRow{}
		for rows.Next() {
			var r ledgerRow
			var qty string
			require.NoError(t, rows.Scan(&r.claimKey, &r.event, &qty))
			r.qty, err = decimal.NewFromString(qty)
			require.NoError(t, err)
			out = append(out, r)
		}
		require.NoError(t, rows.Err())
		return out
	}
	// requireRow compares a ledger row by VALUE. require.Equal on a decimal.Decimal compares its
	// internal representation, where 60 and 60.000 differ — the column is DECIMAL(12,3), so every
	// round-trip comes back scaled and a struct-equality assertion would fail on identical money.
	requireRow := func(t *testing.T, got ledgerRow, claimKey, event string, qty int64, msg string) {
		t.Helper()
		require.Equal(t, claimKey, got.claimKey, msg)
		require.Equal(t, event, got.event, msg)
		require.True(t, got.qty.Equal(dec(qty)), "%s: expected qty %d, got %s", msg, qty, got.qty)
	}

	// ── Probe 2 ──────────────────────────────────────────────────────────────────────────────────
	// Two runs on one cloth. Before Ф5б.4 both read "there is enough", because nothing held fabric
	// at all. The whole fix is that the FIRST run's hold is visible to everyone reading the material.
	t.Run("probe 2: a run's hold makes the next run see a shortage, with openReservedQty untouched", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric P2", 100)
		runA, runB := newRun(t), newRun(t)

		require.True(t, available(t, mat).Available.Equal(dec(100)), "nothing held yet")

		require.NoError(t, MS.SetRunMaterialReservations(ctx, runA, hold(mat, 60), "tester"))

		// This assertion is the design's own verification (Р1). MaterialAvailable calls
		// openReservedQty, which was NOT edited for this phase — it sums by material_id and has never
		// known what an owner is. It reports a run's hold anyway.
		av := available(t, mat)
		require.True(t, av.OnHand.Equal(dec(100)), "the hold is soft: on_hand does not move, got %s", av.OnHand)
		require.True(t, av.Reserved.Equal(dec(60)), "the unmodified reader must count a RUN's claim, got %s", av.Reserved)
		require.True(t, av.Available.Equal(dec(40)), "available = on_hand − reserved, got %s", av.Available)

		// The second run needs 60 and can only have 40: the shortage is real and readable, where
		// before both runs would have read 100 free.
		require.True(t, av.Available.LessThan(dec(60)),
			"the second run on the same cloth must see a shortage, not 'there is enough'")

		// Its own hold stacks rather than replacing: the ledger is one number over all owners.
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runB, hold(mat, 40), "tester"))
		require.True(t, available(t, mat).Available.Equal(decimal.Zero),
			"two runs holding 60 + 40 of 100 leave nothing free")
	})

	// ── Probe 3 ──────────────────────────────────────────────────────────────────────────────────
	t.Run("probe 3: an abandoned run keeps its hold, and the hold's age is visible", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric P3", 100)
		runID := newRun(t)
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 35), "tester"))

		claims, err := MS.ListMaterialReservations(ctx, mat)
		require.NoError(t, err)
		require.Len(t, claims, 1, "exactly one open claim on this cloth")

		c := claims[0]
		require.True(t, c.RunId.Valid, "the claim names a run as its owner")
		require.EqualValues(t, runID, c.RunId.Int32)
		require.False(t, c.OrderId.Valid, "and NOT an order — the two owners are exclusive (XOR)")
		require.True(t, c.Qty.Equal(dec(35)))

		// The age is the point. A run parked in `planned` holds cloth until somebody closes or
		// cancels it; that consequence is only tolerable because it is visible.
		require.False(t, c.CreatedAt.IsZero(), "an open claim must report when it was taken")
		require.GreaterOrEqual(t, c.Age(time.Now().Add(time.Second)), time.Duration(0),
			"the claim's age must be derivable from what the read returns")

		// Σ over the listed claims is the SAME number MaterialAvailable reports as reserved — the
		// list can never disagree with the figure it explains.
		require.True(t, c.Qty.Equal(available(t, mat).Reserved))
	})

	// ── Probe 4 ──────────────────────────────────────────────────────────────────────────────────
	// The correction the lays force on the norm. It must be release + reserve of a NEW generation:
	// the ledger is append-only and UNIQUE(claim_key, event) physically forbids a second reserve on
	// one key, so an "update the quantity" implementation would silently write nothing.
	t.Run("probe 4: norm → lays correction closes the old claim and moves available by the difference", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric P4", 100)
		runID := newRun(t)

		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 60), "tester"))
		before := available(t, mat)
		require.True(t, before.Available.Equal(dec(40)))

		gen0 := entity.RunReservationClaimKey(runID, mat, 0)
		gen1 := entity.RunReservationClaimKey(runID, mat, 1)

		// The lays measured 75 where the norm estimated 60.
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 75), "tester"))

		rows := ledgerEvents(t, runID)
		require.Len(t, rows, 3, "reserve(gen0), release(gen0), reserve(gen1) — nothing rewritten in place")
		requireRow(t, rows[0], gen0, "reserve", 60, "the norm's hold")
		requireRow(t, rows[1], gen0, "release", 60, "the previous generation is CLOSED, not amended")
		requireRow(t, rows[2], gen1, "reserve", 75, "the new quantity opens under a new generation of the key")

		after := available(t, mat)
		require.True(t, after.Available.Equal(dec(25)), "got %s", after.Available)
		require.True(t, before.Available.Sub(after.Available).Equal(dec(15)),
			"available moved by exactly 75 − 60, so the old hold was fully returned before the new one was taken")

		// Re-running the same plan must write NOTHING: churning generations on every save would make
		// the trail unreadable and the 'corrections' meaningless.
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 75), "tester"))
		require.Len(t, ledgerEvents(t, runID), 3, "re-holding the same quantity is a no-op")

		// A material that drops out of the requirement entirely is RELEASED, not orphaned.
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, map[int]entity.RunMaterialRequirement{}, "tester"))
		require.True(t, available(t, mat).Available.Equal(dec(100)),
			"a material no longer required must not keep holding cloth")
	})

	// ── Probe 5 ──────────────────────────────────────────────────────────────────────────────────
	t.Run("probe 5: cancelling a run closes its claims and leaves order claims alone", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric P5", 100)
		orderID := newOrderClaim(t, mat, 10)
		runID := newRun(t)
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 30), "tester"))
		require.True(t, available(t, mat).Available.Equal(dec(60)), "order 10 + run 30 held")

		require.NoError(t, PR.UpdateProductionRun(ctx, runID, &entity.ProductionRunInsert{
			TechCardId: tcID, Status: entity.ProductionRunCancelled, Actor: "tester",
			Lines: []entity.ProductionRunLine{{SizeId: 1, PlannedQty: 10}},
		}, entity.LockVersion(0), dto.CostingFx{}))

		require.True(t, available(t, mat).Available.Equal(dec(90)),
			"the cancelled run's cloth goes back; the order's 10 stays held")

		claims, err := MS.ListMaterialReservations(ctx, mat)
		require.NoError(t, err)
		require.Len(t, claims, 1, "exactly the ORDER's claim survives")
		require.True(t, claims[0].OrderId.Valid)
		require.EqualValues(t, orderID, claims[0].OrderId.Int32)
		require.False(t, claims[0].RunId.Valid)
	})

	// ── Probe 10 ─────────────────────────────────────────────────────────────────────────────────
	t.Run("probe 10: deleting a run cascades its claims away and leaves order claims alone", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric P10", 100)
		orderID := newOrderClaim(t, mat, 10)
		runID := newRun(t)
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 25), "tester"))
		require.True(t, available(t, mat).Available.Equal(dec(65)))

		require.NoError(t, PR.DeleteProductionRun(ctx, runID))

		require.True(t, available(t, mat).Available.Equal(dec(90)),
			"the deleted run's claims went with it via ON DELETE CASCADE — no code had the chance to forget")
		claims, err := MS.ListMaterialReservations(ctx, mat)
		require.NoError(t, err)
		require.Len(t, claims, 1)
		require.EqualValues(t, orderID, claims[0].OrderId.Int32)
		require.Empty(t, ledgerEvents(t, runID), "not one ledger row of the deleted run survives")
	})

	// ── Issue-time consume ───────────────────────────────────────────────────────────────────────
	// The step that keeps `available` honest ACROSS an issue: on_hand drops, and the hold has to drop
	// with it by the same amount, or the cloth is counted both gone and still held.
	t.Run("issuing to a run converts the hold, and re-holds the remainder", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric Issue", 100)
		runID := newRun(t)
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID, hold(mat, 30), "tester"))
		require.True(t, available(t, mat).Available.Equal(dec(70)))

		_, err := MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
			MaterialId: mat, Quantity: dec(12), ProductionRunId: ni(runID), AdminUsername: "tester",
		})
		require.NoError(t, err)

		av := available(t, mat)
		require.True(t, av.OnHand.Equal(dec(88)), "the issue physically left the shelf, got %s", av.OnHand)
		require.True(t, av.Reserved.Equal(dec(18)), "the unissued remainder is STILL held, got %s", av.Reserved)
		require.True(t, av.Available.Equal(dec(70)),
			"available is unchanged across the issue: a soft hold became a hard decrement, got %s", av.Available)

		rows := ledgerEvents(t, runID)
		require.Len(t, rows, 3)
		requireRow(t, rows[1], entity.RunReservationClaimKey(runID, mat, 0), "consume", 12,
			"the issue closes the claim by exactly what left the shelf")
		requireRow(t, rows[2], entity.RunReservationClaimKey(runID, mat, 1), "reserve", 18,
			"a partial issue must re-hold what the run still has to cut")

		// Issuing MORE than is held closes the claim entirely and lets available drop by the excess —
		// that surplus was never planned for, and pretending otherwise would hide the overuse.
		_, err = MS.IssueMaterialStock(ctx, entity.MaterialIssueInsert{
			MaterialId: mat, Quantity: dec(25), ProductionRunId: ni(runID), AdminUsername: "tester",
		})
		require.NoError(t, err)
		av = available(t, mat)
		require.True(t, av.OnHand.Equal(dec(63)), "got %s", av.OnHand)
		require.True(t, av.Reserved.Equal(decimal.Zero), "the claim is fully consumed, got %s", av.Reserved)
		require.True(t, av.Available.Equal(dec(63)),
			"available fell by the 7 metres issued beyond the hold, got %s", av.Available)
	})

	// ── Per-lot availability (Р7) ────────────────────────────────────────────────────────────────
	// The one question that needs a roll rather than an article: a recut has to come out of the SAME
	// dye lot, because a month later out of another lot the difference shows on the garment.
	t.Run("a lot-pinned hold is fully counted for the material and separately visible on the lot", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric Lot", 60)
		_, err := MS.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: mat, Quantity: dec(40), Lot: ns("F5B-LOT-A"),
			UnitCost: decimal.NullDecimal{Decimal: dec(5), Valid: true}, Currency: "EUR",
		})
		require.NoError(t, err)
		lots, err := MS.ListMaterialLots(ctx, mat, false)
		require.NoError(t, err)
		require.Len(t, lots, 1)
		lotID := lots[0].Id

		free, err := MS.LotAvailable(ctx, lotID)
		require.NoError(t, err)
		require.True(t, free.Available.Equal(dec(40)), "the whole lot is free before any hold")

		runID := newRun(t)
		require.NoError(t, MS.SetRunMaterialReservations(ctx, runID,
			map[int]entity.RunMaterialRequirement{mat: {Qty: dec(30), LotId: ni(lotID)}}, "tester"))

		free, err = MS.LotAvailable(ctx, lotID)
		require.NoError(t, err)
		require.True(t, free.Available.Equal(dec(10)),
			"the lot has 40 with 30 held on it, got %s", free.Available)
		require.True(t, free.RemainingQty.Equal(dec(40)))

		// And the general question is still answered about the MATERIAL: a lot-pinned claim is a hold
		// on that cloth whatever roll it names, so it counts in full.
		require.True(t, available(t, mat).Reserved.Equal(dec(30)),
			"a lot-pinned claim counts fully in available(material) — Р7")

		_, err = MS.LotAvailable(ctx, lotID+100000)
		require.ErrorIs(t, err, entity.ErrMaterialLotNotFound,
			"a missing lot is a caller bug in the recut check, not an empty answer")
	})

	// ── The migration's own promise (0286 §3.2) ──────────────────────────────────────────────────
	// The XOR is what makes "one owner" an invariant of the DATA rather than a habit of the writer.
	// A claim with no owner is a hold nobody can close — released by no order closing and no run
	// closing, pressing on available forever. A claim with two owners closes twice and hands back
	// cloth the other owner is still holding.
	t.Run("the ledger refuses a claim with two owners or none", func(t *testing.T) {
		mat := newFabric(t, "F5B Fabric XOR", 10)
		runID := newRun(t)
		orderID := seedOrder(ctx, t)
		t.Cleanup(func() {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM customer_order WHERE id = ?", orderID)
		})

		_, err := testDB.ExecContext(ctx, `
			INSERT INTO material_reservation_ledger (material_id, order_id, run_id, qty, event, claim_key, created_by)
			VALUES (?, ?, ?, 1, 'reserve', 'xor:both', 'tester')`, mat, orderID, runID)
		require.Error(t, err, "a claim owned by BOTH an order and a run must be refused by the DB")

		_, err = testDB.ExecContext(ctx, `
			INSERT INTO material_reservation_ledger (material_id, qty, event, claim_key, created_by)
			VALUES (?, 1, 'reserve', 'xor:none', 'tester')`, mat)
		require.Error(t, err, "a claim owned by NOBODY must be refused by the DB")

		require.True(t, available(t, mat).Available.Equal(dec(10)), "neither refusal left a row behind")
	})

	// ── Claim-key namespacing ────────────────────────────────────────────────────────────────────
	// Р2's reason for the `run:` prefix, made falsifiable. Without it, run N and order N claiming the
	// same material would produce the same claim_key, and the second claim would vanish into the
	// first's INSERT IGNORE — recorded nowhere, closed by nobody.
	t.Run("run and order claim keys cannot collide", func(t *testing.T) {
		require.NotEqual(t, entity.PackagingClaimKey(7, 42), entity.RunReservationClaimKey(7, 42, 0))
		require.Equal(t, "run:7:42:0", entity.RunReservationClaimKey(7, 42, 0))
		require.Equal(t, fmt.Sprintf("%d:%d", 7, 42), entity.PackagingClaimKey(7, 42))

		gen, ok := entity.ParseRunReservationGeneration("run:7:42:3")
		require.True(t, ok)
		require.Equal(t, 3, gen)
		_, ok = entity.ParseRunReservationGeneration(entity.PackagingClaimKey(7, 42))
		require.False(t, ok, "an ORDER key must never parse as run generation 0 — that key would be reused")
	})
}
