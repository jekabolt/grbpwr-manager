package dto

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// Step 6 (admin API) dto conversions for the double-entry accounting module
// (docs/plan-accounting/05-admin-api.md, 06-reports.md). Money reuses the existing
// pbDecimalFromDecimal / nullDecimalFromPb / pbDecimalFromNull / requiredDecimalFromPb / roundMoney
// helpers (dto/techcard.go) and the costMaxFrac/costLimit bounds (dto/techcard_production.go) — the
// accounting columns share the same DECIMAL(12,2) shape as the costing columns those guard. Dates
// are plain YYYY-MM-DD strings (never google.protobuf.Timestamp — 05's convention); "month" fields
// reuse the OPEX firstOfMonth precedent (dto/opex.go) per 09-implementation-notes.md §9.
//
// Balance (Σdebit == Σcredit) is intentionally NOT validated here — that invariant belongs to the
// store (ErrAcctUnbalanced, mapped to InvalidArgument in apisrv).

// acctDateLayout is the wire format for every plain accounting date (occurred_at, from/to, as_of,
// and the formatted "month" field of AcctPeriod).
const acctDateLayout = "2006-01-02"

// Column-width guards mirror the acct_account / acct_journal_entry / acct_journal_line VARCHAR
// widths (migration 0189_accounting_core.sql) so an over-long value is a clean InvalidArgument
// instead of a MySQL data-truncation error surfacing as a 500 (the g25-15 precedent applied
// elsewhere in this package, e.g. ConvertPbEmployeeToEntity).
const (
	maxAcctCode        = 8   // acct_account.code VARCHAR(8)
	maxAcctName        = 128 // acct_account.name VARCHAR(128)
	maxAcctDescription = 512 // acct_journal_entry.description VARCHAR(512)
	maxAcctNote        = 255 // acct_journal_line.note VARCHAR(255)
	maxAcctCurrencySrc = 4   // acct_journal_line.currency_src VARCHAR(4) — precedent: USDT (0185)
	// maxAcctJournalLines caps a single manual entry's line count (D-4). The store inserts the lines
	// in one bulk statement; a very large array would otherwise blow MySQL's ~65535 placeholder limit
	// and surface as an opaque driver error instead of a clean InvalidArgument. 1000 lines is far more
	// than any hand-posted entry needs while staying well under the placeholder ceiling.
	maxAcctJournalLines = 1000
)

// validAcctSections / validAcctStatements mirror the acct_account CHECK constraints
// (chk_acct_account_section / chk_acct_account_statement, migration 0189). entity/accounting.go
// exports the section/statement values as constants but no validity map (unlike
// entity.ValidAcctSourceTypes), so CreateAcctAccount validates against its own small set here.
var validAcctSections = map[string]bool{
	string(entity.AcctSectionAsset):     true,
	string(entity.AcctSectionLiability): true,
	string(entity.AcctSectionEquity):    true,
	string(entity.AcctSectionRevenue):   true,
	string(entity.AcctSectionCogs):      true,
	string(entity.AcctSectionOpex):      true,
}

var validAcctStatements = map[string]bool{
	entity.AcctStatementBS: true,
	entity.AcctStatementPL: true,
}

// parseAcctDate parses a required YYYY-MM-DD date field.
func parseAcctDate(s, field string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, fmt.Errorf("%s is required", field)
	}
	t, err := time.Parse(acctDateLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: %w", field, s, err)
	}
	return t, nil
}

// parseOptionalAcctDate parses an optional YYYY-MM-DD field; empty returns the zero time.Time,
// which the accounting store treats as an unbounded end of the range (ListJournalEntries already
// substitutes year-1/year-9999 sentinels for a zero From/To).
func parseOptionalAcctDate(s, field string) (time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return time.Time{}, nil
	}
	return parseAcctDate(s, field)
}

// ParseAcctDateRange parses a required half-open [from, to) range (to exclusive) shared by the
// period-scoped reports (Trial Balance, P&L, reconciliation) — see entity.AcctEntryFilter's doc
// comment for the half-open convention this mirrors.
func ParseAcctDateRange(from, to string) (time.Time, time.Time, error) {
	f, err := parseAcctDate(from, "from")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	t, err := parseAcctDate(to, "to")
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if t.Before(f) {
		return time.Time{}, time.Time{}, fmt.Errorf("to precedes from")
	}
	return f, t, nil
}

// ParseAcctAsOf parses GetBalanceSheet's required, inclusive as_of date.
func ParseAcctAsOf(s string) (time.Time, error) {
	return parseAcctDate(s, "as_of")
}

// ParseAcctMonth parses a period 'month' field (CloseAcctPeriod/ReopenAcctPeriod): canonical
// YYYY-MM, or any YYYY-MM-DD date in the month, normalised to the 1st — the OPEX firstOfMonth
// convention (dto/opex.go) that 09-implementation-notes.md §9 points to for accounting's "month" fields.
func ParseAcctMonth(s string) (time.Time, error) {
	return firstOfMonth(s, "month")
}

// parseOccurredAt parses CreateJournalEntry's occurred_at: a required date, not more than one day
// in the future (05-admin-api.md: "occurred_at парсится строго 2006-01-02, не в будущем более чем
// на 1 день").
func parseOccurredAt(s string) (time.Time, error) {
	t, err := parseAcctDate(s, "occurred_at")
	if err != nil {
		return time.Time{}, err
	}
	if t.After(time.Now().UTC().AddDate(0, 0, 1)) {
		return time.Time{}, fmt.Errorf("occurred_at must not be more than 1 day in the future")
	}
	return t, nil
}

// --- chart of accounts ---

// ConvertAcctAccountToPb converts a stored chart-of-accounts row to protobuf.
func ConvertAcctAccountToPb(a entity.AcctAccount) *pb_admin.AcctAccount {
	return &pb_admin.AcctAccount{
		Id:        int32(a.Id),
		Code:      a.Code,
		Name:      a.Name,
		Section:   string(a.Section),
		Statement: a.Statement,
		IsSystem:  a.IsSystem,
		Archived:  a.Archived,
	}
}

// ConvertAcctAccountListToPb converts the chart of accounts to protobuf.
func ConvertAcctAccountListToPb(list []entity.AcctAccount) []*pb_admin.AcctAccount {
	out := make([]*pb_admin.AcctAccount, 0, len(list))
	for _, a := range list {
		out = append(out, ConvertAcctAccountToPb(a))
	}
	return out
}

// ConvertPbCreateAcctAccount validates and converts a new chart-of-accounts entry. Code is
// upper-cased (accounts are conventionally numeric, e.g. "1030", but the column has no format
// CHECK, only a width one — normalising case avoids a confusing near-duplicate under the table's
// case-insensitive collation).
func ConvertPbCreateAcctAccount(req *pb_admin.CreateAcctAccountRequest) (entity.AcctAccountInsert, error) {
	code := strings.ToUpper(strings.TrimSpace(req.GetCode()))
	if code == "" {
		return entity.AcctAccountInsert{}, fmt.Errorf("code is required")
	}
	if len(code) > maxAcctCode {
		return entity.AcctAccountInsert{}, fmt.Errorf("code must be at most %d characters", maxAcctCode)
	}
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return entity.AcctAccountInsert{}, fmt.Errorf("name is required")
	}
	if len(name) > maxAcctName {
		return entity.AcctAccountInsert{}, fmt.Errorf("name must be at most %d characters", maxAcctName)
	}
	section := strings.ToLower(strings.TrimSpace(req.GetSection()))
	if !validAcctSections[section] {
		return entity.AcctAccountInsert{}, fmt.Errorf("invalid section %q", req.GetSection())
	}
	statement := strings.ToUpper(strings.TrimSpace(req.GetStatement()))
	if !validAcctStatements[statement] {
		return entity.AcctAccountInsert{}, fmt.Errorf("invalid statement %q", req.GetStatement())
	}
	return entity.AcctAccountInsert{
		Code:      code,
		Name:      name,
		Section:   entity.AcctSection(section),
		Statement: statement,
	}, nil
}

// ConvertPbUpdateAcctAccount validates an account rename (code and section are immutable).
func ConvertPbUpdateAcctAccount(req *pb_admin.UpdateAcctAccountRequest) (code, name string, err error) {
	code = strings.ToUpper(strings.TrimSpace(req.GetCode()))
	if code == "" {
		return "", "", fmt.Errorf("code is required")
	}
	name = strings.TrimSpace(req.GetName())
	if name == "" {
		return "", "", fmt.Errorf("name is required")
	}
	if len(name) > maxAcctName {
		return "", "", fmt.Errorf("name must be at most %d characters", maxAcctName)
	}
	return code, name, nil
}

// --- journal ---

// ConvertPbCreateJournalEntry validates and converts a manual journal-entry request:
//   - occurred_at parses strictly as YYYY-MM-DD, not more than 1 day in the future;
//   - at least 2 lines;
//   - each line carries either `amount` or (`amount_src` + `currency_src`), never both.
//
// Balance (Σdebit == Σcredit) is intentionally NOT checked here — the store owns that invariant
// (ErrAcctUnbalanced). A line that supplied amount_src+currency_src is returned with Amount left
// at its zero value: the caller (apisrv) folds it to base currency via the costing FX rates and
// fills Amount in before the entry reaches the store, which never touches FX itself.
func ConvertPbCreateJournalEntry(req *pb_admin.CreateJournalEntryRequest) (entity.AcctJournalEntryInsert, error) {
	occurredAt, err := parseOccurredAt(req.GetOccurredAt())
	if err != nil {
		return entity.AcctJournalEntryInsert{}, err
	}
	description := strings.TrimSpace(req.GetDescription())
	if description == "" {
		return entity.AcctJournalEntryInsert{}, fmt.Errorf("description is required")
	}
	if len(description) > maxAcctDescription {
		return entity.AcctJournalEntryInsert{}, fmt.Errorf("description must be at most %d characters", maxAcctDescription)
	}
	if len(req.GetLines()) < 2 {
		return entity.AcctJournalEntryInsert{}, fmt.Errorf("a journal entry needs at least 2 lines, got %d", len(req.GetLines()))
	}
	if len(req.GetLines()) > maxAcctJournalLines {
		return entity.AcctJournalEntryInsert{}, fmt.Errorf("a journal entry has at most %d lines, got %d", maxAcctJournalLines, len(req.GetLines()))
	}
	lines := make([]entity.AcctJournalLineInsert, 0, len(req.GetLines()))
	for i, l := range req.GetLines() {
		ln, err := convertPbJournalLineInput(l)
		if err != nil {
			return entity.AcctJournalEntryInsert{}, fmt.Errorf("line %d: %w", i+1, err)
		}
		lines = append(lines, ln)
	}
	ins := entity.AcctJournalEntryInsert{
		OccurredAt:  occurredAt,
		Description: description,
		Lines:       lines,
	}
	// Optional AP supplier tag (phase 2, wave 4): a manual 2010 payment against a supplier so GetPayables
	// nets it. 0 = untagged.
	if id := req.GetSupplierId(); id > 0 {
		ins.SupplierID = sql.NullInt64{Int64: int64(id), Valid: true}
	}
	return ins, nil
}

// convertPbJournalLineInput validates one journal line: account_code is required, and the line
// must carry either `amount` (already base currency) or `amount_src`+`currency_src` (folded to
// base by the caller) — never both, never neither. currency_src must be a currency the shop can
// denominate an expense in: internal/currency.IsSupported || internal/currency.IsExpenseCurrency
// (05's literal validation rule; IsExpenseCurrency alone already implies IsSupported, so this is
// belt-and-suspenders, kept explicit per spec).
func convertPbJournalLineInput(l *pb_admin.AcctJournalLineInput) (entity.AcctJournalLineInsert, error) {
	if l == nil {
		return entity.AcctJournalLineInsert{}, fmt.Errorf("line is required")
	}
	accountCode := strings.ToUpper(strings.TrimSpace(l.GetAccountCode()))
	if accountCode == "" {
		return entity.AcctJournalLineInsert{}, fmt.Errorf("account_code is required")
	}
	side := entity.AcctSideCredit
	if l.GetIsDebit() {
		side = entity.AcctSideDebit
	}
	note, err := acctLineNote(l.GetNote())
	if err != nil {
		return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: %w", accountCode, err)
	}

	hasAmount := l.GetAmount() != nil && strings.TrimSpace(l.GetAmount().Value) != ""
	hasSrc := l.GetAmountSrc() != nil && strings.TrimSpace(l.GetAmountSrc().Value) != ""
	currencySrc := strings.ToUpper(strings.TrimSpace(l.GetCurrencySrc()))

	switch {
	case hasAmount && hasSrc:
		return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: either amount or amount_src+currency_src, not both", accountCode)
	case !hasAmount && !hasSrc:
		return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: amount or amount_src+currency_src is required", accountCode)
	case hasSrc:
		if currencySrc == "" {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: currency_src is required with amount_src", accountCode)
		}
		if len(currencySrc) > maxAcctCurrencySrc {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: currency_src must be at most %d characters", accountCode, maxAcctCurrencySrc)
		}
		if !currency.IsSupported(currencySrc) && !currency.IsExpenseCurrency(currencySrc) {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: unsupported currency_src %q", accountCode, currencySrc)
		}
		amountSrc, err := requiredDecimalFromPb(l.GetAmountSrc(), "amount_src", costMaxFrac, costLimit)
		if err != nil {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: %w", accountCode, err)
		}
		if amountSrc.LessThanOrEqual(decimal.Zero) {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: amount_src must be > 0", accountCode)
		}
		return entity.AcctJournalLineInsert{
			AccountCode: accountCode,
			Side:        side,
			AmountSrc:   decimal.NullDecimal{Decimal: amountSrc, Valid: true},
			CurrencySrc: sql.NullString{String: currencySrc, Valid: true},
			Note:        note,
		}, nil
	default: // hasAmount
		if currencySrc != "" {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: currency_src without amount_src", accountCode)
		}
		amount, err := requiredDecimalFromPb(l.GetAmount(), "amount", costMaxFrac, costLimit)
		if err != nil {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: %w", accountCode, err)
		}
		if amount.LessThanOrEqual(decimal.Zero) {
			return entity.AcctJournalLineInsert{}, fmt.Errorf("account %s: amount must be > 0", accountCode)
		}
		return entity.AcctJournalLineInsert{
			AccountCode: accountCode,
			Side:        side,
			Amount:      amount,
			Note:        note,
		}, nil
	}
}

// acctLineNote trims a journal line's free-text note, returning NULL for an empty string.
func acctLineNote(s string) (sql.NullString, error) {
	n := strings.TrimSpace(s)
	if n == "" {
		return sql.NullString{}, nil
	}
	if len(n) > maxAcctNote {
		return sql.NullString{}, fmt.Errorf("note must be at most %d characters", maxAcctNote)
	}
	return sql.NullString{String: n, Valid: true}, nil
}

// ErrNoFxRate is returned by FoldJournalLineAmountToBase when the source currency has no costing FX
// rate; the caller (apisrv) maps it to InvalidArgument ("add <CCY> costing fx rate first").
var ErrNoFxRate = errors.New("no costing fx rate")

// FoldJournalLineAmountToBase converts a manual journal line's amount_src/currency_src into the base
// currency (EUR) via fx, mirroring FoldOpexLinesToBase / FoldProductionRunCostsToBase. It returns
// ErrNoFxRate when the currency has no rate. D-3: an amount_src within DECIMAL(12,2) can exceed that
// column bound once folded to base (rate > 1), so the folded result is re-validated here and an
// out-of-range fold is returned as an error rather than overflowing the store as an opaque Internal;
// the caller maps both errors to InvalidArgument.
func FoldJournalLineAmountToBase(fx CostingFx, amountSrc decimal.Decimal, currencySrc string) (decimal.Decimal, error) {
	base, ok := fx.toBase(amountSrc, currencySrc)
	if !ok {
		return decimal.Decimal{}, ErrNoFxRate
	}
	base = roundMoney(base)
	if base.Abs().GreaterThanOrEqual(decimal.NewFromInt(costLimit)) {
		return decimal.Decimal{}, fmt.Errorf("folded amount %s exceeds the maximum of %d", base, costLimit)
	}
	return base, nil
}

// ConvertAcctJournalLineToPb converts one stored journal line (with its joined account code/name).
func ConvertAcctJournalLineToPb(l entity.AcctJournalLine) *pb_admin.AcctJournalLine {
	pb := &pb_admin.AcctJournalLine{
		Id:          int32(l.Id),
		AccountCode: l.AccountCode,
		AccountName: l.AccountName,
		Side:        string(l.Side),
		Amount:      pbDecimalFromDecimal(l.Amount),
		AmountSrc:   pbDecimalFromNull(l.AmountSrc),
	}
	if l.CurrencySrc.Valid {
		pb.CurrencySrc = l.CurrencySrc.String
	}
	if l.Note.Valid {
		pb.Note = l.Note.String
	}
	return pb
}

// ConvertAcctJournalEntryToPb converts a journal-entry HEADER only — no lines, no total — the
// shape ListJournalEntries returns (05-admin-api.md: "entries []AcctJournalEntry (без lines)").
// Use ConvertAcctJournalEntryFullToPb when lines are available (GetJournalEntry,
// CreateJournalEntry, ReverseJournalEntry).
func ConvertAcctJournalEntryToPb(e entity.AcctJournalEntry) *pb_admin.AcctJournalEntry {
	pb := &pb_admin.AcctJournalEntry{
		Id:          int32(e.Id),
		OccurredAt:  e.OccurredAt.Format(acctDateLayout),
		Description: e.Description,
		SourceType:  string(e.SourceType),
		SourceKey:   e.SourceKey,
		ReversalOf:  nullInt32ToPb(e.ReversalOf),
		ReversedBy:  nullInt32ToPb(e.ReversedBy),
		CreatedBy:   e.CreatedBy,
		HasCaveat:   e.HasCaveat,
	}
	if e.HasCaveat && e.Caveat.Valid {
		pb.Caveat = e.Caveat.String
	}
	return pb
}

// ConvertAcctJournalEntryListToPb converts a page of journal-entry headers.
func ConvertAcctJournalEntryListToPb(list []entity.AcctJournalEntry) []*pb_admin.AcctJournalEntry {
	out := make([]*pb_admin.AcctJournalEntry, 0, len(list))
	for _, e := range list {
		out = append(out, ConvertAcctJournalEntryToPb(e))
	}
	return out
}

// ConvertAcctJournalEntryFullToPb converts an entry WITH its lines (GetJournalEntry,
// CreateJournalEntry's and ReverseJournalEntry's response) and computes `total` as Σdebit, which
// equals Σcredit under the store's balance invariant.
func ConvertAcctJournalEntryFullToPb(full entity.AcctJournalEntryFull) *pb_admin.AcctJournalEntry {
	pb := ConvertAcctJournalEntryToPb(full.Entry)
	pb.Lines = make([]*pb_admin.AcctJournalLine, 0, len(full.Lines))
	total := decimal.Zero
	for _, l := range full.Lines {
		pb.Lines = append(pb.Lines, ConvertAcctJournalLineToPb(l))
		if l.Side == entity.AcctSideDebit {
			total = total.Add(l.Amount)
		}
	}
	pb.Total = pbDecimalFromDecimal(total)
	return pb
}

// ConvertPbAcctEntryFilter validates and converts ListJournalEntries' filter. from/to are optional
// (empty = unbounded, mirroring the store's own zero-time substitution in ListJournalEntries).
func ConvertPbAcctEntryFilter(req *pb_admin.ListJournalEntriesRequest) (entity.AcctEntryFilter, error) {
	from, err := parseOptionalAcctDate(req.GetFrom(), "from")
	if err != nil {
		return entity.AcctEntryFilter{}, err
	}
	to, err := parseOptionalAcctDate(req.GetTo(), "to")
	if err != nil {
		return entity.AcctEntryFilter{}, err
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return entity.AcctEntryFilter{}, fmt.Errorf("to precedes from")
	}
	return entity.AcctEntryFilter{
		From:        from,
		To:          to,
		AccountCode: strings.ToUpper(strings.TrimSpace(req.GetAccountCode())),
		SourceType:  entity.AcctSourceType(strings.TrimSpace(req.GetSourceType())),
		Limit:       int(req.GetLimit()),
		Offset:      int(req.GetOffset()),
	}, nil
}

// --- periods ---

// ConvertAcctPeriodToPb converts a stored accounting period to protobuf.
func ConvertAcctPeriodToPb(p entity.AcctPeriod) *pb_admin.AcctPeriod {
	pb := &pb_admin.AcctPeriod{
		Period: p.Period.Format(acctDateLayout),
		Status: p.Status,
	}
	if p.ClosedAt.Valid {
		pb.ClosedAt = p.ClosedAt.Time.Format(time.RFC3339)
	}
	if p.ClosedBy.Valid {
		pb.ClosedBy = p.ClosedBy.String
	}
	return pb
}

// ConvertAcctPeriodListToPb converts a slice of accounting periods to protobuf.
func ConvertAcctPeriodListToPb(list []entity.AcctPeriod) []*pb_admin.AcctPeriod {
	out := make([]*pb_admin.AcctPeriod, 0, len(list))
	for _, p := range list {
		out = append(out, ConvertAcctPeriodToPb(p))
	}
	return out
}

// --- reports (docs/plan-accounting/06-reports.md) ---

// ConvertAcctTrialBalanceToPb converts a Trial Balance report to protobuf.
func ConvertAcctTrialBalanceToPb(tb entity.AcctTrialBalance) *pb_admin.GetTrialBalanceResponse {
	rows := make([]*pb_admin.TrialBalanceRow, 0, len(tb.Rows))
	for _, r := range tb.Rows {
		rows = append(rows, &pb_admin.TrialBalanceRow{
			Code:    r.Code,
			Name:    r.Name,
			Section: string(r.Section),
			Debit:   pbDecimalFromDecimal(r.Debit),
			Credit:  pbDecimalFromDecimal(r.Credit),
			Balance: pbDecimalFromDecimal(r.Balance),
		})
	}
	return &pb_admin.GetTrialBalanceResponse{
		Rows:        rows,
		TotalDebit:  pbDecimalFromDecimal(tb.TotalDebit),
		TotalCredit: pbDecimalFromDecimal(tb.TotalCredit),
		Balanced:    tb.Balanced,
	}
}

// pbDecimalListFromDecimals converts a slice of decimals to a slice of pb decimals, preserving
// order (P&L rows/totals are month-column-aligned slices).
func pbDecimalListFromDecimals(list []decimal.Decimal) []*pb_decimal.Decimal {
	out := make([]*pb_decimal.Decimal, 0, len(list))
	for _, d := range list {
		out = append(out, pbDecimalFromDecimal(d))
	}
	return out
}

// ConvertAcctProfitLossToPb converts a P&L (Income Statement) report to protobuf.
func ConvertAcctProfitLossToPb(pl entity.AcctProfitLoss) *pb_admin.GetProfitLossStatementResponse {
	months := make([]string, 0, len(pl.Months))
	for _, m := range pl.Months {
		months = append(months, m.Format(acctDateLayout))
	}
	sections := make([]*pb_admin.AcctPLSection, 0, len(pl.Sections))
	for _, sec := range pl.Sections {
		rows := make([]*pb_admin.AcctPLRow, 0, len(sec.Rows))
		for _, r := range sec.Rows {
			rows = append(rows, &pb_admin.AcctPLRow{
				Code:   r.Code,
				Name:   r.Name,
				Values: pbDecimalListFromDecimals(r.Values),
				Total:  pbDecimalFromDecimal(r.Total),
			})
		}
		sections = append(sections, &pb_admin.AcctPLSection{Section: sec.Section, Rows: rows})
	}
	return &pb_admin.GetProfitLossStatementResponse{
		Months:   months,
		Sections: sections,
		Totals: &pb_admin.AcctPLTotals{
			TotalRevenue:      pbDecimalListFromDecimals(pl.TotalRevenue),
			NetCogs:           pbDecimalListFromDecimals(pl.NetCogs),
			GrossProfit:       pbDecimalListFromDecimals(pl.GrossProfit),
			GrossMarginPct:    pbDecimalListFromDecimals(pl.GrossMarginPct),
			TotalOpex:         pbDecimalListFromDecimals(pl.TotalOpex),
			OperatingProfit:   pbDecimalListFromDecimals(pl.OperatingProfit),
			NetMarginPct:      pbDecimalListFromDecimals(pl.NetMarginPct),
			TotalTax:          pbDecimalListFromDecimals(pl.TotalTax),
			NetProfitAfterTax: pbDecimalListFromDecimals(pl.NetProfitAfterTax),
		},
		Caveats: pl.Caveats,
	}
}

// acctNetProfitRowName is the label the step-7 Balance Sheet report builder is expected to give the
// virtual "Current Period Net Profit" equity row (06-reports.md) — it has no acct_account code, so
// it cannot be matched by code. GetBalanceSheetResponse.net_profit_row is a best-effort convenience
// surface of that row, matched by this name; it is simply left unset if step 7 names the row
// differently, which is harmless — the row is still present in equity.rows either way.
const acctNetProfitRowName = "Current Period Net Profit"

// ConvertAcctBalanceSheetToPb converts a Balance Sheet report to protobuf.
func ConvertAcctBalanceSheetToPb(bs entity.AcctBalanceSheet) *pb_admin.GetBalanceSheetResponse {
	resp := &pb_admin.GetBalanceSheetResponse{
		AsOf:             bs.AsOf.Format(acctDateLayout),
		Assets:           convertAcctBsSectionToPb(bs.Assets),
		Liabilities:      convertAcctBsSectionToPb(bs.Liabilities),
		Equity:           convertAcctBsSectionToPb(bs.Equity),
		TotalAssets:      pbDecimalFromDecimal(bs.TotalAssets),
		TotalLiabilities: pbDecimalFromDecimal(bs.TotalLiabilities),
		TotalEquity:      pbDecimalFromDecimal(bs.TotalEquity),
		BalanceCheck:     pbDecimalFromDecimal(bs.BalanceCheck),
		Balanced:         bs.Balanced,
		Caveats:          bs.Caveats,
	}
	for _, r := range bs.Equity.Rows {
		if r.Name == acctNetProfitRowName {
			resp.NetProfitRow = &pb_admin.AcctBalanceSheetRow{Code: r.Code, Name: r.Name, Balance: pbDecimalFromDecimal(r.Balance)}
			break
		}
	}
	return resp
}

func convertAcctBsSectionToPb(sec entity.AcctBalanceSheetSection) *pb_admin.AcctBalanceSheetSection {
	rows := make([]*pb_admin.AcctBalanceSheetRow, 0, len(sec.Rows))
	for _, r := range sec.Rows {
		rows = append(rows, &pb_admin.AcctBalanceSheetRow{Code: r.Code, Name: r.Name, Balance: pbDecimalFromDecimal(r.Balance)})
	}
	return &pb_admin.AcctBalanceSheetSection{Section: sec.Section, Rows: rows, Total: pbDecimalFromDecimal(sec.Total)}
}

// ConvertAcctAccountLedgerToPb converts a drill-down ledger page to protobuf.
func ConvertAcctAccountLedgerToPb(l entity.AcctAccountLedger) *pb_admin.GetAccountLedgerResponse {
	rows := make([]*pb_admin.AcctLedgerRow, 0, len(l.Rows))
	for _, r := range l.Rows {
		row := &pb_admin.AcctLedgerRow{
			EntryId:        int32(r.EntryId),
			OccurredAt:     r.OccurredAt.Format(acctDateLayout),
			Description:    r.Description,
			SourceType:     string(r.SourceType),
			SourceKey:      r.SourceKey,
			Side:           string(r.Side),
			Amount:         pbDecimalFromDecimal(r.Amount),
			RunningBalance: pbDecimalFromDecimal(r.RunningBalance),
		}
		if r.Note.Valid {
			row.Note = r.Note.String
		}
		rows = append(rows, row)
	}
	return &pb_admin.GetAccountLedgerResponse{
		Code:           l.Code,
		Name:           l.Name,
		Section:        string(l.Section),
		OpeningBalance: pbDecimalFromDecimal(l.OpeningBalance),
		ClosingBalance: pbDecimalFromDecimal(l.ClosingBalance),
		Rows:           rows,
		Total:          int32(l.Total),
	}
}

// ConvertPbAcctLedgerFilter validates and converts GetAccountLedger's request into the account
// code (a path parameter) and the store filter; from/to are optional (empty = unbounded, so a
// drill-down can browse an account's entire history).
func ConvertPbAcctLedgerFilter(req *pb_admin.GetAccountLedgerRequest) (code string, f entity.AcctLedgerFilter, err error) {
	code = strings.ToUpper(strings.TrimSpace(req.GetCode()))
	if code == "" {
		return "", entity.AcctLedgerFilter{}, fmt.Errorf("code is required")
	}
	from, err := parseOptionalAcctDate(req.GetFrom(), "from")
	if err != nil {
		return "", entity.AcctLedgerFilter{}, err
	}
	to, err := parseOptionalAcctDate(req.GetTo(), "to")
	if err != nil {
		return "", entity.AcctLedgerFilter{}, err
	}
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return "", entity.AcctLedgerFilter{}, fmt.Errorf("to precedes from")
	}
	return code, entity.AcctLedgerFilter{From: from, To: to, Limit: int(req.GetLimit()), Offset: int(req.GetOffset())}, nil
}

// ConvertAcctReconciliationToPb converts a reconciliation report to protobuf.
func ConvertAcctReconciliationToPb(r entity.AcctReconciliation) *pb_admin.GetAcctReconciliationResponse {
	resp := &pb_admin.GetAcctReconciliationResponse{
		Revenue:           convertAcctReconBlockToPb(r.Revenue),
		Fees:              convertAcctReconBlockToPb(r.Fees),
		Cogs:              convertAcctReconBlockToPb(r.COGS),
		Materials:         convertAcctReconBlockToPb(r.Materials),
		FinishedGoods:     convertAcctReconBlockToPb(r.FinishedGoods),
		Pending:           convertAcctReconBlockToPb(r.Pending),
		UnpostedMovements: convertAcctReconBlockToPb(r.UnpostedMovements),
	}
	// Vat is a phase-2 wave-1 addition and a pointer on the entity (older reconciliations
	// may not carry it); nil stays nil on the wire.
	if r.Vat != nil {
		resp.Vat = convertAcctReconBlockToPb(*r.Vat)
	}
	return resp
}

func convertAcctReconBlockToPb(b entity.AcctReconBlock) *pb_admin.AcctReconBlock {
	items := make([]*pb_admin.AcctReconItem, 0, len(b.Items))
	for _, it := range b.Items {
		items = append(items, &pb_admin.AcctReconItem{Ref: it.Ref, Label: it.Label, Amount: pbDecimalFromDecimal(it.Amount)})
	}
	return &pb_admin.AcctReconBlock{
		Name:        b.Name,
		Ledger:      pbDecimalFromDecimal(b.Ledger),
		Operational: pbDecimalFromDecimal(b.Operational),
		Delta:       pbDecimalFromDecimal(b.Delta),
		Items:       items,
		TotalCount:  int32(b.TotalCount),
	}
}

// ConvertAcctEventsToPb converts posting-outbox events (disposition view; the internal JSON payload is
// omitted) to protobuf. Times are RFC3339; processed_at is empty while pending (H-1/H-2/B-5 queue).
func ConvertAcctEventsToPb(events []entity.AcctEvent) []*pb_admin.AcctEvent {
	out := make([]*pb_admin.AcctEvent, 0, len(events))
	for _, e := range events {
		pb := &pb_admin.AcctEvent{
			Id:          e.Id,
			EventType:   string(e.EventType),
			SourceKey:   e.SourceKey,
			OccurredAt:  e.OccurredAt.Format(time.RFC3339),
			CreatedAt:   e.CreatedAt.Format(time.RFC3339),
			Attempts:    int32(e.Attempts),
			NeedsReview: e.NeedsReview,
		}
		if e.ProcessedAt.Valid {
			pb.ProcessedAt = e.ProcessedAt.Time.Format(time.RFC3339)
		}
		if e.LastError.Valid {
			pb.LastError = e.LastError.String
		}
		out = append(out, pb)
	}
	return out
}

// =====================================================================================
// Wave 4 — money side (docs/plan-accounting-phase2/04-wave4-money.md). Revolut bank inbox (4.1) and the
// AP/AR subledgers (4.4).
// =====================================================================================

// ConvertAcctBankTxnToPb converts a stored bank inbox line to its proto shape.
func ConvertAcctBankTxnToPb(t entity.AcctBankTxn) *pb_admin.AcctBankTxn {
	pb := &pb_admin.AcctBankTxn{
		Id:          int32(t.Id),
		Source:      t.Source,
		ExternalId:  t.ExternalId,
		BookedAt:    t.BookedAt.Format(time.RFC3339),
		Amount:      pbDecimalFromDecimal(t.Amount),
		Currency:    t.Currency,
		Fee:         pbDecimalFromNull(t.Fee),
		Description: t.Description,
		State:       string(t.State),
		CreatedAt:   t.CreatedAt.Format(time.RFC3339),
	}
	if t.Counterparty.Valid {
		pb.Counterparty = t.Counterparty.String
	}
	if t.MatchedEntryId.Valid {
		pb.MatchedEntryId = int32(t.MatchedEntryId.Int64)
	}
	if t.SuggestedAccount.Valid {
		pb.SuggestedAccount = t.SuggestedAccount.String
	}
	return pb
}

// ConvertAcctBankTxnListToPb converts a page of bank inbox lines.
func ConvertAcctBankTxnListToPb(list []entity.AcctBankTxn) []*pb_admin.AcctBankTxn {
	out := make([]*pb_admin.AcctBankTxn, 0, len(list))
	for _, t := range list {
		out = append(out, ConvertAcctBankTxnToPb(t))
	}
	return out
}

// ConvertAcctBankImportResultToPb converts an import result.
func ConvertAcctBankImportResultToPb(r entity.AcctBankImportResult) *pb_admin.ImportBankCsvResponse {
	return &pb_admin.ImportBankCsvResponse{
		Parsed:   int32(r.Parsed),
		Imported: int32(r.Imported),
		Skipped:  int32(r.Skipped),
	}
}

// ConvertAcctBankRuleToPb converts a suggestion rule.
func ConvertAcctBankRuleToPb(r entity.AcctBankRule) *pb_admin.AcctBankRule {
	return &pb_admin.AcctBankRule{Id: int32(r.Id), Pattern: r.Pattern, AccountCode: r.AccountCode}
}

// ConvertAcctBankRuleListToPb converts a rule list.
func ConvertAcctBankRuleListToPb(list []entity.AcctBankRule) []*pb_admin.AcctBankRule {
	out := make([]*pb_admin.AcctBankRule, 0, len(list))
	for _, r := range list {
		out = append(out, ConvertAcctBankRuleToPb(r))
	}
	return out
}

// ConvertSupplierToPb converts a supplier.
func ConvertSupplierToPb(s entity.Supplier) *pb_admin.Supplier {
	pb := &pb_admin.Supplier{Id: int32(s.Id), Name: s.Name, CreatedAt: s.CreatedAt.Format(time.RFC3339)}
	if s.VatId.Valid {
		pb.VatId = s.VatId.String
	}
	if s.Notes.Valid {
		pb.Notes = s.Notes.String
	}
	return pb
}

// ConvertSupplierListToPb converts a supplier list.
func ConvertSupplierListToPb(list []entity.Supplier) []*pb_admin.Supplier {
	out := make([]*pb_admin.Supplier, 0, len(list))
	for _, s := range list {
		out = append(out, ConvertSupplierToPb(s))
	}
	return out
}

// ConvertPbCreateSupplier validates a create-supplier request into an insert payload.
func ConvertPbCreateSupplier(req *pb_admin.CreateSupplierRequest) (entity.SupplierInsert, error) {
	name := strings.TrimSpace(req.GetName())
	if name == "" {
		return entity.SupplierInsert{}, fmt.Errorf("name is required")
	}
	if len(name) > maxAcctName {
		return entity.SupplierInsert{}, fmt.Errorf("name must be at most %d characters", maxAcctName)
	}
	return entity.SupplierInsert{
		Name:  name,
		VatId: nullStringFromPb(strings.TrimSpace(req.GetVatId())),
		Notes: nullStringFromPb(strings.TrimSpace(req.GetNotes())),
	}, nil
}

// ConvertAcctPayableListToPb converts the payables view.
func ConvertAcctPayableListToPb(rows []entity.AcctPayableRow) []*pb_admin.AcctPayableRow {
	out := make([]*pb_admin.AcctPayableRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &pb_admin.AcctPayableRow{
			SupplierId:   int32(r.SupplierId),
			SupplierName: r.SupplierName,
			Accrued:      pbDecimalFromDecimal(r.Accrued),
			Paid:         pbDecimalFromDecimal(r.Paid),
			Balance:      pbDecimalFromDecimal(r.Balance),
		})
	}
	return out
}

// ConvertAcctReceivableListToPb converts the receivables view.
func ConvertAcctReceivableListToPb(rows []entity.AcctReceivableRow) []*pb_admin.AcctReceivableRow {
	out := make([]*pb_admin.AcctReceivableRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, &pb_admin.AcctReceivableRow{
			Ref:      r.Ref,
			Invoiced: pbDecimalFromDecimal(r.Invoiced),
			Received: pbDecimalFromDecimal(r.Received),
			Balance:  pbDecimalFromDecimal(r.Balance),
		})
	}
	return out
}
