package entity

// Address represents the address table
type Address struct {
	ID int `db:"id"`
	AddressInsert
}

type AddressInsert struct {
	Street          string `db:"street"`
	HouseNumber     string `db:"house_number"`
	ApartmentNumber string `db:"apartment_number"`
	City            string `db:"city"`
	State           string `db:"state"`
	Country         string `db:"country"`
	PostalCode      string `db:"postal_code"`
}

// Buyer represents the buyer table
type Buyer struct {
	ID                int `db:"id"`
	BillingAddressID  int `db:"billing_address_id"`
	ShippingAddressID int `db:"shipping_address_id"`
	BuyerInsert
}

type BuyerInsert struct {
	FirstName          string `db:"first_name"`
	LastName           string `db:"last_name"`
	Email              string `db:"email"`
	Phone              string `db:"phone"`
	ReceivePromoEmails bool   `db:"receive_promo_emails"`
}
