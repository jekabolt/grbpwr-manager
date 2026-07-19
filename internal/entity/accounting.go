package entity

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/shopspring/decimal"
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
	RefundAmount   decimal.Decimal `json:"refund_amount"`    // order currency, THIS refund only, shipping included
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
