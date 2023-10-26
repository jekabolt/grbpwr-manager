package form

import (
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type GetProductsPagedRequest struct {
	*pb_admin.GetProductsPagedRequest
}

func (r *GetProductsPagedRequest) Validate() error {
	return ValidateStruct(r)
}

// func validateFilterConditions(value interface{}) error {
// 	conditions, ok := value.(*pb_common.FilterConditions)
// 	if !ok {
// 		return fmt.Errorf("invalid type for FilterConditions eblo")
// 	}

// 	return ValidateStruct(conditions,
// 		v.Field(&conditions.PriceFromTo, v.By(validatePriceFromTo)),
// 		v.Field(&conditions.OnSale),
// 		v.Field(&conditions.Color, v.Length(0, 50)),
// 		v.Field(&conditions.CategoryId, v.Min(0)),
// 		v.Field(&conditions.SizesIds, v.Each(v.Min(1))),
// 		v.Field(&conditions.Preorder),
// 		v.Field(&conditions.ByTag, v.Length(0, 50)),
// 	)
// }

// func validatePriceFromTo(value interface{}) error {
// 	price, ok := value.(*pb_common.PriceFromTo)
// 	if !ok {
// 		return fmt.Errorf("invalid type for PriceFromTo")
// 	}

// 	priceRegex, err := regexp.Compile(`^\d+(\.\d{1,2})?$`)
// 	if err != nil {
// 		return err
// 	}

// 	return ValidateStruct(price,
// 		v.Field(&price.From, v.Match(priceRegex)),
// 		v.Field(&price.To, v.Match(priceRegex)),
// 	)
// }
