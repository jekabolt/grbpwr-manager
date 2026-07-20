package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// VAT filing exports (phase 2, wave 1 — docs/plan-accounting-phase2/01-wave1-vat.md §1.5). Both are
// SOURCE-TYPE-AGNOSTIC (07 §7.4.15): they read the 2070/2080 ledger lines of order and material-
// receipt entries, split by customer_order.vat_regime / material_stock_movement.input_vat_regime, over
// the PAYMENT period (the entry's occurred_at). A refund's 2070 line is a debit, so summing
// credit−debit nets refunds with a minus automatically. They survive wave 2's prepayment split
// because they never filter on source_type beyond "an order / a receipt".
//
// The join to customer_order / material_stock_movement casts the entry source_key to the ledger's
// utf8mb4_unicode_ci collation (the acct_* tables' collation) so it compares cleanly with those
// tables' own collation — the same guard reconcile.go uses. An order-refund source_key is
// "uuid:seq", so SUBSTRING_INDEX(..., ':', 1) recovers the order uuid.

// signedAmount is the SQL fragment turning a (side, amount) into a signed contribution (credit +,
// debit −) — a VAT credit adds, a refund's VAT debit subtracts.
const signedAmount = "CASE WHEN l.side = 'credit' THEN l.amount ELSE -l.amount END"

// orderKeyMatch / movementKeyMatch join an entry's source_key back to its operational row across the
// collation boundary (see the package note). The separator is CHAR(58) (ASCII ':'), not a literal
// ':', because these fragments are embedded in sqlx.Named queries and its parser would read the colon
// in a ':' literal as the start of a (empty-named) bind parameter and fail — a refund source_key is
// "uuid:seq", so SUBSTRING_INDEX(..., CHAR(58), 1) still recovers the order uuid.
const (
	orderKeyMatch    = "CAST(co.uuid AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci = SUBSTRING_INDEX(e.source_key, CHAR(58), 1)"
	movementKeyMatch = "CAST(m.id AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci = e.source_key"
)

// GetVatReturnPL aggregates the month's JPK_VAT figures: output VAT by regime (domestic PL, WNT/import
// self-charge, OSS shown for reference), input VAT 2080 by type, and the net payable. OSS output is
// informational only — it is filed via the OSS return, not the domestic JPK — so it is excluded from
// NetPayable. Numbers for the accountant's manual filing (full XML is phase 3).
func (s *Store) GetVatReturnPL(ctx context.Context, month time.Time) (*entity.AcctVatReturnPL, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)
	fromStr, toStr := from.Format(dateLayout), to.Format(dateLayout)

	ret := &entity.AcctVatReturnPL{Month: from}

	// --- output VAT from order 2070 lines, by regime (refunds net with a minus) ---
	orderRows, err := storeutil.QueryListNamed[struct {
		Regime string          `db:"regime"`
		NetVat decimal.Decimal `db:"net_vat"`
	}](ctx, s.DB, `
		SELECT COALESCE(co.vat_regime, 'none') AS regime,
		       COALESCE(SUM(`+signedAmount+`), 0) AS net_vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		WHERE a.code = '2070'
		  AND e.source_type IN ('order_sale','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		GROUP BY COALESCE(co.vat_regime, 'none')`,
		map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: vat return output by regime %s: %w", fromStr, err)
	}
	for _, r := range orderRows {
		switch entity.VatRegime(r.Regime) {
		case entity.VatRegimePLDomestic:
			ret.OutputDomestic = ret.OutputDomestic.Add(r.NetVat)
		case entity.VatRegimeOSS:
			ret.OssInfoTotal = ret.OssInfoTotal.Add(r.NetVat)
		}
	}

	// --- material receipts: WNT/import self-charge output (2070 credit) + input VAT (2080 debit) ---
	matRows, err := storeutil.QueryListNamed[struct {
		Regime     string          `db:"regime"`
		OutputSelf decimal.Decimal `db:"output_self"`
		InputVat   decimal.Decimal `db:"input_vat"`
	}](ctx, s.DB, `
		SELECT m.input_vat_regime AS regime,
		       COALESCE(SUM(CASE WHEN a.code = '2070' AND l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS output_self,
		       COALESCE(SUM(CASE WHEN a.code = '2080' AND l.side = 'debit'  THEN l.amount ELSE 0 END), 0) AS input_vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime IS NOT NULL
		  AND a.code IN ('2070','2080')
		GROUP BY m.input_vat_regime`,
		map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: vat return input by regime %s: %w", fromStr, err)
	}
	for _, r := range matRows {
		switch entity.InputVatRegime(r.Regime) {
		case entity.InputVatRegimeWNT:
			ret.InputWnt = ret.InputWnt.Add(r.InputVat)
			ret.OutputWntSelfCharge = ret.OutputWntSelfCharge.Add(r.OutputSelf)
		case entity.InputVatRegimeImport:
			ret.InputImport = ret.InputImport.Add(r.InputVat)
			ret.OutputWntSelfCharge = ret.OutputWntSelfCharge.Add(r.OutputSelf)
		case entity.InputVatRegimeDomesticPL, entity.InputVatRegimeDomesticUK:
			ret.InputDomestic = ret.InputDomestic.Add(r.InputVat)
		}
	}

	// Net payable = domestic output + reverse-charge output − all input. WNT/import output and input are
	// equal (net-zero self-charge), so they cancel here; only domestic output − domestic input remains.
	// OSS is deliberately excluded (declared on the OSS return).
	ret.NetPayable = ret.OutputDomestic.Add(ret.OutputWntSelfCharge).
		Sub(ret.InputDomestic).Sub(ret.InputWnt).Sub(ret.InputImport)

	caveats, err := s.vatReturnCaveats(ctx, fromStr, toStr)
	if err != nil {
		return nil, err
	}
	ret.Caveats = caveats
	return ret, nil
}

// vatReturnCaveats returns the distinct entry caveats over the period for the order / material-receipt
// sources the return reads, so a filing preparer sees the month's flagged approximations. Bounded.
func (s *Store) vatReturnCaveats(ctx context.Context, fromStr, toStr string) ([]string, error) {
	rows, err := storeutil.QueryListNamed[struct {
		Caveat string `db:"caveat"`
	}](ctx, s.DB, `
		SELECT DISTINCT caveat
		FROM acct_journal_entry
		WHERE has_caveat = TRUE AND caveat IS NOT NULL AND caveat <> ''
		  AND source_type IN ('order_sale','order_refund','material_receipt')
		  AND occurred_at >= :from AND occurred_at < :to
		ORDER BY caveat
		LIMIT 50`, map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: vat return caveats: %w", err)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Caveat)
	}
	return out, nil
}

// GetOssReturn aggregates the quarter's OSS B2C sales (vat_regime = 'oss') by destination country:
// country, applied rate, net (taxable base = net revenue + shipping = G − VAT) and VAT. Refunds net
// with a minus. The rate is derived from VAT/net (self-consistent with the inclusive extraction) so it
// reflects what was actually charged, not today's vat_rate.
func (s *Store) GetOssReturn(ctx context.Context, quarterStart time.Time) (*entity.AcctOssReturn, error) {
	from := firstOfMonthUTC(quarterStart)
	to := from.AddDate(0, 3, 0)
	fromStr, toStr := from.Format(dateLayout), to.Format(dateLayout)

	rows, err := storeutil.QueryListNamed[struct {
		Country string          `db:"country"`
		Net     decimal.Decimal `db:"net"`
		Vat     decimal.Decimal `db:"vat"`
	}](ctx, s.DB, `
		SELECT COALESCE(NULLIF(a.country_code, ''), a.country, '') AS country,
		       COALESCE(SUM(CASE WHEN acc.code IN ('4010','4020','4310','4110','4040')
		                         THEN (`+signedAmount+`) ELSE 0 END), 0) AS net,
		       COALESCE(SUM(CASE WHEN acc.code = '2070'
		                         THEN (`+signedAmount+`) ELSE 0 END), 0) AS vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account acc ON acc.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		LEFT JOIN buyer b ON b.order_id = co.id
		LEFT JOIN address a ON a.id = b.shipping_address_id
		WHERE co.vat_regime = 'oss'
		  AND e.source_type IN ('order_sale','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND acc.code IN ('4010','4020','4310','4110','4040','2070')
		GROUP BY COALESCE(NULLIF(a.country_code, ''), a.country, '')
		HAVING net <> 0 OR vat <> 0
		ORDER BY country`,
		map[string]any{"from": fromStr, "to": toStr})
	if err != nil {
		return nil, fmt.Errorf("accounting: oss return %s: %w", fromStr, err)
	}

	ret := &entity.AcctOssReturn{QuarterStart: from}
	for _, r := range rows {
		rate := decimal.Zero
		if r.Net.IsPositive() {
			rate = r.Vat.Div(r.Net).Mul(decimal.NewFromInt(100)).Round(2)
		}
		ret.Rows = append(ret.Rows, entity.AcctOssRow{
			Country: r.Country,
			RatePct: rate,
			Net:     r.Net,
			Vat:     r.Vat,
		})
		ret.TotalNet = ret.TotalNet.Add(r.Net)
		ret.TotalVat = ret.TotalVat.Add(r.Vat)
	}
	return ret, nil
}
