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

	AcctSideDebit  AcctSide = "debit"
	AcctSideCredit AcctSide = "credit"

	AcctSourceOrderSale          AcctSourceType = "order_sale"
	AcctSourceOrderRefund        AcctSourceType = "order_refund"
	AcctSourceMaterialReceipt    AcctSourceType = "material_receipt"
	AcctSourceMaterialIssue      AcctSourceType = "material_issue"
	AcctSourceMaterialReturn     AcctSourceType = "material_return"
	AcctSourceMaterialWriteoff   AcctSourceType = "material_writeoff"
	AcctSourceMaterialAdjustment AcctSourceType = "material_adjustment"
	AcctSourceProductionReceive  AcctSourceType = "production_receive"
	AcctSourceOpexMonth          AcctSourceType = "opex_month"
	AcctSourceManual             AcctSourceType = "manual"
	AcctSourceReversal           AcctSourceType = "reversal"

	AcctEventOrderPaid   AcctEventType = "order_paid"
	AcctEventOrderRefund AcctEventType = "order_refund"
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
// of truth mirrored by the DB CHECK (chk_acct_entry_source_type, migration 0189) and validated in
// dto. Map-to-bool (not struct{}), matching entity.ValidMaterialClasses: migrationlint's
// enum_drift_test.go compares it via mapKeysAsStrings[K ~string](m map[K]bool).
var ValidAcctSourceTypes = map[AcctSourceType]bool{
	AcctSourceOrderSale:          true,
	AcctSourceOrderRefund:        true,
	AcctSourceMaterialReceipt:    true,
	AcctSourceMaterialIssue:      true,
	AcctSourceMaterialReturn:     true,
	AcctSourceMaterialWriteoff:   true,
	AcctSourceMaterialAdjustment: true,
	AcctSourceProductionReceive:  true,
	AcctSourceOpexMonth:          true,
	AcctSourceManual:             true,
	AcctSourceReversal:           true,
}

// ValidAcctSides is the storable set for acct_journal_line.side — mirrors the DB CHECK
// (chk_acct_line_side, migration 0189).
var ValidAcctSides = map[AcctSide]bool{
	AcctSideDebit:  true,
	AcctSideCredit: true,
}

// ValidAcctEventTypes is the storable set for acct_event.event_type — mirrors the DB CHECK
// (chk_acct_event_type, migration 0189).
var ValidAcctEventTypes = map[AcctEventType]bool{
	AcctEventOrderPaid:   true,
	AcctEventOrderRefund: true,
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
	Lines       []AcctJournalLineInsert
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
	VatRegime sql.NullString      `db:"vat_regime"`
	Items     []AcctOrderItemFact `db:"-"`
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
	Caveats         []string
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
	Caveats   []string
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
