package dto

import (
	"fmt"
	"strconv"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
)

func ConvertCommonHeroFullInsertToEntity(hi *pb_common.HeroFullInsert) entity.HeroFullInsert {
	result := entity.HeroFullInsert{
		Entities: make([]entity.HeroEntityInsert, len(hi.Entities)),
	}

	for i, entity := range hi.Entities {
		result.Entities[i] = ConvertCommonHeroEntityInsertToEntity(entity)
	}

	if hi.NavFeatured != nil {
		result.NavFeatured = ConvertCommonNavFeaturedInsertToEntity(hi.NavFeatured)
	}

	return result
}

func ConvertCommonNavFeaturedInsertToEntity(hi *pb_common.NavFeaturedInsert) entity.NavFeaturedInsert {
	result := entity.NavFeaturedInsert{
		Men:   ConvertCommonNavFeaturedEntityInsertToEntity(hi.Men),
		Women: ConvertCommonNavFeaturedEntityInsertToEntity(hi.Women),
	}
	return result
}

func ConvertCommonNavFeaturedEntityInsertToEntity(hi *pb_common.NavFeaturedEntityInsert) entity.NavFeaturedEntityInsert {
	result := entity.NavFeaturedEntityInsert{
		MediaId:           int(hi.MediaId),
		ExploreText:       hi.ExploreText,
		FeaturedTag:       hi.FeaturedTag,
		FeaturedArchiveId: int(hi.FeaturedArchiveId),
	}
	return result
}

func ConvertCommonHeroEntityInsertToEntity(hi *pb_common.HeroEntityInsert) entity.HeroEntityInsert {
	result := entity.HeroEntityInsert{
		Type: entity.HeroType(hi.Type),
	}

	switch hi.Type {
	case pb_common.HeroType_HERO_TYPE_SINGLE:
		if hi.Single != nil {
			result.Single = entity.HeroSingleInsert{
				MediaPortraitId:  int(hi.Single.MediaPortraitId),
				MediaLandscapeId: int(hi.Single.MediaLandscapeId),
				ExploreLink:      hi.Single.ExploreLink,
				ExploreText:      hi.Single.ExploreText,
				Headline:         hi.Single.Headline,
			}
		}
	case pb_common.HeroType_HERO_TYPE_DOUBLE:
		if hi.Double != nil {
			result.Double = entity.HeroDoubleInsert{
				Left: entity.HeroSingleInsert{
					MediaPortraitId:  int(hi.Double.Left.MediaPortraitId),
					MediaLandscapeId: int(hi.Double.Left.MediaLandscapeId),
					ExploreLink:      hi.Double.Left.ExploreLink,
					ExploreText:      hi.Double.Left.ExploreText,
					Headline:         hi.Double.Left.Headline,
				},
				Right: entity.HeroSingleInsert{
					MediaPortraitId:  int(hi.Double.Right.MediaPortraitId),
					MediaLandscapeId: int(hi.Double.Right.MediaLandscapeId),
					ExploreLink:      hi.Double.Right.ExploreLink,
					ExploreText:      hi.Double.Right.ExploreText,
					Headline:         hi.Double.Right.Headline,
				},
			}
		}
	case pb_common.HeroType_HERO_TYPE_MAIN:
		if hi.Main != nil && hi.Main.Single != nil {
			result.Main = entity.HeroMainInsert{
				Single: entity.HeroSingleInsert{
					MediaPortraitId:  int(hi.Main.Single.MediaPortraitId),
					MediaLandscapeId: int(hi.Main.Single.MediaLandscapeId),
					ExploreLink:      hi.Main.Single.ExploreLink,
					ExploreText:      hi.Main.Single.ExploreText,
					Headline:         hi.Main.Single.Headline,
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
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_ARCHIVE:
		if hi.FeaturedArchive != nil {
			result.FeaturedArchive = entity.HeroFeaturedArchiveInsert{
				ArchiveId:   int(hi.FeaturedArchive.ArchiveId),
				Tag:         hi.FeaturedArchive.Tag,
				Headline:    hi.FeaturedArchive.Headline,
				ExploreText: hi.FeaturedArchive.ExploreText,
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
		Entities:    make([]*pb_common.HeroEntity, len(hf.Entities)),
		NavFeatured: ConvertEntityNavFeaturedToCommon(&hf.NavFeatured),
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

func ConvertEntityNavFeaturedToCommon(nf *entity.NavFeatured) *pb_common.NavFeatured {
	if nf == nil {
		return nil
	}
	return &pb_common.NavFeatured{
		Men:   ConvertEntityNavFeaturedEntityToCommon(&nf.Men),
		Women: ConvertEntityNavFeaturedEntityToCommon(&nf.Women),
	}
}

func ConvertEntityNavFeaturedEntityToCommon(ne *entity.NavFeaturedEntity) *pb_common.NavFeaturedEntity {
	if ne == nil {
		return nil
	}
	return &pb_common.NavFeaturedEntity{
		Media:             ConvertEntityToCommonMedia(&ne.Media),
		ExploreText:       ne.ExploreText,
		FeaturedTag:       ne.FeaturedTag,
		FeaturedArchiveId: strconv.Itoa(ne.FeaturedArchiveId),
	}
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
		Archive:     ConvertArchiveFullEntityToPb(&he.Archive),
		Tag:         he.Tag,
		Headline:    he.Headline,
		ExploreText: he.ExploreText,
	}
}

func ConvertEntityHeroSingleToCommon(hsa *entity.HeroSingle) *pb_common.HeroSingle {
	if hsa == nil {
		return nil
	}
	return &pb_common.HeroSingle{
		MediaPortrait:  ConvertEntityToCommonMedia(&hsa.MediaPortrait),
		MediaLandscape: ConvertEntityToCommonMedia(&hsa.MediaLandscape),
		Headline:       hsa.Headline,
		ExploreLink:    hsa.ExploreLink,
		ExploreText:    hsa.ExploreText,
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
