package entity

type Subscriber struct {
	ID                 int    `db:"id"`
	Email              string `db:"email"`
	ReceivePromoEmails bool   `db:"receive_promo_emails"`
}
