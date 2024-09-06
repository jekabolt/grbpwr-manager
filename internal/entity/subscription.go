package entity

type Subscriber struct {
	Id                 int    `db:"id"`
	Email              string `db:"email"`
	ReceivePromoEmails bool   `db:"receive_promo_emails"`
}
