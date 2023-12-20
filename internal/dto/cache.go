package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	pb_decimal "google.golang.org/genproto/googleapis/type/decimal"
)

type Dict struct {
	Categories       []entity.Category
	Measurements     []entity.MeasurementName
	OrderStatuses    []entity.OrderStatus
	PaymentMethods   []entity.PaymentMethod
	Promos           []entity.PromoCode
	ShipmentCarriers []entity.ShipmentCarrier
	Sizes            []entity.Size
	Genders          []pb_common.Genders
	SortFactors      []pb_common.SortFactors
	OrderFactors     []pb_common.OrderFactors
}

var (
	genders []*pb_common.Genders = []*pb_common.Genders{
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

	sortFactors []*pb_common.SortFactors = []*pb_common.SortFactors{
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

	orderFactors []*pb_common.OrderFactors = []*pb_common.OrderFactors{
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

var (
	categoryEntityPbMap = map[entity.CategoryEnum]pb_common.CategoryEnum{
		entity.TShirt:    pb_common.CategoryEnum_CATEGORY_ENUM_T_SHIRT,
		entity.Jeans:     pb_common.CategoryEnum_CATEGORY_ENUM_JEANS,
		entity.Dress:     pb_common.CategoryEnum_CATEGORY_ENUM_DRESS,
		entity.Jacket:    pb_common.CategoryEnum_CATEGORY_ENUM_JACKET,
		entity.Sweater:   pb_common.CategoryEnum_CATEGORY_ENUM_SWEATER,
		entity.Pant:      pb_common.CategoryEnum_CATEGORY_ENUM_PANT,
		entity.Skirt:     pb_common.CategoryEnum_CATEGORY_ENUM_SKIRT,
		entity.Short:     pb_common.CategoryEnum_CATEGORY_ENUM_SHORT,
		entity.Blazer:    pb_common.CategoryEnum_CATEGORY_ENUM_BLAZER,
		entity.Coat:      pb_common.CategoryEnum_CATEGORY_ENUM_COAT,
		entity.Socks:     pb_common.CategoryEnum_CATEGORY_ENUM_SOCKS,
		entity.Underwear: pb_common.CategoryEnum_CATEGORY_ENUM_UNDERWEAR,
		entity.Bra:       pb_common.CategoryEnum_CATEGORY_ENUM_BRA,
		entity.Hat:       pb_common.CategoryEnum_CATEGORY_ENUM_HAT,
		entity.Scarf:     pb_common.CategoryEnum_CATEGORY_ENUM_SCARF,
		entity.Gloves:    pb_common.CategoryEnum_CATEGORY_ENUM_GLOVES,
		entity.Shoes:     pb_common.CategoryEnum_CATEGORY_ENUM_SHOES,
		entity.Belt:      pb_common.CategoryEnum_CATEGORY_ENUM_BELT,
		entity.Other:     pb_common.CategoryEnum_CATEGORY_ENUM_OTHER,
	}

	categoryPbEntityMap = map[pb_common.CategoryEnum]entity.CategoryEnum{
		pb_common.CategoryEnum_CATEGORY_ENUM_T_SHIRT:   entity.TShirt,
		pb_common.CategoryEnum_CATEGORY_ENUM_JEANS:     entity.Jeans,
		pb_common.CategoryEnum_CATEGORY_ENUM_DRESS:     entity.Dress,
		pb_common.CategoryEnum_CATEGORY_ENUM_JACKET:    entity.Jacket,
		pb_common.CategoryEnum_CATEGORY_ENUM_SWEATER:   entity.Sweater,
		pb_common.CategoryEnum_CATEGORY_ENUM_PANT:      entity.Pant,
		pb_common.CategoryEnum_CATEGORY_ENUM_SKIRT:     entity.Skirt,
		pb_common.CategoryEnum_CATEGORY_ENUM_SHORT:     entity.Short,
		pb_common.CategoryEnum_CATEGORY_ENUM_BLAZER:    entity.Blazer,
		pb_common.CategoryEnum_CATEGORY_ENUM_COAT:      entity.Coat,
		pb_common.CategoryEnum_CATEGORY_ENUM_SOCKS:     entity.Socks,
		pb_common.CategoryEnum_CATEGORY_ENUM_UNDERWEAR: entity.Underwear,
		pb_common.CategoryEnum_CATEGORY_ENUM_BRA:       entity.Bra,
		pb_common.CategoryEnum_CATEGORY_ENUM_HAT:       entity.Hat,
		pb_common.CategoryEnum_CATEGORY_ENUM_SCARF:     entity.Scarf,
		pb_common.CategoryEnum_CATEGORY_ENUM_GLOVES:    entity.Gloves,
		pb_common.CategoryEnum_CATEGORY_ENUM_SHOES:     entity.Shoes,
		pb_common.CategoryEnum_CATEGORY_ENUM_BELT:      entity.Belt,
		pb_common.CategoryEnum_CATEGORY_ENUM_OTHER:     entity.Other,
	}

	measurementEntityPbMap = map[entity.MeasurementNameEnum]pb_common.MeasurementNameEnum{
		entity.Waist:     pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WAIST,
		entity.Inseam:    pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_INSEAM,
		entity.Length:    pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_LENGTH,
		entity.Rise:      pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_RISE,
		entity.Hips:      pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HIPS,
		entity.Shoulders: pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SHOULDERS,
		entity.Bust:      pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_BUST,
		entity.Sleeve:    pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SLEEVE,
		entity.Width:     pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WIDTH,
		entity.Height:    pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HEIGHT,
	}

	measurementPbEntityMap = map[pb_common.MeasurementNameEnum]entity.MeasurementNameEnum{
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WAIST:     entity.Waist,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_INSEAM:    entity.Inseam,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_LENGTH:    entity.Length,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_RISE:      entity.Rise,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HIPS:      entity.Hips,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SHOULDERS: entity.Shoulders,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_BUST:      entity.Bust,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_SLEEVE:    entity.Sleeve,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_WIDTH:     entity.Width,
		pb_common.MeasurementNameEnum_MEASUREMENT_NAME_ENUM_HEIGHT:    entity.Height,
	}

	orderStatusEntityPbMap = map[entity.OrderStatusName]pb_common.OrderStatusEnum{
		entity.Placed:    pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED,
		entity.Confirmed: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED,
		entity.Shipped:   pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED,
		entity.Delivered: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED,
		entity.Cancelled: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED,
		entity.Refunded:  pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED,
	}

	orderStatusPbEntityMap = map[pb_common.OrderStatusEnum]entity.OrderStatusName{
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED:    entity.Placed,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED: entity.Confirmed,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED:   entity.Shipped,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED: entity.Delivered,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED: entity.Cancelled,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED:  entity.Refunded,
	}

	paymentMethodEntityPbMap = map[entity.PaymentMethodName]pb_common.PaymentMethodNameEnum{
		entity.Card: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD,
		entity.Eth:  pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH,
		entity.Usdc: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDC,
		entity.Usdt: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT,
	}

	paymentMethodPbEntityMap = map[pb_common.PaymentMethodNameEnum]entity.PaymentMethodName{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD: entity.Card,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH:  entity.Eth,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDC: entity.Usdc,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT: entity.Usdt,
	}

	sizeEntityPbMap = map[entity.SizeEnum]pb_common.SizeEnum{
		entity.XXS: pb_common.SizeEnum_SIZE_ENUM_XXS,
		entity.XS:  pb_common.SizeEnum_SIZE_ENUM_XS,
		entity.S:   pb_common.SizeEnum_SIZE_ENUM_S,
		entity.M:   pb_common.SizeEnum_SIZE_ENUM_M,
		entity.L:   pb_common.SizeEnum_SIZE_ENUM_L,
		entity.XL:  pb_common.SizeEnum_SIZE_ENUM_XL,
		entity.XXL: pb_common.SizeEnum_SIZE_ENUM_XXL,
		entity.OS:  pb_common.SizeEnum_SIZE_ENUM_OS,
	}

	sizePbEntityMap = map[pb_common.SizeEnum]entity.SizeEnum{
		pb_common.SizeEnum_SIZE_ENUM_XXS: entity.XXS,
		pb_common.SizeEnum_SIZE_ENUM_XS:  entity.XS,
		pb_common.SizeEnum_SIZE_ENUM_S:   entity.S,
		pb_common.SizeEnum_SIZE_ENUM_M:   entity.M,
		pb_common.SizeEnum_SIZE_ENUM_L:   entity.L,
		pb_common.SizeEnum_SIZE_ENUM_XL:  entity.XL,
		pb_common.SizeEnum_SIZE_ENUM_XXL: entity.XXL,
		pb_common.SizeEnum_SIZE_ENUM_OS:  entity.OS,
	}
)

func ConvertPbToEntityCategory(c pb_common.CategoryEnum) (entity.CategoryEnum, bool) {
	g, ok := categoryPbEntityMap[c]
	if !ok {
		return entity.CategoryEnum(""), false
	}
	return g, true
}

func ConvertEntityToPbCategory(c entity.CategoryEnum) (pb_common.CategoryEnum, bool) {
	g, ok := categoryEntityPbMap[c]
	if !ok {
		return pb_common.CategoryEnum(0), false
	}
	return g, true
}

func ConvertPbToEntityMeasurement(m pb_common.MeasurementNameEnum) (entity.MeasurementNameEnum, bool) {
	g, ok := measurementPbEntityMap[m]
	if !ok {
		return entity.MeasurementNameEnum(""), false
	}
	return g, true
}

func ConvertEntityToPbMeasurement(m entity.MeasurementNameEnum) (pb_common.MeasurementNameEnum, bool) {
	g, ok := measurementEntityPbMap[m]
	if !ok {
		return pb_common.MeasurementNameEnum(0), false
	}
	return g, true
}

func ConvertPbToEntityOrderStatus(o pb_common.OrderStatusEnum) (entity.OrderStatusName, bool) {
	g, ok := orderStatusPbEntityMap[o]
	if !ok {
		return entity.OrderStatusName(""), false
	}
	return g, true
}

func ConvertEntityToPbOrderStatus(o entity.OrderStatusName) (pb_common.OrderStatusEnum, bool) {
	g, ok := orderStatusEntityPbMap[o]
	if !ok {
		return pb_common.OrderStatusEnum(0), false
	}
	return g, true
}

func ConvertPbToEntityPaymentMethod(p pb_common.PaymentMethodNameEnum) (entity.PaymentMethodName, bool) {
	g, ok := paymentMethodPbEntityMap[p]
	if !ok {
		return entity.PaymentMethodName(""), false
	}
	return g, true
}

func ConvertEntityToPbPaymentMethod(p entity.PaymentMethodName) (pb_common.PaymentMethodNameEnum, bool) {
	g, ok := paymentMethodEntityPbMap[p]
	if !ok {
		return pb_common.PaymentMethodNameEnum(0), false
	}
	return g, true
}

func ConvertPbToEntitySize(s pb_common.SizeEnum) (entity.SizeEnum, bool) {
	g, ok := sizePbEntityMap[s]
	if !ok {
		return entity.SizeEnum(""), false
	}
	return g, true
}

func ConvertEntityToPbSize(s entity.SizeEnum) (pb_common.SizeEnum, bool) {
	g, ok := sizeEntityPbMap[s]
	if !ok {
		return pb_common.SizeEnum(0), false
	}
	return g, true
}

func ConvertToCommonDictionary(dict *Dict) *pb_common.Dictionary {
	commonDict := &pb_common.Dictionary{}

	for _, c := range dict.Categories {
		name, _ := ConvertEntityToPbCategory(c.Name)
		commonDict.Categories = append(commonDict.Categories,
			&pb_common.Category{
				Id:   int32(c.ID),
				Name: name,
			})
	}

	for _, m := range dict.Measurements {
		name, _ := ConvertEntityToPbMeasurement(m.Name)
		commonDict.Measurements = append(commonDict.Measurements,
			&pb_common.MeasurementName{
				Id:   int32(m.ID),
				Name: name,
			})
	}

	for _, o := range dict.OrderStatuses {
		name, _ := ConvertEntityToPbOrderStatus(o.Name)
		commonDict.OrderStatuses = append(commonDict.OrderStatuses,
			&pb_common.OrderStatus{
				Id:   int32(o.ID),
				Name: name,
			})
	}

	for _, p := range dict.PaymentMethods {
		name, _ := ConvertEntityToPbPaymentMethod(p.Name)
		commonDict.PaymentMethods = append(commonDict.PaymentMethods,
			&pb_common.PaymentMethod{
				Id:   int32(p.ID),
				Name: *pb_common.PaymentMethodNameEnum(name).Enum(),
			})
	}

	for _, s := range dict.ShipmentCarriers {
		commonDict.ShipmentCarriers = append(commonDict.ShipmentCarriers, &pb_common.ShipmentCarrier{
			Id: int32(s.ID),
			ShipmentCarrier: &pb_common.ShipmentCarrierInsert{
				Carrier: s.Carrier,
				Price: &pb_decimal.Decimal{
					Value: s.Price.String(),
				},
				Allowed: s.Allowed,
			},
		})
	}

	for _, sz := range dict.Sizes {
		name, _ := ConvertEntityToPbSize(sz.Name)
		commonDict.Sizes = append(commonDict.Sizes,
			&pb_common.Size{
				Id:   int32(sz.ID),
				Name: *pb_common.SizeEnum(name).Enum(),
			})
	}

	commonDict.Genders = genders
	commonDict.SortFactors = sortFactors
	commonDict.OrderFactors = orderFactors

	return commonDict
}
