package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/dependency"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"golang.org/x/exp/slices"
)

var validSizes = []string{"XXS", "XS", "S", "M", "L", "XL", "XXL", "OS"}

type productStore struct {
	*MYSQLStore
}

// ParticipateStore returns an object implementing participate interface
func (ms *MYSQLStore) Products() dependency.Products {
	return &productStore{
		MYSQLStore: ms,
	}
}

// Function to fetch basic product information
func (ms *MYSQLStore) fetchProductInfo(ctx context.Context, productId int32) (*dto.ProductInfo, error) {
	queryBuilder := strings.Builder{}
	queryBuilder.WriteString(`
	SELECT 
		id, 
		created_at, 
		name, 
		reorder, 
		description, 
		hidden 
	FROM products 
	WHERE id = :productId`)

	params := map[string]interface{}{
		"productId": productId,
	}

	productInfo, err := QueryNamedOne[dto.ProductInfo](ctx, ms.DB(), queryBuilder.String(), params)
	if err != nil {
		return nil, fmt.Errorf("can't get product info: %w", err)
	}
	return &productInfo, nil

}

// Function to fetch basic products information
func (ms *MYSQLStore) fetchProductsInfo(ctx context.Context, limit, offset int32, sortFactors []dto.SortFactor, filterConditions []dto.FilterCondition, showHidden bool) ([]dto.ProductInfo, error) {
	queryBuilder := strings.Builder{}
	queryBuilder.WriteString("SELECT id, created_at, name, preorder, description, hidden FROM products WHERE ")

	if !showHidden {
		queryBuilder.WriteString("hidden = FALSE ")
	}

	// Adding filters
	if len(filterConditions) > 0 {
		queryBuilder.WriteString(" AND ")
		for i, condition := range filterConditions {
			if i > 0 {
				queryBuilder.WriteString(" AND ")
			}
			queryBuilder.WriteString(fmt.Sprintf("%s = '%s'", condition.Field, condition.Value))
		}
	}

	// Adding sorting
	if len(sortFactors) > 0 {
		queryBuilder.WriteString(" ORDER BY ")
		for i, factor := range sortFactors {
			if i > 0 {
				queryBuilder.WriteString(", ")
			}
			switch factor.Field {
			case dto.SortFieldDateAdded:
				queryBuilder.WriteString("p.created_at")
			case dto.SortFieldPriceUSD:
				queryBuilder.WriteString("pr.USD")
			}
			if factor.Order == dto.SortOrderDesc {
				queryBuilder.WriteString(" DESC")
			} else {
				queryBuilder.WriteString(" ASC")
			}
		}
	}

	queryBuilder.WriteString(" LIMIT ? OFFSET ?")
	query := queryBuilder.String()

	rows, err := ms.DB().QueryContext(ctx, query, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var productInfos []dto.ProductInfo
	for rows.Next() {
		var info dto.ProductInfo
		if err := rows.Scan(&info.Id, &info.Created, &info.Name, &info.Preorder, &info.Description, &info.Hidden); err != nil {
			return nil, err
		}
		productInfos = append(productInfos, info)
	}
	return productInfos, nil
}

// Function to fetch sizes for a list of product IDs
func (ms *MYSQLStore) fetchSizes(ctx context.Context, productIDs []int32) ([]dto.Size, error) {
	if len(productIDs) == 0 {
		return []dto.Size{}, nil
	}
	query := `SELECT 
	id, product_id, XXS, XS, S, M, L, XL, XXL, OS FROM product_sizes 
	WHERE product_id IN (:productIds)`

	params := map[string]interface{}{
		"productIds": productIDs,
	}

	sizes, err := QueryListNamed[dto.Size](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}

	return sizes, nil
}

// Function to fetch sizes for a list of product IDs
func (ms *MYSQLStore) fetchSize(ctx context.Context, productID int32) (*dto.Size, error) {
	query := `SELECT 
	id, product_id, XXS, XS, S, M, L, XL, XXL, OS FROM product_sizes 
	WHERE product_id IN :productId`

	params := map[string]interface{}{
		"productId": productID,
	}

	size, err := QueryNamedOne[dto.Size](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get sizes: %w", err)
	}

	return &size, nil
}

// Function to fetch categories for a list of product IDs
func (ms *MYSQLStore) fetchCategories(ctx context.Context, productIDs []int32) ([]dto.Category, error) {
	if len(productIDs) == 0 {
		return []dto.Category{}, nil
	}
	query := `SELECT 
	id, product_id, category FROM product_categories 
	WHERE product_id IN (:productIds)`

	params := map[string]interface{}{
		"productIds": productIDs,
	}

	categories, err := QueryListNamed[dto.Category](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get categories: %w", err)
	}

	return categories, nil
}

// Function to fetch media for a list of product IDs
func (ms *MYSQLStore) fetchMedia(ctx context.Context, productIDs []int32) ([]dto.Media, error) {
	if len(productIDs) == 0 {
		return []dto.Media{}, nil
	}
	query := `SELECT 
	id, product_id, full_size, thumbnail, compressed FROM product_images 
	WHERE product_id IN (:productIds)`

	params := map[string]interface{}{
		"productIds": productIDs,
	}

	media, err := QueryListNamed[dto.Media](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get media: %w", err)
	}

	return media, nil
}

// fetchPrices fetches prices related to a slice of product IDs
func (ms *MYSQLStore) fetchPrices(ctx context.Context, productIDs []int32) ([]dto.Price, error) {
	if len(productIDs) == 0 {
		return []dto.Price{}, nil
	}
	query := `SELECT 
	id, product_id, USD, EUR, USDC, ETH, sale FROM product_prices 
	WHERE product_id IN (:productIds)`

	params := map[string]interface{}{
		"productIds": productIDs,
	}

	prices, err := QueryListNamed[dto.Price](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get prices: %w", err)
	}

	return prices, nil
}

// fetchPrice fetches prices related to a slice of product IDs
func (ms *MYSQLStore) fetchPrice(ctx context.Context, productID int32) (*dto.Price, error) {
	query := `SELECT 
	id, product_id, USD, EUR, USDC, ETH, sale FROM product_prices 
	WHERE product_id = :productId`

	params := map[string]interface{}{
		"productId": productID,
	}

	price, err := QueryNamedOne[dto.Price](ctx, ms.DB(), query, params)
	if err != nil {
		return nil, fmt.Errorf("can't get prices: %w", err)
	}

	return &price, nil
}

// GetProductsPaged retrieves a paged list of products based on the provided parameters.
// Parameters:
//   - limit: The maximum number of products per page.
//   - offset: The starting offset for retrieving products.
//   - sortFactors: Sorting factors and orders.
//   - filterConditions: Filtering conditions.
func (ms *MYSQLStore) GetProductsPaged(ctx context.Context, limit, offset int32, sortFactors []dto.SortFactor, filterConditions []dto.FilterCondition, showHidden bool) ([]dto.Product, error) {
	// Fetch products info first
	productInfos, err := ms.fetchProductsInfo(ctx, limit, offset, sortFactors, filterConditions, showHidden)
	if err != nil {
		return nil, err
	}

	// Extract product IDs
	var productIDs []int32
	for _, pi := range productInfos {
		productIDs = append(productIDs, pi.Id)
	}

	// Fetch Prices, Sizes, Categories, and Media
	prices, err := ms.fetchPrices(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	sizes, err := ms.fetchSizes(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	categories, err := ms.fetchCategories(ctx, productIDs)
	if err != nil {
		return nil, err
	}
	media, err := ms.fetchMedia(ctx, productIDs)
	if err != nil {
		return nil, err
	}

	// Assemble the final []dto.Product
	var products []dto.Product
	for _, pi := range productInfos {
		product := dto.Product{
			ProductInfo: &pi,
		}

		// Assign Price
		for _, price := range prices {
			if price.ProductID == pi.Id {
				product.Price = &price
				break
			}
		}

		// Assign Size
		for _, size := range sizes {
			if size.ProductID == pi.Id {
				product.AvailableSizes = &size
				break
			}
		}

		// Assign Categories
		for _, category := range categories {
			if category.ProductID == pi.Id {
				product.Categories = append(product.Categories, category)
			}
		}

		// Assign Media
		for _, m := range media {
			if m.ProductID == pi.Id {
				product.Media = append(product.Media, m)
			}
		}
		products = append(products, product)
	}

	return products, nil
}

// GetProductByID retrieves product information from the product store based on the given product ID.
func (ms *MYSQLStore) GetProductByID(ctx context.Context, productId int32) (*dto.Product, error) {
	product := &dto.Product{}
	var err error

	product.ProductInfo, err = ms.fetchProductInfo(ctx, productId)
	if err != nil {
		return nil, err
	}
	// Fetch Prices, Sizes, Categories, and Media
	product.Price, err = ms.fetchPrice(ctx, productId)
	if err != nil {
		return nil, err
	}
	product.AvailableSizes, err = ms.fetchSize(ctx, productId)
	if err != nil {
		return nil, err
	}
	product.Categories, err = ms.fetchCategories(ctx, []int32{productId})
	if err != nil {
		return nil, err
	}
	product.Media, err = ms.fetchMedia(ctx, []int32{productId})
	if err != nil {
		return nil, err
	}

	return product, nil

}

// AddProduct adds a new product to the product store.
func (ms *MYSQLStore) AddProduct(ctx context.Context,
	name, description,
	preorder string,
	availableSizes *dto.Size,
	price *dto.Price,
	media []dto.Media,
	categories []string) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
		INSERT INTO products (name, description, preorder)
		VALUES (?, ?, ?)`, name, description, preorder)
		if err != nil {
			return err
		}
		pid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		err = addCategories(ctx, rep, pid, categories)
		if err != nil {
			return err
		}

		err = addProductImages(ctx, rep, pid, media)
		if err != nil {
			return err
		}
		err = addPrices(ctx, rep, pid, price)
		if err != nil {
			return err
		}
		err = addSizes(ctx, rep, pid, availableSizes)
		if err != nil {
			return err
		}
		return nil
	})
}
func addCategories(ctx context.Context, rep dependency.Repository, productID int64, categories []string) error {
	if !rep.InTx() {
		return fmt.Errorf("addCategories must be called from within transaction")
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO product_categories (product_id, category) VALUES ")

	params := []string{}
	values := []interface{}{}
	for _, c := range categories {
		params = append(params, "(?, ?)")
		values = append(values, productID, c)
	}
	sb.WriteString(strings.Join(params, ", "))

	_, err := rep.DB().ExecContext(ctx, sb.String(), values...)
	if err != nil {
		return err
	}

	return nil
}
func addProductImages(ctx context.Context, rep dependency.Repository, productID int64, images []dto.Media) error {
	if !rep.InTx() {
		return fmt.Errorf("addProductImages must be called from within transaction")
	}

	var sb strings.Builder
	sb.WriteString("INSERT INTO product_images (product_id, full_size, thumbnail, compressed) VALUES ")

	params := []string{}
	values := []interface{}{}
	for _, img := range images {
		params = append(params, "(?, ?, ?, ?)")
		values = append(values, productID, img.FullSize, img.Thumbnail, img.Compressed)
	}
	sb.WriteString(strings.Join(params, ", ")) // The join operation will insert commas between each values group

	_, err := rep.DB().ExecContext(ctx, sb.String(), values...)
	if err != nil {
		return err
	}

	return nil
}
func addPrices(ctx context.Context, rep dependency.Repository, productID int64, p *dto.Price) error {
	if !rep.InTx() {
		return fmt.Errorf("addProductImages must be called from within transaction")
	}
	_, err := rep.DB().ExecContext(ctx, `
	INSERT INTO product_prices (product_id, USD, EUR, USDC, ETH)
	VALUES (?, ?, ?, ?, ?)`,
		productID, p.USD, p.EUR, p.USDC, p.ETH)
	if err != nil {
		return err
	}
	return err
}
func addSizes(ctx context.Context, rep dependency.Repository, productID int64, sizes *dto.Size) error {
	if !rep.InTx() {
		return fmt.Errorf("addSizes must be called from within transaction")
	}
	_, err := rep.DB().ExecContext(ctx, `
	INSERT INTO product_sizes (product_id, XXS, XS, S, M, L, XL, XXL, OS)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		productID, sizes.XXS, sizes.XS, sizes.S, sizes.M, sizes.L, sizes.XL, sizes.XXL, sizes.OS)
	if err != nil {
		return err
	}
	return nil
}

// DeleteProductByID deletes a product from the product store based on the given product ID.
func (ms *MYSQLStore) DeleteProductByID(ctx context.Context, id int32) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		_, err := rep.DB().ExecContext(ctx, `
		DELETE FROM product_categories 
		WHERE product_id = ?`, id)
		if err != nil {
			return err
		}

		_, err = rep.DB().ExecContext(ctx, `
		DELETE FROM product_images 
		WHERE product_id = ?`, id)
		if err != nil {
			return err
		}

		_, err = rep.DB().ExecContext(ctx, `
		DELETE FROM product_prices 
		WHERE product_id = ?`, id)
		if err != nil {
			return err
		}

		_, err = rep.DB().ExecContext(ctx, `
		DELETE FROM product_sizes 
		WHERE product_id = ?`, id)
		if err != nil {
			return err
		}

		_, err = rep.DB().ExecContext(ctx, `
		DELETE FROM products 
		WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return nil
	})
}

// HideProductByID updates the "hidden" status of a product in the product store based on the given product ID.
func (ms *MYSQLStore) HideProductByID(ctx context.Context, id int32, hide bool) error {
	res, err := ms.DB().ExecContext(ctx, `
		UPDATE products 
		SET hidden = ? 
		WHERE id = ?
	`, hide, id)
	if err != nil {
		return err
	}

	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("id not found")
	}

	return err
}

func (ms *MYSQLStore) DecreaseAvailableSizes(ctx context.Context, items []dto.Item) error {
	if !ms.InTx() {
		return fmt.Errorf("DecreaseAvailableSize must be called from within transaction")
	}

	for _, item := range items {
		// Validate size
		size := strings.ToUpper(item.Size)
		if !slices.Contains(validSizes, size) {
			return errors.New("invalid size")
		}
		// Decrease size availability by item.Quantity
		result, err := ms.DB().ExecContext(ctx, fmt.Sprintf(`
			UPDATE product_sizes 
			SET %s = %s - ?
			WHERE product_id = ? 
			AND %s >= ?`, size, size, size), item.Quantity, item.ID, item.Quantity)
		if err != nil {
			return err
		}

		rowsAffected, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rowsAffected == 0 {
			return fmt.Errorf("size availability for product id %d is less than the desired amount", item.ID)
		}
	}
	return nil
}

func (ms *MYSQLStore) SetSaleByID(ctx context.Context, id int32, salePercent float64) error {
	res, err := ms.DB().ExecContext(ctx, `
        UPDATE product_prices 
        SET sale = ?
        WHERE product_id = ?
    `, salePercent, id)

	if err != nil {
		return err
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return errors.New("id not found")
	}
	return nil
}
