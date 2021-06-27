package store

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

func isCategoryExist(json string, category string) bool {
	return strings.Contains(gjson.Get(json, "categories").String(), category)
}

func (p *Product) String() string {
	bs, _ := json.Marshal(p)
	return string(bs)
}

// func CustomMarshalArrayOfProducts(products []string) []byte {
// 	resp := "{"
// 	for _, p := range products {
// 		resp = append(resp,)
// 	}
// 	resp  = append(resp,"}")

// 	return []byte(resp)
// }

func getProductFromString(product string) Product {
	p := &Product{}
	json.Unmarshal([]byte(product), p)
	return *p
}

func (p *Product) Validate() url.Values {

	err := url.Values{}

	if len(p.Categories) == 0 {
		err.Add("Categories", "validateProduct:empty categories")
	}

	if len(p.ImageURLs) == 0 {
		err.Add("ImageURLs", "validateProduct:no images")
	}

	if len(p.MainImage) == 0 {
		err.Add("MainImage", "validateProduct:no main image")
	}

	if len(p.Description) == 0 {
		err.Add("Description", "validateProduct:no description")
	}

	if len(p.Name) == 0 {
		err.Add("Name", "validateProduct:empty name")
	}

	if p.Price.BYN == 0 || p.Price.EUR == 0 || p.Price.USD == 0 || p.Price.RUB == 0 {
		err.Add("Price", fmt.Sprintf("validateProduct:prices were not set [%+v]", p.Price))
	}

	return err

}
