package store

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/bucket"
	"github.com/tidwall/gjson"
)

type Product struct {
	Id                  int64            `json:"id"`
	DateCreated         int64            `json:"dateCreated"`
	LastActionTime      int64            `json:"lat"`
	MainImage           bucket.MainImage `json:"mainImage"`
	Name                string           `json:"name"`
	Price               *Price           `json:"price"`
	AvailableSizes      *Size            `json:"availableSizes"`
	ShortDescription    string           `json:"shortDescription,omitempty"`
	DetailedDescription []string         `json:"detailedDescription,omitempty"`
	Categories          []string         `json:"categories,omitempty"`
	ProductImages       []bucket.Image   `json:"productImages,omitempty"`
}

type Price struct {
	USD float64 `json:"usd"`
	RUB float64 `json:"rub"`
	BYN float64 `json:"byn"`
	EUR float64 `json:"eur"`
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

func (p *Product) String() string {
	bs, _ := json.Marshal(p)
	return string(bs)
}

func isCategoryExist(json string, category string) bool {
	return strings.Contains(gjson.Get(json, "categories").String(), category)
}

func getProductFromString(product string) *Product {
	p := &Product{}
	json.Unmarshal([]byte(product), p)
	return p
}

func (p *Product) Validate() error {

	if len(p.Categories) == 0 {
		return fmt.Errorf("missing categories")
	}

	if len(p.ProductImages) == 0 {
		return fmt.Errorf("missing product images")
	}

	if len(p.MainImage.FullSize) == 0 {
		return fmt.Errorf("missing main image")
	}

	if len(p.ShortDescription) == 0 {
		return fmt.Errorf("missing description")
	}
	if len(p.DetailedDescription) == 0 {
		return fmt.Errorf("missing description")
	}

	if len(p.Name) == 0 {
		return fmt.Errorf("missing Name")
	}

	if p.Price.BYN == 0 || p.Price.EUR == 0 || p.Price.USD == 0 || p.Price.RUB == 0 {
		return fmt.Errorf("prices were not set [%+v]", p.Price)
	}

	return nil

}
