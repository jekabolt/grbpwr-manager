package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// EmployeeInsert is the writable payload of an employee-registry row (gap-07 v2 A). A person here is
// the counterpart to salary OpexRecurring templates (OpexRecurringInsert.EmployeeId): the registry
// holds identity + employment window + an informational default monthly cost, while the actual
// booked cost stays in the OPEX journal. DefaultMonthlyCost is a hint for pre-filling a salary
// template, never itself a cost figure in any report.
type EmployeeInsert struct {
	FullName           string              `db:"full_name"`
	Role               sql.NullString      `db:"role"` // free-text title (швея / конструктор / …)
	EmploymentStart    sql.NullTime        `db:"employment_start"`
	EmploymentEnd      sql.NullTime        `db:"employment_end"` // NULL = still employed
	DefaultCurrency    sql.NullString      `db:"default_currency"`
	DefaultMonthlyCost decimal.NullDecimal `db:"default_monthly_cost"`
	Note               sql.NullString      `db:"note"`
}

// Employee is a stored employee-registry row.
type Employee struct {
	Id int `db:"id"`
	EmployeeInsert
	Archived  bool      `db:"archived"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}
