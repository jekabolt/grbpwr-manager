package dto

import (
	"database/sql"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
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
		IsTransactionDone: pbPayment.IsTransactionDone,
		ExpiredAt:         sql.NullTime{Time: pbPayment.ExpiredAt.AsTime(), Valid: pbPayment.ExpiredAt != nil},
	}, nil
}

func ConvertEntityToPbPayment(p entity.Payment) (*pb_common.Payment, error) {
	pi, err := ConvertEntityToPbPaymentInsert(&p.PaymentInsert)
	if err != nil {
		return nil, err
	}

	return &pb_common.Payment{
		CreatedAt:     timestamppb.New(p.CreatedAt),
		ModifiedAt:    timestamppb.New(p.ModifiedAt),
		PaymentInsert: pi,
	}, nil
}

func ConvertEntityToPbPaymentInsert(p *entity.PaymentInsert) (*pb_common.PaymentInsert, error) {
	if p == nil {
		return nil, fmt.Errorf("empty entity.PaymentInsert")
	}
	pm, ok := cache.GetPaymentMethodById(p.PaymentMethodID)
	if !ok {
		return nil, fmt.Errorf("payment method not found")
	}

	return &pb_common.PaymentInsert{
		PaymentMethod:                    pm.PB,
		TransactionId:                    p.TransactionID.String,
		TransactionAmount:                &pb_decimal.Decimal{Value: p.TransactionAmount.String()},
		TransactionAmountPaymentCurrency: &pb_decimal.Decimal{Value: p.TransactionAmountPaymentCurrency.String()},
		ClientSecret:                     p.ClientSecret.String,
		IsTransactionDone:                p.IsTransactionDone,
		ExpiredAt:                        timestamppb.New(p.ExpiredAt.Time),
	}, nil
}

// TODO:
var paymentMethodToCurrency = map[pb_common.PaymentMethodNameEnum]string{
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:       "EUR",
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST:  "EUR",
}

var pbPaymentMethodToEntity = map[pb_common.PaymentMethodNameEnum]entity.PaymentMethodName{
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:      entity.CARD,
	pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST: entity.CARD_TEST,
}

func ConvertPaymentMethodToCurrency(pbPaymentMethod pb_common.PaymentMethodNameEnum) string {
	return paymentMethodToCurrency[pbPaymentMethod]
}

func ConvertPbPaymentMethodToEntity(pbPaymentMethod pb_common.PaymentMethodNameEnum) entity.PaymentMethodName {
	return pbPaymentMethodToEntity[pbPaymentMethod]
}
