package dependency

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/dto"
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	"github.com/jekabolt/grbpwr-manager/openapi/gen/resend"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/stripe/stripe-go/v79"
)

//go:generate mockery --log-level=warn
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
		// GetProductsByIds returns a list of products by their IDs.
		GetProductsByIds(ctx context.Context, ids []int) ([]entity.Product, error)
		// GetProductsByTag returns a list of products by their tag.
		GetProductsByTag(ctx context.Context, tag string) ([]entity.Product, error)
		// GetProductByIdShowHidden returns a product by its ID no matter hidden they or not.
		GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ProductFull, error)
		// GetProductByIdNoHidden returns a product by its ID, excluding hidden products.
		GetProductByIdNoHidden(ctx context.Context, id int) (*entity.ProductFull, error)
		// DeleteProductById deletes a product by its ID.
		DeleteProductById(ctx context.Context, id int) error
		// ReduceStockForProductSizes reduces the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// RestoreStockForProductSizes restores the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// UpdateProductSizeStock adds a new available size for a product.
		UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error
		// UpdateProductSizeStockWithHistory updates stock and records to product_stock_change_history.
		UpdateProductSizeStockWithHistory(ctx context.Context, productId int, sizeId int, quantity int) error
		// GetProductSizeStock gets the current stock quantity for a specific product/size combination.
		GetProductSizeStock(ctx context.Context, productId int, sizeId int) (decimal.Decimal, bool, error)
		// AddToWaitlist adds an email to the waitlist for a specific product/size combination.
		AddToWaitlist(ctx context.Context, productId int, sizeId int, email string) error
		// GetWaitlistEntriesByProductSize retrieves all waitlist entries for a specific product/size combination.
		GetWaitlistEntriesByProductSize(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntry, error)
		// RemoveFromWaitlist removes a specific waitlist entry.
		RemoveFromWaitlist(ctx context.Context, productId int, sizeId int, email string) error
		// RemoveFromWaitlistBatch removes all waitlist entries for a specific product/size combination.
		RemoveFromWaitlistBatch(ctx context.Context, productId int, sizeId int) error
		// GetWaitlistEntriesWithBuyerNames retrieves waitlist entries with buyer names in a single query.
		GetWaitlistEntriesWithBuyerNames(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntryWithBuyer, error)
		// RecordStockChange inserts stock change history entries.
		RecordStockChange(ctx context.Context, entries []entity.StockChangeInsert) error
		// GetStockChangeHistory returns paginated stock change history with optional filters.
		GetStockChangeHistory(ctx context.Context, productId, sizeId *int, dateFrom, dateTo *time.Time, source string, limit, offset int, orderFactor entity.OrderFactor) ([]entity.StockChange, int, error)
	}
	Hero interface {
		RefreshHero(ctx context.Context) error
		SetHero(ctx context.Context, hfi entity.HeroFullInsert) error
		GetHero(ctx context.Context) (*entity.HeroFullWithTranslations, error)
	}

	Mail interface {
		AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error)
		GetAllUnsent(ctx context.Context, withError bool) ([]entity.SendEmailRequest, error)
		UpdateSent(ctx context.Context, id int) error
		AddError(ctx context.Context, id int, errMsg string) error
	}

	Order interface {
		CreateOrder(ctx context.Context, orderNew *entity.OrderNew, receivePromo bool, expiredAt time.Time) (*entity.Order, bool, error)
		ValidateOrderItemsInsert(ctx context.Context, items []entity.OrderItemInsert, currency string) (*entity.OrderItemValidation, error)
		ValidateOrderItemsInsertWithReservation(ctx context.Context, items []entity.OrderItemInsert, currency string, sessionID string) (*entity.OrderItemValidation, error)
		ValidateOrderByUUID(ctx context.Context, orderUUID string) (*entity.OrderFull, error)
		InsertFiatInvoice(ctx context.Context, orderUUID string, clientSecret string, pm entity.PaymentMethod, expiredAt time.Time) (*entity.OrderFull, error)
		AssociatePaymentIntentWithOrder(ctx context.Context, orderUUID string, paymentIntentId string) error
		UpdateTotalPaymentCurrency(ctx context.Context, orderUUID string, tapc decimal.Decimal) error
		SetTrackingNumber(ctx context.Context, orderUUID string, trackingCode string) (*entity.OrderBuyerShipment, error)
		GetOrderById(ctx context.Context, orderID int) (*entity.OrderFull, error)
		GetPaymentByOrderUUID(ctx context.Context, orderUUID string) (*entity.Payment, error)
		GetOrderFullByUUID(ctx context.Context, orderUUID string) (*entity.OrderFull, error)
		GetOrderByUUIDAndEmail(ctx context.Context, orderUUID string, email string) (*entity.OrderFull, error)
		GetOrderByUUID(ctx context.Context, orderUUID string) (*entity.Order, error)
		GetOrderByPaymentIntentId(ctx context.Context, paymentIntentId string) (*entity.OrderFull, error)
		GetOrdersByStatusAndPaymentTypePaged(ctx context.Context, email string, statusId, paymentMethodId, orderId, lim int, off int, of entity.OrderFactor) ([]entity.Order, error)
		GetAwaitingPaymentsByPaymentType(ctx context.Context, pmn ...entity.PaymentMethodName) ([]entity.PaymentOrderUUID, error)
		ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error)
		OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (wasUpdated bool, err error)
		RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason string) error
		DeliveredOrder(ctx context.Context, orderUUID string) error
		CancelOrder(ctx context.Context, orderUUID string) error
		GetStuckPlacedOrders(ctx context.Context, olderThan time.Time) ([]entity.Order, error)
		GetExpiredAwaitingPaymentOrders(ctx context.Context, now time.Time) ([]entity.Order, error)
		CancelOrderByUser(ctx context.Context, orderUUID string, email string, reason string) (*entity.OrderFull, error)
		SetOrderStatusToPendingReturn(ctx context.Context, orderUUID string, changedBy string) error
		AddOrderComment(ctx context.Context, orderUUID string, comment string) error
	}

	// TODO: invoice to separate interface
	Invoicer interface {
		GetOrderInvoice(ctx context.Context, orderUUID string) (*entity.PaymentInsert, error)
		CancelMonitorPayment(orderUUID string) error
		CheckForTransactions(ctx context.Context, orderUUID string, payment entity.Payment) (*entity.Payment, error)
		ExpirationDuration() time.Duration
		// CreatePreOrderPaymentIntent creates a PaymentIntent before order submission (for card payments)
		CreatePreOrderPaymentIntent(ctx context.Context, amount decimal.Decimal, currency string, country string, idempotencyKey string) (*stripe.PaymentIntent, error)
		// UpdatePaymentIntentWithOrder updates an existing PaymentIntent with order details
		UpdatePaymentIntentWithOrder(ctx context.Context, paymentIntentID string, order entity.OrderFull) error
		// UpdatePaymentIntentWithOrderNew updates a PaymentIntent with order data from OrderNew (optimized, no DB query)
		UpdatePaymentIntentWithOrderNew(ctx context.Context, paymentIntentID string, orderUUID string, orderNew *entity.OrderNew) error
		// GetPaymentIntentByID retrieves a PaymentIntent by its ID
		GetPaymentIntentByID(ctx context.Context, paymentIntentID string) (*stripe.PaymentIntent, error)
		// UpdatePaymentIntentAmount updates the amount of an existing PaymentIntent
		UpdatePaymentIntentAmount(ctx context.Context, paymentIntentID string, amount decimal.Decimal, currency string) error
		// StartMonitoringPayment starts monitoring an existing payment
		StartMonitoringPayment(ctx context.Context, orderUUID string, payment entity.Payment)
		// Refund performs a Stripe refund for an order. No-op for non-Stripe payment methods.
		// If amount is nil, performs full refund. Otherwise refunds the specified amount in order currency.
		Refund(ctx context.Context, payment entity.Payment, orderUUID string, amount *decimal.Decimal, currency string) error
	}

	StripePayment interface {
		CreatePaymentIntent(order entity.OrderFull) (*stripe.PaymentIntent, error)
	}

	Subscribers interface {
		GetActiveSubscribers(ctx context.Context) ([]entity.Subscriber, error)
		UpsertSubscription(ctx context.Context, email string, receivePromo bool) (bool, error)
		IsSubscribed(ctx context.Context, email string) (bool, error)
		GetNewSubscribersCount(ctx context.Context, from, to time.Time) (int, error)
	}

	Metrics interface {
		GetBusinessMetrics(ctx context.Context, period, comparePeriod entity.TimeRange, granularity entity.MetricsGranularity) (*entity.BusinessMetrics, error)
	}

	Support interface {
		GetSupportTicketsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, status bool) ([]entity.SupportTicket, error)
		UpdateStatus(ctx context.Context, id int, status bool) error
		SubmitTicket(ctx context.Context, ticket entity.SupportTicketInsert) error
	}

	Promo interface {
		AddPromo(ctx context.Context, promo *entity.PromoCodeInsert) error
		ListPromos(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.PromoCode, error)
		DeletePromoCode(ctx context.Context, code string) error
		DisablePromoCode(ctx context.Context, code string) error
		DisableVoucher(ctx context.Context, promoID sql.NullInt32) error
	}

	Archive interface {
		AddArchive(ctx context.Context, archiveInsert *entity.ArchiveInsert) (int, error)
		UpdateArchive(ctx context.Context, id int, archiveInsert *entity.ArchiveInsert) error
		GetArchivesPaged(ctx context.Context, limit int, offset int, orderFactor entity.OrderFactor) ([]entity.ArchiveList, int, error)
		DeleteArchiveById(ctx context.Context, id int) error
		GetArchiveById(ctx context.Context, id int) (*entity.ArchiveFull, error)
		GetArchiveTranslations(ctx context.Context, id int) ([]entity.ArchiveTranslation, error)
	}

	GA4 interface {
		SaveGA4DailyMetrics(ctx context.Context, metrics []ga4.DailyMetrics) error
		SaveGA4ProductPageMetrics(ctx context.Context, metrics []ga4.ProductPageMetrics) error
		SaveGA4TrafficSourceMetrics(ctx context.Context, metrics []ga4.TrafficSourceMetrics) error
		SaveGA4DeviceMetrics(ctx context.Context, metrics []ga4.DeviceMetrics) error
		SaveGA4CountryMetrics(ctx context.Context, metrics []ga4.CountryMetrics) error
		UpdateGA4SyncStatus(ctx context.Context, syncType string, lastSyncDate time.Time, status string, recordsSynced int, errorMsg string) error
		GetGA4LastSyncDate(ctx context.Context, syncType string) (time.Time, error)
		GetGA4DailyMetrics(ctx context.Context, from, to time.Time) ([]ga4.DailyMetrics, error)
		GetGA4ProductPageMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductViewMetric, error)
		GetGA4TrafficSourceMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.TrafficSourceMetric, error)
		GetGA4DeviceMetrics(ctx context.Context, from, to time.Time) ([]entity.DeviceMetric, error)
		GetGA4SessionsByCountry(ctx context.Context, from, to time.Time, limit int) ([]entity.GeographySessionMetric, error)
	}

	Language interface {
		GetAllLanguages(ctx context.Context) ([]entity.Language, error)
		GetActiveLanguages(ctx context.Context) ([]entity.Language, error)
		GetLanguageByCode(ctx context.Context, code string) (*entity.Language, error)
		GetDefaultLanguage(ctx context.Context) (*entity.Language, error)
	}
	Media interface {
		AddMedia(ctx context.Context, media *entity.MediaItem) (int, error)
		GetMediaById(ctx context.Context, id int) (*entity.MediaFull, error)
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
		AddShipmentCarrier(ctx context.Context, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) (int, error)
		UpdateShipmentCarrier(ctx context.Context, id int, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) error
		DeleteShipmentCarrier(ctx context.Context, id int) error
		SetShipmentCarrierAllowance(ctx context.Context, carrier string, allowance bool) error
		SetShipmentCarrierPrices(ctx context.Context, carrier string, prices map[string]decimal.Decimal) error
		SetPaymentMethodAllowance(ctx context.Context, paymentMethod entity.PaymentMethodName, allowance bool) error
		SetSiteAvailability(ctx context.Context, allowance bool) error
		SetMaxOrderItems(ctx context.Context, count int) error
		SetBigMenu(ctx context.Context, bigMenu bool) error
		SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) error
		GetAnnounce(ctx context.Context) (*entity.AnnounceWithTranslations, error)
		SetOrderExpirationSeconds(ctx context.Context, seconds int) error
	}

	Waitlist interface {
		AddToWaitlist(ctx context.Context, productId int, sizeId int, email string) error
		GetWaitlistEntriesByProductSize(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntry, error)
		RemoveFromWaitlist(ctx context.Context, productId int, sizeId int, email string) error
		RemoveFromWaitlistBatch(ctx context.Context, productId int, sizeId int) error
		GetWaitlistEntriesWithBuyerNames(ctx context.Context, productId int, sizeId int) ([]entity.WaitlistEntryWithBuyer, error)
	}
	Repository interface {
		Products() Products
		Hero() Hero
		Order() Order
		Promo() Promo
		Admin() Admin
		Cache() Cache
		Mail() Mail
		Archive() Archive
		GA4() GA4
		Subscribers() Subscribers
		Metrics() Metrics
		Media() Media
		Settings() Settings
		Support() Support
		Language() Language
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

	Cache interface {
		GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error)
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

	RevalidationService interface {
		RevalidateAll(ctx context.Context, revalidationData *dto.RevalidationData) error
	}

	Mailer interface {
		SendNewSubscriber(ctx context.Context, rep Repository, to string) error
		QueueNewSubscriber(ctx context.Context, rep Repository, to string) error
		SendOrderConfirmation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderConfirmed) error
		QueueOrderConfirmation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderConfirmed) error
		SendOrderCancellation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderCancelled) error
		SendOrderShipped(ctx context.Context, rep Repository, to string, shipmentDetails *dto.OrderShipment) error
		SendRefundInitiated(ctx context.Context, rep Repository, to string, refundDetails *dto.OrderRefundInitiated) error
		SendPromoCode(ctx context.Context, rep Repository, to string, promoDetails *dto.PromoCodeDetails) error
		SendBackInStock(ctx context.Context, rep Repository, to string, productDetails *dto.BackInStock) error
		Start(ctx context.Context) error
		Stop() error
	}

	Sender interface {
		PostEmails(ctx context.Context, body resend.SendEmailRequest, reqEditors ...resend.RequestEditorFn) (*http.Response, error)
	}

	PaymentPool interface {
		AddPaymentExpiration(ctx context.Context, poid entity.PaymentOrderUUID) error
		RemovePaymentExpiration(orderId int) error
		Start(ctx context.Context) error
	}

	// StockReservationManager handles temporary stock holds
	StockReservationManager interface {
		Release(ctx context.Context, orderUUID string)
	}
)
