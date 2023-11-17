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
		PaymentMethodID:   int(pbPayment.PaymentMethodId),
		TransactionID:     sql.NullString{String: pbPayment.TransactionId, Valid: pbPayment.TransactionId != ""},
		TransactionAmount: transactionAmount,
		Payer:             sql.NullString{String: pbPayment.Payer, Valid: pbPayment.Payer != ""},
		Payee:             sql.NullString{String: pbPayment.Payee, Valid: pbPayment.Payee != ""},
		IsTransactionDone: pbPayment.IsTransactionDone,
	}, nil
}
