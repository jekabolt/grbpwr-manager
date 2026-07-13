package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

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
