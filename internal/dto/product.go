package dto

import (
	"fmt"
	"time"

	common_pb "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

type Product struct {
	Id             int64
	Created        time.Time
	Name           string
	Preorder       string
	Price          *Price
	AvailableSizes *Size
	Description    string
	Categories     []string
	Media          []Media
}

type Price struct {
	USD  decimal.Decimal
	EUR  decimal.Decimal
	USDC decimal.Decimal
	ETH  decimal.Decimal
	Sale decimal.Decimal
}

type Media struct {
	FullSize   string `json:"fullSize"`
	Thumbnail  string `json:"thumbnail"`
	Compressed string `json:"compressed"`
}

type Size struct {
	XXS int `json:"xxs,omitempty"`
	XS  int `json:"xs,omitempty"`
	S   int `json:"s,omitempty"`
	M   int `json:"m,omitempty"`
	L   int `json:"l,omitempty"`
	XL  int `json:"xl,omitempty"`
	XXL int `json:"xxl,omitempty"`
	OS  int `json:"os,omitempty"`
}

// ValidateConvertProtoProduct converts a common.Product to dto.Product
func ValidateConvertProtoProduct(commonProduct *common_pb.Product) (*Product, error) {
	if commonProduct == nil {
		return nil, fmt.Errorf("invalid product equals nil")
	}

	var dtoPrice *Price
	if commonProduct.Price != nil {
		if commonProduct.Price.Usd == 0 ||
			commonProduct.Price.Eur == 0 ||
			commonProduct.Price.Usdc == 0 ||
			commonProduct.Price.Eth == 0 {
			return nil, fmt.Errorf("price cannot be zero")
		}
		dtoPrice = &Price{
			USD:  decimal.NewFromFloat(commonProduct.Price.Usd),
			EUR:  decimal.NewFromFloat(commonProduct.Price.Eur),
			USDC: decimal.NewFromFloat(commonProduct.Price.Usdc),
			ETH:  decimal.NewFromFloat(commonProduct.Price.Eth),
			Sale: decimal.NewFromFloat(commonProduct.Price.Sale),
		}
	} else {
		return nil, fmt.Errorf("invalid product price")
	}

	var dtoSize *Size
	if commonProduct.AvailableSizes != nil {
		dtoSize = &Size{
			XXS: int(commonProduct.AvailableSizes.Xxs),
			XS:  int(commonProduct.AvailableSizes.Xs),
			S:   int(commonProduct.AvailableSizes.S),
			M:   int(commonProduct.AvailableSizes.M),
			L:   int(commonProduct.AvailableSizes.L),
			XL:  int(commonProduct.AvailableSizes.Xl),
			XXL: int(commonProduct.AvailableSizes.Xxl),
			OS:  int(commonProduct.AvailableSizes.Os),
		}
	} else {
		return nil, fmt.Errorf("invalid available sizes")
	}

	var dtoMedia []Media
	for _, media := range commonProduct.ProductMedia {
		dtoMedia = append(dtoMedia, Media{
			FullSize:   media.FullSize,
			Thumbnail:  media.Thumbnail,
			Compressed: media.Compressed,
		})
	}

	dtoProduct := &Product{
		Name:           commonProduct.Name,
		Preorder:       commonProduct.Preorder,
		Price:          dtoPrice,
		AvailableSizes: dtoSize,
		Description:    commonProduct.Description,
		Categories:     commonProduct.Categories,
		Media:          dtoMedia,
	}

	return dtoProduct, nil
}
