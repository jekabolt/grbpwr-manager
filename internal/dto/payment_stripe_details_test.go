package dto

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
)

// nd (decimal.NullDecimal from string) is shared with style_economics_test.go.

func ns(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func TestConvertToOrderStripeDetails_NilWhenEmpty(t *testing.T) {
	// No settled amount and no captured payment detail -> nothing to show.
	of := &entity.OrderFull{}
	if got := ConvertToOrderStripeDetails(of); got != nil {
		t.Fatalf("expected nil for an order with no Stripe data, got %+v", got)
	}
	if got := ConvertToOrderStripeDetails(nil); got != nil {
		t.Fatalf("expected nil for a nil order, got %+v", got)
	}
}

func TestConvertToOrderStripeDetails_FullCapture(t *testing.T) {
	of := &entity.OrderFull{}
	of.Order.TotalSettledBase = nd("91.30")
	of.Order.PaymentFee = nd("3.20")
	of.Payment.PaymentInsert.TransactionID = ns("pi_test123")
	of.Payment.PaymentInsert.CardBrand = ns("visa")
	of.Payment.PaymentInsert.CardLast4 = ns("4242")
	of.Payment.PaymentInsert.StripeRiskLevel = ns("normal")
	of.Payment.PaymentInsert.StripeExchangeRate = nd("0.9130")

	got := ConvertToOrderStripeDetails(of)
	if got == nil {
		t.Fatal("expected non-nil details")
	}
	if got.GetTotalSettledBase().GetValue() != "91.3" {
		t.Errorf("settled base = %q, want 91.3", got.GetTotalSettledBase().GetValue())
	}
	if got.GetPaymentFee().GetValue() != "3.2" {
		t.Errorf("fee = %q, want 3.2", got.GetPaymentFee().GetValue())
	}
	if got.GetNetSettledBase().GetValue() != "88.1" { // 91.30 - 3.20
		t.Errorf("net = %q, want 88.1", got.GetNetSettledBase().GetValue())
	}
	if got.GetStripeExchangeRate().GetValue() != "0.913" {
		t.Errorf("fx = %q, want 0.913", got.GetStripeExchangeRate().GetValue())
	}
	if got.GetCardBrand() != "visa" || got.GetCardLast4() != "4242" {
		t.Errorf("card = %s %s, want visa 4242", got.GetCardBrand(), got.GetCardLast4())
	}
	if got.GetRiskLevel() != "normal" {
		t.Errorf("risk = %q, want normal", got.GetRiskLevel())
	}
	// URL is built from the PaymentIntent id; live vs test prefix depends on the payment
	// method (cache), but the id and Stripe host are always present.
	url := got.GetStripeDashboardUrl()
	if !strings.Contains(url, "dashboard.stripe.com") || !strings.HasSuffix(url, "pi_test123") {
		t.Errorf("dashboard url = %q, want a stripe dashboard link ending in the PI id", url)
	}
}

func TestConvertToOrderStripeDetails_SettledWithoutFee_NoNet(t *testing.T) {
	// Settled captured but the fee was not: net is undefined, not zero.
	of := &entity.OrderFull{}
	of.Order.TotalSettledBase = nd("50.00")

	got := ConvertToOrderStripeDetails(of)
	if got == nil {
		t.Fatal("expected non-nil details")
	}
	if got.GetPaymentFee() != nil {
		t.Errorf("expected nil fee, got %v", got.GetPaymentFee())
	}
	if got.GetNetSettledBase() != nil {
		t.Errorf("expected nil net when fee is absent, got %v", got.GetNetSettledBase())
	}
}

func TestConvertToOrderStripeDetails_DetailWithoutSettled(t *testing.T) {
	// A charge captured (card/receipt) but the balance transaction not yet settled:
	// still surface the detail block, just without the EUR figures.
	of := &entity.OrderFull{}
	of.Payment.PaymentInsert.CardBrand = ns("mastercard")
	of.Payment.PaymentInsert.ReceiptURL = ns("https://pay.stripe.com/receipts/x")

	got := ConvertToOrderStripeDetails(of)
	if got == nil {
		t.Fatal("expected non-nil details when only charge detail is present")
	}
	if got.GetTotalSettledBase() != nil {
		t.Errorf("expected nil settled base, got %v", got.GetTotalSettledBase())
	}
	if got.GetCardBrand() != "mastercard" {
		t.Errorf("card brand = %q, want mastercard", got.GetCardBrand())
	}
}
