package form

import (
	"regexp"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

type AddProductRequest struct {
	*pb_admin.AddProductRequest
}

func (f *AddProductRequest) Validate() error {
	priceRegex, err := regexp.Compile(`^\d+(\.\d{1,2})?$`)
	if err != nil {
		return err
	}
	return ValidateStruct(f,
		v.Field(&f.Product, v.Required),
		v.Field(&f.Product.Product.Name, v.Required, v.Length(1, 100)),
		v.Field(&f.Product.Product.Brand, v.Required, v.Length(1, 100)),
		v.Field(&f.Product.Product.Sku, v.Required, v.Length(1, 100)),
		v.Field(&f.Product.Product.Color, v.Required, v.Length(1, 100)),
		v.Field(&f.Product.Product.CountryOfOrigin, v.Required, v.Length(1, 100)),
		v.Field(&f.Product.Product.Price, v.Required, v.Match(priceRegex)),
		v.Field(&f.Product.Product.CategoryId, v.Required, v.Min(1)),
		v.Field(&f.Product.Product.Description, v.Required, v.Length(1, 700)),
		v.Field(&f.Product.Product.TargetGender, v.Required, v.In(pb_common.GenderEnum_MALE, pb_common.GenderEnum_FEMALE, pb_common.GenderEnum_UNISEX)),
		v.Field(&f.Product.SizeMeasurements, v.Required, v.Length(0, 80)), // 8 sizes 10 measurements
		v.Field(&f.Product.Media, v.Required, v.Length(1, 20)),
		v.Field(&f.Product.Tags, v.Required, v.Length(0, 20)),
	)
}
