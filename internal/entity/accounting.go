package entity

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// Sentinel errors for the accounting module. apisrv maps these to codes.FailedPrecondition /
// InvalidArgument; they live in entity (like the other domain sentinels here, e.g. the material
// errors) so the API layer can errors.Is against them without importing the store package.
var (
	// ErrAcctUnbalanced is returned when an entry has fewer than two lines, a non-positive amount, or
	// Σdebit != Σcredit.
	ErrAcctUnbalanced = errors.New("accounting: journal entry is unbalanced")
	// ErrAcctPeriodClosed is returned when posting into a closed accounting period.
	ErrAcctPeriodClosed = errors.New("accounting: period is closed")
	// ErrAcctPeriodNotReady is returned by ClosePeriod when the month cannot be closed yet (pending
	// events, unposted pull sources, or an unbalanced month); the reason is in the error text.
	ErrAcctPeriodNotReady = errors.New("accounting: period not ready to close")
	// ErrAcctUnknownAccount is returned when a referenced account code does not exist.
	ErrAcctUnknownAccount = errors.New("accounting: unknown account code")
	// ErrAcctArchivedAccount is returned when a journal line references an archived account.
	ErrAcctArchivedAccount = errors.New("accounting: account is archived")
	// ErrAcctSystemAccount is returned when renaming/archiving a system (is_system) account.
	ErrAcctSystemAccount = errors.New("accounting: system account cannot be modified")
	// ErrAcctAlreadyReversed is returned when reversing an entry that was already reversed.
	ErrAcctAlreadyReversed = errors.New("accounting: entry already reversed")
	// ErrAcctCannotReverseReversal is returned when reversing a reversal entry (fix with a new entry).
	ErrAcctCannotReverseReversal = errors.New("accounting: cannot reverse a reversal entry")
)

// Accounting core (double-entry ledger), phase 1. The ledger is a DERIVED, append-only
// projection of existing operational facts (orders, material movements, production runs, opex)
// plus manual entries. Base currency is EUR — see the CLAUDE.md "two EUR figures" rule; the
// ledger reads total_settled_base, never total_price_eur. Full design in docs/plan-accounting/;
// docs/plan-accounting/01-db-schema.md is the source of truth for the shapes below (migrations
// 0189_accounting_core.sql / 0190_accounting_seed_coa.sql).

// AcctSection is the chart-of-accounts section (acct_account.section): it drives placement in
// reports (BS vs PL) and the sign of an account's normal balance — asset/cogs/opex are
// debit-normal, liability/equity/revenue are credit-normal.
type AcctSection string

// AcctSide is which side of a journal line an amount sits on (acct_journal_line.side). Amounts are
// always stored positive (DECIMAL > 0, chk_acct_line_amount) — the side alone carries the sign.
type AcctSide string

// AcctSourceType identifies what produced a journal entry (acct_journal_entry.source_type): an
// automated posting rule keyed to an operational event/fact (order_sale, order_refund,
// material_*, production_receive, opex_month), a manual entry, or a reversal of another entry.
type AcctSourceType string

// AcctEventType is the kind of outbox event queued in acct_event by the order flows (push
// producers — see docs/plan-accounting/03-event-capture.md). Pull sources (material movements,
// production runs, opex) are NOT events: they are scanned by checkpoint (acct_checkpoint), never
// queued here.
type AcctEventType string

const (
	AcctSectionAsset     AcctSection = "asset"
	AcctSectionLiability AcctSection = "liability"
	AcctSectionEquity    AcctSection = "equity"
	AcctSectionRevenue   AcctSection = "revenue"
	AcctSectionCogs      AcctSection = "cogs"
	AcctSectionOpex      AcctSection = "opex"
	// AcctSectionTax is Corporation Tax's own P&L section (phase 2, wave 3 — migration 0196). It is
	// debit-normal like cogs/opex (a tax charge is a debit to 8010) and drives the P&L's
	// Net-Profit-after-tax line (OperatingProfit − Σ tax).
	AcctSectionTax AcctSection = "tax"

	AcctSideDebit  AcctSide = "debit"
	AcctSideCredit AcctSide = "credit"

	AcctSourceOrderSale   AcctSourceType = "order_sale"
	AcctSourceOrderRefund AcctSourceType = "order_refund"
	// Delivered-recognition chain (phase 2, wave 2 — migration 0195). order_prepayment posts at
	// payment (money in, revenue parked on 2090); order_transit at shipped (1130→1140); and
	// order_delivered_sale at delivered (2090→revenue, 1140→COGS). See docs/plan-accounting-phase2/
	// 02-wave2-delivered.md.
	AcctSourceOrderPrepayment    AcctSourceType = "order_prepayment"
	AcctSourceOrderTransit       AcctSourceType = "order_transit"
	AcctSourceOrderDeliveredSale AcctSourceType = "order_delivered_sale"
	AcctSourceMaterialReceipt    AcctSourceType = "material_receipt"
	AcctSourceMaterialIssue      AcctSourceType = "material_issue"
	AcctSourceMaterialReturn     AcctSourceType = "material_return"
	AcctSourceMaterialWriteoff   AcctSourceType = "material_writeoff"
	AcctSourceMaterialAdjustment AcctSourceType = "material_adjustment"
	AcctSourceProductionReceive  AcctSourceType = "production_receive"
	AcctSourceOpexMonth          AcctSourceType = "opex_month"
	// Wave-3 pull sources (phase 2, wave 3 — migration 0196). shipping_actual posts the actual carrier
	// cost from shipment.actual_cost / return_shipping_cost (Dr 6030 / Cr 2030); dev_expense posts a
	// tech_card_dev_expense R&D charge (Dr 6210 / Cr 2030). Both are mutable pull sources: a change
	// reposts (reverse + a versioned source_key), like opex_month.
	AcctSourceShippingActual AcctSourceType = "shipping_actual"
	AcctSourceDevExpense     AcctSourceType = "dev_expense"
	// AcctSourceOrderDispute is a Stripe chargeback (phase 2, wave 4 — migration 0198). A created
	// dispute posts Dr 4040 (disputed amount) + Dr 6050 (dispute fee) / Cr 1030 (money pulled from
	// Stripe); a closed-won dispute is reversed. COGS is untouched (the goods were not returned). See
	// docs/plan-accounting-phase2/04-wave4-money.md §4.3.
	AcctSourceOrderDispute AcctSourceType = "order_dispute"
	AcctSourceManual       AcctSourceType = "manual"
	AcctSourceReversal     AcctSourceType = "reversal"
	// AcctSourceDepreciation is a monthly straight-line depreciation charge on a fixed asset
	// (Dr 6370 / Cr 1225); source_key "asset:<id>:<YYYY-MM>" gives one-per-asset-per-month idempotency.
	AcctSourceDepreciation AcctSourceType = "depreciation"
	// AcctSourceCorpTax is a corporation-tax accrual for a period (Dr 8010 / Cr 2050); source_key
	// "corp_tax:<from>:<to>" so re-accruing the same period is idempotent.
	AcctSourceCorpTax AcctSourceType = "corp_tax"

	AcctEventOrderPaid      AcctEventType = "order_paid"
	AcctEventOrderRefund    AcctEventType = "order_refund"
	AcctEventOrderShipped   AcctEventType = "order_shipped"   // phase 2, wave 2 — triggers order_transit
	AcctEventOrderDelivered AcctEventType = "order_delivered" // phase 2, wave 2 — triggers order_delivered_sale
	AcctEventOrderDispute   AcctEventType = "order_dispute"   // phase 2, wave 4 — Stripe chargeback created/closed
)

// VatRegime classifies an order's VAT treatment (customer_order.vat_regime), resolved at posting from
// the ship-from / ship-to countries and whether the buyer is B2B (see internal/accounting/vatregime.go
// and docs/plan-accounting-phase2/01-wave1-vat.md §1.1). It drives which VAT rate (if any) the sale
// posts and which revenue account (B2C vs B2B/wholesale) it credits. Stored as a plain string; the DB
// CHECK (chk_customer_order_vat_regime, migration 0191) mirrors ValidVatRegimes.
type VatRegime string

const (
	// VatRegimeOSS: EU B2C to a country other than PL — destination-rate VAT under the OSS scheme.
	VatRegimeOSS VatRegime = "oss"
	// VatRegimePLDomestic: domestic PL sale — Polish standard rate (23%).
	VatRegimePLDomestic VatRegime = "pl_domestic"
	// VatRegimeExport: non-EU B2C (incl. UK shipped from PL) or non-EU B2B — 0% VAT, no VAT line.
	VatRegimeExport VatRegime = "export"
	// VatRegimeWDT: EU B2B with a VAT id — intra-community supply, 0% reverse charge, no VAT line.
	VatRegimeWDT VatRegime = "wdt"
	// VatRegimeUKStockDomestic: cash / UK-stock popup sale — UK domestic rate (20%).
	VatRegimeUKStockDomestic VatRegime = "uk_stock_domestic"
	// VatRegimeNone: no regime resolved (fail-safe placeholder; never posts VAT).
	VatRegimeNone VatRegime = "none"
)

// InputVatRegime classifies the input (purchase-side) VAT of a material receipt
// (material_stock_movement.input_vat_regime), driving the extended M1 posting rule
// (docs/plan-accounting-phase2/01-wave1-vat.md §1.4). Stored as a plain string; the DB CHECK
// (chk_material_input_vat_regime, migration 0192) mirrors ValidInputVatRegimes.
type InputVatRegime string

const (
	// InputVatRegimeWNT: intra-community acquisition (Art.33a) — net-zero self-charge Dr 2080 / Cr 2070.
	InputVatRegimeWNT InputVatRegime = "wnt"
	// InputVatRegimeImport: import (Art.33a) — net-zero self-charge Dr 2080 / Cr 2070.
	InputVatRegimeImport InputVatRegime = "import"
	// InputVatRegimeDomesticPL: domestic PL purchase — Dr 1110 NET + Dr 2080 VAT / Cr 2010 GROSS.
	InputVatRegimeDomesticPL InputVatRegime = "domestic_pl"
	// InputVatRegimeDomesticUK: domestic UK purchase — Dr 1110 NET + Dr 2080 VAT / Cr 2010 GROSS.
	InputVatRegimeDomesticUK InputVatRegime = "domestic_uk"
)

// ValidVatRegimes is the storable set for customer_order.vat_regime — mirrored by the DB CHECK
// (chk_customer_order_vat_regime, migration 0191) and asserted by migrationlint's enum-drift test.
var ValidVatRegimes = map[VatRegime]bool{
	VatRegimeOSS:             true,
	VatRegimePLDomestic:      true,
	VatRegimeExport:          true,
	VatRegimeWDT:             true,
	VatRegimeUKStockDomestic: true,
	VatRegimeNone:            true,
}

// ValidInputVatRegimes is the storable set for material_stock_movement.input_vat_regime — mirrored by
// the DB CHECK (chk_material_input_vat_regime, migration 0192) and asserted by the enum-drift test.
var ValidInputVatRegimes = map[InputVatRegime]bool{
	InputVatRegimeWNT:        true,
	InputVatRegimeImport:     true,
	InputVatRegimeDomesticPL: true,
	InputVatRegimeDomesticUK: true,
}

// acct_account.statement is a plain string in the schema (not a distinct Go type — mirrors the
// entity skeleton in docs/plan-accounting/01-db-schema.md): 'BS' (Balance Sheet) or 'PL' (Profit &
// Loss).
const (
	AcctStatementBS = "BS"
	AcctStatementPL = "PL"
)

// acct_period.status is a plain string in the schema (mirrors the entity skeleton): a month is
// 'open' for posting or 'closed' (see ClosePeriod/ReopenPeriod, docs/plan-accounting/02-store-layer.md).
const (
	AcctPeriodStatusOpen   = "open"
	AcctPeriodStatusClosed = "closed"
)

// ValidAcctSourceTypes is the storable set for acct_journal_entry.source_type — the single source
// of truth mirrored by the DB CHECK (chk_acct_entry_source_type, migrations 0189 + 0195) and validated
// in dto. Map-to-bool (not struct{}), matching entity.ValidMaterialClasses: migrationlint's
// enum_drift_test.go compares it via mapKeysAsStrings[K ~string](m map[K]bool).
var ValidAcctSourceTypes = map[AcctSourceType]bool{
	AcctSourceOrderSale:          true,
	AcctSourceOrderRefund:        true,
	AcctSourceOrderPrepayment:    true,
	AcctSourceOrderTransit:       true,
	AcctSourceOrderDeliveredSale: true,
	AcctSourceMaterialReceipt:    true,
	AcctSourceMaterialIssue:      true,
	AcctSourceMaterialReturn:     true,
	AcctSourceMaterialWriteoff:   true,
	AcctSourceMaterialAdjustment: true,
	AcctSourceProductionReceive:  true,
	AcctSourceOpexMonth:          true,
	AcctSourceShippingActual:     true,
	AcctSourceDevExpense:         true,
	AcctSourceDepreciation:       true,
	AcctSourceCorpTax:            true,
	AcctSourceOrderDispute:       true,
	AcctSourceManual:             true,
	AcctSourceReversal:           true,
}

// ValidAcctSections is the storable set for acct_account.section — mirrored by the DB CHECK
// (chk_acct_account_section, migrations 0189 + 0196) and asserted by migrationlint's enum-drift test.
var ValidAcctSections = map[AcctSection]bool{
	AcctSectionAsset:     true,
	AcctSectionLiability: true,
	AcctSectionEquity:    true,
	AcctSectionRevenue:   true,
	AcctSectionCogs:      true,
	AcctSectionOpex:      true,
	AcctSectionTax:       true,
}

// ValidAcctSides is the storable set for acct_journal_line.side — mirrors the DB CHECK
// (chk_acct_line_side, migration 0189).
var ValidAcctSides = map[AcctSide]bool{
	AcctSideDebit:  true,
	AcctSideCredit: true,
}

// ValidAcctEventTypes is the storable set for acct_event.event_type — mirrors the DB CHECK
// (chk_acct_event_type, migrations 0189 + 0195).
var ValidAcctEventTypes = map[AcctEventType]bool{
	AcctEventOrderPaid:      true,
	AcctEventOrderRefund:    true,
	AcctEventOrderShipped:   true,
	AcctEventOrderDelivered: true,
	AcctEventOrderDispute:   true,
}

// AcctAccount is a stored chart-of-accounts row (migration 0190 seeds 34 of these). IsSystem marks
// accounts that participate in automated posting rules — those cannot be archived or renamed by
// code (see docs/plan-accounting/02-store-layer.md: SetAccountArchived / ErrAcctSystemAccount).
type AcctAccount struct {
	Id        int         `db:"id"`
	Code      string      `db:"code"`
	Name      string      `db:"name"`
	Section   AcctSection `db:"section"`
	Statement string      `db:"statement"`
	IsSystem  bool        `db:"is_system"`
	Archived  bool        `db:"archived"`
	CreatedAt time.Time   `db:"created_at"`
	UpdatedAt time.Time   `db:"updated_at"`
}

// AcctAccountInsert is the writable payload of a custom (non-system, admin-created) account.
// IsSystem/Archived are not settable here: only the seed migration (0190) creates is_system
// accounts, and archiving is a separate call (SetAccountArchived).
type AcctAccountInsert struct {
	Code      string      `db:"code"`
	Name      string      `db:"name"`
	Section   AcctSection `db:"section"`
	Statement string      `db:"statement"`
}

// AcctJournalLineInsert is one side of a journal entry being created. AccountCode is resolved to
// account_id by the store (CreateJournalEntry); Amount is base currency (EUR), always > 0 — Side
// carries the sign. AmountSrc/CurrencySrc are an optional original-currency trace for manual
// entries (e.g. a GBP rent invoice booked as EUR base + 250.00 GBP src); NULL for automated
// postings.
type AcctJournalLineInsert struct {
	AccountCode string
	Side        AcctSide
	Amount      decimal.Decimal
	AmountSrc   decimal.NullDecimal
	CurrencySrc sql.NullString
	Note        sql.NullString
}

// AcctJournalEntryInsert is the input to CreateJournalEntry — the single write path for both
// automated postings and manual entries (docs/plan-accounting/02-store-layer.md). SourceType +
// SourceKey are the idempotency key (UNIQUE(source_type, source_key) on acct_journal_entry): a
// retried automated posting is a no-op, not a duplicate.
type AcctJournalEntryInsert struct {
	OccurredAt  time.Time
	Description string
	SourceType  AcctSourceType
	SourceKey   string
	CreatedBy   string
	HasCaveat   bool
	Caveat      sql.NullString
	// SupplierID optionally tags the entry with the supplier it concerns (phase 2, wave 4 — AP by
	// supplier): a material_receipt (M1) entry inherits its movement's supplier, and a manual AP payment
	// (Dr 2010) is tagged so GetPayables can net the open balance per supplier. NULL for every other entry.
	SupplierID sql.NullInt64
	Lines      []AcctJournalLineInsert
}

// AcctJournalEntry is a stored journal-entry header (acct_journal_line rows are fetched
// separately — see AcctJournalEntryFull). ReversalOf/ReversedBy implement reversal-not-edit: a
// mistaken entry is never updated or deleted, only mirrored by a new entry that references it.
type AcctJournalEntry struct {
	Id          int            `db:"id"`
	OccurredAt  time.Time      `db:"occurred_at"`
	Description string         `db:"description"`
	SourceType  AcctSourceType `db:"source_type"`
	SourceKey   string         `db:"source_key"`
	ReversalOf  sql.NullInt32  `db:"reversal_of"`
	ReversedBy  sql.NullInt32  `db:"reversed_by"`
	CreatedBy   string         `db:"created_by"`
	HasCaveat   bool           `db:"has_caveat"`
	Caveat      sql.NullString `db:"caveat"`
	SupplierID  sql.NullInt64  `db:"supplier_id"` // AP-by-supplier tag (phase 2, wave 4); NULL when untagged
	CreatedAt   time.Time      `db:"created_at"`
}

// AcctJournalLine is a stored journal line. AccountCode/AccountName are populated by queries that
// JOIN acct_account (ledger drill-down, entry detail) and are zero-valued on plain
// acct_journal_line scans — the same denormalised-JOIN-field convention as Order.BuyerEmail
// (internal/entity/order.go).
type AcctJournalLine struct {
	Id          int                 `db:"id"`
	EntryId     int                 `db:"entry_id"`
	AccountId   int                 `db:"account_id"`
	Side        AcctSide            `db:"side"`
	Amount      decimal.Decimal     `db:"amount"`
	AmountSrc   decimal.NullDecimal `db:"amount_src"`
	CurrencySrc sql.NullString      `db:"currency_src"`
	Note        sql.NullString      `db:"note"`
	AccountCode string              `db:"account_code"`
	AccountName string              `db:"account_name"`
}

// AcctJournalEntryFull is an entry with its lines — the shape returned by GetJournalEntry.
type AcctJournalEntryFull struct {
	Entry AcctJournalEntry
	Lines []AcctJournalLine
}

// AcctPeriod is one accounting month (Period is always normalised to the 1st). Status gates
// CreateJournalEntry: posting into a closed period fails (ErrAcctPeriodClosed).
type AcctPeriod struct {
	Period   time.Time      `db:"period"`
	Status   string         `db:"status"`
	ClosedAt sql.NullTime   `db:"closed_at"`
	ClosedBy sql.NullString `db:"closed_by"`
}

// AcctEvent is a stored outbox row (push producer). ProcessedAt NULL means pending. Attempts /
// NextRetryAt / LastError implement explicit backoff (see docs/plan-accounting/07-worker-config.md).
type AcctEvent struct {
	Id          int64           `db:"id"`
	EventType   AcctEventType   `db:"event_type"`
	SourceKey   string          `db:"source_key"`
	Payload     json.RawMessage `db:"payload"`
	OccurredAt  time.Time       `db:"occurred_at"`
	CreatedAt   time.Time       `db:"created_at"`
	ProcessedAt sql.NullTime    `db:"processed_at"`
	Attempts    int             `db:"attempts"`
	NextRetryAt sql.NullTime    `db:"next_retry_at"`
	LastError   sql.NullString  `db:"last_error"`
	// NeedsReview: terminally disposed (processed) but could not post automatically and needs an
	// operator — a manual entry, an orphan refund, or a dead-letter (H-1/H-2/B-5). ClosePeriod blocks
	// the month until it is cleared by ReprocessAcctEvent (retry) or ResolveAcctEvent (handled).
	NeedsReview bool `db:"needs_review"`
}

// AcctEventInsert is the input to EnqueueEvent. Payload is a typed struct (AcctOrderPaidPayload /
// AcctOrderRefundPayload) — EnqueueEvent marshals it to JSON itself, so hot-path producers
// (OrderPaymentDone, RefundOrder, CreateCustomOrder) never touch json.RawMessage directly.
type AcctEventInsert struct {
	EventType  AcctEventType
	SourceKey  string
	Payload    any
	OccurredAt time.Time
}

// AcctCheckpoint is the cursor for one pull-source scan (material_movement, production run
// receives, opex_line). A missing row is NOT an error — GetCheckpoint returns the zero value,
// which the worker treats as "first run" (last_id=0 / last_ts=accounting.start_date).
type AcctCheckpoint struct {
	Source    string        `db:"source"`
	LastId    sql.NullInt64 `db:"last_id"`
	LastTs    sql.NullTime  `db:"last_ts"`
	UpdatedAt time.Time     `db:"updated_at"`
}

// AcctOrderPaidPayload is the outbox payload for an order_paid event. Deliberately thin (just the
// order UUID): settled base / fee / VAT are read from customer_order at posting time, since
// total_settled_base can arrive asynchronously after OrderPaymentDone (topUpSettledBase) —
// freezing a stale NULL into the payload would be wrong.
type AcctOrderPaidPayload struct {
	OrderUUID string `json:"order_uuid"`
}

// AcctOrderRefundPayload is the outbox payload for an order_refund event. Unlike order_paid, this
// DOES carry the amount: one specific refund's size cannot be recovered later from the aggregate
// customer_order.refunded_amount column once further refunds have accumulated on top of it.
type AcctOrderRefundPayload struct {
	OrderUUID      string          `json:"order_uuid"`
	RefundAmount   decimal.Decimal `json:"refund_amount"` // order currency, THIS refund only, shipping included
	OrderCurrency  string          `json:"order_currency"`
	RefundedByItem map[int]int64   `json:"refunded_by_item"` // order_item.id -> refunded qty (for COGS)
}

// AcctOrderShippedPayload / AcctOrderDeliveredPayload are the outbox payloads for the wave-2
// delivered-recognition events. Thin (just the order UUID), like AcctOrderPaidPayload: the worker
// re-reads order facts (cost snapshots, settled base, vat_regime) from customer_order at posting time
// rather than freezing them into the payload.
type AcctOrderShippedPayload struct {
	OrderUUID string `json:"order_uuid"`
}

type AcctOrderDeliveredPayload struct {
	OrderUUID string `json:"order_uuid"`
}

// AcctOrderDisputePayload is the outbox payload for an order_dispute event (phase 2, wave 4 — Stripe
// chargebacks, §4.3). Unlike the thin order payloads it carries the money figures: the disputed amount
// and the dispute fee come from Stripe (stripe.Dispute.BalanceTransactions), not customer_order, so the
// worker cannot re-read them. Both figures are the account's settlement currency (EUR) — the balance
// transactions are booked in the Stripe balance currency. Closed=false is the created event (open the
// dispute); Closed=true is the closed event, Won distinguishing a reversal (won → the entry is reversed)
// from a loss (lost → the money stays gone, the open entry stands).
type AcctOrderDisputePayload struct {
	OrderUUID  string          `json:"order_uuid"`
	DisputeID  string          `json:"dispute_id"`
	Closed     bool            `json:"closed"`      // false = created/opened, true = closed
	Won        bool            `json:"won"`         // closed && resolved in the merchant's favour
	AmountBase decimal.Decimal `json:"amount_base"` // EUR disputed amount (Σ|balance_txn.amount|); zero if unknown
	FeeBase    decimal.Decimal `json:"fee_base"`    // EUR dispute fee (Σ balance_txn.fee); zero if unknown
	FeeKnown   bool            `json:"fee_known"`   // balance transactions were present (fee is authoritative even at 0)
	Currency   string          `json:"currency"`    // dispute presentment currency (description only)
}

// AcctOrderPostingState is the ledger's current posting state for one order, read by the worker before
// it posts a delivered-chain entry (phase 2, wave 2). The booleans report which of the order's entries
// already exist (matched across the order's entries incl. refund "uuid:seq" keys). DeliveredEvent is
// true when an order_delivered event has been enqueued — the order is operationally delivered even if
// its order_delivered_sale entry has not posted yet (the refund defer-guard, synthesis D8).
// Remaining2090 / Remaining1140 are the order's outstanding balances on those accounts, signed to their
// normal side (2090 credit−debit, 1140 debit−credit): the EXACT amounts BuildOrderDeliveredSaleEntry
// drains, so a vat-rate edit or a partial pre-delivery refund cannot leave a residual (synthesis D1).
type AcctOrderPostingState struct {
	LegacySale     bool
	Prepayment     bool
	Transit        bool
	DeliveredSale  bool
	DeliveredEvent bool
	Remaining2090  decimal.Decimal
	Remaining1140  decimal.Decimal
}

// AcctEntryFilter narrows ListJournalEntries (docs/plan-accounting/02-store-layer.md,
// 05-admin-api.md). From/To bound occurred_at as a half-open interval [From, To) — To exclusive,
// mirroring the rest of the metrics/reporting SQL in this repo. AccountCode/SourceType are
// optional ("" = any); AccountCode filters via a join through acct_journal_line.
type AcctEntryFilter struct {
	From        time.Time
	To          time.Time
	AccountCode string
	SourceType  AcctSourceType
	Limit       int
	Offset      int
}

// AcctLedgerFilter narrows GetAccountLedger's drill-down rows for one account (the account itself
// is a separate `code` parameter, not part of the filter). From/To bound occurred_at as a
// half-open interval [From, To); Limit/Offset paginate.
type AcctLedgerFilter struct {
	From   time.Time
	To     time.Time
	Limit  int
	Offset int
}

// =====================================================================================
// Posting facts — flat, SQL-shaped structs the accounting store reads from OTHER domains'
// tables (customer_order, material_stock_movement, production_run*, opex_line) and hands to
// the pure posting-rule builders in internal/accounting. The store reads them directly (the
// same cross-domain-read precedent as internal/store/metrics/*); SQL sources are in
// docs/plan-accounting/09-implementation-notes.md §9.2 and 03/04. Base currency is EUR.
// =====================================================================================

// AcctOrderItemFact is one order line's COGS input for posting (09.2). UnitCost is
// COALESCE(order_item.cost_price_at_sale, product.cost_price) — the same snapshot-first fallback as
// metrics/margin.go. NULL when neither is set (a pre-0093 line with no live cost): the builder
// treats it as uncosted (excluded from COGS, flagged in the entry caveat).
type AcctOrderItemFact struct {
	Id        int                 `db:"id"`
	ProductId int                 `db:"product_id"`
	Quantity  decimal.Decimal     `db:"quantity"`
	UnitCost  decimal.NullDecimal `db:"unit_cost"`
}

// AcctOrderFacts is the flat fact set for posting an order sale (S1) or refund (S2), assembled by
// GetOrderFactsForPosting from customer_order JOIN payment LEFT JOIN shipment (header) plus the
// COGS lines (Items). Revenue is taken from TotalSettledBase (never total_price_eur — CLAUDE.md
// "two EUR figures"); ShipmentCost/FreeShipping are NULL when the order has no shipment row.
// PaymentMethodName/FeePct/FeeFixed are not read from SQL (db:"-"): GetOrderFactsForPosting fills
// them in from the payment-method cache (cache.GetPaymentMethodById), keyed by PaymentMethodId,
// after the header query.
type AcctOrderFacts struct {
	Id                int                 `db:"id"`
	UUID              string              `db:"uuid"`
	Placed            time.Time           `db:"placed"`
	TotalPrice        decimal.Decimal     `db:"total_price"`
	Currency          string              `db:"currency"`
	TotalSettledBase  decimal.NullDecimal `db:"total_settled_base"`
	PaymentFee        decimal.NullDecimal `db:"payment_fee"`
	VatAmount         decimal.NullDecimal `db:"vat_amount"`
	VatRatePct        decimal.NullDecimal `db:"vat_rate_pct"`
	PaymentMethodId   int                 `db:"payment_method_id"`
	PaymentMethodName PaymentMethodName   `db:"-"`
	FeePct            decimal.Decimal     `db:"-"`
	FeeFixed          decimal.Decimal     `db:"-"`
	ShipmentCost      decimal.NullDecimal `db:"shipment_cost"`
	FreeShipping      sql.NullBool        `db:"free_shipping"`
	// DestCountry is the ship-to country for VAT resolution: shipping address.country_code with a
	// fallback to .country (07 §7.4.1). Empty / non-2-letter → export regime + caveat. Filled by the
	// buyer→address JOIN in GetOrderFactsForPosting (phase 2, wave 1).
	DestCountry string `db:"dest_country"`
	// BuyerVatID is the B2B customer's VAT identifier (custom orders only); its presence makes the
	// order B2B (wdt / 4310 wholesale revenue). NULL for storefront orders.
	BuyerVatID sql.NullString `db:"buyer_vat_id"`
	// VatRegime is the previously-snapshotted regime (customer_order.vat_regime), read back for
	// reconciliation / debugging; the worker recomputes it each tick and does not trust this value.
	VatRegime sql.NullString `db:"vat_regime"`
	// PromoDiscountPct is the applied promo percentage snapshot (customer_order.promo_discount_pct,
	// migration 0123; DECIMAL(5,2), NULL when no promo). A DISCOUNT percentage on the goods subtotal —
	// free-shipping-only promos leave it NULL / 0 and are NOT a discount. The sale builders use it to
	// split gross revenue into a full-price credit + a 4030 Discounts contra debit (phase 2, wave 3),
	// analytics only: the P&L total is unchanged and the split is applied only when it reconstructs
	// cleanly (07 §7.4.11).
	PromoDiscountPct decimal.NullDecimal `db:"promo_discount_pct"`
	Items            []AcctOrderItemFact `db:"-"`
}

// AcctShipmentCostFacts is one shipment's actual carrier cost, the fact set for the wave-3 shipping_actual
// pull (3.1). ActualCost / ReturnShippingCost are shipment.actual_cost / return_shipping_cost (base EUR,
// NULL = none, mutable — entered manually with delay). ShippingDate is the posting instant (occurred_at),
// with UpdatedAt as a fallback when it is NULL. OrderUUID is denormalised for the entry description.
type AcctShipmentCostFacts struct {
	ShipmentID         int                 `db:"shipment_id"`
	OrderUUID          string              `db:"order_uuid"`
	ActualCost         decimal.NullDecimal `db:"actual_cost"`
	ReturnShippingCost decimal.NullDecimal `db:"return_shipping_cost"`
	ShippingDate       sql.NullTime        `db:"shipping_date"`
	UpdatedAt          time.Time           `db:"updated_at"`
}

// AcctDevExpenseFacts is one tech_card_dev_expense row, the fact set for the wave-3 dev_expense pull
// (3.2). AmountBase is the base-EUR amount (tech_card_dev_expense.amount_base; NULL = uncosted → skipped
// with a caveat, the phase-1 standard). IncurredAt is the posting instant (occurred_at) with CreatedAt as
// a fallback when it is NULL. TechCardName / Kind / Description are denormalised for the entry description.
type AcctDevExpenseFacts struct {
	Id           int                 `db:"id"`
	TechCardID   int                 `db:"tech_card_id"`
	TechCardName string              `db:"tech_card_name"`
	Kind         string              `db:"kind"`
	Description  sql.NullString      `db:"description"`
	AmountBase   decimal.NullDecimal `db:"amount_base"`
	IncurredAt   sql.NullTime        `db:"incurred_at"`
	CreatedAt    time.Time           `db:"created_at"`
}

// AcctMovementFacts is one material_stock_movement joined with its material name — the fact set for
// the M1–M8 material-movement rules (03 §3.3, 04). UnitCostBase NULL where money is expected means
// the movement is uncosted: no entry is posted and it surfaces in the reconciliation report.
type AcctMovementFacts struct {
	MaterialMovement
	MaterialName string `db:"material_name"`
}

// AcctRunIssueFact is one material issue/return movement of a production run (03/04, P1). It carries
// CreatedAt so the LEDGER_WIP figure can be computed with the pre-cutover exclusion
// (created_at >= accounting.start_date) by the caller that knows the cutover date (the worker) —
// GetRunFactsForPosting itself has no start-date argument. UnitCostBase NULL = uncosted issue.
type AcctRunIssueFact struct {
	MovementType MaterialMovementType `db:"movement_type"`
	Quantity     decimal.Decimal      `db:"quantity"`
	UnitCostBase decimal.NullDecimal  `db:"unit_cost_base"`
	CreatedAt    time.Time            `db:"created_at"`
}

// AcctRunFacts is the fact set for posting a production receive (P1, 04): the run's manual cost
// articles (production_run_cost; amount_base NULL → uncosted, flagged) and its material
// issue/return movements (Issues). LEDGER_WIP (Σ costed issue_production − return_production, with
// the cutover filter) is derived from Issues by the caller, not precomputed here, because the
// pre-cutover exclusion needs accounting.start_date which this store method is not given.
// TechCardName is joined in by GetRunFactsForPosting (production_run JOIN tech_card) for the
// journal entry's human-readable description.
type AcctRunFacts struct {
	RunID        int
	ReceivedAt   time.Time
	TechCardName string
	Costs        []ProductionRunCost
	Issues       []AcctRunIssueFact
}

// AcctOpexCategorySum is one OPEX category's costed total for a month (O1, 04): Σ opex_line.amount_base
// of that category, NULL-base (unconverted) lines excluded. Category is one of
// entity.ValidOpexCategories; the builder maps it to a 6xxx account. UncostedLabels is not read from
// SQL (db:"-"): GetOpexMonthFacts fills it in from a second query over the month's NULL-amount_base
// rows so the posting caveat can name what was skipped, including categories that are entirely
// uncosted (a zero-amount entry with only UncostedLabels set).
type AcctOpexCategorySum struct {
	Category       string          `db:"category"`
	AmountBase     decimal.Decimal `db:"amount_base"`
	UncostedLabels []string        `db:"-"`
}

// =====================================================================================
// Report shapes — the read-only contracts of the accounting reports (Trial Balance, P&L,
// Balance Sheet, account drill-down, reconciliation). Shapes follow docs/plan-accounting/
// 06-reports.md; the store queries that fill them are implemented in step 7 (reports.go /
// reconcile.go), so these are the API/return contracts, kept skeletal here.
// =====================================================================================

// AcctTrialBalanceRow is one account's turnover + closing balance over [From, To). Balance sign is
// per section: asset/cogs/opex → dr − cr; liability/equity/revenue → cr − dr (06).
type AcctTrialBalanceRow struct {
	Code      string          `db:"code"`
	Name      string          `db:"name"`
	Section   AcctSection     `db:"section"`
	Statement string          `db:"statement"`
	Debit     decimal.Decimal `db:"dr"`
	Credit    decimal.Decimal `db:"cr"`
	Balance   decimal.Decimal `db:"-"`
}

// AcctTrialBalance is GetTrialBalance's result: per-account rows + totals. Balanced (ΣDr == ΣCr) is
// the system's core smoke invariant — theoretically always true, surfaced so the UI can flag it.
type AcctTrialBalance struct {
	From        time.Time
	To          time.Time
	Rows        []AcctTrialBalanceRow
	TotalDebit  decimal.Decimal
	TotalCredit decimal.Decimal
	Balanced    bool
}

// AcctPLRow is one P&L account's values across the month columns plus its row total.
type AcctPLRow struct {
	Code   string
	Name   string
	Values []decimal.Decimal // per Months column
	Total  decimal.Decimal
}

// AcctPLSection groups P&L rows by section (revenue / cogs / opex, in that order).
type AcctPLSection struct {
	Section string
	Rows    []AcctPLRow
}

// AcctProfitLoss is GetProfitLoss's result (Excel "Income Statement"), monthly columns over the
// interval. Derived lines are per-month slices aligned to Months (06). Caveats lists phase-1 gaps
// (pre-tax, no carrier shipping cost) + per-entry caveats aggregated from the period.
type AcctProfitLoss struct {
	From            time.Time
	To              time.Time
	Months          []time.Time
	Sections        []AcctPLSection
	TotalRevenue    []decimal.Decimal
	NetCogs         []decimal.Decimal
	GrossProfit     []decimal.Decimal
	GrossMarginPct  []decimal.Decimal
	TotalOpex       []decimal.Decimal
	OperatingProfit []decimal.Decimal
	NetMarginPct    []decimal.Decimal
	// TotalTax / NetProfitAfterTax are the wave-3 Corporation-Tax lines (per-month, aligned to Months):
	// TotalTax is the period's Σ 'tax'-section (8010) charge, NetProfitAfterTax = OperatingProfit − TotalTax.
	// Tax is only ever a manual journal (no auto-accrual), so both are zero until the accountant posts CT.
	TotalTax          []decimal.Decimal
	NetProfitAfterTax []decimal.Decimal
	Caveats           []string
}

// AcctBalanceSheetRow is one BS account's closing balance as of the report date.
type AcctBalanceSheetRow struct {
	Code    string
	Name    string
	Balance decimal.Decimal
}

// AcctBalanceSheetSection is one BS grouping (assets / liabilities / equity) with its rows + total.
type AcctBalanceSheetSection struct {
	Section string
	Rows    []AcctBalanceSheetRow
	Total   decimal.Decimal
}

// AcctBalanceSheet is GetBalanceSheet's result (Excel "Balance Sheet"), balances from inception to
// AsOf. Equity includes the virtual "Current Period Net Profit" row (Σ of all PL accounts over the
// same span). BalanceCheck = Assets − (Liabilities + Equity); zero under the Dr=Cr invariant, kept
// as the Excel CHK trust panel.
type AcctBalanceSheet struct {
	AsOf             time.Time
	Assets           AcctBalanceSheetSection
	Liabilities      AcctBalanceSheetSection
	Equity           AcctBalanceSheetSection
	TotalAssets      decimal.Decimal
	TotalLiabilities decimal.Decimal
	TotalEquity      decimal.Decimal
	BalanceCheck     decimal.Decimal
	Balanced         bool
	Caveats          []string
}

// AcctAccountLedgerRow is one drill-down line for an account with its running balance (06).
type AcctAccountLedgerRow struct {
	EntryId        int             `db:"id"`
	OccurredAt     time.Time       `db:"occurred_at"`
	Description    string          `db:"description"`
	SourceType     AcctSourceType  `db:"source_type"`
	SourceKey      string          `db:"source_key"`
	Side           AcctSide        `db:"side"`
	Amount         decimal.Decimal `db:"amount"`
	Note           sql.NullString  `db:"note"`
	RunningBalance decimal.Decimal `db:"-"`
}

// AcctAccountLedger is GetAccountLedger's result: a page of drill-down rows for one account with the
// opening balance (balance before From) and closing balance; Total is the unpaginated row count.
type AcctAccountLedger struct {
	Code           string
	Name           string
	Section        AcctSection
	From           time.Time
	To             time.Time
	OpeningBalance decimal.Decimal
	ClosingBalance decimal.Decimal
	Rows           []AcctAccountLedgerRow
	Total          int
}

// AcctReconItem is one row inside a reconciliation block (a top-N sample; TotalCount on the block
// carries the full count). Ref is the operational identity (order uuid, movement id, run id, month).
type AcctReconItem struct {
	Ref    string
	Label  string
	Amount decimal.Decimal
}

// AcctReconBlock is one reconciliation dimension: the ledger figure, the operational figure, their
// delta, and a bounded item sample. A non-zero delta (outside FG, where drift is expected) is the
// signal the ledger diverged from operational truth (06).
type AcctReconBlock struct {
	Name        string
	Ledger      decimal.Decimal
	Operational decimal.Decimal
	Delta       decimal.Decimal
	Items       []AcctReconItem
	TotalCount  int
}

// AcctReconciliation is GetReconciliation's result: the per-dimension blocks proving the derived
// ledger matches operational data and surfacing what is deliberately unposted (06). It is both an
// admin report and the source for the worker's health alerts.
type AcctReconciliation struct {
	From              time.Time
	To                time.Time
	Revenue           AcctReconBlock
	Fees              AcctReconBlock
	COGS              AcctReconBlock
	Materials         AcctReconBlock
	FinishedGoods     AcctReconBlock
	Pending           AcctReconBlock
	UnpostedMovements AcctReconBlock
	// Vat reconciles the 2070 VAT-Payable ledger movement against the VAT the period's orders imply by
	// regime (phase 2, wave 1). A pointer so pre-phase-2 callers/serialisers see it absent rather than
	// a zero block; nil until GetReconciliation fills it.
	Vat *AcctReconBlock
	// Prepayments reconciles the 2090 Customer-Prepayments ledger balance at period end against the
	// obligation implied by orders paid on the delivered chain but not yet delivered (phase 2, wave 2).
	// Pointer for the same reason as Vat; nil until GetReconciliation fills it.
	Prepayments *AcctReconBlock
	// Shipping reconciles the 6030 Shipping & Fulfillment ledger debits against Σ shipment.actual_cost +
	// return_shipping_cost over the period (phase 2, wave 3). Pointer for the same reason as Vat/
	// Prepayments; nil until GetReconciliation fills it. Not yet surfaced on the wire (a UI follow-up,
	// like Prepayments) — it is available to the worker's health checks.
	Shipping *AcctReconBlock
	// Bank reconciles the 1010 Cash-Bank ledger balance as of the period end against Σ posted+matched
	// Revolut inbox lines (phase 2, wave 4 — §4.1). Pointer for the same reason as Vat/Prepayments/
	// Shipping; nil until GetReconciliation fills it.
	Bank *AcctReconBlock
}

// =====================================================================================
// VAT filing exports (phase 2, wave 1 — docs/plan-accounting-phase2/01-wave1-vat.md §1.5). Both are
// SOURCE-TYPE-AGNOSTIC: they aggregate the 2070 VAT lines of order entries by customer_order.vat_regime
// over the payment period, so they survive wave 2's prepayment split. Refunds net with a minus sign.
// Numbers for the accountant's manual JPK_VAT / OSS filing; full XML is phase 3.
// =====================================================================================

// AcctVatReturnPL is the JPK_VAT monthly aggregate (filed by the 25th). Output VAT split by regime
// (domestic PL, WNT self-charge, OSS shown for reference), input VAT by type, and the net payable.
// Caveats surfaces per-entry caveats aggregated over the month.
type AcctVatReturnPL struct {
	Month               time.Time
	OutputDomestic      decimal.Decimal
	OutputWntSelfCharge decimal.Decimal
	OssInfoTotal        decimal.Decimal
	InputDomestic       decimal.Decimal
	InputWnt            decimal.Decimal
	InputImport         decimal.Decimal
	NetPayable          decimal.Decimal
	// UK figures are a DIFFERENT jurisdiction: they are filed on the UK VAT return, never the Polish
	// JPK, so they are reported separately and excluded from NetPayable (mixing them understated/
	// overstated the Polish liability). Surfaced today via a caveat; a dedicated UK return is Phase 3.
	OutputUkStockDomestic decimal.Decimal // uk_stock_domestic output VAT (2070)
	InputUkDomestic       decimal.Decimal // domestic_uk purchase input VAT (2080), recoverable in the UK
	// Zero-rated NET revenue bases that JPK_VAT still declares (K_21 intra-community WDT, K_22 export).
	// They carry no VAT so they do not enter NetPayable; they exist for the declaration itself.
	NetWdt    decimal.Decimal
	NetExport decimal.Decimal
	// NET (tax base) figures the JPK_V7M declaration reports alongside the VAT amounts above — a
	// declaration line is (net, vat), not vat alone. NetDomestic backs P_19 (domestic 23% sales),
	// NetWnt P_23, NetImport P_25, NetInputDomestic P_42 (input on other domestic purchases). Sourced
	// from the same ledger lines the VAT figures come from, so they reconcile exactly.
	NetDomestic      decimal.Decimal
	NetWnt           decimal.Decimal
	NetImport        decimal.Decimal
	NetInputDomestic decimal.Decimal
	Caveats          []string
}

// FixedAsset is one capitalised asset in the register, depreciated straight-line over
// UsefulLifeMonths from AcquiredOn. CostBase is the base-currency cost; DisposedOn stops depreciation.
type FixedAsset struct {
	ID               int             `db:"id"`
	Name             string          `db:"name"`
	CostBase         decimal.Decimal `db:"cost_base"`
	AcquiredOn       time.Time       `db:"acquired_on"`
	UsefulLifeMonths int             `db:"useful_life_months"`
	DisposedOn       sql.NullTime    `db:"disposed_on"`
	CreatedAt        time.Time       `db:"created_at"`
}

// FixedAssetInsert is the create payload for a fixed asset.
type FixedAssetInsert struct {
	Name             string
	CostBase         decimal.Decimal
	AcquiredOn       time.Time
	UsefulLifeMonths int
}

// AcctFrs105Accounts is an FRS 105 micro-entity accounts DRAFT — the Income Statement + Statement of
// Financial Position re-grouped from the ledger into micro-entity line items. The entity is a single
// UK Ltd with EUR as its functional currency (the Polish operations are part of it), so the figures'
// currency (EUR) and whole-ledger scope are correct; it is a draft only for completeness (no tax /
// depreciation accrual) and accountant review — see Caveats.
type AcctFrs105Accounts struct {
	From time.Time
	To   time.Time
	// Income statement — each line natural-positive.
	Turnover               decimal.Decimal
	CostOfSales            decimal.Decimal
	GrossProfit            decimal.Decimal
	AdministrativeExpenses decimal.Decimal // opex excluding depreciation and tax
	Depreciation           decimal.Decimal // 6370, shown separately per FRS 105
	OperatingProfit        decimal.Decimal
	Tax                    decimal.Decimal // 6360
	ProfitForYear          decimal.Decimal
	// Statement of financial position — as at To.
	FixedAssets                decimal.Decimal // 1220 Equipment net of 1225 Accumulated Depreciation
	CurrentAssets              decimal.Decimal
	CreditorsWithinYear        decimal.Decimal
	NetCurrentAssets           decimal.Decimal
	TotalAssetsLessCurrentLiab decimal.Decimal
	CreditorsAfterYear         decimal.Decimal
	NetAssets                  decimal.Decimal
	CapitalAndReserves         decimal.Decimal
	// Currency is the ledger base currency the figures are in (EUR); a DRAFT flag for the UI.
	Currency string
	Caveats  []string
}

// AcctUkVatReturn is the quarterly UK VAT return (9-box MTD layout) for the UK-stock domestic regime.
// GRBPWR sells UK stock domestically (uk_stock_domestic) and reclaims UK input VAT (input_vat_regime =
// domestic_uk) — a separate jurisdiction from the Polish JPK. Boxes 2/8/9 are intra-EU and always zero
// post-Brexit for a GB return; Box 3 = Box 1 (no acquisitions), Box 5 = Box 3 − Box 4.
type AcctUkVatReturn struct {
	QuarterStart     time.Time
	Box1OutputVat    decimal.Decimal // VAT due on sales (uk_stock_domestic 2070)
	Box4InputVat     decimal.Decimal // VAT reclaimed on purchases (domestic_uk 2080)
	Box6NetSales     decimal.Decimal // total value of sales ex-VAT
	Box7NetPurchases decimal.Decimal // total value of purchases ex-VAT
}

// Box3TotalVatDue is Box 1 + Box 2 (Box 2 = 0 for GB).
func (r AcctUkVatReturn) Box3TotalVatDue() decimal.Decimal { return r.Box1OutputVat }

// Box5NetVat is Box 3 − Box 4: positive = payable to HMRC, negative = reclaimable.
func (r AcctUkVatReturn) Box5NetVat() decimal.Decimal { return r.Box1OutputVat.Sub(r.Box4InputVat) }

// AcctVatSalesRow is one order's sales figures for the JPK_V7M evidence (Ewidencja/SprzedazWiersz).
// One row per order for the regimes the Polish register declares — pl_domestic, wdt, export (OSS and
// uk_stock are filed elsewhere). Orders carrying a BuyerVatID become individual invoice rows; the rest
// (B2C) are aggregated into a single periodic internal row per regime by the JPK builder.
type AcctVatSalesRow struct {
	UUID       string          `db:"uuid"`         // order reference → DowodSprzedazy
	Placed     time.Time       `db:"placed"`       // DataWystawienia / DataSprzedazy
	BuyerVatID sql.NullString  `db:"buyer_vat_id"` // B2B buyer NIP (NrKontrahenta); empty → B2C aggregate
	Regime     string          `db:"regime"`       // vat_regime (pl_domestic / wdt / export)
	Net        decimal.Decimal `db:"net"`          // net revenue (refunds signed negative)
	Vat        decimal.Decimal `db:"vat"`          // output VAT from 2070 (refunds signed negative)
}

// AcctOssRow is one destination country's OSS B2C line: country, applied rate, net and VAT.
type AcctOssRow struct {
	Country string
	RatePct decimal.Decimal
	Net     decimal.Decimal
	Vat     decimal.Decimal
}

// AcctOssReturn is the quarterly OSS aggregate: per-country B2C rows plus the quarter totals.
type AcctOssReturn struct {
	QuarterStart time.Time
	Rows         []AcctOssRow
	TotalNet     decimal.Decimal
	TotalVat     decimal.Decimal
}

// =====================================================================================
// Wave 4 — money side (docs/plan-accounting-phase2/04-wave4-money.md). The Revolut bank inbox (4.1)
// and the AP/AR subledgers (4.4). Base currency is EUR; a non-EUR bank line posts via the phase-1
// amount_src / currency_src FX mechanic.
// =====================================================================================

// AcctBankTxnState is the lifecycle of a parsed bank inbox line (acct_bank_txn.state — DB CHECK
// chk_acct_bank_txn_state, migration 0197): unmatched (needs a decision), matched (linked but not posted
// — reserved for auto-match), posted (a journal entry was created), ignored (deliberately not booked,
// e.g. an internal EXCHANGE leg).
type AcctBankTxnState string

const (
	AcctBankTxnUnmatched AcctBankTxnState = "unmatched"
	AcctBankTxnMatched   AcctBankTxnState = "matched"
	AcctBankTxnPosted    AcctBankTxnState = "posted"
	AcctBankTxnIgnored   AcctBankTxnState = "ignored"
)

// ValidAcctBankTxnStates mirrors the DB CHECK chk_acct_bank_txn_state (migration 0197).
var ValidAcctBankTxnStates = map[AcctBankTxnState]bool{
	AcctBankTxnUnmatched: true,
	AcctBankTxnMatched:   true,
	AcctBankTxnPosted:    true,
	AcctBankTxnIgnored:   true,
}

// AcctBankTxnInsert is one parsed bank statement line ready for the inbox (produced by a BankCsvParser).
// Amount is SIGNED (negative = outflow); Currency is the payment currency (multi-currency Revolut). State
// is the parser's default disposition (unmatched, or ignored for an internal EXCHANGE leg). Raw is the whole
// CSV row as JSON. ExternalId is the dedup key (Revolut id + ':' + payment currency).
type AcctBankTxnInsert struct {
	Source           string
	ExternalId       string
	BookedAt         time.Time
	Amount           decimal.Decimal
	Currency         string
	Fee              decimal.NullDecimal
	Description      string
	Counterparty     sql.NullString
	State            AcctBankTxnState
	SuggestedAccount sql.NullString
	Raw              string
}

// AcctBankTxn is a stored bank inbox line (acct_bank_txn).
type AcctBankTxn struct {
	Id               int                 `db:"id"`
	Source           string              `db:"source"`
	ExternalId       string              `db:"external_id"`
	BookedAt         time.Time           `db:"booked_at"`
	Amount           decimal.Decimal     `db:"amount"`
	Currency         string              `db:"currency"`
	Fee              decimal.NullDecimal `db:"fee"`
	Description      string              `db:"description"`
	Counterparty     sql.NullString      `db:"counterparty"`
	State            AcctBankTxnState    `db:"state"`
	MatchedEntryId   sql.NullInt64       `db:"matched_entry_id"`
	SuggestedAccount sql.NullString      `db:"suggested_account"`
	CreatedAt        time.Time           `db:"created_at"`
}

// AcctBankImportResult is the outcome of ImportBankCsv: how many parsed lines were newly inserted vs
// skipped as duplicates (a re-imported statement), and the total parsed.
type AcctBankImportResult struct {
	Parsed   int
	Imported int
	Skipped  int
}

// AcctBankRule is a substring→account suggestion (acct_bank_rule): a bank line whose counterparty or
// description contains Pattern (case-insensitive) is suggested AccountCode at import.
type AcctBankRule struct {
	Id          int    `db:"id"`
	Pattern     string `db:"pattern"`
	AccountCode string `db:"account_code"`
}

// Supplier is a purchase-side counterparty (supplier table, migration 0197) — the AP catalog (4.4).
type Supplier struct {
	Id        int            `db:"id"`
	Name      string         `db:"name"`
	VatId     sql.NullString `db:"vat_id"`
	Notes     sql.NullString `db:"notes"`
	CreatedAt time.Time      `db:"created_at"`
}

// SupplierInsert is the writable payload of a new supplier.
type SupplierInsert struct {
	Name  string
	VatId sql.NullString
	Notes sql.NullString
}

// AcctPayableRow is one supplier's Accounts-Payable (2010) position (GetPayables, 4.4): Accrued is the Σ
// 2010 credits of the supplier's tagged entries (material receipts owed), Paid the Σ 2010 debits (payments
// booked against the supplier), Balance = Accrued − Paid (what is still owed). SupplierId 0 groups entries
// with a 2010 movement but no supplier tag (legacy / untagged AP).
type AcctPayableRow struct {
	SupplierId   int             `db:"supplier_id"`
	SupplierName string          `db:"supplier_name"`
	Accrued      decimal.Decimal `db:"accrued"`
	Paid         decimal.Decimal `db:"paid"`
	Balance      decimal.Decimal `db:"-"`
}

// AcctReceivableRow is one bank-invoice order's Accounts-Receivable (1040) position (GetReceivables, 4.4):
// Invoiced is the Σ 1040 debits for the order (revenue recognised against a receivable), Received the Σ
// 1040 credits (payment received), Balance = Invoiced − Received (still outstanding). Ref is the order uuid.
type AcctReceivableRow struct {
	Ref      string          `db:"ref"`
	Invoiced decimal.Decimal `db:"invoiced"`
	Received decimal.Decimal `db:"received"`
	Balance  decimal.Decimal `db:"-"`
}
