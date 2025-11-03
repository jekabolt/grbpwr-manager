package dto

import (
	"encoding/base64"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
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

// ProductFullToBackInStock converts entity.ProductFull to BackInStock DTO
func ProductFullToBackInStock(product *entity.ProductFull, sizeId int, buyerName string, email string) *BackInStock {
	// Get product name from first translation
	productName := "Product"
	if product.Product != nil && len(product.Product.ProductDisplay.ProductBody.Translations) > 0 {
		productName = product.Product.ProductDisplay.ProductBody.Translations[0].Name
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

	// Generate product URL
	productURL := ""
	if product.Product != nil {
		gender := product.Product.ProductDisplay.ProductBody.ProductBodyInsert.TargetGender.String()
		productURL = fmt.Sprintf("https://grbpwr.com%s", GetProductSlug(
			product.Product.Id,
			brand,
			productName,
			gender,
		))
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
