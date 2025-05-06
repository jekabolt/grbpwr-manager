package entity

import (
	"database/sql"
	"time"
)

type SupportTicket struct {
	Id         int          `db:"id"`
	CreatedAt  time.Time    `db:"created_at"`
	UpdatedAt  time.Time    `db:"updated_at"`
	Status     bool         `db:"status"`
	ResolvedAt sql.NullTime `db:"resolved_at"`
	SupportTicketInsert
}

type SupportTicketInsert struct {
	Topic          string `db:"topic"`
	Subject        string `db:"subject"`
	Civility       string `db:"civility"`
	Email          string `db:"email"`
	FirstName      string `db:"first_name"`
	LastName       string `db:"last_name"`
	OrderReference string `db:"order_reference"`
	Notes          string `db:"notes"`
}
