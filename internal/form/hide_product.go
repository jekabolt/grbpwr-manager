package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type HideProductByIDRequest struct {
	*pb_admin.HideProductByIDRequest
}

func (r *HideProductByIDRequest) Validate() error {
	return ValidateStruct(r,
		v.Field(&r.Id, v.Required, v.Min(1)),
		v.Field(&r.Hide, v.Required),
	)
}
