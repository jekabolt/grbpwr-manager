package dto

import (
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func ConvertCommonHeroEntityInsertToEntity(hi *pb_common.HeroEntityInsert) entity.HeroEntityInsert {
	result := entity.HeroEntityInsert{
		Type: entity.HeroType(hi.Type),
	}

	switch hi.Type {
	case pb_common.HeroType_HERO_TYPE_SINGLE_ADD:
		if hi.SingleAdd != nil {
			result.SingleAdd = entity.HeroSingleAddInsert{
				MediaId:     int(hi.SingleAdd.MediaId),
				ExploreLink: hi.SingleAdd.ExploreLink,
				ExploreText: hi.SingleAdd.ExploreText,
			}
		}
	case pb_common.HeroType_HERO_TYPE_DOUBLE_ADD:
		if hi.DoubleAdd != nil {
			result.DoubleAdd = entity.HeroDoubleAddInsert{
				Left: entity.HeroSingleAddInsert{
					MediaId:     int(hi.DoubleAdd.Left.MediaId),
					ExploreLink: hi.DoubleAdd.Left.ExploreLink,
					ExploreText: hi.DoubleAdd.Left.ExploreText,
				},
				Right: entity.HeroSingleAddInsert{
					MediaId:     int(hi.DoubleAdd.Right.MediaId),
					ExploreLink: hi.DoubleAdd.Right.ExploreLink,
					ExploreText: hi.DoubleAdd.Right.ExploreText,
				},
			}
		}
	case pb_common.HeroType_HERO_TYPE_MAIN_ADD:
		if hi.MainAdd != nil && hi.MainAdd.SingleAdd != nil {
			result.MainAdd = entity.HeroMainAddInsert{
				SingleAdd: entity.HeroSingleAddInsert{
					MediaId:     int(hi.MainAdd.SingleAdd.MediaId),
					ExploreLink: hi.MainAdd.SingleAdd.ExploreLink,
					ExploreText: hi.MainAdd.SingleAdd.ExploreText,
				},
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS:
		if hi.FeaturedProducts != nil {
			result.FeaturedProducts = entity.HeroFeaturedProductsInsert{
				ProductIDs:  make([]int, len(hi.FeaturedProducts.ProductIds)),
				Title:       hi.FeaturedProducts.Title,
				ExploreText: hi.FeaturedProducts.ExploreText,
				ExploreLink: hi.FeaturedProducts.ExploreLink,
			}
			for i, id := range hi.FeaturedProducts.ProductIds {
				result.FeaturedProducts.ProductIDs[i] = int(id)
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS_TAG:
		if hi.FeaturedProductsTag != nil {
			result.FeaturedProductsTag = entity.HeroFeaturedProductsTagInsert{
				Tag:         hi.FeaturedProductsTag.Tag,
				Title:       hi.FeaturedProductsTag.Title,
				ExploreText: hi.FeaturedProductsTag.ExploreText,
				ExploreLink: hi.FeaturedProductsTag.ExploreLink,
			}
		}
	}

	return result
}

func ConvertEntityHeroFullToCommon(hf *entity.HeroFull) (*pb_common.HeroFull, error) {
	if hf == nil {
		return nil, nil
	}

	result := &pb_common.HeroFull{
		Entities: make([]*pb_common.HeroEntity, len(hf.Entities)),
	}

	for i, entity := range hf.Entities {
		commonEntity, err := ConvertEntityHeroEntityToCommon(&entity)
		if err != nil {
			return nil, fmt.Errorf("failed to convert entity at index %d: %w", i, err)
		}
		result.Entities[i] = commonEntity
	}

	return result, nil
}

func ConvertEntityHeroEntityToCommon(he *entity.HeroEntity) (*pb_common.HeroEntity, error) {
	if he == nil {
		return nil, nil
	}

	result := &pb_common.HeroEntity{
		Type: pb_common.HeroType(he.Type),
	}

	switch he.Type {
	case entity.HeroTypeSingleAdd:
		if he.SingleAdd != nil {
			result.SingleAdd = ConvertEntityHeroSingleAddToCommon(he.SingleAdd)
		}
	case entity.HeroTypeDoubleAdd:
		if he.DoubleAdd != nil {
			result.DoubleAdd = ConvertEntityHeroDoubleAddToCommon(he.DoubleAdd)
		}
	case entity.HeroTypeMainAdd:
		if he.MainAdd != nil {
			result.MainAdd = ConvertEntityHeroMainAddToCommon(he.MainAdd)
		}
	case entity.HeroTypeFeaturedProducts:
		if he.FeaturedProducts != nil {
			featuredProducts, err := ConvertEntityHeroFeaturedProductsToCommon(he.FeaturedProducts)
			if err != nil {
				return nil, fmt.Errorf("failed to convert featured products: %w", err)
			}
			result.FeaturedProducts = featuredProducts
		}
	case entity.HeroTypeFeaturedProductsTag:
		if he.FeaturedProductsTag != nil {
			featuredProductsTag, err := ConvertEntityHeroFeaturedProductsTagToCommon(he.FeaturedProductsTag)
			if err != nil {
				return nil, err
			}
			result.FeaturedProductsTag = featuredProductsTag
		}
	}

	return result, nil
}

func ConvertEntityHeroSingleAddToCommon(hsa *entity.HeroSingleAdd) *pb_common.HeroSingleAdd {
	if hsa == nil {
		return nil
	}
	return &pb_common.HeroSingleAdd{
		Media:       ConvertEntityToCommonMedia(&hsa.Media),
		ExploreLink: hsa.ExploreLink,
		ExploreText: hsa.ExploreText,
	}
}

func ConvertEntityHeroDoubleAddToCommon(hda *entity.HeroDoubleAdd) *pb_common.HeroDoubleAdd {
	if hda == nil {
		return nil
	}
	return &pb_common.HeroDoubleAdd{
		Left:  ConvertEntityHeroSingleAddToCommon(&hda.Left),
		Right: ConvertEntityHeroSingleAddToCommon(&hda.Right),
	}
}

func ConvertEntityHeroMainAddToCommon(hma *entity.HeroMainAdd) *pb_common.HeroMainAdd {
	if hma == nil {
		return nil
	}
	return &pb_common.HeroMainAdd{
		SingleAdd: ConvertEntityHeroSingleAddToCommon(&hma.SingleAdd),
	}
}

func ConvertEntityHeroFeaturedProductsToCommon(hfp *entity.HeroFeaturedProducts) (*pb_common.HeroFeaturedProducts, error) {
	if hfp == nil {
		return nil, nil
	}
	result := &pb_common.HeroFeaturedProducts{
		Products:    make([]*pb_common.Product, len(hfp.Products)),
		Title:       hfp.Title,
		ExploreText: hfp.ExploreText,
		ExploreLink: hfp.ExploreLink,
	}
	for i, product := range hfp.Products {
		commonProduct, err := ConvertEntityProductToCommon(&product)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product at index %d: %w", i, err)
		}
		result.Products[i] = commonProduct
	}
	return result, nil
}

func ConvertEntityHeroFeaturedProductsTagToCommon(hfp *entity.HeroFeaturedProductsTag) (*pb_common.HeroFeaturedProductsTag, error) {
	if hfp == nil {
		return nil, nil
	}
	commonProducts, err := ConvertEntityHeroFeaturedProductsToCommon(&entity.HeroFeaturedProducts{
		Products:    hfp.Products,
		Title:       hfp.Title,
		ExploreText: hfp.ExploreText,
		ExploreLink: hfp.ExploreLink,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to convert featured products: %w", err)
	}
	return &pb_common.HeroFeaturedProductsTag{
		Tag:      hfp.Tag,
		Products: commonProducts,
	}, nil
}
