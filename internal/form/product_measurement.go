package form

import (
	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

// AddProductMeasurementRequest is your validation wrapper around the proto message.
type AddProductMeasurementRequest struct {
	*pb_admin.AddProductMeasurementRequest
}

// Validate performs field validation for AddProductMeasurementRequest
func (r AddProductMeasurementRequest) Validate() error {
	return ValidateStruct(&r,
		v.Field(&r.ProductId, v.Required, v.Min(1)),
		v.Field(&r.SizeId, v.Required, v.Min(1)),
		v.Field(&r.MeasurementNameId, v.Required, v.Min(1)),
		v.Field(&r.MeasurementValue, v.Required, v.Length(1, 1000)),
	)
}
