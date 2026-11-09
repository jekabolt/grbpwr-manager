package store

import (
	"context"
	"database/sql"
	"strconv"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// TestMaterialAvgKeepsCentsOnLargeStock (w1107 №221): the moving average used to be rounded to four
// places, so on_hand × avg on a big stock drifted from the money actually received. 3000 m @ 1 € plus
// 6000 m @ 2 € is 15 000.00 € for 9000 m; a 4-place average (1.6667) values it at 15 000.30 €.
func TestMaterialAvgKeepsCentsOnLargeStock(t *testing.T) {
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

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "test E avg scale fabric", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		cctx := context.Background()
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
	})

	ms := s.MaterialStock()
	receive := func(qty int64, unit string) {
		_, err := ms.ReceiveMaterialStock(ctx, entity.MaterialReceiptInsert{
			MaterialId: matID, Quantity: decimal.NewFromInt(qty),
			UnitCost: decimal.NullDecimal{Decimal: decimal.RequireFromString(unit), Valid: true},
			Currency: "EUR",
		})
		require.NoError(t, err)
	}
	receive(3000, "1")
	receive(6000, "2")

	rows, err := ms.ListMaterialStock(ctx, entity.MaterialStockFilter{Query: "test E avg scale fabric"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.True(t, rows[0].StockValueBase.Valid)
	require.Equal(t, "15000.00", rows[0].StockValueBase.Decimal.StringFixed(2),
		"stock value must equal the money received, avg %s", rows[0].AvgUnitCostBase.Decimal)

	// The valuation report sums on_hand × avg in SQL: it must agree to the cent too.
	var value string
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT ROUND(on_hand * avg_unit_cost_base, 2) FROM material_stock WHERE material_id = ?", matID).Scan(&value))
	require.Equal(t, "15000.00", value)

	// Half the stock leaves at the average: the remainder is still worth exactly half.
	_, err = ms.AdjustMaterialStock(ctx, entity.MaterialAdjustInsert{
		MaterialId: matID, Mode: entity.MaterialAdjustModeWriteoff, Quantity: decimal.NewFromInt(4500),
		Reason: entity.MaterialAdjustReasonDamage,
	})
	require.NoError(t, err)
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT ROUND(on_hand * avg_unit_cost_base, 2) FROM material_stock WHERE material_id = ?", matID).Scan(&value))
	require.Equal(t, "7500.00", value)
}

// TestClosePeriodPLNRateOnlyForVATReceipts (w1107 №222): gate 3f asks for a PLN reference rate on the
// days a JPK filing converts. A material_receipt entry is read by the filings only when its movement
// carries an input VAT regime; a conversion (convert_in) posts under the same source type without one
// and must not block the close. Uses an empty far-past month that has no PLN rate before it.
func TestClosePeriodPLNRateOnlyForVATReceipts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	defer s.Close()
	acc := s.Accounting()

	occ := time.Date(2001, 5, 10, 12, 0, 0, 0, time.UTC)
	var rateBefore int
	require.NoError(t, testDB.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM costing_fx_rate WHERE currency = 'PLN' AND valid_from < '2001-05-10'").Scan(&rateBefore))
	if rateBefore > 0 {
		t.Skip("a PLN rate before 2001-05 exists in this base; the gate cannot be exercised")
	}

	matID, err := s.TechCards().CreateMaterial(ctx, &entity.MaterialInsert{
		Name: "test E close gate fabric", Section: "fabric",
		Unit: sql.NullString{String: "m", Valid: true},
	})
	require.NoError(t, err)

	movement := func(kind string, regime any) int64 {
		res, err := testDB.ExecContext(ctx, `
			INSERT INTO material_stock_movement
				(material_id, movement_type, quantity, on_hand_before, on_hand_after, input_vat_regime, occurred_at)
			VALUES (?, ?, 1, 0, 1, ?, ?)`, matID, kind, regime, occ)
		require.NoError(t, err)
		id, err := res.LastInsertId()
		require.NoError(t, err)
		return id
	}
	post := func(movementID int64) {
		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			_, _, e := rep.Accounting().CreateJournalEntry(ctx, entity.AcctJournalEntryInsert{
				OccurredAt: occ, Description: "test E close gate", CreatedBy: "tester",
				SourceType: entity.AcctSourceMaterialReceipt, SourceKey: strconv.FormatInt(movementID, 10),
				Lines: []entity.AcctJournalLineInsert{
					{AccountCode: "1110", Side: entity.AcctSideDebit, Amount: decimal.RequireFromString("10.00")},
					{AccountCode: "1120", Side: entity.AcctSideCredit, Amount: decimal.RequireFromString("10.00")},
				},
			})
			return e
		}))
	}

	var keys []string
	t.Cleanup(func() {
		cctx := context.Background()
		for _, k := range keys {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE source_type = 'material_receipt' AND source_key = ?", k)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE material_id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", matID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = '2001-05-01'")
	})

	conv := movement(string(entity.MaterialMovementConvertIn), nil)
	keys = append(keys, strconv.FormatInt(conv, 10))
	post(conv)
	require.NoError(t, acc.ClosePeriod(ctx, occ, "tester"), "a conversion carries no VAT and needs no PLN rate")
	require.NoError(t, acc.ReopenPeriod(ctx, occ, "tester"))

	vat := movement(string(entity.MaterialMovementReceipt), "domestic_pl")
	keys = append(keys, strconv.FormatInt(vat, 10))
	post(vat)
	err = acc.ClosePeriod(ctx, occ, "tester")
	require.ErrorIs(t, err, entity.ErrAcctPeriodNotReady)
	require.Contains(t, err.Error(), "no PLN reference rate")
}
