package dependency

import (
	"context"
	"database/sql"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
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
		GetProductsPaged(ctx context.Context, limit, offset int, sortFactors []dto.SortFactor, filterConditions []dto.FilterCondition) ([]dto.Product, error)
		AddProduct(ctx context.Context, p *dto.Product) error
		GetProductByID(ctx context.Context, id int64) (*dto.Product, error)
		DeleteProductByID(ctx context.Context, id int64) error
		HideProductByID(ctx context.Context, id int64, hide bool) error
		DecreaseAvailableSizes(ctx context.Context, items []dto.Item) error
		SetSaleByID(ctx context.Context, id int64, salePercent float64) error
	}
	Hero interface {
		SetHero(ctx context.Context, left, right dto.HeroElement) error
		GetHero(ctx context.Context) (*dto.Hero, error)
	}
	Order interface {
		// CreateOrder is used to create a new order.
		CreateOrder(ctx context.Context, order *dto.Order) (*dto.Order, error)

		// ApplyPromoCode applies a promo code to an order and returns the order if promo is valid.
		ApplyPromoCode(ctx context.Context, orderId int64, promoCode string) error

		// GetOrder retrieves an existing order by its ID.
		GetOrder(ctx context.Context, orderID int64) (*dto.Order, error)

		// UpdateOrderStatus is used to update the status of an order.
		UpdateOrderStatus(ctx context.Context, orderID int64, status dto.OrderStatus) error

		// UpdateShippingInfo updates the shipping status of an order.
		UpdateShippingInfo(ctx context.Context, orderID int64, carrier string, trackingCode string, shippingTime time.Time) error

		// UpdateOrderTotalByCurrency retrieves the total price of an order
		// including shipping and updates total price in the order.
		UpdateOrderTotalByCurrency(ctx context.Context, orderID int64, pc dto.PaymentCurrency, promo *dto.PromoCode) (decimal.Decimal, error)

		// RefundOrder refunds an existing order.
		RefundOrder(ctx context.Context, orderID int64) error

		// UpdateOrderItems updates the items of an order.
		UpdateOrderItems(ctx context.Context, orderID int64, items []dto.Item) error

		// GetOrderCurrency gets orders currency
		GetOrderCurrency(ctx context.Context, orderID int64) (dto.PaymentCurrency, error)

		// OrderPaymentDone updates the payment status of an order and adds payment info to order.
		OrderPaymentDone(ctx context.Context, orderID int64, payment *dto.Payment) error

		// OrdersByEmail retrieves all orders for a given email address.
		OrdersByEmail(ctx context.Context, email string) ([]dto.Order, error)

		// GetOrderItems retrieves all items for a given order id.
		GetOrderItems(ctx context.Context, orderID int64) ([]dto.Item, error)
	}
	Purchase interface {
		// Acquire acquires an order if order is valid and all items are available
		Acquire(ctx context.Context, oid int64, payment *dto.Payment) error

		// ValidateOrder validates an order i.e checks if order is valid and all items are available
		ValidateOrder(ctx context.Context, oid int64) (bool, error)
	}

	Subscribers interface {
		GetActiveSubscribers(ctx context.Context) ([]string, error)
		Subscribe(ctx context.Context, email string) error
		Unsubscribe(ctx context.Context, email string) error
	}

	Promo interface {
		AddPromo(ctx context.Context, promo *dto.PromoCode) error
		GetAllPromoCodes(ctx context.Context) ([]dto.PromoCode, error)
		DeletePromoCode(ctx context.Context, code string) error
		GetPromoByCode(ctx context.Context, code string) (*dto.PromoCode, error)
		DisablePromoCode(ctx context.Context, code string) error
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
		Hero() Hero
		Order() Order
		Purchase() Purchase
		Promo() Promo
		Admin() Admin
		Tx(ctx context.Context, f func(context.Context, Repository) error) error
		TxBegin(ctx context.Context) (Repository, error)
		TxCommit(ctx context.Context) error
		TxRollback(ctx context.Context) error
		Now() time.Time
		InTx() bool
		Close()
		IsErrUniqueViolation(err error) bool
		IsErrorRepeat(err error) bool
		DB() DB
	}

	// DB represents database interface.
	DB interface {
		ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
		QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error)
		QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
		BeginTxx(ctx context.Context, opts *sql.TxOptions) (*sqlx.Tx, error)

		QueryxContext(ctx context.Context, query string, args ...interface{}) (*sqlx.Rows, error)
		QueryRowxContext(ctx context.Context, query string, args ...interface{}) *sqlx.Row
	}

	FileStore interface {
		UploadContentImage(ctx context.Context, rawB64Image, folder, imageName string) (*pb_common.Media, error)
		// UploadContentVideo uploads mp4 video to bucket
		UploadContentVideo(ctx context.Context, raw []byte, folder, videoName, contentType string) (*pb_common.Media, error)
		// DeleteFromBucket deletes an object from the specified bucket.
		DeleteFromBucket(ctx context.Context, objectKeys []string) error
	}
)
