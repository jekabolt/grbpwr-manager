package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
)

func getRandomProductInsert(c *entity.Category, i int) *entity.ProductInsert {
	return &entity.ProductInsert{
		Preorder:        sql.NullString{String: "", Valid: true},
		Name:            fmt.Sprintf("RandomName_%d", i),
		Brand:           "RandomBrand",
		SKU:             "SKU123",
		Color:           "Red",
		ColorHex:        "#FF0000",
		CountryOfOrigin: "USA",
		Thumbnail:       "thumbnail.jpg",
		Price:           decimal.NewFromInt(100),
		SalePercentage:  decimal.NullDecimal{Decimal: decimal.NewFromInt(0), Valid: false},
		CategoryID:      c.ID,
		Description:     "RandomDescription",
		Hidden:          sql.NullBool{Bool: false, Valid: true},
		TargetGender:    entity.Male,
	}
}

func getRandomSizeWithMeasurement(s *entity.Size, m *entity.MeasurementName) []entity.SizeWithMeasurementInsert {
	return []entity.SizeWithMeasurementInsert{
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(10),
				SizeID:   s.ID,
			},
			Measurements: []entity.ProductMeasurementInsert{
				{
					MeasurementNameID: m.ID,
					MeasurementValue:  decimal.NewFromFloat(10.5),
				},
			},
		},
	}
}

func getRandomEnumKeys(enumMap interface{}) []interface{} {
	keys := reflect.ValueOf(enumMap).MapKeys()
	randomKeys := make([]interface{}, len(keys))
	for i, key := range keys {
		randomKeys[i] = key.Interface()
	}
	return randomKeys
}

func getRandomCategory(db *MYSQLStore) (*entity.Category, error) {
	keys := getRandomEnumKeys(entity.ValidCategories)
	category := keys[rand.Intn(len(keys))].(entity.CategoryEnum)
	c, ok := db.cache.GetCategoryByName(category)
	if !ok {
		return nil, fmt.Errorf("category not found")
	}
	return &c, nil
}

func getRandomSize(db *MYSQLStore) (*entity.Size, error) {
	keys := getRandomEnumKeys(entity.ValidSizes)
	size := keys[rand.Intn(len(keys))].(entity.SizeEnum)
	s, ok := db.cache.GetSizesByName(size)
	if !ok {
		return nil, fmt.Errorf("size not found")
	}
	return &s, nil
}

func getRandomMeasurement(db *MYSQLStore) (*entity.MeasurementName, error) {
	keys := getRandomEnumKeys(entity.ValidMeasurementNames)
	measurementName := keys[rand.Intn(len(keys))].(entity.MeasurementNameEnum)
	m, ok := db.cache.GetMeasurementsByName(measurementName)
	if !ok {
		return nil, fmt.Errorf("measurement not found")
	}
	if m.Name == entity.Height {
		return getRandomMeasurement(db)
	}
	return &m, nil
}

func getRandomMedia() []entity.ProductMediaInsert {
	return []entity.ProductMediaInsert{
		{
			FullSize:   "full_size.jpg",
			Thumbnail:  "thumbnail.jpg",
			Compressed: "compressed.jpg",
		},
	}
}

func getRandomTags() []entity.ProductTagInsert {
	return []entity.ProductTagInsert{
		{
			Tag: "RandomTag",
		},
	}
}

func randomProductInsert(db *MYSQLStore, i int) (*entity.ProductNew, error) {
	c, err := getRandomCategory(db)
	if err != nil {
		return nil, err
	}
	s, err := getRandomSize(db)
	if err != nil {
		return nil, err
	}
	m, err := getRandomMeasurement(db)
	if err != nil {
		return nil, err
	}

	product := getRandomProductInsert(c, 1)

	sizeWithMeasurement := getRandomSizeWithMeasurement(s, m)
	media := getRandomMedia()
	tags := getRandomTags()

	return &entity.ProductNew{
		Product:          product,
		SizeMeasurements: sizeWithMeasurement,
		Media:            media,
		Tags:             tags,
	}, nil
}

func TestProductStore_AddProduct(t *testing.T) {
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	// Insert new product
	newPrd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err := ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	// Assertions on Product fields except IDs
	assert.Equal(t, np.Product.Preorder, pf.Product.Preorder)
	assert.Equal(t, np.Product.Name, pf.Product.Name)
	assert.Equal(t, np.Product.Brand, pf.Product.Brand)
	assert.Equal(t, np.Product.SKU, pf.Product.SKU)
	assert.Equal(t, np.Product.Color, pf.Product.Color)
	assert.Equal(t, np.Product.ColorHex, pf.Product.ColorHex)
	assert.Equal(t, np.Product.CountryOfOrigin, pf.Product.CountryOfOrigin)
	assert.Equal(t, np.Product.Thumbnail, pf.Product.Thumbnail)
	assert.True(t, np.Product.Price.Equal(pf.Product.Price))
	assert.True(t, pf.Product.SalePercentage.Valid)
	assert.True(t, np.Product.SalePercentage.Decimal.Equal(pf.Product.SalePercentage.Decimal))
	assert.Equal(t, np.Product.CategoryID, pf.Product.CategoryID)
	assert.Equal(t, np.Product.Description, pf.Product.Description)
	assert.Equal(t, np.Product.Hidden, pf.Product.Hidden)
	assert.Equal(t, np.Product.TargetGender, pf.Product.TargetGender)

	// Assertions on SizeMeasurements
	assert.Equal(t, len(np.SizeMeasurements), len(pf.Sizes))
	for i, sm := range np.SizeMeasurements {
		assert.True(t, sm.ProductSize.Quantity.Equal(pf.Sizes[i].Quantity))
		assert.Equal(t, sm.ProductSize.SizeID, pf.Sizes[i].SizeID)

		// Assuming Measurements is a slice with at least one element
		assert.True(t, sm.Measurements[0].MeasurementValue.Equal(pf.Measurements[i].MeasurementValue))
	}

	// Assertions on Media
	assert.Equal(t, len(np.Media), len(pf.Media))
	for i, m := range np.Media {
		assert.Equal(t, m.FullSize, pf.Media[i].FullSize)
		assert.Equal(t, m.Thumbnail, pf.Media[i].Thumbnail)
		assert.Equal(t, m.Compressed, pf.Media[i].Compressed)
	}

	// Assertions on Tags
	assert.Equal(t, len(np.Tags), len(pf.Tags))
	for i, tag := range np.Tags {
		assert.Equal(t, tag.Tag, pf.Tags[i].Tag)
	}

	// Hide the product
	err = ps.HideProductById(ctx, newPrd.Product.ID, true)
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, sql.NullBool{Valid: true, Bool: true}, pf.Product.Hidden)

	// Set sale percentage
	err = ps.SetSaleById(ctx, newPrd.Product.ID, decimal.NewFromInt(10))
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.True(t, pf.Product.SalePercentage.Valid)
	assert.True(t, decimal.NewFromInt(10).Equal(pf.Product.SalePercentage.Decimal))

	// Update product preorder
	err = ps.UpdateProductPreorder(ctx, newPrd.Product.ID, "preorder")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, sql.NullString{Valid: true, String: "preorder"}, pf.Product.Preorder)

	ps.UpdateProductName(ctx, newPrd.Product.ID, "new name")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, "new name", pf.Product.Name)

	ps.UpdateProductSKU(ctx, newPrd.Product.ID, "new sku")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, "new sku", pf.Product.SKU)

	// invalid  color hex
	err = ps.UpdateProductColorAndColorHex(ctx, newPrd.Product.ID, "new color", "new color hex")
	assert.Error(t, err)

	// valid color hex
	err = ps.UpdateProductColorAndColorHex(ctx, newPrd.Product.ID, "new color", "#000000")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, "new color", pf.Product.Color)
	assert.Equal(t, "#000000", pf.Product.ColorHex)

	// update country of origin
	err = ps.UpdateProductCountryOfOrigin(ctx, newPrd.Product.ID, "new country of origin")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, "new country of origin", pf.Product.CountryOfOrigin)

	// update brand
	err = ps.UpdateProductBrand(ctx, newPrd.Product.ID, "new brand")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, "new brand", pf.Product.Brand)

	err = ps.UpdateProductTargetGender(ctx, newPrd.Product.ID, entity.Female)
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.Equal(t, entity.Female, pf.Product.TargetGender)

	// update thumbnail
	err = ps.UpdateProductThumbnail(ctx, newPrd.Product.ID, "new thumbnail")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.Equal(t, "new thumbnail", pf.Product.Thumbnail)

	// update price
	err = ps.UpdateProductPrice(ctx, newPrd.Product.ID, decimal.NewFromInt(1000))
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.True(t, decimal.NewFromInt(1000).Equal(pf.Product.Price))

	// update sale
	err = ps.UpdateProductSale(ctx, newPrd.Product.ID, decimal.NewFromInt(1))
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.True(t, pf.Product.SalePercentage.Valid)
	assert.True(t, decimal.NewFromInt(1).Equal(pf.Product.SalePercentage.Decimal))

	// update category
	c, err := getRandomCategory(db)
	assert.NoError(t, err)
	err = ps.UpdateProductCategory(ctx, newPrd.Product.ID, c.ID)
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.Equal(t, c.ID, pf.Product.CategoryID)

	// update description
	err = ps.UpdateProductDescription(ctx, newPrd.Product.ID, "new description")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.Equal(t, "new description", pf.Product.Description)

	assert.True(t, len(pf.Sizes) > 0)

	ms, ok := db.cache.GetMeasurementsByName(entity.Height)
	assert.True(t, ok)

	err = ps.AddProductMeasurement(ctx, newPrd.Product.ID, pf.Sizes[0].SizeID, ms.ID, decimal.NewFromInt(12))
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	ok = false
	for _, m := range pf.Measurements {
		if m.MeasurementValue.Equal(decimal.NewFromInt(12)) {
			ok = true
		}
	}
	assert.True(t, ok)

	for _, m := range pf.Measurements {
		err = ps.DeleteProductMeasurement(ctx, m.ID)
		assert.NoError(t, err)
	}

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.True(t, len(pf.Measurements) == 0)

	// add new size

	err = ps.AddProductTag(ctx, newPrd.Product.ID, "new tag")
	assert.NoError(t, err)

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.True(t, len(pf.Tags) == 2)

	for _, ta := range pf.Tags {
		err = ps.DeleteProductTag(ctx, pf.Product.ID, ta.Tag)
		assert.NoError(t, err)
	}

	// Fetch the product by ID
	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)
	assert.True(t, len(pf.Tags) == 0)

	assert.True(t, len(pf.Media) == 1)

	err = ps.AddProductMedia(ctx, newPrd.Product.ID, "full_size", "thumbnail", "compressed")
	assert.NoError(t, err)

	pf, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	assert.True(t, len(pf.Media) == 2)

	for _, m := range pf.Media {
		err = ps.DeleteProductMedia(ctx, m.ID)
		assert.NoError(t, err)
	}

	// delete size product
	err = ps.DeleteProductById(ctx, newPrd.Product.ID)
	assert.NoError(t, err)

	// Fetch the product by ID
	_, err = ps.GetProductById(ctx, newPrd.Product.ID)
	assert.Error(t, err)

}

func TestProductStore_GetProductsPaged(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	teeCategory, ok := db.cache.GetCategoryByName(entity.TShirt)
	assert.True(t, ok)

	dressCategory, ok := db.cache.GetCategoryByName(entity.Dress)
	assert.True(t, ok)

	xlSize, ok := db.cache.GetSizesByName(entity.XL)
	assert.True(t, ok)

	lSize, ok := db.cache.GetSizesByName(entity.L)
	assert.True(t, ok)

	for i := 0; i < 50; i++ {
		np, err := randomProductInsert(db, i)
		assert.NoError(t, err)

		np.Product.Price = decimal.NewFromInt(int64(i + 1))

		// assuming there is no dresses
		if np.Product.CategoryID == dressCategory.ID {
			np.Product.CategoryID = teeCategory.ID
		}

		if i%3 == 0 {
			np.SizeMeasurements[0].ProductSize.Quantity = decimal.NewFromInt(10)
			np.SizeMeasurements[0].ProductSize.SizeID = lSize.ID
		}

		if i%2 == 0 {
			np.Product.Color = "green"
			np.Product.ColorHex = "#00FF00"
			np.Product.CategoryID = teeCategory.ID
			np.Product.SalePercentage = decimal.NullDecimal{Decimal: decimal.NewFromInt(10), Valid: true}
			np.SizeMeasurements[0].ProductSize.Quantity = decimal.NewFromInt(10)
			np.SizeMeasurements[0].ProductSize.SizeID = xlSize.ID
			np.Product.Preorder = sql.NullString{String: "preorder", Valid: true}
			np.Tags = []entity.ProductTagInsert{
				{
					Tag: "ss23",
				},
			}
		}

		// Insert new product
		_, err = ps.AddProduct(ctx, np)
		assert.NoError(t, err)
	}

	testCases := []struct {
		name             string
		limit            int
		offset           int
		sortFactors      []entity.SortFactor
		orderFactor      entity.OrderFactor
		filterConditions *entity.FilterConditions
		showHidden       bool
		expectedCount    int
		checkFunc        func([]entity.Product) error
	}{
		{
			name:          "First page, 5 per page",
			limit:         5,
			offset:        0,
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				return nil
			},
		},
		{
			name:          "Second page, 5 per page",
			limit:         5,
			offset:        5,
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				// set sale for
				for _, p := range products {
					err := ps.SetSaleById(ctx, p.ID, decimal.NewFromInt(10))
					assert.NoError(t, err)
				}
				return nil
			},
		},
		{
			name:   "Price filter applied",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				PriceFromTo: entity.PriceFromTo{
					From: decimal.NewFromInt(1),
					To:   decimal.NewFromInt(50),
				},
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				return nil
			},
		},
		{
			name:   "Sort by price ascending",
			limit:  5,
			offset: 0,
			sortFactors: []entity.SortFactor{
				entity.Price,
			},
			orderFactor:   entity.Ascending,
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error { // Add this block
				for i := 1; i < len(products); i++ {
					if !products[i-1].Price.LessThanOrEqual(products[i].Price) {
						return errors.New("products are not sorted in ascending order")
					}
				}
				return nil
			},
		},
		{
			name:   "Sort by price descending",
			limit:  5,
			offset: 0,
			sortFactors: []entity.SortFactor{
				entity.Price,
			},
			orderFactor:   entity.Descending,
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for i := 1; i < len(products); i++ {
					if products[i].Price.GreaterThanOrEqual(products[i-1].Price) {
						return fmt.Errorf("products[%d].Price must be in descending order", i)
					}
				}
				return nil
			},
		},
		{
			name:   "Filter by color",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				Color: "green",
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for _, product := range products {
					if product.Color != "green" {
						return errors.New("products are not filtered by color correctly")
					}
				}
				return nil
			},
		},
		{
			name:   "Filter by category",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				CategoryId: teeCategory.ID,
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for _, product := range products {
					if product.CategoryID != teeCategory.ID {
						return errors.New("products are not filtered by category correctly")
					}
				}
				return nil
			},
		},
		{
			name:   "Filter by dress category must not exist",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				CategoryId: dressCategory.ID,
			},
			expectedCount: 0,
			checkFunc: func(products []entity.Product) error {
				return nil
			},
		},
		{
			name:   "Filter by on sale",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				OnSale: true,
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for _, product := range products {
					if !product.SalePercentage.Decimal.GreaterThan(decimal.Zero) {
						return errors.New("products are not filtered by sale status correctly")
					}
				}
				return nil
			},
		},
		{
			name:   "Filter by sizes available",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				SizesIds: []int{xlSize.ID, lSize.ID},
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for _, product := range products {
					prd, err := db.GetProductById(ctx, product.ID)
					assert.NoError(t, err)

					hasValidSize := false // Flag to check if the product has either "XL" or "L" sizes
					for _, sz := range prd.Sizes {
						s, ok := db.cache.GetSizeById(sz.SizeID)
						assert.True(t, ok)
						if s.Name == entity.XL || s.Name == entity.L {
							hasValidSize = true
							break
						}
					}

					// If the product does not have "XL" or "L" size, return an error
					if !hasValidSize {
						return errors.New("products are not filtered by sizes correctly")
					}
				}
				return nil // Return nil if all products have "XL" or "L" sizes
			},
		},
		{
			name:   "Filter by preorder",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				Preorder: true,
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				for _, product := range products {
					if product.Preorder.String == "" {
						return errors.New("products are not filtered by preorder status correctly")
					}
				}
				return nil
			},
		},
		{
			name:   "Filter by tags",
			limit:  5,
			offset: 0,
			filterConditions: &entity.FilterConditions{
				ByTag: "ss23",
			},
			expectedCount: 5,
			checkFunc: func(products []entity.Product) error {
				hasValidSize := false
				for _, product := range products {
					prd, err := db.GetProductById(ctx, product.ID)
					assert.NoError(t, err)

					for _, t := range prd.Tags {
						if t.Tag == "ss23" {
							hasValidSize = true
							break
						}
					}
				}
				if !hasValidSize {
					return errors.New("products are not filtered by sizes correctly")
				}
				return nil
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			fetchedPrds, err := ps.GetProductsPaged(ctx, tc.limit, tc.offset, tc.sortFactors, tc.orderFactor, tc.filterConditions, tc.showHidden)
			if err != nil {
				t.Fatalf("GetProductsPaged failed with error: %v", err)
			}

			// Assert that the number of fetched products is as expected
			assert.Equal(t, tc.expectedCount, len(fetchedPrds))

			// Run additional checks if checkFunc is defined
			if tc.checkFunc != nil {
				assert.NoError(t, tc.checkFunc(fetchedPrds))
			}
		})
	}
}

func TestProductStore_StockTest(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	np, err := randomProductInsert(db, 1)
	assert.NoError(t, err)

	xlSize, ok := db.cache.GetSizesByName(entity.XL)
	assert.True(t, ok)

	lSize, ok := db.cache.GetSizesByName(entity.L)
	assert.True(t, ok)

	np.SizeMeasurements = []entity.SizeWithMeasurementInsert{
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(10),
				SizeID:   xlSize.ID,
			},
		},
		{
			ProductSize: entity.ProductSizeInsert{
				Quantity: decimal.NewFromInt(15),
				SizeID:   lSize.ID,
			},
		},
	}

	// Insert new product
	prd, err := ps.AddProduct(ctx, np)
	assert.NoError(t, err)

	err = ps.ReduceStockForProductSizes(ctx, []entity.OrderItemInsert{
		{
			ProductID: prd.Product.ID,
			SizeID:    xlSize.ID,
			Quantity:  decimal.NewFromInt32(1),
		},
		{
			ProductID: prd.Product.ID,
			SizeID:    lSize.ID,
			Quantity:  decimal.NewFromInt32(1),
		},
	})
	assert.NoError(t, err)

	err = ps.RestoreStockForProductSizes(ctx, []entity.OrderItemInsert{
		{
			ProductID: prd.Product.ID,
			SizeID:    xlSize.ID,
			Quantity:  decimal.NewFromInt32(1),
		},
		{
			ProductID: prd.Product.ID,
			SizeID:    lSize.ID,
			Quantity:  decimal.NewFromInt32(1),
		},
	})
	assert.NoError(t, err)

	p, err := ps.GetProductById(ctx, prd.Product.ID)

	assert.NoError(t, err)

	hasLSize := false
	hasXLSize := false
	for _, sz := range p.Sizes {
		if sz.SizeID == xlSize.ID && sz.Quantity.Equal(decimal.NewFromInt(10)) {
			hasXLSize = true
		}
		if sz.SizeID == lSize.ID && sz.Quantity.Equal(decimal.NewFromInt(15)) {
			hasLSize = true
		}
	}

	assert.True(t, hasLSize)
	assert.True(t, hasXLSize)

	// must fail because of insufficient stock
	err = ps.ReduceStockForProductSizes(ctx, []entity.OrderItemInsert{
		{
			ProductID: prd.Product.ID,
			SizeID:    xlSize.ID,
			Quantity:  decimal.NewFromInt32(11),
		},
	})
	assert.Error(t, err)

	err = ps.UpdateProductSizeStock(ctx, prd.Product.ID, xlSize.ID, 20)
	assert.NoError(t, err)

	err = ps.ReduceStockForProductSizes(ctx, []entity.OrderItemInsert{
		{
			ProductID: prd.Product.ID,
			SizeID:    xlSize.ID,
			Quantity:  decimal.NewFromInt32(11),
		},
	})
	assert.NoError(t, err)

}
