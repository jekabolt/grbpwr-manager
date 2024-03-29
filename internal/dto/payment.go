package dto

import (
	"database/sql"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

func ConvertToEntityPaymentInsert(pbPayment *pb_common.PaymentInsert) (*entity.PaymentInsert, error) {
	// Convert TransactionAmount from pb_common's Decimal to shopspring's Decimal
	transactionAmount, err := decimal.NewFromString(pbPayment.TransactionAmount.GetValue())
	if err != nil {
		return nil, err
	}

	return &entity.PaymentInsert{
		PaymentMethodID:   int(pbPayment.PaymentMethod),
		TransactionID:     sql.NullString{String: pbPayment.TransactionId, Valid: pbPayment.TransactionId != ""},
		TransactionAmount: transactionAmount,
		Payer:             sql.NullString{String: pbPayment.Payer, Valid: pbPayment.Payer != ""},
		Payee:             sql.NullString{String: pbPayment.Payee, Valid: pbPayment.Payee != ""},
		IsTransactionDone: pbPayment.IsTransactionDone,
	}, nil
}

var paymentMethodToCurrency = map[pb_common.PaymentMethodNameEnum]string{
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:        "EUR",
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH:         "ETH",
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON:   "USDT",
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA: "USDT_TESTNET",
}

var pbPaymentMethodToEntity = map[pb_common.PaymentMethodNameEnum]entity.PaymentMethodName{
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:        entity.CARD,
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH:         entity.ETH,
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON:   entity.USDT_TRON,
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA: entity.USDT_TRON_TEST,
}

func ConvertPaymentMethodToCurrency(pbPaymentMethod pb_common.PaymentMethodNameEnum) string {
	return paymentMethodToCurrency[pbPaymentMethod]
}

func ConvertPbPaymentMethodToEntity(pbPaymentMethod pb_common.PaymentMethodNameEnum) entity.PaymentMethodName {
	return pbPaymentMethodToEntity[pbPaymentMethod]
}
