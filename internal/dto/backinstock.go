package dto

import (
	"encoding/base64"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/canonical"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/internal/slug"
)

// BackInStock represents the data needed for back-in-stock email notifications
type BackInStock struct {
	Preheader   string
	BuyerName   string // First name or full name if available
	ProductName string
	Brand       string
	Size        string
	Thumbnail   string
	ProductURL  string
	EmailB64    string
}

// ProductFullToBackInStock converts entity.ColorwayFull to BackInStock DTO
func ProductFullToBackInStock(product *entity.ColorwayFull, sizeId int, buyerName string, email string) *BackInStock {
	// Use the same default-language canonical name as every public product URL.
	productName := "Product"
	if product.Product != nil {
		if name, ok := canonical.ProductName(product.Product.ProductDisplay.ProductBody.Translations, cache.GetLanguages()); ok {
			productName = name
		}
	}

	// Get brand
	brand := ""
	if product.Product != nil {
		brand = product.Product.ProductDisplay.ProductBody.ProductBodyInsert.Brand
	}

	// Get size name from cache
	size, ok := cache.GetSizeById(sizeId)
	sizeName := "Unknown"
	if ok {
		sizeName = size.Name
	}

	// Get thumbnail
	thumbnail := ""
	if product.Product != nil {
		thumbnail = product.Product.ProductDisplay.Thumbnail.ThumbnailMediaURL
	}

	// Generate product URL (/p/{pretty}-{base-sku})
	productURL := ""
	if product.Product != nil {
		productURL = fmt.Sprintf("https://grbpwr.com%s", slug.ProductPath(productName, product.Product.SKU))
	}

	return &BackInStock{
		Preheader:   "YOUR WAITLIST ITEM IS BACK IN STOCK",
		BuyerName:   buyerName,
		ProductName: productName,
		Brand:       brand,
		Size:        sizeName,
		Thumbnail:   thumbnail,
		ProductURL:  productURL,
		EmailB64:    base64.StdEncoding.EncodeToString([]byte(email)),
	}
}
