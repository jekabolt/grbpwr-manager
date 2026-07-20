package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	acctrules "github.com/jekabolt/grbpwr-manager/internal/accounting"
	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Phase 2, wave 1 (VAT engine) store-level integration tests. Like the other accounting integration
// tests they run only against the real MySQL TestMain connects + migrates, and clean up their own
// rows. Numbers are hand-computed in the comments.

// tbRow finds one account's trial-balance row by code (fails if absent).
func tbRow(t *testing.T, tb *entity.AcctTrialBalance, code string) entity.AcctTrialBalanceRow {
	t.Helper()
	for _, r := range tb.Rows {
		if r.Code == code {
			return r
		}
	}
	t.Fatalf("account %s not found in trial balance", code)
	return entity.AcctTrialBalanceRow{}
}

// TestAccountingInputVATPosting posts a domestic and a WNT purchase receipt with input VAT through the
// material builder and asserts the resulting account turnover (§1.4):
//
//	domestic_pl: V=180, VAT 41.40 → Dr 1110 180 + Dr 2080 41.40 / Cr 2010 221.40
//	wnt:         V=100, VAT 20.00 → Dr 1110 100 / Cr 2010 100 (plain) + Dr 2080 20 / Cr 2070 20 (self-charge)
//
// Combined turnover: 1110 Dr 280, 2080 Dr 61.40, 2010 Cr 321.40, 2070 Cr 20.
func TestAccountingInputVATPosting(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	dec := decimal.RequireFromString

	month := time.Date(2034, 5, 1, 0, 0, 0, 0, time.UTC)
	next := month.AddDate(0, 1, 0)
	occ := sql.NullTime{Time: time.Date(2034, 5, 10, 0, 0, 0, 0, time.UTC), Valid: true}

	var entryIDs []int
	post := func(entry entity.AcctJournalEntryInsert) {
		var id int
		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e error
			id, _, e = rep.Accounting().CreateJournalEntry(ctx, entry)
			return e
		}), "post %s/%s", entry.SourceType, entry.SourceKey)
		entryIDs = append(entryIDs, id)
	}
	defer func() {
		for _, id := range entryIDs {
			_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_journal_entry WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(context.Background(), "DELETE FROM acct_period WHERE period = ?", "2034-05-01")
	}()

	dpl, err := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id: 934001, MaterialId: 934100, MovementType: entity.MaterialMovementReceipt,
			Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("180"), Valid: true},
			OccurredAt: occ, CreatedAt: occ.Time,
			InputVatAmount: decimal.NullDecimal{Decimal: dec("41.40"), Valid: true},
			InputVatRegime: sql.NullString{String: string(entity.InputVatRegimeDomesticPL), Valid: true},
		},
		MaterialName: "Domestic fabric",
	}, month)
	require.NoError(t, err)
	post(dpl)

	wnt, err := acctrules.BuildMaterialMovementEntry(entity.AcctMovementFacts{
		MaterialMovement: entity.MaterialMovement{
			Id: 934002, MaterialId: 934101, MovementType: entity.MaterialMovementReceipt,
			Quantity: dec("1"), UnitCostBase: decimal.NullDecimal{Decimal: dec("100"), Valid: true},
			OccurredAt: occ, CreatedAt: occ.Time,
			InputVatAmount: decimal.NullDecimal{Decimal: dec("20"), Valid: true},
			InputVatRegime: sql.NullString{String: string(entity.InputVatRegimeWNT), Valid: true},
		},
		MaterialName: "EU fabric",
	}, month)
	require.NoError(t, err)
	post(wnt)

	tb, err := s.Accounting().GetTrialBalance(ctx, month, next)
	require.NoError(t, err)
	require.Equal(t, "280.00", tbRow(t, tb, "1110").Debit.StringFixed(2))
	require.Equal(t, "61.40", tbRow(t, tb, "2080").Debit.StringFixed(2))
	require.Equal(t, "321.40", tbRow(t, tb, "2010").Credit.StringFixed(2))
	require.Equal(t, "20.00", tbRow(t, tb, "2070").Credit.StringFixed(2))
	require.True(t, tb.Balanced, "trial balance must balance")
}

// TestAccountingVatReturns exercises the source-type-agnostic VAT exports (§1.5). It seeds three order
// entries in one month — a PL-domestic sale, its partial refund (2070 debit nets with a minus), and an
// OSS sale to Germany — and asserts GetVatReturnPL and GetOssReturn.
//
//	PL sale:    Cr 2070 23   (vat_regime pl_domestic)
//	PL refund:  Dr 2070 5    (source_key "…:1" → same order) → domestic output 23 − 5 = 18
//	DE sale:    Cr 2070 19, Cr 4020 100 (vat_regime oss, ships to DE) → OSS net 100 / vat 19 / rate 19%
func TestAccountingVatReturns(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	s, err := NewForTest(ctx, *testCfg)
	require.NoError(t, err)
	dec := decimal.RequireFromString

	month := time.Date(2035, 8, 1, 0, 0, 0, 0, time.UTC)
	occ := time.Date(2035, 8, 12, 0, 0, 0, 0, time.UTC)

	const plUUID = "vat-int-pl-000000000000000000000001"
	const deUUID = "vat-int-de-000000000000000000000002"

	var confirmedID int
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM order_status WHERE name = ?", string(entity.Confirmed)).Scan(&confirmedID))

	// customer_order rows carrying the resolved regime (the join key for both exports).
	_, err = testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, total_settled_base, vat_amount, vat_regime, placed)
		VALUES (?, ?, 'EUR', 123, 123, 23, 'pl_domestic', ?)`, plUUID, confirmedID, occ)
	require.NoError(t, err)
	var plOrderID int64
	require.NoError(t, testDB.QueryRowContext(ctx, "SELECT id FROM customer_order WHERE uuid = ?", plUUID).Scan(&plOrderID))

	res, err := testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, total_settled_base, vat_amount, vat_regime, placed)
		VALUES (?, ?, 'EUR', 119, 119, 19, 'oss', ?)`, deUUID, confirmedID, occ)
	require.NoError(t, err)
	deOrderID, err := res.LastInsertId()
	require.NoError(t, err)

	// DE order needs a shipping address (country_code) for the OSS per-country grouping.
	res, err = testDB.ExecContext(ctx, `INSERT INTO address (country, country_code, city, address_line_one, postal_code)
		VALUES ('DE', 'DE', 'Berlin', 'Teststr. 1', '10115')`)
	require.NoError(t, err)
	addrID, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = testDB.ExecContext(ctx, `INSERT INTO buyer
		(order_id, first_name, last_name, email, phone, billing_address_id, shipping_address_id)
		VALUES (?, 'Test', 'Buyer', 'vatint@example.com', '1234567', ?, ?)`, deOrderID, addrID, addrID)
	require.NoError(t, err)

	// H-4: a UK-stock domestic sale (2070 output) and a domestic_uk material receipt (2080 input) are a
	// DIFFERENT jurisdiction — they must stay out of the Polish domestic totals and NetPayable.
	const ukUUID = "vat-int-uk-000000000000000000000003"
	_, err = testDB.ExecContext(ctx, `INSERT INTO customer_order
		(uuid, order_status_id, currency, total_price, total_settled_base, vat_amount, vat_regime, placed)
		VALUES (?, ?, 'EUR', 120, 120, 20, 'uk_stock_domestic', ?)`, ukUUID, confirmedID, occ)
	require.NoError(t, err)

	res, err = testDB.ExecContext(ctx, "INSERT INTO material (name, section) VALUES ('VATINT-uk-fabric', 'fabric')")
	require.NoError(t, err)
	ukMatID, err := res.LastInsertId()
	require.NoError(t, err)
	res, err = testDB.ExecContext(ctx, `INSERT INTO material_stock_movement
		(material_id, movement_type, quantity, on_hand_before, on_hand_after, unit_cost_base,
		 input_vat_amount, input_vat_regime, occurred_at, admin_username)
		VALUES (?, 'receipt', 1, 0, 1, 100, 15, 'domestic_uk', ?, 'VATINT')`, ukMatID, occ)
	require.NoError(t, err)
	ukMovID, err := res.LastInsertId()
	require.NoError(t, err)

	var entryIDs []int
	post := func(entry entity.AcctJournalEntryInsert) {
		var id int
		require.NoError(t, s.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
			var e error
			id, _, e = rep.Accounting().CreateJournalEntry(ctx, entry)
			return e
		}), "post %s/%s", entry.SourceType, entry.SourceKey)
		entryIDs = append(entryIDs, id)
	}
	defer func() {
		cctx := context.Background()
		for _, id := range entryIDs {
			_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_journal_entry WHERE id = ?", id)
		}
		_, _ = testDB.ExecContext(cctx, "DELETE FROM buyer WHERE order_id = ?", deOrderID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM address WHERE id = ?", addrID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM customer_order WHERE uuid IN (?, ?, ?)", plUUID, deUUID, ukUUID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material_stock_movement WHERE id = ?", ukMovID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM material WHERE id = ?", ukMatID)
		_, _ = testDB.ExecContext(cctx, "DELETE FROM acct_period WHERE period = ?", "2035-08-01")
	}()

	line := func(code string, side entity.AcctSide, amt string) entity.AcctJournalLineInsert {
		return entity.AcctJournalLineInsert{AccountCode: code, Side: side, Amount: dec(amt)}
	}
	// PL domestic sale (Cr 2070 23).
	post(entity.AcctJournalEntryInsert{
		OccurredAt: occ, Description: "pl sale", SourceType: entity.AcctSourceOrderSale, SourceKey: plUUID, CreatedBy: "system",
		Lines: []entity.AcctJournalLineInsert{line("1030", entity.AcctSideDebit, "123"), line("4020", entity.AcctSideCredit, "100"), line("2070", entity.AcctSideCredit, "23")},
	})
	// PL partial refund (Dr 2070 5) — source_key "uuid:1" resolves to the same order.
	post(entity.AcctJournalEntryInsert{
		OccurredAt: occ, Description: "pl refund", SourceType: entity.AcctSourceOrderRefund, SourceKey: plUUID + ":1", CreatedBy: "system",
		Lines: []entity.AcctJournalLineInsert{line("4040", entity.AcctSideDebit, "15"), line("2070", entity.AcctSideDebit, "5"), line("1030", entity.AcctSideCredit, "20")},
	})
	// DE OSS sale (Cr 2070 19, Cr 4020 100).
	post(entity.AcctJournalEntryInsert{
		OccurredAt: occ, Description: "de sale", SourceType: entity.AcctSourceOrderSale, SourceKey: deUUID, CreatedBy: "system",
		Lines: []entity.AcctJournalLineInsert{line("1030", entity.AcctSideDebit, "119"), line("4020", entity.AcctSideCredit, "100"), line("2070", entity.AcctSideCredit, "19")},
	})
	// UK-stock domestic sale (Cr 2070 20) — output VAT in a different jurisdiction.
	post(entity.AcctJournalEntryInsert{
		OccurredAt: occ, Description: "uk sale", SourceType: entity.AcctSourceOrderSale, SourceKey: ukUUID, CreatedBy: "system",
		Lines: []entity.AcctJournalLineInsert{line("1010", entity.AcctSideDebit, "120"), line("4010", entity.AcctSideCredit, "100"), line("2070", entity.AcctSideCredit, "20")},
	})
	// domestic_uk material receipt (Dr 2080 15) — UK-recoverable input VAT.
	post(entity.AcctJournalEntryInsert{
		OccurredAt: occ, Description: "uk receipt", SourceType: entity.AcctSourceMaterialReceipt, SourceKey: strconv.FormatInt(ukMovID, 10), CreatedBy: "system",
		Lines: []entity.AcctJournalLineInsert{line("1110", entity.AcctSideDebit, "100"), line("2080", entity.AcctSideDebit, "15"), line("2010", entity.AcctSideCredit, "115")},
	})

	// --- JPK_VAT monthly return ---
	ret, err := s.Accounting().GetVatReturnPL(ctx, month)
	require.NoError(t, err)
	require.Equal(t, "18.00", ret.OutputDomestic.StringFixed(2), "23 sale − 5 refund")
	require.Equal(t, "19.00", ret.OssInfoTotal.StringFixed(2))
	require.Equal(t, "0.00", ret.InputDomestic.StringFixed(2))
	require.Equal(t, "18.00", ret.NetPayable.StringFixed(2), "domestic output only; OSS + UK excluded")
	// H-4: UK output/input are a different jurisdiction — reported separately, never folded into the PL
	// domestic totals (still 18.00 / 0.00 above) or NetPayable.
	require.Equal(t, "20.00", ret.OutputUkStockDomestic.StringFixed(2), "UK output reported separately")
	require.Equal(t, "15.00", ret.InputUkDomestic.StringFixed(2), "UK input reported separately")
	ukCaveat := false
	for _, c := range ret.Caveats {
		if strings.Contains(c, "UK VAT present") {
			ukCaveat = true
		}
	}
	require.Truef(t, ukCaveat, "UK-VAT caveat expected, got %v", ret.Caveats)

	// --- OSS quarterly return (window starts at the seeded month) ---
	oss, err := s.Accounting().GetOssReturn(ctx, month)
	require.NoError(t, err)
	require.Len(t, oss.Rows, 1, "only the DE oss order")
	require.Equal(t, "DE", oss.Rows[0].Country)
	require.Equal(t, "100.00", oss.Rows[0].Net.StringFixed(2))
	require.Equal(t, "19.00", oss.Rows[0].Vat.StringFixed(2))
	require.Equal(t, "19.00", oss.Rows[0].RatePct.StringFixed(2))
	require.Equal(t, "100.00", oss.TotalNet.StringFixed(2))
	require.Equal(t, "19.00", oss.TotalVat.StringFixed(2))
}
