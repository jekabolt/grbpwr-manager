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
	case pb_common.HeroType_HERO_TYPE_SINGLE:
		if hi.Single != nil {
			result.Single = entity.HeroSingleInsert{
				MediaId:     int(hi.Single.MediaId),
				ExploreLink: hi.Single.ExploreLink,
				ExploreText: hi.Single.ExploreText,
				Headline:    hi.Single.Headline,
			}
		}
	case pb_common.HeroType_HERO_TYPE_DOUBLE:
		if hi.Double != nil {
			result.Double = entity.HeroDoubleInsert{
				Left: entity.HeroSingleInsert{
					MediaId:     int(hi.Double.Left.MediaId),
					ExploreLink: hi.Double.Left.ExploreLink,
					ExploreText: hi.Double.Left.ExploreText,
					Headline:    hi.Double.Left.Headline,
				},
				Right: entity.HeroSingleInsert{
					MediaId:     int(hi.Double.Right.MediaId),
					ExploreLink: hi.Double.Right.ExploreLink,
					ExploreText: hi.Double.Right.ExploreText,
					Headline:    hi.Double.Right.Headline,
				},
			}
		}
	case pb_common.HeroType_HERO_TYPE_MAIN:
		if hi.Main != nil && hi.Main.Single != nil {
			result.Main = entity.HeroMainInsert{
				Single: entity.HeroSingleInsert{
					MediaId:     int(hi.Main.Single.MediaId),
					ExploreLink: hi.Main.Single.ExploreLink,
					ExploreText: hi.Main.Single.ExploreText,
					Headline:    hi.Main.Single.Headline,
				},
				Tag:         hi.Main.Tag,
				Description: hi.Main.Description,
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS:
		if hi.FeaturedProducts != nil {
			result.FeaturedProducts = entity.HeroFeaturedProductsInsert{
				ProductIDs:  make([]int, len(hi.FeaturedProducts.ProductIds)),
				Headline:    hi.FeaturedProducts.Headline,
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
				Headline:    hi.FeaturedProductsTag.Headline,
				ExploreText: hi.FeaturedProductsTag.ExploreText,
				ExploreLink: hi.FeaturedProductsTag.ExploreLink,
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_ARCHIVE:
		if hi.FeaturedArchive != nil {
			result.FeaturedArchive = entity.HeroFeaturedArchiveInsert{
				ArchiveId: int(hi.FeaturedArchive.ArchiveId),
				Tag:       hi.FeaturedArchive.Tag,
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
	case entity.HeroTypeSingle:
		if he.Single != nil {
			result.Single = ConvertEntityHeroSingleToCommon(he.Single)
		}
	case entity.HeroTypeDouble:
		if he.Double != nil {
			result.Double = ConvertEntityHeroDoubleToCommon(he.Double)
		}
	case entity.HeroTypeMain:
		if he.Main != nil {
			result.Main = ConvertEntityHeroMainToCommon(he.Main)
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
	case entity.HeroTypeFeaturedArchive:
		if he.FeaturedArchive != nil {
			result.FeaturedArchive = ConvertEntityHeroFeaturedArchiveToCommon(he.FeaturedArchive)
		}
	}

	return result, nil
}

func ConvertEntityHeroFeaturedArchiveToCommon(he *entity.HeroFeaturedArchive) *pb_common.HeroFeaturedArchive {
	if he == nil {
		return nil
	}
	return &pb_common.HeroFeaturedArchive{
		Archive: ConvertArchiveFullEntityToPb(&he.Archive),
		Tag:     he.Tag,
	}
}

func ConvertEntityHeroSingleToCommon(hsa *entity.HeroSingle) *pb_common.HeroSingle {
	if hsa == nil {
		return nil
	}
	return &pb_common.HeroSingle{
		Media:       ConvertEntityToCommonMedia(&hsa.Media),
		Headline:    hsa.Headline,
		ExploreLink: hsa.ExploreLink,
		ExploreText: hsa.ExploreText,
	}
}

func ConvertEntityHeroDoubleToCommon(hda *entity.HeroDouble) *pb_common.HeroDouble {
	if hda == nil {
		return nil
	}
	return &pb_common.HeroDouble{
		Left:  ConvertEntityHeroSingleToCommon(&hda.Left),
		Right: ConvertEntityHeroSingleToCommon(&hda.Right),
	}
}

func ConvertEntityHeroMainToCommon(hma *entity.HeroMain) *pb_common.HeroMain {
	if hma == nil {
		return nil
	}
	return &pb_common.HeroMain{
		Single:      ConvertEntityHeroSingleToCommon(&hma.Single),
		Tag:         hma.Tag,
		Description: hma.Description,
	}
}

func ConvertEntityHeroFeaturedProductsToCommon(hfp *entity.HeroFeaturedProducts) (*pb_common.HeroFeaturedProducts, error) {
	if hfp == nil {
		return nil, nil
	}
	result := &pb_common.HeroFeaturedProducts{
		Products:    make([]*pb_common.Product, len(hfp.Products)),
		Headline:    hfp.Headline,
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
		Headline:    hfp.Headline,
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
