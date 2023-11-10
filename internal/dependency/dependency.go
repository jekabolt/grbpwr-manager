package dependency

import (
	"context"
	"database/sql"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

//go:generate go run github.com/vektra/mockery/v2@v2.24.0 --with-expecter --case underscore --all
type (
	ContextStore interface {
		Tx(ctx context.Context, fn func(ctx context.Context, store Repository) error) error
	}
	Products interface {
		ContextStore
		// AddProduct adds a new product along with its associated data.
		AddProduct(ctx context.Context, prd *entity.ProductNew) (*entity.ProductFull, error)
		// GetProductsPaged returns a paged list of products based on provided parameters.
		GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Product, error)
		// GetProductById retrieves a product by its ID.
		GetProductById(ctx context.Context, id int) (*entity.ProductFull, error)
		// DeleteProductById deletes a product by its ID.
		DeleteProductById(ctx context.Context, id int) error
		// HideProductById toggles the visibility of a product by its ID.
		HideProductById(ctx context.Context, id int, hide bool) error
		// SetSaleById sets the sale percentage for a product by its ID.
		SetSaleById(ctx context.Context, id int, salePercent decimal.Decimal) error
		// ReduceStockForProductSizes reduces the stock for a product by its ID.
		ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error
		// RestoreStockForProductSizes restores the stock for a product by its ID.
		RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error
		// UpdateProductPreorder updates the preorder status of a product.
		UpdateProductPreorder(ctx context.Context, productID int, preorder string) error
		// UpdateProductName updates the name of a product.
		UpdateProductName(ctx context.Context, productID int, name string) error
		// UpdateProductSKU updates the vendor code of a product.
		UpdateProductSKU(ctx context.Context, productID int, sku string) error
		// UpdateProductColorAndColorHex updates the color and colorHex of a product.
		UpdateProductColorAndColorHex(ctx context.Context, productID int, color, colorHex string) error
		// UpdateProductCountryOfOrigin updates the country of origin of a product.
		UpdateProductCountryOfOrigin(ctx context.Context, productID int, countryOfOrigin string) error
		// UpdateProductBrand updates the brand of a product.
		UpdateProductBrand(ctx context.Context, productID int, brand string) error
		// UpdateProductTargetGender updates target gender of a product.
		UpdateProductTargetGender(ctx context.Context, productID int, gender entity.GenderEnum) error
		// UpdateProductThumbnail updates the thumbnail of a product.
		UpdateProductThumbnail(ctx context.Context, productID int, thumbnail string) error
		// UpdateProductPrice updates the price of a product.
		UpdateProductPrice(ctx context.Context, productID int, price decimal.Decimal) error
		// UpdateProductSale updates the sale percentage of a product.
		UpdateProductSale(ctx context.Context, productID int, sale decimal.Decimal) error
		// UpdateProductCategory updates the category of a product.
		UpdateProductCategory(ctx context.Context, productID int, categoryID int) error
		// UpdateProductDescription updates the description of a product.
		UpdateProductDescription(ctx context.Context, productID int, description string) error
		// DeleteProductMeasurement deletes a size measurement for a product.
		DeleteProductMeasurement(ctx context.Context, id int) error
		// AddProductMeasurement adds a new size measurement for a product.
		AddProductMeasurement(ctx context.Context, productId, sizeId, measurementNameId int, measurementValue decimal.Decimal) error
		// UpdateProductSizeStock adds a new available size for a product.
		UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error
		// DeleteProductMedia deletes media for a product.
		DeleteProductMedia(ctx context.Context, productMediaId int) error
		// AddProductMedia adds new media for a product.
		AddProductMedia(ctx context.Context, productId int, fullSize string, thumbnail string, compressed string) error
		// AddProductTag adds a new tag for a product.
		AddProductTag(ctx context.Context, productId int, tag string) error
		// DeleteProductTag deletes a tag for a product.
		DeleteProductTag(ctx context.Context, productId int, tag string) error
	}
	// Hero interface {
	// 	SetHero(ctx context.Context, left, right dto.HeroElement) error
	// 	GetHero(ctx context.Context) (*dto.Hero, error)
	// }

	Order interface {
		CreateOrder(ctx context.Context, orderNew *entity.OrderNew) (*entity.Order, error)
		ApplyPromoCode(ctx context.Context, orderId int, promoCode string) (decimal.Decimal, error)
		UpdateOrderItems(ctx context.Context, orderId int, items []entity.OrderItemInsert) error
		UpdateOrderShippingCarrier(ctx context.Context, orderId int, shipmentCarrierId int) error
		OrderPaymentDone(ctx context.Context, orderId int, payment *entity.PaymentInsert) error
		UpdateShippingInfo(ctx context.Context, orderId int, shipment *entity.Shipment) error
		GetOrderById(ctx context.Context, orderId int) (*entity.OrderFull, error)
		GetOrdersByEmail(ctx context.Context, email string) ([]entity.OrderFull, error)
		GetOrdersByStatus(ctx context.Context, status entity.OrderStatusName) ([]entity.OrderFull, error)
		RefundOrder(ctx context.Context, orderId int) error
		DeliveredOrder(ctx context.Context, orderId int) error
		CancelOrder(ctx context.Context, orderId int) error
	}

	Subscribers interface {
		GetActiveSubscribers(ctx context.Context) ([]entity.BuyerInsert, error)
		Subscribe(ctx context.Context, email, name string) error
		Unsubscribe(ctx context.Context, email string) error
	}

	Promo interface {
		AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error
		ListPromos(ctx context.Context) ([]entity.PromoCode, error)
		DeletePromoCode(ctx context.Context, code string) error
		DisablePromoCode(ctx context.Context, code string) error
	}

	Archive interface {
		AddArchive(ctx context.Context, archiveNew *entity.ArchiveNew) (int, error)
		AddArchiveItems(ctx context.Context, archiveId int, archiveItemNew []entity.ArchiveItemInsert) error
		DeleteArchiveItem(ctx context.Context, archiveItemID int) error
		GetArchivesPaged(ctx context.Context, limit int, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveFull, error)
		GetArchiveById(ctx context.Context, id int) (*entity.ArchiveFull, error)
		DeleteArchiveById(ctx context.Context, id int) error
	}
	Media interface {
		AddMedia(ctx context.Context, media *entity.MediaInsert) error
		DeleteMediaById(ctx context.Context, id int) error
		ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.Media, error)
	}

	Admin interface {
		AddAdmin(ctx context.Context, un, pwHash string) error
		DeleteAdmin(ctx context.Context, username string) error
		ChangePassword(ctx context.Context, un, newHash string) error
		PasswordHashByUsername(ctx context.Context, un string) (string, error)
		GetAdminByUsername(ctx context.Context, username string) (*dto.Admin, error)
	}

	Repository interface {
		Products() Products
		// Hero() Hero
		Order() Order
		Promo() Promo
		Admin() Admin
		Archive() Archive
		Media() Media
		Tx(ctx context.Context, f func(context.Context, Repository) error) error
		TxBegin(ctx context.Context) (Repository, error)
		TxCommit(ctx context.Context) error
		TxRollback(ctx context.Context) error
		Now() time.Time
		InTx() bool
		Close()
		IsErrUniqueViolation(err error) bool
		IsErrorRepeat(err error) bool
		Cache() Cache
		DB() DB
	}

	// DB represents database interface.
	DB interface {
		BeginTxx(ctx context.Context, opts *sql.TxOptions) (*sqlx.Tx, error)
		ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)

		// sqlx methods
		GetContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
		NamedExecContext(ctx context.Context, query string, arg interface{}) (sql.Result, error)
		NamedQuery(query string, arg interface{}) (*sqlx.Rows, error)
		PrepareNamedContext(ctx context.Context, query string) (*sqlx.NamedStmt, error)
		PreparexContext(ctx context.Context, query string) (*sqlx.Stmt, error)
		QueryRowxContext(ctx context.Context, query string, args ...interface{}) *sqlx.Row
		QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error)
		SelectContext(ctx context.Context, dest interface{}, query string, args ...interface{}) error
	}

	FileStore interface {
		UploadContentImage(ctx context.Context, rawB64Image, folder, imageName string) (*pb_common.Media, error)
		// UploadContentVideo uploads mp4 video to bucket
		UploadContentVideo(ctx context.Context, raw []byte, folder, videoName, contentType string) (*pb_common.Media, error)
		// DeleteFromBucket deletes an object from the specified bucket.
		DeleteFromBucket(ctx context.Context, objectKeys []string) error
		// ListObjects list all objects in base folder
		ListObjects(ctx context.Context) ([]*pb_common.ListEntityMedia, error)
	}

	Rates interface {
		GetExchangeRate(targetCurrency string) (float64, error)
	}

	Cache interface {
		GetCategoryById(id int) (*entity.Category, bool)
		GetCategoryByName(category entity.CategoryEnum) (entity.Category, bool)

		GetMeasurementById(id int) (*entity.MeasurementName, bool)
		GetMeasurementsByName(measurement entity.MeasurementNameEnum) (entity.MeasurementName, bool)

		GetOrderStatusById(id int) (*entity.OrderStatus, bool)
		GetOrderStatusByName(orderStatus entity.OrderStatusName) (entity.OrderStatus, bool)

		GetPaymentMethodById(id int) (*entity.PaymentMethod, bool)
		GetPaymentMethodsByName(paymentMethod entity.PaymentMethodName) (entity.PaymentMethod, bool)

		GetPromoById(id int) (*entity.PromoCode, bool)
		GetPromoByName(paymentMethod string) (entity.PromoCode, bool)
		AddPromo(promo entity.PromoCode)
		DeletePromo(code string)
		DisablePromo(code string)

		GetShipmentCarrierById(id int) (*entity.ShipmentCarrier, bool)
		GetShipmentCarriersByName(carrier string) (entity.ShipmentCarrier, bool)

		GetSizeById(id int) (*entity.Size, bool)
		GetSizesByName(size entity.SizeEnum) (entity.Size, bool)

		GetDict() *dto.Dict
	}
)
