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

	topCategories := make([]int32, len(fc.TopCategoryIds))
	for i, v := range fc.TopCategoryIds {
		topCategories[i] = int32(v)
	}

	subCategories := make([]int32, len(fc.SubCategoryIds))
	for i, v := range fc.SubCategoryIds {
		subCategories[i] = int32(v)
	}

	types := make([]int32, len(fc.TypeIds))
	for i, v := range fc.TypeIds {
		types[i] = int32(v)
	}

	return &pb_common.FilterConditions{
		From:           fc.From.String(),
		To:             fc.To.String(),
		Currency:       fc.Currency,
		OnSale:         fc.OnSale,
		Color:          fc.Color,
		TopCategoryIds: topCategories,
		SubCategoryIds: subCategories,
		TypeIds:        types,
		SizesIds:       sizes,
		Preorder:       fc.Preorder,
		ByTag:          fc.ByTag,
		Collections:    fc.Collections,
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

	topCategories := make([]int, len(fc.TopCategoryIds))
	for i, v := range fc.TopCategoryIds {
		topCategories[i] = int(v)
	}

	subCategories := make([]int, len(fc.SubCategoryIds))
	for i, v := range fc.SubCategoryIds {
		subCategories[i] = int(v)
	}

	types := make([]int, len(fc.TypeIds))
	for i, v := range fc.TypeIds {
		types[i] = int(v)
	}

	from, err := decimal.NewFromString(fc.From)
	if err != nil {
		from = decimal.Zero
	}
	to, err := decimal.NewFromString(fc.To)
	if err != nil {
		to = decimal.Zero
	}

	genders := make([]entity.GenderEnum, len(fc.Gender))
	for i, v := range fc.Gender {
		genders[i] = genderPbEntityMap[v]
	}

	return &entity.FilterConditions{
		From:           from,
		To:             to,
		Currency:       fc.Currency,
		OnSale:         fc.OnSale,
		Gender:         genders,
		Color:          fc.Color,
		TopCategoryIds: topCategories,
		SubCategoryIds: subCategories,
		TypeIds:        types,
		SizesIds:       sizes,
		Preorder:       fc.Preorder,
		ByTag:          fc.ByTag,
		Collections:    fc.Collections,
	}
}
