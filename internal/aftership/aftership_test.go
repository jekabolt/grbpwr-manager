package aftership

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestClient(srv *httptest.Server) *Client {
	return &Client{apiKey: "k", baseURL: srv.URL, http: srv.Client()}
}

func TestGetTrackingStatusDelivered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"code":200},"data":{"tracking":{"tag":"Delivered"}}}`))
	}))
	defer srv.Close()

	st, err := newTestClient(srv).GetTrackingStatus(context.Background(), "dhl", "TN1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Found || !st.Delivered {
		t.Fatalf("want found+delivered, got %+v", st)
	}
}

func TestGetTrackingStatusInTransit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"meta":{"code":200},"data":{"tracking":{"tag":"InTransit"}}}`))
	}))
	defer srv.Close()

	st, err := newTestClient(srv).GetTrackingStatus(context.Background(), "dhl", "TN1")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Found || st.Delivered {
		t.Fatalf("want found, not delivered, got %+v", st)
	}
}

func TestGetTrackingStatusNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"meta":{"code":4004,"message":"tracking does not exist"}}`))
	}))
	defer srv.Close()

	st, err := newTestClient(srv).GetTrackingStatus(context.Background(), "dhl", "TNX")
	if err != nil {
		t.Fatalf("a not-found tracking must not error: %v", err)
	}
	if st.Found {
		t.Fatalf("want not found, got %+v", st)
	}
}

func TestRegisterTrackingAlreadyExistsIsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"meta":{"code":4003,"message":"tracking already exists"}}`))
	}))
	defer srv.Close()

	if err := newTestClient(srv).RegisterTracking(context.Background(), "dhl", "TN1"); err != nil {
		t.Fatalf("meta code 4003 must be treated as success, got %v", err)
	}
}

func TestDisabledTrackerIsNoOp(t *testing.T) {
	d := Disabled{}
	if err := d.RegisterTracking(context.Background(), "dhl", "TN1"); err != nil {
		t.Fatalf("disabled register must be a no-op, got %v", err)
	}
	st, err := d.GetTrackingStatus(context.Background(), "dhl", "TN1")
	if err != nil || st.Found {
		t.Fatalf("disabled status must be not-found, no error; got %+v err=%v", st, err)
	}
}

func TestNewReturnsDisabledWithoutKey(t *testing.T) {
	if _, ok := New(&Config{}).(Disabled); !ok {
		t.Fatal("empty api key must yield the disabled no-op tracker")
	}
	if _, ok := New(&Config{APIKey: "k"}).(*Client); !ok {
		t.Fatal("a configured api key must yield a real client")
	}
}
