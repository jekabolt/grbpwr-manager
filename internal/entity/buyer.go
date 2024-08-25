package entity

import "database/sql"

// Address represents the address table
type Address struct {
	ID int `db:"id"`
	AddressInsert
}

type AddressInsert struct {
	OrderId        int            `db:"order_id"`
	Country        string         `db:"country" valid:"required"`
	State          sql.NullString `db:"state" valid:"-"`
	City           string         `db:"city" valid:"required"`
	AddressLineOne string         `db:"address_line_one" valid:"required"`
	AddressLineTwo sql.NullString `db:"address_line_two" valid:"-"`
	Company        sql.NullString `db:"company" valid:"-"`
	PostalCode     string         `db:"postal_code" valid:"required"`
}

// Buyer represents the buyer table
type Buyer struct {
	ID                int `db:"id"`
	BillingAddressID  int `db:"billing_address_id"`
	ShippingAddressID int `db:"shipping_address_id"`
	BuyerInsert
}

type BuyerInsert struct {
	OrderId            int          `db:"order_id"`
	FirstName          string       `db:"first_name" valid:"required"`
	LastName           string       `db:"last_name" valid:"required"`
	Email              string       `db:"email" valid:"required,email"`
	Phone              string       `db:"phone" valid:"required"`
	ReceivePromoEmails sql.NullBool `db:"receive_promo_emails"`
}
