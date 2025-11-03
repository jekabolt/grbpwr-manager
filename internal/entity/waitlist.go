package entity

import (
	"database/sql"
	"time"
)

// WaitlistEntry represents a waitlist entry for a product/size combination
type WaitlistEntry struct {
	Id        int       `db:"id"`
	ProductId int       `db:"product_id"`
	SizeId    int       `db:"size_id"`
	Email     string    `db:"email"`
	CreatedAt time.Time `db:"created_at"`
}

// WaitlistEntryWithBuyer represents a waitlist entry with buyer name information
type WaitlistEntryWithBuyer struct {
	WaitlistEntry
	FirstName sql.NullString `db:"first_name"`
	LastName  sql.NullString `db:"last_name"`
}
