package dto

import (
	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"github.com/shopspring/decimal"
)

// ConvertEntityFilterConditionsToPBCommon converts FilterConditions from entity to pb_common
func ConvertEntityFilterConditionsToPBCommon(fc entity.FilterConditions) *pb_common.FilterConditions {
	sizes := make([]int32, len(fc.SizesIds))
	for i, v := range fc.SizesIds {
		sizes[i] = int32(v)
	}

	categories := make([]int32, len(fc.CategoryIds))
	for i, v := range fc.CategoryIds {
		categories[i] = int32(v)
	}

	return &pb_common.FilterConditions{
		From:        fc.From.String(),
		To:          fc.To.String(),
		OnSale:      fc.OnSale,
		Color:       fc.Color,
		CategoryIds: categories,
		SizesIds:    sizes,
		Preorder:    fc.Preorder,
		ByTag:       fc.ByTag,
	}
}

// ConvertPBCommonOrderFactorToEntity converts OrderFactor from pb_common to entity
func ConvertPBCommonOrderFactorToEntity(of pb_common.OrderFactor) entity.OrderFactor {
	switch of {
	case pb_common.OrderFactor_ORDER_FACTOR_ASC:
		return entity.Ascending
	case pb_common.OrderFactor_ORDER_FACTOR_DESC:
		return entity.Descending
	default:
		return entity.Ascending // default value
	}
}

// ConvertPBCommonSortFactorToEntity converts SortFactor from pb_common to entity
func ConvertPBCommonSortFactorToEntity(sf pb_common.SortFactor) entity.SortFactor {
	switch sf {
	case pb_common.SortFactor_SORT_FACTOR_CREATED_AT:
		return entity.CreatedAt
	case pb_common.SortFactor_SORT_FACTOR_UPDATED_AT:
		return entity.UpdatedAt
	case pb_common.SortFactor_SORT_FACTOR_NAME:
		return entity.Name
	case pb_common.SortFactor_SORT_FACTOR_PRICE:
		return entity.Price
	default:
		return entity.CreatedAt // default value
	}
}

// ConvertPBCommonFilterConditionsToEntity converts FilterConditions from pb_common to entity
func ConvertPBCommonFilterConditionsToEntity(fc *pb_common.FilterConditions) *entity.FilterConditions {
	if fc == nil {
		return nil
	}

	sizes := make([]int, len(fc.SizesIds))
	for i, v := range fc.SizesIds {
		sizes[i] = int(v)
	}

	categories := make([]int, len(fc.CategoryIds))
	for i, v := range fc.CategoryIds {
		categories[i] = int(v)
	}

	from, err := decimal.NewFromString(fc.From)
	if err != nil {
		from = decimal.Zero
	}
	to, err := decimal.NewFromString(fc.To)
	if err != nil {
		to = decimal.Zero
	}

	return &entity.FilterConditions{
		From:        from,
		To:          to,
		OnSale:      fc.OnSale,
		Gender:      genderPbEntityMap[fc.Gender],
		Color:       fc.Color,
		CategoryIds: categories,
		SizesIds:    sizes,
		Preorder:    fc.Preorder,
		ByTag:       fc.ByTag,
	}
}
