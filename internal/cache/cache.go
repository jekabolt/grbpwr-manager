package cache

import (
	"context"
	"fmt"
	"sync"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

type Status struct {
	Status entity.OrderStatus
	PB     pb_common.OrderStatusEnum
}

type Measurement struct {
	Measurement entity.MeasurementName
	PB          string
}

type PaymentMethod struct {
	Method entity.PaymentMethod
	PB     pb_common.PaymentMethodNameEnum
}

var (

	// Statuses
	OrderStatusPlaced            = Status{Status: entity.OrderStatus{Name: entity.Placed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED}
	OrderStatusAwaitingPayment   = Status{Status: entity.OrderStatus{Name: entity.AwaitingPayment}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT}
	OrderStatusConfirmed         = Status{Status: entity.OrderStatus{Name: entity.Confirmed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED}
	OrderStatusShipped           = Status{Status: entity.OrderStatus{Name: entity.Shipped}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED}
	OrderStatusDelivered         = Status{Status: entity.OrderStatus{Name: entity.Delivered}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED}
	OrderStatusCancelled         = Status{Status: entity.OrderStatus{Name: entity.Cancelled}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED}
	OrderStatusPendingReturn     = Status{Status: entity.OrderStatus{Name: entity.PendingReturn}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PENDING_RETURN}
	OrderStatusRefundInProgress  = Status{Status: entity.OrderStatus{Name: entity.RefundInProgress}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUND_IN_PROGRESS}
	OrderStatusRefunded          = Status{Status: entity.OrderStatus{Name: entity.Refunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED}
	OrderStatusPartiallyRefunded = Status{Status: entity.OrderStatus{Name: entity.PartiallyRefunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PARTIALLY_REFUNDED}

	orderStatuses = []*Status{
		&OrderStatusPlaced,
		&OrderStatusAwaitingPayment,
		&OrderStatusConfirmed,
		&OrderStatusShipped,
		&OrderStatusDelivered,
		&OrderStatusCancelled,
		&OrderStatusPendingReturn,
		&OrderStatusRefundInProgress,
		&OrderStatusRefunded,
		&OrderStatusPartiallyRefunded,
	}

	entityOrderStatuses = []entity.OrderStatus{}
	orderStatusesById   = map[int]*Status{}
	orderStatusesByName = map[entity.OrderStatusName]*Status{}

	// Measurements

	entityCategories = []entity.Category{}
	categoryById     = map[int]entity.Category{}

	// CategorySizeSystems: category -> permitted size-system(s) mapping (S10/WS5, migration 0175).
	entityCategorySizeSystems = []entity.CategorySizeSystem{}

	entityMeasurements = []entity.MeasurementName{}

	// PaymentMethods
	PaymentMethodCard = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD}
	PaymentMethodCardTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST}
	PaymentMethodBankInvoice = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.BANK_INVOICE,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE}
	PaymentMethodCash = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CASH,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CASH}

	paymentMethods = []*PaymentMethod{
		&PaymentMethodCard,
		&PaymentMethodCardTest,
		&PaymentMethodBankInvoice,
		&PaymentMethodCash,
	}

	entityPaymentMethods = []entity.PaymentMethod{}

	paymentMethodsById = map[int]*PaymentMethod{}

	paymentMethodsByName = map[entity.PaymentMethodName]*PaymentMethod{
		entity.CARD:         &PaymentMethodCard,
		entity.CARD_TEST:    &PaymentMethodCardTest,
		entity.BANK_INVOICE: &PaymentMethodBankInvoice,
		entity.CASH:         &PaymentMethodCash,
	}

	paymentMethodIdByPbId = map[pb_common.PaymentMethodNameEnum]int{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:         PaymentMethodCard.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST:    PaymentMethodCardTest.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_BANK_INVOICE: PaymentMethodBankInvoice.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CASH:         PaymentMethodCash.Method.Id,
	}

	sizeById = map[int]entity.Size{}

	entitySizes = []entity.Size{}

	// Colour dictionary (task 01). Codes are the only accepted identity; names are display data.
	entityColors = []entity.Color{}
	colorByCode  = map[string]entity.Color{}

	// Fibre dictionary (S17/P0.4): the controlled vocabulary material composition references by code.
	entityFibers = []entity.Fiber{}

	entityCollections = []entity.Collection{}

	entityProductTags = []string{}

	// Tag dictionary (R9): the controlled merchandising tags sourced from the `tag` table, distinct
	// from the usage-derived entityProductTags.
	entityTags = []entity.TagDict{}

	entityLanguages = []entity.Language{}

	promoCodes                    = make(map[string]entity.PromoCode)
	promoCodesMu                  sync.RWMutex
	shipmentCarriersById          = make(map[int]entity.ShipmentCarrier)
	shipmentCarriersMu            sync.RWMutex
	entityShipmentCarriers        = []entity.ShipmentCarrier{}
	hero                          = &entity.HeroFullWithTranslations{}
	maxOrderItems                 = 3
	siteEnabled                   = true
	defaultCurrency               = "EUR"
	bigMenu                       = false
	backgroundHeroColor           string
	backgroundHeroColorMu         sync.RWMutex
	announce                      = &entity.AnnounceWithTranslations{}
	orderExpirationSeconds        = 0 // 0 = use payment handler default
	complimentaryShippingPrices   = make(map[string]decimal.Decimal)
	complimentaryShippingPricesMu sync.RWMutex
	paymentIsProd                 = true // true = prod Stripe (CARD), false = test Stripe (CARD_TEST)
	paymentIsProdMu               sync.RWMutex

	// cacheMu guards the runtime-mutable dictionary, settings and payment-method
	// state below that is otherwise unsynchronized: the dictionary slices/maps
	// (entityCategories, entitySizes, entityCollections, sizeById), the payment
	// method maps/slice, the hero/announce pointers, and the scalar settings
	// (maxOrderItems, siteEnabled, defaultCurrency, bigMenu, orderExpirationSeconds).
	// These are written by admin handlers while read by storefront handlers, so
	// without this lock the maps can trigger a fatal concurrent map read/write.
	cacheMu sync.RWMutex
)

var (
	genders []pb_common.Genders = []pb_common.Genders{
		{
			Name: string(entity.Male),
			Id:   pb_common.GenderEnum_GENDER_ENUM_MALE,
		},
		{
			Name: string(entity.Female),
			Id:   pb_common.GenderEnum_GENDER_ENUM_FEMALE,
		},
		{
			Name: string(entity.Unisex),
			Id:   pb_common.GenderEnum_GENDER_ENUM_UNISEX,
		},
	}

	sortFactors []pb_common.SortFactors = []pb_common.SortFactors{
		{
			Name: string(entity.CreatedAt),
			Id:   pb_common.SortFactor_SORT_FACTOR_CREATED_AT,
		},

		{
			Name: string(entity.UpdatedAt),
			Id:   pb_common.SortFactor_SORT_FACTOR_UPDATED_AT,
		},

		{
			Name: string(entity.Name),
			Id:   pb_common.SortFactor_SORT_FACTOR_NAME,
		},

		{
			Name: string(entity.Price),
			Id:   pb_common.SortFactor_SORT_FACTOR_PRICE,
		},
	}

	orderFactors []pb_common.OrderFactors = []pb_common.OrderFactors{
		{
			Name: string(entity.Ascending),
			Id:   pb_common.OrderFactor_ORDER_FACTOR_ASC,
		},
		{
			Name: string(entity.Descending),
			Id:   pb_common.OrderFactor_ORDER_FACTOR_DESC,
		},
	}
)

func InitConsts(ctx context.Context, dInfo *entity.DictionaryInfo, h *entity.HeroFullWithTranslations) error {

	entityCategories = dInfo.Categories
	entitySizes = dInfo.Sizes
	entityCollections = dInfo.Collections
	entityProductTags = dInfo.ProductTags
	entityTags = dInfo.Tags
	entityMeasurements = dInfo.Measurements
	entityLanguages = dInfo.Languages
	announce = dInfo.Announce
	entityCategorySizeSystems = dInfo.CategorySizeSystems
	entityFibers = dInfo.Fibers

	for _, c := range entityCategories {
		categoryById[c.ID] = c
	}

	backgroundHeroColorMu.Lock()
	backgroundHeroColor = dInfo.BackgroundHeroColor
	backgroundHeroColorMu.Unlock()

	for _, s := range entitySizes {
		sizeById[s.Id] = s
	}

	loadColors(dInfo.Colors)

	for _, os := range dInfo.OrderStatuses {
		for _, s := range orderStatuses {
			if os.Name == s.Status.Name {
				s.Status.Id = os.Id
				orderStatusesById[os.Id] = s
				entityOrderStatuses = append(entityOrderStatuses, os)
				orderStatusesByName[os.Name] = s
			}
		}
	}

	for _, pm := range dInfo.PaymentMethods {
		for _, p := range paymentMethods {
			if pm.Name == p.Method.Name {
				p.Method.Id = pm.Id
				p.Method.Allowed = pm.Allowed
				entityPaymentMethods = append(entityPaymentMethods, pm)
				paymentMethodsById[pm.Id] = p
				paymentMethodIdByPbId[p.PB] = pm.Id
			}
		}
	}

	promoCodesMu.Lock()
	for _, p := range dInfo.Promos {
		promoCodes[p.Code] = p
	}
	promoCodesMu.Unlock()

	shipmentCarriersMu.Lock()
	for _, sc := range dInfo.ShipmentCarriers {
		shipmentCarriersById[sc.Id] = sc
		entityShipmentCarriers = append(entityShipmentCarriers, sc)
	}
	shipmentCarriersMu.Unlock()

	complimentaryShippingPricesMu.Lock()
	complimentaryShippingPrices = dInfo.ComplimentaryShippingPrices
	complimentaryShippingPricesMu.Unlock()

	hero = h

	// check if all consts are initialized
	for _, v := range orderStatuses {
		if v.Status.Id == 0 {
			return fmt.Errorf("order status %s not found", v.Status.Name)
		}
	}

	for _, v := range paymentMethods {
		if v.Method.Id == 0 {
			return fmt.Errorf("payment method %s not found", v.Method.Name)
		}
	}

	return nil
}

func GetOrderStatusById(id int) (Status, bool) {
	st, ok := orderStatusesById[id]
	return *st, ok
}

func GetOrderStatusByName(n entity.OrderStatusName) (Status, bool) {
	st, ok := orderStatusesByName[n]
	return *st, ok
}

// OrderStatusIDsForNetRevenue returns status IDs for orders contributing to net revenue (confirmed, shipped, delivered, partially_refunded).
func OrderStatusIDsForNetRevenue() []int {
	names := []entity.OrderStatusName{entity.Confirmed, entity.Shipped, entity.Delivered, entity.PartiallyRefunded}
	ids := make([]int, 0, len(names))
	for _, n := range names {
		if s, ok := GetOrderStatusByName(n); ok {
			ids = append(ids, s.Status.Id)
		}
	}
	return ids
}

// OrderStatusIDsForRefund returns status IDs for orders with refunded_amount (net revenue set + refunded).
func OrderStatusIDsForRefund() []int {
	names := []entity.OrderStatusName{entity.Confirmed, entity.Shipped, entity.Delivered, entity.Refunded, entity.PartiallyRefunded}
	ids := make([]int, 0, len(names))
	for _, n := range names {
		if s, ok := GetOrderStatusByName(n); ok {
			ids = append(ids, s.Status.Id)
		}
	}
	return ids
}

func UpdateHero(hf *entity.HeroFullWithTranslations) {
	cacheMu.Lock()
	hero = hf
	cacheMu.Unlock()
}

// RefreshDictionary updates categories, sizes, collections (including CountMen/CountWomen),
// product tags and the category size-system mapping (S10/WS5) in the in-memory cache.
// Call after product add/update/delete so counts and tags stay accurate.
func RefreshDictionary(dInfo *entity.DictionaryInfo) {
	if dInfo == nil {
		return
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	entityCategories = dInfo.Categories
	entitySizes = dInfo.Sizes
	entityCollections = dInfo.Collections
	entityProductTags = dInfo.ProductTags
	entityTags = dInfo.Tags
	entityCategorySizeSystems = dInfo.CategorySizeSystems
	entityFibers = dInfo.Fibers
	categoryById = make(map[int]entity.Category, len(entityCategories))
	for _, c := range entityCategories {
		categoryById[c.ID] = c
	}
	sizeById = make(map[int]entity.Size, len(entitySizes))
	for _, s := range entitySizes {
		sizeById[s.Id] = s
	}
	loadColors(dInfo.Colors)
}

// loadColors rebuilds the colour dictionary lookups. Callers hold cacheMu (RefreshDictionary) or
// run during single-threaded init (InitConsts).
func loadColors(colors []entity.Color) {
	entityColors = colors
	colorByCode = make(map[string]entity.Color, len(colors))
	for _, c := range colors {
		colorByCode[c.Code] = c
	}
}

func UpdatePromos(promos []entity.PromoCode) {
	promoCodesMu.Lock()
	promoCodes = make(map[string]entity.PromoCode, len(promos))
	for _, p := range promos {
		promoCodes[p.Code] = p
	}
	promoCodesMu.Unlock()
}

func AddPromo(p entity.PromoCode) {
	promoCodesMu.Lock()
	promoCodes[p.Code] = p
	promoCodesMu.Unlock()
}

func GetPromoById(id int) (entity.PromoCode, bool) {
	promoCodesMu.RLock()
	defer promoCodesMu.RUnlock()
	for _, p := range promoCodes {
		if p.Id == id {
			return p, true
		}
	}
	return entity.PromoCode{}, false
}

func DeletePromo(code string) {
	promoCodesMu.Lock()
	delete(promoCodes, code)
	promoCodesMu.Unlock()
}

func DisablePromo(code string) {
	promoCodesMu.Lock()
	defer promoCodesMu.Unlock()
	p, ok := promoCodes[code]
	if ok {
		p.Allowed = false
		promoCodes[code] = p
	}
}

func GetPromoByCode(code string) (entity.PromoCode, bool) {
	promoCodesMu.RLock()
	defer promoCodesMu.RUnlock()
	p, ok := promoCodes[code]
	return p, ok
}

func UpdateShipmentCarriers(scs []entity.ShipmentCarrier) {
	shipmentCarriersMu.Lock()
	shipmentCarriersById = make(map[int]entity.ShipmentCarrier, len(scs))
	for _, sc := range scs {
		shipmentCarriersById[sc.Id] = sc
	}
	shipmentCarriersMu.Unlock()
}

func GetShipmentCarrierById(id int) (entity.ShipmentCarrier, bool) {
	shipmentCarriersMu.RLock()
	defer shipmentCarriersMu.RUnlock()
	sc, ok := shipmentCarriersById[id]
	return sc, ok
}

func UpdateShipmentCarrierAllowance(carrier string, allowed bool) {
	shipmentCarriersMu.Lock()
	defer shipmentCarriersMu.Unlock()
	for _, sc := range shipmentCarriersById {
		if sc.Carrier == carrier {
			sc.Allowed = allowed
			shipmentCarriersById[sc.Id] = sc
			break
		}
	}
}

// UpdateShipmentCarrierCost is deprecated. Use UpdateShipmentCarriers instead.
func UpdateShipmentCarrierCost(carrier string, price decimal.Decimal) {
	shipmentCarriersMu.Lock()
	defer shipmentCarriersMu.Unlock()
	for _, sc := range shipmentCarriersById {
		if sc.Carrier == carrier {
			// Update all prices to the same value (for backward compatibility)
			for i := range sc.Prices {
				sc.Prices[i].Price = price
			}
			shipmentCarriersById[sc.Id] = sc
		}
	}
}

func GetPaymentMethodById(id int) (PaymentMethod, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	pm, ok := paymentMethodsById[id]
	if !ok {
		return PaymentMethod{}, false
	}
	return *pm, ok
}

func GetPaymentMethodByName(n entity.PaymentMethodName) (PaymentMethod, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	pm, ok := paymentMethodsByName[n]
	if !ok {
		return PaymentMethod{}, false
	}
	return *pm, ok
}

func UpdatePaymentMethodAllowance(method entity.PaymentMethodName, allowed bool) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	pm, ok := paymentMethodsByName[method]
	if ok {
		pm.Method.Allowed = allowed
		paymentMethodsByName[method] = pm
		for _, v := range paymentMethods {
			if v.Method.Name == method {
				v.Method.Allowed = allowed
				paymentMethodsById[v.Method.Id] = v
				break
			}
		}
		paymentMethodsById[pm.Method.Id] = pm

	}
}

func GetSizeById(id int) (entity.Size, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	s, ok := sizeById[id]
	return s, ok
}

// GetCategoryById returns a single category-tree node by id (S10/WS5): used to resolve a style's
// most-specific category into a human-readable label for size-system validation errors.
func GetCategoryById(id int) (entity.Category, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	c, ok := categoryById[id]
	return c, ok
}

// GetCategorySizeSystems returns the category -> permitted size-system(s) mapping (S10/WS5, migration
// 0175): both the admin dictionary (size picker) and server-side size-write validation
// (entity.ResolveSizeSystemPolicy) read this same slice, so they can never drift from each other.
func GetCategorySizeSystems() []entity.CategorySizeSystem {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityCategorySizeSystems
}

// CategoryLabel returns a human-readable name for a style's most specific set category node (type >
// sub-category > top-category, S10/WS5), or "" when the style has no category assigned at all.
func CategoryLabel(path entity.StyleCategoryPath) string {
	id, ok := path.MostSpecificID()
	if !ok {
		return ""
	}
	c, ok := GetCategoryById(id)
	if !ok {
		return ""
	}
	return c.Name
}

func GetHero() *entity.HeroFullWithTranslations {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return hero
}
func GetMaxOrderItems() int {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return maxOrderItems
}

func SetMaxOrderItems(n int) {
	cacheMu.Lock()
	maxOrderItems = n
	cacheMu.Unlock()
}

func SetBigMenu(enabled bool) {
	cacheMu.Lock()
	bigMenu = enabled
	cacheMu.Unlock()
}

// GetBackgroundHeroColor returns the storefront hero background CSS color (may be empty).
func GetBackgroundHeroColor() string {
	backgroundHeroColorMu.RLock()
	defer backgroundHeroColorMu.RUnlock()
	return backgroundHeroColor
}

// SetBackgroundHeroColor updates the in-memory hero background color cache.
func SetBackgroundHeroColor(color string) {
	backgroundHeroColorMu.Lock()
	backgroundHeroColor = color
	backgroundHeroColorMu.Unlock()
}

func SetAnnounce(link string, translations []entity.AnnounceTranslation) {
	cacheMu.Lock()
	announce = &entity.AnnounceWithTranslations{
		Link:         link,
		Translations: translations,
	}
	cacheMu.Unlock()
}

func SetDefaultCurrency(c string) {
	cacheMu.Lock()
	defaultCurrency = c
	cacheMu.Unlock()
}

func SetSiteAvailability(enabled bool) {
	cacheMu.Lock()
	siteEnabled = enabled
	cacheMu.Unlock()
}

func GetSiteAvailability() bool {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return siteEnabled
}

func GetBaseCurrency() string {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return defaultCurrency
}

func GetBigMenu() bool {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return bigMenu
}

func GetAnnounce() *entity.AnnounceWithTranslations {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return announce
}

func GetOrderExpirationSeconds() int {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return orderExpirationSeconds
}

func SetOrderExpirationSeconds(seconds int) {
	cacheMu.Lock()
	orderExpirationSeconds = seconds
	cacheMu.Unlock()
}

func UpdateComplimentaryShippingPrices(prices map[string]decimal.Decimal) {
	complimentaryShippingPricesMu.Lock()
	defer complimentaryShippingPricesMu.Unlock()
	complimentaryShippingPrices = prices
}

func GetComplimentaryShippingPrices() map[string]decimal.Decimal {
	complimentaryShippingPricesMu.RLock()
	defer complimentaryShippingPricesMu.RUnlock()
	result := make(map[string]decimal.Decimal)
	for k, v := range complimentaryShippingPrices {
		result[k] = v
	}
	return result
}

func GetCategories() []entity.Category {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityCategories
}

func GetMeasurements() []entity.MeasurementName {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityMeasurements
}

func GetGenders() []pb_common.Genders {
	return genders
}

func GetSortFactors() []pb_common.SortFactors {
	return sortFactors
}

func GetOrderFactors() []pb_common.OrderFactors {
	return orderFactors
}

func GetOrderStatuses() []entity.OrderStatus {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityOrderStatuses
}

func GetPaymentMethods() []entity.PaymentMethod {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityPaymentMethods
}

func SetPaymentIsProd(isProd bool) {
	paymentIsProdMu.Lock()
	paymentIsProd = isProd
	paymentIsProdMu.Unlock()
}

func GetPaymentIsProd() bool {
	paymentIsProdMu.RLock()
	defer paymentIsProdMu.RUnlock()
	return paymentIsProd
}

// GetPaymentMethodsFilteredByIsProd returns payment methods for checkout: only CARD if isProd, only CARD_TEST if !isProd.
// Respects the Allowed flag for each method.
func GetPaymentMethodsFilteredByIsProd() []entity.PaymentMethod {
	paymentIsProdMu.RLock()
	isProd := paymentIsProd
	paymentIsProdMu.RUnlock()

	var target entity.PaymentMethodName
	if isProd {
		target = entity.CARD
	} else {
		target = entity.CARD_TEST
	}

	cacheMu.RLock()
	defer cacheMu.RUnlock()
	result := make([]entity.PaymentMethod, 0, 1)
	for _, pm := range entityPaymentMethods {
		if pm.Name == target && pm.Allowed {
			result = append(result, pm)
			break
		}
	}
	return result
}

func GetSizes() []entity.Size {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entitySizes
}

func GetCollections() []entity.Collection {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityCollections
}

// GetColors returns the controlled colour dictionary.
func GetColors() []entity.Color {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityColors
}

// GetColorByCode returns the dictionary entry for a 3-char colour code.
func GetColorByCode(code string) (entity.Color, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	c, ok := colorByCode[code]
	return c, ok
}

// GetFibers returns the controlled fibre vocabulary (S17/P0.4).
func GetFibers() []entity.Fiber {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityFibers
}

func GetProductTags() []string {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityProductTags
}

// GetTags returns the controlled merchandising tag dictionary (R9) from the `tag` table — the set an
// admin creates via CreateTag and reuses by code/name. Distinct from GetProductTags (usage-derived).
func GetTags() []entity.TagDict {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return entityTags
}

func GetShipmentCarriers() []entity.ShipmentCarrier {
	shipmentCarriersMu.RLock()
	defer shipmentCarriersMu.RUnlock()
	scs := make([]entity.ShipmentCarrier, 0, len(shipmentCarriersById))
	for _, sc := range shipmentCarriersById {
		scs = append(scs, sc)
	}
	return scs
}

func GetLanguages() []entity.Language {
	return entityLanguages
}

func GetPaymentMethodIdByPbId(pbId pb_common.PaymentMethodNameEnum) int {
	id, ok := paymentMethodIdByPbId[pbId]
	if !ok {
		return 0
	}
	return id
}

// RefreshEntityPaymentMethods updates the exported entityPaymentMethods slice from the current paymentMethodsById map.
func RefreshEntityPaymentMethods() {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	// Build a fresh slice rather than reusing the backing array, so concurrent
	// readers holding a previously returned slice never observe in-place mutation.
	refreshed := make([]entity.PaymentMethod, 0, len(paymentMethodsById))
	for _, pm := range paymentMethodsById {
		refreshed = append(refreshed, pm.Method)
	}
	entityPaymentMethods = refreshed
}
