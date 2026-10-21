package store

import (
	"bufio"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// occurredBackfillStatement returns the backfill UPDATE of the *_material_movement_occurred_backfill
// migration, read from the file itself (by suffix, not number — the number is assigned before push) so
// the test exercises the exact SQL that runs on beta/prod.
func occurredBackfillStatement(t *testing.T) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join("sql", "*_material_movement_occurred_backfill.sql"))
	require.NoError(t, err)
	require.Len(t, matches, 1, "exactly one occurred_at backfill migration")
	f, err := os.Open(matches[0])
	require.NoError(t, err)
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "UPDATE material_stock_movement SET occurred_at") {
			return strings.TrimSuffix(line, ";")
		}
	}
	require.NoError(t, sc.Err())
	t.Fatal("backfill UPDATE not found in the migration")
	return ""
}

// TestMaterialMovementOccurredAt pins the movement journal's business date: an insert that names no
// date is dated the store clock's UTC day (write-offs and undated receipts used to be stored NULL), the
// legacy-NULL backfill dates a row by the UTC day it was recorded, the list sorts occurred_at DESC then
// id DESC (a backdated receipt sits on its own day, not on top), and a date window includes the
// backfilled rows.
func TestMaterialMovementOccurredAt(t *testing.T) {
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

	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	dated := func(t time.Time) sql.NullTime { return sql.NullTime{Time: t, Valid: true} }
	todayUTC := func() time.Time {
		n := time.Now().UTC()
		return day(n.Year(), n.Month(), n.Day())
	}
	// isToday tolerates a test that straddles UTC midnight.
	isToday := func(before time.Time, got sql.NullTime) bool {
		return got.Valid && (got.Time.UTC().Equal(before) || got.Time.UTC().Equal(todayUTC()))
	}

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name:    "Movement Date Fabric",
		Section: "fabric",
		Unit:    sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID) // cascades stock + price
	})

	ms := s.MaterialStock()
	eur := func(q int64, occ sql.NullTime) entity.MaterialMovement {
		mv, err := ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: matID, Quantity: decimal.NewFromInt(q),
			UnitCost: decimal.NullDecimal{Decimal: decimal.NewFromInt(5), Valid: true}, Currency: "EUR",
			OccurredAt: occ,
		})
		require.NoError(t, err)
		return mv
	}

	// 1) dated receipt keeps its own date.
	mvA := eur(10, dated(day(2026, time.January, 5)))
	require.True(t, mvA.OccurredAt.Valid && mvA.OccurredAt.Time.Equal(day(2026, time.January, 5)))

	// 2) undated receipt and 3) write-off (the adjust path never takes a date) are dated today (UTC).
	before := todayUTC()
	mvB := eur(4, sql.NullTime{})
	require.True(t, isToday(before, mvB.OccurredAt), "undated receipt dated today, got %v", mvB.OccurredAt)
	mvC, err := ms.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
		MaterialId: matID, Mode: entity.MaterialAdjustModeWriteoff, Quantity: decimal.NewFromInt(1), Reason: "damage",
	})
	require.NoError(t, err)
	require.True(t, isToday(before, mvC.OccurredAt), "write-off dated today, got %v", mvC.OccurredAt)

	// 4) a legacy undated row (as a pre-fix binary wrote it), recorded 2026-01-04 10:00 UTC, then the
	// migration's backfill dates it by its created_at day.
	res, err := testDB.ExecContext(ctx, `INSERT INTO material_stock_movement
		(material_id, movement_type, quantity, on_hand_before, on_hand_after, admin_username, occurred_at, created_at)
		VALUES (?, 'writeoff', 1, 13, 12, 'legacy', NULL, '2026-01-04 10:00:00')`, matID)
	require.NoError(t, err)
	legacyID64, err := res.LastInsertId()
	require.NoError(t, err)
	legacyID := int(legacyID64)
	_, err = testDB.ExecContext(ctx, occurredBackfillStatement(t))
	require.NoError(t, err)
	var nulls int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM material_stock_movement WHERE material_id = ? AND occurred_at IS NULL", matID).Scan(&nulls))
	require.Zero(t, nulls, "no undated movement left after the backfill")

	// 5) a receipt backdated to 2026-01-03, inserted LAST (highest id) — it must not sort on top.
	mvD := eur(2, dated(day(2026, time.January, 3)))

	ids := func(mvs []entity.MaterialMovement) []int {
		out := make([]int, len(mvs))
		for i, m := range mvs {
			out[i] = m.Id
		}
		return out
	}
	// occurred_at DESC, id DESC: today's write-off over today's receipt, then 01-05, 01-04 (backfilled),
	// 01-03 (backdated, newest id).
	wantOrder := []int{mvC.Id, mvB.Id, mvA.Id, legacyID, mvD.Id}

	all, total, err := ms.ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{MaterialId: matID})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, wantOrder, ids(all))
	for _, m := range all {
		require.True(t, m.OccurredAt.Valid, "movement %d is dated", m.Id)
	}
	require.True(t, all[3].OccurredAt.Time.Equal(day(2026, time.January, 4)), "backfilled to created_at's day, got %v", all[3].OccurredAt)

	// offset paging walks the same order and keeps the total.
	page, total, err := ms.ListMaterialMovements(ctx, 2, 1, entity.MaterialMovementFilter{MaterialId: matID})
	require.NoError(t, err)
	require.Equal(t, 5, total)
	require.Equal(t, wantOrder[1:3], ids(page))

	// a one-day window on the backfilled day finds the backfilled row.
	one, total, err := ms.ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{
		MaterialId: matID, OccurredFrom: "2026-01-04", OccurredTo: "2026-01-04",
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, []int{legacyID}, ids(one))

	// an inclusive window over 01-03..01-05 holds exactly the three January rows, in order.
	jan, total, err := ms.ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{
		MaterialId: matID, OccurredFrom: "2026-01-03", OccurredTo: "2026-01-05",
	})
	require.NoError(t, err)
	require.Equal(t, 3, total)
	require.Equal(t, []int{mvA.Id, legacyID, mvD.Id}, ids(jan))

	// a write-off-only filter combines with the date window: only the backfilled legacy write-off.
	wo, total, err := ms.ListMaterialMovements(ctx, 50, 0, entity.MaterialMovementFilter{
		MaterialId: matID, MovementType: entity.MaterialMovementWriteoff, OccurredTo: "2026-01-31",
	})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Equal(t, []int{legacyID}, ids(wo))
}
