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
	ShipmentCarriers []entity.ShipmentCarrier
	Sizes            []entity.Size
	Genders          []pb_common.Genders
	SortFactors      []pb_common.SortFactors
	OrderFactors     []pb_common.OrderFactors
	SiteEnabled      bool
	MaxOrderItems    int
	BaseCurrency     string
	BigMenu          bool
	TopCategories    []entity.CategoryCount
	SubCategories    []entity.CategoryCount
}

var (
	orderStatusEntityPbMap = map[entity.OrderStatusName]pb_common.OrderStatusEnum{
		entity.Placed:          pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED,
		entity.AwaitingPayment: pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT,
		entity.Confirmed:       pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED,
		entity.Shipped:         pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED,
		entity.Delivered:       pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED,
		entity.Cancelled:       pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED,
		entity.Refunded:        pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED,
	}

	orderStatusPbEntityMap = map[pb_common.OrderStatusEnum]entity.OrderStatusName{
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_PLACED:           entity.Placed,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_AWAITING_PAYMENT: entity.AwaitingPayment,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CONFIRMED:        entity.Confirmed,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_SHIPPED:          entity.Shipped,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_DELIVERED:        entity.Delivered,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_CANCELLED:        entity.Cancelled,
		pb_common.OrderStatusEnum_ORDER_STATUS_ENUM_REFUNDED:         entity.Refunded,
	}

	paymentMethodEntityPbMap = map[entity.PaymentMethodName]pb_common.PaymentMethodNameEnum{
		entity.CARD:           pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD,
		entity.CARD_TEST:      pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD_TEST,
		entity.ETH:            pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH,
		entity.ETH_TEST:       pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH_TEST,
		entity.USDT_TRON:      pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON,
		entity.USDT_TRON_TEST: pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA,
	}

	paymentMethodPbEntityMap = map[pb_common.PaymentMethodNameEnum]entity.PaymentMethodName{
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_CARD:        entity.CARD,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_ETH:         entity.ETH,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_TRON:   entity.USDT_TRON,
		pb_common.PaymentMethodNameEnum_PAYMENT_METHOD_NAME_ENUM_USDT_SHASTA: entity.USDT_TRON_TEST,
	}
)

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

func ConvertToCommonDictionary(dict Dict) *pb_common.Dictionary {
	commonDict := &pb_common.Dictionary{}

	commonDict.Categories = CategorySliceToProto(dict.Categories)

	for _, m := range dict.Measurements {
		commonDict.Measurements = append(commonDict.Measurements,
			&pb_common.MeasurementName{
				Id:   int32(m.Id),
				Name: m.Name,
			})
	}

	for _, o := range dict.OrderStatuses {
		name, _ := ConvertEntityToPbOrderStatus(o.Name)
		commonDict.OrderStatuses = append(commonDict.OrderStatuses,
			&pb_common.OrderStatus{
				Id:   int32(o.Id),
				Name: name,
			})
	}

	for _, p := range dict.PaymentMethods {
		name, _ := ConvertEntityToPbPaymentMethod(p.Name)
		commonDict.PaymentMethods = append(commonDict.PaymentMethods,
			&pb_common.PaymentMethod{
				Id:      int32(p.Id),
				Name:    *pb_common.PaymentMethodNameEnum(name).Enum(),
				Allowed: p.Allowed,
			})
	}

	for _, s := range dict.ShipmentCarriers {
		commonDict.ShipmentCarriers = append(commonDict.ShipmentCarriers, &pb_common.ShipmentCarrier{
			Id: int32(s.Id),
			ShipmentCarrier: &pb_common.ShipmentCarrierInsert{
				Carrier: s.Carrier,
				Price: &pb_decimal.Decimal{
					Value: s.Price.String(),
				},
				Allowed:     s.Allowed,
				Description: s.Description,
			},
		})
	}

	for _, sz := range dict.Sizes {
		commonDict.Sizes = append(commonDict.Sizes,
			&pb_common.Size{
				Id:   int32(sz.Id),
				Name: sz.Name,
			})
	}
	commonDict.SiteEnabled = dict.SiteEnabled
	commonDict.MaxOrderItems = int32(dict.MaxOrderItems)
	commonDict.BaseCurrency = dict.BaseCurrency
	commonDict.BigMenu = dict.BigMenu

	for _, tc := range dict.TopCategories {
		commonDict.TopCategories = append(commonDict.TopCategories, &pb_common.CategoryCount{
			CategoryId:   int32(tc.CategoryId),
			CategoryName: tc.CategoryName,
			Count:        int32(tc.Count),
		})
	}

	for _, sc := range dict.SubCategories {
		commonDict.SubCategories = append(commonDict.SubCategories, &pb_common.CategoryCount{
			CategoryId:   int32(sc.CategoryId),
			CategoryName: sc.CategoryName,
			Count:        int32(sc.Count),
		})
	}
	return commonDict
}

func ConvertEntityToPbCategory(c *entity.Category) *pb_common.Category {
	if c == nil {
		return nil
	}

	proto := &pb_common.Category{
		Id:      int32(c.ID),
		Name:    c.Name,
		LevelId: int32(c.LevelID),
		Level:   c.Level,
	}

	// Handle optional parent ID
	if c.ParentID != nil {
		parentID := int32(*c.ParentID)
		proto.ParentId = parentID
	}

	return proto
}

// FromProto converts a protobuf message to a Category model
func CategoryFromProto(proto *pb_common.Category) *entity.Category {
	if proto == nil {
		return nil
	}

	category := &entity.Category{
		ID:      int(proto.Id),
		Name:    proto.Name,
		LevelID: int(proto.LevelId),
		Level:   proto.Level,
	}

	// Handle optional parent ID
	if proto.ParentId != 0 {
		parentID := int(proto.ParentId)
		category.ParentID = &parentID
	}

	return category
}

// Helper functions for working with slices

// CategorySliceToProto converts a slice of Categories to protobuf messages
func CategorySliceToProto(categories []entity.Category) []*pb_common.Category {
	result := make([]*pb_common.Category, len(categories))
	for i, category := range categories {
		categoryCopy := category // Create a copy to avoid issues with loop variable
		result[i] = ConvertEntityToPbCategory(&categoryCopy)
	}
	return result
}

// CategorySliceFromProto converts a slice of protobuf messages to Categories
func CategorySliceFromProto(protos []*pb_common.Category) []entity.Category {
	result := make([]entity.Category, len(protos))
	for i, proto := range protos {
		if category := CategoryFromProto(proto); category != nil {
			result[i] = *category
		}
	}
	return result
}
