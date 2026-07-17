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
		// CreateColorway creates a DRAFT colourway attached to an existing style (R2/R4 write
		// decomposition): colourway-owned data only (merch row, translations, media, tags, prices), no
		// style facts, variants or size chart. sql.ErrNoRows when the style is absent;
		// entity.ErrColorwayColorExists on a duplicate (style_id, color_code). Returns the colourway id.
		CreateColorway(ctx context.Context, styleID int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert) (int, error)
		// UpdateColorway patches a colourway's own fields under an optimistic guard on the shared
		// tech_card.lock_version (entity.ErrTechCardConflict on a stale value; sql.ErrNoRows when absent).
		// Never touches style facts, variants, stock or the chart. Returns the new shared lock_version.
		UpdateColorway(ctx context.Context, colorwayID, expectedVersion int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert) (int, error)
		// UpdateStyle is the sole writer of a style's catalogue facts (R4/§14.7), optimistically locked on
		// the shared tech_card.lock_version. A SKU-fact (season) change re-mints unfrozen siblings, or is
		// refused (entity.ErrStyleFrozenSiblings) if any sibling is SKU-frozen. Returns the new lock_version.
		UpdateStyle(ctx context.Context, styleID, expectedLockVersion int, patch entity.StylePatch) (int, error)
		// AddProduct is the legacy coupled create, retained as a store-level test fixture (no RPC surface
		// after UpsertColorway was decomposed).
		AddProduct(ctx context.Context, prd *entity.ColorwayNew) (int, error)
		// UpdateProduct is the legacy coupled update, retained as a store-level test fixture.
		UpdateProduct(ctx context.Context, prd *entity.ColorwayNew, id int) error
		// AssignPrimaryTechCardIfUnset makes techCardID the primary (authoritative-for-costing)
		// card of each given product that has no primary yet. Empty ids is a no-op.
		AssignPrimaryTechCardIfUnset(ctx context.Context, techCardID int, productIDs []int) error
		// SeedProductsCostPriceFromTechCard writes cost as the tech-card-sourced cost of every
		// product whose primary card is techCardID (and cost is not manual, and the card links
		// it), never overwriting a manual cost. Returns the number of products updated.
		SeedProductsCostPriceFromTechCard(ctx context.Context, techCardID int, cost decimal.Decimal) (int64, error)
		// SeedProductsCostBreakdownFromTechCard writes the per-unit COGS decomposition JSON onto the
		// same (primary, non-manual) products as SeedProductsCostPriceFromTechCard, so cost_price and
		// cost_breakdown stay in sync; a NULL breakdown clears any stale one. Returns rows updated.
		SeedProductsCostBreakdownFromTechCard(ctx context.Context, techCardID int, breakdown sql.NullString) (int64, error)
		// ForceSetProductCostPriceFromTechCard writes cost as the tech-card-sourced cost of one
		// product, overriding any manual value (explicit SyncProductCostFromTechCard action).
		ForceSetProductCostPriceFromTechCard(ctx context.Context, productID, techCardID int, cost decimal.Decimal) error
		// ReceiveProductionStock increments a product's per-size stock from a production run's
		// received quantities, recording each change with the production_received source. Runs on
		// the caller's connection (no new transaction) so it composes into ReceiveProductionRun.
		ReceiveProductionStock(ctx context.Context, productID int, perSize map[int]int, runID int, username string) error
		// SetProductCostPriceFromProductionRun writes cost (base) as the production-run-sourced
		// cost_price of a product, recording provenance (source + run id + timestamp).
		SetProductCostPriceFromProductionRun(ctx context.Context, productID, runID int, cost decimal.Decimal) error
		// SetPrimaryTechCard repoints a product's authoritative-for-costing card.
		SetPrimaryTechCard(ctx context.Context, productID, techCardID int) error
		// GetProductCostInfo returns a product's confidential COGS/provenance fields (admin only).
		GetProductCostInfo(ctx context.Context, id int) (*entity.ColorwayCostInfo, error)
		// SetProductCustoms sets a product's international-shipping customs data (HS code, ISO-3
		// origin, declared description); GetProductCustoms reads it back.
		SetProductCustoms(ctx context.Context, productID int, customs entity.ColorwayCustoms) error
		GetProductCustoms(ctx context.Context, productID int) (*entity.ColorwayCustoms, error)
		// IsProductLinkedToTechCard reports whether a product is currently linked to the card.
		IsProductLinkedToTechCard(ctx context.Context, productID, techCardID int) (bool, error)
		// GetProductsPaged returns a paged list of products based on provided parameters.
		GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, showHidden bool) ([]entity.Colorway, int, error)
		// GetProductsByIds returns a list of products by their IDs.
		GetProductsByIds(ctx context.Context, ids []int) ([]entity.Colorway, error)
		// GetProductsByTag returns a list of products by their tag.
		GetProductsByTag(ctx context.Context, tag string) ([]entity.Colorway, error)
		// GetLowStockProducts returns visible products with total stock in (0, threshold], ordered by ascending stock.
		GetLowStockProducts(ctx context.Context, threshold int, limit int) ([]entity.Colorway, error)
		// GetProductByIdShowHidden returns a product by its ID no matter hidden they or not.
		GetProductByIdShowHidden(ctx context.Context, id int) (*entity.ColorwayFull, error)
		// GetVariantByID returns a variant (product_size) by its stable id, sql.ErrNoRows if absent
		// (variant addressing never implicitly creates a variant, R2/p012).
		GetVariantByID(ctx context.Context, variantID int) (entity.Variant, error)
		// GetVariantBySKU returns a variant (product_size) by its public variant SKU, sql.ErrNoRows if
		// absent (storefront NotifyMe resolve, R2/R3/p013).
		GetVariantBySKU(ctx context.Context, variantSKU string) (entity.Variant, error)
		// CreateVariant adds a new variant (size) to a colourway at zero stock, ACTIVE, minting its
		// variant SKU (R2). Rejects an absent (sql.ErrNoRows) or archived colourway and a duplicate size.
		CreateVariant(ctx context.Context, colorwayID, sizeID int) (entity.Variant, error)
		// SetVariantStatus applies a lifecycle status to a variant under an optimistic guard (R2:
		// archive-not-delete). Returns sql.ErrNoRows if the variant is absent; size_id/SKU are immutable.
		SetVariantStatus(ctx context.Context, variantID int, target entity.VariantStatus) (entity.Variant, error)
		// RelinkDraftColorway moves a DRAFT colourway onto a different style (R4), guarded on both sides'
		// shared lock_version, re-minting its SKU. entity.ErrColorwayNotDraft if not draft,
		// entity.ErrTechCardConflict on a stale version, sql.ErrNoRows if colourway/target style absent.
		RelinkDraftColorway(ctx context.Context, colorwayID, targetStyleID, expectedColorwayVersion, expectedTargetStyleVersion int) error
		// GetProductByIdNoHidden returns a product by its ID, excluding hidden products.
		GetProductByIdNoHidden(ctx context.Context, id int) (*entity.ColorwayFull, error)
		// GetProductBySKU returns a product by its base SKU (public resolve key), excluding hidden.
		GetProductBySKU(ctx context.Context, sku string) (*entity.ColorwayFull, error)
		// DeleteProductById deletes a product by its ID.
		DeleteProductById(ctx context.Context, id int) error
		// PublishColorway transitions a colourway DRAFT->ACTIVE (R6), enforcing the sellable
		// preconditions and an optimistic guard on the current lifecycle_status.
		PublishColorway(ctx context.Context, colorwayID int) error
		// HideColorway transitions ACTIVE->HIDDEN (kept admin-visible, off the storefront).
		HideColorway(ctx context.Context, colorwayID int) error
		// UnhideColorway transitions HIDDEN->ACTIVE (back onto the storefront).
		UnhideColorway(ctx context.Context, colorwayID int) error
		// ArchiveColorway transitions ACTIVE|HIDDEN->ARCHIVED (terminal) and stamps the archival audit.
		ArchiveColorway(ctx context.Context, colorwayID int) error
		// ReduceStockForProductSizes reduces the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// RestoreStockForProductSizes restores the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// RestoreStockSilently restores stock without recording history (for expired orders).
		RestoreStockSilently(ctx context.Context, items []entity.OrderItemInsert) error
		// UpdateProductSizeStock adds a new available size for a product.
		UpdateProductSizeStock(ctx context.Context, productId int, sizeId int, quantity int) error
		// UpdateProductSizeStockWithHistory applies a stock change (mode Set=absolute, Adjust=signed
		// delta) and records history atomically under a row lock, returning the committed before/after.
		UpdateProductSizeStockWithHistory(ctx context.Context, productId int, sizeId int, mode entity.StockUpdateMode, amount int, reason string, comment string) (before decimal.Decimal, after decimal.Decimal, err error)
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
		GetStockChanges(ctx context.Context, dateFrom, dateTo time.Time, productId *int, sizeId *int, source string, limit, offset int, sortByDirection entity.StockAdjustmentDirection, orderFactor entity.OrderFactor) ([]entity.StockChangeRow, int, error)
	}
	Hero interface {
		RefreshHero(ctx context.Context) error
		SetHero(ctx context.Context, hfi entity.HeroFullInsert) error
		GetHero(ctx context.Context) (*entity.HeroFullWithTranslations, error)
	}

	Mail interface {
		AddMail(ctx context.Context, ser *entity.SendEmailRequest) (int, error)
		// GetAllUnsent returns unsent rows. withError false limits to worker-eligible rows (attempts and next_retry_at).
		// Rows whose to_email is in email_suppression are always excluded.
		GetAllUnsent(ctx context.Context, withError bool, maxSendAttempts int, nowUTC time.Time) ([]entity.SendEmailRequest, error)
		UpdateSent(ctx context.Context, id int) error
		// ClearNextRetryAt clears next_retry_at on an unsent row (e.g. after inline send failed) so the worker can retry.
		ClearNextRetryAt(ctx context.Context, id int) error
		ScheduleSendRetry(ctx context.Context, id int, errMsg string, nextRetryAt time.Time) error
		MarkSendDead(ctx context.Context, id int, errMsg string, maxSendAttempts int) error
		// AddSuppression adds an email address to the suppression list. Idempotent.
		AddSuppression(ctx context.Context, email string, reason entity.SuppressionReason) error
		// IsSuppressed returns true if the address is on the suppression list.
		IsSuppressed(ctx context.Context, email string) (bool, error)
		// IncrementEmailMetric atomically increments a counter in email_daily_metrics for the given date.
		// metricType must be one of: "sent", "delivered", "bounced", "opened", "clicked".
		IncrementEmailMetric(ctx context.Context, metricType string, date time.Time) error
		// GetEmailMetrics returns daily email metric rows for a date range (inclusive).
		GetEmailMetrics(ctx context.Context, from, to time.Time) ([]entity.EmailDailyMetrics, error)
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
		UpdateSettledBaseAndFee(ctx context.Context, orderUUID string, settledBase, paymentFee decimal.Decimal) error
		UpdatePaymentStripeDetails(ctx context.Context, orderUUID string, d entity.StripePaymentDetails) error
		SetTrackingNumber(ctx context.Context, orderUUID string, trackingCode string) (*entity.OrderBuyerShipment, error)
		SetShipmentActualCost(ctx context.Context, orderUUID string, actualCost, returnShippingCost decimal.NullDecimal) error
		// SetShipmentLabel persists the carrier-generated shipping-label fields (Sendcloud)
		// on an order's shipment; the tracking code and Shipped transition are written by
		// SetTrackingNumber. GetOrderParcelItems returns each line's packaging weight/box (joined
		// from the product's primary tech card) to pre-fill the label parcel with a manual override.
		SetShipmentLabel(ctx context.Context, orderUUID string, label entity.ShipmentLabel) error
		GetOrderParcelItems(ctx context.Context, orderID int) ([]entity.OrderItemParcel, error)
		// VoidShipmentLabel clears a generated label + tracking and reverts Shipped->Confirmed so
		// the order can be re-shipped (the carrier-side cancel is done by the caller first).
		VoidShipmentLabel(ctx context.Context, orderUUID string) error
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
		RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason, reasonCode string, refundShipping bool) error
		DeliveredOrder(ctx context.Context, orderUUID string) error
		// DeliverOrderWithSource marks an order delivered attributed to changedBy/notes and
		// reports whether this call performed the transition (used by the delivery-sync worker
		// and AfterShip webhook to send the delivered email at most once).
		DeliverOrderWithSource(ctx context.Context, orderUUID, changedBy, notes string) (bool, error)
		CancelOrder(ctx context.Context, orderUUID string) error
		GetStuckPlacedOrders(ctx context.Context, olderThan time.Time) ([]entity.Order, error)
		GetExpiredAwaitingPaymentOrders(ctx context.Context, now time.Time) ([]entity.Order, error)
		// GetShippedOrdersForDeliverySync returns Shipped orders with a shipping_date for the
		// delivery-sync worker (AfterShip poll + timer safety net).
		GetShippedOrdersForDeliverySync(ctx context.Context) ([]entity.ShipmentToAutoDeliver, error)
		// GetOrderUUIDByTrackingCode resolves an AfterShip delivery event to an order UUID.
		GetOrderUUIDByTrackingCode(ctx context.Context, trackingCode string) (string, error)
		CancelOrderByUser(ctx context.Context, orderUUID string, email string, reason string) (*entity.OrderFull, error)
		SetOrderStatusToPendingReturn(ctx context.Context, orderUUID string, changedBy string) error
		AddOrderComment(ctx context.Context, orderUUID string, comment string) error
		// Reviews (internal statistics)
		AddOrderReview(ctx context.Context, orderUUID string, email string, orderReview *entity.OrderReviewInsert, itemReviews []entity.OrderItemReviewInsert) error
		GetOrderReviewsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.OrderReviewFull, int, error)
		DeleteOrderReview(ctx context.Context, orderId int) error
		GetProductReviewsPaged(ctx context.Context, productId int, limit, offset int, orderFactor entity.OrderFactor) ([]entity.OrderItemReview, int, error)
		GetOrderReviewByUUID(ctx context.Context, orderUUID string) (*entity.OrderReviewFull, error)
		// ListOrdersFullByBuyerEmailPaged returns orders where buyer email matches, newest first, with total count.
		ListOrdersFullByBuyerEmailPaged(ctx context.Context, email string, limit, offset int) ([]entity.OrderFull, int, error)
	}

	// StorefrontAccount handles customer account login, sessions, and saved addresses.
	StorefrontAccount interface {
		InsertLoginChallenge(ctx context.Context, email, otpHash, magicHash string, expiresAt time.Time) error
		ConsumeLoginChallengeOTP(ctx context.Context, email, otpPlain, otpPepper string) (string, error)
		ConsumeLoginChallengeMagic(ctx context.Context, magicPlain, magicPepper string) (string, error)
		GetOrCreateAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error)
		GetAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error)
		UpdateAccountProfile(ctx context.Context, email string, firstName, lastName string, birthDate sql.NullTime, shoppingPreference entity.StorefrontShoppingPreference, phone sql.NullString, subscribeNewsletter, subscribeNewArrivals, subscribeEvents bool, defaultCountry, defaultLanguage sql.NullString) error
		InsertRefreshToken(ctx context.Context, accountID int, tokenHash, familyID string, expiresAt time.Time) (int64, error)
		// RotateRefreshToken validates the current refresh token, revokes it, inserts a new one in the same family, and returns the new raw token and account email.
		RotateRefreshToken(ctx context.Context, rawRefresh, refreshPepper string, refreshTTL time.Duration, now time.Time) (newRaw string, accountEmail string, err error)
		// RevokeRefreshTokenFamilyByRawTokenForAccount revokes every refresh token in the family identified by rawRefresh, scoped to accountID.
		RevokeRefreshTokenFamilyByRawTokenForAccount(ctx context.Context, rawRefresh, refreshPepper string, accountID int) error
		// RevokeAllRefreshTokensForAccount revokes all refresh tokens for the account (logout all devices).
		RevokeAllRefreshTokensForAccount(ctx context.Context, accountID int) error
		InsertJtiDenylist(ctx context.Context, jti string, accountID int, expiresAt time.Time) error
		IsJtiDenylisted(ctx context.Context, jti string) (bool, error)
		CleanupExpiredJtiDenylist(ctx context.Context) (int64, error)
		CleanupExpiredLoginChallenges(ctx context.Context) (int64, error)
		CleanupExpiredRefreshTokens(ctx context.Context) (int64, error)
		ListSavedAddresses(ctx context.Context, accountID int) ([]entity.StorefrontSavedAddress, error)
		AddSavedAddress(ctx context.Context, accountID int, ins *entity.StorefrontSavedAddressInsert) (int, error)
		UpdateSavedAddress(ctx context.Context, accountID int, id int, ins *entity.StorefrontSavedAddressInsert) error
		DeleteSavedAddress(ctx context.Context, accountID int, id int) error
		SetDefaultSavedAddress(ctx context.Context, accountID int, id int) error
	}

	// Membership handles loyalty tier state, qualifying-spend, tier config,
	// audit history, account lifecycle (soft-delete / erasure), and hacker invites.
	Membership interface {
		ComputeQualifyingSpendEUR(ctx context.Context, email string, windowStart time.Time) (decimal.Decimal, error)
		BackfillOrderEURSnapshots(ctx context.Context) (int64, error)
		CountQualifyingOrders(ctx context.Context, email string) (int, error)
		UpsertSpendCache(ctx context.Context, accountID int, amount decimal.Decimal, windowStart, windowEnd time.Time) error
		GetSpendCache(ctx context.Context, accountID int) (*entity.QualifyingSpend, error)
		ListTierConfig(ctx context.Context) ([]entity.TierConfig, error)
		GetTierConfig(ctx context.Context, code int16) (*entity.TierConfig, error)
		UpdateTierConfig(ctx context.Context, upd entity.TierConfigUpdate) error
		ListMembers(ctx context.Context, f entity.MemberListFilter) ([]entity.Member, int, error)
		GetMember(ctx context.Context, accountID int) (*entity.Member, error)
		ApplyTierTransition(ctx context.Context, t entity.TierTransition) error
		ListTierHistory(ctx context.Context, accountID int) ([]entity.TierHistoryEntry, error)
		ListAuditLog(ctx context.Context, f entity.TierAuditFilter) ([]entity.TierHistoryEntry, int, error)
		SetAccountStatus(ctx context.Context, accountID int, st entity.StorefrontAccountStatus) error
		SoftDeleteAccount(ctx context.Context, accountID int) error
		HardEraseAccount(ctx context.Context, accountID int) error
		ListAccountsForDowngradeReview(ctx context.Context, now time.Time) ([]entity.StorefrontAccount, error)
		ListAccountsForDowngradeReminder(ctx context.Context, now time.Time, reminderDays int) ([]entity.StorefrontAccount, error)
		ListAccountsWithBirthday(ctx context.Context, month, day int) ([]entity.StorefrontAccount, error)
		CreateHackerInvite(ctx context.Context, tokenHash string, email sql.NullString, createdBy string, expiresAt time.Time) (int64, error)
		ListHackerInvites(ctx context.Context, activeOnly bool, now time.Time) ([]entity.HackerInvite, error)
		ConsumeHackerInvite(ctx context.Context, tokenHash string, accountID int, now time.Time) (*entity.HackerInvite, error)
		RevokeHackerInvite(ctx context.Context, id int64) error
		ListHackerAccounts(ctx context.Context) ([]entity.Member, error)
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
		// idempotencyKey must be derived deterministically from the refund scope so retries and
		// concurrent identical refunds dedupe at Stripe (see stripe.RefundIdempotencyKey).
		Refund(ctx context.Context, payment entity.Payment, orderUUID string, amount *decimal.Decimal, currency string, idempotencyKey string) error
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
		// UpsertInventoryTargets sets per-SKU reorder targets (insert or replace by product+size).
		UpsertInventoryTargets(ctx context.Context, targets []entity.InventoryTargetInsert) error
		// GetSellThroughByDrop rolls each drop cohort (product.collection) into lifetime
		// sell-through totals. from/to are accepted for interface consistency but not applied.
		GetSellThroughByDrop(ctx context.Context, from, to time.Time, limit int) ([]entity.SellThroughByDropRow, error)
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
		// GetDashboard returns the small, DB-trusted decision payload (headline + alerts +
		// action lists) without building the full BusinessMetrics god-object.
		GetDashboard(ctx context.Context, from, to time.Time, limit int) (*entity.Dashboard, error)
		// GetDashboardHeadline returns only the six headline decision figures for a window (no
		// action lists / alerts), using the same arithmetic as GetDashboard. Used to compute the
		// dashboard's period-over-period comparison cheaply.
		GetDashboardHeadline(ctx context.Context, from, to time.Time) (*entity.DashboardHeadline, error)
		// GetAlertThresholds / UpsertAlertThresholds read and write the operator-tunable
		// thresholds behind the dashboard alerts (alert_setting table).
		GetAlertThresholds(ctx context.Context) (entity.AlertThresholds, error)
		UpsertAlertThresholds(ctx context.Context, t entity.AlertThresholds) error
		// UpsertOpexEntries writes the fixed-cost (OPEX) journal used by the dashboard
		// operating result (opex_entry table), upserting on (month, category). NF-08: it also
		// mirrors each aggregate into opex_line as an '(aggregate)' base-currency line.
		UpsertOpexEntries(ctx context.Context, rows []entity.OpexEntry) error
		// UpsertOpexLines writes OPEX line items (opex_line, NF-08), upserting on
		// (month, category, label). AmountBase is folded to base currency by the caller.
		UpsertOpexLines(ctx context.Context, rows []entity.OpexLineInsert) error
		// DeleteOpexLine removes one OPEX line by id.
		DeleteOpexLine(ctx context.Context, id int) error
		// ListOpexLines returns OPEX lines within the (inclusive) month bounds, optional category.
		ListOpexLines(ctx context.Context, f entity.OpexLineFilter) ([]entity.OpexLine, error)
		// UpsertOpexRecurring inserts (id==0) or updates a recurring OPEX template, returning its id.
		UpsertOpexRecurring(ctx context.Context, ins entity.OpexRecurringInsert, id int) (int, error)
		// ArchiveOpexRecurring stops a template from materialising further months.
		ArchiveOpexRecurring(ctx context.Context, id int) error
		// ListOpexRecurring returns recurring templates (active-only unless includeArchived).
		ListOpexRecurring(ctx context.Context, includeArchived bool) ([]entity.OpexRecurring, error)
		// MaterializeOpexRecurring books each active template into monthly opex_lines up to `upTo`,
		// folding each month at its own effective FX rate (loaded internally). Dedup is
		// (recurring_id, month); already-costed months are frozen, uncosted ones are recosted on a
		// later tick. Returns lines newly created (recosts excluded). Fails if FX history won't load.
		MaterializeOpexRecurring(ctx context.Context, upTo time.Time) (int, error)
		// UpsertEmployee inserts (id==0) or updates an employee-registry row, returning its id (gap-07
		// v2 A). The registry links salary OpexRecurring templates to a person via employee_id.
		UpsertEmployee(ctx context.Context, ins entity.EmployeeInsert, id int) (int, error)
		// ArchiveEmployee soft-archives an employee; linked recurring templates keep their employee_id.
		ArchiveEmployee(ctx context.Context, id int) error
		// ListEmployees returns registry rows (active-only unless includeArchived).
		ListEmployees(ctx context.Context, includeArchived bool) ([]entity.Employee, error)
		// ListVatRates / UpsertVatRates read and write the destination-country VAT rates
		// (vat_rate table) used to compute net-of-VAT revenue.
		ListVatRates(ctx context.Context) ([]entity.VatRate, error)
		UpsertVatRates(ctx context.Context, rates []entity.VatRate) error
		// GetEmailMetricsSummary aggregates email delivery counters for a date range and computes rates.
		GetEmailMetricsSummary(ctx context.Context, from, to time.Time) (*entity.EmailMetricsSummary, error)
		// GetPeriodOrderCount returns the number of placed orders (valid statuses) in [from, to).
		GetPeriodOrderCount(ctx context.Context, from, to time.Time) (int, error)
		// GetRevenueByCountry returns revenue breakdown by country with share % and AOV.
		GetRevenueByCountry(ctx context.Context, from, to time.Time) ([]entity.GeographyMetric, error)
		// GetCountryEconomics returns per-country profitability (margin, contribution, profit/order, LTV).
		GetCountryEconomics(ctx context.Context, from, to time.Time) ([]entity.CountryEconomicsRow, error)
		// GetCountryLogistics returns per-country fulfilment durations, on-time rate, shipping cost, returns.
		GetCountryLogistics(ctx context.Context, from, to time.Time) ([]entity.CountryLogisticsRow, error)
		// GetCountryDemand returns the DB side of per-country demand (orders, AOV, new/returning, top cats).
		GetCountryDemand(ctx context.Context, from, to time.Time) ([]entity.CountryDemandRow, error)
		// GetCustomerSegmentation returns AOV-based customer segmentation (high/medium/low tiers).
		GetCustomerSegmentation(ctx context.Context, from, to time.Time) ([]entity.CustomerSegmentRow, error)
		// GetOrderValueBands buckets net-revenue orders into fixed order-value bands (upsell view).
		GetOrderValueBands(ctx context.Context, from, to time.Time) ([]entity.OrderValueBandRow, error)
		// GetDeliveryMetrics reports fulfilment durations + on-time rate for orders placed in the period.
		GetDeliveryMetrics(ctx context.Context, from, to time.Time) (entity.DeliverySection, error)
		// GetRevenueForecast projects net revenue for the calendar month containing asOf (DB-only).
		GetRevenueForecast(ctx context.Context, asOf time.Time) (entity.RevenueForecast, error)
		// GetProfitability assembles the profitability tab (margin, CPO/CAC/LTV·CAC, opex roll-up).
		GetProfitability(ctx context.Context, period, comparePeriod entity.TimeRange) (entity.ProfitabilitySection, error)
		// GetRFMAnalysis returns RFM (Recency, Frequency, Monetary) customer segmentation.
		GetRFMAnalysis(ctx context.Context, from, to time.Time) ([]entity.RFMSegmentRow, error)
		// GetMarginByStyle rolls the per-SKU margin breakdown up to the style (tech card) via
		// product.primary_tech_card_id; products with no primary card fall into a "no style" row.
		GetMarginByStyle(ctx context.Context, from, to time.Time, limit int) ([]entity.MarginByStyleRow, error)
		// GetStyleMargin returns the lifetime sales margin for one style (all its colourway SKUs) as a
		// single MarginByStyleRow, or nil when the style has no sales. Sales anchor of GetStyleEconomics.
		GetStyleMargin(ctx context.Context, techCardID int) (*entity.MarginByStyleRow, error)
		// GetStyleSampleSummary returns a style's sample count and the warehouse-material cost they
		// consumed (informational, not folded into the sales net) — NF-09 style economics.
		GetStyleSampleSummary(ctx context.Context, techCardID int) (entity.StyleSampleSummary, error)
		// GetStyleMaterialsFromStock returns the net warehouse-material cost issued into a style's
		// production runs (base EUR) — the material actuals for the production summary (NF-09).
		GetStyleMaterialsFromStock(ctx context.Context, techCardID int) (entity.StyleMaterialsFromStock, error)
		// GetChannelRoasSettled attributes settled order revenue to marketing channels via the
		// bq_order_channel map (order.ga_client_id → last non-direct UTM), returning per-channel settled
		// revenue, order count and new-customer count over the period (task 20 step 2). Spend/ROAS/CAC
		// are layered on by the caller from channel_spend.
		GetChannelRoasSettled(ctx context.Context, from, to time.Time) ([]entity.ChannelSettledRow, error)
		// GetCogsStructure decomposes the cost of goods sold in the period into its components
		// (materials / cmt / … / unattributed) from each product's cost_breakdown snapshot.
		GetCogsStructure(ctx context.Context, from, to time.Time) ([]entity.CogsStructureRow, error)
		// GetInventoryValuation is the money view of the warehouse: cost frozen in stock, dead
		// stock (unsold in the window), and damage/loss write-offs in the period, valued at the
		// current plan cost_price with uncosted stock counted honestly.
		GetInventoryValuation(ctx context.Context, from, to time.Time, limit int) (*entity.InventoryValuation, error)
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
		GetArchiveByCode(ctx context.Context, code string) (*entity.ArchiveFull, error)
		GetArchiveTranslations(ctx context.Context, id int) ([]entity.ArchiveTranslation, error)
	}

	// Models manages fit/fashion model profiles and their body measurements.
	Models interface {
		AddModel(ctx context.Context, m *entity.ModelInsert) (int, error)
		UpdateModel(ctx context.Context, id int, m *entity.ModelInsert) error
		DeleteModel(ctx context.Context, id int) error
		GetModelById(ctx context.Context, id int) (*entity.Model, error)
		ListModels(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, gender, nameSearch string) ([]entity.Model, int, error)
	}

	// Fittings manages garment try-on sessions with their sizes and media.
	Fittings interface {
		AddFitting(ctx context.Context, f *entity.FittingInsert) (int, error)
		UpdateFitting(ctx context.Context, id int, f *entity.FittingInsert) error
		DeleteFitting(ctx context.Context, id int) error
		GetFittingById(ctx context.Context, id int) (*entity.Fitting, error)
		ListFittings(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, productID, modelID, techCardID int) ([]entity.Fitting, int, error)
	}

	// Tasks manages the internal team kanban (task manager): cards with content,
	// board/status/position placement, labels, media and comments.
	Tasks interface {
		AddTask(ctx context.Context, t *entity.Task) (int, error)
		GetTaskById(ctx context.Context, id int) (*entity.Task, error)
		UpdateTask(ctx context.Context, id int, t *entity.TaskInsert) error
		MoveTask(ctx context.Context, id int, board entity.TaskBoard, status entity.TaskStatus, position int) error
		DeleteTask(ctx context.Context, id int) error
		ListTasks(ctx context.Context, f entity.TaskListFilter) ([]entity.Task, int, error)
		AddTaskComment(ctx context.Context, c *entity.TaskCommentInsert, author string) (int, error)
		ListTaskComments(ctx context.Context, taskID int) ([]entity.TaskComment, error)
		ArchiveTask(ctx context.Context, id int) error
		UnarchiveTask(ctx context.Context, id int) error
		AddTaskChecklistItem(ctx context.Context, taskID int, content string) (int, error)
		SetTaskChecklistItemDone(ctx context.Context, id int, done bool) error
		DeleteTaskChecklistItem(ctx context.Context, id int) error
	}

	// Fulfillment is the orders-fulfillment board's storage: the board-owned
	// annotation (assignee/notes/checklist) overlaid on orders. Order STATUS
	// transitions (ship/deliver) are NOT here — they go through Order so the board
	// never duplicates order status.
	Fulfillment interface {
		// GetFulfillmentBoard returns the three columns (to_fulfill/shipped/
		// delivered) as a projection of orders, with each card's annotation
		// summary. deliveredLimit caps the (historical) delivered column.
		GetFulfillmentBoard(ctx context.Context, deliveredLimit int) (*entity.FulfillmentBoard, error)
		// GetOrderFulfillment returns an order's annotation (assignee/notes/
		// checklist), or (nil, nil) when the order has none yet.
		GetOrderFulfillment(ctx context.Context, orderUUID string) (*entity.OrderFulfillment, error)
		SetFulfillmentAssignee(ctx context.Context, orderUUID, assignee, createdBy string) error
		SetFulfillmentNotes(ctx context.Context, orderUUID, notes, createdBy string) error
		AddFulfillmentChecklistItem(ctx context.Context, orderUUID, content, createdBy string) (int, error)
		SetFulfillmentChecklistItemDone(ctx context.Context, id int, done bool) error
		DeleteFulfillmentChecklistItem(ctx context.Context, id int) error
	}

	// TechCards manages garment tech packs (техкарта): the header, size range,
	// linked products, sketch media, callouts and revision log.
	TechCards interface {
		AddTechCard(ctx context.Context, tc *entity.TechCardInsert) (int, error)
		UpdateTechCard(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) error
		DeleteTechCard(ctx context.Context, id int) error
		GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error)
		ListTechCards(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, filter entity.TechCardListFilter) ([]entity.TechCard, int, error)
		// GetStylePipeline returns the development board: one column per lifecycle stage with its count
		// and up to cardsPerStage light preview cards (gap-01).
		GetStylePipeline(ctx context.Context, cardsPerStage int) ([]entity.StylePipelineColumn, error)
		// GetStyleSizeChart returns a style's full size chart + the shared tech_card.lock_version (R5).
		// sql.ErrNoRows when the style is absent.
		GetStyleSizeChart(ctx context.Context, styleID int) (entity.StyleSizeChart, error)
		// UpdateStyleSizeChart replaces a style's ENTIRE size chart in one versioned request (R5,
		// full-replace) under the shared optimistic lock; entity.ErrTechCardConflict on a stale version.
		UpdateStyleSizeChart(ctx context.Context, styleID, expectedLockVersion int, cells []entity.StyleSizeChartCell) (entity.StyleSizeChart, error)
		// GetCostingFxRatesToBase returns the effective manual FX rate per currency (UPPERCASE
		// ISO → base-currency units per 1 unit), used to fold multi-currency costing into base.
		GetCostingFxRatesToBase(ctx context.Context) (map[string]decimal.Decimal, error)
		// ListCostingFxRates returns every stored rate (all effective dates) for admin display.
		ListCostingFxRates(ctx context.Context) ([]entity.CostingFxRate, error)
		// UpsertCostingFxRates inserts/updates rates by (currency, valid_from). Empty is a no-op.
		UpsertCostingFxRates(ctx context.Context, rates []entity.CostingFxRate) error
		// Material catalog (task 10): shared nomenclature a BOM line can optionally link to,
		// with an append-only price history.
		CreateMaterial(ctx context.Context, m *entity.MaterialInsert) (int, error)
		UpdateMaterial(ctx context.Context, id int, m *entity.MaterialInsert, expectedLockVersion int) error
		ArchiveMaterial(ctx context.Context, id int, archived bool) error
		GetMaterial(ctx context.Context, id int) (*entity.MaterialWithPrice, error)
		ListMaterials(ctx context.Context, section string, includeArchived bool) ([]entity.MaterialWithPrice, error)
		AddMaterialPrice(ctx context.Context, p entity.MaterialPrice) error
		ListMaterialPrices(ctx context.Context, materialID int) ([]entity.MaterialPrice, error)
		// Immutable release snapshots (task 11): a full JSON snapshot of the enriched read-model
		// frozen at each release, so a card's prior spec + planned cost survive re-open/re-release.
		SaveTechCardRelease(ctx context.Context, rel entity.TechCardRelease) error
		ListTechCardReleases(ctx context.Context, techCardID int) ([]entity.TechCardReleaseMeta, error)
		GetTechCardRelease(ctx context.Context, id int) (*entity.TechCardRelease, error)
		// Development (R&D) cost journal (task 14): append + delete + list rows at the tech-card
		// level (NOT full-replace); a period cost, never seeded into product.cost_price.
		AddTechCardDevExpense(ctx context.Context, e entity.TechCardDevExpense) (entity.TechCardDevExpense, error)
		DeleteTechCardDevExpense(ctx context.Context, id int) error
		ListTechCardDevExpenses(ctx context.Context, techCardID int) ([]entity.TechCardDevExpense, error)
	}

	// ProductionRuns is the production-run (партия) repository: the run header + per-size
	// planned/received/defect grid, with the planned unit cost snapshotted at plan time.
	ProductionRuns interface {
		CreateProductionRun(ctx context.Context, r *entity.ProductionRunInsert) (int, error)
		UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion int) error
		DeleteProductionRun(ctx context.Context, id int) error
		GetProductionRun(ctx context.Context, id int) (*entity.ProductionRun, error)
		ListProductionRuns(ctx context.Context, limit, offset int, filter entity.ProductionRunListFilter) ([]entity.ProductionRun, int, error)
		// ReceiveProductionRun receives a multi-colourway run into stock (NF-06): perProduct maps each
		// product_id → (size_id → qty), pre-validated by the caller against the run's tech card. Inside
		// one transaction it locks the run, re-reads its lines to confirm perProduct is still current
		// (else ErrProductionRunConcurrentModification), increments every product's stock, and — when
		// updateCostPrice is set — seeds each product's cost_price from the run's actual unit cost
		// recomputed from the freshly-read costs/movements (so a material issue racing the receive is
		// not missed). It transitions the run to received, guarded against a double receipt, and reports
		// whether cost_price was seeded.
		ReceiveProductionRun(ctx context.Context, runID int, perProduct map[int]map[int]int, updateCostPrice bool, username string) (bool, error)
		// ReceiveAuxiliaryProductionRun receives an auxiliary run's output into the material warehouse
		// (NF-07): under the run lock it re-reads the lines (product-free, Σ received_qty > 0 — else
		// ErrProductionRunConcurrentModification / ErrProductionRunNothingReceived) and recomputes the
		// actual per-unit base cost from the freshly-read costs+movements (g25-07), books a
		// receipt_production of that quantity into outputMaterialID (moving the average when costed)
		// and transitions the run to received — guarded against a double receipt.
		ReceiveAuxiliaryProductionRun(ctx context.Context, runID, outputMaterialID int, username string) error
	}

	// Samples is the sample (сэмпл) repository (new-flow NF-04): a sewn prototype of a style, with
	// a cost composed on read from material issues + the dev-expense journal.
	Samples interface {
		AddSample(ctx context.Context, sm *entity.SampleInsert) (int, error)
		UpdateSample(ctx context.Context, id int, sm *entity.SampleInsert) error
		DeleteSample(ctx context.Context, id int) error
		GetSampleById(ctx context.Context, id int) (*entity.Sample, error)
		// ListSamples lists samples; techCardID <= 0 spans all styles (cross-style queue), and
		// status/purpose are optional string filters ("" = any).
		ListSamples(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, techCardID int, status, purpose string) ([]entity.Sample, int, error)
	}

	// MaterialStock is the material warehouse (new-flow NF-01): the maintained on-hand balance +
	// moving-average unit cost per catalog material, and the append-only movement ledger. Distinct
	// from Inventory (which is the finished-goods valuation metrics of task 16).
	MaterialStock interface {
		ReceiveMaterialStock(ctx context.Context, ins entity.MaterialReceiptInsert) (entity.MaterialMovement, error)
		IssueMaterialStock(ctx context.Context, ins entity.MaterialIssueInsert) (entity.MaterialMovement, error)
		AdjustMaterialStock(ctx context.Context, ins entity.MaterialAdjustInsert) (entity.MaterialMovement, error)
		GetMaterialStock(ctx context.Context, materialID int) (*entity.MaterialStock, error)
		ListMaterialStock(ctx context.Context, filter entity.MaterialStockFilter) ([]entity.MaterialStockRow, error)
		ListMaterialMovements(ctx context.Context, limit, offset int, filter entity.MaterialMovementFilter) ([]entity.MaterialMovement, int, error)
		// UpsertPackagingBom full-replaces the global packaging recipe consumed on ship (gap-07 v2 B).
		UpsertPackagingBom(ctx context.Context, items []entity.PackagingBomItem) error
		// ListPackagingBom returns the packaging recipe joined with material name/unit.
		ListPackagingBom(ctx context.Context) ([]entity.PackagingBomItem, error)
		// ConsumePackagingForOrder writes off packaging for a shipped order and closes its reservation
		// claims, idempotently (PK guard) and best-effort (a short material is skipped, never failing the
		// ship). itemCount is unused (per-item quantities come from the order's lines).
		ConsumePackagingForOrder(ctx context.Context, orderID, itemCount int, username string) ([]entity.MaterialMovement, error)
		// ReservePackagingForOrder soft-reserves an order's packaging at placement (S22): resolves the
		// per-material requirement (product→style→global) and appends idempotent 'reserve' claims. Never
		// blocks — an oversell is surfaced via available, not refused.
		ReservePackagingForOrder(ctx context.Context, orderID int, username string) error
		// ReleasePackagingForOrder closes an order's open packaging claims with 'release' rows (cancel/
		// refund) — the soft hold is returned without any physical writeoff. Idempotent.
		ReleasePackagingForOrder(ctx context.Context, orderID int, username string) error
		// MaterialAvailable returns a material's on_hand, open-reserved and available (on_hand − reserved).
		MaterialAvailable(ctx context.Context, materialID int) (entity.MaterialAvailability, error)
		// ListPackagingRecipe returns every packaging recipe (all scopes) joined with material name/unit.
		ListPackagingRecipe(ctx context.Context) ([]entity.PackagingRecipe, error)
		// UpsertPackagingRecipe full-replaces one scope target's recipe lines (the whole global set, or
		// one style's set, or one product's set).
		UpsertPackagingRecipe(ctx context.Context, scope entity.PackagingRecipeScope, techCardID, productID sql.NullInt32, items []entity.PackagingRecipeInsert, username string) error
		// ListMaterialLots returns a material's structured lots / rolls (gap-07 v2 D), active-only unless
		// includeArchived. Traceability registry; valuation stays moving-average.
		ListMaterialLots(ctx context.Context, materialID int, includeArchived bool) ([]entity.MaterialLot, error)
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
		// GetOrderChannelMap maps each GA4 client_id to its last non-direct UTM channel, for
		// server-side settled-revenue attribution (task 20 step 2).
		GetOrderChannelMap(ctx context.Context, startDate, endDate time.Time) ([]entity.OrderChannelRow, error)
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
		SumBQHeroFunnel(ctx context.Context, from, to time.Time) (*entity.HeroFunnelAggregate, error)
		GetBQSizeConfidence(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeConfidenceMetric, error)
		GetBQPaymentRecovery(ctx context.Context, from, to time.Time) ([]entity.PaymentRecoveryMetric, error)
		GetBQCheckoutTimings(ctx context.Context, from, to time.Time) ([]entity.CheckoutTimingMetric, error)
		GetBQAddToCartRate(ctx context.Context, from, to time.Time, granularity entity.TrendGranularity, limit, offset int) (*entity.AddToCartRateAnalysis, error)
		GetBQBrowserBreakdown(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.BrowserBreakdownRow, error)
		GetBQNewsletter(ctx context.Context, from, to time.Time) ([]entity.NewsletterMetricRow, error)
		GetBQAbandonedCart(ctx context.Context, from, to time.Time) ([]entity.AbandonedCartRow, error)
		GetBQCampaignAttribution(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.CampaignAttributionRow, error)
		GetBQCampaignAttributionBySourceMedium(ctx context.Context, from, to time.Time) ([]entity.CampaignAttributionAggregated, error)
		GetBQCampaignAttributionAggregated(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.CampaignAttributionAggregatedFull, error)
		GetBQTimeOnPage(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.TimeOnPageRow, error)
		GetBQProductZoom(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ProductZoomRow, error)
		GetBQImageSwipes(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.ImageSwipeRow, error)
		GetBQSizeGuideClicks(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.SizeGuideClickRow, error)
		GetBQDetailsExpansion(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.DetailsExpansionRow, error)
		GetBQNotifyMeIntent(ctx context.Context, from, to time.Time, limit, offset int) ([]entity.NotifyMeIntentRow, error)
		// GetChannelSpendByCampaign returns operator-entered marketing spend aggregated by
		// channel over [from, to] in base currency, for computing ROAS.
		GetChannelSpendByCampaign(ctx context.Context, from, to time.Time) ([]entity.ChannelSpendRow, error)
	}

	// BQCacheStoreWriter handles BigQuery precomputed analytics cache writes
	BQCacheStoreWriter interface {
		// UpsertChannelSpend records operator-entered marketing spend per channel per day.
		UpsertChannelSpend(ctx context.Context, rows []entity.ChannelSpendInsert) error
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
		// SaveBQOrderChannel upserts the client_id→channel attribution map (task 20 step 2), keyed on
		// client_id so a client's latest non-direct touch replaces the prior one.
		SaveBQOrderChannel(ctx context.Context, rows []entity.OrderChannelRow) error
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
		// InvalidateBQAnalyticsSyncStatus sets success=false for all sync_type values prefixed with bq_
		// so metrics freshness treats BQ cache as stale until the next successful worker run.
		InvalidateBQAnalyticsSyncStatus(ctx context.Context, reason string) (rowsAffected int64, err error)
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
		GetMediaByIds(ctx context.Context, ids []int) (map[int]entity.MediaFull, error)
		DeleteMediaById(ctx context.Context, id int) error
		ListMediaPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.MediaFull, error)
	}

	Admin interface {
		// AddAccount creates an account with an initial permission set; isSuper
		// grants full access (permissions are then ignored).
		AddAccount(ctx context.Context, username, pwHash string, isSuper bool, perms []entity.AdminPermission) error
		// SetAccountPermissions replaces an account's super flag and permission set.
		SetAccountPermissions(ctx context.Context, username string, isSuper bool, perms []entity.AdminPermission) error
		// SetAccountDisabled toggles whether an account may log in (get new tokens).
		SetAccountDisabled(ctx context.Context, username string, disabled bool) error
		DeleteAdmin(ctx context.Context, username string) error
		ChangePassword(ctx context.Context, un, newHash string) error
		PasswordHashByUsername(ctx context.Context, un string) (string, error)
		GetAdminByUsername(ctx context.Context, username string) (*entity.Admin, error)
		// GetAccountWithPermissions returns an account with its resolved permissions.
		GetAccountWithPermissions(ctx context.Context, username string) (*entity.AdminAccount, error)
		// ListAccounts returns every account with its permissions.
		ListAccounts(ctx context.Context) ([]entity.AdminAccount, error)
		// CountSuperAdmins returns the number of enabled super-admin accounts.
		CountSuperAdmins(ctx context.Context) (int, error)
	}

	Settings interface {
		AddShipmentCarrier(ctx context.Context, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) (int, error)
		UpdateShipmentCarrier(ctx context.Context, id int, carrier *entity.ShipmentCarrierInsert, prices map[string]decimal.Decimal, allowedRegions []string) error
		DeleteShipmentCarrier(ctx context.Context, id int) error
		SetShipmentCarrierAllowance(ctx context.Context, carrier string, allowance bool) error
		SetShipmentCarrierPrices(ctx context.Context, carrier string, prices map[string]decimal.Decimal) error
		SetPaymentMethodAllowance(ctx context.Context, paymentMethod entity.PaymentMethodName, allowance bool) error
		// SetPaymentMethodFees sets a method's estimated processing-fee model (percent + fixed).
		SetPaymentMethodFees(ctx context.Context, paymentMethod entity.PaymentMethodName, feePct, feeFixed decimal.Decimal) error
		SetPaymentIsProd(ctx context.Context, isProd bool) error
		SetSiteAvailability(ctx context.Context, allowance bool) error
		SetMaxOrderItems(ctx context.Context, count int) error
		SetBigMenu(ctx context.Context, bigMenu bool) error
		SetAnnounce(ctx context.Context, link string, translations []entity.AnnounceTranslation) error
		GetAnnounce(ctx context.Context) (*entity.AnnounceWithTranslations, error)
		SetOrderExpirationSeconds(ctx context.Context, seconds int) error
		SetComplimentaryShippingPrices(ctx context.Context, prices map[string]decimal.Decimal) error
		GetComplimentaryShippingPrices(ctx context.Context) (map[string]decimal.Decimal, error)
		GetBackgroundHeroColor(ctx context.Context) (string, error)
		SetBackgroundHeroColor(ctx context.Context, color string) error
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
		StorefrontAccount() StorefrontAccount
		Membership() Membership
		Promo() Promo
		Models() Models
		Fittings() Fittings
		Tasks() Tasks
		Fulfillment() Fulfillment
		TechCards() TechCards
		ProductionRuns() ProductionRuns
		MaterialStock() MaterialStock
		Samples() Samples
		Admin() Admin
		Cache() Cache
		Dictionary() Dictionary
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
		IsErrForeignKeyViolation(err error) bool
		IsErrorRepeat(err error) bool
		DB() DB
	}

	Cache interface {
		GetDictionaryInfo(ctx context.Context) (*entity.DictionaryInfo, error)
	}

	// Dictionary is the write/manage surface for the controlled merch dictionaries (R9). Every mutation
	// carries an optimistic expected_version (0 opts out) and returns the namespace's new
	// dictionary_revision, bumped in the same transaction as the change.
	Dictionary interface {
		GetDictionaryRevisions(ctx context.Context) (map[entity.DictionaryNamespace]int64, error)

		ListColors(ctx context.Context, includeArchived bool) ([]entity.Color, error)
		CreateColor(ctx context.Context, code, name, hex string, expectedVersion int64) (entity.Color, int64, error)
		UpdateColor(ctx context.Context, code, name, hex string, expectedVersion int64) (int64, error)
		ArchiveColor(ctx context.Context, code string, expectedVersion int64) (int64, error)

		ListCollections(ctx context.Context, includeArchived bool) ([]entity.CollectionDict, error)
		CreateCollection(ctx context.Context, name string, expectedVersion int64) (entity.CollectionDict, int64, error)
		UpdateCollection(ctx context.Context, id int, name string, expectedVersion int64) (int64, error)
		ArchiveCollection(ctx context.Context, id int, expectedVersion int64) (int64, error)

		ListTags(ctx context.Context, includeArchived bool) ([]entity.TagDict, error)
		CreateTag(ctx context.Context, name string, expectedVersion int64) (entity.TagDict, int64, error)
		UpdateTag(ctx context.Context, id int, name string, expectedVersion int64) (int64, error)
		ArchiveTag(ctx context.Context, id int, expectedVersion int64) (int64, error)

		ListCountries(ctx context.Context, activeOnly bool) ([]entity.Country, error)
		SetCountryActive(ctx context.Context, code string, active bool, expectedVersion int64) (int64, error)
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
		// UploadPatternPDF uploads a raw PDF cut pattern (выкройка) and returns its url and
		// stored byte size. The file is kept out of the media library.
		UploadPatternPDF(ctx context.Context, raw []byte, objectName string) (string, int64, error)
		// UploadLabelPDF durably stores a carrier shipping-label PDF (whose provider URL expires)
		// and returns its CDN url and stored byte size. Kept out of the media library.
		UploadLabelPDF(ctx context.Context, raw []byte, objectName string) (string, int64, error)
		// GetBaseFolder returns the base folder for the bucket
		GetBaseFolder() string
		// DeleteObjects best-effort removes the S3 objects behind the given media URLs
		// (empty and duplicate URLs are ignored). Used so deleting a media row or a
		// partially-failed variant upload does not orphan public CDN objects.
		DeleteObjects(ctx context.Context, urls ...string) error
	}

	RevalidationService interface {
		RevalidateAll(ctx context.Context, revalidationData *dto.RevalidationData) error
	}

	// Tracker is an external shipment-tracking provider (AfterShip). RegisterTracking makes the
	// provider start monitoring a shipment (so it emits delivery webhooks); GetTrackingStatus
	// polls the current normalized status (the delivery-sync worker's reconcile path). Behind an
	// interface per the external-dependency convention; a disabled no-op impl is used when no API
	// key is configured.
	Tracker interface {
		RegisterTracking(ctx context.Context, slug, trackingNumber string) error
		GetTrackingStatus(ctx context.Context, slug, trackingNumber string) (entity.TrackingStatus, error)
	}

	// LabelProvider is an external shipping-label provider (Sendcloud). CreateLabel announces a
	// shipment and returns the carrier tracking number + the decoded label PDF bytes (Sendcloud
	// returns the label inline as base64). Behind an interface per the external-dependency
	// convention; a disabled no-op impl (methods return entity.ErrLabelsDisabled) is used when no
	// API keys are set, so callers fall back to manual tracking-number entry.
	LabelProvider interface {
		// Enabled reports whether the provider is configured (API keys present). When false the
		// UI hides label generation and operators enter tracking numbers manually.
		Enabled() bool
		CreateLabel(ctx context.Context, req entity.LabelRequest) (*entity.LabelResult, error)
		// GetShippingOptions fetches the shipping options (carrier + service + quote) available for a
		// parcel, so an operator can pick one before generating. Returns entity.ErrLabelsDisabled when disabled.
		GetShippingOptions(ctx context.Context, req entity.OptionsRequest) ([]entity.ShippingOption, error)
		// VoidLabel cancels a previously announced parcel with the carrier (by Sendcloud parcel id)
		// so it is no longer billable/usable. Returns entity.ErrLabelsDisabled when disabled.
		VoidLabel(ctx context.Context, carrierShipmentID string) error
		// SchedulePickup books a carrier pickup for the day (Sendcloud's end-of-day handover
		// equivalent; v3 has no generic manifest API). Returns entity.ErrLabelsDisabled when disabled.
		SchedulePickup(ctx context.Context, req entity.PickupRequest) (*entity.PickupResult, error)
	}

	Mailer interface {
		SendNewSubscriber(ctx context.Context, rep Repository, to string) error
		QueueNewSubscriber(ctx context.Context, rep Repository, to string) error
		SendOrderConfirmation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderConfirmed) error
		QueueOrderConfirmation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderConfirmed) error
		SendOrderCancellation(ctx context.Context, rep Repository, to string, orderDetails *dto.OrderCancelled) error
		SendOrderShipped(ctx context.Context, rep Repository, to string, shipmentDetails *dto.OrderShipment) error
		SendOrderDelivered(ctx context.Context, rep Repository, to string, deliveryDetails *dto.OrderDelivered) error
		SendRefundInitiated(ctx context.Context, rep Repository, to string, refundDetails *dto.OrderRefundInitiated) error
		SendPendingReturn(ctx context.Context, rep Repository, to string, details *dto.OrderPendingReturn) error
		SendPromoCode(ctx context.Context, rep Repository, to string, promoDetails *dto.PromoCodeDetails) error
		SendBackInStock(ctx context.Context, rep Repository, to string, productDetails *dto.BackInStock) error
		QueueAccountLogin(ctx context.Context, rep Repository, to string, otpCode string, magicLinkURL string) error
		QueueTierUpgrade(ctx context.Context, rep Repository, to string, data *dto.TierChangeEmail) error
		QueueTierDowngrade(ctx context.Context, rep Repository, to string, data *dto.TierChangeEmail) error
		QueueDowngradeReminder(ctx context.Context, rep Repository, to string, data *dto.TierChangeEmail) error
		QueueTierRollback(ctx context.Context, rep Repository, to string, data *dto.TierChangeEmail) error
		QueueFirstPurchaseThanks(ctx context.Context, rep Repository, to string, data *dto.TierChangeEmail) error
		QueueUnsubscribeConfirmation(ctx context.Context, rep Repository, to string, data *dto.UnsubscribeConfirmationEmail) error
		QueueBirthdayGift(ctx context.Context, rep Repository, to string, data *dto.BirthdayEmail) error
		QueueEventInvite(ctx context.Context, rep Repository, to string, data *dto.MemberCustomEmail) error
		QueueHackerInvite(ctx context.Context, rep Repository, to string, data *dto.HackerInviteEmail) error
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
