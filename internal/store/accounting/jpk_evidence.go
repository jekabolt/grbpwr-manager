package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	storeutil "github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
)

// VatSalesEvidence returns one row per order for the JPK_V7M sales register (Ewidencja/SprzedazWiersz),
// covering the month's confirmed sales in the regimes the Polish register declares: pl_domestic (23%),
// wdt (intra-community, 0%) and export (0%). OSS and uk_stock are filed on their own returns and are
// excluded here, exactly as GetVatReturnPL excludes them.
//
// Net is the signed revenue (refunds subtract) from the same accounts the declaration net bases use;
// Vat is the signed 2070 output VAT. The JPK builder aggregates the B2C rows (no BuyerVatID) into a
// periodic internal document per regime and emits the B2B rows (with a buyer NIP) individually.
func (s *Store) VatSalesEvidence(ctx context.Context, month time.Time) ([]entity.AcctVatSalesRow, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)

	rows, err := storeutil.QueryListNamed[entity.AcctVatSalesRow](ctx, s.DB, `
		SELECT co.uuid AS uuid,
		       co.placed AS placed,
		       co.buyer_vat_id AS buyer_vat_id,
		       co.vat_regime AS regime,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040','2090')
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS net,
		       COALESCE(SUM(CASE WHEN a.code = '2070'
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		-- Wave 2 (delivered recognition): post-cutover the tax point stays at payment, so the register
		-- reads order_prepayment (VAT on 2070, net base on 2090); order_delivered_sale is excluded (its
		-- revenue is a later period). Consistent with GetVatReturnPL.
		WHERE e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime IN ('pl_domestic','wdt','export')
		GROUP BY co.uuid, co.placed, co.buyer_vat_id, co.vat_regime
		ORDER BY co.placed, co.uuid`,
		map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)})
	if err != nil {
		return nil, fmt.Errorf("accounting: vat sales evidence %s: %w", from.Format(dateLayout), err)
	}
	return rows, nil
}
