package entity

import "database/sql"

// Address represents the address table
type Address struct {
	ID int `db:"id"`
	AddressInsert
}

type AddressInsert struct {
	Street          string `db:"street" valid:"required"`
	HouseNumber     string `db:"house_number" valid:"required"`
	ApartmentNumber string `db:"apartment_number" valid:"-"`
	City            string `db:"city" valid:"required"`
	State           string `db:"state" valid:"required"`
	Country         string `db:"country" valid:"required"`
	PostalCode      string `db:"postal_code" valid:"required"`
}

// Buyer represents the buyer table
type Buyer struct {
	ID                int `db:"id"`
	BillingAddressID  int `db:"billing_address_id"`
	ShippingAddressID int `db:"shipping_address_id"`
	BuyerInsert
}

type BuyerInsert struct {
	FirstName          string       `db:"first_name" valid:"required"`
	LastName           string       `db:"last_name" valid:"required"`
	Email              string       `db:"email" valid:"required,email"`
	Phone              string       `db:"phone" valid:"required"`
	ReceivePromoEmails sql.NullBool `db:"receive_promo_emails"`
}
