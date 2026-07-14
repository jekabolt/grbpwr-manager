package shippinglabel

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

func TestNewDisabledWhenNoKeys(t *testing.T) {
	for _, c := range []*Config{
		{},
		{PublicKey: "pub"}, // secret missing
		{SecretKey: "sec"}, // public missing
	} {
		p := New(c)
		if _, ok := p.(Disabled); !ok {
			t.Fatalf("expected Disabled provider for %+v, got %T", c, p)
		}
	}
	p := New(&Config{PublicKey: "pub", SecretKey: "sec"})
	if _, ok := p.(*Client); !ok {
		t.Fatalf("expected Client when both keys set, got %T", p)
	}
	if !p.Enabled() {
		t.Error("expected Enabled() true for configured client")
	}
	if (Disabled{}).Enabled() {
		t.Error("expected Enabled() false for disabled provider")
	}
	_, err := (Disabled{}).CreateLabel(context.Background(), entity.LabelRequest{})
	if !errors.Is(err, entity.ErrLabelsDisabled) {
		t.Fatalf("expected ErrLabelsDisabled, got %v", err)
	}
}

func TestCreateLabelRequestAndResponse(t *testing.T) {
	labelBytes := []byte("%PDF-1.4 sendcloud label")
	var gotBody wireAnnounceRequest
	var gotUser, gotPass string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPass, _ = r.BasicAuth()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"data": {
				"id": 555,
				"carrier": {"code": "dhl", "name": "DHL Express"},
				"shipping_option_code": "dhl:express",
				"parcels": [
					{"id": 999, "tracking_number": "3884930103", "status": {"code": "announced", "message": "ok"},
					 "label_file": {"data": "` + base64.StdEncoding.EncodeToString(labelBytes) + `", "format": "pdf"}}
				]
			}
		}`))
	}))
	defer srv.Close()

	c := &Client{publicKey: "pub", secretKey: "sec", baseURL: srv.URL, http: srv.Client()}
	res, err := c.CreateLabel(context.Background(), entity.LabelRequest{
		ShippingOptionCode: "dhl:express",
		ShipFrom:           entity.LabelAddress{ContactName: "WH", Street1: "Origin 1", City: "Berlin", PostalCode: "10115", CountryISO2: "DE"},
		ShipTo:             entity.LabelAddress{ContactName: "Jane Doe", Street1: "Dest 2", City: "Paris", PostalCode: "75001", CountryISO2: "FR"},
		Parcel:             entity.LabelParcel{WeightGrams: 1500, LengthCM: 30, WidthCM: 22, HeightCM: 10},
		References:         []string{"order-uuid-1"},
	})
	if err != nil {
		t.Fatalf("CreateLabel error: %v", err)
	}

	// Response parsing.
	if res.TrackingNumber != "3884930103" {
		t.Errorf("tracking = %q, want 3884930103", res.TrackingNumber)
	}
	if string(res.LabelPDF) != string(labelBytes) {
		t.Errorf("label pdf = %q, want %q", res.LabelPDF, labelBytes)
	}
	if res.CarrierShipmentID != "999" { // prefer parcel id
		t.Errorf("carrier shipment id = %q, want 999", res.CarrierShipmentID)
	}
	if res.CarrierCode != "dhl" || res.CarrierName != "DHL Express" {
		t.Errorf("carrier = %q/%q", res.CarrierCode, res.CarrierName)
	}
	if res.ShippingOptionCode != "dhl:express" {
		t.Errorf("shipping_option_code = %q", res.ShippingOptionCode)
	}

	// Request building.
	if gotUser != "pub" || gotPass != "sec" {
		t.Errorf("basic auth = %q/%q", gotUser, gotPass)
	}
	if gotBody.OrderNumber != "order-uuid-1" {
		t.Errorf("order_number = %q", gotBody.OrderNumber)
	}
	if gotBody.ShipWith == nil || gotBody.ShipWith.Properties.ShippingOptionCode != "dhl:express" {
		t.Errorf("ship_with = %+v", gotBody.ShipWith)
	}
	if len(gotBody.Parcels) != 1 {
		t.Fatalf("expected 1 parcel, got %d", len(gotBody.Parcels))
	}
	p := gotBody.Parcels[0]
	if p.Weight.Value != "1.500" || p.Weight.Unit != defaultWeightU {
		t.Errorf("weight = %v %s, want 1.500 kg", p.Weight.Value, p.Weight.Unit)
	}
	if p.Dimensions == nil || p.Dimensions.Length != "30" || p.Dimensions.Width != "22" || p.Dimensions.Height != "10" {
		t.Errorf("dimensions = %+v", p.Dimensions)
	}
	if gotBody.ToAddress.CountryCode != "FR" || gotBody.FromAddress.CountryCode != "DE" {
		t.Errorf("countries = %q/%q", gotBody.FromAddress.CountryCode, gotBody.ToAddress.CountryCode)
	}
}

func TestCreateLabelOmitsShipWithWhenNoOption(t *testing.T) {
	var gotBody wireAnnounceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"id":1,"parcels":[{"id":2,"tracking_number":"T1","label_file":{"data":"` +
			base64.StdEncoding.EncodeToString([]byte("pdf")) + `"}}]}}`))
	}))
	defer srv.Close()
	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	_, err := c.CreateLabel(context.Background(), entity.LabelRequest{
		Parcel: entity.LabelParcel{WeightGrams: 500},
	})
	if err != nil {
		t.Fatalf("CreateLabel error: %v", err)
	}
	if gotBody.ShipWith != nil {
		t.Errorf("expected ship_with omitted, got %+v", gotBody.ShipWith)
	}
}

func TestCreateLabelUsesDefaultOption(t *testing.T) {
	var gotBody wireAnnounceRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"data":{"id":1,"parcels":[{"id":2,"tracking_number":"T1","label_file":{"data":"` +
			base64.StdEncoding.EncodeToString([]byte("pdf")) + `"}}]}}`))
	}))
	defer srv.Close()
	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, defaultOption: "dpd:home", http: srv.Client()}
	if _, err := c.CreateLabel(context.Background(), entity.LabelRequest{Parcel: entity.LabelParcel{WeightGrams: 500}}); err != nil {
		t.Fatalf("CreateLabel error: %v", err)
	}
	if gotBody.ShipWith == nil || gotBody.ShipWith.Properties.ShippingOptionCode != "dpd:home" {
		t.Errorf("expected default option dpd:home, got %+v", gotBody.ShipWith)
	}
}

func TestCreateLabelErrors(t *testing.T) {
	// No parcels in response.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":1,"parcels":[]}}`))
	}))
	defer srv.Close()
	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	if _, err := c.CreateLabel(context.Background(), entity.LabelRequest{Parcel: entity.LabelParcel{WeightGrams: 100}}); err == nil {
		t.Error("expected error when no parcels returned")
	}
	// Non-2xx with errors.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"errors":[{"code":"invalid","detail":"bad address"}]}`))
	}))
	defer srv2.Close()
	c2 := &Client{publicKey: "p", secretKey: "s", baseURL: srv2.URL, http: srv2.Client()}
	if _, err := c2.CreateLabel(context.Background(), entity.LabelRequest{Parcel: entity.LabelParcel{WeightGrams: 100}}); err == nil {
		t.Error("expected error on http 400")
	}
	// Weight non-positive.
	if _, err := c.CreateLabel(context.Background(), entity.LabelRequest{}); err == nil {
		t.Error("expected error on zero weight")
	}
}

func TestGetShippingOptions(t *testing.T) {
	var gotBody wireOptionsRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{
			"data": [
				{"code": "dhl:express", "name": "DHL Express", "carrier": {"code": "dhl", "name": "DHL"},
				 "product": {"code": "express", "name": "Express"},
				 "quotes": [{"lead_time": 48, "price": {"total": {"value": "12.34", "currency": "EUR"}}}]}
			]
		}`))
	}))
	defer srv.Close()

	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	opts, err := c.GetShippingOptions(context.Background(), entity.OptionsRequest{
		ShipFrom: entity.LabelAddress{PostalCode: "10115", CountryISO2: "DE"},
		ShipTo:   entity.LabelAddress{PostalCode: "75001", CountryISO2: "FR"},
		Parcel:   entity.LabelParcel{WeightGrams: 800},
	})
	if err != nil {
		t.Fatalf("GetShippingOptions error: %v", err)
	}
	if gotBody.FromCountryCode != "DE" || gotBody.ToCountryCode != "FR" || gotBody.Weight.Value != "0.800" {
		t.Errorf("request = %+v", gotBody)
	}
	if len(opts) != 1 {
		t.Fatalf("options = %d, want 1", len(opts))
	}
	o := opts[0]
	if o.Code != "dhl:express" || o.CarrierCode != "dhl" || o.Currency != "EUR" || o.TransitDays != 2 {
		t.Errorf("option = %+v", o)
	}
	if o.TotalCharge.String() != "12.34" {
		t.Errorf("total charge = %s, want 12.34", o.TotalCharge)
	}
}

func TestVoidLabel(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	if err := c.VoidLabel(context.Background(), "999"); err != nil {
		t.Fatalf("VoidLabel error: %v", err)
	}
	if gotPath != "/parcels/999/cancel" {
		t.Errorf("path = %q, want /parcels/999/cancel", gotPath)
	}
	if err := c.VoidLabel(context.Background(), ""); err == nil {
		t.Error("expected error on empty id")
	}
}

func TestVoidLabelFailedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":[{"code":"cannot_cancel","detail":"already handed over"}]}`))
	}))
	defer srv.Close()
	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	if err := c.VoidLabel(context.Background(), "1"); err == nil {
		t.Error("expected error on failed cancel")
	}
}

func TestSchedulePickup(t *testing.T) {
	var gotBody wirePickupRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pickups" {
			t.Errorf("path = %q, want /pickups", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"pu_1","status":"CREATED"}}`))
	}))
	defer srv.Close()

	c := &Client{publicKey: "p", secretKey: "s", baseURL: srv.URL, http: srv.Client()}
	res, err := c.SchedulePickup(context.Background(), entity.PickupRequest{
		Address:     entity.LabelAddress{Street1: "Origin 1", City: "Berlin", PostalCode: "10115", CountryISO2: "DE"},
		CarrierCode: "dhl",
		Date:        "2026-07-20",
		Quantity:    3,
	})
	if err != nil {
		t.Fatalf("SchedulePickup error: %v", err)
	}
	if res.PickupID != "pu_1" || !res.Confirmed {
		t.Errorf("result = %+v", res)
	}
	if gotBody.CarrierCode != "dhl" || gotBody.Quantity != 3 || len(gotBody.TimeSlots) != 1 {
		t.Errorf("request = %+v", gotBody)
	}
	if gotBody.TimeSlots[0].StartAt != "2026-07-20T09:00:00Z" {
		t.Errorf("start_at = %q", gotBody.TimeSlots[0].StartAt)
	}
	if _, err := c.SchedulePickup(context.Background(), entity.PickupRequest{Date: "2026-07-20"}); err == nil {
		t.Error("expected error when carrier_code missing")
	}
}

func TestDisabledMethods(t *testing.T) {
	d := Disabled{}
	if _, err := d.GetShippingOptions(context.Background(), entity.OptionsRequest{}); !errors.Is(err, entity.ErrLabelsDisabled) {
		t.Errorf("GetShippingOptions disabled err = %v", err)
	}
	if err := d.VoidLabel(context.Background(), "x"); !errors.Is(err, entity.ErrLabelsDisabled) {
		t.Errorf("VoidLabel disabled err = %v", err)
	}
	if _, err := d.SchedulePickup(context.Background(), entity.PickupRequest{}); !errors.Is(err, entity.ErrLabelsDisabled) {
		t.Errorf("SchedulePickup disabled err = %v", err)
	}
}

func TestResolveLabelPDFURLFallback(t *testing.T) {
	labelBytes := []byte("%PDF-1.4 via url")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _, _ := r.BasicAuth(); u != "pub" {
			t.Errorf("expected basic auth on label download")
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(labelBytes)
	}))
	defer srv.Close()
	c := &Client{publicKey: "pub", secretKey: "sec", baseURL: "http://unused", http: srv.Client()}
	got, err := c.resolveLabelPDF(context.Background(), &wireLabelFile{URL: srv.URL + "/label.pdf"})
	if err != nil {
		t.Fatalf("resolveLabelPDF error: %v", err)
	}
	if string(got) != string(labelBytes) {
		t.Errorf("got %q, want %q", got, labelBytes)
	}
}
