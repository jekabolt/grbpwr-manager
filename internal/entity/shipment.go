package entity

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// ShipmentCarrierPrice represents a shipment carrier price in a specific currency
type ShipmentCarrierPrice struct {
	Id                int             `db:"id"`
	ShipmentCarrierId int             `db:"shipment_carrier_id"`
	Currency          string          `db:"currency"`
	Price             decimal.Decimal `db:"price"`
	CreatedAt         time.Time       `db:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
}

// ShipmentCarriers represents the shipment_carrier table
type ShipmentCarrier struct {
	Id int `db:"id"`
	ShipmentCarrierInsert
	Prices []ShipmentCarrierPrice // Multi-currency prices
}

type ShipmentCarrierInsert struct {
	Carrier     string `db:"carrier"`
	TrackingURL string `db:"tracking_url"`
	Allowed     bool   `db:"allowed"`
	Description string `db:"description"`
}

// PriceDecimal returns the price for the specified currency
// Returns an error if the currency is not found
func (sc *ShipmentCarrier) PriceDecimal(currency string) (decimal.Decimal, error) {
	for _, price := range sc.Prices {
		if price.Currency == currency {
			return price.Price.Round(2), nil
		}
	}
	return decimal.Zero, fmt.Errorf("shipment carrier %d does not have a price in currency %s", sc.Id, currency)
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
