package dependency

import (
	"context"
	"database/sql"
	"net/http"
	"time"

	bq "github.com/jekabolt/grbpwr-manager/internal/analytics/bigquery"
	"github.com/jekabolt/grbpwr-manager/internal/analytics/ga4"
	"github.com/jekabolt/grbpwr-manager/internal/circuitbreaker"
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
		UpdateProductSizeStockWithHistory(ctx context.Context, productId int, sizeId int, quantity int, reason string, comment string) error
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
		// GetStockChanges returns simplified stock changes for reporting API.
		GetStockChanges(ctx context.Context, dateFrom, dateTo time.Time, sku *string, source *string, limit, offset int) ([]entity.StockChangeRow, int, error)
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
		CreateCustomOrder(ctx context.Context, orderNew *entity.OrderNew) (*entity.Order, error)
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
		GetOrdersByStatusAndPaymentTypePaged(ctx context.Context, email string, orderUUID string, statusId, paymentMethodId, orderId, lim int, off int, of entity.OrderFactor) ([]entity.Order, error)
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
		// GetOrCreatePreOrderPaymentIntent gets or creates a PaymentIntent for pre-order, with idempotency and rotation.
		// Returns (pi, rotatedKey, err). If rotatedKey != "", client should replace stored key.
		// ErrPaymentAlreadyCompleted when PI was already used for a completed payment.
		GetOrCreatePreOrderPaymentIntent(ctx context.Context, idempotencyKey string, amount decimal.Decimal, currency, country string, cartFingerprint string) (pi *stripe.PaymentIntent, rotatedKey string, err error)
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

	Inventory interface {
		GetInventoryHealth(ctx context.Context, from, to time.Time, limit int) ([]entity.InventoryHealthRow, error)
		GetSizeRunEfficiency(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeRunEfficiencyRow, error)
	}

	Retention interface {
		GetCohortRetention(ctx context.Context, from, to time.Time) ([]entity.CohortRetentionRow, error)
		GetOrderSequenceMetrics(ctx context.Context, from, to time.Time) ([]entity.OrderSequenceMetric, error)
		GetEntryProducts(ctx context.Context, from, to time.Time, limit int) ([]entity.EntryProductMetric, error)
		GetRevenuePareto(ctx context.Context, from, to time.Time, limit int) ([]entity.RevenueParetoRow, error)
		GetCustomerSpendingCurve(ctx context.Context, from, to time.Time) ([]entity.SpendingCurvePoint, error)
		GetCategoryLoyalty(ctx context.Context, from, to time.Time) ([]entity.CategoryLoyaltyRow, error)
	}

	Analytics interface {
		GetSlowMovers(ctx context.Context, from, to time.Time, limit int) ([]entity.SlowMoverRow, error)
		GetReturnByProduct(ctx context.Context, from, to time.Time, limit int) ([]entity.ReturnByProductRow, error)
		GetReturnBySize(ctx context.Context, from, to time.Time) ([]entity.ReturnBySizeRow, error)
		GetSizeAnalytics(ctx context.Context, from, to time.Time, limit int) ([]entity.SizeAnalyticsRow, error)
		GetDeadStock(ctx context.Context, from, to time.Time, limit int) ([]entity.DeadStockRow, error)
		GetProductTrend(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductTrendRow, error)
	}

	// Metrics aggregates Retention, Inventory, Analytics plus business metrics.
	// Embedding ensures new methods on those interfaces are automatically included.
	Metrics interface {
		Retention
		Inventory
		Analytics
		GetBusinessMetrics(ctx context.Context, period, comparePeriod entity.TimeRange, granularity entity.MetricsGranularity) (*entity.BusinessMetrics, error)
	}

	Support interface {
		GetSupportTicketsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, filters entity.SupportTicketFilters) ([]entity.SupportTicket, int, error)
		GetSupportTicketById(ctx context.Context, id int) (entity.SupportTicket, error)
		GetSupportTicketByCaseNumber(ctx context.Context, caseNumber string) (entity.SupportTicket, error)
		UpdateStatus(ctx context.Context, id int, status entity.SupportTicketStatus) error
		UpdatePriority(ctx context.Context, id int, priority entity.SupportTicketPriority) error
		UpdateCategory(ctx context.Context, id int, category string) error
		UpdateInternalNotes(ctx context.Context, id int, notes string) error
		SubmitTicket(ctx context.Context, ticket entity.SupportTicketInsert) (string, error)
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

	// BQClient is the BigQuery analytics client interface. Implementations can be mocked for testing.
	BQClient interface {
		CircuitBreakerState() circuitbreaker.State
		Close() error
		GetFunnelAnalysis(ctx context.Context, startDate, endDate time.Time) ([]entity.DailyFunnel, error)
		GetFunnelAnalysisStream(ctx context.Context, startDate, endDate time.Time, batchSize int, fn func([]entity.DailyFunnel) error) error
		GetOOSImpact(ctx context.Context, startDate, endDate time.Time) ([]entity.OOSImpactMetric, error)
		GetPaymentFailures(ctx context.Context, startDate, endDate time.Time) ([]entity.PaymentFailureMetric, error)
		GetWebVitals(ctx context.Context, startDate, endDate time.Time) ([]entity.WebVitalMetric, error)
		GetUserJourneys(ctx context.Context, startDate, endDate time.Time, limit int) ([]entity.UserJourneyMetric, error)
		GetSessionDuration(ctx context.Context, startDate, endDate time.Time) ([]entity.SessionDurationMetric, error)
		GetSizeIntent(ctx context.Context, startDate, endDate time.Time) ([]bq.SizeIntentRow, error)
		GetDeviceFunnel(ctx context.Context, startDate, endDate time.Time) ([]entity.DeviceFunnelMetric, error)
		GetProductEngagement(ctx context.Context, startDate, endDate time.Time) ([]entity.ProductEngagementMetric, error)
		GetFormErrors(ctx context.Context, startDate, endDate time.Time) ([]entity.FormErrorMetric, error)
		GetExceptions(ctx context.Context, startDate, endDate time.Time) ([]entity.ExceptionMetric, error)
		Get404Pages(ctx context.Context, startDate, endDate time.Time) ([]entity.NotFoundMetric, error)
		GetHeroFunnel(ctx context.Context, startDate, endDate time.Time) ([]entity.HeroFunnelMetric, error)
		GetSizeConfidence(ctx context.Context, startDate, endDate time.Time) ([]entity.SizeConfidenceMetric, error)
		GetPaymentRecovery(ctx context.Context, startDate, endDate time.Time) ([]entity.PaymentRecoveryMetric, error)
		GetCheckoutTimings(ctx context.Context, startDate, endDate time.Time) ([]entity.CheckoutTimingMetric, error)
		GetAddToCartRate(ctx context.Context, startDate, endDate time.Time) ([]entity.AddToCartRateRow, error)
		GetBrowserBreakdown(ctx context.Context, startDate, endDate time.Time) ([]entity.BrowserBreakdownRow, error)
		GetNewsletterSignups(ctx context.Context, startDate, endDate time.Time) ([]entity.NewsletterMetricRow, error)
		GetAbandonedCart(ctx context.Context, startDate, endDate time.Time) ([]entity.AbandonedCartRow, error)
		GetCampaignAttribution(ctx context.Context, startDate, endDate time.Time) ([]entity.CampaignAttributionRow, error)
		GetTimeOnPage(ctx context.Context, startDate, endDate time.Time) ([]entity.TimeOnPageRow, error)
		GetProductZoom(ctx context.Context, startDate, endDate time.Time) ([]entity.ProductZoomRow, error)
		GetImageSwipes(ctx context.Context, startDate, endDate time.Time) ([]entity.ImageSwipeRow, error)
		GetSizeGuideClicks(ctx context.Context, startDate, endDate time.Time) ([]entity.SizeGuideClickRow, error)
		GetDetailsExpansion(ctx context.Context, startDate, endDate time.Time) ([]entity.DetailsExpansionRow, error)
		GetNotifyMeIntent(ctx context.Context, startDate, endDate time.Time) ([]entity.NotifyMeIntentRow, error)
	}

	// GA4DataStore handles GA4 Data API persistence (raw GA4 metrics)
	GA4DataStore interface {
		SaveGA4DailyMetrics(ctx context.Context, metrics []ga4.DailyMetrics) error
		SaveGA4ProductPageMetrics(ctx context.Context, metrics []ga4.ProductPageMetrics) error
		SaveGA4CountryMetrics(ctx context.Context, metrics []ga4.CountryMetrics) error
		SaveGA4EcommerceMetrics(ctx context.Context, metrics []ga4.EcommerceMetrics) error
		SaveGA4RevenueBySource(ctx context.Context, metrics []ga4.RevenueSourceMetrics) error
		SaveGA4ProductConversion(ctx context.Context, metrics []ga4.ProductConversionMetrics) error
		GetGA4DailyMetrics(ctx context.Context, from, to time.Time) ([]ga4.DailyMetrics, error)
		GetGA4ProductPageMetrics(ctx context.Context, from, to time.Time, limit int) ([]entity.ProductViewMetric, error)
		GetGA4SessionsByCountry(ctx context.Context, from, to time.Time, limit int) ([]entity.GeographySessionMetric, error)
	}

	// BQCacheStoreRead handles BigQuery precomputed analytics cache reads.
	// High-cardinality methods accept limit, offset for pagination; 0 limit uses default 500.
	BQCacheStoreRead interface {
		SumBQFunnelAnalysis(ctx context.Context, from, to time.Time) (*entity.FunnelAggregate, error)
		GetDailyBQFunnelAnalysis(ctx context.Context, from, to time.Time) ([]entity.DailyFunnel, error)
		GetBQOOSImpact(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.OOSImpactMetric, error)
		GetBQPaymentFailures(ctx context.Context, from, to time.Time) ([]entity.PaymentFailureMetric, error)
		GetBQWebVitals(ctx context.Context, from, to time.Time) ([]entity.WebVitalMetric, error)
		GetBQUserJourneys(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.UserJourneyMetric, error)
		GetBQSessionDuration(ctx context.Context, from, to time.Time) ([]entity.SessionDurationMetric, error)
		GetBQSizeIntent(ctx context.Context, from, to time.Time, limit, offset int) ([]bq.SizeIntentRow, error)
		GetBQDeviceFunnel(ctx context.Context, from, to time.Time) ([]entity.DeviceFunnelMetric, error)
		GetBQProductEngagement(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ProductEngagementMetric, error)
		GetBQFormErrors(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.FormErrorMetric, error)
		GetBQExceptions(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ExceptionMetric, error)
		GetBQNotFoundPages(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.NotFoundMetric, error)
		GetBQHeroFunnel(ctx context.Context, from, to time.Time) ([]entity.HeroFunnelMetric, error)
		GetBQSizeConfidence(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeConfidenceMetric, error)
		GetBQPaymentRecovery(ctx context.Context, from, to time.Time) ([]entity.PaymentRecoveryMetric, error)
		GetBQCheckoutTimings(ctx context.Context, from, to time.Time) ([]entity.CheckoutTimingMetric, error)
		GetBQAddToCartRate(ctx context.Context, from, to time.Time, granularity entity.TrendGranularity, limit, offset int) (*entity.AddToCartRateAnalysis, error)
		GetBQBrowserBreakdown(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.BrowserBreakdownRow, error)
		GetBQNewsletter(ctx context.Context, from, to time.Time) ([]entity.NewsletterMetricRow, error)
		GetBQAbandonedCart(ctx context.Context, from, to time.Time) ([]entity.AbandonedCartRow, error)
		GetBQCampaignAttribution(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.CampaignAttributionRow, error)
		GetBQTimeOnPage(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.TimeOnPageRow, error)
		GetBQProductZoom(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ProductZoomRow, error)
		GetBQImageSwipes(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ImageSwipeRow, error)
		GetBQSizeGuideClicks(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeGuideClickRow, error)
		GetBQDetailsExpansion(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.DetailsExpansionRow, error)
		GetBQNotifyMeIntent(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.NotifyMeIntentRow, error)
	}

	// BQCacheStoreWriter handles BigQuery precomputed analytics cache writes
	BQCacheStoreWriter interface {
		DeleteBQFunnelAnalysisByDateRange(ctx context.Context, startDate, endDate time.Time) error
		InsertBQFunnelAnalysisBatch(ctx context.Context, rows []entity.DailyFunnel) error
		SaveBQFunnelAnalysis(ctx context.Context, rows []entity.DailyFunnel) error
		SaveBQOOSImpact(ctx context.Context, rows []entity.OOSImpactMetric) error
		SaveBQPaymentFailures(ctx context.Context, rows []entity.PaymentFailureMetric) error
		SaveBQWebVitals(ctx context.Context, rows []entity.WebVitalMetric) error
		SaveBQUserJourneys(ctx context.Context, rows []entity.UserJourneyMetric) error
		SaveBQSessionDuration(ctx context.Context, rows []entity.SessionDurationMetric) error
		SaveBQSizeIntent(ctx context.Context, rows []bq.SizeIntentRow) error
		SaveBQDeviceFunnel(ctx context.Context, rows []entity.DeviceFunnelMetric) error
		SaveBQProductEngagement(ctx context.Context, rows []entity.ProductEngagementMetric) error
		SaveBQFormErrors(ctx context.Context, rows []entity.FormErrorMetric) error
		SaveBQExceptions(ctx context.Context, rows []entity.ExceptionMetric) error
		SaveBQNotFoundPages(ctx context.Context, rows []entity.NotFoundMetric) error
		SaveBQHeroFunnel(ctx context.Context, rows []entity.HeroFunnelMetric) error
		SaveBQSizeConfidence(ctx context.Context, rows []entity.SizeConfidenceMetric) error
		SaveBQPaymentRecovery(ctx context.Context, rows []entity.PaymentRecoveryMetric) error
		SaveBQCheckoutTimings(ctx context.Context, rows []entity.CheckoutTimingMetric) error
		SaveBQAddToCartRate(ctx context.Context, rows []entity.AddToCartRateRow) error
		SaveBQBrowserBreakdown(ctx context.Context, rows []entity.BrowserBreakdownRow) error
		SaveBQNewsletter(ctx context.Context, rows []entity.NewsletterMetricRow) error
		SaveBQAbandonedCart(ctx context.Context, rows []entity.AbandonedCartRow) error
		SaveBQCampaignAttribution(ctx context.Context, rows []entity.CampaignAttributionRow) error
		SaveBQTimeOnPage(ctx context.Context, rows []entity.TimeOnPageRow) error
		SaveBQProductZoom(ctx context.Context, rows []entity.ProductZoomRow) error
		SaveBQImageSwipes(ctx context.Context, rows []entity.ImageSwipeRow) error
		SaveBQSizeGuideClicks(ctx context.Context, rows []entity.SizeGuideClickRow) error
		SaveBQDetailsExpansion(ctx context.Context, rows []entity.DetailsExpansionRow) error
		SaveBQNotifyMeIntent(ctx context.Context, rows []entity.NotifyMeIntentRow) error
	}

	// BQCacheStore combines read and write for backward compatibility
	BQCacheStore interface {
		BQCacheStoreRead
		BQCacheStoreWriter
	}

	// SyncStatusStore handles sync metadata and retention
	SyncStatusStore interface {
		UpdateGA4SyncStatus(ctx context.Context, syncType string, lastSyncDate time.Time, success bool, recordsSynced int, errorMsg string) error
		GetGA4LastSyncDate(ctx context.Context, syncType string) (time.Time, error)
		GetGA4MinLastSyncDate(ctx context.Context) (time.Time, error)
		GetAllSyncStatuses(ctx context.Context) ([]entity.SyncSourceStatus, error)
		DeleteOldAnalyticsData(ctx context.Context, olderThan time.Time) (int64, error)
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
		SetPaymentIsProd(ctx context.Context, isProd bool) error
		SetSiteAvailability(ctx context.Context, allowance bool) error
		SetMaxOrderItems(ctx context.Context, count int) error
		SetBigMenu(ctx context.Context, bigMenu bool) error
		SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) error
		GetAnnounce(ctx context.Context) (*entity.AnnounceWithTranslations, error)
		SetOrderExpirationSeconds(ctx context.Context, seconds int) error
		SetComplimentaryShippingPrices(ctx context.Context, prices map[string]decimal.Decimal) error
		GetComplimentaryShippingPrices(ctx context.Context) (map[string]decimal.Decimal, error)
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
		GA4Data() GA4DataStore
		BQCache() BQCacheStore
		SyncStatus() SyncStatusStore
		Subscribers() Subscribers
		Metrics() Metrics
		Inventory() Inventory
		Retention() Retention
		Analytics() Analytics
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
		SendPendingReturn(ctx context.Context, rep Repository, to string, details *dto.OrderPendingReturn) error
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
