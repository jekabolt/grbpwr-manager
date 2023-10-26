package form

import (
	"regexp"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type UpdateProductColorAndColorHexRequest struct {
	*pb_admin.UpdateProductColorAndColorHexRequest
}

func (r *UpdateProductColorAndColorHexRequest) Validate() error {
	return v.ValidateStruct(r,
		v.Field(&r.ProductID, v.Required, v.Min(1)),
		v.Field(&r.Color, v.Required, v.Length(1, 100)),
		v.Field(&r.ColorHex, v.Required, v.Match(regexp.MustCompile("^#([a-fA-F0-9]{6}|[a-fA-F0-9]{3})$"))),
	)
}
