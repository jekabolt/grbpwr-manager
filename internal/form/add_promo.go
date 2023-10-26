package form

import (
	"fmt"
	"regexp"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type AddPromoRequest struct {
	*pb_admin.AddPromoRequest
}

func (r *AddPromoRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.AddPromoRequest.Promo, v.Required, v.By(validatePromoCodeInsert)),
	)
}

func validatePromoCodeInsert(value interface{}) error {
	promo, ok := value.(*pb_common.PromoCodeInsert)
	if !ok {
		return fmt.Errorf("invalid type for AddPromoRequest")
	}

	discountRegex, err := regexp.Compile(`^\d+(\.\d{1,2})?$`)
	if err != nil {
		return err
	}

	return ValidateStruct(promo,
		v.Field(&promo.Code, v.Required, v.Length(1, 50)),
		v.Field(&promo.FreeShipping),
		v.Field(&promo.Discount, v.Match(discountRegex)),
		v.Field(&promo.Expiration, v.Required),
		v.Field(&promo.Allowed),
	)
}
