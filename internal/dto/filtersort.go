package dto

import (
	"fmt"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/cache"
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

	excludeTopCategories := make([]int32, len(fc.ExcludeTopCategoryIds))
	for i, v := range fc.ExcludeTopCategoryIds {
		excludeTopCategories[i] = int32(v)
	}

	subCategories := make([]int32, len(fc.SubCategoryIds))
	for i, v := range fc.SubCategoryIds {
		subCategories[i] = int32(v)
	}

	types := make([]int32, len(fc.TypeIds))
	for i, v := range fc.TypeIds {
		types[i] = int32(v)
	}

	seasons := make([]pb_common.SeasonEnum, 0, len(fc.Seasons))
	for _, v := range fc.Seasons {
		if pbSeason, ok := seasonEntityPbMap[v]; ok {
			seasons = append(seasons, pbSeason)
		}
	}

	return &pb_common.FilterConditions{
		From:                  fc.From.String(),
		To:                    fc.To.String(),
		Currency:              fc.Currency,
		OnSale:                fc.OnSale,
		ColorCodes:            fc.ColorCodes,
		TopCategoryIds:        topCategories,
		ExcludeTopCategoryIds: excludeTopCategories,
		SubCategoryIds:        subCategories,
		TypeIds:               types,
		SizesIds:              sizes,
		Preorder:              fc.Preorder,
		ByTag:                 fc.ByTag,
		Collections:           fc.Collections,
		Seasons:               seasons,
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
func ConvertPBCommonFilterConditionsToEntity(fc *pb_common.FilterConditions) (*entity.FilterConditions, error) {
	if fc == nil {
		return nil, nil
	}

	for i, code := range fc.ColorCodes {
		if len(code) != 3 || code != strings.ToUpper(code) || strings.TrimSpace(code) != code {
			return nil, fmt.Errorf("color_codes[%d] must be exactly 3 uppercase characters", i)
		}
		if _, ok := cache.GetColorByCode(code); !ok {
			return nil, fmt.Errorf("color_codes[%d] %q is not in the color dictionary", i, code)
		}
	}

	sizes := make([]int, len(fc.SizesIds))
	for i, v := range fc.SizesIds {
		sizes[i] = int(v)
	}

	topCategories := make([]int, len(fc.TopCategoryIds))
	for i, v := range fc.TopCategoryIds {
		topCategories[i] = int(v)
	}

	excludeTopCategories := make([]int, len(fc.ExcludeTopCategoryIds))
	for i, v := range fc.ExcludeTopCategoryIds {
		excludeTopCategories[i] = int(v)
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

	genders := make([]entity.GenderEnum, 0, len(fc.Gender))
	for _, v := range fc.Gender {
		if g, ok := genderPbEntityMap[v]; ok {
			genders = append(genders, g)
		}
	}

	seasons := make([]entity.SeasonEnum, 0, len(fc.Seasons))
	for _, v := range fc.Seasons {
		if s, ok := seasonPbEntityMap[v]; ok {
			seasons = append(seasons, s)
		}
	}

	return &entity.FilterConditions{
		From:                  from,
		To:                    to,
		Currency:              fc.Currency,
		OnSale:                fc.OnSale,
		Gender:                genders,
		ColorCodes:            fc.ColorCodes,
		TopCategoryIds:        topCategories,
		ExcludeTopCategoryIds: excludeTopCategories,
		SubCategoryIds:        subCategories,
		TypeIds:               types,
		SizesIds:              sizes,
		Preorder:              fc.Preorder,
		ByTag:                 fc.ByTag,
		Collections:           fc.Collections,
		Seasons:               seasons,
		Exclusive:             fc.Exclusive,
	}, nil
}
