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
		CreateColorway(ctx context.Context, styleID int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert, dev *entity.ColorwayDevelopmentPatch) (int, error)
		// UpdateColorway patches a colourway's own fields under an optimistic guard on the shared
		// tech_card.lock_version (entity.ErrTechCardConflict on a stale value; sql.ErrNoRows when absent).
		// Never touches style facts, variants, stock or the chart. Returns the new shared lock_version.
		UpdateColorway(ctx context.Context, colorwayID, expectedVersion int, prd *entity.ColorwayInsert, mediaIDs []int, tags []entity.ColorwayTagInsert, prices []entity.ColorwayPriceInsert, dev *entity.ColorwayDevelopmentPatch) (int, error)
		// LabDipRoundsByStyleID returns the lab-dip round journal of every colourway of a style,
		// grouped by colourway id and oldest first (one query for the whole style).
		LabDipRoundsByStyleID(ctx context.Context, styleID int) (map[int][]entity.ColorwayLabDipRound, error)
		// UpdateStyle is the sole writer of a style's catalogue facts (R4/§14.7), optimistically locked on
		// the shared tech_card.lock_version. A SKU-fact (season) change re-mints unfrozen siblings, or is
		// refused (entity.ErrStyleFrozenSiblings) if any sibling is SKU-frozen. Returns the new lock_version.
		UpdateStyle(ctx context.Context, styleID, expectedLockVersion int, patch entity.StylePatch, fields []string) (int, error)
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
		// SeedProductCostFromTechCard atomically writes one colourway's price + breakdown only while
		// the source tech-card version and product provenance/ownership still match the observed read.
		SeedProductCostFromTechCard(ctx context.Context, productID, techCardID, techCardLockVersion int,
			cost decimal.Decimal, breakdown sql.NullString) (bool, error)
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
		// Returns the committed per-size transitions (locked before/after) so the API layer can
		// fire the stock-write contract's storefront side effects (ISR, waitlist notify).
		ReceiveProductionStock(ctx context.Context, productID int, perSize map[int]int, runID int, username, grade string) ([]entity.StockTransition, error)
		// SetProductCostPriceFromProductionRun writes cost (base) as the production-run-sourced
		// cost_price of a product, recording provenance (source + run id + timestamp) and clearing
		// the tech-card cost_breakdown. A manually set cost is never overwritten — the returned bool
		// is false for such a product (the receipt itself still succeeds).
		SetProductCostPriceFromProductionRun(ctx context.Context, productID, runID int, cost decimal.Decimal) (bool, error)
		// ReverseProductionStock is the receive mirror (Phase 6): it takes a reversed receipt's
		// good units back OUT of per-size stock on the caller's connection, journalling each
		// decrement with the production_reversed source. Never writes below zero — short variants
		// come back in the list (with ANY shortfall the caller aborts its transaction).
		ReverseProductionStock(ctx context.Context, productID int, perSize map[int]int, receiptID int, username, reason, grade string) ([]entity.ProductionRunReversalShortfallItem, error)
		// ClearProductCostPriceClaimOfRun rolls a product's cost_price back off a reversed run's
		// claim: to the tech-card estimate when one is computable, to NULL otherwise. Only a cost
		// the run actually claims is touched (returned bool false = superseded, left alone).
		ClearProductCostPriceClaimOfRun(ctx context.Context, productID, runID, techCardID int, est entity.ProductCostReseed) (bool, error)
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
		// GetProductsPaged returns a paged list of products based on provided parameters. statuses is the
		// ADMIN-only lifecycle-status filter (empty = ACTIVE-only default) and is honoured only on the admin
		// path (showHidden=true); the storefront path (showHidden=false) ignores it and returns ACTIVE-only
		// with tier gating. Tier gating is storefront-only and is never applied when showHidden=true.
		GetProductsPaged(ctx context.Context, limit int, offset int, sortFactors []entity.SortFactor, orderFactor entity.OrderFactor, filterConditions *entity.FilterConditions, statuses []entity.ColorwayStatus, showHidden bool) ([]entity.Colorway, int, error)
		// GetProductsByIds returns a list of products by their IDs.
		GetProductsByIds(ctx context.Context, ids []int) ([]entity.Colorway, error)
		// GetProductsByTag returns a list of products by their tag.
		GetProductsByTag(ctx context.Context, tag string) ([]entity.Colorway, error)
		// GetLowStockProducts returns visible products with total stock in (0, threshold], ordered by ascending stock.
		GetLowStockProducts(ctx context.Context, threshold int, limit int) ([]entity.Colorway, error)
		// GetProductByIdShowHidden returns a product by its ID no matter hidden they or not (admin read).
		// includeArchived additionally allows an ARCHIVED colourway to be returned read-only (admin detail);
		// when false it keeps excluding ARCHIVED. Must only ever be called with includeArchived=true from an
		// admin surface — the storefront uses GetProductByIdNoHidden/GetProductBySKU.
		GetProductByIdShowHidden(ctx context.Context, id int, includeArchived bool) (*entity.ColorwayFull, error)
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
		// ListVariantSeconds returns a colourway's B-grade (seconds) variants with their manual price
		// lists (0251) — the admin's only read surface for seconds stock.
		ListVariantSeconds(ctx context.Context, productID int) ([]entity.SecondsVariant, error)
		// SetVariantPrice atomically replaces a B-grade variant's manual price set (0251); empty clears
		// (fail-closed unsellable). entity.ErrVariantPriceNotSeconds for grade 'A', sql.ErrNoRows if absent.
		SetVariantPrice(ctx context.Context, variantID int, prices []entity.ColorwayPriceInsert) error
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
		// ArchiveColorway transitions ACTIVE|HIDDEN->ARCHIVED and stamps the archival audit.
		ArchiveColorway(ctx context.Context, colorwayID int) error
		// TransitionColorwayToHidden moves a colourway to HIDDEN via the single legal edge from its current
		// state: hide (ACTIVE->HIDDEN) or restore/unarchive (ARCHIVED->HIDDEN, clearing the deleted_at
		// tombstone). Any other source state is rejected by the entity state machine (fail-closed).
		TransitionColorwayToHidden(ctx context.Context, colorwayID int) error
		// ReduceStockForProductSizes reduces the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		ReduceStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// RestoreStockForProductSizes restores the stock for a product by its ID.
		// When history is not nil, records each change to product_stock_change_history.
		RestoreStockForProductSizes(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
		// RestoreStockForProductSizesSeconds restocks a refund's returned units into the product's
		// B-GRADE variant (seconds disposition, Phase 8) — created on first touch, zero carried cost.
		RestoreStockForProductSizesSeconds(ctx context.Context, items []entity.OrderItemInsert, history *entity.StockHistoryParams) error
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
		// ListWaitlist is the admin read over the waitlist (Phase 9): optional product filter,
		// newest first, capped pagination. CountWaitlistForProduct is the per-colourway counter.
		ListWaitlist(ctx context.Context, productId *int, limit, offset int) ([]entity.WaitlistEntryWithBuyer, int, error)
		CountWaitlistForProduct(ctx context.Context, productId int) (int, error)
		// RecordStockChange inserts stock change history entries.
		RecordStockChange(ctx context.Context, entries []entity.StockChangeInsert) error
		// GetStockChangeHistory returns paginated stock change history with optional filters.
		GetStockChangeHistory(ctx context.Context, productId, sizeId *int, dateFrom, dateTo *time.Time, source string, limit, offset int, orderFactor entity.OrderFactor) ([]entity.StockChange, int, error)
		// GetStockChanges returns simplified stock changes for reporting API.
		// GetStockChanges reads the stock journal; productionRunID (Phase 8) narrows to one run's
		// whole reference family (the run itself + its receipts).
		GetStockChanges(ctx context.Context, dateFrom, dateTo time.Time, productId *int, sizeId *int, source string, productionRunID *int, limit, offset int, sortByDirection entity.StockAdjustmentDirection, orderFactor entity.OrderFactor) ([]entity.StockChangeRow, int, error)
	}
	Hero interface {
		RefreshHero(ctx context.Context) error
		SetHero(ctx context.Context, hfi entity.HeroFullInsert) error
		GetHero(ctx context.Context) (*entity.HeroFullWithTranslations, error)
	}

	Campaigns interface {
		UpsertEmailCampaign(ctx context.Context, id int, campaign *entity.EmailCampaignInsert) (int, error)
		GetEmailCampaignByID(ctx context.Context, id int) (*entity.EmailCampaignFull, error)
		ListEmailCampaignsPaged(ctx context.Context, limit, offset int, status entity.EmailCampaignStatus, topic entity.EmailCampaignTopic) ([]entity.EmailCampaignFull, int, error)
		DeleteEmailCampaign(ctx context.Context, id int) error
		UpsertEmailSegment(ctx context.Context, id int, segment *entity.EmailSegment) (int, error)
		GetEmailSegmentByID(ctx context.Context, id int) (*entity.EmailSegment, error)
		ListEmailSegments(ctx context.Context) ([]entity.EmailSegment, error)
		DeleteEmailSegment(ctx context.Context, id int) error
		RefreshMarketingAggregate(ctx context.Context) (int64, error)
		PreviewSegmentCount(ctx context.Context, pred entity.SegmentPredicate) (int, error)
		SaveSegmentCount(ctx context.Context, segmentID, count int) error
		ScheduleEmailCampaign(ctx context.Context, campaignID int, at time.Time) error
		SendEmailCampaignNow(ctx context.Context, campaignID int) error
		PauseEmailCampaign(ctx context.Context, campaignID int, dispatchError *string) error
		ResumeEmailCampaign(ctx context.Context, campaignID int) error
		CancelEmailCampaign(ctx context.Context, campaignID int) error
		PromoteDueEmailCampaign(ctx context.Context) (int, error)
		PromoteEmailCampaignABWinners(ctx context.Context, selectWinner entity.EmailCampaignABWinnerSelector) (int, error)
		AdvanceEmailCampaignFanout(ctx context.Context, pageSize int, assign entity.EmailCampaignVariantAssigner) (*entity.EmailCampaignFanoutPageResult, error)
		ClaimEmailCampaignBatch(ctx context.Context, batchSize int, lease time.Duration) (*entity.EmailCampaignBatch, error)
		SaveEmailCampaignRecipientPayload(ctx context.Context, recipientID uint64, batchID, claimToken, unsubscribeURL string, payloadSHA256 []byte) error
		VerifyEmailCampaignRecipientPayload(ctx context.Context, recipientID uint64, payloadSHA256 []byte) (bool, error)
		MarkEmailCampaignBatchProviderAttempt(ctx context.Context, batchID, claimToken string) error
		ReleaseEmailCampaignBatch(ctx context.Context, batchID, claimToken string, nextAttemptAt *time.Time, errorCode, lastError *string) error
		CompleteEmailCampaignBatch(ctx context.Context, batchID, claimToken string, status entity.EmailCampaignRecipientStatus, errorCode string, lastError *string) error
		RecordEmailCampaignBatchAccepted(ctx context.Context, batchID, claimToken string, providerIDs []string) error
		QuarantineEmailCampaignRecipient(ctx context.Context, recipientID uint64, batchID, claimToken, errorCode, message string) error
		PutEmailCampaignRenderSnapshot(ctx context.Context, snapshot entity.EmailCampaignRenderSnapshot) error
		GetEmailCampaignRenderSnapshot(ctx context.Context, campaignID, variantID, languageID int) (*entity.EmailCampaignRenderSnapshot, error)
		FinalizeEmailCampaigns(ctx context.Context) (int64, error)
		GetEmailCampaignDispatchStatus(ctx context.Context, campaignID int) (*entity.EmailCampaignDispatchStatus, error)
		GetEmailCampaignRecipients(ctx context.Context, campaignID int, afterID uint64, limit int) (*entity.EmailCampaignRecipientPage, error)
		RecordRecipientEngagement(ctx context.Context, resendEmailID string, kind entity.EmailCampaignEngagementKind, at time.Time) error
		GetCampaignMetrics(ctx context.Context, campaignID int) (entity.CampaignMetrics, error)
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
		GetOrdersByStatusAndPaymentTypePaged(ctx context.Context, email string, orderUUID string, statusId, paymentMethodId, orderId, lim int, off int, of entity.OrderFactor) ([]entity.Order, int, error)
		GetOrdersOverview(ctx context.Context, todayStart time.Time) (*entity.OrdersOverview, error)
		GetAwaitingPaymentsByPaymentType(ctx context.Context, pmn ...entity.PaymentMethodName) ([]entity.PaymentOrderUUID, error)
		ExpireOrderPayment(ctx context.Context, orderUUID string) (*entity.Payment, error)
		OrderPaymentDone(ctx context.Context, orderUUID string, p *entity.Payment) (wasUpdated bool, err error)
		RefundOrder(ctx context.Context, orderUUID string, orderItemIDs []int32, reason, reasonCode string, refundShipping bool, disposition string) error
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
		AddOrderThreadComment(ctx context.Context, orderUUID, author, body string) (*entity.OrderComment, error)
		ListOrderComments(ctx context.Context, orderUUID string) ([]entity.OrderComment, error)
		// Reviews (internal statistics)
		AddOrderReview(ctx context.Context, orderUUID string, email string, orderReview *entity.OrderReviewInsert, itemReviews []entity.OrderItemReviewInsert) error
		GetOrderReviewsPaged(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor) ([]entity.OrderReviewFull, int, error)
		DeleteOrderReview(ctx context.Context, orderId int) error
		GetProductReviewsPaged(ctx context.Context, productId int, limit, offset int, orderFactor entity.OrderFactor) ([]entity.OrderItemReview, int, error)
		GetOrderReviewByUUID(ctx context.Context, orderUUID string) (*entity.OrderReviewFull, error)
		// ListOrdersFullByBuyerEmailPaged returns orders where buyer email matches, newest first, with total count.
		ListOrdersFullByBuyerEmailPaged(ctx context.Context, email string, limit, offset int) ([]entity.OrderFull, int, error)
	}

	// RecipientLanguage is the narrow read the mailer uses to resolve a recipient's email
	// language. Satisfied by StorefrontAccount.
	RecipientLanguage interface {
		GetRecipientLanguage(ctx context.Context, email string) (emailLang, defaultLang string, err error)
	}

	// StorefrontAccount handles customer account login, sessions, and saved addresses.
	StorefrontAccount interface {
		InsertLoginChallenge(ctx context.Context, email, otpHash, magicHash string, expiresAt time.Time) error
		ConsumeLoginChallengeOTP(ctx context.Context, email, otpPlain, otpPepper string) (string, error)
		ConsumeLoginChallengeMagic(ctx context.Context, magicPlain, magicPepper string) (string, error)
		GetOrCreateAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error)
		GetAccountByEmail(ctx context.Context, email string) (*entity.StorefrontAccount, error)
		// GetRecipientLanguage returns (email_language, default_language) for the account with
		// this email — both empty when unset or absent. Used by the mailer's language resolver.
		GetRecipientLanguage(ctx context.Context, email string) (emailLang, defaultLang string, err error)
		UpdateAccountProfile(ctx context.Context, email string, firstName, lastName string, birthDate sql.NullTime, shoppingPreference entity.StorefrontShoppingPreference, phone sql.NullString, subscribeNewsletter, subscribeNewArrivals, subscribeEvents bool, defaultCountry, defaultLanguage, emailLanguage sql.NullString) error
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
		UpdatePromoCode(ctx context.Context, promo *entity.PromoCodeInsert) error
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

	// Fittings manages garment try-on sessions with their sizes and media, plus the structured S26
	// change-request items (dedicated CRUD so their id is stable for carry-over).
	Fittings interface {
		AddFitting(ctx context.Context, f *entity.FittingInsert) (int, error)
		UpdateFitting(ctx context.Context, id int, f *entity.FittingInsert, expectedLockVersion int) error
		// UpdateFittingAndListOrphanedPatternURLs performs the same mutation and returns bucket-owned
		// pattern URLs that became globally unreferenced when the transaction committed.
		UpdateFittingAndListOrphanedPatternURLs(ctx context.Context, id int, f *entity.FittingInsert, expectedLockVersion int) ([]string, error)
		DeleteFitting(ctx context.Context, id int) error
		// DeleteFittingAndListOrphanedPatternURLs collects pattern URLs before the cascading delete and
		// returns only those no other card/fitting references after the transaction.
		DeleteFittingAndListOrphanedPatternURLs(ctx context.Context, id int) ([]string, error)
		GetFittingById(ctx context.Context, id int) (*entity.Fitting, error)
		ListFittings(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, productID, modelID, techCardID int) ([]entity.Fitting, int, error)
		// Structured change requests (S26): individually managed so carried_from_id / carry-over hold.
		AddFittingChangeRequest(ctx context.Context, cr *entity.FittingChangeRequest) (int, error)
		UpdateFittingChangeRequest(ctx context.Context, id int, cr *entity.FittingChangeRequest) error
		DeleteFittingChangeRequest(ctx context.Context, id int) error
		// ListOpenFittingChangeRequests is the carry-over view: a style's open remark tips from earlier
		// rounds (beforeRound > 0 scopes to rounds < beforeRound; 0 = all).
		ListOpenFittingChangeRequests(ctx context.Context, techCardID, beforeRound int) ([]entity.FittingChangeRequest, error)
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
		// CloneTechCardForSeason inserts the converted card and its non-TechCardInsert carry-over
		// (size chart, grade rule and assembly) in one transaction, under a source-version guard.
		CloneTechCardForSeason(ctx context.Context, sourceID, expectedSourceVersion int, tc *entity.TechCardInsert) (int, error)
		UpdateTechCard(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) error
		// UpdateTechCardAndListOrphanedPatternURLs performs the same mutation and returns bucket-owned
		// pattern URLs that became globally unreferenced when the transaction committed.
		UpdateTechCardAndListOrphanedPatternURLs(ctx context.Context, id int, tc *entity.TechCardInsert, expectedLockVersion int) ([]string, error)
		// UpdateColorwayRecipe replaces a colourway's material recipe (usages), optimistically locked
		// on the shared tech_card.lock_version; returns the bumped version (S2/S3 recipe write-path).
		UpdateColorwayRecipe(ctx context.Context, colorwayID, expectedVersion int, usages []entity.TechCardColorwayUsage) (int, error)
		// GetColorwayRecipe returns a colourway's material recipe (usages), the read side of
		// UpdateColorwayRecipe (H1 fix: the write-path was restored — WS3/S2-S3 — without a matching
		// read, leaving a full-replace write unsafe to edit partially). Empty, not an error, for a
		// colourway with no recipe yet.
		GetColorwayRecipe(ctx context.Context, colorwayID int) ([]entity.TechCardColorwayUsage, error)
		// SuggestStyleNumber proposes the next free style number for a season (Q1): {SEASON}{YY}-{SEQ}.
		SuggestStyleNumber(ctx context.Context, seasonCode string, seasonYear int) (string, error)
		// Role assignments (Q5): responsible admin accounts on a card, multi per role.
		AssignTechCardRole(ctx context.Context, a entity.TechCardRoleAssignment) (entity.TechCardRoleAssignment, error)
		RemoveTechCardRoleAssignment(ctx context.Context, id int) error
		ListTechCardRoleAssignments(ctx context.Context, techCardID int) ([]entity.TechCardRoleAssignment, error)
		// ListStyleAssembly returns a garment style's assembly bill: the auxiliary components (labels/
		// tags) that physically go on/into it, resolved for display (WS7, §2.8).
		ListStyleAssembly(ctx context.Context, styleID int) ([]entity.StyleAssembly, error)
		// UpsertStyleAssembly full-replaces a garment style's assembly bill (empty list clears it);
		// components must be auxiliary cards. Field-tagged errors on a bad payload (WS7, §2.8).
		UpsertStyleAssembly(ctx context.Context, styleID int, items []entity.StyleAssemblyInsert, username string) error
		// GetTechCardNames returns id → name for the given tech cards (cheap header-only lookup used by
		// the packing spec to label garment styles without an N+1 GetTechCardById).
		GetTechCardNames(ctx context.Context, ids []int) (map[int]string, error)
		// GetPatternViewerManifest is the narrow read behind the public pattern viewer
		// (GET /api/pv/{token}): style header, named size range, all pattern sheet rows and
		// the roll-goods BOM lines. Deliberately NOT GetTechCardById — that read carries the
		// whole card including costing, and this one feeds an unauthenticated endpoint.
		// sql.ErrNoRows when the card is absent.
		GetPatternViewerManifest(ctx context.Context, techCardID int) (*entity.PatternViewerCard, error)
		DeleteTechCard(ctx context.Context, id int) error
		// DeleteTechCardAndListOrphanedPatternURLs collects pattern URLs before the cascading delete and
		// returns only those no other card/fitting references after the transaction.
		DeleteTechCardAndListOrphanedPatternURLs(ctx context.Context, id int) ([]string, error)
		GetTechCardById(ctx context.Context, id int) (*entity.TechCard, error)
		GetTechCardByIdConsistent(ctx context.Context, id int) (*entity.TechCard, error)
		GetTechCardLockVersion(ctx context.Context, id int) (int, error)
		ListTechCards(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, filter entity.TechCardListFilter) ([]entity.TechCard, int, error)
		// GetStylePipeline returns the development board: one column per lifecycle stage with its count
		// and up to cardsPerStage light preview cards (gap-01).
		GetStylePipeline(ctx context.Context, cardsPerStage int) ([]entity.StylePipelineColumn, error)
		// GetTechCardReadiness returns the raw counts a style's advance/release checklist is scored
		// against, in one round trip. sql.ErrNoRows when the card is absent.
		GetTechCardReadiness(ctx context.Context, techCardID int) (entity.TechCardReadinessFacts, error)
		// GetTechCardReadinessSnapshot returns the facts and enriched card from one read snapshot so
		// callers can compare signed section digests with the exact content those facts describe.
		GetTechCardReadinessSnapshot(ctx context.Context, techCardID int) (entity.TechCardReadinessFacts, *entity.TechCard, error)
		// GetStyleSizeChart returns a style's full size chart + the shared tech_card.lock_version (R5).
		// sql.ErrNoRows when the style is absent.
		GetStyleSizeChart(ctx context.Context, styleID int) (entity.StyleSizeChart, error)
		// UpdateStyleSizeChart replaces a style's ENTIRE size chart in one versioned request (R5,
		// full-replace) under the shared optimistic lock; entity.ErrTechCardConflict on a stale version.
		// gradeBaseSizeID + gradeSteps are the authoring grade rule behind the expanded cells and are
		// replaced in the same transaction (0 / empty clears the rule).
		UpdateStyleSizeChart(ctx context.Context, styleID, expectedLockVersion int, cells []entity.StyleSizeChartCell, gradeBaseSizeID int, gradeSteps []entity.StyleSizeChartGradeStep) (entity.StyleSizeChart, error)
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
		// Colour variants of an AUXILIARY card's warehouse output (0252): one stock bucket per
		// colour, extending the single tech_card.output_material_id of 0111. ZERO variants is legacy
		// single-output mode and behaves exactly as before; the first variant switches the card over.
		//
		// ListOutputVariants resolves colour name, material name/unit and on-hand for display.
		// UpsertOutputVariant writes ONE row (ins.Id 0 creates; ins.MaterialId 0 on create
		// auto-creates the bucket) and returns its id; it refuses a sellable card
		// (ErrTechCardNotAuxiliary), a released card (ErrTechCardReleased), a duplicate colour
		// (*entity.ValidationError), a bucket another variant already owns
		// (ErrOutputVariantMaterialClaimed) and a unit that disagrees with the card's other colours
		// (ErrOutputVariantUnitMismatch). DeleteOutputVariant hard-deletes the row (the bucket itself
		// survives) — ErrOutputVariantNotFound when it is already gone.
		//
		// ListOutputVariantsByCardIds is the batched read behind the packing spec's colour resolution:
		// card id → its colours, in ONE round trip. RETIRED colours are included deliberately — "your
		// colour exists but is switched off" is the one answer that must never be auto-substituted — as
		// is each bucket's material.archived. Cards with no colours at all are absent from the map
		// (legacy single-output mode).
		ListOutputVariants(ctx context.Context, techCardID int) ([]entity.TechCardOutputVariant, error)
		ListOutputVariantsByCardIds(ctx context.Context, techCardIDs []int) (map[int][]entity.TechCardOutputVariant, error)
		UpsertOutputVariant(ctx context.Context, techCardID int, ins entity.TechCardOutputVariantInsert, username string) (int, error)
		DeleteOutputVariant(ctx context.Context, id int) error
		// Saved раскладки (markers, 0257). SaveMarker upserts by id (0 creates; last-write-wins, no
		// lock_version bump — see the store), refusing an incomplete layout (ErrMarkerIncomplete), a
		// released card, a size outside the card's range or an unknown bom_line_key. GetMarker is
		// the only read carrying the layout blob; summaries ride GetTechCardById.
		//
		// ins.ProductionRunId decides OWNERSHIP (run_id, 0282) and is written on CREATE ONLY: a save
		// that would move an existing раскладка between the card and a прогон is refused as a field
		// violation, because ownership is what decides whether the row dies with a run and whether a
		// секция настила may stand on it. Whoever needs the other kind copies the geometry (Р2).
		// GetTechCardById's markers, the card list's marker badge and the Ф1.8 direction report all
		// count КАРТОЧНЫЕ раскладки only; a run's are read through ListRunMarkers below.
		SaveMarker(ctx context.Context, techCardID, id int, ins entity.TechCardMarkerInsert, username string) (int, error)
		GetMarker(ctx context.Context, id int) (*entity.TechCardMarker, error)
		// ListRunMarkers returns the РАСКРОЙНЫЕ раскладки of ONE production run (tech_card_marker.run_id,
		// 0282) WITHOUT their layout blobs — the set the lay editor picks a section's marker from, and the
		// only read that returns them at all (a run's markers are hidden from the card's list). The
		// geometry of the markers a lay actually names is fetched through GetMarker and memoised per
		// marker_id by the caller.
		ListRunMarkers(ctx context.Context, runID int) ([]entity.TechCardMarkerSummary, error)
		// DeleteMarker removes a раскладка. Refuses on a released card, and — since Ф4 — on a
		// раскладка a секция настила stands on (ErrMarkerUsedByLay, wrapped with the настилы BY NAME).
		// That guard is the application's RESTRICT: fk_prlays_marker is CASCADE and must be, or
		// deleting a run would depend on InnoDB's cascade order.
		DeleteMarker(ctx context.Context, id int) error
		// SetMarkerNorm designates one раскладка as the НОРМИРОВОЧНАЯ one for its cloth (Ф3.4), or
		// clears it, and returns the id of the previous norm of the SAME cloth (0 = there was none).
		// The ONLY writer of is_norm: SaveMarker does not list the column, so re-saving geometry can
		// neither seize a norm nor lose one. Exclusivity within (card, BOM line) is held by this
		// transaction rather than by a UNIQUE index — see the store for the ERROR 1761 that rules the
		// index out — which is why every reader owes a deterministic tiebreak (entity.SelectNorm).
		SetMarkerNorm(ctx context.Context, id int, isNorm bool, username string) (int, error)
		// ListFabricDirectionGaps reads the кампания Д1 worklist (Ф1.8): cards whose roll-goods BOM
		// lines still carry no направление ткани, with the counts an owner triages by. techCardID
		// 0 = all cards. Every such card comes back — deciding which belong on a worklist is
		// entity.BuildFabricDirectionGapReport's job, not the store's.
		ListFabricDirectionGaps(ctx context.Context, techCardID int) ([]entity.FabricDirectionGapCard, error)
		// RepriceTechCardBom pulls the current catalog price into every catalog-linked BOM line of a
		// DRAFT card, stamping price_source='catalog' (production-costing Phase 3). Returns the
		// visited lines + the count of unlinked lines it could not touch.
		RepriceTechCardBom(ctx context.Context, tcID int, baseCurrency string) ([]entity.RepricedBomLine, int, error)
		// ListCostingMigrationExceptions reads the Phase 2 scalar→BOM migration exception report;
		// techCardID 0 = all cards.
		ListCostingMigrationExceptions(ctx context.Context, techCardID int) ([]entity.CostingMigrationException, error)
		// GetTechCardPatternSizeIndex returns a card's stored DXF size-token index, keyed by fabric
		// scope_key (Ф6.3, 0280). A missing key means «nobody has run the audit for that scope», which
		// the readiness gate must render as NO VERDICT — never as «that scope has no sizes».
		GetTechCardPatternSizeIndex(ctx context.Context, techCardID int) (map[string]entity.PatternSizeIndexRow, error)
		// PutTechCardPatternSizeIndex stores one scope's parsed size tokens. The sheet-set fingerprint
		// is computed BY THE STORE out of its own tech_card_size_pattern rows, and a client sheet list
		// that disagrees with the scope's membership is refused — that is the whole safety property of
		// the table, so it may not move to the caller.
		PutTechCardPatternSizeIndex(ctx context.Context, in entity.PatternSizeIndexWrite) (entity.PatternSizeIndexResult, error)
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
		// UpdateProductionRun expects the incoming cost articles UNFOLDED: it preserves each
		// unchanged article's stored amount_base under the run lock, then folds the rest with fx.
		// expectedLockVersion carries PRESENCE (entity.LockGuard): a supplied version is enforced
		// even at 0, and only an absent one keeps the legacy last-write-wins.
		UpdateProductionRun(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion entity.LockGuard, fx dto.CostingFx) error
		// UpdateProductionRunPreservingCosts performs the same update but reloads and carries stored
		// cost articles under the run's FOR UPDATE lock, for callers without costing write access.
		UpdateProductionRunPreservingCosts(ctx context.Context, id int, r *entity.ProductionRunInsert, expectedLockVersion entity.LockGuard) error
		DeleteProductionRun(ctx context.Context, id int) error
		GetProductionRun(ctx context.Context, id int) (*entity.ProductionRun, error)
		ListProductionRuns(ctx context.Context, limit, offset int, filter entity.ProductionRunListFilter) ([]entity.ProductionRun, int, error)
		// CleanupExpiredCommandIdempotency purges command_idempotency rows past the 90-day replay
		// window (bounded per call; storefrontcleanup ticks it).
		CleanupExpiredCommandIdempotency(ctx context.Context) (int64, error)
		// PostProductionRunReceipt is the atomic receiving command (Phase 4, receipt v1, final-only):
		// one transaction records the immutable receipt + counted lines (addressed by the plan lines'
		// line_key, resolved under the run lock), stamps the counts onto the plan grid, books good
		// units into product stock (or params.OutputMaterialID's warehouse for an auxiliary run),
		// freezes the run's actual unit cost on the receipt, optionally seeds cost_price, transitions
		// the run to received, and writes the idempotency record. A retry with the same key and hash
		// replays the original result (Replayed=true); the same key with a different hash returns
		// entity.ErrIdempotencyConflict. The receipt row is the accounting outbox the posting worker
		// consumes.
		PostProductionRunReceipt(ctx context.Context, params entity.PostProductionRunReceiptParams) (*entity.PostProductionRunReceiptResult, error)
		// ReverseProductionRunReceipt undoes ONE receipt in a single transaction (Phase 6): stock
		// back out (full shortfall list on refusal — never negative, sold units never stolen),
		// rollups subtracted, scoped accounting compensation (Dr WIP / Cr FG; the manual/AP
		// capitalisation stays), reversal linkage in the receipt history, cost_price rollback for
		// products the run still claims, run status recomputed, production_run_event recorded.
		// The run lock is the idempotency guard: an already-reversed receipt returns
		// entity.ErrProductionRunReceiptAlreadyReversed.
		ReverseProductionRunReceipt(ctx context.Context, params entity.ReverseProductionRunReceiptParams) (*entity.ReverseProductionRunReceiptResult, error)

		// НАСТИЛЫ (Ф4, migration 0281). Deliberately THREE commands of their own rather than a field
		// of the run's payload: a full replace of the run's children on every run save is what killed
		// production_run_marker (0119, dropped by 0243), and UpdateProductionRun stays unable to touch
		// a lay because it does not know lays exist.
		//
		// SaveLay addresses EXACTLY ONE lay by its key: a lay the payload does not mention is not
		// touched, and only DeleteLay removes one. Inside that lay the sections are diffed by
		// section_key — a section whose ply count changed keeps its id, so Ф5б can hang the
		// consumption fact and the cutting receipt off it. expectedLockVersion is enforced by
		// PRESENCE: an absent token on an existing lay is entity.ErrProductionRunLayConflict, with no
		// legacy opt-out. reaffirm recomputes the quantity snapshot without a section edit; a
		// note-only save leaves it (and the stale badge) alone.
		SaveLay(ctx context.Context, runID int, ins entity.ProductionRunLayInsert,
			expectedLockVersion entity.LockGuard, reaffirm bool, username string) (entity.ProductionRunLay, error)
		// DeleteLay removes one lay and its sections. entity.ErrProductionRunLayNotFound when the run
		// does not hold that key.
		DeleteLay(ctx context.Context, runID int, layKey string) error
		// ListLays returns the run's whole lay plan. Applicable=false (with a reason) for an auxiliary
		// card — stated explicitly, because an empty list reads as an invitation to build one.
		ListLays(ctx context.Context, runID int) (entity.ProductionRunLayList, error)
		// ListMeasuredLayCandidates returns every настил ACROSS RUNS that carries a consumption fact
		// and whose tech card names the given article anywhere — the input the cutting-coefficient
		// calibration (Ф5б.3) medians over.
		//
		// «CANDIDATES» IS A PROMISE ABOUT WHAT IT DOES NOT DO. A настил does not store material_id: it
		// stores (colourway, BOM slot), and the article behind that pair is resolved by exactly one
		// function, dto.LayArticleMaterialId — the colourway's pin when it has one, the slot's default
		// otherwise. A SQL filter on the slot's material_id would drop every lay whose colourway pinned
		// a different cloth, quietly and plausibly. So the query only bounds the work (cards that name
		// the article NOWHERE cannot resolve to it for any lay) and the caller decides lay by lay.
		//
		// Sections ARE loaded (the plan is Σ marker length × plies); the quantity snapshot is NOT —
		// staleness is a per-run comparison and this list crosses runs.
		ListMeasuredLayCandidates(ctx context.Context, materialID int) ([]entity.ProductionRunLayFact, error)

		// ПРИЁМКА КРОЯ (Ф5б.5, migration 0287). Two numbers per pair (настил, размер) — выкроено and
		// принято в пошив — inserted BETWEEN production_run_line's planned_qty and received_qty,
		// which these methods only ever READ. Re-meaning the existing fields would open a double
		// ledger where «принято в пошив» and «сдано готовым» drift apart under one name.
		//
		// Unlike a настил, a receipt is accepted on a TERMINAL run: a настил is a plan and rewriting
		// a finished run's plan rewrites history, while a receipt is a report of what happened at the
		// cutting table — and cutting precedes sewing, which precedes receiving. The numbers are
		// never validated against each other or against the plan: overcutting is legitimate, and the
		// discrepancy is carried to the reader rather than refused.
		//
		// SaveCutReceipt upserts ONE pair (настил addressed by lay_key, размер) and leaves every
		// other pair alone — a pair the payload does not mention is not touched.
		SaveCutReceipt(ctx context.Context, runID int, layKey string,
			ins entity.ProductionRunCutReceiptInsert, username string) (entity.ProductionRunCutReceipt, error)
		// DeleteCutReceipt removes the count of ONE pair — the only way to say «this size was never
		// on this настил», which a row of zeroes cannot say.
		// entity.ErrProductionRunCutReceiptNotFound when the pair holds no receipt.
		DeleteCutReceipt(ctx context.Context, runID int, layKey string, sizeID int) error
		// ListCutReceipts returns every receipt of the run across all of its настилы. By run and not
		// by настил: the cutting room reconciles заказанное against выкроенное for the whole run at
		// once. The rows carry NO planned/received quantity — those are per (колорвей, размер) while
		// a receipt is per (настил, размер), and one colourway legitimately has several настилы, so
		// copying the order onto each of their rows would make any sum over receipts double it.
		ListCutReceipts(ctx context.Context, runID int) ([]entity.ProductionRunCutReceipt, error)
	}

	// Samples is the sample (сэмпл) repository (new-flow NF-04): a sewn prototype of a style, with
	// a cost composed on read from material issues + the dev-expense journal.
	Samples interface {
		AddSample(ctx context.Context, sm *entity.SampleInsert) (int, error)
		UpdateSample(ctx context.Context, id int, sm *entity.SampleInsert, expectedLockVersion int) error
		DeleteSample(ctx context.Context, id int) error
		GetSampleById(ctx context.Context, id int) (*entity.Sample, error)
		// ListSamples lists samples; techCardID <= 0 spans all styles (cross-style queue), and
		// status/purpose are optional string filters ("" = any).
		ListSamples(ctx context.Context, limit, offset int, orderFactor entity.OrderFactor, techCardID int, status, purpose string) ([]entity.Sample, int, error)
		// Substitutions (§2.7): dev-time material deviations recorded on a sample (Q2: never COGS).
		AddSampleSubstitution(ctx context.Context, sub *entity.SampleSubstitutionInsert) (int, error)
		ListSampleSubstitutions(ctx context.Context, sampleID int) ([]entity.SampleSubstitution, error)
		DeleteSampleSubstitution(ctx context.Context, id int) error
	}

	// MaterialStock is the material warehouse (new-flow NF-01): the maintained on-hand balance +
	// moving-average unit cost per catalog material, and the append-only movement ledger. Distinct
	// from Inventory (which is the finished-goods valuation metrics of task 16).
	MaterialStock interface {
		ReceiveMaterialStock(ctx context.Context, ins entity.MaterialReceiptInsert) (entity.MaterialMovement, error)
		IssueMaterialStock(ctx context.Context, ins entity.MaterialIssueInsert) (entity.MaterialMovement, error)
		BatchIssueMaterialStock(ctx context.Context, ins entity.MaterialBatchIssueInsert) ([]entity.MaterialMovement, error)
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
		// Since 0286 "reserved" folds BOTH owners of the ledger — orders holding packaging and runs
		// holding fabric — because the reader sums by material without asking who owns the claim.
		MaterialAvailable(ctx context.Context, materialID int) (entity.MaterialAvailability, error)
		// SetRunMaterialReservations makes a production run's open fabric claims equal `required`
		// (Ф5б.4): the hold placed when the run is created (requirement from the NORM) and the
		// correction when the lays displace that norm are the SAME call with different numbers. A
		// correction is release + reserve of the next generation, never an update, so `available`
		// moves by exactly the difference. `required` is the OUTSTANDING need — gross requirement
		// minus what is already issued, which has physically left on_hand. Idempotent.
		SetRunMaterialReservations(ctx context.Context, runID int, required map[int]entity.RunMaterialRequirement, username string) error
		// ReleaseRunReservations closes every open fabric claim of a run. Called automatically when a
		// run is closed or cancelled; exposed for an operator freeing an abandoned run's cloth by hand.
		ReleaseRunReservations(ctx context.Context, runID int, username string) error
		// ListMaterialReservations returns every OPEN claim on a material — both owners, with the
		// claim's age — oldest first. This is the answer to "the cloth reads short, who is holding it":
		// a run parked in `planned` since March is a legitimate, and visible, hold.
		ListMaterialReservations(ctx context.Context, materialID int) ([]entity.MaterialReservationClaim, error)
		// LotAvailable returns one lot's remaining minus the open claims naming that lot (Ф5б.6, Р7).
		// Asked ONLY by the recut check — a recut must come out of the same dye lot as the original
		// cut. Deliberately not folded into MaterialAvailable: the general question is about the
		// material, and answering it per roll would refuse a run holding plenty across several rolls.
		LotAvailable(ctx context.Context, lotID int) (entity.MaterialLotAvailability, error)
		// ListPackagingRecipe returns every packaging recipe (all scopes) joined with material name/unit.
		ListPackagingRecipe(ctx context.Context) ([]entity.PackagingRecipe, error)
		// ResolveOrderPackaging returns the packaging materials an order needs (product → style → global),
		// joined with material name/unit, for the read-only packer packing spec (WS7 scope 3).
		ResolveOrderPackaging(ctx context.Context, orderID int) ([]entity.OrderPackingSpecPackaging, error)
		// UpsertPackagingRecipe full-replaces one scope target's recipe lines (the whole global set, or
		// one style's set, or one product's set).
		UpsertPackagingRecipe(ctx context.Context, scope entity.PackagingRecipeScope, techCardID, productID sql.NullInt32, items []entity.PackagingRecipeInsert, username string) error
		// ListMaterialLots returns a material's structured lots / rolls (gap-07 v2 D), active-only unless
		// includeArchived. Traceability registry; valuation stays moving-average.
		ListMaterialLots(ctx context.Context, materialID int, includeArchived bool) ([]entity.MaterialLot, error)
		// NarrowestMeasuredLotWidths returns, per material, the narrowest MEASURED ROLL width among the
		// lots that still have stock (Ф6.2). A material with no measured, non-empty lot is ABSENT from
		// the map — «nobody measured it» is not «it matches the nominal», and the readiness gate falls
		// back to the catalogue width on an absent key rather than inventing one. кромка is NOT
		// subtracted here; the comparison rule needs the raw figure to show its arithmetic.
		NarrowestMeasuredLotWidths(ctx context.Context, materialIDs []int) (map[int]decimal.NullDecimal, error)
	}

	// Accounting is the double-entry general ledger (docs/plan-accounting/). The ledger is a DERIVED,
	// append-only projection of existing operational facts (orders, material movements, production
	// runs, opex) plus manual entries; base currency is EUR (reads total_settled_base, never
	// total_price_eur — CLAUDE.md "two EUR figures"). CreateJournalEntry is the single write path and
	// never opens its own transaction — callers (the acctposting worker and the manual-entry admin
	// handlers) wrap it in repo.Tx so the entry header and its lines commit atomically.
	Accounting interface {
		ContextStore

		// --- chart of accounts ---
		ListAccounts(ctx context.Context, includeArchived bool) ([]entity.AcctAccount, error)
		CreateAccount(ctx context.Context, in entity.AcctAccountInsert) (int, error)
		// UpdateAccountName renames a custom account; code and section are immutable, and a system
		// account cannot be renamed by code (ErrAcctSystemAccount).
		UpdateAccountName(ctx context.Context, code, name string) error
		// SetAccountArchived archives/unarchives a custom account; a system account (is_system) cannot
		// be archived (ErrAcctSystemAccount).
		SetAccountArchived(ctx context.Context, code string, archived bool) error

		// --- journal ---
		// CreateJournalEntry is the ONLY write path into the journal (both automated posting and manual
		// entries). It validates: >= 2 lines, each amount > 0, Σdebit == Σcredit (ErrAcctUnbalanced),
		// accounts exist and are not archived (ErrAcctUnknownAccount / ErrAcctArchivedAccount), and the
		// occurred_at period is open (ErrAcctPeriodClosed). Idempotent on (source_type, source_key): a
		// duplicate returns the existing id with alreadyExists=true and no error (the upsert pattern).
		CreateJournalEntry(ctx context.Context, in entity.AcctJournalEntryInsert) (id int, alreadyExists bool, err error)
		// ReverseJournalEntry posts a mirror entry (sides swapped) in the currently open period
		// (occurred_at = the original's date if still open, else today), source_type='reversal',
		// source_key='rev:'+<origID>, and sets the original's reversed_by. Reversing an already-reversed
		// entry → ErrAcctAlreadyReversed; reversing a reversal → ErrAcctCannotReverseReversal.
		ReverseJournalEntry(ctx context.Context, entryID int, reason, adminUsername string) (int, error)

		ListJournalEntries(ctx context.Context, f entity.AcctEntryFilter) ([]entity.AcctJournalEntry, int, error)
		GetJournalEntry(ctx context.Context, id int) (*entity.AcctJournalEntryFull, error)
		// GetLiveProductionReceiveEntry returns the receipt's live (un-reversed) production_receive
		// entry with lines, or nil when none exists. Key families mirror ListUnpostedReceipts. The
		// reversal command sizes its scoped compensation from it.
		GetLiveProductionReceiveEntry(ctx context.Context, receiptID, runID int) (*entity.AcctJournalEntryFull, error)
		// EntryExistsBySource is an O(1) (source_type, source_key) unique-index existence lookup
		// (uniq_acct_entry_source) — e.g. the refund worker's "has the sale been posted?" check.
		EntryExistsBySource(ctx context.Context, sourceType entity.AcctSourceType, sourceKey string) (bool, error)

		// GetOrderPostingState reports one order's delivered-chain posting state (which entries exist,
		// whether an order_delivered event was enqueued) and the exact outstanding 2090 / 1140 balances
		// the delivered sale must drain (phase 2, wave 2).
		GetOrderPostingState(ctx context.Context, orderUUID string) (entity.AcctOrderPostingState, error)

		// --- periods ---
		// EnsurePeriodOpen lazily creates the period row for month and returns ErrAcctPeriodClosed if it
		// exists and is closed.
		EnsurePeriodOpen(ctx context.Context, month time.Time) error
		ClosePeriod(ctx context.Context, month time.Time, adminUsername string) error
		ReopenPeriod(ctx context.Context, month time.Time, adminUsername string) error
		ListPeriods(ctx context.Context) ([]entity.AcctPeriod, error)

		// --- outbox / checkpoints (used by producers and the posting worker) ---
		// EnqueueEvent marshals ev.Payload (any → JSON) itself; a marshal error is returned (a producer
		// in a hot Tx must propagate it). A duplicate (event_type, source_key) is a no-op.
		EnqueueEvent(ctx context.Context, ev entity.AcctEventInsert) error
		// ListPendingEvents returns events with processed_at IS NULL AND (next_retry_at IS NULL OR
		// next_retry_at <= NOW()), ordered by id, up to limit.
		ListPendingEvents(ctx context.Context, limit int) ([]entity.AcctEvent, error)
		MarkEventProcessed(ctx context.Context, id int64) error
		// MarkEventFailed bumps attempts, records errMsg and sets next_retry_at = NOW() + retryAfter.
		MarkEventFailed(ctx context.Context, id int64, errMsg string, retryAfter time.Duration) error
		// MarkEventNeedsReview terminally disposes an event (processed) but flags it needs_review so
		// ClosePeriod blocks the month until an operator clears it (H-1/H-2/B-5).
		MarkEventNeedsReview(ctx context.Context, id int64, reason string) error
		// ReprocessAcctEvent resets an event (clears processed/needs_review/attempts/backoff) to retry.
		ReprocessAcctEvent(ctx context.Context, id int64) error
		// ResolveAcctEvent clears needs_review (handled manually), keeping the processed/audit record.
		ResolveAcctEvent(ctx context.Context, id int64) error
		// ListEventsNeedingReview returns needs_review events, oldest first, up to limit.
		ListEventsNeedingReview(ctx context.Context, limit int) ([]entity.AcctEvent, error)
		// CountEventsNeedingReviewInPeriod counts needs_review events with occurred_at in [from,to).
		CountEventsNeedingReviewInPeriod(ctx context.Context, from, to string) (int, error)
		// GetCheckpoint returns the pull-source cursor; a missing row is NOT an error — it returns the
		// zero AcctCheckpoint (the worker treats it as last_id=0 / last_ts=accounting.start_date).
		GetCheckpoint(ctx context.Context, source string) (entity.AcctCheckpoint, error)
		SetCheckpoint(ctx context.Context, source string, lastID sql.NullInt64, lastTS sql.NullTime) error

		// --- reports (contracts in docs/plan-accounting/06-reports.md; filled in step 7) ---
		GetTrialBalance(ctx context.Context, from, to time.Time) (*entity.AcctTrialBalance, error)
		GetProfitLoss(ctx context.Context, from, to time.Time) (*entity.AcctProfitLoss, error)
		GetBalanceSheet(ctx context.Context, asOf time.Time) (*entity.AcctBalanceSheet, error)
		GetAccountLedger(ctx context.Context, code string, f entity.AcctLedgerFilter) (*entity.AcctAccountLedger, error)
		GetReconciliation(ctx context.Context, from, to time.Time) (*entity.AcctReconciliation, error)

		// --- VAT filing exports (phase 2, wave 1; source-type-agnostic, aggregated by vat_regime over
		//     the payment period — docs/plan-accounting-phase2/01-wave1-vat.md §1.5) ---
		GetVatReturnPL(ctx context.Context, month time.Time) (*entity.AcctVatReturnPL, error)
		GetOssReturn(ctx context.Context, quarterStart time.Time) (*entity.AcctOssReturn, error)
		// VatSalesEvidence returns per-order sales rows for the JPK_V7M sales register (SprzedazWiersz),
		// for the regimes the Polish register declares (pl_domestic/wdt/export).
		VatSalesEvidence(ctx context.Context, month time.Time) ([]entity.AcctVatSalesRow, error)
		// GetUkVatReturn aggregates the quarter's UK VAT figures (9-box MTD) for the uk_stock_domestic
		// regime — a separate jurisdiction, never folded into the Polish net payable.
		GetUkVatReturn(ctx context.Context, quarterStart time.Time) (*entity.AcctUkVatReturn, error)
		// Filing-currency variants (statutory review 13): PLN for the Polish JPK set, GBP for the
		// UK return, converted per transaction at the D-1 daily reference rate; they FAIL when a
		// needed rate is missing rather than silently misstating a filing.
		GetVatReturnPLFiling(ctx context.Context, month time.Time) (*entity.AcctVatReturnPL, error)
		VatSalesEvidenceFiling(ctx context.Context, month time.Time) ([]entity.AcctVatSalesRow, error)
		VatPurchaseEvidenceFiling(ctx context.Context, month time.Time) ([]entity.AcctVatPurchaseRow, error)
		GetVatUe(ctx context.Context, month time.Time) (*entity.AcctVatUe, error)
		GetUkVatReturnFiling(ctx context.Context, quarterStart time.Time) (*entity.AcctUkVatReturn, error)
		// GetFrs105Accounts re-groups the ledger into FRS 105 micro-entity line items (Income Statement +
		// SoFP) over [from, to) — a base-currency DRAFT (not GBP / entity-isolated).
		GetFrs105Accounts(ctx context.Context, from, to time.Time) (*entity.AcctFrs105Accounts, error)
		// GetCashFlowStatement is the indirect-method cash-flow statement over [from, to) (wave 5, §5.1):
		// net profit + non-cash add-backs + balance-sheet deltas, reconciled against the actual cash balance.
		GetCashFlowStatement(ctx context.Context, from, to time.Time) (*entity.AcctCashFlowStatement, error)
		// GetFinancialHealth computes the financial-health ratio set over [from, to) (wave 5, §5.2) from the
		// ledger (money) plus operational unit counts from metrics (labelled by source).
		GetFinancialHealth(ctx context.Context, from, to time.Time) (*entity.AcctFinancialHealth, error)
		// Fixed-asset register + straight-line depreciation (posts Dr 6370 / Cr 1225 per asset-month).
		CreateFixedAsset(ctx context.Context, in entity.FixedAssetInsert) (int, error)
		ListFixedAssets(ctx context.Context) ([]entity.FixedAsset, error)
		PostDepreciationDue(ctx context.Context, upTo time.Time) (posted int, skipped int, err error)
		// AccrueCorporationTax posts CT on the period's pre-tax profit (Dr 8010 / Cr 2050); idempotent
		// per period. Returns the CT accrued and whether it already existed.
		AccrueCorporationTax(ctx context.Context, from, to time.Time, ratePct decimal.Decimal) (decimal.Decimal, bool, error)

		// --- fact reads for the posting worker (the accounting store reads other domains' tables
		//     directly, the internal/store/metrics precedent; SQL in 09-implementation-notes.md §9.2) ---
		GetOrderFactsForPosting(ctx context.Context, orderUUID string) (*entity.AcctOrderFacts, error)
		// GetVatRatesFor returns the vat_rate percent of each present ISO alpha-2 code (phase 2, wave 1);
		// an absent country is simply missing from the map, so the worker can skip the order with a "vat
		// rate missing" alert rather than post a zero rate (07 §7.4.14).
		GetVatRatesFor(ctx context.Context, codes []string) (map[string]decimal.Decimal, error)
		// SetOrderVatRegime snapshots the resolved VAT regime onto customer_order.vat_regime; the worker
		// calls it in the same tx as the order-sale entry (§1.3).
		SetOrderVatRegime(ctx context.Context, orderUUID, regime string) error
		ListUnpostedMovements(ctx context.Context, afterID int64, startDate time.Time, limit int) ([]entity.AcctMovementFacts, error)
		// ListUnpostedReceipts returns production receipts (Phase 4: the accounting unit of a receive)
		// received on/after startDate with posting_status='pending', no reversal linkage, and no live
		// production_receive entry under either the 'receipt:<id>' family or the legacy '<run_id>'
		// family. Oldest first. Dead-lettered receipts are excluded (they alerted; period close still
		// sees them).
		ListUnpostedReceipts(ctx context.Context, startDate time.Time, limit int) ([]entity.AcctReceiptRef, error)
		// GetReceiptFactsForPosting assembles the production-receive fact set for one receipt: the
		// run's costs and material issues (P1, run-scoped — v1 receipts are final-only) plus the
		// receipt's own received_at and good-quantity total.
		GetReceiptFactsForPosting(ctx context.Context, receiptID int) (*entity.AcctRunFacts, error)
		// LockReceiptForPosting takes the receipt's row lock inside the worker's posting tx and
		// reports whether the receipt was scope-reversed meanwhile — posting must then be skipped
		// (the reversal already took the goods out of stock).
		LockReceiptForPosting(ctx context.Context, receiptID int) (reversed bool, err error)
		// MarkReceiptPosted stamps posting_status='posted' plus what the live entry capitalised
		// (Cr 2010) and relieved (Dr 1130); called in the same tx as the entry insert. A no-op on
		// a scope-reversed receipt (backstop to LockReceiptForPosting).
		MarkReceiptPosted(ctx context.Context, receiptID int, manualBase, fgBase decimal.Decimal) error
		// MarkReceiptPostedFromEntry marks posted with amounts recovered from an existing live
		// entry's lines (the worker's raced path).
		MarkReceiptPostedFromEntry(ctx context.Context, receiptID, entryID int) error
		// MarkReceiptSkipped stamps last_skipped_at = now: the worker rebuilt the receipt and got a
		// clean "nothing to post". The receipt stays pending (transient empties self-heal) but drops
		// behind never-skipped receipts in the scan order and out of the stuck-pending gauge while
		// the stamp is fresh.
		MarkReceiptSkipped(ctx context.Context, receiptID int) error
		// RecordReceiptPostingFailure increments posting_attempts, stores the error, and flips the
		// receipt to dead_letter once attempts reach maxAttempts — reporting whether it did.
		RecordReceiptPostingFailure(ctx context.Context, receiptID int, errMsg string, maxAttempts int) (deadLettered bool, err error)
		// CountReceiptPostingBacklog reports pending receipts received in [startDate, olderThan) —
		// stuck work; the lower bound excludes 0231's legacy backfills, which the scan never drains —
		// and the number of dead-lettered receipts (operator attention required).
		CountReceiptPostingBacklog(ctx context.Context, startDate, olderThan time.Time) (pending int, deadLettered int, err error)
		ListChangedOpexMonths(ctx context.Context, afterTS time.Time) ([]time.Time, error)
		GetOpexMonthFacts(ctx context.Context, month time.Time) ([]entity.AcctOpexCategorySum, error)
		// ListChangedShipmentsForActualCost returns shipments whose actual carrier cost changed after
		// afterTS (the shipping_actual checkpoint), for the wave-3 6030 pull (3.1). The worker reposts each.
		ListChangedShipmentsForActualCost(ctx context.Context, afterTS, startDate time.Time) ([]entity.AcctShipmentCostFacts, error)
		// ListDevExpensesForPosting returns tech_card_dev_expense rows created on/after startDate, for the
		// wave-3 6210 dev-expense pull (3.2) — a full reconcile scan (the table has no updated_at).
		ListDevExpensesForPosting(ctx context.Context, startDate time.Time) ([]entity.AcctDevExpenseFacts, error)

		// --- wave 4: Revolut bank inbox (4.1) ---
		// ImportBankTxns deduplicates parsed inbox lines into acct_bank_txn (external_id UNIQUE), applies
		// the acct_bank_rule substring suggestions, and reports parsed/imported/skipped counts.
		ImportBankTxns(ctx context.Context, txns []entity.AcctBankTxnInsert) (entity.AcctBankImportResult, error)
		// ListBankTxns returns inbox lines filtered by state ("" = all), newest first, bounded to limit.
		ListBankTxns(ctx context.Context, state string, limit int) ([]entity.AcctBankTxn, error)
		// GetBankTxn loads one inbox line (sql.ErrNoRows when absent).
		GetBankTxn(ctx context.Context, id int) (*entity.AcctBankTxn, error)
		// SetBankTxnPosted marks a line posted and links it to its journal entry (no-op if already posted).
		SetBankTxnPosted(ctx context.Context, id, entryID int) error
		// SetBankTxnIgnored marks a not-yet-posted line ignored and persists the operator's reason
		// (an ignored line books nothing, so the reason is its only trace).
		SetBankTxnIgnored(ctx context.Context, id int, reason string) error
		// ListBankRules returns the substring→account suggestion rules.
		ListBankRules(ctx context.Context) ([]entity.AcctBankRule, error)
		// CreateBankRule inserts a suggestion rule and returns its id.
		CreateBankRule(ctx context.Context, pattern, accountCode string) (int, error)
		// DeleteBankRule removes a suggestion rule (sql.ErrNoRows when absent).
		DeleteBankRule(ctx context.Context, id int) error

		// --- wave 4: Stripe disputes (4.3) ---
		// GetEntryBySource returns the journal-entry header for a (source_type, source_key), sql.ErrNoRows
		// when none — the dispute worker uses it to find the open dispute entry to reverse on a win.
		GetEntryBySource(ctx context.Context, sourceType entity.AcctSourceType, sourceKey string) (*entity.AcctJournalEntry, error)

		// --- wave 4: AP/AR subledgers (4.4) ---
		// CreateSupplier inserts a supplier (unique name) and returns its id.
		CreateSupplier(ctx context.Context, in entity.SupplierInsert) (int, error)
		// ListSuppliers returns the supplier catalog, name-ordered.
		ListSuppliers(ctx context.Context) ([]entity.Supplier, error)
		// GetPayables returns the open Accounts-Payable (2010) position per supplier (accrued − paid).
		GetPayables(ctx context.Context) ([]entity.AcctPayableRow, error)
		// GetReceivables returns the open Accounts-Receivable (1040) position per bank-invoice order.
		GetReceivables(ctx context.Context) ([]entity.AcctReceivableRow, error)
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

	// Workshop is «дом настроек цеха» (Ф2.5, 0272): the singleton row of shop-floor constants that
	// several phases of the cutting plan each assumed existed. Distinct from Settings, which is the
	// STOREFRONT's configuration (carriers, payment, site availability) — nothing about the workshop
	// belongs on that surface and nothing here belongs on the storefront.
	Workshop interface {
		GetSettings(ctx context.Context) (*entity.WorkshopSettings, error)
		// UpdateSettings applies a partial patch (a setting the patch does not name keeps its stored
		// value) and returns the resulting configuration.
		UpdateSettings(ctx context.Context, patch entity.WorkshopSettingsPatch, updatedBy string) (*entity.WorkshopSettings, error)
	}

	// PatternObjects manages pattern_object_access rows — per-object revocation epoch,
	// expiry policy and coarse access stats behind the tokenized pattern read path
	// /api/p/{token}. Rows are created lazily; a missing row means default access state.
	PatternObjects interface {
		GetById(ctx context.Context, id int64) (*entity.PatternObjectAccess, error)
		// EnsureByKeys returns rows for the given managed object keys, creating missing
		// ones (epoch 0, no expiry) without touching existing state.
		EnsureByKeys(ctx context.Context, refs []entity.PatternObjectRef) (map[string]entity.PatternObjectAccess, error)
		// BumpEpoch invalidates every token minted for the object so far.
		BumpEpoch(ctx context.Context, id int64) error
		// Revoke hard-disables access until un-revoked (distinct from a rotating bump).
		Revoke(ctx context.Context, id int64, at time.Time) error
		// RecordAccess folds a debounced batch of access stats into the rows.
		RecordAccess(ctx context.Context, counts map[int64]int64, last map[int64]time.Time) error
		// DeleteByKeys drops rows whose objects were garbage-collected.
		DeleteByKeys(ctx context.Context, keys []string) error

		// Card-viewer rows (tech_card_pattern_viewer_access, 0288): the card-level twin of
		// the object rows above, behind /api/pv/{token}. Keyed by TECH CARD id — a 'c'
		// token must resolve through these, never through GetById (the id spaces overlap
		// numerically and name different things).
		//
		// EnsureCardViewer returns the card's row, creating it lazily (epoch 1, no expiry)
		// without touching existing state. GetCardViewer returns sql.ErrNoRows when absent.
		EnsureCardViewer(ctx context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error)
		GetCardViewer(ctx context.Context, techCardID int) (*entity.TechCardPatternViewerAccess, error)
		// RecordCardViewerAccess folds a debounced batch of viewer access stats into the rows.
		RecordCardViewerAccess(ctx context.Context, counts map[int]int64, last map[int]time.Time) error
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
		Campaigns() Campaigns
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
		Accounting() Accounting
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
		Workshop() Workshop
		Support() Support
		Language() Language
		PatternObjects() PatternObjects
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

		ListFibers(ctx context.Context, includeArchived bool) ([]entity.Fiber, error)
		CreateFiber(ctx context.Context, code, name string, expectedVersion int64) (entity.Fiber, int64, error)
		ArchiveFiber(ctx context.Context, code string, expectedVersion int64) (int64, error)

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
		// UploadPatternFile uploads a raw cut pattern (выкройка) — PDF or DXF, sniffed from
		// the bytes — and returns its url and stored byte size. The object extension
		// (.pdf / .dxf) carries the file type. The file is kept out of the media library.
		UploadPatternFile(ctx context.Context, raw []byte, objectName string) (string, int64, error)
		// UploadLabelPDF durably stores a carrier shipping-label PDF (whose provider URL expires)
		// and returns its CDN url and stored byte size. Kept out of the media library.
		UploadLabelPDF(ctx context.Context, raw []byte, objectName string) (string, int64, error)
		// PresignPatternObject returns a short-lived presigned GET url for a managed
		// pattern object key, targeting the ORIGIN endpoint (presigned requests never
		// pass the CDN — SigV4 binds the Host). The expiry is snapped to a deterministic
		// window so the url string is stable within it (browser HTTP cache; no
		// <object>/viewer remounts). download=true adds a content-disposition=attachment
		// response override.
		PresignPatternObject(ctx context.Context, objectKey string, download bool, downloadName string) (url string, expiresAt time.Time, err error)
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
		SendCampaignTest(ctx context.Context, rep Repository, to, subject, htmlBody, textBody string) error
		CampaignDispatchConfigured() error
		CampaignSendingDisabled() bool
		CampaignEnvelope(campaign *entity.EmailCampaignFull) (string, *string, error)
		CampaignUnsubscribeURL(topic entity.EmailCampaignTopic, email string) (string, error)
		// CampaignFooterStrings resolves the localized campaign-footer labels for a recipient's
		// language (from the transactional catalog) so campaign emails aren't hardcoded English.
		CampaignFooterStrings(languageID int, langs []entity.Language) entity.EmailFooterStrings
		BuildCampaignSendRequest(to string, snapshot entity.EmailCampaignRenderSnapshot, htmlBody, textBody, unsubscribeURL string) (resend.SendEmailRequest, error)
		SendCampaignBatch(ctx context.Context, requests []resend.SendEmailRequest, idempotencyKey string, beforePost func() error) ([]string, error)
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
		PostEmailsBatch(ctx context.Context, body resend.PostEmailsBatchJSONRequestBody, reqEditors ...resend.RequestEditorFn) (*http.Response, error)
	}

	// Translator localizes short marketing/UI strings between locales via an LLM (OpenRouter).
	// Backs the admin auto-translate-campaign action. Enabled reports whether it is configured;
	// Translate returns a same-length, same-order slice, degrading a broken-markup item to source.
	Translator interface {
		Enabled() bool
		Translate(ctx context.Context, sourceLocale, targetLocale string, items []string) ([]string, error)
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
