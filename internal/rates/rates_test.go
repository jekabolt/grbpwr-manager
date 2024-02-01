package rates

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dependency/mocks"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func TestStartStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/price") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"BTC":0.00004024,"ETH":0.0006705}`))
			return
		}
		if strings.Contains(r.URL.Path, "/latest") {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"success":true,"timestamp":1695586623,"base":"EUR","date":"2023-09-24","rates":{"SEK":1,"SGD":1,"USD":1,"CNY":1,"CZK":1,"DKK":1,"ILS":1,"RUB":1,"TRY":1,"CHF":1,"GEL":1,"HKD":1,"HUF":1,"NOK":1,"UAH":1,"GBP":1,"EUR":1,"JPY":1,"PLN":1,"BTC":1}}`))
			return
		}

	}))

	defer server.Close()

	exchangeRatesBaseURL = server.URL
	cryptoCompareBaseURL = server.URL

	rs := mocks.NewRates(t)

	rs.EXPECT().GetLatestRates(mock.Anything).Return([]entity.CurrencyRate{}, nil)
	rs.EXPECT().BulkUpdateRates(mock.Anything, mock.Anything).Return(nil)

	// Initialize the Service with the mocked server
	cli := New(&Config{
		ExchangeAPIKey:    "fake_api_key",
		RatesUpdatePeriod: time.Second,
	}, rs)

	err := cli.Start()
	assert.NoError(t, err)

	cli.Stop()

	rates := cli.GetRates()
	assert.NotEmpty(t, rates)
	t.Logf("Rates: %v", rates)

}
