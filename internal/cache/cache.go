package cache

import (
	"context"
	"fmt"

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
	OrderStatusPlaced          = Status{Status: entity.OrderStatus{Name: entity.Placed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED}
	OrderStatusAwaitingPayment = Status{Status: entity.OrderStatus{Name: entity.AwaitingPayment}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT}
	OrderStatusConfirmed       = Status{Status: entity.OrderStatus{Name: entity.Confirmed}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED}
	OrderStatusShipped         = Status{Status: entity.OrderStatus{Name: entity.Shipped}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED}
	OrderStatusDelivered       = Status{Status: entity.OrderStatus{Name: entity.Delivered}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED}
	OrderStatusCancelled       = Status{Status: entity.OrderStatus{Name: entity.Cancelled}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED}
	OrderStatusRefunded        = Status{Status: entity.OrderStatus{Name: entity.Refunded}, PB: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED}

	orderStatuses = []*Status{
		&OrderStatusPlaced,
		&OrderStatusAwaitingPayment,
		&OrderStatusConfirmed,
		&OrderStatusShipped,
		&OrderStatusDelivered,
		&OrderStatusCancelled,
		&OrderStatusRefunded,
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
	PaymentMethodEth = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.ETH,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH}
	PaymentMethodEthTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.ETH_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH_TEST}
	PaymentMethodUsdtTron = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.USDT_TRON,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON}
	PaymentMethodUsdtTronTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.USDT_TRON_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA}

	paymentMethods = []*PaymentMethod{
		&PaymentMethodCard,
		&PaymentMethodCardTest,
		&PaymentMethodEth,
		&PaymentMethodEthTest,
		&PaymentMethodUsdtTron,
		&PaymentMethodUsdtTronTest,
	}

	entityPaymentMethods = []entity.PaymentMethod{}

	paymentMethodsById = map[int]*PaymentMethod{}

	paymentMethodsByName = map[entity.PaymentMethodName]*PaymentMethod{
		entity.CARD:           &PaymentMethodCard,
		entity.CARD_TEST:      &PaymentMethodCardTest,
		entity.ETH:            &PaymentMethodEth,
		entity.ETH_TEST:       &PaymentMethodEthTest,
		entity.USDT_TRON:      &PaymentMethodUsdtTron,
		entity.USDT_TRON_TEST: &PaymentMethodUsdtTronTest,
	}

	paymentMethodIdByPbId = map[pb_common.PaymentMethodNameEnum]int{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:        PaymentMethodCard.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST:   PaymentMethodCardTest.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH:         PaymentMethodEth.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH_TEST:    PaymentMethodEthTest.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON:   PaymentMethodUsdtTron.Method.Id,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA: PaymentMethodUsdtTronTest.Method.Id,
	}

	sizeById = map[int]entity.Size{}

	entitySizes = []entity.Size{}

	promoCodes             = make(map[string]entity.PromoCode)
	shipmentCarriersById   = make(map[int]entity.ShipmentCarrier)
	entityShipmentCarriers = []entity.ShipmentCarrier{}
	hero                   = &entity.HeroFull{}
	maxOrderItems          = 3
	siteEnabled            = true
	defaultCurrency        = ""
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

func InitConsts(ctx context.Context, dInfo *entity.DictionaryInfo, h *entity.HeroFull) error {

	entityCategories = dInfo.Categories
	entitySizes = dInfo.Sizes
	entityMeasurements = dInfo.Measurements
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

	for _, p := range dInfo.Promos {
		promoCodes[p.Code] = p
	}

	for _, sc := range dInfo.ShipmentCarriers {
		shipmentCarriersById[sc.Id] = sc
		entityShipmentCarriers = append(entityShipmentCarriers, sc)
	}
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
func UpdateHero(hf *entity.HeroFull) {
	hero = hf
}

func UpdatePromos(promos []entity.PromoCode) {
	promoCodes = make(map[string]entity.PromoCode, len(promos))
	for _, p := range promos {
		promoCodes[p.Code] = p
	}
}

func AddPromo(p entity.PromoCode) {
	promoCodes[p.Code] = p
}

func GetPromoById(id int) (entity.PromoCode, bool) {
	for _, p := range promoCodes {
		if p.Id == id {
			return p, true
		}
	}
	return entity.PromoCode{}, false
}

func DeletePromo(code string) {
	delete(promoCodes, code)
}

func DisablePromo(code string) {
	p, ok := promoCodes[code]
	if ok {
		p.Allowed = false
		promoCodes[code] = p
	}
}

func GetPromoByCode(code string) (entity.PromoCode, bool) {
	p, ok := promoCodes[code]
	return p, ok
}

func UpdateShipmentCarriers(scs []entity.ShipmentCarrier) {
	shipmentCarriersById = make(map[int]entity.ShipmentCarrier, len(scs))
	for _, sc := range scs {
		shipmentCarriersById[sc.Id] = sc
	}
}

func GetShipmentCarrierById(id int) (entity.ShipmentCarrier, bool) {
	sc, ok := shipmentCarriersById[id]
	return sc, ok
}

func UpdateShipmentCarrierAllowance(carrier string, allowed bool) {
	for _, sc := range shipmentCarriersById {
		if sc.Carrier == carrier {
			sc.Allowed = allowed
			shipmentCarriersById[sc.Id] = sc
			break
		}
	}
}

func UpdateShipmentCarrierCost(carrier string, price decimal.Decimal) {
	for _, sc := range shipmentCarriersById {
		if sc.Carrier == carrier {
			sc.Price = price
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
	}
}

func GetSizeById(id int) (entity.Size, bool) {
	s, ok := sizeById[id]
	return s, ok
}

func GetHero() *entity.HeroFull {
	return hero
}
func GetMaxOrderItems() int {
	return maxOrderItems
}

func SetMaxOrderItems(n int) {
	maxOrderItems = n
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

func GetShipmentCarriers() []entity.ShipmentCarrier {
	scs := make([]entity.ShipmentCarrier, 0, len(shipmentCarriersById))
	for _, sc := range shipmentCarriersById {
		scs = append(scs, sc)
	}
	return scs
}

func GetPaymentMethodIdByPbId(pbId pb_common.PaymentMethodNameEnum) int {
	id, ok := paymentMethodIdByPbId[pbId]
	if !ok {
		return 0
	}
	return id
}
