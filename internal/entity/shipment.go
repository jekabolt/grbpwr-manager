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
	// Carrier-generated shipping label (Sendcloud). All NULL until GenerateShippingLabel succeeds;
	// a manually-entered tracking number leaves them NULL. label_service_type holds the Sendcloud
	// shipping_option_code; carrier_shipment_id holds the Sendcloud parcel id.
	LabelURL          sql.NullString `db:"label_url"`
	CarrierShipmentID sql.NullString `db:"carrier_shipment_id"`
	LabelServiceType  sql.NullString `db:"label_service_type"`
	LabelCreatedAt    sql.NullTime   `db:"label_created_at"`
	ParcelWeightGrams sql.NullInt32  `db:"parcel_weight_grams"`
	ParcelDimensions  sql.NullString `db:"parcel_dimensions"`
}

// HasLabel reports whether a carrier label has already been generated for this shipment. Used
// as the idempotency guard so GenerateShippingLabel never issues a second CreateLabel (which
// would double-charge the carrier account).
func (s *Shipment) HasLabel() bool {
	return s.CarrierShipmentID.Valid && strings.TrimSpace(s.CarrierShipmentID.String) != ""
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

// ErrLabelsDisabled is returned by the disabled no-op LabelProvider when no Sendcloud API keys
// are configured, so callers can surface a clear "labels not configured" state.
var ErrLabelsDisabled = fmt.Errorf("shipping label provider is disabled (no api keys configured)")

// CarrierValidationError wraps a 4xx validation rejection from the carrier (e.g. an invalid
// destination postal code, or missing HS code). Detail is the carrier's own human-facing message,
// which is safe operational data (a field name + reason, not customer PII) — the handler surfaces it
// to the operator as FailedPrecondition so a bad address can be fixed, rather than a generic 500.
type CarrierValidationError struct {
	Detail string
}

func (e *CarrierValidationError) Error() string { return e.Detail }

// LabelAddress is one endpoint of a shipping label. CountryISO2 is an ISO 3166-1 alpha-2 code
// (Sendcloud requires alpha-2). Residential is informational (Sendcloud has no address type).
type LabelAddress struct {
	ContactName string
	Company     string
	Street1     string
	HouseNumber string // Sendcloud splits address_line_1 / house_number; empty is tolerated
	Street2     string
	City        string
	State       string
	PostalCode  string
	CountryISO2 string
	Phone       string
	Email       string
	Residential bool
}

// LabelParcel is the physical parcel a label is generated for. Weight is in grams and dimensions
// in whole centimetres; the client converts to the provider's units (kg / cm). Zero dimensions
// are omitted. BoxType is retained for the UI but not sent to Sendcloud.
type LabelParcel struct {
	WeightGrams int
	LengthCM    int
	WidthCM     int
	HeightCM    int
	BoxType     string
}

// LabelCustomsItem / LabelCustoms carry the international customs declaration for cross-border
// (non-intra-EU) shipments. OriginISO2 is an ISO 3166-1 alpha-2 code. Sendcloud auto-generates the
// customs documents from the parcel_items built from these.
type LabelCustomsItem struct {
	Description   string
	Quantity      int
	PriceAmount   decimal.Decimal
	PriceCurrency string
	WeightGrams   int
	HSCode        string
	OriginISO2    string
	SKU           string
}

type LabelCustoms struct {
	Purpose string // Sendcloud parcel_items shipment reason, e.g. "merchandise"
	Items   []LabelCustomsItem
}

// LabelRequest is the provider-agnostic input to LabelProvider.CreateLabel. ShipFrom is resolved
// from configuration by the caller (the warehouse origin). References carries the order UUID.
// ShippingOptionCode is the Sendcloud shipping_option_code; empty means "let Sendcloud shipping
// rules pick the carrier/contract".
type LabelRequest struct {
	ShippingOptionCode string
	ShipFrom           LabelAddress
	ShipTo             LabelAddress
	Parcel             LabelParcel
	References         []string
	Customs            *LabelCustoms
}

// OptionsRequest is the provider-agnostic input to LabelProvider.GetShippingOptions: fetch the
// shipping options (carrier + service + quote) available for a parcel. ShipFrom is the warehouse
// origin; ShipTo is the order destination.
type OptionsRequest struct {
	ShipFrom LabelAddress
	ShipTo   LabelAddress
	Parcel   LabelParcel
}

// ShippingOption is one quoted service option returned by GetShippingOptions. Code is the
// shipping_option_code passed back to CreateLabel to select this option. TotalCharge/Currency and
// TransitDays/DeliveryDate are best-effort (zero when Sendcloud does not return a quote).
type ShippingOption struct {
	Code         string
	CarrierCode  string
	CarrierName  string
	ProductName  string
	TotalCharge  decimal.Decimal
	Currency     string
	TransitDays  int
	DeliveryDate string
}

// LabelResult is the normalized output of a successful CreateLabel: the carrier tracking number,
// the decoded label PDF bytes (Sendcloud returns the label inline as base64), the provider parcel
// id (stored for void and idempotency), the resolved carrier + shipping_option_code, and status.
type LabelResult struct {
	TrackingNumber     string
	LabelPDF           []byte
	CarrierShipmentID  string
	CarrierCode        string
	CarrierName        string
	ShippingOptionCode string
	Status             string
}

// ShipmentLabel is the persisted result of a generated label, written to the shipment row by
// SetShipmentLabel. ServiceType holds the Sendcloud shipping_option_code; ParcelDimensions is the
// free-text "LxWxH cm" actually sent to the carrier.
type ShipmentLabel struct {
	LabelURL          string
	CarrierShipmentID string
	ServiceType       string
	ParcelWeightGrams int
	ParcelDimensions  string
}

// PickupRequest schedules a carrier pickup (Sendcloud's end-of-day handover equivalent — there is
// no generic manifest API in v3). Address is the warehouse origin; Date is the pickup day
// (YYYY-MM-DD); CarrierCode is the Sendcloud carrier to collect. Quantity is the parcel count.
type PickupRequest struct {
	Address     LabelAddress
	CarrierCode string
	Date        string
	FromTime    string
	ToTime      string
	Quantity    int
}

// PickupResult is the normalized output of a scheduled pickup.
type PickupResult struct {
	PickupID  string
	Confirmed bool
	Message   string
}

// OrderItemParcel is one order line's packaging + customs data, joined from the product and its
// primary tech card (order_item -> product.primary_tech_card_id -> tech_card_packaging). WeightGross
// and BoxDimensions are NULL when the product has no primary tech card or no packaging spec; the
// caller then flags the parcel as incomplete and requires a manual weight/box override. The SKU,
// price and customs fields feed an international label's customs declaration.
type OrderItemParcel struct {
	ProductId            int             `db:"product_id"`
	Quantity             decimal.Decimal `db:"quantity"`
	WeightGrossGrams     sql.NullInt32   `db:"weight_gross_grams"`
	BoxDimensions        sql.NullString  `db:"box_dimensions"`
	SKU                  string          `db:"sku"`
	ProductPriceWithSale decimal.Decimal `db:"product_price_with_sale"`
	HSCode               sql.NullString  `db:"hs_code"`
	CountryOfOrigin      sql.NullString  `db:"country_of_origin"`
	CustomsDescription   sql.NullString  `db:"customs_description"`
}
