package form

import (
	"errors"
	"net/url"
	"strings"

	v "github.com/go-ozzo/ozzo-validation/v4"
	pb_admin "github.com/jekabolt/grbpwr-manager/proto/gen/admin"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AddProductRequest struct {
	*pb_admin.AddProductRequest
}

func (f *AddProductRequest) Validate() error {
	if f == nil ||
		f.AddProductRequest == nil ||
		f.AvailableSizes == nil ||
		f.ProductMedia == nil ||
		f.Price == nil {
		return status.Error(codes.InvalidArgument, "request or product is nil")
	}

	return v.ValidateStruct(f,
		// Validate Price
		v.Field(&f.Price, v.Required, v.By(func(value interface{}) error {
			price, ok := value.(*pb_common.Price)
			if !ok || price == nil {
				return errors.New("invalid price")
			}

			// Convert string prices to decimal.Decimal and check their validity
			usd, err := decimal.NewFromString(price.Usd)
			if err != nil || usd.IsNegative() || usd.IsZero() {
				return errors.New("USD price must be a positive decimal number")
			}

			eur, err := decimal.NewFromString(price.Eur)
			if err != nil || eur.IsNegative() || eur.IsZero() {
				return errors.New("EUR price must be a positive decimal number")
			}

			usdc, err := decimal.NewFromString(price.Usdc)
			if err != nil || usdc.IsNegative() || usdc.IsZero() {
				return errors.New("USDC price must be a positive decimal number")
			}

			eth, err := decimal.NewFromString(price.Eth)
			if err != nil || eth.IsNegative() || eth.IsZero() {
				return errors.New("ETH price must be a positive decimal number")
			}

			return nil
		})),
		// Validate Sizes
		v.Field(&f.AvailableSizes, v.Required, v.By(func(value interface{}) error {
			size, ok := value.(*pb_common.Size)
			if !ok || size == nil {
				return errors.New("invalid sizes")
			}
			totalSize := size.Xxs + size.Xs + size.S + size.M + size.L + size.Xl + size.Xxl + size.Os
			if totalSize == 0 {
				return errors.New("total sum of sizes must be greater than zero")
			}
			return nil
		})),
		// Validate Media
		v.Field(&f.ProductMedia, v.Required, v.Length(1, 0), v.By(func(value interface{}) error {
			medias, ok := value.([]*pb_common.Media)
			if !ok {
				return errors.New("invalid media array")
			}
			for _, media := range medias {
				if _, err := url.ParseRequestURI(media.FullSize); err != nil {
					return errors.New("invalid FullSize URL")
				}

				// Check if FullSize URL has .mp4 or .webm extension
				if !strings.HasSuffix(media.FullSize, ".mp4") && !strings.HasSuffix(media.FullSize, ".webm") {
					if _, err := url.ParseRequestURI(media.Thumbnail); err != nil {
						return errors.New("invalid Thumbnail URL")
					}
					if _, err := url.ParseRequestURI(media.Compressed); err != nil {
						return errors.New("invalid Compressed URL")
					}
				}
			}
			return nil
		})),

		v.Field(&f.ProductMedia, v.By(func(value interface{}) error {
			medias, ok := value.([]*pb_common.Media)
			if ok {
				mediaSet := make(map[string]bool)
				for _, media := range medias {
					if mediaSet[media.FullSize] || mediaSet[media.Thumbnail] || mediaSet[media.Compressed] {
						return errors.New("duplicate media URLs are not allowed")
					}
					mediaSet[media.FullSize] = true
					mediaSet[media.Thumbnail] = true
					mediaSet[media.Compressed] = true
				}
			}
			return nil
		})),
		// Validate other fields
		v.Field(&f.Name, v.Length(1, 100)),
		v.Field(&f.Description, v.Length(1, 500)),
		v.Field(&f.Categories, v.Required, v.Length(1, 0)),
	)
}
