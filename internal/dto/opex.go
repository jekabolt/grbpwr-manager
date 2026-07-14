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
		EmployeeId: pbToNullInt32(r.EmployeeId),
	}, nil
}

// ConvertPbEmployeeToEntity validates and converts an employee-registry insert (gap-07 v2 A).
// Length/format guards mirror the columns (g25-09) — an over-long value must be a clean
// InvalidArgument, not a strict-mode DB error surfacing as a 500.
func ConvertPbEmployeeToEntity(e *pb_admin.EmployeeInsert) (entity.EmployeeInsert, error) {
	if e == nil {
		return entity.EmployeeInsert{}, fmt.Errorf("employee is required")
	}
	name := strings.TrimSpace(e.FullName)
	if name == "" {
		return entity.EmployeeInsert{}, fmt.Errorf("employee full_name is required")
	}
	for _, f := range []struct {
		name string
		v    string
		max  int
	}{
		{"full_name", name, maxVarchar191},
		{"role", strings.TrimSpace(e.Role), maxVarchar64},
		{"note", strings.TrimSpace(e.Note), maxVarchar255},
	} {
		if len(f.v) > f.max {
			return entity.EmployeeInsert{}, fmt.Errorf("employee %s must be at most %d characters", f.name, f.max)
		}
	}
	currency := normalizeCurrency(e.DefaultCurrency)
	if currency != "" && len(currency) != maxCurrency {
		return entity.EmployeeInsert{}, fmt.Errorf("employee default_currency must be a 3-letter ISO 4217 code")
	}
	start, err := parseOptionalDate(e.EmploymentStart, "employment_start")
	if err != nil {
		return entity.EmployeeInsert{}, err
	}
	end, err := parseOptionalDate(e.EmploymentEnd, "employment_end")
	if err != nil {
		return entity.EmployeeInsert{}, err
	}
	if start.Valid && end.Valid && end.Time.Before(start.Time) {
		return entity.EmployeeInsert{}, fmt.Errorf("employment_end precedes employment_start")
	}
	var cost decimal.NullDecimal
	if e.DefaultMonthlyCost != nil && strings.TrimSpace(e.DefaultMonthlyCost.Value) != "" {
		d, err := parseOpexAmount(e.DefaultMonthlyCost.Value)
		if err != nil {
			return entity.EmployeeInsert{}, fmt.Errorf("default_monthly_cost: %w", err)
		}
		cost = decimal.NullDecimal{Decimal: d, Valid: true}
	}
	return entity.EmployeeInsert{
		FullName:           name,
		Role:               trimmedNullString(e.Role),
		EmploymentStart:    start,
		EmploymentEnd:      end,
		DefaultCurrency:    trimmedNullString(currency),
		DefaultMonthlyCost: cost,
		Note:               trimmedNullString(e.Note),
	}, nil
}

// EmployeeToPb converts a stored employee to protobuf.
func EmployeeToPb(e entity.Employee) *pb_admin.Employee {
	ins := &pb_admin.EmployeeInsert{FullName: e.FullName}
	if e.Role.Valid {
		ins.Role = e.Role.String
	}
	if e.EmploymentStart.Valid {
		ins.EmploymentStart = e.EmploymentStart.Time.Format("2006-01-02")
	}
	if e.EmploymentEnd.Valid {
		ins.EmploymentEnd = e.EmploymentEnd.Time.Format("2006-01-02")
	}
	if e.DefaultCurrency.Valid {
		ins.DefaultCurrency = e.DefaultCurrency.String
	}
	if e.DefaultMonthlyCost.Valid {
		ins.DefaultMonthlyCost = pbDecimalFromDecimal(e.DefaultMonthlyCost.Decimal)
	}
	if e.Note.Valid {
		ins.Note = e.Note.String
	}
	return &pb_admin.Employee{Id: int32(e.Id), Employee: ins, Archived: e.Archived}
}

// EmployeeListToPb converts a slice of stored employees to protobuf.
func EmployeeListToPb(list []entity.Employee) []*pb_admin.Employee {
	out := make([]*pb_admin.Employee, 0, len(list))
	for _, e := range list {
		out = append(out, EmployeeToPb(e))
	}
	return out
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
		EmployeeId: nullInt32ToPb(r.EmployeeId),
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

// pbToNullInt32 maps a proto id (0 = none) to a nullable int32.
func pbToNullInt32(v int32) sql.NullInt32 {
	return sql.NullInt32{Int32: v, Valid: v > 0}
}

// parseOptionalDate parses an optional YYYY-MM-DD date (empty → NULL), keeping the exact day (not
// month-snapped, unlike OPEX dates).
func parseOptionalDate(s, field string) (sql.NullTime, error) {
	if strings.TrimSpace(s) == "" {
		return sql.NullTime{}, nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		return sql.NullTime{}, fmt.Errorf("invalid %s %q: %w", field, s, err)
	}
	return sql.NullTime{Time: d, Valid: true}, nil
}
