package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	storeutil "github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// GetUkVatReturn aggregates the quarter's UK VAT figures (9-box MTD return) for the uk_stock_domestic
// regime — a separate jurisdiction from the Polish JPK, so it is never folded into the PL net payable.
// Box 1 = output VAT on UK-stock sales (2070), Box 6 = their net; Box 4 = reclaimable UK input VAT
// (domestic_uk 2080), Box 7 = its net. Refunds net with a minus, matching the other returns.
func (s *Store) GetUkVatReturn(ctx context.Context, quarterStart time.Time) (*entity.AcctUkVatReturn, error) {
	from := firstOfMonthUTC(quarterStart)
	to := from.AddDate(0, 3, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	// Sales side: output VAT (2070) + net revenue, for uk_stock_domestic orders.
	sale, err := storeutil.QueryNamedOne[struct {
		Vat decimal.Decimal `db:"vat"`
		Net decimal.Decimal `db:"net"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN a.code = '2070' THEN `+signedAmount+` ELSE 0 END), 0) AS vat,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040') THEN `+signedAmount+` ELSE 0 END), 0) AS net
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		WHERE e.source_type IN ('order_sale','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime = 'uk_stock_domestic'
		  AND a.code IN ('2070','4010','4020','4310','4110','4040')`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: uk vat sales %s: %w", from.Format(dateLayout), err)
	}

	// Purchase side: reclaimable input VAT (2080) + net purchase base (1110), for domestic_uk receipts.
	purch, err := storeutil.QueryNamedOne[struct {
		Vat decimal.Decimal `db:"vat"`
		Net decimal.Decimal `db:"net"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(CASE WHEN a.code = '2080' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS vat,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS net
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime = 'domestic_uk'
		  AND a.code IN ('2080','1110')`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: uk vat purchases %s: %w", from.Format(dateLayout), err)
	}

	return &entity.AcctUkVatReturn{
		QuarterStart:     from,
		Box1OutputVat:    sale.Vat,
		Box6NetSales:     sale.Net,
		Box4InputVat:     purch.Vat,
		Box7NetPurchases: purch.Net,
	}, nil
}
