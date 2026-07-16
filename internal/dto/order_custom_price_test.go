package dto

import (
	"testing"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

// TestConvertCustomOrderItemPositivePrice is part of the acceptance for problem 044: a custom order
// line must carry a strictly positive price. Zero and negative custom_price are rejected at the DTO
// boundary (the admin handler maps the error to InvalidArgument); a positive price converts cleanly.
func TestConvertCustomOrderItemPositivePrice(t *testing.T) {
	mk := func(price string) *pb_common.CustomOrderItemInsert {
		return &pb_common.CustomOrderItemInsert{
			ProductId:   1,
			Quantity:    1,
			SizeId:      1,
			CustomPrice: &pb_decimal.Decimal{Value: price},
		}
	}

	rejected := []string{"0", "0.00", "-1", "-0.01"}
	for _, p := range rejected {
		if _, err := ConvertCustomOrderItemInsertToEntity(mk(p)); err == nil {
			t.Errorf("custom_price %q: expected rejection, got nil error", p)
		}
	}

	accepted := []string{"0.01", "1", "199.99"}
	for _, p := range accepted {
		got, err := ConvertCustomOrderItemInsertToEntity(mk(p))
		if err != nil {
			t.Errorf("custom_price %q: unexpected error %v", p, err)
			continue
		}
		if !got.ProductPrice.IsPositive() {
			t.Errorf("custom_price %q: ProductPrice not positive: %s", p, got.ProductPrice)
		}
		if got.ProductPrice.Cmp(got.ProductPriceWithSale) != 0 {
			t.Errorf("custom_price %q: price/with-sale mismatch %s vs %s", p, got.ProductPrice, got.ProductPriceWithSale)
		}
	}

	// a missing custom_price is still required
	if _, err := ConvertCustomOrderItemInsertToEntity(&pb_common.CustomOrderItemInsert{ProductId: 1}); err == nil {
		t.Errorf("nil custom_price: expected error, got nil")
	}
}
