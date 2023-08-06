package dto

import (
	"time"

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
	ProductImages  []Image
}

type Price struct {
	USD  decimal.Decimal
	EUR  decimal.Decimal
	USDC decimal.Decimal
	ETH  decimal.Decimal
	Sale decimal.Decimal
}

type Image struct {
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
