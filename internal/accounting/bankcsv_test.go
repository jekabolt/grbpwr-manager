package accounting

import (
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// revolutHeader is the owner's real 33-column export header (order preserved).
var revolutHeader = []string{
	"Date started (UTC)", "Date completed (UTC)", "ID", "Type", "State", "Description", "Reference",
	"Payer", "Card number", "Card label", "Card state", "Orig currency", "Orig amount",
	"Payment currency", "Amount", "Total amount", "Exchange rate", "Fee", "Fee currency", "Balance",
	"Account", "International account number", "Beneficiary account number",
	"Beneficiary sort code or routing number", "Beneficiary IBAN", "Beneficiary BIC",
	"Beneficiary name", "MCC", "Related transaction id", "Spend program", "Sender account",
	"Sender name", "Card references",
}

// revRow builds one CSV data line from a header→value map, filling absent columns with "".
func revRow(vals map[string]string) string {
	cells := make([]string, len(revolutHeader))
	for i, h := range revolutHeader {
		cells[i] = vals[h]
	}
	return strings.Join(cells, ",")
}

func revolutCSV(rows ...map[string]string) string {
	lines := []string{strings.Join(revolutHeader, ",")}
	for _, r := range rows {
		lines = append(lines, revRow(r))
	}
	return strings.Join(lines, "\n")
}

func TestRevolutParser_Parse(t *testing.T) {
	csv := revolutCSV(
		map[string]string{ // EUR inflow (top-up) — counterparty from Payer
			"ID": "tx1", "Type": "TOPUP", "State": "COMPLETED", "Payment currency": "EUR",
			"Amount": "100.00", "Fee": "0.00", "Payer": "Alice", "Description": "Card top-up",
			"Date completed (UTC)": "2026-07-10 12:00:00",
		},
		map[string]string{ // GBP outflow — counterparty from Beneficiary name, has a fee
			"ID": "tx2", "Type": "TRANSFER", "State": "COMPLETED", "Payment currency": "GBP",
			"Amount": "-50.00", "Fee": "0.20", "Beneficiary name": "Acme Ltd",
			"Description": "Supplier payment", "Date completed (UTC)": "2026-07-11 09:30:00",
		},
		map[string]string{ // EXCHANGE leg PLN — same ID as the GBP leg below
			"ID": "tx3", "Type": "EXCHANGE", "State": "COMPLETED", "Payment currency": "PLN",
			"Amount": "-200.00", "Date completed (UTC)": "2026-07-12 10:00:00",
		},
		map[string]string{ // EXCHANGE leg GBP — same ID, different payment currency
			"ID": "tx3", "Type": "EXCHANGE", "State": "COMPLETED", "Payment currency": "GBP",
			"Amount": "40.00", "Date completed (UTC)": "2026-07-12 10:00:00",
		},
		map[string]string{ // not COMPLETED — filtered out entirely
			"ID": "tx4", "Type": "TRANSFER", "State": "PENDING", "Payment currency": "EUR",
			"Amount": "5.00", "Date completed (UTC)": "2026-07-13 10:00:00",
		},
	)

	got, err := RevolutParser{}.Parse(csv)
	require.NoError(t, err)
	require.Len(t, got, 4, "the PENDING row is filtered; both EXCHANGE legs survive")

	byExt := map[string]entity.AcctBankTxnInsert{}
	for _, tx := range got {
		byExt[tx.ExternalId] = tx
	}

	a := byExt["tx1:EUR"]
	assert.Equal(t, "revolut", a.Source)
	assert.Equal(t, "100.00", a.Amount.StringFixed(2))
	assert.Equal(t, "EUR", a.Currency)
	assert.Equal(t, entity.AcctBankTxnUnmatched, a.State)
	assert.Equal(t, "Alice", a.Counterparty.String)
	require.True(t, a.Fee.Valid)
	assert.Equal(t, "0.00", a.Fee.Decimal.StringFixed(2))
	assert.Equal(t, time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC), a.BookedAt)
	assert.Contains(t, a.Raw, "tx1")

	b := byExt["tx2:GBP"]
	assert.Equal(t, "-50.00", b.Amount.StringFixed(2))
	assert.Equal(t, "GBP", b.Currency)
	assert.Equal(t, "Acme Ltd", b.Counterparty.String) // Beneficiary name wins over empty Payer
	assert.Equal(t, "0.20", b.Fee.Decimal.StringFixed(2))

	// The two EXCHANGE legs share ID tx3 but land under distinct external_ids (dedup-safe) and default ignored.
	pln, okPln := byExt["tx3:PLN"]
	gbp, okGbp := byExt["tx3:GBP"]
	require.True(t, okPln && okGbp, "both EXCHANGE legs present under composite external_id")
	assert.Equal(t, entity.AcctBankTxnIgnored, pln.State)
	assert.Equal(t, entity.AcctBankTxnIgnored, gbp.State)
}

func TestRevolutParser_MissingRequiredColumn(t *testing.T) {
	// Drop the ID column → a wrong-bank / corrupt file is a hard error, not silent.
	bad := "Date completed (UTC),Type,State,Payment currency,Amount\n2026-07-10 12:00:00,TOPUP,COMPLETED,EUR,10.00"
	_, err := RevolutParser{}.Parse(bad)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ID")
}

var testBankDate = time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC)

func bankTxn(id int, amount, currency string) entity.AcctBankTxn {
	return entity.AcctBankTxn{
		Id: id, Source: "revolut", ExternalId: "ext:" + currency,
		BookedAt: testBankDate, Amount: dec(amount), Currency: currency,
	}
}

func TestBuildBankTxnEntry_EURInflow(t *testing.T) {
	e, err := BuildBankTxnEntry(bankTxn(7, "100.00", "EUR"), "4020", testBankDate)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	assert.Equal(t, entity.AcctSourceManual, e.SourceType)
	assert.Equal(t, "bank:7", e.SourceKey)
	// Inflow: money into 1010, credit the chosen account.
	assertAmount(t, e, Acc1010, entity.AcctSideDebit, "100.00")
	assertAmount(t, e, "4020", entity.AcctSideCredit, "100.00")
}

func TestBuildBankTxnEntry_EUROutflow(t *testing.T) {
	e, err := BuildBankTxnEntry(bankTxn(8, "-50.00", "EUR"), "6340", testBankDate)
	require.NoError(t, err)
	require.NoError(t, ValidateBalanced(e))
	// Outflow: debit the chosen account, money out of 1010 (absolute amount).
	assertAmount(t, e, "6340", entity.AcctSideDebit, "50.00")
	assertAmount(t, e, Acc1010, entity.AcctSideCredit, "50.00")
}

func TestBuildBankTxnEntry_NonEURUsesSrcMechanic(t *testing.T) {
	e, err := BuildBankTxnEntry(bankTxn(9, "-200.00", "PLN"), "6340", testBankDate)
	require.NoError(t, err)
	// A non-EUR line leaves Amount for the FX fold and carries amount_src/currency_src on BOTH legs.
	for _, l := range e.Lines {
		require.True(t, l.AmountSrc.Valid, "amount_src set on %s", l.AccountCode)
		assert.Equal(t, "200.00", l.AmountSrc.Decimal.StringFixed(2))
		assert.Equal(t, "PLN", l.CurrencySrc.String)
		assert.True(t, l.Amount.IsZero(), "base amount left zero until folded")
	}
	// Direction is still correct: outflow debits the chosen account, credits 1010.
	assert.True(t, hasLine(e, "6340", entity.AcctSideDebit))
	assert.True(t, hasLine(e, Acc1010, entity.AcctSideCredit))
}

func TestBuildBankTxnEntry_ZeroAmount(t *testing.T) {
	_, err := BuildBankTxnEntry(bankTxn(10, "0.00", "EUR"), "4020", testBankDate)
	assert.ErrorIs(t, err, ErrDegenerateAmounts)
}
