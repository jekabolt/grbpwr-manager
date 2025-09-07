package dto

import (
	"fmt"

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
		FeaturedTag:       hi.FeaturedTag,
		FeaturedArchiveId: int(hi.FeaturedArchiveId),
		Translations:      make([]entity.NavFeaturedEntityInsertTranslation, len(hi.Translations)),
	}

	for i, trans := range hi.Translations {
		result.Translations[i] = entity.NavFeaturedEntityInsertTranslation{
			LanguageId:  int(trans.LanguageId),
			ExploreText: trans.ExploreText,
		}
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
				Translations:     make([]entity.HeroSingleInsertTranslation, len(hi.Single.Translations)),
			}

			for i, trans := range hi.Single.Translations {
				result.Single.Translations[i] = entity.HeroSingleInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}
		}
	case pb_common.HeroType_HERO_TYPE_DOUBLE:
		if hi.Double != nil {
			leftTranslations := make([]entity.HeroSingleInsertTranslation, len(hi.Double.Left.Translations))
			for i, trans := range hi.Double.Left.Translations {
				leftTranslations[i] = entity.HeroSingleInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			rightTranslations := make([]entity.HeroSingleInsertTranslation, len(hi.Double.Right.Translations))
			for i, trans := range hi.Double.Right.Translations {
				rightTranslations[i] = entity.HeroSingleInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			result.Double = entity.HeroDoubleInsert{
				Left: entity.HeroSingleInsert{
					MediaPortraitId:  int(hi.Double.Left.MediaPortraitId),
					MediaLandscapeId: int(hi.Double.Left.MediaLandscapeId),
					ExploreLink:      hi.Double.Left.ExploreLink,
					Translations:     leftTranslations,
				},
				Right: entity.HeroSingleInsert{
					MediaPortraitId:  int(hi.Double.Right.MediaPortraitId),
					MediaLandscapeId: int(hi.Double.Right.MediaLandscapeId),
					ExploreLink:      hi.Double.Right.ExploreLink,
					Translations:     rightTranslations,
				},
			}
		}
	case pb_common.HeroType_HERO_TYPE_MAIN:
		if hi.Main != nil {
			translations := make([]entity.HeroMainInsertTranslation, len(hi.Main.Translations))
			for i, trans := range hi.Main.Translations {
				translations[i] = entity.HeroMainInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Tag:         trans.Tag,
					Description: trans.Description,
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			result.Main = entity.HeroMainInsert{
				MediaPortraitId:  int(hi.Main.MediaPortraitId),
				MediaLandscapeId: int(hi.Main.MediaLandscapeId),
				ExploreLink:      hi.Main.ExploreLink,
				Translations:     translations,
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS:
		if hi.FeaturedProducts != nil {
			translations := make([]entity.HeroFeaturedProductsInsertTranslation, len(hi.FeaturedProducts.Translations))
			for i, trans := range hi.FeaturedProducts.Translations {
				translations[i] = entity.HeroFeaturedProductsInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			result.FeaturedProducts = entity.HeroFeaturedProductsInsert{
				ProductIDs:   make([]int, len(hi.FeaturedProducts.ProductIds)),
				ExploreLink:  hi.FeaturedProducts.ExploreLink,
				Translations: translations,
			}
			for i, id := range hi.FeaturedProducts.ProductIds {
				result.FeaturedProducts.ProductIDs[i] = int(id)
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS_TAG:
		if hi.FeaturedProductsTag != nil {
			translations := make([]entity.HeroFeaturedProductsTagInsertTranslation, len(hi.FeaturedProductsTag.Translations))
			for i, trans := range hi.FeaturedProductsTag.Translations {
				translations[i] = entity.HeroFeaturedProductsTagInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			result.FeaturedProductsTag = entity.HeroFeaturedProductsTagInsert{
				Tag:          hi.FeaturedProductsTag.Tag,
				Translations: translations,
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_ARCHIVE:
		if hi.FeaturedArchive != nil {
			translations := make([]entity.HeroFeaturedArchiveInsertTranslation, len(hi.FeaturedArchive.Translations))
			for i, trans := range hi.FeaturedArchive.Translations {
				translations[i] = entity.HeroFeaturedArchiveInsertTranslation{
					LanguageId:  int(trans.LanguageId),
					Headline:    trans.Headline,
					ExploreText: trans.ExploreText,
				}
			}

			result.FeaturedArchive = entity.HeroFeaturedArchiveInsert{
				ArchiveId:    int(hi.FeaturedArchive.ArchiveId),
				Tag:          hi.FeaturedArchive.Tag,
				Translations: translations,
			}
		}
	}

	return result
}

// ConvertEntityHeroFullToCommonWithTranslations converts entity.HeroFull to pb_common.HeroFullWithTranslations
func ConvertEntityHeroFullToCommonWithTranslations(hf *entity.HeroFullWithTranslations) (*pb_common.HeroFullWithTranslations, error) {
	if hf == nil {
		return nil, nil
	}

	result := &pb_common.HeroFullWithTranslations{
		Entities:    make([]*pb_common.HeroEntityWithTranslations, len(hf.Entities)),
		NavFeatured: ConvertEntityNavFeaturedToCommonWithTranslations(&hf.NavFeatured),
	}

	for i, entity := range hf.Entities {
		commonEntity, err := ConvertEntityHeroEntityToCommonWithTranslations(&entity)
		if err != nil {
			return nil, fmt.Errorf("failed to convert entity at index %d: %w", i, err)
		}
		result.Entities[i] = commonEntity
	}

	return result, nil
}

func ConvertEntityNavFeaturedToCommonWithTranslations(nf *entity.NavFeaturedWithTranslations) *pb_common.NavFeaturedWithTranslations {
	if nf == nil {
		return nil
	}
	return &pb_common.NavFeaturedWithTranslations{
		Men:   ConvertEntityNavFeaturedEntityToCommonWithTranslations(&nf.Men),
		Women: ConvertEntityNavFeaturedEntityToCommonWithTranslations(&nf.Women),
	}
}

func ConvertEntityNavFeaturedEntityToCommonWithTranslations(ne *entity.NavFeaturedEntityWithTranslations) *pb_common.NavFeaturedEntityWithTranslations {
	if ne == nil {
		return nil
	}

	translations := make([]*pb_common.NavFeaturedEntityInsertTranslation, len(ne.Translations))
	for i, trans := range ne.Translations {
		translations[i] = &pb_common.NavFeaturedEntityInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			ExploreText: trans.ExploreText,
		}
	}

	return &pb_common.NavFeaturedEntityWithTranslations{
		Media:             ConvertEntityToCommonMedia(&ne.Media),
		FeaturedTag:       ne.FeaturedTag,
		FeaturedArchiveId: ne.FeaturedArchiveId,
		Translations:      translations,
	}
}

func ConvertEntityHeroEntityToCommonWithTranslations(he *entity.HeroEntityWithTranslations) (*pb_common.HeroEntityWithTranslations, error) {
	if he == nil {
		return nil, nil
	}

	result := &pb_common.HeroEntityWithTranslations{
		Type: pb_common.HeroType(he.Type),
	}

	switch he.Type {
	case entity.HeroTypeSingle:
		if he.Single != nil {
			result.Single = ConvertEntityHeroSingleToCommonWithTranslations(he.Single)
		}
	case entity.HeroTypeDouble:
		if he.Double != nil {
			result.Double = ConvertEntityHeroDoubleToCommonWithTranslations(he.Double)
		}
	case entity.HeroTypeMain:
		if he.Main != nil {
			result.Main = ConvertEntityHeroMainToCommonWithTranslations(he.Main)
		}
	case entity.HeroTypeFeaturedProducts:
		if he.FeaturedProducts != nil {
			featuredProducts, err := ConvertEntityHeroFeaturedProductsToCommonWithTranslations(he.FeaturedProducts)
			if err != nil {
				return nil, fmt.Errorf("failed to convert featured products: %w", err)
			}
			result.FeaturedProducts = featuredProducts
		}
	case entity.HeroTypeFeaturedProductsTag:
		if he.FeaturedProductsTag != nil {
			featuredProductsTag, err := ConvertEntityHeroFeaturedProductsTagToCommonWithTranslations(he.FeaturedProductsTag)
			if err != nil {
				return nil, err
			}
			result.FeaturedProductsTag = featuredProductsTag
		}
	case entity.HeroTypeFeaturedArchive:
		if he.FeaturedArchive != nil {
			result.FeaturedArchive = ConvertEntityHeroFeaturedArchiveToCommonWithTranslations(he.FeaturedArchive)
		}
	}

	return result, nil
}

func ConvertEntityHeroSingleToCommonWithTranslations(hsa *entity.HeroSingleWithTranslations) *pb_common.HeroSingleWithTranslations {
	if hsa == nil {
		return nil
	}

	translations := make([]*pb_common.HeroSingleInsertTranslation, len(hsa.Translations))
	for i, trans := range hsa.Translations {
		translations[i] = &pb_common.HeroSingleInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Headline:    trans.Headline,
			ExploreText: trans.ExploreText,
		}
	}

	return &pb_common.HeroSingleWithTranslations{
		MediaPortrait:  ConvertEntityToCommonMedia(&hsa.MediaPortrait),
		MediaLandscape: ConvertEntityToCommonMedia(&hsa.MediaLandscape),
		ExploreLink:    hsa.ExploreLink,
		Translations:   translations,
	}
}

func ConvertEntityHeroDoubleToCommonWithTranslations(hda *entity.HeroDoubleWithTranslations) *pb_common.HeroDoubleWithTranslations {
	if hda == nil {
		return nil
	}
	return &pb_common.HeroDoubleWithTranslations{
		Left:  ConvertEntityHeroSingleToCommonWithTranslations(&hda.Left),
		Right: ConvertEntityHeroSingleToCommonWithTranslations(&hda.Right),
	}
}

func ConvertEntityHeroMainToCommonWithTranslations(hma *entity.HeroMainWithTranslations) *pb_common.HeroMainWithTranslations {
	if hma == nil {
		return nil
	}

	translations := make([]*pb_common.HeroMainInsertTranslation, len(hma.Translations))
	for i, trans := range hma.Translations {
		translations[i] = &pb_common.HeroMainInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Tag:         trans.Tag,
			Description: trans.Description,
			Headline:    trans.Headline,
			ExploreText: trans.ExploreText,
		}
	}

	return &pb_common.HeroMainWithTranslations{
		Single:       ConvertEntityHeroSingleToCommonWithTranslations(&hma.Single),
		Translations: translations,
	}
}

func ConvertEntityHeroFeaturedProductsToCommonWithTranslations(hfp *entity.HeroFeaturedProductsWithTranslations) (*pb_common.HeroFeaturedProductsWithTranslations, error) {
	if hfp == nil {
		return nil, nil
	}

	translations := make([]*pb_common.HeroFeaturedProductsInsertTranslation, len(hfp.Translations))
	for i, trans := range hfp.Translations {
		translations[i] = &pb_common.HeroFeaturedProductsInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Headline:    trans.Headline,
			ExploreText: trans.ExploreText,
		}
	}

	result := &pb_common.HeroFeaturedProductsWithTranslations{
		Products:     make([]*pb_common.Product, len(hfp.Products)),
		ExploreLink:  hfp.ExploreLink,
		Translations: translations,
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

func ConvertEntityHeroFeaturedProductsTagToCommonWithTranslations(hfp *entity.HeroFeaturedProductsTagWithTranslations) (*pb_common.HeroFeaturedProductsTagWithTranslations, error) {
	if hfp == nil {
		return nil, nil
	}
	commonProducts, err := ConvertEntityHeroFeaturedProductsToCommonWithTranslations(&hfp.Products)
	if err != nil {
		return nil, fmt.Errorf("failed to convert featured products: %w", err)
	}

	translations := make([]*pb_common.HeroFeaturedProductsTagInsertTranslation, len(hfp.Translations))
	for i, trans := range hfp.Translations {
		translations[i] = &pb_common.HeroFeaturedProductsTagInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Headline:    trans.Headline,
			ExploreText: trans.ExploreText,
		}
	}

	return &pb_common.HeroFeaturedProductsTagWithTranslations{
		Tag:          hfp.Tag,
		Products:     commonProducts,
		Translations: translations,
	}, nil
}

func ConvertEntityHeroFeaturedArchiveToCommonWithTranslations(he *entity.HeroFeaturedArchiveWithTranslations) *pb_common.HeroFeaturedArchiveWithTranslations {
	if he == nil {
		return nil
	}

	translations := make([]*pb_common.HeroFeaturedArchiveInsertTranslation, len(he.Translations))
	for i, trans := range he.Translations {
		translations[i] = &pb_common.HeroFeaturedArchiveInsertTranslation{
			LanguageId:  int32(trans.LanguageId),
			Headline:    trans.Headline,
			ExploreText: trans.ExploreText,
		}
	}

	return &pb_common.HeroFeaturedArchiveWithTranslations{
		Archive:      ConvertArchiveFullEntityToPb(&he.Archive),
		Tag:          he.Tag,
		Headline:     he.Headline,
		ExploreText:  he.ExploreText,
		Translations: translations,
	}
}
