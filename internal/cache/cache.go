package cache

import (
	"context"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

type Category struct {
	Category entity.Category
	PB       pb_common.CategoryEnum
}

type Status struct {
	Status entity.OrderStatus
	PB     pb_common.OrderStatusEnum
}

type Measurement struct {
	Measurement entity.MeasurementName
	PB          pb_common.MeasurementNameEnum
}

type PaymentMethod struct {
	Method entity.PaymentMethod
	PB     pb_common.PaymentMethodNameEnum
}

type Size struct {
	Size entity.Size
	PB   pb_common.SizeEnum
}

var (

	// Categories
	CategoryTShirt    = Category{Category: entity.Category{Name: entity.TShirt}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_T_SHIRT}
	CategoryJeans     = Category{Category: entity.Category{Name: entity.Jeans}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_JEANS}
	CategoryDress     = Category{Category: entity.Category{Name: entity.Dress}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_DRESS}
	CategoryJacket    = Category{Category: entity.Category{Name: entity.Jacket}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_JACKET}
	CategorySweater   = Category{Category: entity.Category{Name: entity.Sweater}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SWEATER}
	CategoryPant      = Category{Category: entity.Category{Name: entity.Pant}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_PANT}
	CategorySkirt     = Category{Category: entity.Category{Name: entity.Skirt}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SKIRT}
	CategoryShort     = Category{Category: entity.Category{Name: entity.Short}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SHORT}
	CategoryBlazer    = Category{Category: entity.Category{Name: entity.Blazer}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_BLAZER}
	CategoryCoat      = Category{Category: entity.Category{Name: entity.Coat}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_COAT}
	CategorySocks     = Category{Category: entity.Category{Name: entity.Socks}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SOCKS}
	CategoryUnderwear = Category{Category: entity.Category{Name: entity.Underwear}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_UNDERWEAR}
	CategoryBra       = Category{Category: entity.Category{Name: entity.Bra}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_BRA}
	CategoryHat       = Category{Category: entity.Category{Name: entity.Hat}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_HAT}
	CategoryScarf     = Category{Category: entity.Category{Name: entity.Scarf}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SCARF}
	CategoryGloves    = Category{Category: entity.Category{Name: entity.Gloves}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_GLOVES}
	CategoryShoes     = Category{Category: entity.Category{Name: entity.Shoes}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_SHOES}
	CategoryBelt      = Category{Category: entity.Category{Name: entity.Belt}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_BELT}
	CategoryOther     = Category{Category: entity.Category{Name: entity.Other}, PB: pb_common.CategoryEnum_CATEGORY_ENUM_OTHER}

	categories = []*Category{
		&CategoryTShirt,
		&CategoryJeans,
		&CategoryDress,
		&CategoryJacket,
		&CategorySweater,
		&CategoryPant,
		&CategorySkirt,
		&CategoryShort,
		&CategoryBlazer,
		&CategoryCoat,
		&CategorySocks,
		&CategoryUnderwear,
		&CategoryBra,
		&CategoryHat,
		&CategoryScarf,
		&CategoryGloves,
		&CategoryShoes,
		&CategoryBelt,
		&CategoryOther,
	}

	entityCategories = []entity.Category{}

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
	MeasurementWaist     = Measurement{Measurement: entity.MeasurementName{Name: entity.Waist}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WAIST}
	MeasurementInseam    = Measurement{Measurement: entity.MeasurementName{Name: entity.Inseam}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_INSEAM}
	MeasurementLength    = Measurement{Measurement: entity.MeasurementName{Name: entity.Length}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_LENGTH}
	MeasurementRise      = Measurement{Measurement: entity.MeasurementName{Name: entity.Rise}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_RISE}
	MeasurementHips      = Measurement{Measurement: entity.MeasurementName{Name: entity.Hips}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HIPS}
	MeasurementShoulders = Measurement{Measurement: entity.MeasurementName{Name: entity.Shoulders}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SHOULDERS}
	MeasurementBust      = Measurement{Measurement: entity.MeasurementName{Name: entity.Bust}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_BUST}
	MeasurementSleeve    = Measurement{Measurement: entity.MeasurementName{Name: entity.Sleeve}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SLEEVE}
	MeasurementWidth     = Measurement{Measurement: entity.MeasurementName{Name: entity.Width}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WIDTH}
	MeasurementHeight    = Measurement{Measurement: entity.MeasurementName{Name: entity.Height}, PB: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HEIGHT}

	measurements = []*Measurement{
		&MeasurementWaist,
		&MeasurementInseam,
		&MeasurementLength,
		&MeasurementRise,
		&MeasurementHips,
		&MeasurementShoulders,
		&MeasurementBust,
		&MeasurementSleeve,
		&MeasurementWidth,
		&MeasurementHeight,
	}

	entityMeasurements = []entity.MeasurementName{}

	// PaymentMethods
	PaymentMethodCard = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.CARD,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD}
	PaymentMethodEth = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.ETH,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH}
	PaymentMethodUsdtTron = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.USDT_TRON,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON}
	PaymentMethodUsdtTronTest = PaymentMethod{Method: entity.PaymentMethod{
		Name: entity.USDT_TRON_TEST,
	}, PB: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA}

	paymentMethods = []*PaymentMethod{
		&PaymentMethodCard,
		&PaymentMethodEth,
		&PaymentMethodUsdtTron,
		&PaymentMethodUsdtTronTest,
	}

	entityPaymentMethods = []entity.PaymentMethod{}

	paymentMethodsById = map[int]*PaymentMethod{}

	paymentMethodsByName = map[entity.PaymentMethodName]*PaymentMethod{
		entity.CARD:           &PaymentMethodCard,
		entity.ETH:            &PaymentMethodEth,
		entity.USDT_TRON:      &PaymentMethodUsdtTron,
		entity.USDT_TRON_TEST: &PaymentMethodUsdtTronTest,
	}

	// Sizes
	SizeXXS = Size{Size: entity.Size{Name: entity.XXS}, PB: pb_common.SizeEnum_SIZE_ENUM_XXS}
	SizeXS  = Size{Size: entity.Size{Name: entity.XS}, PB: pb_common.SizeEnum_SIZE_ENUM_XS}
	SizeS   = Size{Size: entity.Size{Name: entity.S}, PB: pb_common.SizeEnum_SIZE_ENUM_S}
	SizeM   = Size{Size: entity.Size{Name: entity.M}, PB: pb_common.SizeEnum_SIZE_ENUM_M}
	SizeL   = Size{Size: entity.Size{Name: entity.L}, PB: pb_common.SizeEnum_SIZE_ENUM_L}
	SizeXL  = Size{Size: entity.Size{Name: entity.XL}, PB: pb_common.SizeEnum_SIZE_ENUM_XL}
	SizeXXL = Size{Size: entity.Size{Name: entity.XXL}, PB: pb_common.SizeEnum_SIZE_ENUM_XXL}
	SizeOS  = Size{Size: entity.Size{Name: entity.OS}, PB: pb_common.SizeEnum_SIZE_ENUM_OS}

	sizes = []*Size{
		&SizeXXS,
		&SizeXS,
		&SizeS,
		&SizeM,
		&SizeL,
		&SizeXL,
		&SizeXXL,
		&SizeOS,
	}
	sizeById = map[int]*Size{}

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

	for _, c := range dInfo.Categories {
		for _, cat := range categories {
			if c.Name == cat.Category.Name {
				entityCategories = append(entityCategories, c)
				cat.Category.Id = c.Id
			}
		}
	}

	for _, mn := range dInfo.Measurements {
		for _, m := range measurements {
			if mn.Name == m.Measurement.Name {
				entityMeasurements = append(entityMeasurements, mn)
				m.Measurement.Id = mn.Id
			}
		}
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
				entityPaymentMethods = append(entityPaymentMethods, pm)
				paymentMethodsById[pm.Id] = p
			}
		}
	}

	for _, s := range dInfo.Sizes {
		for _, size := range sizes {
			if s.Name == size.Size.Name {
				entitySizes = append(entitySizes, s)
				size.Size.Id = s.Id
				sizeById[s.Id] = size
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
	for _, v := range measurements {
		if v.Measurement.Id == 0 {
			return fmt.Errorf("measurement %s not found", v.Measurement.Name)
		}
	}
	for _, v := range categories {
		if v.Category.Id == 0 {
			return fmt.Errorf("category %s not found", v.Category.Name)
		}
	}
	for _, v := range paymentMethods {
		if v.Method.Id == 0 {
			return fmt.Errorf("payment method %s not found", v.Method.Name)
		}
	}
	for _, v := range sizes {
		if v.Size.Id == 0 {
			return fmt.Errorf("size %s not found", v.Size.Name)
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

func GetSizeById(id int) (Size, bool) {
	s, ok := sizeById[id]
	return *s, ok
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
