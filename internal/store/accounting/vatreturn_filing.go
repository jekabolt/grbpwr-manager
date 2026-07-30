package accounting

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/store/storeutil"
	"github.com/shopspring/decimal"
)

// Filing-currency variants of the VAT reports (statutory review 13, P0 "currency" blocker). The
// ledger is EUR; the Polish JPK_V7M must be filed in PLN and the UK return in GBP. Conversion is
// per transaction at the DAILY reference rate effective on the last day STRICTLY BEFORE the tax
// point — the D-1 rule of both art. 31a ustawy o VAT and HMRC's daily-rate guidance. Rates come
// from costing_fx_rate, which the fxsync worker fills every day from the ECB reference feed:
// Poland explicitly permits ECB rates instead of NBP (art. 31a ust. 2a–2d; the election binds for
// 12 months — noted in docs/plan-accounting-phase2/13-statutory-review.md and to be confirmed
// with the accountant). A date with NO stored rate makes the export FAIL LOUDLY (the missing
// dates are listed) rather than silently misstating a filing.
//
// The management (EUR) variants in vatreturn.go stay untouched — the app screens keep showing
// ledger truth; these variants exist for the generated filings only.

// fxSeries is one currency's daily rate history (rate_to_base = EUR per 1 unit), sorted by date.
type fxSeries struct {
	currency string
	dates    []time.Time
	rates    []decimal.Decimal
}

// loadFxSeries loads the full daily history for one currency, oldest first.
func (s *Store) loadFxSeries(ctx context.Context, currency string) (*fxSeries, error) {
	rows, err := storeutil.QueryListNamed[struct {
		ValidFrom time.Time       `db:"valid_from"`
		Rate      decimal.Decimal `db:"rate_to_base"`
	}](ctx, s.DB, `
		SELECT valid_from, rate_to_base FROM costing_fx_rate
		WHERE currency = :cur ORDER BY valid_from`,
		map[string]any{"cur": strings.ToUpper(currency)})
	if err != nil {
		return nil, fmt.Errorf("accounting: load fx series %s: %w", currency, err)
	}
	fs := &fxSeries{currency: strings.ToUpper(currency)}
	for _, r := range rows {
		fs.dates = append(fs.dates, r.ValidFrom)
		fs.rates = append(fs.rates, r.Rate)
	}
	return fs, nil
}

// rateBefore returns the rate effective on the last stored day STRICTLY BEFORE day (D-1: the ECB
// publishes business days only, so this lands on the preceding business day automatically).
func (f *fxSeries) rateBefore(day time.Time) (decimal.Decimal, bool) {
	// first index with dates[i] >= day → the one before it is the D-1 rate.
	i := sort.Search(len(f.dates), func(i int) bool { return !f.dates[i].Before(day) })
	if i == 0 {
		return decimal.Decimal{}, false
	}
	return f.rates[i-1], true
}

// fromEUR converts an EUR amount into the series currency at the D-1 rate of day, rounded to 2dp.
// rate_to_base is "EUR per 1 unit of currency", so EUR→currency divides.
func (f *fxSeries) fromEUR(eur decimal.Decimal, day time.Time) (decimal.Decimal, bool) {
	rate, ok := f.rateBefore(day)
	if !ok || !rate.IsPositive() {
		return decimal.Decimal{}, false
	}
	return eur.Div(rate).Round(2), true
}

// missingRateCaveat formats the fail-loud caveat for dates without a rate.
func missingRateCaveat(currency string, days map[string]struct{}) string {
	list := make([]string, 0, len(days))
	for d := range days {
		list = append(list, d)
	}
	sort.Strings(list)
	const max = 8
	if len(list) > max {
		list = append(list[:max], fmt.Sprintf("… +%d more", len(list)-max))
	}
	return fmt.Sprintf("no %s reference rate stored for: %s — enable/backfill fxsync (costing_fx_rate) before filing", currency, strings.Join(list, ", "))
}

// dailyRegimeSum is one (day, key) bucket of EUR sums — the granularity FX conversion needs (the
// rate is a per-day constant, so daily buckets convert exactly like per-row conversion).
type dailyRegimeSum struct {
	Day    time.Time       `db:"day"`
	Key    string          `db:"k"`
	Amount decimal.Decimal `db:"amount"`
	Extra  decimal.Decimal `db:"extra"`
	Third  decimal.Decimal `db:"third"`
}

// GetVatReturnPLFiling builds the JPK_V7M monthly aggregate in PLN: the same ledger sources as
// GetVatReturnPL, converted per tax-point day. Register-backed OPEX input VAT (doc identity
// present, regime domestic_pl) is included in InputDomestic/NetInputDomestic so the declaration's
// P_42/P_43 always cross-check with the emitted purchase register; VAT-recorded-but-undocumented
// opex lands in InputUnregistered + a caveat and is deducted only in the management summary.
func (s *Store) GetVatReturnPLFiling(ctx context.Context, month time.Time) (*entity.AcctVatReturnPL, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	pln, err := s.loadFxSeries(ctx, "PLN")
	if err != nil {
		return nil, err
	}
	missing := map[string]struct{}{}
	conv := func(eur decimal.Decimal, day time.Time) decimal.Decimal {
		v, ok := pln.fromEUR(eur, day)
		if !ok {
			missing[day.Format(dateLayout)] = struct{}{}
			return decimal.Zero
		}
		return v
	}

	ret := &entity.AcctVatReturnPL{Month: from, Currency: "PLN"}

	// Output VAT (2070) by regime, daily.
	outRows, err := storeutil.QueryListNamed[dailyRegimeSum](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day, co.vat_regime AS k,
		       COALESCE(SUM(`+signedAmount+`), 0) AS amount,
		       0 AS extra, 0 AS third
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		WHERE a.code = '2070'
		  AND e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		GROUP BY DATE(e.occurred_at), co.vat_regime`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing output vat: %w", err)
	}
	for _, r := range outRows {
		v := conv(r.Amount, r.Day)
		switch r.Key {
		case "pl_domestic":
			ret.OutputDomestic = ret.OutputDomestic.Add(v)
		case "uk_stock_domestic":
			ret.OutputUkStockDomestic = ret.OutputUkStockDomestic.Add(v)
		case "oss":
			ret.OssInfoTotal = ret.OssInfoTotal.Add(v)
		}
	}

	// Material receipts: self-charge output (2070 credit), input VAT (2080 debit) and net base
	// (1110 debit) by input regime, daily.
	recRows, err := storeutil.QueryListNamed[dailyRegimeSum](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day, m.input_vat_regime AS k,
		       COALESCE(SUM(CASE WHEN a.code = '2080' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS amount,
		       COALESCE(SUM(CASE WHEN a.code = '2070' AND l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS extra,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS third
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime IS NOT NULL
		GROUP BY DATE(e.occurred_at), m.input_vat_regime`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing receipt vat: %w", err)
	}
	for _, r := range recRows {
		vat := conv(r.Amount, r.Day)
		out := conv(r.Extra, r.Day)
		net := conv(r.Third, r.Day)
		switch r.Key {
		case "wnt":
			ret.InputWnt = ret.InputWnt.Add(vat)
			ret.OutputWntSelfCharge = ret.OutputWntSelfCharge.Add(out)
			ret.NetWnt = ret.NetWnt.Add(net)
		case "import":
			ret.InputImport = ret.InputImport.Add(vat)
			ret.OutputWntSelfCharge = ret.OutputWntSelfCharge.Add(out)
			ret.NetImport = ret.NetImport.Add(net)
		case "domestic_pl":
			ret.InputDomestic = ret.InputDomestic.Add(vat)
			ret.NetInputDomestic = ret.NetInputDomestic.Add(net)
		case "domestic_uk":
			ret.InputUkDomestic = ret.InputUkDomestic.Add(vat)
		}
	}

	// Net revenue bases by order regime (declaration nets), daily.
	netRows, err := storeutil.QueryListNamed[dailyRegimeSum](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day, co.vat_regime AS k,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040','2090')
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS amount,
		       0 AS extra, 0 AS third
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		WHERE e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime IN ('pl_domestic','wdt','export')
		GROUP BY DATE(e.occurred_at), co.vat_regime`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing net bases: %w", err)
	}
	for _, r := range netRows {
		v := conv(r.Amount, r.Day)
		switch r.Key {
		case "pl_domestic":
			ret.NetDomestic = ret.NetDomestic.Add(v)
		case "wdt":
			ret.NetWdt = ret.NetWdt.Add(v)
		case "export":
			ret.NetExport = ret.NetExport.Add(v)
		}
	}

	// OPEX input VAT (migration 0203), split register-backed vs undocumented. Conversion date is
	// the invoice date; PLN-invoiced lines use their original PLN figures directly (no EUR round
	// trip). A line with VAT but no doc identity cannot form a register row → InputUnregistered.
	opexRows, err := storeutil.QueryListNamed[struct {
		Day      time.Time           `db:"day"`
		Currency string              `db:"currency"`
		Amount   decimal.Decimal     `db:"amount"`
		Vat      decimal.Decimal     `db:"vat"`
		AmountB  decimal.NullDecimal `db:"amount_base"`
		VatB     decimal.NullDecimal `db:"vat_base"`
		HasDoc   bool                `db:"has_doc"`
	}](ctx, s.DB, `
		SELECT COALESCE(doc_date, DATE_SUB(DATE_ADD(month, INTERVAL 1 MONTH), INTERVAL 1 DAY)) AS day,
		       currency, amount, COALESCE(vat_amount, 0) AS vat,
		       amount_base, vat_amount_base AS vat_base,
		       (doc_number IS NOT NULL AND doc_number <> '' AND doc_date IS NOT NULL) AS has_doc
		FROM opex_line
		WHERE month = :from AND vat_amount IS NOT NULL AND vat_regime = 'domestic_pl'`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing opex input: %w", err)
	}
	for _, r := range opexRows {
		var vatPLN, netPLN decimal.Decimal
		if strings.EqualFold(r.Currency, "PLN") {
			vatPLN = r.Vat.Round(2)
			netPLN = r.Amount.Round(2)
		} else {
			if !r.VatB.Valid || !r.AmountB.Valid {
				continue // uncosted line — already caveated by the opex posting
			}
			vatPLN = conv(r.VatB.Decimal, r.Day)
			netPLN = conv(r.AmountB.Decimal, r.Day)
		}
		if r.HasDoc {
			ret.InputDomestic = ret.InputDomestic.Add(vatPLN)
			ret.NetInputDomestic = ret.NetInputDomestic.Add(netPLN)
		} else {
			ret.InputUnregistered = ret.InputUnregistered.Add(vatPLN)
		}
	}
	if ret.InputUnregistered.IsPositive() {
		ret.Caveats = append(ret.Caveats, fmt.Sprintf(
			"%s PLN of opex input VAT has no invoice number/date — excluded from the generated JPK (no register row possible); add doc identity to deduct it in the filing",
			ret.InputUnregistered.StringFixed(2)))
	}
	// WNT/import self-charge IS in the generated filing now (declaration P_23..P_26 + K_23..K_26
	// sales rows for the self-charged output, K_42/K_43 purchase rows for the deduction), so it no
	// longer needs a merge-manually caveat — a JPK that omitted it while the generated VAT-UE
	// declared the same acquisitions was an automatic MF cross-check mismatch (review pass 3,
	// superseding review pass 1 H-1). What still needs flagging is an acquisition whose supplier has
	// no VAT id: the JPK row falls back to BRAK and the VAT-UE cannot declare it at all.
	if ret.InputWnt.IsPositive() || ret.InputImport.IsPositive() {
		untagged, uErr := storeutil.QueryCountNamed(ctx, s.DB, `
			SELECT COUNT(DISTINCT m.id)
			FROM material_stock_movement m
			LEFT JOIN supplier sup ON sup.id = m.supplier_id
			JOIN acct_journal_entry e ON e.source_type = 'material_receipt' AND `+movementKeyMatch+`
			WHERE e.occurred_at >= :from AND e.occurred_at < :to
			  AND m.input_vat_regime IN ('wnt', 'import')
			  AND (sup.vat_id IS NULL OR sup.vat_id = '')`, params)
		if uErr == nil && untagged > 0 {
			ret.Caveats = append(ret.Caveats, fmt.Sprintf(
				"%d WNT/import receipt(s) have no supplier VAT id — the JPK row reports BRAK and the VAT-UE statement cannot declare them; tag the supplier before filing", untagged))
		}
	}

	ret.NetPayable = ret.OutputDomestic.Add(ret.OutputWntSelfCharge).
		Sub(ret.InputDomestic).Sub(ret.InputWnt).Sub(ret.InputImport)

	if len(missing) > 0 {
		return nil, fmt.Errorf("accounting: %s", missingRateCaveat("PLN", missing))
	}
	caveats, cErr := s.vatReturnCaveats(ctx, from.Format(dateLayout), to.Format(dateLayout))
	if cErr == nil {
		ret.Caveats = append(ret.Caveats, caveats...)
	}
	if cv, dErr := s.opexAccrualDriftCaveat(ctx, from); dErr == nil && cv != "" {
		ret.Caveats = append(ret.Caveats, cv)
	}
	return ret, nil
}

// opexAccrualDriftCaveat compares the 2080 input VAT the month's opex_line rows WOULD accrue (the
// posting rule applied to the table the filings read) against the posted, non-reversed opex_month
// 2080 accrual (what the ledger holds). They diverge when a line is edited after the period closed
// (commitOpex warns + skips the repost) or just before the worker's next tick — the filing would then
// silently disagree with the books (review pass 2, H-1). Both sides are base-currency EUR, so the
// comparison needs no FX.
func (s *Store) opexAccrualDriftCaveat(ctx context.Context, month time.Time) (string, error) {
	m := firstOfMonthUTC(month)
	// The table side must mirror the POSTING rule (BuildOpexMonthEntry + GetOpexMonthFacts), not just
	// sum the column: the accrual groups by category over costed lines (amount_base NOT NULL), rounds
	// to 2dp, and posts a category's VAT only when it is POSITIVE — a category netting negative (a
	// credit note) is dropped rather than credited back. A plain signed SUM therefore reported drift
	// that NO repost could ever clear, so the one caveat that matters cried wolf on every filing of
	// such a month (review pass 3).
	table, err := storeutil.QueryNamedOne[struct {
		Vat decimal.Decimal `db:"vat"`
		Amt decimal.Decimal `db:"amt"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(GREATEST(ROUND(vat_sum, 2), 0)), 0) AS vat,
		       COALESCE(SUM(GREATEST(ROUND(amt_sum, 2), 0)), 0) AS amt
		FROM (
			SELECT COALESCE(SUM(amount_base), 0) AS amt_sum,
			       COALESCE(SUM(vat_amount_base), 0) AS vat_sum
			FROM opex_line
			WHERE month = :m AND amount_base IS NOT NULL
			GROUP BY category
		) c`,
		map[string]any{"m": m.Format(dateLayout)})
	if err != nil {
		return "", fmt.Errorf("accounting: opex drift table sum: %w", err)
	}
	// A month with no positive category amount posts NOTHING (ErrSkipEmpty), VAT included — so the
	// ledger holding zero is the correct outcome there, not drift.
	expectedVat := table.Vat
	if !table.Amt.IsPositive() {
		expectedVat = decimal.Zero
	}
	ledger, err := storeutil.QueryNamedOne[struct {
		V decimal.Decimal `db:"v"`
	}](ctx, s.DB, `
		SELECT COALESCE(SUM(l.amount), 0) AS v
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		WHERE e.source_type = 'opex_month' AND e.reversed_by IS NULL
		  AND e.source_key LIKE :prefix
		  AND a.code = '2080' AND l.side = 'debit'`,
		map[string]any{"prefix": m.Format("2006-01") + "%"})
	if err != nil {
		return "", fmt.Errorf("accounting: opex drift ledger sum: %w", err)
	}
	if expectedVat.Sub(ledger.V).Abs().Cmp(decimal.NewFromFloat(0.01)) < 0 {
		return "", nil
	}
	return fmt.Sprintf(
		"opex input VAT drift %s: the month's opex lines imply %s EUR of recoverable input VAT but the posted 2080 accrual holds %s EUR — a line changed after close or before the worker's tick; reopen the period and let it repost before filing",
		m.Format("2006-01"), expectedVat.StringFixed(2), ledger.V.StringFixed(2)), nil
}

// VatSalesEvidenceFiling returns the JPK sales-register rows in PLN with filing semantics: rows
// split sale vs refund (a B2B refund becomes its own corrective row), stamped and converted at the
// tax-point day (payment for sales, refund date for refunds), and carrying the buyer's name
// (billing company, else first+last name). It also returns the month's WNT / import self-charge rows
// (regime = entity.InputVatRegime wnt / import): a reverse charge is declared on the OUTPUT side of
// the register (K_23..K_26), and without those rows the JPK contradicted the VAT-UE statement, which
// declares the same WNT acquisitions.
func (s *Store) VatSalesEvidenceFiling(ctx context.Context, month time.Time) ([]entity.AcctVatSalesRow, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	pln, err := s.loadFxSeries(ctx, "PLN")
	if err != nil {
		return nil, err
	}
	missing := map[string]struct{}{}

	rows, err := storeutil.QueryListNamed[entity.AcctVatSalesRow](ctx, s.DB, `
		SELECT co.uuid AS uuid,
		       co.placed AS placed,
		       co.buyer_vat_id AS buyer_vat_id,
		       co.vat_regime AS regime,
		       DATE(e.occurred_at) AS tax_point,
		       (e.source_type = 'order_refund') AS is_refund,
		       COALESCE(NULLIF(ba.company, ''), CONCAT(b.first_name, ' ', b.last_name)) AS buyer_name,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040','2090')
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS net,
		       COALESCE(SUM(CASE WHEN a.code = '2070'
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		LEFT JOIN buyer b ON b.order_id = co.id
		LEFT JOIN address ba ON ba.id = b.billing_address_id
		WHERE e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime IN ('pl_domestic','wdt','export')
		GROUP BY co.uuid, co.placed, co.buyer_vat_id, co.vat_regime,
		         DATE(e.occurred_at), (e.source_type = 'order_refund'), buyer_name
		ORDER BY tax_point, co.uuid`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing sales evidence: %w", err)
	}

	// WNT / import (art. 33a) self-charge: the reverse-charge OUTPUT leg (2070 credit) against the
	// material's net base (1110 debit), per receipt document. It belongs in the sales register
	// (K_23/K_24, K_25/K_26) with the SUPPLIER as counterparty, while its deductible leg is a
	// purchase row — that is how the declaration's P_23..P_26 stay evidenced and how the JPK agrees
	// with the VAT-UE statement built from the same movements. The self-charge posts Dr 2080 / Cr
	// 2070 with one amount (rule M1), so this VAT equals the input claimed on the purchase side.
	// domestic_uk is NOT here: it is a different jurisdiction (UK return).
	selfCharge, err := storeutil.QueryListNamed[entity.AcctVatSalesRow](ctx, s.DB, `
		SELECT COALESCE(NULLIF(m.supplier_doc, ''), CONCAT('MOV-', m.id)) AS uuid,
		       DATE(e.occurred_at) AS placed,
		       NULLIF(COALESCE(sup.vat_id, ''), '') AS buyer_vat_id,
		       m.input_vat_regime AS regime,
		       DATE(e.occurred_at) AS tax_point,
		       FALSE AS is_refund,
		       NULLIF(COALESCE(sup.name, ''), '') AS buyer_name,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS net,
		       COALESCE(SUM(CASE WHEN a.code = '2070' AND l.side = 'credit' THEN l.amount ELSE 0 END), 0) AS vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		LEFT JOIN supplier sup ON sup.id = m.supplier_id
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime IN ('wnt', 'import')
		GROUP BY uuid, DATE(e.occurred_at), buyer_vat_id, m.input_vat_regime, buyer_name
		ORDER BY tax_point, uuid`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing self-charge evidence: %w", err)
	}
	rows = append(rows, selfCharge...)

	for i := range rows {
		day := rows[i].TaxPointAt
		net, ok1 := pln.fromEUR(rows[i].Net, day)
		vat, ok2 := pln.fromEUR(rows[i].Vat, day)
		if !ok1 || !ok2 {
			missing[day.Format(dateLayout)] = struct{}{}
			continue
		}
		rows[i].Net = net
		rows[i].Vat = vat
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("accounting: %s", missingRateCaveat("PLN", missing))
	}
	return rows, nil
}

// VatPurchaseEvidenceFiling returns the JPK purchase-register rows (ewidencja zakupu) in PLN:
// material receipts whose input VAT is deductible in Poland — domestic_pl plus the WNT / import
// (art. 33a) self-charge input legs, all with the supplier + supplier_doc identity a register row
// needs — and register-backed OPEX lines. Every row lands in K_42/K_43 ("other purchases"), so the
// rows sum to the declaration's P_42/P_43/P_48; the self-charge's OUTPUT leg is a sales-register row
// (K_23..K_26). domestic_uk is excluded: it is reclaimed on the UK return, not the JPK.
func (s *Store) VatPurchaseEvidenceFiling(ctx context.Context, month time.Time) ([]entity.AcctVatPurchaseRow, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	pln, err := s.loadFxSeries(ctx, "PLN")
	if err != nil {
		return nil, err
	}
	missing := map[string]struct{}{}
	out := make([]entity.AcctVatPurchaseRow, 0, 16)

	receipts, err := storeutil.QueryListNamed[struct {
		Day     time.Time       `db:"day"`
		DocNo   string          `db:"doc_no"`
		SupVat  string          `db:"sup_vat"`
		SupName string          `db:"sup_name"`
		Net     decimal.Decimal `db:"net"`
		Vat     decimal.Decimal `db:"vat"`
	}](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day,
		       COALESCE(NULLIF(m.supplier_doc, ''), CONCAT('MOV-', m.id)) AS doc_no,
		       COALESCE(sup.vat_id, '') AS sup_vat,
		       COALESCE(sup.name, '') AS sup_name,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS net,
		       COALESCE(SUM(CASE WHEN a.code = '2080' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS vat
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		LEFT JOIN supplier sup ON sup.id = m.supplier_id
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime IN ('domestic_pl', 'wnt', 'import')
		GROUP BY day, doc_no, sup_vat, sup_name`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing purchase receipts: %w", err)
	}
	for _, r := range receipts {
		net, ok1 := pln.fromEUR(r.Net, r.Day)
		vat, ok2 := pln.fromEUR(r.Vat, r.Day)
		if !ok1 || !ok2 {
			missing[r.Day.Format(dateLayout)] = struct{}{}
			continue
		}
		out = append(out, entity.AcctVatPurchaseRow{
			DocNumber: r.DocNo, DocDate: r.Day,
			SupplierVatId: r.SupVat, SupplierName: r.SupName,
			Net: net, Vat: vat,
		})
	}

	opex, err := storeutil.QueryListNamed[struct {
		Day      time.Time           `db:"day"`
		DocNo    string              `db:"doc_no"`
		SupVat   string              `db:"sup_vat"`
		SupName  string              `db:"sup_name"`
		Currency string              `db:"currency"`
		Amount   decimal.Decimal     `db:"amount"`
		Vat      decimal.Decimal     `db:"vat"`
		AmountB  decimal.NullDecimal `db:"amount_base"`
		VatB     decimal.NullDecimal `db:"vat_base"`
	}](ctx, s.DB, `
		SELECT doc_date AS day, doc_number AS doc_no,
		       COALESCE(supplier_vat_id, '') AS sup_vat,
		       COALESCE(supplier_name, '') AS sup_name,
		       currency, amount, COALESCE(vat_amount, 0) AS vat,
		       amount_base, vat_amount_base AS vat_base
		FROM opex_line
		WHERE month = :from AND vat_amount IS NOT NULL AND vat_regime = 'domestic_pl'
		  AND doc_number IS NOT NULL AND doc_number <> '' AND doc_date IS NOT NULL`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: filing purchase opex: %w", err)
	}
	for _, r := range opex {
		var net, vat decimal.Decimal
		if strings.EqualFold(r.Currency, "PLN") {
			net, vat = r.Amount.Round(2), r.Vat.Round(2)
		} else {
			if !r.VatB.Valid || !r.AmountB.Valid {
				continue
			}
			var ok1, ok2 bool
			net, ok1 = pln.fromEUR(r.AmountB.Decimal, r.Day)
			vat, ok2 = pln.fromEUR(r.VatB.Decimal, r.Day)
			if !ok1 || !ok2 {
				missing[r.Day.Format(dateLayout)] = struct{}{}
				continue
			}
		}
		out = append(out, entity.AcctVatPurchaseRow{
			DocNumber: r.DocNo, DocDate: r.Day,
			SupplierVatId: r.SupVat, SupplierName: r.SupName,
			Net: net, Vat: vat,
		})
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("accounting: %s", missingRateCaveat("PLN", missing))
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].DocDate.Equal(out[j].DocDate) {
			return out[i].DocDate.Before(out[j].DocDate)
		}
		return out[i].DocNumber < out[j].DocNumber
	})
	return out, nil
}

// GetVatUe returns the monthly VAT-UE recapitulative statement (informacja podsumowująca) in PLN:
// WDT supplies grouped by the buyer's EU VAT id, WNT acquisitions grouped by the supplier's VAT
// id. Rows without a counterparty VAT id cannot be declared and are caveated.
func (s *Store) GetVatUe(ctx context.Context, month time.Time) (*entity.AcctVatUe, error) {
	from := firstOfMonthUTC(month)
	to := from.AddDate(0, 1, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	pln, err := s.loadFxSeries(ctx, "PLN")
	if err != nil {
		return nil, err
	}
	missing := map[string]struct{}{}
	ue := &entity.AcctVatUe{Month: from}

	wdt, err := storeutil.QueryListNamed[entity.AcctVatUeRow](ctx, s.DB, `
		SELECT COALESCE(co.buyer_vat_id, '') AS vat_id,
		       COALESCE(NULLIF(ba.company, ''), CONCAT(COALESCE(b.first_name,''), ' ', COALESCE(b.last_name,''))) AS name,
		       DATE(e.occurred_at) AS tax_point,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040','2090')
		                         THEN `+signedAmount+` ELSE 0 END), 0) AS net
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		LEFT JOIN buyer b ON b.order_id = co.id
		LEFT JOIN address ba ON ba.id = b.billing_address_id
		WHERE e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime = 'wdt'
		GROUP BY vat_id, name, DATE(e.occurred_at)`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: vat-ue wdt: %w", err)
	}
	ue.Wdt = mergeVatUeRows(wdt, pln, missing, &ue.Caveats, "WDT supply")

	wnt, err := storeutil.QueryListNamed[entity.AcctVatUeRow](ctx, s.DB, `
		SELECT COALESCE(sup.vat_id, '') AS vat_id,
		       COALESCE(sup.name, '') AS name,
		       DATE(e.occurred_at) AS tax_point,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS net
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		LEFT JOIN supplier sup ON sup.id = m.supplier_id
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime = 'wnt'
		GROUP BY vat_id, name, DATE(e.occurred_at)`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: vat-ue wnt: %w", err)
	}
	ue.Wnt = mergeVatUeRows(wnt, pln, missing, &ue.Caveats, "WNT acquisition")

	if len(missing) > 0 {
		return nil, fmt.Errorf("accounting: %s", missingRateCaveat("PLN", missing))
	}
	return ue, nil
}

// mergeVatUeRows converts daily rows to PLN, merges them per counterparty VAT id, derives the
// country prefix, and caveats rows that have amounts but no VAT id (undeclarable).
func mergeVatUeRows(rows []entity.AcctVatUeRow, pln *fxSeries, missing map[string]struct{}, caveats *[]string, what string) []entity.AcctVatUeRow {
	byID := map[string]*entity.AcctVatUeRow{}
	order := []string{}
	var untagged decimal.Decimal
	for _, r := range rows {
		v, ok := pln.fromEUR(r.NetBase, r.TaxPoint)
		if !ok {
			missing[r.TaxPoint.Format(dateLayout)] = struct{}{}
			continue
		}
		id := strings.ToUpper(strings.TrimSpace(r.CounterpartyVatId))
		if id == "" {
			untagged = untagged.Add(v)
			continue
		}
		agg, exists := byID[id]
		if !exists {
			cp := r
			cp.CounterpartyVatId = id
			cp.NetPln = decimal.Zero
			if len(id) >= 2 && id[0] >= 'A' && id[0] <= 'Z' && id[1] >= 'A' && id[1] <= 'Z' {
				cp.Country = id[:2]
			}
			byID[id] = &cp
			order = append(order, id)
			agg = &cp
		}
		agg.NetPln = agg.NetPln.Add(v)
	}
	out := make([]entity.AcctVatUeRow, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	if !untagged.IsZero() {
		*caveats = append(*caveats, fmt.Sprintf(
			"%s PLN of %s carries NO counterparty VAT id — it cannot be declared on VAT-UE; tag the counterparty and re-check", untagged.StringFixed(2), what))
	}
	return out
}

// GetUkVatReturnFiling builds the quarterly UK 9-box return in GBP, converted per tax-point day
// (same D-1 daily-rate rule). Purchases include domestic_uk material receipts AND register-backed
// domestic_uk OPEX input (Box 4 / Box 7).
func (s *Store) GetUkVatReturnFiling(ctx context.Context, quarterStart time.Time) (*entity.AcctUkVatReturn, error) {
	from := firstOfMonthUTC(quarterStart)
	to := from.AddDate(0, 3, 0)
	params := map[string]any{"from": from.Format(dateLayout), "to": to.Format(dateLayout)}

	gbp, err := s.loadFxSeries(ctx, "GBP")
	if err != nil {
		return nil, err
	}
	missing := map[string]struct{}{}
	ret := &entity.AcctUkVatReturn{QuarterStart: from, Currency: "GBP"}
	conv := func(eur decimal.Decimal, day time.Time) decimal.Decimal {
		v, ok := gbp.fromEUR(eur, day)
		if !ok {
			missing[day.Format(dateLayout)] = struct{}{}
			return decimal.Zero
		}
		return v
	}

	sales, err := storeutil.QueryListNamed[dailyRegimeSum](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day, 'sale' AS k,
		       COALESCE(SUM(CASE WHEN a.code = '2070' THEN `+signedAmount+` ELSE 0 END), 0) AS amount,
		       COALESCE(SUM(CASE WHEN a.code IN ('4010','4020','4310','4110','4040','2090') THEN `+signedAmount+` ELSE 0 END), 0) AS extra,
		       0 AS third
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN customer_order co ON `+orderKeyMatch+`
		WHERE e.source_type IN ('order_sale','order_prepayment','order_refund')
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND co.vat_regime = 'uk_stock_domestic'
		GROUP BY DATE(e.occurred_at)`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: uk filing sales: %w", err)
	}
	for _, r := range sales {
		ret.Box1OutputVat = ret.Box1OutputVat.Add(conv(r.Amount, r.Day))
		ret.Box6NetSales = ret.Box6NetSales.Add(conv(r.Extra, r.Day))
	}

	purch, err := storeutil.QueryListNamed[dailyRegimeSum](ctx, s.DB, `
		SELECT DATE(e.occurred_at) AS day, 'purchase' AS k,
		       COALESCE(SUM(CASE WHEN a.code = '2080' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS amount,
		       COALESCE(SUM(CASE WHEN a.code = '1110' AND l.side = 'debit' THEN l.amount ELSE 0 END), 0) AS extra,
		       0 AS third
		FROM acct_journal_line l
		JOIN acct_journal_entry e ON e.id = l.entry_id
		JOIN acct_account a ON a.id = l.account_id
		JOIN material_stock_movement m ON `+movementKeyMatch+`
		WHERE e.source_type = 'material_receipt'
		  AND e.occurred_at >= :from AND e.occurred_at < :to
		  AND m.input_vat_regime = 'domestic_uk'
		GROUP BY DATE(e.occurred_at)`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: uk filing purchases: %w", err)
	}
	for _, r := range purch {
		ret.Box4InputVat = ret.Box4InputVat.Add(conv(r.Amount, r.Day))
		ret.Box7NetPurchases = ret.Box7NetPurchases.Add(conv(r.Extra, r.Day))
	}

	opex, err := storeutil.QueryListNamed[struct {
		Day      time.Time           `db:"day"`
		Currency string              `db:"currency"`
		Amount   decimal.Decimal     `db:"amount"`
		Vat      decimal.Decimal     `db:"vat"`
		AmountB  decimal.NullDecimal `db:"amount_base"`
		VatB     decimal.NullDecimal `db:"vat_base"`
	}](ctx, s.DB, `
		SELECT COALESCE(doc_date, DATE_SUB(DATE_ADD(month, INTERVAL 1 MONTH), INTERVAL 1 DAY)) AS day,
		       currency, amount, COALESCE(vat_amount, 0) AS vat,
		       amount_base, vat_amount_base AS vat_base
		FROM opex_line
		WHERE month >= :from AND month < :to AND vat_amount IS NOT NULL AND vat_regime = 'domestic_uk'`, params)
	if err != nil {
		return nil, fmt.Errorf("accounting: uk filing opex: %w", err)
	}
	for _, r := range opex {
		var net, vat decimal.Decimal
		if strings.EqualFold(r.Currency, "GBP") {
			net, vat = r.Amount.Round(2), r.Vat.Round(2)
		} else {
			if !r.VatB.Valid || !r.AmountB.Valid {
				continue
			}
			net = conv(r.AmountB.Decimal, r.Day)
			vat = conv(r.VatB.Decimal, r.Day)
		}
		ret.Box4InputVat = ret.Box4InputVat.Add(vat)
		ret.Box7NetPurchases = ret.Box7NetPurchases.Add(net)
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("accounting: %s", missingRateCaveat("GBP", missing))
	}
	// Same table-vs-ledger drift guard as the PL filing, month by month over the quarter.
	for i := 0; i < 3; i++ {
		if cv, dErr := s.opexAccrualDriftCaveat(ctx, from.AddDate(0, i, 0)); dErr == nil && cv != "" {
			ret.Caveats = append(ret.Caveats, cv)
		}
	}
	return ret, nil
}
