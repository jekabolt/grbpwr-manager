package entity

import (
	"time"

	"github.com/shopspring/decimal"
)

// ShipmentCarriers represents the shipment_carrier table
type ShipmentCarrier struct {
	ID int `db:"id"`
	ShipmentCarrierInsert
}

type ShipmentCarrierInsert struct {
	Carrier string          `db:"carrier"`
	Price   decimal.Decimal `db:"price"`
	Allowed bool            `db:"allowed"`
}

// Shipment represents the shipment table
type Shipment struct {
	ID                   int       `db:"id"`
	CreatedAt            time.Time `db:"created_at"`
	UpdatedAt            time.Time `db:"updated_at"`
	CarrierID            int       `db:"carrier_id"`
	TrackingCode         string    `db:"tracking_code"`
	ShippingDate         time.Time `db:"shipping_date"`
	EstimatedArrivalDate time.Time `db:"estimated_arrival_date"`
}
