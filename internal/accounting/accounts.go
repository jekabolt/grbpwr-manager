// Package accounting holds the pure double-entry posting rules of the phase-1 general ledger
// (docs/plan-accounting/04-posting-rules.md). Every Build* function is a pure function of
// operational facts (entity.Acct*Facts) to an entity.AcctJournalEntryInsert — no SQL, no store,
// no clock, no network. The worker (internal/acctposting) assembles the facts and persists the
// returned entry through internal/store/accounting; reports and reconciliation read the ledger.
//
// Invariants held by this package (see balance.go): every returned entry balances
// (Σ debit == Σ credit), every line amount is strictly positive (the side carries the sign), and
// the last, balancing line is always a difference — never an independent product — so rounding
// never breaks Dr==Cr. Rounding to 2dp happens only on final line amounts, never on intermediate
// proportions.
package accounting

// Chart-of-accounts codes (migration 0190 seeds these 34 accounts; codes are Excel-compatible and
// stable — the ledger references accounts by code, resolved to account_id in the store). Grouped
// by section: asset (1xxx), liability (2xxx), equity (3xxx), revenue (4xxx), cogs (5xxx + 6210),
// opex (6xxx).
const (
	// Assets (8).
	Acc1010 = "1010" // Cash – Bank
	Acc1030 = "1030" // Payment Processor (Stripe)
	Acc1040 = "1040" // Accounts Receivable
	Acc1110 = "1110" // Materials
	Acc1120 = "1120" // Work in Progress
	Acc1130 = "1130" // Finished Goods
	Acc1140 = "1140" // Inventory in Transit — outbound, shipped-not-delivered (phase 2, wave 2)
	Acc1210 = "1210" // Prepaid Expenses
	Acc1220 = "1220" // Equipment

	// Liabilities (4).
	Acc2010 = "2010" // Accounts Payable
	Acc2030 = "2030" // Accrued Expenses
	Acc2050 = "2050" // Income Tax Payable — Corporation Tax liability (phase 2, wave 3, manual only)
	Acc2070 = "2070" // VAT Payable
	Acc2080 = "2080" // VAT Input (Recoverable) — contra-liability (phase 2, wave 1)
	Acc2090 = "2090" // Customer Prepayments — delivered-recognition liability (phase 2, wave 2)

	// Equity (3).
	Acc3010 = "3010" // Owner's Equity
	Acc3020 = "3020" // Retained Earnings
	Acc3030 = "3030" // Draws / Distributions

	// Revenue (6).
	Acc4010 = "4010" // Sales – Retail / Popup
	Acc4020 = "4020" // Sales – DTC (Website)
	Acc4030 = "4030" // Discounts / Promotions — contra-revenue, debit-normal (phase 2, wave 3)
	Acc4040 = "4040" // Returns & Refunds (contra-revenue, debit-normal)
	Acc4050 = "4050" // Trade Discounts (B2B) — contra-revenue (phase 2, wave 1)
	Acc4110 = "4110" // Shipping Income
	Acc4310 = "4310" // Sales – B2B / Wholesale (phase 2, wave 1)

	// COGS (5) — includes 6210, seeded in the cogs section so it lands beside production costs.
	Acc5010 = "5010" // COGS
	Acc5040 = "5040" // Inventory Write-offs
	Acc5050 = "5050" // Returns to Inventory (contra-COGS)
	Acc5090 = "5090" // Stock Adjustments
	Acc6210 = "6210" // Samples & Prototyping

	// OPEX (11).
	Acc6010 = "6010" // Transportation & Office Logistics
	Acc6050 = "6050" // Merchant Processing Fees
	Acc6060 = "6060" // Bank Fees
	Acc6110 = "6110" // Advertising & Marketing
	Acc6125 = "6125" // Production Content
	Acc6320 = "6320" // Software & Subscriptions
	Acc6330 = "6330" // Salaries
	Acc6340 = "6340" // Rent
	Acc6350 = "6350" // Professional Services
	Acc6360 = "6360" // Taxes
	Acc6390 = "6390" // Other Operating Expenses

	// Shipping & Fulfillment (opex) — actual carrier cost pull (phase 2, wave 3).
	Acc6030 = "6030" // Shipping & Fulfillment

	// Employer-side social contributions (ZUS/NI) — statutory review 13, seeded by 0204.
	Acc6335 = "6335" // Employer Social Contributions

	// Tax (its own P&L section) — manual Corporation-Tax journal only (phase 2, wave 3).
	Acc8010 = "8010" // Corporation Tax
)

// opexCategoryAccounts maps every OPEX category (entity.ValidOpexCategories) to its P&L account
// (docs/plan-accounting/01-db-schema.md, 04-posting-rules.md O1). The mapping lives in Go, not the
// DB, so it evolves with the dto-validated category set without a migration. Keep this in lockstep
// with entity.ValidOpexCategories — accounts_test.go asserts every valid category is mapped.
var opexCategoryAccounts = map[string]string{
	"salaries":              Acc6330,
	"rent":                  Acc6340,
	"software":              Acc6320,
	"marketing_other":       Acc6110,
	"production_content":    Acc6125,
	"taxes":                 Acc6360,
	"bank_fees":             Acc6060,
	"professional_services": Acc6350,
	"logistics_office":      Acc6010,
	"employer_social":       Acc6335,
	"other":                 Acc6390,
}

// OpexCategoryAccount maps an OPEX category to its P&L account code. The second return is false
// when the category is unknown to the mapping (dto validation widened but the mapping was not) —
// the caller then books it to 6390 Other Operating Expenses and raises a caveat: a fail-open with
// visibility rather than a silently dropped line (04/O1).
func OpexCategoryAccount(category string) (string, bool) {
	if code, ok := opexCategoryAccounts[category]; ok {
		return code, true
	}
	return Acc6390, false
}
