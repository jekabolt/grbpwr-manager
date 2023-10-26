package form

import (
	"fmt"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type UpdateProductTargetGenderRequest struct {
	*pb_admin.UpdateProductTargetGenderRequest
}

func (r *UpdateProductTargetGenderRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductID, v.Required, v.Min(1)),
		v.Field(&r.Gender, v.Required, v.By(validateGender)),
	)
}

func validateGender(value interface{}) error {
	gender, ok := value.(pb_common.GenderEnum)
	if !ok {
		return fmt.Errorf("gender not found")
	}
	switch gender {
	case pb_common.GenderEnum_MALE, pb_common.GenderEnum_FEMALE, pb_common.GenderEnum_UNISEX:
		return nil
	default:
		return fmt.Errorf("invalid gender")
	}
}
