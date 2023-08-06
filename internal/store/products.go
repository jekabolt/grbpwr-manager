package store

import (
	"context"
	"database/sql"
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

// GetProductsPaged retrieves a paged list of products based on the provided parameters.
// Parameters:
//   - limit: The maximum number of products per page.
//   - offset: The starting offset for retrieving products.
//   - sortFactors: Sorting factors and orders.
//   - filterConditions: Filtering conditions.
func (ms *MYSQLStore) GetProductsPaged(ctx context.Context, limit, offset int, sortFactors []dto.SortFactor, filterConditions []dto.FilterCondition) ([]dto.Product, error) {
	var queryBuilder strings.Builder

	queryBuilder.WriteString(`
        SELECT p.id, p.created_at, p.name, p.description, p.preorder,
        IF(pr.sale > 0, pr.USD * (1 - pr.sale / 100.0), pr.USD) as USD, 
        IF(pr.sale > 0, pr.EUR * (1 - pr.sale / 100.0), pr.EUR) as EUR, 
        IF(pr.sale > 0, pr.USDC * (1 - pr.sale / 100.0), pr.USDC) as USDC, 
        IF(pr.sale > 0, pr.ETH * (1 - pr.sale / 100.0), pr.ETH) as ETH, 
		pr.sale,
        s.XXS, s.XS, s.S, s.M, s.L, s.XL, s.XXL, s.OS,
        GROUP_CONCAT(DISTINCT pc.category) as categories,
        GROUP_CONCAT(DISTINCT CONCAT(pi.full_size, '|', pi.thumbnail, '|', pi.compressed)) as images
		FROM products p
		INNER JOIN product_prices pr ON p.id = pr.product_id
		INNER JOIN product_sizes s ON p.id = s.product_id
		LEFT JOIN product_categories pc ON p.id = pc.product_id
		LEFT JOIN product_images pi ON p.id = pi.product_id
		WHERE p.hidden = FALSE
    `)

	// Adding filters
	if len(filterConditions) > 0 {
		for _, condition := range filterConditions {
			queryBuilder.WriteString(" AND ")
			switch condition.Field {
			case dto.FilterFieldSize:
				sz := strings.ToUpper(condition.Value)
				// check if value in validSizes
				if slices.Contains(validSizes, sz) {
					queryBuilder.WriteString(fmt.Sprintf("s.%s > 0", sz))
				}
			case dto.FilterFieldCategory:
				queryBuilder.WriteString(fmt.Sprintf("pc.category = '%s'", condition.Value))
			}
		}
	}

	queryBuilder.WriteString(" GROUP BY p.id, p.created_at, p.name, p.description, pr.USD, pr.EUR, pr.USDC, pr.ETH, pr.sale, s.XXS, s.XS, s.S, s.M, s.L, s.XL, s.XXL, s.OS ")

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
	args := []interface{}{limit, offset}

	rows, err := ms.DB().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	products := make([]dto.Product, 0)
	for rows.Next() {
		p := dto.Product{
			Price:          &dto.Price{},
			AvailableSizes: &dto.Size{},
		}
		var images, categories string
		err := rows.Scan(&p.Id, &p.Created, &p.Name, &p.Description, &p.Preorder,
			&p.Price.USD, &p.Price.EUR, &p.Price.USDC, &p.Price.ETH, &p.Price.Sale,
			&p.AvailableSizes.XXS, &p.AvailableSizes.XS, &p.AvailableSizes.S, &p.AvailableSizes.M,
			&p.AvailableSizes.L, &p.AvailableSizes.XL, &p.AvailableSizes.XXL, &p.AvailableSizes.OS,
			&categories, &images)
		if err != nil {
			return nil, err
		}

		p.Categories = strings.Split(categories, ",")
		p.ProductImages = splitImage(images)
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return products, nil
}

func splitImage(imageString string) []dto.Image {
	images := strings.Split(imageString, ",")
	productImages := make([]dto.Image, 0)
	for _, image := range images {
		imageParts := strings.Split(image, "|")
		if len(imageParts) == 3 {
			image := dto.Image{
				FullSize:   imageParts[0],
				Thumbnail:  imageParts[1],
				Compressed: imageParts[2],
			}
			productImages = append(productImages, image)
		}
	}
	return productImages
}

// AddProduct adds a new product to the product store.
func (ms *MYSQLStore) AddProduct(ctx context.Context, p *dto.Product) error {
	return ms.Tx(ctx, func(ctx context.Context, rep dependency.Repository) error {
		res, err := rep.DB().ExecContext(ctx, `
		INSERT INTO products (name, description, preorder)
		VALUES (?, ?, ?)`, p.Name, p.Description, p.Preorder)
		if err != nil {
			return err
		}
		pid, err := res.LastInsertId()
		if err != nil {
			return err
		}
		err = addCategories(ctx, rep, pid, p.Categories)
		if err != nil {
			return err
		}

		err = addProductImages(ctx, rep, pid, p.ProductImages)
		if err != nil {
			return err
		}
		err = addPrices(ctx, rep, pid, p.Price)
		if err != nil {
			return err
		}
		err = addSizes(ctx, rep, pid, p.AvailableSizes)
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
func addProductImages(ctx context.Context, rep dependency.Repository, productID int64, images []dto.Image) error {
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

// GetProductByID retrieves product information from the product store based on the given product ID.
func (ms *MYSQLStore) GetProductByID(ctx context.Context, id int64) (*dto.Product, error) {
	p := &dto.Product{
		Price:          &dto.Price{},
		AvailableSizes: &dto.Size{},
	}
	query := `
		SELECT 
			p.id, p.created_at, p.name, p.description, p.preorder,
			GROUP_CONCAT(DISTINCT pc.category),
			GROUP_CONCAT(DISTINCT CONCAT(pi.full_size, '|', pi.thumbnail, '|', pi.compressed)),
			pr.USD,	pr.EUR, pr.USDC, pr.ETH, s.XXS, 
			s.XS, s.S, s.M, s.L, s.XL, s.XXL, s.OS
		FROM products p
		LEFT JOIN product_categories pc ON p.id = pc.product_id
		LEFT JOIN product_images pi ON p.id = pi.product_id
		LEFT JOIN product_prices pr ON p.id = pr.product_id
		LEFT JOIN product_sizes s ON p.id = s.product_id
		WHERE p.id = ?
		GROUP BY p.id, p.created_at, p.name, p.description, pr.USD, pr.EUR, pr.USDC, pr.ETH, s.XXS, s.XS, s.S, s.M, s.L, s.XL, s.XXL, s.OS
	`

	row := ms.DB().QueryRowContext(ctx, query, id)

	var categories, images string
	if err := row.Scan(
		&p.Id, &p.Created, &p.Name, &p.Description, &p.Preorder, &categories, &images,
		&p.Price.USD, &p.Price.EUR, &p.Price.USDC, &p.Price.ETH, &p.AvailableSizes.XXS,
		&p.AvailableSizes.XS, &p.AvailableSizes.S, &p.AvailableSizes.M, &p.AvailableSizes.L, &p.AvailableSizes.XL, &p.AvailableSizes.XXL, &p.AvailableSizes.OS,
	); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("product with id %d does not exist", id)
		}
		return nil, err
	}

	p.Categories = strings.Split(categories, ",")
	p.ProductImages = splitImage(images)

	return p, nil
}

// DeleteProductByID deletes a product from the product store based on the given product ID.
func (ms *MYSQLStore) DeleteProductByID(ctx context.Context, id int64) error {
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
func (ms *MYSQLStore) HideProductByID(ctx context.Context, id int64, hide bool) error {
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

func (ms *MYSQLStore) SetSaleByID(ctx context.Context, id int64, salePercent float64) error {
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
