package dto

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	"github.com/shopspring/decimal"
)

// NF-08 OPEX v2 dto conversions: line items (opex_line) and recurring templates (opex_recurring).
// Amounts arrive in their own currency; AmountBase is folded in the handler via FoldOpexLinesToBase
// (dto has no FX rates of its own), mirroring FoldProductionRunCostsToBase.

// firstOfMonth parses an any-day-in-month YYYY-MM-DD string and snaps it to the 1st at 00:00 UTC.
func firstOfMonth(s, field string) (time.Time, error) {
	m, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid %s %q: %w", field, s, err)
	}
	return time.Date(m.Year(), m.Month(), 1, 0, 0, 0, 0, time.UTC), nil
}

// parseOpexAmount reads a proto decimal into a non-negative rounded amount (zero when absent).
func parseOpexAmount(v string) (decimal.Decimal, error) {
	if strings.TrimSpace(v) == "" {
		return decimal.Zero, nil
	}
	d, err := decimal.NewFromString(v)
	if err != nil {
		return decimal.Zero, fmt.Errorf("invalid opex amount %q: %w", v, err)
	}
	if d.IsNegative() {
		return decimal.Zero, fmt.Errorf("opex amount must be >= 0")
	}
	return d.Round(2), nil
}

// ConvertPbOpexLinesToEntity validates and converts OPEX line inserts. AmountBase is left unset —
// the handler folds it via FoldOpexLinesToBase using the costing FX rates.
func ConvertPbOpexLinesToEntity(list []*pb_admin.OpexLineInsert) ([]entity.OpexLineInsert, error) {
	out := make([]entity.OpexLineInsert, 0, len(list))
	for _, e := range list {
		if e == nil {
			continue
		}
		month, err := firstOfMonth(e.Month, "opex month")
		if err != nil {
			return nil, err
		}
		category := strings.TrimSpace(e.Category)
		if _, ok := entity.ValidOpexCategories[category]; !ok {
			return nil, fmt.Errorf("invalid opex category %q", category)
		}
		label := strings.TrimSpace(e.Label)
		if label == "" {
			return nil, fmt.Errorf("opex line label is required")
		}
		var amountVal string
		if e.Amount != nil {
			amountVal = e.Amount.Value
		}
		amount, err := parseOpexAmount(amountVal)
		if err != nil {
			return nil, err
		}
		out = append(out, entity.OpexLineInsert{
			Month:    month,
			Category: category,
			Label:    label,
			Amount:   amount,
			Currency: normalizeCurrency(e.Currency),
			Note:     trimmedNullString(e.Note),
		})
	}
	return out, nil
}

// ConvertPbOpexRecurringToEntity validates and converts one recurring OPEX template insert.
func ConvertPbOpexRecurringToEntity(r *pb_admin.OpexRecurringInsert) (entity.OpexRecurringInsert, error) {
	if r == nil {
		return entity.OpexRecurringInsert{}, fmt.Errorf("recurring template is required")
	}
	label := strings.TrimSpace(r.Label)
	if label == "" {
		return entity.OpexRecurringInsert{}, fmt.Errorf("recurring template label is required")
	}
	category := strings.TrimSpace(r.Category)
	if _, ok := entity.ValidOpexCategories[category]; !ok {
		return entity.OpexRecurringInsert{}, fmt.Errorf("invalid opex category %q", category)
	}
	var amountVal string
	if r.Amount != nil {
		amountVal = r.Amount.Value
	}
	amount, err := parseOpexAmount(amountVal)
	if err != nil {
		return entity.OpexRecurringInsert{}, err
	}
	activeFrom, err := firstOfMonth(r.ActiveFrom, "opex active_from")
	if err != nil {
		return entity.OpexRecurringInsert{}, err
	}
	activeTo := sql.NullTime{}
	if strings.TrimSpace(r.ActiveTo) != "" {
		to, err := firstOfMonth(r.ActiveTo, "opex active_to")
		if err != nil {
			return entity.OpexRecurringInsert{}, err
		}
		if to.Before(activeFrom) {
			return entity.OpexRecurringInsert{}, fmt.Errorf("opex active_to precedes active_from")
		}
		activeTo = sql.NullTime{Time: to, Valid: true}
	}
	return entity.OpexRecurringInsert{
		Label:      label,
		Category:   category,
		Amount:     amount,
		Currency:   normalizeCurrency(r.Currency),
		ActiveFrom: activeFrom,
		ActiveTo:   activeTo,
		Note:       trimmedNullString(r.Note),
	}, nil
}

// FoldOpexLinesToBase fills each line's AmountBase by folding Amount from its currency into base via
// the costing FX rates. A line whose currency has no rate is left uncosted (AmountBase invalid) and
// excluded from the operating result, mirroring FoldProductionRunCostsToBase.
func FoldOpexLinesToBase(lines []entity.OpexLineInsert, fx CostingFx) {
	for i := range lines {
		if lines[i].AmountBase.Valid {
			continue
		}
		if base, ok := fx.toBase(lines[i].Amount, lines[i].Currency); ok {
			lines[i].AmountBase = decimal.NullDecimal{Decimal: roundMoney(base), Valid: true}
		}
	}
}

// ConvertOpexLineFilter builds the store filter from a list request (inclusive month bounds).
func ConvertOpexLineFilter(req *pb_admin.ListOpexLinesRequest) (entity.OpexLineFilter, error) {
	from, err := firstOfMonth(req.MonthFrom, "opex month_from")
	if err != nil {
		return entity.OpexLineFilter{}, err
	}
	to, err := firstOfMonth(req.MonthTo, "opex month_to")
	if err != nil {
		return entity.OpexLineFilter{}, err
	}
	if to.Before(from) {
		return entity.OpexLineFilter{}, fmt.Errorf("opex month_to precedes month_from")
	}
	return entity.OpexLineFilter{MonthFrom: from, MonthTo: to, Category: strings.TrimSpace(req.Category)}, nil
}

// OpexLineToPb converts a stored OPEX line to protobuf.
func OpexLineToPb(l entity.OpexLine) *pb_admin.OpexLine {
	pb := &pb_admin.OpexLine{
		Id:          int32(l.Id),
		Month:       l.Month.Format("2006-01-02"),
		Category:    l.Category,
		Label:       l.Label,
		Amount:      pbDecimalFromDecimal(l.Amount),
		Currency:    l.Currency,
		AmountBase:  pbDecimalFromNull(l.AmountBase),
		Costed:      l.AmountBase.Valid,
		RecurringId: nullInt32ToPb(l.RecurringId),
	}
	if l.Note.Valid {
		pb.Note = l.Note.String
	}
	return pb
}

// OpexLinesToPb converts a slice of stored OPEX lines to protobuf.
func OpexLinesToPb(lines []entity.OpexLine) []*pb_admin.OpexLine {
	out := make([]*pb_admin.OpexLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, OpexLineToPb(l))
	}
	return out
}

// OpexRecurringToPb converts a stored recurring template to protobuf.
func OpexRecurringToPb(r entity.OpexRecurring) *pb_admin.OpexRecurring {
	ins := &pb_admin.OpexRecurringInsert{
		Label:      r.Label,
		Category:   r.Category,
		Amount:     pbDecimalFromDecimal(r.Amount),
		Currency:   r.Currency,
		ActiveFrom: r.ActiveFrom.Format("2006-01-02"),
	}
	if r.ActiveTo.Valid {
		ins.ActiveTo = r.ActiveTo.Time.Format("2006-01-02")
	}
	if r.Note.Valid {
		ins.Note = r.Note.String
	}
	return &pb_admin.OpexRecurring{
		Id:        int32(r.Id),
		Recurring: ins,
		Archived:  r.Archived,
	}
}

// OpexRecurringListToPb converts a slice of recurring templates to protobuf.
func OpexRecurringListToPb(list []entity.OpexRecurring) []*pb_admin.OpexRecurring {
	out := make([]*pb_admin.OpexRecurring, 0, len(list))
	for _, r := range list {
		out = append(out, OpexRecurringToPb(r))
	}
	return out
}

// normalizeCurrency upper-cases a trimmed ISO currency code (empty stays empty → base currency).
func normalizeCurrency(c string) string {
	return strings.ToUpper(strings.TrimSpace(c))
}

// trimmedNullString wraps a trimmed non-empty string into a valid sql.NullString.
func trimmedNullString(s string) sql.NullString {
	if t := strings.TrimSpace(s); t != "" {
		return sql.NullString{String: t, Valid: true}
	}
	return sql.NullString{}
}

// nullInt32ToPb returns the int32 value or 0 when unset.
func nullInt32ToPb(n sql.NullInt32) int32 {
	if n.Valid {
		return n.Int32
	}
	return 0
}
