package form

import (
	"regexp"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
)

type SetSaleByIDRequest struct {
	*pb_admin.SetSaleByIDRequest
}

func (r *SetSaleByIDRequest) Validate() error {
	salePercentRegex, err := regexp.Compile(`^\d+(\.\d{1,2})?$`)
	if err != nil {
		return err
	}
	return v.ValidateStruct(r,
		v.Field(&r.Id, v.Required, v.Min(1)),
		v.Field(&r.SalePercent, v.Required, v.Match(salePercentRegex)),
	)
}
