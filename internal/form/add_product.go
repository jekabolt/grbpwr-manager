package form

import (
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type AddProductRequest struct {
	*pb_admin.AddProductRequest
}

func (f *AddProductRequest) Validate() error {
	// priceRegex, err := regexp.Compile(`^\d+(\.\d{1,2})?$`)
	// if err != nil {
	// 	return err
	// }
	// fmt.Printf("---- %v \n\n\n", f)
	// prd := f.AddProductRequest.Product
	// return ValidateStruct(f,
	// 	v.Field(&prd.Product.Name, v.Required, v.Length(1, 100)),
	// 	v.Field(&prd.Product.Brand, v.Required, v.Length(1, 100)),
	// 	v.Field(&prd.Product.Sku, v.Required, v.Length(1, 100)),
	// 	v.Field(&prd.Product.Color, v.Required),
	// 	v.Field(&prd.Product.CountryOfOrigin, v.Required),
	// 	v.Field(&prd.Product.Price, v.Required, v.Match(priceRegex)),
	// 	v.Field(&prd.Product.CategoryId, v.Required),
	// 	v.Field(&prd.Product.Description, v.Required),
	// 	v.Field(&prd.Product.TargetGender, v.Required, v.In(pb_common.GenderEnum_MALE, pb_common.GenderEnum_FEMALE, pb_common.GenderEnum_UNISEX)),
	// 	v.Field(&prd.SizeMeasurements, v.Required), // 8 sizes 10 measurements
	// 	v.Field(&prd.Media, v.Required),
	// 	v.Field(&prd.Tags, v.Required),
	// )
	return nil
}
