// Package shippinglabel is the external shipping-label client (Sendcloud API v3) behind
// dependency.LabelProvider. It fetches shipping options, announces a shipment (creating a carrier
// tracking number + label), cancels an announced parcel, and schedules a pickup. Sendcloud returns
// the label inline as base64, so CreateLabel returns the decoded PDF bytes directly. A disabled
// no-op impl is returned when no API keys are configured, so callers fall back to manual
// tracking-number entry.
//
// Sendcloud v3 specifics: Basic Auth with a public/secret key pair; ISO 3166-1 alpha-2 country
// codes; weight in kg; single-collo synchronous announce returns the label instantly. Endpoints
// (relative to baseURL): POST /fetch-shipping-options, POST /shipments/announce-with-shipping-rules
// (omitting ship_with lets Sendcloud shipping rules pick the carrier/contract), POST
// /parcels/{id}/cancel, POST /pickups.
package shippinglabel

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
)

const (
	prodBaseURL    = "https://panel.sendcloud.sc/api/v3"
	httpTimeout    = 30 * time.Second // announce is heavier than a tracking poll
	maxRespBody    = 16 << 20         // 16 MiB (the label is returned inline as base64)
	maxLabelBytes  = 10 << 20         // 10 MiB cap on a decoded/downloaded label PDF
	announcePath   = "/shipments/announce-with-shipping-rules"
	optionsPath    = "/fetch-shipping-options"
	pickupsPath    = "/pickups"
	defaultWeightU = "kilogram"
	defaultDimU    = "centimeter"
)

// Config holds Sendcloud credentials and the warehouse origin (ship-from) address that every
// generated label ships from. PublicKey/SecretKey are the Sendcloud integration key pair (Basic
// Auth). DefaultShippingOption is an optional shipping_option_code used when neither the operator
// nor Sendcloud shipping rules resolve one (usually left empty — rules pick the carrier).
type Config struct {
	PublicKey             string `mapstructure:"public_key"`
	SecretKey             string `mapstructure:"secret_key"`
	DefaultShippingOption string `mapstructure:"default_shipping_option"`

	FromName        string `mapstructure:"from_name"`
	FromCompany     string `mapstructure:"from_company"`
	FromStreet1     string `mapstructure:"from_street1"`
	FromHouseNumber string `mapstructure:"from_house_number"`
	FromStreet2     string `mapstructure:"from_street2"`
	FromCity        string `mapstructure:"from_city"`
	FromState       string `mapstructure:"from_state"`
	FromPostalCode  string `mapstructure:"from_postal_code"`
	FromCountry     string `mapstructure:"from_country"` // ISO 3166-1 alpha-2
	FromPhone       string `mapstructure:"from_phone"`
	FromEmail       string `mapstructure:"from_email"`
}

// ShipFromAddress builds the label ship-from endpoint from configuration. The country is resolved
// to ISO alpha-2; the caller validates completeness (validateShipFrom).
func (c *Config) ShipFromAddress() entity.LabelAddress {
	iso2 := strings.ToUpper(strings.TrimSpace(c.FromCountry))
	if resolved, ok := entity.ResolveCountryISO2(c.FromCountry); ok {
		iso2 = resolved
	}
	return entity.LabelAddress{
		ContactName: c.FromName,
		Company:     c.FromCompany,
		Street1:     c.FromStreet1,
		HouseNumber: c.FromHouseNumber,
		Street2:     c.FromStreet2,
		City:        c.FromCity,
		State:       c.FromState,
		PostalCode:  c.FromPostalCode,
		CountryISO2: iso2,
		Phone:       c.FromPhone,
		Email:       c.FromEmail,
		Residential: false,
	}
}

// Client is the Sendcloud label client implementing dependency.LabelProvider.
type Client struct {
	publicKey     string
	secretKey     string
	baseURL       string
	defaultOption string
	http          *http.Client
}

// New builds a shipping-label provider. When either key is empty it returns a disabled no-op so the
// rest of the app wires the same interface regardless of configuration; callers then fall back to
// manual tracking-number entry.
func New(c *Config) dependency.LabelProvider {
	if c == nil || strings.TrimSpace(c.PublicKey) == "" || strings.TrimSpace(c.SecretKey) == "" {
		return Disabled{}
	}
	return &Client{
		publicKey:     strings.TrimSpace(c.PublicKey),
		secretKey:     strings.TrimSpace(c.SecretKey),
		baseURL:       prodBaseURL,
		defaultOption: strings.TrimSpace(c.DefaultShippingOption),
		http:          &http.Client{Timeout: httpTimeout},
	}
}

// Enabled reports the provider is configured.
func (c *Client) Enabled() bool { return true }

// ---- Sendcloud v3 wire types (only the fields we send/read) ----

type wireMeasure struct {
	Value string `json:"value"`
	Unit  string `json:"unit"`
}

type wireAddress struct {
	Name         string `json:"name"`
	CompanyName  string `json:"company_name,omitempty"`
	AddressLine1 string `json:"address_line_1"`
	HouseNumber  string `json:"house_number,omitempty"`
	AddressLine2 string `json:"address_line_2,omitempty"`
	City         string `json:"city"`
	PostalCode   string `json:"postal_code"`
	CountryCode  string `json:"country_code"`
	Phone        string `json:"phone_number,omitempty"`
	Email        string `json:"email,omitempty"`
}

type wireDimensions struct {
	Length string `json:"length"`
	Width  string `json:"width"`
	Height string `json:"height"`
	Unit   string `json:"unit"`
}

type wireParcelItem struct {
	Description   string      `json:"description"`
	Quantity      int         `json:"quantity"`
	Price         wireMoney   `json:"price"`
	Weight        wireMeasure `json:"weight"`
	HSCode        string      `json:"hs_code,omitempty"`
	OriginCountry string      `json:"origin_country,omitempty"`
	SKU           string      `json:"sku,omitempty"`
}

type wireMoney struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type wireParcel struct {
	Weight      wireMeasure      `json:"weight"`
	Dimensions  *wireDimensions  `json:"dimensions,omitempty"`
	ParcelItems []wireParcelItem `json:"parcel_items,omitempty"`
}

type wireShipWithProps struct {
	ShippingOptionCode string `json:"shipping_option_code"`
}

type wireShipWith struct {
	Type       string            `json:"type"`
	Properties wireShipWithProps `json:"properties"`
}

type wireAnnounceRequest struct {
	FromAddress wireAddress   `json:"from_address"`
	ToAddress   wireAddress   `json:"to_address"`
	Parcels     []wireParcel  `json:"parcels"`
	ShipWith    *wireShipWith `json:"ship_with,omitempty"`
	OrderNumber string        `json:"order_number,omitempty"`
}

// wireLabelFile decodes Sendcloud's label file, which may arrive as inline base64 (data) or as a
// signed URL. We prefer base64; a URL is fetched (authenticated) as a fallback.
type wireLabelFile struct {
	Data   string `json:"data"`
	Base64 string `json:"base64"`
	URL    string `json:"url"`
	Format string `json:"format"`
}

type wireStatus struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type wireResponseParcel struct {
	ID             json.Number    `json:"id"`
	TrackingNumber string         `json:"tracking_number"`
	Status         wireStatus     `json:"status"`
	LabelFile      *wireLabelFile `json:"label_file"`
}

type wireCarrier struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type wireAnnounceResponse struct {
	Data struct {
		ID                 json.Number          `json:"id"`
		Carrier            wireCarrier          `json:"carrier"`
		ShippingOptionCode string               `json:"shipping_option_code"`
		Parcels            []wireResponseParcel `json:"parcels"`
	} `json:"data"`
	Errors []wireAPIError `json:"errors"`
}

type wireAPIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
}

type wireOptionsRequest struct {
	FromCountryCode string          `json:"from_country_code"`
	ToCountryCode   string          `json:"to_country_code"`
	FromPostalCode  string          `json:"from_postal_code,omitempty"`
	ToPostalCode    string          `json:"to_postal_code,omitempty"`
	Weight          wireMeasure     `json:"weight"`
	Dimensions      *wireDimensions `json:"dimensions,omitempty"`
}

type wireOption struct {
	Code    string      `json:"code"`
	Name    string      `json:"name"`
	Carrier wireCarrier `json:"carrier"`
	Product struct {
		Code string `json:"code"`
		Name string `json:"name"`
	} `json:"product"`
	Quotes []struct {
		LeadTime *int `json:"lead_time"` // hours, best-effort
		Price    struct {
			Total wireMoney `json:"total"`
		} `json:"price"`
	} `json:"quotes"`
}

type wireOptionsResponse struct {
	Data   []wireOption   `json:"data"`
	Errors []wireAPIError `json:"errors"`
}

type wireTimeSlot struct {
	StartAt string `json:"start_at"`
	EndAt   string `json:"end_at"`
}

type wirePickupRequest struct {
	CarrierCode string         `json:"carrier_code"`
	Address     wireAddress    `json:"address"`
	TimeSlots   []wireTimeSlot `json:"time_slots,omitempty"`
	Quantity    int            `json:"quantity"`
	Reference   string         `json:"reference,omitempty"`
}

type wirePickupResponse struct {
	Data struct {
		ID     flexID `json:"id"`
		Status string `json:"status"`
	} `json:"data"`
	// Some responses inline id/status at the top level.
	ID     flexID         `json:"id"`
	Status string         `json:"status"`
	Errors []wireAPIError `json:"errors"`
}

// flexID decodes a pickup id that Sendcloud may return as either a JSON number or a string.
type flexID string

func (f *flexID) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" {
		*f = ""
		return nil
	}
	*f = flexID(strings.Trim(s, `"`))
	return nil
}

func (f flexID) String() string { return string(f) }

// GetShippingOptions fetches the shipping options available for a parcel via POST
// /fetch-shipping-options, returning each carrier/service and its quote so an operator can pick one.
func (c *Client) GetShippingOptions(ctx context.Context, req entity.OptionsRequest) ([]entity.ShippingOption, error) {
	if req.Parcel.WeightGrams <= 0 {
		return nil, fmt.Errorf("shippinglabel: parcel weight must be positive")
	}
	body, err := json.Marshal(wireOptionsRequest{
		FromCountryCode: req.ShipFrom.CountryISO2,
		ToCountryCode:   req.ShipTo.CountryISO2,
		FromPostalCode:  req.ShipFrom.PostalCode,
		ToPostalCode:    req.ShipTo.PostalCode,
		Weight:          gramsToKg(req.Parcel.WeightGrams),
		Dimensions:      dimensions(req.Parcel),
	})
	if err != nil {
		return nil, fmt.Errorf("shippinglabel: marshal options request: %w", err)
	}
	raw, statusCode, err := c.postRaw(ctx, optionsPath, body)
	if err != nil {
		return nil, err
	}
	var env wireOptionsResponse
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, fmt.Errorf("shippinglabel: decode options (http=%d): %w", statusCode, err)
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, fmt.Errorf("shippinglabel: fetch shipping options failed: http=%d %s", statusCode, firstErr(env.Errors))
	}
	out := make([]entity.ShippingOption, 0, len(env.Data))
	for _, o := range env.Data {
		opt := entity.ShippingOption{
			Code:        o.Code,
			CarrierCode: o.Carrier.Code,
			CarrierName: o.Carrier.Name,
			ProductName: firstNonEmpty(o.Product.Name, o.Name),
		}
		if len(o.Quotes) > 0 {
			q := o.Quotes[0]
			if s := strings.TrimSpace(q.Price.Total.Value); s != "" {
				if d, derr := decimal.NewFromString(s); derr == nil {
					opt.TotalCharge = d
				}
			}
			opt.Currency = q.Price.Total.Currency
			if q.LeadTime != nil && *q.LeadTime > 0 {
				opt.TransitDays = (*q.LeadTime + 23) / 24 // hours -> whole days, rounded up
			}
		}
		out = append(out, opt)
	}
	return out, nil
}

// CreateLabel announces a single-collo shipment via POST /shipments/announce-with-shipping-rules
// and returns the tracking number + decoded label PDF. When req.ShippingOptionCode is empty (and no
// configured default), ship_with is omitted so Sendcloud shipping rules pick the carrier/contract.
func (c *Client) CreateLabel(ctx context.Context, req entity.LabelRequest) (*entity.LabelResult, error) {
	if req.Parcel.WeightGrams <= 0 {
		return nil, fmt.Errorf("shippinglabel: parcel weight must be positive")
	}

	parcel := wireParcel{
		Weight:     gramsToKg(req.Parcel.WeightGrams),
		Dimensions: dimensions(req.Parcel),
	}
	if req.Customs != nil {
		for _, it := range req.Customs.Items {
			parcel.ParcelItems = append(parcel.ParcelItems, wireParcelItem{
				Description:   it.Description,
				Quantity:      it.Quantity,
				Price:         wireMoney{Value: it.PriceAmount.StringFixed(2), Currency: it.PriceCurrency},
				Weight:        gramsToKg(it.WeightGrams),
				HSCode:        it.HSCode,
				OriginCountry: it.OriginISO2,
				SKU:           it.SKU,
			})
		}
	}

	body := wireAnnounceRequest{
		FromAddress: toWireAddress(req.ShipFrom),
		ToAddress:   toWireAddress(req.ShipTo),
		Parcels:     []wireParcel{parcel},
	}
	if len(req.References) > 0 {
		body.OrderNumber = req.References[0]
	}
	optionCode := strings.TrimSpace(req.ShippingOptionCode)
	if optionCode == "" {
		optionCode = c.defaultOption
	}
	if optionCode != "" {
		body.ShipWith = &wireShipWith{
			Type:       "shipping_option_code",
			Properties: wireShipWithProps{ShippingOptionCode: optionCode},
		}
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("shippinglabel: marshal announce request: %w", err)
	}
	respRaw, statusCode, err := c.postRaw(ctx, announcePath, raw)
	if err != nil {
		return nil, err
	}
	var env wireAnnounceResponse
	if len(respRaw) > 0 {
		if err := json.Unmarshal(respRaw, &env); err != nil {
			return nil, fmt.Errorf("shippinglabel: decode announce (http=%d): %w", statusCode, err)
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, fmt.Errorf("shippinglabel: announce shipment failed: http=%d %s", statusCode, firstErr(env.Errors))
	}
	if len(env.Data.Parcels) == 0 {
		return nil, fmt.Errorf("shippinglabel: announce returned no parcels")
	}
	p := env.Data.Parcels[0]
	if strings.TrimSpace(p.TrackingNumber) == "" {
		return nil, fmt.Errorf("shippinglabel: announce returned no tracking number (status=%q)", p.Status.Message)
	}
	pdf, err := c.resolveLabelPDF(ctx, p.LabelFile)
	if err != nil {
		return nil, fmt.Errorf("shippinglabel: resolve label file: %w", err)
	}

	shipmentID := env.Data.ID.String()
	if pid := p.ID.String(); pid != "" && pid != "0" {
		shipmentID = pid // prefer the parcel id (used to cancel)
	}
	return &entity.LabelResult{
		TrackingNumber:     strings.TrimSpace(p.TrackingNumber),
		LabelPDF:           pdf,
		CarrierShipmentID:  shipmentID,
		CarrierCode:        env.Data.Carrier.Code,
		CarrierName:        env.Data.Carrier.Name,
		ShippingOptionCode: firstNonEmpty(env.Data.ShippingOptionCode, optionCode),
		Status:             p.Status.Code,
	}, nil
}

// resolveLabelPDF turns Sendcloud's label_file into PDF bytes: inline base64 when present,
// otherwise an authenticated GET of the label URL.
func (c *Client) resolveLabelPDF(ctx context.Context, lf *wireLabelFile) ([]byte, error) {
	if lf == nil {
		return nil, fmt.Errorf("no label file in response")
	}
	if enc := firstNonEmpty(lf.Data, lf.Base64); enc != "" {
		dec, err := base64.StdEncoding.DecodeString(strings.TrimSpace(enc))
		if err != nil {
			return nil, fmt.Errorf("decode base64 label: %w", err)
		}
		if len(dec) == 0 {
			return nil, fmt.Errorf("decoded label is empty")
		}
		return dec, nil
	}
	if strings.TrimSpace(lf.URL) != "" {
		return c.fetchLabelURL(ctx, lf.URL)
	}
	return nil, fmt.Errorf("label file has neither inline data nor url")
}

// fetchLabelURL downloads a label PDF from a Sendcloud-hosted URL with Basic Auth (label URLs on
// this host require credentials). Capped at maxLabelBytes.
func (c *Client) fetchLabelURL(ctx context.Context, labelURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, labelURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build label download request: %w", err)
	}
	req.SetBasicAuth(c.publicKey, c.secretKey)
	req.Header.Set("Accept", "application/pdf")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download label: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download label failed: http=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLabelBytes))
	if err != nil {
		return nil, fmt.Errorf("read label body: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("downloaded label is empty")
	}
	return raw, nil
}

// VoidLabel cancels an announced parcel via POST /parcels/{id}/cancel. A 2xx (or already-cancelled)
// response is success.
func (c *Client) VoidLabel(ctx context.Context, carrierShipmentID string) error {
	id := strings.TrimSpace(carrierShipmentID)
	if id == "" {
		return fmt.Errorf("shippinglabel: carrier shipment id is required")
	}
	raw, statusCode, err := c.postRaw(ctx, "/parcels/"+id+"/cancel", []byte("{}"))
	if err != nil {
		return err
	}
	if statusCode < 200 || statusCode >= 300 {
		var env struct {
			Errors []wireAPIError `json:"errors"`
		}
		_ = json.Unmarshal(raw, &env)
		return fmt.Errorf("shippinglabel: cancel parcel failed: http=%d %s", statusCode, firstErr(env.Errors))
	}
	return nil
}

// SchedulePickup books a carrier pickup for the day via POST /pickups (Sendcloud's end-of-day
// handover equivalent — v3 has no generic manifest API).
func (c *Client) SchedulePickup(ctx context.Context, req entity.PickupRequest) (*entity.PickupResult, error) {
	if strings.TrimSpace(req.CarrierCode) == "" {
		return nil, fmt.Errorf("shippinglabel: pickup carrier_code is required")
	}
	if strings.TrimSpace(req.Date) == "" {
		return nil, fmt.Errorf("shippinglabel: pickup date is required")
	}
	body := wirePickupRequest{
		CarrierCode: req.CarrierCode,
		Address:     toWireAddress(req.Address),
		Quantity:    req.Quantity,
	}
	from := firstNonEmpty(req.FromTime, "09:00:00")
	to := firstNonEmpty(req.ToTime, "17:00:00")
	body.TimeSlots = []wireTimeSlot{{
		StartAt: req.Date + "T" + from + "Z",
		EndAt:   req.Date + "T" + to + "Z",
	}}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("shippinglabel: marshal pickup request: %w", err)
	}
	respRaw, statusCode, err := c.postRaw(ctx, pickupsPath, raw)
	if err != nil {
		return nil, err
	}
	var env wirePickupResponse
	if len(respRaw) > 0 {
		if err := json.Unmarshal(respRaw, &env); err != nil {
			return nil, fmt.Errorf("shippinglabel: decode pickup (http=%d): %w", statusCode, err)
		}
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, fmt.Errorf("shippinglabel: schedule pickup failed: http=%d %s", statusCode, firstErr(env.Errors))
	}
	id := env.Data.ID.String()
	if id == "" || id == "0" {
		id = env.ID.String()
	}
	st := firstNonEmpty(env.Data.Status, env.Status)
	return &entity.PickupResult{
		PickupID:  id,
		Confirmed: strings.EqualFold(st, "CREATED") || strings.EqualFold(st, "ANNOUNCING"),
		Message:   st,
	}, nil
}

// postRaw POSTs a JSON body to a Sendcloud endpoint with Basic Auth and returns the raw (capped)
// response bytes and HTTP status. Shared by every call, each of which decodes its own envelope.
func (c *Client) postRaw(ctx context.Context, path string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, 0, fmt.Errorf("shippinglabel: build request: %w", err)
	}
	req.SetBasicAuth(c.publicKey, c.secretKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("shippinglabel: request: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxRespBody))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("shippinglabel: read response: %w", err)
	}
	return raw, resp.StatusCode, nil
}

func toWireAddress(a entity.LabelAddress) wireAddress {
	return wireAddress{
		Name:         firstNonEmpty(a.ContactName, a.Company),
		CompanyName:  a.Company,
		AddressLine1: a.Street1,
		HouseNumber:  a.HouseNumber,
		AddressLine2: a.Street2,
		City:         a.City,
		PostalCode:   a.PostalCode,
		CountryCode:  a.CountryISO2,
		Phone:        a.Phone,
		Email:        a.Email,
	}
}

// gramsToKg formats a gram weight as a Sendcloud kilogram measure (3 decimal places).
func gramsToKg(grams int) wireMeasure {
	return wireMeasure{
		Value: decimal.NewFromInt(int64(grams)).Div(decimal.NewFromInt(1000)).StringFixed(3),
		Unit:  defaultWeightU,
	}
}

// dimensions returns a Sendcloud dimensions object in centimetres, or nil when any side is zero.
func dimensions(p entity.LabelParcel) *wireDimensions {
	if p.LengthCM <= 0 || p.WidthCM <= 0 || p.HeightCM <= 0 {
		return nil
	}
	return &wireDimensions{
		Length: fmt.Sprintf("%d", p.LengthCM),
		Width:  fmt.Sprintf("%d", p.WidthCM),
		Height: fmt.Sprintf("%d", p.HeightCM),
		Unit:   defaultDimU,
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstErr(errs []wireAPIError) string {
	if len(errs) == 0 {
		return ""
	}
	e := errs[0]
	return fmt.Sprintf("code=%q %s", e.Code, firstNonEmpty(e.Detail, e.Message, e.Title))
}

// Disabled is a no-op LabelProvider used when Sendcloud is not configured. Every method returns
// ErrLabelsDisabled so the handler can report a clear not-configured state.
type Disabled struct{}

// Enabled reports the provider is not configured.
func (Disabled) Enabled() bool { return false }

// CreateLabel always returns ErrLabelsDisabled.
func (Disabled) CreateLabel(_ context.Context, _ entity.LabelRequest) (*entity.LabelResult, error) {
	return nil, entity.ErrLabelsDisabled
}

// GetShippingOptions always returns ErrLabelsDisabled.
func (Disabled) GetShippingOptions(_ context.Context, _ entity.OptionsRequest) ([]entity.ShippingOption, error) {
	return nil, entity.ErrLabelsDisabled
}

// VoidLabel always returns ErrLabelsDisabled.
func (Disabled) VoidLabel(_ context.Context, _ string) error {
	return entity.ErrLabelsDisabled
}

// SchedulePickup always returns ErrLabelsDisabled.
func (Disabled) SchedulePickup(_ context.Context, _ entity.PickupRequest) (*entity.PickupResult, error) {
	return nil, entity.ErrLabelsDisabled
}
