package stripe

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
)

var (
	// PaymentMethodTypes is a list of all supported payment method types
	PaymentMethodTypes = []string{
		PaymentMethodTypeCard,
		// PaymentMethodTypeGooglePay,
		// PaymentMethodTypeApplePay,
		PaymentMethodTypeKlarna,
		// PaymentMethodTypeWechatPay,
		PaymentMethodTypePayPal,
	}
)
