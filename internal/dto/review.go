package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ==================== ProductRating ====================

func ConvertPbProductRatingToEntity(pb pb_common.ProductRatingEnum) entity.ProductRating {
	switch pb {
	case pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_POOR:
		return entity.ProductRatingPoor
	case pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_FAIR:
		return entity.ProductRatingFair
	case pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_GOOD:
		return entity.ProductRatingGood
	case pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_VERY_GOOD:
		return entity.ProductRatingVeryGood
	case pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_EXCELLENT:
		return entity.ProductRatingExcellent
	default:
		return ""
	}
}

func ConvertEntityToPbProductRating(r entity.ProductRating) pb_common.ProductRatingEnum {
	switch r {
	case entity.ProductRatingPoor:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_POOR
	case entity.ProductRatingFair:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_FAIR
	case entity.ProductRatingGood:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_GOOD
	case entity.ProductRatingVeryGood:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_VERY_GOOD
	case entity.ProductRatingExcellent:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_EXCELLENT
	default:
		return pb_common.ProductRatingEnum_PRODUCT_RATING_ENUM_UNKNOWN
	}
}

// ==================== FitScale ====================

func ConvertPbFitScaleToEntity(pb pb_common.FitScaleEnum) entity.FitScale {
	switch pb {
	case pb_common.FitScaleEnum_FIT_SCALE_ENUM_RUNS_SMALL:
		return entity.FitScaleRunsSmall
	case pb_common.FitScaleEnum_FIT_SCALE_ENUM_SLIGHTLY_SMALL:
		return entity.FitScaleSlightlySmall
	case pb_common.FitScaleEnum_FIT_SCALE_ENUM_TRUE_TO_SIZE:
		return entity.FitScaleTrueToSize
	case pb_common.FitScaleEnum_FIT_SCALE_ENUM_SLIGHTLY_LARGE:
		return entity.FitScaleSlightlyLarge
	case pb_common.FitScaleEnum_FIT_SCALE_ENUM_RUNS_LARGE:
		return entity.FitScaleRunsLarge
	default:
		return ""
	}
}

func ConvertEntityToPbFitScale(f entity.FitScale) pb_common.FitScaleEnum {
	switch f {
	case entity.FitScaleRunsSmall:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_RUNS_SMALL
	case entity.FitScaleSlightlySmall:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_SLIGHTLY_SMALL
	case entity.FitScaleTrueToSize:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_TRUE_TO_SIZE
	case entity.FitScaleSlightlyLarge:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_SLIGHTLY_LARGE
	case entity.FitScaleRunsLarge:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_RUNS_LARGE
	default:
		return pb_common.FitScaleEnum_FIT_SCALE_ENUM_UNKNOWN
	}
}

// ==================== DeliverySpeed ====================

func ConvertPbDeliverySpeedToEntity(pb pb_common.DeliverySpeedEnum) entity.DeliverySpeed {
	switch pb {
	case pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_MUCH_FASTER_THAN_EXPECTED:
		return entity.DeliverySpeedMuchFaster
	case pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_FASTER_THAN_EXPECTED:
		return entity.DeliverySpeedFaster
	case pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_AS_EXPECTED:
		return entity.DeliverySpeedAsExpected
	case pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_SLOWER_THAN_EXPECTED:
		return entity.DeliverySpeedSlower
	case pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_MUCH_SLOWER_THAN_EXPECTED:
		return entity.DeliverySpeedMuchSlower
	default:
		return ""
	}
}

func ConvertEntityToPbDeliverySpeed(d entity.DeliverySpeed) pb_common.DeliverySpeedEnum {
	switch d {
	case entity.DeliverySpeedMuchFaster:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_MUCH_FASTER_THAN_EXPECTED
	case entity.DeliverySpeedFaster:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_FASTER_THAN_EXPECTED
	case entity.DeliverySpeedAsExpected:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_AS_EXPECTED
	case entity.DeliverySpeedSlower:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_SLOWER_THAN_EXPECTED
	case entity.DeliverySpeedMuchSlower:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_MUCH_SLOWER_THAN_EXPECTED
	default:
		return pb_common.DeliverySpeedEnum_DELIVERY_SPEED_ENUM_UNKNOWN
	}
}

// ==================== PackagingCondition ====================

func ConvertPbPackagingConditionToEntity(pb pb_common.PackagingConditionEnum) entity.PackagingCondition {
	switch pb {
	case pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_DAMAGED:
		return entity.PackagingConditionDamaged
	case pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_ACCEPTABLE:
		return entity.PackagingConditionAcceptable
	case pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_GOOD:
		return entity.PackagingConditionGood
	case pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_EXCELLENT:
		return entity.PackagingConditionExcellent
	default:
		return ""
	}
}

func ConvertEntityToPbPackagingCondition(p entity.PackagingCondition) pb_common.PackagingConditionEnum {
	switch p {
	case entity.PackagingConditionDamaged:
		return pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_DAMAGED
	case entity.PackagingConditionAcceptable:
		return pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_ACCEPTABLE
	case entity.PackagingConditionGood:
		return pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_GOOD
	case entity.PackagingConditionExcellent:
		return pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_EXCELLENT
	default:
		return pb_common.PackagingConditionEnum_PACKAGING_CONDITION_ENUM_UNKNOWN
	}
}

// ==================== Review Insert Converters ====================

func ConvertPbOrderReviewInsertToEntity(pb *pb_common.OrderReviewInsert) *entity.OrderReviewInsert {
	if pb == nil {
		return nil
	}
	return &entity.OrderReviewInsert{
		DeliveryRating:  ConvertPbDeliverySpeedToEntity(pb.DeliveryRating),
		PackagingRating: ConvertPbPackagingConditionToEntity(pb.PackagingRating),
	}
}

func ConvertPbOrderItemReviewInsertToEntity(pb *pb_common.OrderItemReviewInsert) entity.OrderItemReviewInsert {
	if pb == nil {
		return entity.OrderItemReviewInsert{}
	}
	return entity.OrderItemReviewInsert{
		OrderItemId: int(pb.OrderItemId),
		Rating:      ConvertPbProductRatingToEntity(pb.Rating),
		FitRating:   ConvertPbFitScaleToEntity(pb.FitRating),
		Recommend:   pb.Recommend,
		Text:        pb.Text,
	}
}

func ConvertPbOrderItemReviewInsertsToEntity(pbs []*pb_common.OrderItemReviewInsert) []entity.OrderItemReviewInsert {
	result := make([]entity.OrderItemReviewInsert, 0, len(pbs))
	for _, pb := range pbs {
		result = append(result, ConvertPbOrderItemReviewInsertToEntity(pb))
	}
	return result
}

// ==================== Review Entity to Proto Converters ====================

func ConvertEntityOrderReviewToPb(r *entity.OrderReview) *pb_common.OrderReview {
	if r == nil {
		return nil
	}
	return &pb_common.OrderReview{
		Id:              int32(r.Id),
		OrderId:         int32(r.OrderId),
		DeliveryRating:  ConvertEntityToPbDeliverySpeed(r.DeliveryRating),
		PackagingRating: ConvertEntityToPbPackagingCondition(r.PackagingRating),
		CreatedAt:       timestamppb.New(r.CreatedAt),
	}
}

func ConvertEntityOrderItemReviewToPb(r *entity.OrderItemReview) *pb_common.OrderItemReview {
	if r == nil {
		return nil
	}
	return &pb_common.OrderItemReview{
		Id:          int32(r.Id),
		OrderItemId: int32(r.OrderItemId),
		Rating:      ConvertEntityToPbProductRating(r.Rating),
		FitRating:   ConvertEntityToPbFitScale(r.FitRating),
		Recommend:   r.Recommend,
		Text:        r.Text,
		CreatedAt:   timestamppb.New(r.CreatedAt),
	}
}

func ConvertEntityOrderReviewFullToPb(rf *entity.OrderReviewFull) *pb_common.OrderReviewFull {
	if rf == nil {
		return nil
	}

	itemReviews := make([]*pb_common.OrderItemReview, 0, len(rf.ItemReviews))
	for i := range rf.ItemReviews {
		itemReviews = append(itemReviews, ConvertEntityOrderItemReviewToPb(&rf.ItemReviews[i]))
	}

	return &pb_common.OrderReviewFull{
		OrderReview: ConvertEntityOrderReviewToPb(&rf.OrderReview),
		ItemReviews: itemReviews,
	}
}

func ConvertEntityOrderReviewFullsToPb(rfs []entity.OrderReviewFull) []*pb_common.OrderReviewFull {
	result := make([]*pb_common.OrderReviewFull, 0, len(rfs))
	for i := range rfs {
		result = append(result, ConvertEntityOrderReviewFullToPb(&rfs[i]))
	}
	return result
}
