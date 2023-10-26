package entity

type Subscriber struct {
	ID                 int    `db:"id"`
	Name               string `db:"name"`
	Email              string `db:"email"`
	ReceivePromoEmails bool   `db:"receive_promo_emails"`
}
