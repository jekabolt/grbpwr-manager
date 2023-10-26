package form

import (
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"

	v "github.com/go-ozzo/ozzo-validation/v4"
)

type DeletePromoCodeRequest struct {
	*pb_admin.DeletePromoCodeRequest
}

func (r *DeletePromoCodeRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Code, v.Required, v.Length(1, 50)),
	)
}
