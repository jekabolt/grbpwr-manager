package entity

import "database/sql"

type Subscriber struct {
	Id                 int           `db:"id"`
	Email              string        `db:"email"`
	ReceivePromoEmails bool          `db:"receive_promo_emails"`
	CreatedAt          sql.NullTime  `db:"created_at"`
}
