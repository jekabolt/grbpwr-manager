package accounting

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

// Revolut CSV inbox (phase 2, wave 4 — docs/plan-accounting-phase2/04-wave4-money.md §4.1). A bank
// statement export is parsed into acct_bank_txn inbox lines the operator posts (PostBankTxn) or ignores.
// The parser is pure (no DB, no clock beyond time-parsing) and sits behind BankCsvParser so a second bank
// is a second implementation. Substring→account suggestions (acct_bank_rule) are applied later in the
// store; the parser only sets the default disposition (ignored for an internal EXCHANGE leg, else unmatched).

// BankCsvParser turns a raw bank CSV export into inbox lines. A malformed / wrong-bank file (missing
// required columns) is an error; individual rows that cannot be used (not COMPLETED, unparseable amount
// or date) are skipped, not errored, so one bad line never rejects a whole statement.
type BankCsvParser interface {
	// Source is the acct_bank_txn.source tag this parser writes (e.g. "revolut").
	Source() string
	// Parse returns the importable lines of csvText (COMPLETED rows only), in file order.
	Parse(csvText string) ([]entity.AcctBankTxnInsert, error)
}

// RevolutParser parses a Revolut multi-currency account CSV export against the owner's real column set
// (33 comma-separated columns). Columns are resolved by HEADER NAME, not position, so a future column
// reorder does not silently misread — see revolutColumns.
type RevolutParser struct{}

// NewRevolutParser returns the Revolut CSV parser.
func NewRevolutParser() *RevolutParser { return &RevolutParser{} }

// Source implements BankCsvParser.
func (RevolutParser) Source() string { return "revolut" }

// revolutColumns are the header names the parser reads. Kept in ONE block so the header→index mapping
// survives a column reorder (the file is matched by name, not position).
const (
	revColID              = "ID"
	revColType            = "Type"
	revColState           = "State"
	revColDescription     = "Description"
	revColPayer           = "Payer"
	revColPaymentCurrency = "Payment currency"
	revColAmount          = "Amount"
	revColFee             = "Fee"
	revColDateCompleted   = "Date completed (UTC)"
	revColDateStarted     = "Date started (UTC)"
	revColBeneficiaryName = "Beneficiary name"
	revColSenderName      = "Sender name"
)

// revolutRequiredColumns must all be present or the file is rejected (wrong bank / corrupt export).
var revolutRequiredColumns = []string{
	revColID, revColType, revColState, revColPaymentCurrency, revColAmount,
}

// revolutDateLayouts are tried in order against the Revolut UTC datetime columns.
var revolutDateLayouts = []string{
	"2006-01-02 15:04:05",
	"2006-01-02 15:04:05.000000",
	"2006-01-02T15:04:05.999999Z07:00",
	time.RFC3339,
	"2006-01-02",
}

// Parse implements BankCsvParser.
func (p RevolutParser) Parse(csvText string) ([]entity.AcctBankTxnInsert, error) {
	r := csv.NewReader(strings.NewReader(csvText))
	r.FieldsPerRecord = -1 // tolerate ragged trailing columns rather than hard-failing the whole file
	rows, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("accounting: parse revolut csv: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("accounting: revolut csv is empty")
	}

	header := rows[0]
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.TrimSpace(h)] = i
	}
	for _, req := range revolutRequiredColumns {
		if _, ok := idx[req]; !ok {
			return nil, fmt.Errorf("accounting: revolut csv missing required column %q", req)
		}
	}

	// get reads a cell by header name, tolerant of a short row (returns "").
	get := func(row []string, name string) string {
		i, ok := idx[name]
		if !ok || i >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[i])
	}

	out := make([]entity.AcctBankTxnInsert, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if len(row) == 0 {
			continue
		}
		// Import only settled lines.
		if !strings.EqualFold(get(row, revColState), "COMPLETED") {
			continue
		}
		amount, ok := parseRevolutDecimal(get(row, revColAmount))
		if !ok {
			continue // an unparseable amount cannot be posted — skip the line
		}
		bookedAt, ok := parseRevolutDate(get(row, revColDateCompleted), get(row, revColDateStarted))
		if !ok {
			continue // no usable date — skip
		}

		id := get(row, revColID)
		currency := strings.ToUpper(get(row, revColPaymentCurrency))
		if id == "" || currency == "" {
			continue
		}

		ins := entity.AcctBankTxnInsert{
			Source: p.Source(),
			// An EXCHANGE emits two rows sharing the same ID (one per leg) — the composite id + ':' +
			// payment currency keeps both under UNIQUE(external_id) (the legs differ by currency).
			ExternalId:   id + ":" + currency,
			BookedAt:     bookedAt,
			Amount:       amount,
			Currency:     currency,
			Description:  truncateRunes(get(row, revColDescription), descMaxLen),
			Counterparty: nullStr(revolutCounterparty(get(row, revColBeneficiaryName), get(row, revColPayer), get(row, revColSenderName))),
			Raw:          revolutRawJSON(header, row),
		}
		if fee, ok := parseRevolutDecimal(get(row, revColFee)); ok {
			ins.Fee = decimal.NullDecimal{Decimal: fee, Valid: true}
		}
		// An EXCHANGE is an INTERNAL transfer between the owner's own currency sub-accounts (not income /
		// expense) — default it to ignored (the operator can still post it if they want a 1010↔1010 leg).
		if strings.EqualFold(get(row, revColType), "EXCHANGE") {
			ins.State = entity.AcctBankTxnIgnored
		} else {
			ins.State = entity.AcctBankTxnUnmatched
		}
		out = append(out, ins)
	}
	return out, nil
}

// revolutCounterparty is the first non-empty of Beneficiary name / Payer / Sender name.
func revolutCounterparty(beneficiary, payer, sender string) string {
	for _, v := range []string{beneficiary, payer, sender} {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// revolutRawJSON serialises the whole row as a header-keyed JSON object (the acct_bank_txn.raw trace).
func revolutRawJSON(header, row []string) string {
	m := make(map[string]string, len(header))
	for i, h := range header {
		v := ""
		if i < len(row) {
			v = row[i]
		}
		m[strings.TrimSpace(h)] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// parseRevolutDecimal parses a signed amount, tolerating a thousands-comma and a stray currency sign.
// An empty / unparseable cell returns ok=false.
func parseRevolutDecimal(s string) (decimal.Decimal, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return decimal.Zero, false
	}
	s = strings.ReplaceAll(s, ",", "")
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero, false
	}
	return d, true
}

// parseRevolutDate parses the completed date, falling back to the started date, across the known layouts.
func parseRevolutDate(completed, started string) (time.Time, bool) {
	for _, s := range []string{completed, started} {
		if strings.TrimSpace(s) == "" {
			continue
		}
		for _, layout := range revolutDateLayouts {
			if t, err := time.ParseInLocation(layout, strings.TrimSpace(s), time.UTC); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

// BuildBankTxnEntry builds the manual-provenance journal entry for a posted bank inbox line (§4.1). The
// SIGNED amount drives Dr/Cr: an inflow (amount > 0) is Dr 1010 Cash-Bank / Cr the chosen account; an
// outflow (amount < 0) reverses it. A non-EUR line (Revolut is multi-currency: PLN/GBP…) is posted with
// amount_src + currency_src and left for the admin handler's costing-FX fold to convert to EUR base — the
// existing phase-1 src mechanic, no FX built here. source_key 'bank:<id>' makes a re-post idempotent.
// CreatedBy is left empty for the handler to stamp with the posting admin.
func BuildBankTxnEntry(txn entity.AcctBankTxn, accountCode string, occurredAt time.Time) (entity.AcctJournalEntryInsert, error) {
	amt := txn.Amount.Abs().Round(2)
	if amt.Sign() <= 0 {
		return entity.AcctJournalEntryInsert{}, ErrDegenerateAmounts
	}

	drCode, crCode := Acc1010, accountCode // inflow: money into 1010, credit the chosen account
	if txn.Amount.Sign() < 0 {
		drCode, crCode = accountCode, Acc1010 // outflow: money out of 1010
	}

	eur := isBaseCurrency(txn.Currency)
	mkLine := func(code string, side entity.AcctSide, a decimal.Decimal) entity.AcctJournalLineInsert {
		l := entity.AcctJournalLineInsert{AccountCode: code, Side: side}
		if eur {
			l.Amount = a
			return l
		}
		// Non-EUR: leave Amount for the FX fold; carry the source amount as the amount_src trace (all
		// legs fold to the same EUR base at one rate, so the entry stays balanced).
		l.AmountSrc = decimal.NullDecimal{Decimal: a, Valid: true}
		l.CurrencySrc = sql.NullString{String: strings.ToUpper(strings.TrimSpace(txn.Currency)), Valid: true}
		return l
	}

	desc := fmt.Sprintf("bank %s %s", txn.Source, txn.ExternalId)
	if txn.Counterparty.Valid && strings.TrimSpace(txn.Counterparty.String) != "" {
		desc = fmt.Sprintf("bank %s — %s", txn.Source, strings.TrimSpace(txn.Counterparty.String))
	}

	lines := []entity.AcctJournalLineInsert{
		mkLine(drCode, entity.AcctSideDebit, amt),
		mkLine(crCode, entity.AcctSideCredit, amt),
	}
	// Bank fee (Revolut Fee column): it reduces the 1010 bank balance and is a Bank-Fees expense, so
	// post Dr 6060 / Cr 1010 for it in the txn currency (MED-3). Without this, 1010 drifts from the real
	// Revolut balance by Σ fees and bank-fee expense is understated. Zero/NULL fee adds no lines.
	if txn.Fee.Valid {
		if fee := txn.Fee.Decimal.Abs().Round(2); fee.Sign() > 0 {
			lines = append(lines,
				mkLine(Acc6060, entity.AcctSideDebit, fee),
				mkLine(Acc1010, entity.AcctSideCredit, fee),
			)
		}
	}

	return entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: truncateRunes(desc, descMaxLen),
		SourceType:  entity.AcctSourceManual,
		SourceKey:   fmt.Sprintf("bank:%d", txn.Id),
		Lines:       lines,
	}, nil
}
