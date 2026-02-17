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
	OrderStatusPlaced           = Status{Status: entity.OrderStatus{Name: entity.Placed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED}
	OrderStatusAwaitingPayment  = Status{Status: entity.OrderStatus{Name: entity.AwaitingPayment}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT}
	OrderStatusConfirmed        = Status{Status: entity.OrderStatus{Name: entity.Confirmed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED}
	OrderStatusShipped          = Status{Status: entity.OrderStatus{Name: entity.Shipped}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED}
	OrderStatusDelivered        = Status{Status: entity.OrderStatus{Name: entity.Delivered}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED}
	OrderStatusCancelled        = Status{Status: entity.OrderStatus{Name: entity.Cancelled}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED}
	OrderStatusPendingReturn    = Status{Status: entity.OrderStatus{Name: entity.PendingReturn}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PENDING_RETURN}
	OrderStatusRefundInProgress = Status{Status: entity.OrderStatus{Name: entity.RefundInProgress}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUND_IN_PROGRESS}
	OrderStatusRefunded         = Status{Status: entity.OrderStatus{Name: entity.Refunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED}
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

	entityMeasurements = []entity.MeasurementName{}

	// PaymentMethods
	PaymentMethodCard = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD}
	PaymentMethodCardTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST}

	paymentMethods = []*PaymentMethod{
		&PaymentMethodCard,
		&PaymentMethodCardTest,
	}

	entityPaymentMethods = []entity.PaymentMethod{}

	paymentMethodsById = map[int]*PaymentMethod{}

	paymentMethodsByName = map[entity.PaymentMethodName]*PaymentMethod{
		entity.CARD:      &PaymentMethodCard,
		entity.CARD_TEST: &PaymentMethodCardTest,
	}

	paymentMethodIdByPbId = map[pb_common.PaymentMethodNameEnum]int{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:      PaymentMethodCard.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST: PaymentMethodCardTest.Method.Id,
	}

	sizeById = map[int]entity.Size{}

	entitySizes = []entity.Size{}

	entityCollections = []entity.Collection{}

	entityLanguages = []entity.Language{}

	promoCodes             = make(map[string]entity.PromoCode)
	promoCodesMu           sync.RWMutex
	shipmentCarriersById   = make(map[int]entity.ShipmentCarrier)
	shipmentCarriersMu     sync.RWMutex
	entityShipmentCarriers = []entity.ShipmentCarrier{}
	hero                   = &entity.HeroFullWithTranslations{}
	maxOrderItems          = 3
	siteEnabled            = true
	defaultCurrency        = "EUR"
	bigMenu                = false
	announce               = &entity.AnnounceWithTranslations{}
	orderExpirationSeconds = 0 // 0 = use payment handler default
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
	entityMeasurements = dInfo.Measurements
	entityLanguages = dInfo.Languages
	announce = dInfo.Announce

	for _, s := range entitySizes {
		sizeById[s.Id] = s
	}

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
func UpdateHero(hf *entity.HeroFullWithTranslations) {
	hero = hf
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
	pm, ok := paymentMethodsById[id]
	return *pm, ok
}

func GetPaymentMethodByName(n entity.PaymentMethodName) (PaymentMethod, bool) {
	pm, ok := paymentMethodsByName[n]
	return *pm, ok
}

func UpdatePaymentMethodAllowance(method entity.PaymentMethodName, allowed bool) {
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
	s, ok := sizeById[id]
	return s, ok
}

func GetHero() *entity.HeroFullWithTranslations {
	return hero
}
func GetMaxOrderItems() int {
	return maxOrderItems
}

func SetMaxOrderItems(n int) {
	maxOrderItems = n
}

func SetBigMenu(enabled bool) {
	bigMenu = enabled
}

func SetAnnounce(link string, translations []entity.AnnounceTranslation) {
	announce = &entity.AnnounceWithTranslations{
		Link:         link,
		Translations: translations,
	}
}

func SetDefaultCurrency(c string) {
	defaultCurrency = c
}

func SetSiteAvailability(enabled bool) {
	siteEnabled = enabled
}

func GetSiteAvailability() bool {
	return siteEnabled
}

func GetBaseCurrency() string {
	return defaultCurrency
}

func GetBigMenu() bool {
	return bigMenu
}

func GetAnnounce() *entity.AnnounceWithTranslations {
	return announce
}

func GetOrderExpirationSeconds() int {
	return orderExpirationSeconds
}

func SetOrderExpirationSeconds(seconds int) {
	orderExpirationSeconds = seconds
}

func GetCategories() []entity.Category {
	return entityCategories
}

func GetMeasurements() []entity.MeasurementName {
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
	return entityOrderStatuses
}

func GetPaymentMethods() []entity.PaymentMethod {
	return entityPaymentMethods
}

func GetSizes() []entity.Size {
	return entitySizes
}

func GetCollections() []entity.Collection {
	return entityCollections
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
	entityPaymentMethods = entityPaymentMethods[:0]
	for _, pm := range paymentMethodsById {
		entityPaymentMethods = append(entityPaymentMethods, pm.Method)
	}
}
