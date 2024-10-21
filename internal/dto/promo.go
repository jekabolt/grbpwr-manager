package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ConvertPbCommonToEntity converts a PromoCodeInsert object from pb_common to entity.
func ConvertPbCommonPromoToEntity(pbPromo *pb_common.PromoCodeInsert) (*entity.PromoCodeInsert, error) {
	// Convert the Discount field from string to decimal.Decimal
	discountDecimal, err := decimal.NewFromString(pbPromo.Discount.Value)
	if err != nil {
		return nil, fmt.Errorf("error converting discount to decimal: %v", err)
	}

	// Create the entity.PromoCodeInsert object and populate its fields from the pb_common.PromoCodeInsert object
	entityPromo := &entity.PromoCodeInsert{
		Code:         pbPromo.Code,
		FreeShipping: pbPromo.FreeShipping,
		Discount:     discountDecimal,
		Expiration:   pbPromo.Expiration.AsTime(),
		Start:        pbPromo.Start.AsTime(),
		Allowed:      pbPromo.Allowed,
		Voucher:      pbPromo.Voucher,
	}

	return entityPromo, nil
}

// ConvertEntityToPb converts an entity.PromoCode to pb_common.PromoCode
func ConvertEntityPromoToPb(entityPromo entity.PromoCode) *pb_common.PromoCode {
	pbPromo := &pb_common.PromoCode{
		PromoCodeInsert: ConvertEntityPromoInsertToPb(entityPromo.PromoCodeInsert),
	}

	return pbPromo
}

func ConvertEntityPromoInsertToPb(entityPromo entity.PromoCodeInsert) *pb_common.PromoCodeInsert {
	// Convert decimal.Decimal to string for protobuf
	discountStr := entityPromo.Discount.String()

	// Create pb_common.PromoCodeInsert
	pbPromoInsert := &pb_common.PromoCodeInsert{
		Code:         entityPromo.Code,
		FreeShipping: entityPromo.FreeShipping,
		Discount:     &pb_decimal.Decimal{Value: discountStr},
		Expiration:   timestamppb.New(entityPromo.Expiration),
		Start:        timestamppb.New(entityPromo.Start),
		Allowed:      entityPromo.Allowed,
		Voucher:      entityPromo.Voucher,
	}

	return pbPromoInsert
}
