package dependency

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
)

//go:generate mockery --with-expecter --case underscore --all --output=./mocks
type (
	ContextStore interface {
		Tx(ctx context.Context, fn func(ctx context.Context, store Repository) error) error
	}
	Products interface {
		ContextStore
		// AddProduct adds a new product along with its associated data.
		AddProduct(ctx context.Context, prd *entity.ProductNew) (int, error)
		// AddProduct adds a new product along with its associated data.
		UpdateProduct(ctx context.Context, prd *entity.ProductNew, id int) error
		// GetProductsPaged returns a paged list of products based on provided parameters.
		GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Product, int, error)
		// GetProductByIdShowHidden returns a product by its ID no matter hidden they or not.
		GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ProductFull, error)
		// GetProductByName returns a product by its name if it is not hidden.
		GetProductByNameNoHidden(ctx context.Context, id int, name string) (*entity.ProductFull, error)
		// DeleteProductById deletes a product by its ID.
		DeleteProductById(ctx context.Context, id int) error
		// ReduceStockForProductSizes reduces the stock for a product by its ID.
		ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error
		// RestoreStockForProductSizes restores the stock for a product by its ID.
		RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert) error
		// UpdateProductSizeStock adds a new available size for a product.
		UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error
	}
	Hero interface {
		SetHero(ctx context.Context, ads []entity.HeroInsert, productIds []int) error
		GetHero(ctx context.Context) (*entity.HeroFull, error)
	}

	Mail interface {
		AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error)
		GetAllUnsent(ctx context.Context, withError bool) ([]entity.SendEmailRequest, error)
		UpdateSent(ctx context.Context, id int) error
		AddError(ctx context.Context, id int, errMsg string) error
	}

	Order interface {
		CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool) (*entity.Order, bool, error)
		ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert) (*entity.OrderItemValidation, error)
		ValidateOrderByUUID(ctx context.Context, orderUUID string) (*entity.OrderFull, error)
		InsertOrderInvoice(ctx context.Context, orderUUID string, addr string, pm *entity.PaymentMethod) (*entity.OrderFull, error)
		UpdateTotalPaymentCurrency(ctx context.Context, orderUUID string, tapc decimal.Decimal) error
		SetTrackingNumber(ctx context.Context, orderUUID string, trackingCode string) (*entity.OrderBuyerShipment, error)
		GetOrderById(ctx context.Context, orderID int) (*entity.OrderFull, error)
		GetPaymentByOrderUUID(ctx context.Context, orderUUID string) (*entity.Payment, error)
		GetOrderFullByUUID(ctx context.Context, orderUUID string) (*entity.OrderFull, error)
		GetOrderByUUID(ctx context.Context, orderUUID string) (*entity.Order, error)
		CheckPaymentPendingByUUID(ctx context.Context, orderUUID string) (*entity.Payment, *entity.Order, error)
		GetOrdersByStatusAndPaymentTypePaged(ctx context.Context, email string, statusId, paymentMethodId, orderId, lim int, off int, of entity.OrderFactor) ([]entity.Order, error)
		GetAwaitingPaymentsByPaymentType(ctx context.Context, pmn ...entity.PaymentMethodName) ([]entity.PaymentOrderUUID, error)
		ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error)
		OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (*entity.Payment, error)
		RefundOrder(ctx context.Context, orderUUID string) error
		DeliveredOrder(ctx context.Context, orderUUID string) error
		CancelOrder(ctx context.Context, orderUUID string) error
	}

	// TODO: invoice to separate interface
	CryptoInvoice interface {
		GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, time.Time, error)
		CancelMonitorPayment(orderUUID string) error
		CheckForTransactions(ctx context.Context, orderUUID string, payment *entity.Payment) (*entity.Payment, error)
	}
	Invoice interface {
		GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, time.Time, error)
	}

	Trongrid interface {
		GetAddressTransactions(address string) (*dto.TronTransactionsResponse, error)
	}

	Subscribers interface {
		GetActiveSubscribers(ctx context.Context) ([]entity.Subscriber, error)
		UpsertSubscription(ctx context.Context, email string, receivePromo bool) error
		IsSubscribed(ctx context.Context, email string) (bool, error)
	}

	Promo interface {
		AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error
		ListPromos(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.PromoCode, error)
		DeletePromoCode(ctx context.Context, code string) error
		DisablePromoCode(ctx context.Context, code string) error
		DisableVoucher(ctx context.Context, promoID sql.NullInt32) error
	}

	Rates interface {
		GetLatestRates(ctx context.Context) ([]entity.CurrencyRate, error)
		BulkUpdateRates(ctx context.Context, rates []entity.CurrencyRate) error
	}

	Archive interface {
		AddArchive(ctx context.Context, archiveNew *entity.ArchiveNew) (int, error)
		UpdateArchive(ctx context.Context, id int, archiveUpd *entity.ArchiveBody, archiveItems []entity.ArchiveItemInsert) error
		GetArchivesPaged(ctx context.Context, limit int, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveFull, error)
		DeleteArchiveById(ctx context.Context, id int) error
	}
	Media interface {
		AddMedia(ctx context.Context, media *entity.MediaItem) (int, error)
		DeleteMediaById(ctx context.Context, id int) error
		ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.MediaFull, error)
	}

	Admin interface {
		AddAdmin(ctx context.Context, un, pwHash string) error
		DeleteAdmin(ctx context.Context, username string) error
		ChangePassword(ctx context.Context, un, newHash string) error
		PasswordHashByUsername(ctx context.Context, un string) (string, error)
		GetAdminByUsername(ctx context.Context, username string) (*entity.Admin, error)
	}

	Settings interface {
		SetShipmentCarrierAllowance(ctx context.Context, carrier string, allowance bool) error
		SetShipmentCarrierPrice(ctx context.Context, carrier string, price decimal.Decimal) error
		SetPaymentMethodAllowance(ctx context.Context, paymentMethod entity.PaymentMethodName, allowance bool) error
		SetSiteAvailability(ctx context.Context, allowance bool) error
		SetMaxOrderItems(ctx context.Context, count int) error
	}

	Repository interface {
		Products() Products
		Hero() Hero
		Order() Order
		Promo() Promo
		Rates() Rates
		Admin() Admin
		Mail() Mail
		Archive() Archive
		Subscribers() Subscribers
		Media() Media
		Settings() Settings
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
		UploadContentImage(ctx context.Context, rawB64Image, folder, imageName string) (*pb_common.MediaFull, error)
		// UploadContentVideo uploads mp4 video to bucket
		UploadContentVideo(ctx context.Context, raw []byte, folder, videoName, contentType string) (*pb_common.MediaFull, error)
		// GetBaseFolder returns the base folder for the bucket
		GetBaseFolder() string
	}

	RatesService interface {
		Start()
		Stop()
		GetRates() map[dto.CurrencyTicker]dto.CurrencyRate
		GetBaseCurrency() dto.CurrencyTicker
		ConvertToBaseCurrency(currencyFrom dto.CurrencyTicker, amount decimal.Decimal) (decimal.Decimal, error)
		ConvertFromBaseCurrency(currencyTo dto.CurrencyTicker, amount decimal.Decimal) (decimal.Decimal, error)
	}

	Mailer interface {
		SendNewSubscriber(ctx context.Context, rep Repository, to string) error
		SendOrderConfirmation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderConfirmed) error
		SendOrderCancellation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderCancelled) error
		SendOrderShipped(ctx context.Context, rep Repository, to string, shipmentDetails *dto.OrderShipment) error
		SendPromoCode(ctx context.Context, rep Repository, to string, promoDetails *dto.PromoCodeDetails) error
		Start(ctx context.Context) error
		Stop() error
	}

	Sender interface {
		PostEmails(ctx context.Context, body resend.SendEmailRequest, reqEditors ...resend.RequestEditorFn) (*http.Response, error)
	}

	Cache interface {
		GetCategoryById(id int) (*entity.Category, bool)
		GetCategoryByName(category entity.CategoryEnum) (entity.Category, bool)

		GetMeasurementById(id int) (*entity.MeasurementName, bool)
		GetMeasurementsByName(measurement entity.MeasurementNameEnum) (entity.MeasurementName, bool)

		GetOrderStatusById(id int) (*entity.OrderStatus, bool)
		GetOrderStatusByName(orderStatus entity.OrderStatusName) (entity.OrderStatus, bool)

		GetPaymentMethodById(id int) (*entity.PaymentMethod, bool)
		GetPaymentMethodByName(paymentMethod entity.PaymentMethodName) (entity.PaymentMethod, bool)
		UpdatePaymentMethodAllowance(pm entity.PaymentMethodName, allowance bool) error

		GetPromoById(id int) (*entity.PromoCode, bool)
		GetPromoByName(paymentMethod string) (entity.PromoCode, bool)
		AddPromo(promo entity.PromoCode)
		DeletePromo(code string)
		DisablePromo(code string)

		GetShipmentCarrierById(id int) (*entity.ShipmentCarrier, bool)
		GetShipmentCarriersByName(carrier string) (entity.ShipmentCarrier, bool)
		UpdateShipmentCarrierAllowance(carrier string, allowance bool) error
		UpdateShipmentCarrierCost(carrier string, cost decimal.Decimal) error
		GetAllShipmentCarriers() map[int]entity.ShipmentCarrier

		GetSizeById(id int) (*entity.Size, bool)
		GetSizesByName(size entity.SizeEnum) (entity.Size, bool)
		GetAllSizes() map[int]entity.Size

		GetHero() *entity.HeroFull
		UpdateHero(hf *entity.HeroFull)
		DeleteHero()

		GetDict() *dto.Dict

		SetSiteAvailability(available bool)
		SetMaxOrderItems(count int)
		SetDefaultCurrency(cur dto.CurrencyTicker)
	}

	PaymentPool interface {
		AddPaymentExpiration(ctx context.Context, poid entity.PaymentOrderUUID) error
		RemovePaymentExpiration(orderId int) error
		Start(ctx context.Context) error
	}
)
