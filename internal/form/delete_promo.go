package form

import (
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"

	v "github.com/go-ozzo/ozzo-validation/v4"
)

type DisablePromoCodeRequest struct {
	*pb_admin.DisablePromoCodeRequest
}

func (r *DisablePromoCodeRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Code, v.Required, v.Length(1, 50)),
	)
}
