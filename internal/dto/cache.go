package dto

import (
	"strings"

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
}

func ConvertToCommonDictionary(dict *Dict) *pb_common.Dictionary {
	commonDict := &pb_common.Dictionary{}

	for _, c := range dict.Categories {
		name := pb_common.CategoryEnum_value[strings.ToUpper(strings.ReplaceAll(string(c.Name), "-", "_"))]
		commonDict.Categories = append(commonDict.Categories,
			&pb_common.Category{
				Id:   int32(c.ID),
				Name: *pb_common.CategoryEnum(name).Enum(),
			})
	}

	for _, m := range dict.Measurements {
		name := pb_common.MeasurementNameEnum_value[strings.ToUpper(strings.ReplaceAll(string(m.Name), "-", "_"))]
		commonDict.Measurements = append(commonDict.Measurements,
			&pb_common.MeasurementName{
				Id:   int32(m.ID),
				Name: *pb_common.MeasurementNameEnum(name).Enum(),
			})
	}

	for _, o := range dict.OrderStatuses {
		name := pb_common.OrderStatusEnum_value[strings.ToUpper(strings.ReplaceAll(string(o.Name), "-", "_"))]
		commonDict.OrderStatuses = append(commonDict.OrderStatuses,
			&pb_common.OrderStatus{
				Id:   int32(o.ID),
				Name: *pb_common.OrderStatusEnum(name).Enum(),
			})
	}

	for _, p := range dict.PaymentMethods {
		name := pb_common.PaymentMethodNameEnum_value[strings.ToUpper(strings.ReplaceAll(string(p.Name), "-", "_"))]
		commonDict.PaymentMethods = append(commonDict.PaymentMethods,
			&pb_common.PaymentMethod{
				Id:   int32(p.ID),
				Name: *pb_common.PaymentMethodNameEnum(name).Enum(),
			})
	}

	for _, s := range dict.ShipmentCarriers {

		commonDict.ShipmentCarriers = append(commonDict.ShipmentCarriers, &pb_common.ShipmentCarrier{
			Id: int32(s.ID),
			Insert: &pb_common.ShipmentCarrierInsert{
				Carrier: s.Carrier,
				Price: &pb_decimal.Decimal{
					Value: s.Price.String(),
				},
				Allowed: s.Allowed,
			},
		})
	}

	for _, sz := range dict.Sizes {
		name := pb_common.SizeEnum_value[strings.ToUpper(strings.ReplaceAll(string(sz.Name), "-", "_"))]
		commonDict.Sizes = append(commonDict.Sizes,
			&pb_common.Size{
				Id:   int32(sz.ID),
				Name: *pb_common.SizeEnum(name).Enum(),
			})
	}

	return commonDict
}
