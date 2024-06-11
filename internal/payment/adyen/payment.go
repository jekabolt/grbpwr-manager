package adyen

// import (
// 	"encoding/json"
// 	"fmt"

// 	"net/http"

// 	"github.com/adyen/adyen-go-api-library/v9/src/checkout"
// 	"github.com/adyen/adyen-go-api-library/v9/src/common"
// 	"github.com/jekabolt/grbpwr-manager/internal/dependency"
// 	"github.com/jekabolt/grbpwr-manager/internal/entity"
// )

// type Config struct {
// 	ApiKey string `mapstructure:"api_key"`
// }

// type Processor struct {
// 	c      *Config
// 	pm     *entity.PaymentMethod
// 	addrs  map[string]string //k:address v: order uuid
// 	mailer dependency.Mailer
// 	rates  dependency.RatesService
// }

// func New(c *Config, rep dependency.Repository, m dependency.Mailer, tg dependency.Trongrid, r dependency.RatesService, pmn entity.PaymentMethodName) (dependency.CryptoInvoice, error) {
// 	pm, ok := rep.Cache().GetPaymentMethodByName(pmn)
// 	if !ok {
// 		return nil, fmt.Errorf("payment method not found")
// 	}

// 	p := &Processor{
// 		c:      c,
// 		pm:     &pm,
// 		mailer: m,
// 		rates:  r,
// 	}

// 	err := p.initAddressesFromUnpaidOrders(ctx)
// 	if err != nil {
// 		return nil, fmt.Errorf("can't init addresses from unpaid orders: %w", err)
// 	}

// 	return p, nil

// }

// func handlePayments(w http.ResponseWriter, r *http.Request) {
// 	var req checkout.PaymentRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		http.Error(w, err.Error(), http.StatusBadRequest)
// 		return
// 	}

// 	client := common.NewClient(&common.Config{
// 		ApiKey:      apiKey,
// 		Environment: common.TestEnv,
// 	})
// 	service := checkout.New(client)

// 	req.MerchantAccount = merchantAccount

// 	res, httpRes, err := service.Payments(&req)
// 	if err != nil {
// 		http.Error(w, err.Error(), httpRes.StatusCode)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	json.NewEncoder(w).Encode(res)
// }

// func handlePaymentDetails(w http.ResponseWriter, r *http.Request) {
// 	var req checkout.PaymentDetailsRequest
// 	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
// 		http.Error(w, err.Error(), http.StatusBadRequest)
// 		return
// 	}

// 	client := common.NewClient(&common.Config{
// 		ApiKey:      apiKey,
// 		Environment: common.TestEnv,
// 	})
// 	service := checkout.New(client)

// 	res, httpRes, err := service.PaymentDetails(&req)
// 	if err != nil {
// 		http.Error(w, err.Error(), httpRes.StatusCode)
// 		return
// 	}

// 	w.Header().Set("Content-Type", "application/json")
// 	json.NewEncoder(w).Encode(res)
// }
