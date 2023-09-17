package dto

import (
	"fmt"
	"time"

	common_pb "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

type Product struct {
	ProductInfo    *ProductInfo
	Price          *Price
	AvailableSizes *Size
	Categories     []Category
	Media          []Media
}

type ProductInfo struct {
	Id          int32     `db:"id"`
	Created     time.Time `db:"created_at"`
	Name        string    `db:"name"`
	Preorder    string    `db:"preorder"`
	Description string    `db:"description"`
	Hidden      bool      `db:"hidden"`
}

type Price struct {
	Id        int32           `db:"id"`
	ProductID int32           `db:"product_id"`
	USD       decimal.Decimal `db:"USD"`
	EUR       decimal.Decimal `db:"EUR"`
	USDC      decimal.Decimal `db:"USDC"`
	ETH       decimal.Decimal `db:"ETH"`
	Sale      decimal.Decimal `db:"sale"`
}

type Media struct {
	Id         int32  `db:"id"`
	ProductID  int32  `db:"product_id"`
	FullSize   string `db:"full_size"`
	Thumbnail  string `db:"thumbnail"`
	Compressed string `db:"compressed"`
}

type Size struct {
	Id        int32 `db:"id"`
	ProductID int32 `db:"product_id"`
	XXS       int   `db:"XXS"`
	XS        int   `db:"XS"`
	S         int   `db:"S"`
	M         int   `db:"M"`
	L         int   `db:"L"`
	XL        int   `db:"XL"`
	XXL       int   `db:"XXL"`
	OS        int   `db:"OS"`
}

type Category struct {
	Id        int32  `db:"id"`
	ProductID int32  `db:"product_id"`
	Category  string `db:"category"`
}

func ConvertProtoSize(size *common_pb.Size) *Size {
	return &Size{
		XXS: int(size.Xxs),
		XS:  int(size.Xs),
		S:   int(size.S),
		M:   int(size.M),
		L:   int(size.L),
		XL:  int(size.Xl),
		XXL: int(size.Xxl),
		OS:  int(size.Os),
	}
}

func ConvertProtoPrice(size *common_pb.Price) (*Price, error) {
	USD, err := decimal.NewFromString(size.Usd)
	if err != nil {
		return nil, fmt.Errorf("could not convert USD price: %w", err)
	}
	EUR, err := decimal.NewFromString(size.Eur)
	if err != nil {
		return nil, fmt.Errorf("could not convert EUR price: %w", err)
	}
	USDC, err := decimal.NewFromString(size.Usdc)
	if err != nil {
		return nil, fmt.Errorf("could not convert USDC price: %w", err)
	}
	ETH, err := decimal.NewFromString(size.Eth)
	if err != nil {
		return nil, fmt.Errorf("could not convert ETH price: %w", err)
	}
	Sale, err := decimal.NewFromString(size.Sale)
	if err != nil {
		return nil, fmt.Errorf("could not convert Sale price: %w", err)
	}

	return &Price{
		USD:  USD,
		EUR:  EUR,
		USDC: USDC,
		ETH:  ETH,
		Sale: Sale,
	}, nil
}

func ConvertProtoMediaArray(media []*common_pb.Media) []Media {
	var dtoMedia []Media
	for _, media := range media {
		dtoMedia = append(dtoMedia, Media{
			FullSize:   media.FullSize,
			Thumbnail:  media.Thumbnail,
			Compressed: media.Compressed,
		})
	}
	return dtoMedia
}
