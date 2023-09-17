package store

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"golang.org/x/exp/slices"
)

func getTestProd(count int) []*dto.Product {
	p := []*dto.Product{}
	for i := 0; i < count; i++ {
		p = append(p, &dto.Product{
			ProductInfo: &dto.ProductInfo{
				Name:        fmt.Sprintf("Test Product %d", i),
				Preorder:    "test",
				Description: "This is a test product",
				Hidden:      false,
			},
			Price: &dto.Price{
				USD:  decimal.NewFromFloat(10.0*float64(i+1) + rand.Float64()*10.0),
				EUR:  decimal.NewFromFloat(9.0 * float64(i+1)),
				USDC: decimal.NewFromFloat(10.0 * float64(i+1)),
				ETH:  decimal.NewFromFloat(0.3 * float64(i+1)),
			},
			AvailableSizes: &dto.Size{
				S: 10,
				M: 20,
				L: i,
			},
			Categories: []dto.Category{
				{
					Category: "category1",
				}, {
					Category: "category2",
				},
			},
			Media: []dto.Media{
				{
					FullSize:   "https://example.com/fullsize.jpg",
					Thumbnail:  "https://example.com/thumbnail.jpg",
					Compressed: "https://example.com/compressed.jpg",
				},
				{
					FullSize:   "https://example.com/fullsize2.jpg",
					Thumbnail:  "https://example.com/thumbnail2.jpg",
					Compressed: "https://example.com/compressed2.jpg",
				},
			},
		})
	}

	return p
}

func TestProductStore_AddProduct(t *testing.T) {
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()
	prds := getTestProd(1)
	prds[0].ProductInfo.Preorder = "test"
	p := prds[0]

	categories := []string{}
	for _, c := range p.Categories {
		categories = append(categories, c.Category)
	}

	err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
	assert.NoError(t, err)

}

func TestProductStore_GetProductsPaged(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	// Generate and insert test products
	prds := getTestProd(10)
	for _, prd := range prds {

		categories := []string{}
		for _, c := range prd.Categories {
			categories = append(categories, c.Category)
		}

		err := ps.AddProduct(ctx, prd.ProductInfo.Name, prd.ProductInfo.Description, prd.ProductInfo.Preorder, prd.AvailableSizes, prd.Price, prd.Media, categories)
		assert.NoError(t, err)
	}

	// Define the number of products to be fetched per page
	limit := int32(5)

	// Fetch and validate products for each page
	for i := 0; i < 2; i++ {
		offset := int32(i) * limit
		fetchedPrds, err := ps.GetProductsPaged(ctx, limit, offset, nil, nil, false)
		assert.NoError(t, err)

		// Assert that the number of fetched products is as expected
		assert.Equal(t, limit, len(fetchedPrds))

		// Assert that the fetched products match the inserted products
		for j, fetchedPrd := range fetchedPrds {
			expectedPrd := prds[offset+int32(j)]
			assert.Equal(t, expectedPrd.ProductInfo.Name, fetchedPrd.ProductInfo.Name)
			assert.Equal(t, expectedPrd.ProductInfo.Description, fetchedPrd.ProductInfo.Description)
			assert.True(t, expectedPrd.Price.EUR.Equal(fetchedPrd.Price.EUR))
			assert.True(t, expectedPrd.Price.USDC.Equal(fetchedPrd.Price.USDC))
			assert.True(t, expectedPrd.Price.Sale.Equal(fetchedPrd.Price.Sale))
			assert.Equal(t, expectedPrd.AvailableSizes, fetchedPrd.AvailableSizes)
			assert.ElementsMatch(t, expectedPrd.Categories, fetchedPrd.Categories)
			assert.Equal(t, expectedPrd.Media[0].FullSize, fetchedPrd.Media[0].FullSize)
			assert.Equal(t, expectedPrd.Media[0].Thumbnail, fetchedPrd.Media[0].Thumbnail)
			assert.Equal(t, expectedPrd.Media[0].Compressed, fetchedPrd.Media[0].Compressed)
		}
	}

}

func TestProductStore_GetProductsPagedSortAndFilter(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(20)

	for _, p := range prds {

		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{
		{Field: dto.SortFieldDateAdded, Order: dto.SortOrderDesc},
		{Field: dto.SortFieldPriceUSD, Order: dto.SortOrderAsc},
	}
	filterConditions := []dto.FilterCondition{
		{Field: dto.FilterFieldSize, Value: "L"},
	}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)

	// Assert no error
	assert.NoError(t, err)

	// Assert the returned data.
	// For simplicity, we're just checking the count here.
	assert.Len(t, products, 10)

	// Check that the products are in the correct order (sorted by DateAdded Descending and then by Price Ascending).
	for i := 0; i < len(products)-1; i++ {
		assert.Len(t, products[i].Media, 2)
		if products[i].ProductInfo.Created.Equal(products[i+1].ProductInfo.Created) {
			assert.True(t, products[i].Price.USD.LessThan(products[i+1].Price.USD) || products[i].Price.USD.Equal(products[i+1].Price.USD))
		} else {
			assert.True(t, products[i].ProductInfo.Created.After(products[i+1].ProductInfo.Created))
		}
	}

	assert.True(t, len(products) == 10)

}

func TestProductStore_GetProductsPagedSortAndFilterCategories(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(3)
	prds[0].Categories = append(prds[0].Categories, dto.Category{
		Category: "men",
	})
	prds[1].Categories = append(prds[1].Categories, dto.Category{
		Category: "women",
	})
	prds[1].Categories = append(prds[1].Categories, dto.Category{
		Category: "men",
	})
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{
		{Field: dto.SortFieldDateAdded, Order: dto.SortOrderDesc},
	}
	filterConditions := []dto.FilterCondition{
		{Field: dto.FilterFieldCategory, Value: "men"},
	}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)

	// Assert no error
	assert.NoError(t, err)

	// Assert the returned data.
	// For simplicity, we're just checking the count here.
	assert.Len(t, products, 2)
	assert.True(t, slices.Contains(products[0].Categories, dto.Category{
		Category: "men",
	}))
	assert.True(t, slices.Contains(products[1].Categories, dto.Category{
		Category: "men",
	}))

}

func TestProductStore_GetProductsPagedSortAndFilterSize(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(3)
	prds[0].AvailableSizes.L = 0
	prds[1].AvailableSizes.L = 2
	prds[2].AvailableSizes.L = 2
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}

		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{
		{Field: dto.SortFieldDateAdded, Order: dto.SortOrderDesc},
	}
	filterConditions := []dto.FilterCondition{
		{Field: dto.FilterFieldSize, Value: "L"},
	}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)

	// Assert no error
	assert.NoError(t, err)

	// Assert the returned data.
	// For simplicity, we're just checking the count here.
	assert.Len(t, products, 2)

	// Check that the products meet the filter conditions (size L and category men).
	for _, product := range products {
		assert.True(t, product.AvailableSizes.L > 0)
	}
}

func TestProductStore_GetProductById(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(3)
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)

	// Assert no error
	assert.NoError(t, err)

	// Assert the returned data.
	// For simplicity, we're just checking the count here.
	assert.Len(t, products, 3)

	prd, err := ps.GetProductByID(ctx, products[0].ProductInfo.Id)
	assert.NoError(t, err)
	fmt.Printf("%+v", prd)
	assert.Equal(t, products[0].ProductInfo.Id, prd.ProductInfo.Id)
	assert.Equal(t, prd.Media[0].FullSize, prds[0].Media[0].FullSize)
	assert.Equal(t, prd.Media[1].FullSize, prds[0].Media[1].FullSize)
	assert.Len(t, prd.Media, 2)
}

func TestProductStore_DeleteProductByID(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(1)
	for _, p := range prds {

		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 1)

	// prod id
	id := products[0].ProductInfo.Id

	err = ps.DeleteProductByID(ctx, id)
	assert.NoError(t, err)

	// Execute the function
	products, err = ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 0)

	_, err = ps.GetProductByID(ctx, id)
	assert.Error(t, err)

}

func TestProductStore_HideProductByID(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(1)
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 1)

	// prod id
	id := products[0].ProductInfo.Id
	hide := true

	err = ps.HideProductByID(ctx, id, hide)
	assert.NoError(t, err)

	// Execute the function
	products, err = ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 0)

	prd, err := ps.GetProductByID(ctx, id)
	assert.NoError(t, err)
	assert.True(t, prd.Media != nil)

}
func TestProductStore_DecreaseAvailableSize(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(1)
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 1)

	avS := products[0].AvailableSizes.S
	avM := products[0].AvailableSizes.M
	id := products[0].ProductInfo.Id

	items := []dto.Item{
		{
			ID:       id,
			Size:     "S",
			Quantity: 1,
		},
		{
			ID:       id,
			Size:     "M",
			Quantity: 1,
		},
	}

	err = ps.DecreaseAvailableSizes(ctx, items)
	assert.Error(t, err)

	err = ps.Tx(ctx, func(ctx context.Context, store dependency.Repository) error {
		return store.Products().DecreaseAvailableSizes(ctx, items)
	})
	assert.NoError(t, err)

	pr, err := ps.GetProductByID(ctx, id)
	assert.NoError(t, err)

	// validate that the size has been decreased
	assert.Equal(t, avS-1, pr.AvailableSizes.S)
	assert.Equal(t, avM-1, pr.AvailableSizes.M)

}

func TestProductStore_SetSaleByID(t *testing.T) {
	// Initialize the product store
	db := newTestDB(t)
	ps := db.Products()
	ctx := context.Background()

	prds := getTestProd(1)
	for _, p := range prds {
		categories := []string{}
		for _, c := range p.Categories {
			categories = append(categories, c.Category)
		}
		err := ps.AddProduct(ctx, p.ProductInfo.Name, p.ProductInfo.Description, p.ProductInfo.Preorder, p.AvailableSizes, p.Price, p.Media, categories)
		assert.NoError(t, err)
	}

	limit, offset := int32(10), int32(0)
	sortFactors := []dto.SortFactor{}
	filterConditions := []dto.FilterCondition{}

	// Execute the function
	products, err := ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)
	assert.Len(t, products, 1)

	id := products[0].ProductInfo.Id

	err = ps.SetSaleByID(ctx, id, 10)
	assert.NoError(t, err)

	products, err = ps.GetProductsPaged(ctx, limit, offset, sortFactors, filterConditions, false)
	assert.NoError(t, err)

	// validate that the size has been decreased
	assert.Equal(t, decimal.NewFromFloat(10).String(), products[0].Price.Sale.String())

}
