package entity

import (
	"database/sql"
	"errors"
	"time"

	"github.com/shopspring/decimal"
)

// ErrOpexLineMaterialised is returned by DeleteOpexLine for a materialised line (recurring_id set):
// deleting it would only resurrect it on the worker's next tick (materialisation is insert-only per
// (recurring_id, month)), and an operator who deleted-and-re-entered the month by hand would end up
// double-counted after that tick (g25-11). To stop a recurring cost archive its template; to correct
// a booked month add a manual adjustment line (±).
var ErrOpexLineMaterialised = errors.New("a materialised OPEX line cannot be deleted; archive the template or add a manual adjustment line")

// OpexLineInsert is the writable payload of one operating-expense line (NF-08). Amount is in
// Currency; AmountBase is the base-currency equivalent, folded on write via the costing FX rates
// (NULL when the currency has no rate for the month — the line is then excluded from reports and
// flagged). Month is normalised to the 1st of the month. Label distinguishes lines within a
// month × category (a materialised line carries its template's label + RecurringId).
type OpexLineInsert struct {
	Month       time.Time           `db:"month"`
	Category    string              `db:"category"`
	Label       string              `db:"label"`
	Amount      decimal.Decimal     `db:"amount"`
	Currency    string              `db:"currency"`
	AmountBase  decimal.NullDecimal `db:"amount_base"`
	RecurringId sql.NullInt32       `db:"recurring_id"`
	Note        sql.NullString      `db:"note"`
}

// OpexLine is a stored operating-expense line.
type OpexLine struct {
	Id int `db:"id"`
	OpexLineInsert
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// OpexRecurringInsert is the writable payload of a recurring OPEX template (salary, subscription,
// rent…). A worker materialises it into monthly OpexLines from ActiveFrom to min(current month,
// ActiveTo). ActiveFrom/ActiveTo are normalised to the 1st of the month.
type OpexRecurringInsert struct {
	Label      string          `db:"label"`
	Category   string          `db:"category"`
	Amount     decimal.Decimal `db:"amount"`
	Currency   string          `db:"currency"`
	ActiveFrom time.Time       `db:"active_from"`
	ActiveTo   sql.NullTime    `db:"active_to"`
	Note       sql.NullString  `db:"note"`
	// EmployeeId links a salary template to a person in the employee registry (gap-07 v2 A); 0/NULL
	// for non-salary templates (rent, software…). ON DELETE SET NULL — removing an employee never
	// deletes booked OPEX history.
	EmployeeId sql.NullInt32 `db:"employee_id"`
}

// OpexRecurring is a stored recurring OPEX template.
type OpexRecurring struct {
	Id int `db:"id"`
	OpexRecurringInsert
	Archived  bool      `db:"archived"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

// OpexLineFilter narrows ListOpexLines. Month bounds are inclusive (normalised to the 1st).
type OpexLineFilter struct {
	MonthFrom time.Time
	MonthTo   time.Time
	Category  string // "" = any
}
