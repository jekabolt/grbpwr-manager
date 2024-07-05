package stripe

type PaymentMethodType string

const (
	PaymentMethodTypeCard             PaymentMethodType = "card"
	PaymentMethodTypeAchCredit        PaymentMethodType = "ach_credit_transfer"
	PaymentMethodTypeAchDebit         PaymentMethodType = "ach_debit"
	PaymentMethodTypeAlipay           PaymentMethodType = "alipay"
	PaymentMethodTypeBancontact       PaymentMethodType = "bancontact"
	PaymentMethodTypeBacsDebit        PaymentMethodType = "bacs_debit"
	PaymentMethodTypeEps              PaymentMethodType = "eps"
	PaymentMethodTypeFpx              PaymentMethodType = "fpx"
	PaymentMethodTypeGiropay          PaymentMethodType = "giropay"
	PaymentMethodTypeIdeal            PaymentMethodType = "ideal"
	PaymentMethodTypeMultibanco       PaymentMethodType = "multibanco"
	PaymentMethodTypeP24              PaymentMethodType = "p24"
	PaymentMethodTypeSepaDebit        PaymentMethodType = "sepa_debit"
	PaymentMethodTypeSofort           PaymentMethodType = "sofort"
	PaymentMethodTypeWeChatPay        PaymentMethodType = "wechat_pay"
	PaymentMethodTypeGrabPay          PaymentMethodType = "grabpay"
	PaymentMethodTypeAfterpayClearpay PaymentMethodType = "afterpay_clearpay"
	PaymentMethodTypeBoleto           PaymentMethodType = "boleto"
	PaymentMethodTypeOxxo             PaymentMethodType = "oxxo"
	PaymentMethodTypeKlarna           PaymentMethodType = "klarna"
)
