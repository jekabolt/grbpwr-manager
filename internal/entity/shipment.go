package entity

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"
)

// ShipmentCarriers represents the shipment_carrier table
type ShipmentCarrier struct {
	Id int `db:"id"`
	ShipmentCarrierInsert
}

type ShipmentCarrierInsert struct {
	Carrier     string          `db:"carrier"`
	Price       decimal.Decimal `db:"price"`
	TrackingURL string          `db:"tracking_url"`
	Allowed     bool            `db:"allowed"`
	Description string          `db:"description"`
}

func (sc ShipmentCarrierInsert) PriceDecimal() decimal.Decimal {
	return sc.Price.Round(2)
}

// Shipment represents the shipment table
type Shipment struct {
	Id                   int             `db:"id"`
	OrderId              int             `db:"order_id"`
	Cost                 decimal.Decimal `db:"cost"`
	CreatedAt            time.Time       `db:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at"`
	CarrierId            int             `db:"carrier_id"`
	TrackingCode         sql.NullString  `db:"tracking_code"`
	ShippingDate         sql.NullTime    `db:"shipping_date"`
	EstimatedArrivalDate sql.NullTime    `db:"estimated_arrival_date"`
}

func (s *Shipment) CostDecimal() decimal.Decimal {
	return s.Cost.Round(2)
}
