package stripe

import "strings"

const (
	// Card Payment Method
	PaymentMethodTypeCard = "card"

	// Bank Transfer Payment Methods
	PaymentMethodTypeBancontact = "bancontact"
	PaymentMethodTypeIdeal      = "ideal"
	PaymentMethodTypeSepaDebit  = "sepa_debit"
	PaymentMethodTypeGiropay    = "giropay"
	PaymentMethodTypeEps        = "eps"
	PaymentMethodTypeSofort     = "sofort"
	PaymentMethodTypeP24        = "p24" // Przelewy24
	PaymentMethodTypeFpx        = "fpx" // Malaysia-based banks
	PaymentMethodTypeBacsDebit  = "bacs_debit"
	PaymentMethodTypeAcssDebit  = "acss_debit"
	PaymentMethodTypeMultibanco = "multibanco"
	PaymentMethodTypeWechatPay  = "wechat_pay"

	// Digital Wallets
	PaymentMethodTypeApplePay  = "apple_pay"
	PaymentMethodTypeGooglePay = "google_pay"

	// Klarna (Buy now, pay later)
	PaymentMethodTypeKlarna = "klarna"

	// Affirm (Buy now, pay later)
	PaymentMethodTypeAffirm = "affirm"

	// Alipay (for payments from China)
	PaymentMethodTypeAlipay = "alipay"

	// Afterpay/Clearpay (Buy now, pay later)
	PaymentMethodTypeAfterpayClearpay = "afterpay_clearpay"

	// PayPal
	PaymentMethodTypePayPal = "paypal"

	// Others
	PaymentMethodTypeBoleto        = "boleto"          // Brazilian payment method
	PaymentMethodTypeOxxo          = "oxxo"            // Mexican payment method
	PaymentMethodTypeGrabPay       = "grabpay"         // Southeast Asia-based
	PaymentMethodTypePayNow        = "paynow"          // Singapore payment method
	PaymentMethodTypePix           = "pix"             // Brazil's payment method
	PaymentMethodTypeBLIK          = "blik"            // Poland payment method
	PaymentMethodTypeUSBankAccount = "us_bank_account" // ACH bank account payments
	PaymentMethodTypeKonbini       = "konbini"        // Japan convenience stores
	PaymentMethodTypePayPay        = "paypay"          // Japan
	PaymentMethodTypePromptPay     = "promptpay"       // Thailand
	PaymentMethodTypeSwish         = "swish"           // Sweden
	PaymentMethodTypeTWINT         = "twint"           // Switzerland
	PaymentMethodTypeKRCard        = "kr_card"         // South Korea local cards
)

// paymentMethodCurrencies: per Stripe docs, which currencies each method supports.
// Card supports "most currencies" — treat as universal fallback.
// https://docs.stripe.com/payments/payment-methods/payment-method-support
var paymentMethodCurrencies = map[string]map[string]bool{
	// Buy now, pay later
	PaymentMethodTypeAffirm: {
		"cad": true, "usd": true,
	},
	PaymentMethodTypeAfterpayClearpay: {
		"aud": true, "cad": true, "eur": true, "nzd": true, "gbp": true, "usd": true,
	},
	PaymentMethodTypeKlarna: {
		"aud": true, "cad": true, "chf": true, "czk": true,
		"dkk": true, "eur": true, "gbp": true, "nok": true,
		"nzd": true, "pln": true, "ron": true, "sek": true, "usd": true,
	},
	// Wallets
	PaymentMethodTypeAlipay: {
		"aud": true, "cad": true, "cny": true, "eur": true, "gbp": true,
		"hkd": true, "jpy": true, "myr": true, "nzd": true, "sgd": true, "usd": true,
	},
	PaymentMethodTypeWechatPay: {
		"aud": true, "cad": true, "chf": true, "cny": true, "dkk": true,
		"eur": true, "gbp": true, "hkd": true, "jpy": true, "nok": true,
		"sek": true, "sgd": true, "usd": true,
	},
	PaymentMethodTypePayPal: {
		"aud": true, "cad": true, "chf": true, "czk": true,
		"dkk": true, "eur": true, "gbp": true, "hkd": true,
		"nok": true, "nzd": true, "pln": true, "sek": true,
		"sgd": true, "usd": true,
	},
	PaymentMethodTypeGrabPay: {
		"myr": true, "sgd": true,
	},
	PaymentMethodTypeApplePay: {
		"aud": true, "cad": true, "chf": true, "cny": true, "eur": true, "gbp": true,
		"hkd": true, "jpy": true, "krw": true, "sgd": true, "usd": true,
	},
	// Bank redirects
	PaymentMethodTypeBancontact: {"eur": true},
	PaymentMethodTypeIdeal:      {"eur": true},
	PaymentMethodTypeEps:       {"eur": true},
	PaymentMethodTypeP24:       {"eur": true, "pln": true},
	PaymentMethodTypeFpx:       {"myr": true},
	PaymentMethodTypeSepaDebit: {"eur": true},
	PaymentMethodTypeMultibanco: {"eur": true},
	PaymentMethodTypeBLIK:      {"pln": true},
	// Bank debits
	PaymentMethodTypeBacsDebit: {"gbp": true},
	PaymentMethodTypeAcssDebit: {"cad": true, "usd": true},
	PaymentMethodTypeUSBankAccount: {"usd": true},
	// Vouchers / regional
	PaymentMethodTypeBoleto: {"brl": true},
	PaymentMethodTypeOxxo:   {"mxn": true},
	PaymentMethodTypePix:    {"brl": true},
	PaymentMethodTypePayNow:  {"sgd": true},
	PaymentMethodTypeKonbini: {"jpy": true}, // Konbini: convenience stores in Japan
	PaymentMethodTypePayPay:  {"jpy": true}, // PayPay: Japan
	PaymentMethodTypePromptPay: {"thb": true}, // Thailand
	PaymentMethodTypeSwish:   {"sek": true},  // Sweden
	PaymentMethodTypeTWINT:   {"chf": true},  // Switzerland
	PaymentMethodTypeKRCard:  {"krw": true},  // South Korea local cards (Shinhan, Hyundai, Samsung, etc.)
}

// PaymentMethodTypesForCurrency returns payment method types that support the given currency.
// Card is always included; other methods are filtered by their supported currencies.
// Builds the result on demand from paymentMethodCurrencies.
func PaymentMethodTypesForCurrency(currency string) []string {
	cur := strings.ToLower(currency)
	out := []string{PaymentMethodTypeCard}
	for pm, supported := range paymentMethodCurrencies {
		if supported[cur] {
			out = append(out, pm)
		}
	}
	return out
}
