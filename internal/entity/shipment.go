package entity

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// ShippingRegion represents a geographic region for carrier availability
type ShippingRegion string

const (
	ShippingRegionAfrica      ShippingRegion = "AFRICA"
	ShippingRegionAmericas    ShippingRegion = "AMERICAS"
	ShippingRegionAsiaPacific ShippingRegion = "ASIA PACIFIC"
	ShippingRegionEurope      ShippingRegion = "EUROPE"
	ShippingRegionMiddleEast  ShippingRegion = "MIDDLE EAST"
)

var validShippingRegions = map[ShippingRegion]bool{
	ShippingRegionAfrica:      true,
	ShippingRegionAmericas:    true,
	ShippingRegionAsiaPacific: true,
	ShippingRegionEurope:      true,
	ShippingRegionMiddleEast:  true,
}

// IsValidShippingRegion returns true if the region is valid
func IsValidShippingRegion(r string) bool {
	return validShippingRegions[ShippingRegion(r)]
}

// countryToRegion maps ISO 3166-1 alpha-2 country codes to shipping regions
var countryToRegion = map[string]ShippingRegion{
	// Africa
	"DZ": ShippingRegionAfrica, "AO": ShippingRegionAfrica, "BJ": ShippingRegionAfrica, "BW": ShippingRegionAfrica,
	"BF": ShippingRegionAfrica, "BI": ShippingRegionAfrica, "CV": ShippingRegionAfrica, "CM": ShippingRegionAfrica,
	"CF": ShippingRegionAfrica, "TD": ShippingRegionAfrica, "KM": ShippingRegionAfrica, "CG": ShippingRegionAfrica,
	"CD": ShippingRegionAfrica, "DJ": ShippingRegionAfrica, "EG": ShippingRegionAfrica, "GQ": ShippingRegionAfrica,
	"ER": ShippingRegionAfrica, "SZ": ShippingRegionAfrica, "ET": ShippingRegionAfrica, "GA": ShippingRegionAfrica,
	"GM": ShippingRegionAfrica, "GH": ShippingRegionAfrica, "GN": ShippingRegionAfrica, "GW": ShippingRegionAfrica,
	"CI": ShippingRegionAfrica, "KE": ShippingRegionAfrica, "LS": ShippingRegionAfrica, "LR": ShippingRegionAfrica,
	"LY": ShippingRegionAfrica, "MG": ShippingRegionAfrica, "MW": ShippingRegionAfrica, "ML": ShippingRegionAfrica,
	"MR": ShippingRegionAfrica, "MU": ShippingRegionAfrica, "MA": ShippingRegionAfrica, "MZ": ShippingRegionAfrica,
	"NA": ShippingRegionAfrica, "NE": ShippingRegionAfrica, "NG": ShippingRegionAfrica, "RW": ShippingRegionAfrica,
	"ST": ShippingRegionAfrica, "SN": ShippingRegionAfrica, "SC": ShippingRegionAfrica, "SL": ShippingRegionAfrica,
	"SO": ShippingRegionAfrica, "ZA": ShippingRegionAfrica, "SS": ShippingRegionAfrica, "SD": ShippingRegionAfrica,
	"TZ": ShippingRegionAfrica, "TG": ShippingRegionAfrica, "TN": ShippingRegionAfrica, "UG": ShippingRegionAfrica,
	"ZM": ShippingRegionAfrica, "ZW": ShippingRegionAfrica,
	// Americas
	"AG": ShippingRegionAmericas, "AR": ShippingRegionAmericas, "BS": ShippingRegionAmericas, "BB": ShippingRegionAmericas,
	"BZ": ShippingRegionAmericas, "BO": ShippingRegionAmericas, "BR": ShippingRegionAmericas, "CA": ShippingRegionAmericas,
	"CL": ShippingRegionAmericas, "CO": ShippingRegionAmericas, "CR": ShippingRegionAmericas, "CU": ShippingRegionAmericas,
	"DM": ShippingRegionAmericas, "DO": ShippingRegionAmericas, "EC": ShippingRegionAmericas, "SV": ShippingRegionAmericas,
	"GD": ShippingRegionAmericas, "GT": ShippingRegionAmericas, "HT": ShippingRegionAmericas, "HN": ShippingRegionAmericas,
	"JM": ShippingRegionAmericas, "MX": ShippingRegionAmericas, "NI": ShippingRegionAmericas, "PA": ShippingRegionAmericas,
	"PY": ShippingRegionAmericas, "PE": ShippingRegionAmericas, "KN": ShippingRegionAmericas, "LC": ShippingRegionAmericas,
	"VC": ShippingRegionAmericas, "SR": ShippingRegionAmericas, "TT": ShippingRegionAmericas, "US": ShippingRegionAmericas,
	"UY": ShippingRegionAmericas, "VE": ShippingRegionAmericas,
	// Asia Pacific
	"AF": ShippingRegionAsiaPacific, "AU": ShippingRegionAsiaPacific, "BD": ShippingRegionAsiaPacific, "BT": ShippingRegionAsiaPacific,
	"BN": ShippingRegionAsiaPacific, "KH": ShippingRegionAsiaPacific, "CN": ShippingRegionAsiaPacific, "FJ": ShippingRegionAsiaPacific,
	"IN": ShippingRegionAsiaPacific, "ID": ShippingRegionAsiaPacific, "JP": ShippingRegionAsiaPacific, "KZ": ShippingRegionAsiaPacific,
	"KI": ShippingRegionAsiaPacific, "KP": ShippingRegionAsiaPacific, "KR": ShippingRegionAsiaPacific, "KG": ShippingRegionAsiaPacific,
	"LA": ShippingRegionAsiaPacific, "MY": ShippingRegionAsiaPacific, "MV": ShippingRegionAsiaPacific, "MH": ShippingRegionAsiaPacific,
	"FM": ShippingRegionAsiaPacific, "MN": ShippingRegionAsiaPacific, "MM": ShippingRegionAsiaPacific, "NR": ShippingRegionAsiaPacific,
	"NP": ShippingRegionAsiaPacific, "NZ": ShippingRegionAsiaPacific, "PK": ShippingRegionAsiaPacific, "PW": ShippingRegionAsiaPacific,
	"PG": ShippingRegionAsiaPacific, "PH": ShippingRegionAsiaPacific, "WS": ShippingRegionAsiaPacific, "SB": ShippingRegionAsiaPacific,
	"SG": ShippingRegionAsiaPacific, "LK": ShippingRegionAsiaPacific, "TW": ShippingRegionAsiaPacific, "TJ": ShippingRegionAsiaPacific,
	"TH": ShippingRegionAsiaPacific, "TL": ShippingRegionAsiaPacific, "TO": ShippingRegionAsiaPacific, "TM": ShippingRegionAsiaPacific,
	"TV": ShippingRegionAsiaPacific, "UZ": ShippingRegionAsiaPacific, "VU": ShippingRegionAsiaPacific, "VN": ShippingRegionAsiaPacific,
	// Europe
	"AL": ShippingRegionEurope, "AD": ShippingRegionEurope, "AT": ShippingRegionEurope, "BY": ShippingRegionEurope,
	"BE": ShippingRegionEurope, "BA": ShippingRegionEurope, "BG": ShippingRegionEurope, "HR": ShippingRegionEurope,
	"CY": ShippingRegionEurope, "CZ": ShippingRegionEurope, "DK": ShippingRegionEurope, "EE": ShippingRegionEurope,
	"FI": ShippingRegionEurope, "FR": ShippingRegionEurope, "DE": ShippingRegionEurope, "GR": ShippingRegionEurope,
	"HU": ShippingRegionEurope, "IS": ShippingRegionEurope, "IE": ShippingRegionEurope, "IT": ShippingRegionEurope,
	"LV": ShippingRegionEurope, "LI": ShippingRegionEurope, "LT": ShippingRegionEurope, "LU": ShippingRegionEurope,
	"MT": ShippingRegionEurope, "MD": ShippingRegionEurope, "MC": ShippingRegionEurope, "ME": ShippingRegionEurope,
	"NL": ShippingRegionEurope, "MK": ShippingRegionEurope, "NO": ShippingRegionEurope, "PL": ShippingRegionEurope,
	"PT": ShippingRegionEurope, "RO": ShippingRegionEurope, "RU": ShippingRegionEurope, "SM": ShippingRegionEurope,
	"RS": ShippingRegionEurope, "SK": ShippingRegionEurope, "SI": ShippingRegionEurope, "ES": ShippingRegionEurope,
	"SE": ShippingRegionEurope, "CH": ShippingRegionEurope, "UA": ShippingRegionEurope, "GB": ShippingRegionEurope,
	"VA": ShippingRegionEurope,
	// Middle East
	"BH": ShippingRegionMiddleEast, "IR": ShippingRegionMiddleEast, "IQ": ShippingRegionMiddleEast, "IL": ShippingRegionMiddleEast,
	"JO": ShippingRegionMiddleEast, "KW": ShippingRegionMiddleEast, "LB": ShippingRegionMiddleEast, "OM": ShippingRegionMiddleEast,
	"PS": ShippingRegionMiddleEast, "QA": ShippingRegionMiddleEast, "SA": ShippingRegionMiddleEast, "SY": ShippingRegionMiddleEast,
	"TR": ShippingRegionMiddleEast, "AE": ShippingRegionMiddleEast, "YE": ShippingRegionMiddleEast,
}

// CountryToRegion returns the shipping region for an ISO 3166-1 alpha-2 country code
func CountryToRegion(countryCode string) (ShippingRegion, bool) {
	if countryCode == "" {
		return "", false
	}
	cc := strings.ToUpper(strings.TrimSpace(countryCode))
	r, ok := countryToRegion[cc]
	return r, ok
}

// ShipmentCarrierPrice represents a shipment carrier price in a specific currency
type ShipmentCarrierPrice struct {
	Id                int             `db:"id"`
	ShipmentCarrierId int             `db:"shipment_carrier_id"`
	Currency          string          `db:"currency"`
	Price             decimal.Decimal `db:"price"`
	CreatedAt         time.Time       `db:"created_at"`
	UpdatedAt         time.Time       `db:"updated_at"`
}

// ShipmentCarrier represents the shipment_carrier table
type ShipmentCarrier struct {
	Id             int `db:"id"`
	ShipmentCarrierInsert
	Prices         []ShipmentCarrierPrice // Multi-currency prices
	AllowedRegions []string              // Regions where carrier is available; empty = global
}

type ShipmentCarrierInsert struct {
	Carrier              string         `db:"carrier"`
	TrackingURL          string         `db:"tracking_url"`
	Allowed              bool           `db:"allowed"`
	Description          string         `db:"description"`
	ExpectedDeliveryTime sql.NullString `db:"expected_delivery_time"`
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

// AvailableForRegion returns true if the carrier serves the given region.
// Empty AllowedRegions means global (available everywhere).
func (sc *ShipmentCarrier) AvailableForRegion(region ShippingRegion) bool {
	if len(sc.AllowedRegions) == 0 {
		return true
	}
	for _, r := range sc.AllowedRegions {
		if ShippingRegion(r) == region {
			return true
		}
	}
	return false
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
