package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ConvertPbCommonToEntity converts a PromoCodeInsert object from pb_common to entity.
func ConvertPbCommonPromoToEntity(pbPromo *pb_common.PromoCodeInsert) (*entity.PromoCodeInsert, error) {
	// Convert the Discount field from string to decimal.Decimal
	discountDecimal, err := decimal.NewFromString(pbPromo.Discount)
	if err != nil {
		return nil, fmt.Errorf("error converting discount to decimal: %v", err)
	}

	// Create the entity.PromoCodeInsert object and populate its fields from the pb_common.PromoCodeInsert object
	entityPromo := &entity.PromoCodeInsert{
		Code:         pbPromo.Code,
		FreeShipping: pbPromo.FreeShipping,
		Discount:     discountDecimal,
		Expiration:   pbPromo.Expiration,
		Allowed:      pbPromo.Allowed,
	}

	return entityPromo, nil
}

// ConvertEntityToPb converts an entity.PromoCode to pb_common.PromoCode
func ConvertEntityPromoToPb(entityPromo *entity.PromoCode) *pb_common.PromoCode {
	// Convert decimal.Decimal to string for protobuf
	discountStr := entityPromo.Discount.String()

	// Create pb_common.PromoCodeInsert
	pbPromoInsert := &pb_common.PromoCodeInsert{
		Code:         entityPromo.Code,
		FreeShipping: entityPromo.FreeShipping,
		Discount:     discountStr,
		Expiration:   entityPromo.Expiration,
		Allowed:      entityPromo.Allowed,
	}

	// Create pb_common.PromoCode
	pbPromo := &pb_common.PromoCode{
		Id:              int32(entityPromo.ID),
		PromoCodeInsert: pbPromoInsert,
	}

	return pbPromo
}
