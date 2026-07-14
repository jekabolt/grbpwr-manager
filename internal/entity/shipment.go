package entity

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/currency"
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
	Id int `db:"id"`
	ShipmentCarrierInsert
	Prices         []ShipmentCarrierPrice // Multi-currency prices
	AllowedRegions []string               // Regions where carrier is available; empty = global
}

type ShipmentCarrierInsert struct {
	Carrier              string         `db:"carrier"`
	TrackingURL          string         `db:"tracking_url"`
	Allowed              bool           `db:"allowed"`
	Description          string         `db:"description"`
	ExpectedDeliveryTime sql.NullString `db:"expected_delivery_time"`
	// AftershipSlug is the AfterShip courier slug used to register and poll this carrier's
	// trackings. Empty/NULL = the carrier has no tracking API, so its orders are auto-delivered
	// only by the timer safety net (AutoDeliverAfterHours), never by a real AfterShip signal.
	AftershipSlug sql.NullString `db:"aftership_slug"`
	// AutoDeliverAfterHours is the safety-net window: hours after shipment.shipping_date after
	// which an order is silently marked delivered (no customer email) when no real Delivered
	// signal arrived. 0 = use the delivery-sync worker's configured default.
	AutoDeliverAfterHours int `db:"auto_deliver_after_hours"`
}

// Trackable reports whether this carrier has an AfterShip courier slug configured, i.e. its
// shipments can be registered with and polled from AfterShip for a real delivery signal.
func (sc *ShipmentCarrier) Trackable() bool {
	return sc.AftershipSlug.Valid && strings.TrimSpace(sc.AftershipSlug.String) != ""
}

// Slug returns the trimmed AfterShip courier slug (empty when the carrier is not trackable).
func (sc *ShipmentCarrier) Slug() string {
	if !sc.AftershipSlug.Valid {
		return ""
	}
	return strings.TrimSpace(sc.AftershipSlug.String)
}

// AutoDeliverAfter returns the timer safety-net window as a duration, falling back to def when
// the carrier has no explicit positive override.
func (sc *ShipmentCarrier) AutoDeliverAfter(def time.Duration) time.Duration {
	if sc.AutoDeliverAfterHours > 0 {
		return time.Duration(sc.AutoDeliverAfterHours) * time.Hour
	}
	return def
}

// PriceDecimal returns the price for the specified currency (currency-aware rounding)
func (sc *ShipmentCarrier) PriceDecimal(c string) (decimal.Decimal, error) {
	for _, price := range sc.Prices {
		// Compare case-insensitively: stored currencies are uppercase, the client
		// value may not be (mirrors getProductPrice).
		if strings.EqualFold(price.Currency, c) {
			return currency.Round(price.Price, c), nil
		}
	}
	return decimal.Zero, fmt.Errorf("shipment carrier %d does not have a price in currency %s", sc.Id, c)
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
	FreeShipping         bool            `db:"free_shipping"`
	CreatedAt            time.Time       `db:"created_at"`
	UpdatedAt            time.Time       `db:"updated_at"`
	CarrierId            int             `db:"carrier_id"`
	TrackingCode         sql.NullString  `db:"tracking_code"`
	ShippingDate         sql.NullTime    `db:"shipping_date"`
	EstimatedArrivalDate sql.NullTime    `db:"estimated_arrival_date"`
	DeliveredAt          sql.NullTime    `db:"delivered_at"`
	// ActualCost is the real carrier invoice for this shipment (base currency EUR),
	// distinct from Cost (the price charged to the customer). NULL until an operator
	// enters it; margin analytics falls back to Cost when it is absent.
	ActualCost decimal.NullDecimal `db:"actual_cost"`
	// ReturnShippingCost is the reverse-logistics cost of a return (base currency EUR),
	// NULL when the order was not returned.
	ReturnShippingCost decimal.NullDecimal `db:"return_shipping_cost"`
}

// CostDecimal returns shipment cost with currency-aware rounding
func (s *Shipment) CostDecimal(c string) decimal.Decimal {
	return currency.Round(s.Cost, c)
}

// TrackingStatus is the normalized delivery state of a shipment as reported by the tracking
// provider (AfterShip), decoupled from the provider's raw checkpoint vocabulary.
type TrackingStatus struct {
	// Found is false when the provider has no tracking for the (slug, number) yet — the caller
	// should register it. Delivered/Tag are only meaningful when Found is true.
	Found bool
	// Delivered is true when the normalized status tag is "Delivered" (which the provider also
	// reports for pickup-point / locker collection).
	Delivered bool
	// Tag is the provider's normalized status tag (e.g. "InTransit", "OutForDelivery",
	// "Delivered", "Exception"), kept for logging and diagnostics.
	Tag string
}

// ShipmentToAutoDeliver is a shipped order the delivery-sync worker evaluates for auto-delivery.
// ShippingDate is guaranteed non-NULL by the query (populated from the auto-delivery release
// onward), so historical orders are never surfaced here. CarrierId resolves the AfterShip slug
// and timer window via cache.GetShipmentCarrierById.
type ShipmentToAutoDeliver struct {
	OrderId      int            `db:"order_id"`
	OrderUUID    string         `db:"order_uuid"`
	CarrierId    int            `db:"carrier_id"`
	TrackingCode sql.NullString `db:"tracking_code"`
	ShippingDate time.Time      `db:"shipping_date"`
}
