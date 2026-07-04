package dto

import (
	"fmt"
	"time"

	"github.com/jekabolt/grbpwr-manager/internal/entity"
	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func convertCommonHeroSingleSliceToEntity(in []*pb_common.HeroSingleInsert) []entity.HeroSingleInsert {
	out := make([]entity.HeroSingleInsert, len(in))
	for i, s := range in {
		out[i] = convertCommonHeroSingleInsertToEntity(s)
	}
	return out
}

func convertEntityHeroSingleSliceToCommon(in []entity.HeroSingleWithTranslations) []*pb_common.HeroSingleWithTranslations {
	out := make([]*pb_common.HeroSingleWithTranslations, len(in))
	for i := range in {
		out[i] = convertEntityHeroSingleToCommon(&in[i])
	}
	return out
}

func convertEntityProductsToCommon(products []entity.Product) ([]*pb_common.Product, error) {
	out := make([]*pb_common.Product, len(products))
	for i := range products {
		cp, err := ConvertEntityProductToCommon(&products[i])
		if err != nil {
			return nil, fmt.Errorf("failed to convert product at index %d: %w", i, err)
		}
		out[i] = cp
	}
	return out, nil
}

// ─── fragment helpers ───────────────────────────────────────────────────────

func convertCommonHeroCopyTranslationsToEntity(in []*pb_common.HeroCopyTranslation) []entity.HeroCopyTranslation {
	out := make([]entity.HeroCopyTranslation, len(in))
	for i, t := range in {
		out[i] = entity.HeroCopyTranslation{
			LanguageId:  int(t.LanguageId),
			Tag:         t.Tag,
			Headline:    t.Headline,
			Subhead:     t.Subhead,
			Body:        t.Body,
			CtaText:     t.CtaText,
			ExploreText: t.ExploreText,
			Caption:     t.Caption,
			Placeholder: t.Placeholder,
			SuccessText: t.SuccessText,
		}
	}
	return out
}

func convertEntityHeroCopyTranslationsToCommon(in []entity.HeroCopyTranslation) []*pb_common.HeroCopyTranslation {
	out := make([]*pb_common.HeroCopyTranslation, len(in))
	for i, t := range in {
		out[i] = &pb_common.HeroCopyTranslation{
			LanguageId:  int32(t.LanguageId),
			Tag:         t.Tag,
			Headline:    t.Headline,
			Subhead:     t.Subhead,
			Body:        t.Body,
			CtaText:     t.CtaText,
			ExploreText: t.ExploreText,
			Caption:     t.Caption,
			Placeholder: t.Placeholder,
			SuccessText: t.SuccessText,
		}
	}
	return out
}

func convertCommonHeroMediaToEntity(m *pb_common.HeroMedia) entity.HeroMedia {
	if m == nil {
		return entity.HeroMedia{}
	}
	return entity.HeroMedia{
		PortraitId:     int(m.PortraitId),
		LandscapeId:    int(m.LandscapeId),
		DisableOverlay: m.DisableOverlay,
		DisableTint:    m.DisableTint,
		Stroke:         m.Stroke,
	}
}

func convertEntityHeroMediaFullToCommon(m *entity.HeroMediaFull) *pb_common.HeroMediaFull {
	if m == nil {
		return nil
	}
	return &pb_common.HeroMediaFull{
		Portrait:       ConvertEntityToCommonMedia(&m.Portrait),
		Landscape:      ConvertEntityToCommonMedia(&m.Landscape),
		DisableOverlay: m.DisableOverlay,
		DisableTint:    m.DisableTint,
		Stroke:         m.Stroke,
	}
}

// ─── insert: proto → entity ─────────────────────────────────────────────────

func ConvertCommonHeroFullInsertToEntity(hi *pb_common.HeroFullInsert) entity.HeroFullInsert {
	result := entity.HeroFullInsert{
		Entities: make([]entity.HeroEntityInsert, len(hi.Entities)),
	}
	for i, e := range hi.Entities {
		result.Entities[i] = ConvertCommonHeroEntityInsertToEntity(e)
	}
	if hi.NavFeatured != nil {
		result.NavFeatured = ConvertCommonNavFeaturedInsertToEntity(hi.NavFeatured)
	}
	return result
}

func ConvertCommonNavFeaturedInsertToEntity(hi *pb_common.NavFeaturedInsert) entity.NavFeaturedInsert {
	result := entity.NavFeaturedInsert{}
	if hi.Men != nil {
		result.Men = ConvertCommonNavFeaturedEntityInsertToEntity(hi.Men)
	}
	if hi.Women != nil {
		result.Women = ConvertCommonNavFeaturedEntityInsertToEntity(hi.Women)
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

func convertCommonHeroSingleInsertToEntity(s *pb_common.HeroSingleInsert) entity.HeroSingleInsert {
	if s == nil {
		return entity.HeroSingleInsert{}
	}
	return entity.HeroSingleInsert{
		Media:        convertCommonHeroMediaToEntity(s.Media),
		ExploreLink:  s.ExploreLink,
		Translations: convertCommonHeroCopyTranslationsToEntity(s.Translations),
	}
}

func ConvertCommonHeroEntityInsertToEntity(hi *pb_common.HeroEntityInsert) entity.HeroEntityInsert {
	result := entity.HeroEntityInsert{
		Type:      entity.HeroType(hi.Type),
		Audience:  entity.HeroAudience(hi.Audience),
		MinTierId: int(hi.MinTierId),
	}

	switch hi.Type {
	case pb_common.HeroType_HERO_TYPE_SINGLE:
		if hi.Single != nil {
			result.Single = convertCommonHeroSingleInsertToEntity(hi.Single)
		}
	case pb_common.HeroType_HERO_TYPE_DOUBLE:
		if hi.Double != nil {
			result.Double = entity.HeroDoubleInsert{
				Left:  convertCommonHeroSingleInsertToEntity(hi.Double.Left),
				Right: convertCommonHeroSingleInsertToEntity(hi.Double.Right),
			}
		}
	case pb_common.HeroType_HERO_TYPE_MAIN:
		if hi.Main != nil {
			result.Main = entity.HeroMainInsert{
				Media:        convertCommonHeroMediaToEntity(hi.Main.Media),
				ExploreLink:  hi.Main.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Main.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS:
		if hi.FeaturedProducts != nil {
			ids := make([]int, len(hi.FeaturedProducts.ProductIds))
			for i, id := range hi.FeaturedProducts.ProductIds {
				ids[i] = int(id)
			}
			result.FeaturedProducts = entity.HeroFeaturedProductsInsert{
				ProductIDs:   ids,
				ExploreLink:  hi.FeaturedProducts.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.FeaturedProducts.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_PRODUCTS_TAG:
		if hi.FeaturedProductsTag != nil {
			result.FeaturedProductsTag = entity.HeroFeaturedProductsTagInsert{
				Tag:          hi.FeaturedProductsTag.Tag,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.FeaturedProductsTag.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_FEATURED_ARCHIVE:
		if hi.FeaturedArchive != nil {
			result.FeaturedArchive = entity.HeroFeaturedArchiveInsert{
				ArchiveId:    int(hi.FeaturedArchive.ArchiveId),
				Tag:          hi.FeaturedArchive.Tag,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.FeaturedArchive.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_EMBED:
		if hi.Embed != nil {
			result.Embed = entity.HeroEmbedInsert{
				EmbedUrl:     hi.Embed.EmbedUrl,
				Fallback:     convertCommonHeroMediaToEntity(hi.Embed.Fallback),
				CtaLink:      hi.Embed.CtaLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Embed.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_DROP:
		if hi.Drop != nil {
			var releaseAt time.Time
			if hi.Drop.ReleaseAt != nil {
				releaseAt = hi.Drop.ReleaseAt.AsTime()
			}
			result.Drop = entity.HeroDropInsert{
				Media:        convertCommonHeroMediaToEntity(hi.Drop.Media),
				ReleaseAt:    releaseAt,
				ExploreLink:  hi.Drop.ExploreLink,
				Tag:          hi.Drop.Tag,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Drop.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_LAST_CHANCE:
		if hi.LastChance != nil {
			result.LastChance = entity.HeroLastChanceInsert{
				StockThreshold: int(hi.LastChance.StockThreshold),
				Limit:          int(hi.LastChance.Limit),
				ExploreLink:    hi.LastChance.ExploreLink,
				Translations:   convertCommonHeroCopyTranslationsToEntity(hi.LastChance.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_MARQUEE:
		if hi.Marquee != nil {
			result.Marquee = entity.HeroMarqueeInsert{
				Link:         hi.Marquee.Link,
				Speed:        int(hi.Marquee.Speed),
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Marquee.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_NEW_ARRIVALS:
		if hi.NewArrivals != nil {
			result.NewArrivals = entity.HeroNewArrivalsInsert{
				Limit:        int(hi.NewArrivals.Limit),
				ExploreLink:  hi.NewArrivals.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.NewArrivals.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_SLIDESHOW:
		if hi.Slideshow != nil {
			result.Slideshow = entity.HeroSlideshowInsert{
				Slides:     convertCommonHeroSingleSliceToEntity(hi.Slideshow.Slides),
				IntervalMs: int(hi.Slideshow.IntervalMs),
			}
		}
	case pb_common.HeroType_HERO_TYPE_MOSAIC:
		if hi.Mosaic != nil {
			result.Mosaic = entity.HeroMosaicInsert{
				Tiles:   convertCommonHeroSingleSliceToEntity(hi.Mosaic.Tiles),
				Columns: int(hi.Mosaic.Columns),
			}
		}
	case pb_common.HeroType_HERO_TYPE_SPLIT:
		if hi.Split != nil {
			ids := make([]int, len(hi.Split.ProductIds))
			for i, id := range hi.Split.ProductIds {
				ids[i] = int(id)
			}
			result.Split = entity.HeroSplitInsert{
				Media:      convertCommonHeroSingleInsertToEntity(hi.Split.Media),
				ProductIDs: ids,
				MediaLeft:  hi.Split.MediaLeft,
			}
		}
	case pb_common.HeroType_HERO_TYPE_VIDEO:
		if hi.Video != nil {
			result.Video = entity.HeroVideoInsert{
				MediaId:       int(hi.Video.MediaId),
				PosterMediaId: int(hi.Video.PosterMediaId),
				Autoplay:      hi.Video.Autoplay,
				Loop:          hi.Video.Loop,
				Muted:         hi.Video.Muted,
				CtaLink:       hi.Video.CtaLink,
				Translations:  convertCommonHeroCopyTranslationsToEntity(hi.Video.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_PRODUCT_SPOTLIGHT:
		if hi.ProductSpotlight != nil {
			result.ProductSpotlight = entity.HeroProductSpotlightInsert{
				ProductId:    int(hi.ProductSpotlight.ProductId),
				Media:        convertCommonHeroMediaToEntity(hi.ProductSpotlight.Media),
				ExploreLink:  hi.ProductSpotlight.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.ProductSpotlight.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_NEWSLETTER:
		if hi.Newsletter != nil {
			result.Newsletter = entity.HeroNewsletterInsert{
				Media:        convertCommonHeroMediaToEntity(hi.Newsletter.Media),
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Newsletter.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_STATEMENT:
		if hi.Statement != nil {
			result.Statement = entity.HeroStatementInsert{
				Media:        convertCommonHeroMediaToEntity(hi.Statement.Media),
				ExploreLink:  hi.Statement.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Statement.Translations),
			}
		}
	case pb_common.HeroType_HERO_TYPE_LOOKBOOK:
		if hi.Lookbook != nil {
			result.Lookbook = entity.HeroLookbookInsert{
				Frames:       convertCommonHeroSingleSliceToEntity(hi.Lookbook.Frames),
				ExploreLink:  hi.Lookbook.ExploreLink,
				Translations: convertCommonHeroCopyTranslationsToEntity(hi.Lookbook.Translations),
			}
		}
	}

	return result
}

// ─── read: entity → proto (WithTranslations) ────────────────────────────────

func ConvertEntityHeroFullToCommonWithTranslations(hf *entity.HeroFullWithTranslations) (*pb_common.HeroFullWithTranslations, error) {
	if hf == nil {
		return nil, nil
	}

	result := &pb_common.HeroFullWithTranslations{
		Entities:    make([]*pb_common.HeroEntityWithTranslations, len(hf.Entities)),
		NavFeatured: ConvertEntityNavFeaturedToCommonWithTranslations(&hf.NavFeatured),
	}

	for i := range hf.Entities {
		commonEntity, err := ConvertEntityHeroEntityToCommonWithTranslations(&hf.Entities[i])
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

func convertEntityHeroSingleToCommon(s *entity.HeroSingleWithTranslations) *pb_common.HeroSingleWithTranslations {
	if s == nil {
		return nil
	}
	return &pb_common.HeroSingleWithTranslations{
		Media:        convertEntityHeroMediaFullToCommon(&s.Media),
		ExploreLink:  s.ExploreLink,
		Translations: convertEntityHeroCopyTranslationsToCommon(s.Translations),
	}
}

func convertEntityHeroFeaturedProductsToCommon(fp *entity.HeroFeaturedProductsWithTranslations) (*pb_common.HeroFeaturedProductsWithTranslations, error) {
	if fp == nil {
		return nil, nil
	}
	result := &pb_common.HeroFeaturedProductsWithTranslations{
		Products:     make([]*pb_common.Product, len(fp.Products)),
		ExploreLink:  fp.ExploreLink,
		Translations: convertEntityHeroCopyTranslationsToCommon(fp.Translations),
	}
	for i := range fp.Products {
		commonProduct, err := ConvertEntityProductToCommon(&fp.Products[i])
		if err != nil {
			return nil, fmt.Errorf("failed to convert product at index %d: %w", i, err)
		}
		result.Products[i] = commonProduct
	}
	return result, nil
}

func convertEntityHeroFeaturedProductsTagToCommon(fp *entity.HeroFeaturedProductsTagWithTranslations) (*pb_common.HeroFeaturedProductsTagWithTranslations, error) {
	if fp == nil {
		return nil, nil
	}
	products, err := convertEntityHeroFeaturedProductsToCommon(&fp.Products)
	if err != nil {
		return nil, fmt.Errorf("failed to convert featured products: %w", err)
	}
	return &pb_common.HeroFeaturedProductsTagWithTranslations{
		Tag:          fp.Tag,
		Products:     products,
		Translations: convertEntityHeroCopyTranslationsToCommon(fp.Translations),
	}, nil
}

func ConvertEntityHeroEntityToCommonWithTranslations(he *entity.HeroEntityWithTranslations) (*pb_common.HeroEntityWithTranslations, error) {
	if he == nil {
		return nil, nil
	}

	result := &pb_common.HeroEntityWithTranslations{
		Type:      pb_common.HeroType(he.Type),
		Audience:  pb_common.HeroAudience(he.Audience),
		MinTierId: int32(he.MinTierId),
	}

	switch he.Type {
	case entity.HeroTypeSingle:
		if he.Single != nil {
			result.Single = convertEntityHeroSingleToCommon(he.Single)
		}
	case entity.HeroTypeDouble:
		if he.Double != nil {
			result.Double = &pb_common.HeroDoubleWithTranslations{
				Left:  convertEntityHeroSingleToCommon(&he.Double.Left),
				Right: convertEntityHeroSingleToCommon(&he.Double.Right),
			}
		}
	case entity.HeroTypeMain:
		if he.Main != nil {
			result.Main = &pb_common.HeroMainWithTranslations{
				Media:        convertEntityHeroMediaFullToCommon(&he.Main.Media),
				ExploreLink:  he.Main.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Main.Translations),
			}
		}
	case entity.HeroTypeFeaturedProducts:
		if he.FeaturedProducts != nil {
			featuredProducts, err := convertEntityHeroFeaturedProductsToCommon(he.FeaturedProducts)
			if err != nil {
				return nil, fmt.Errorf("failed to convert featured products: %w", err)
			}
			result.FeaturedProducts = featuredProducts
		}
	case entity.HeroTypeFeaturedProductsTag:
		if he.FeaturedProductsTag != nil {
			featuredProductsTag, err := convertEntityHeroFeaturedProductsTagToCommon(he.FeaturedProductsTag)
			if err != nil {
				return nil, err
			}
			result.FeaturedProductsTag = featuredProductsTag
		}
	case entity.HeroTypeFeaturedArchive:
		if he.FeaturedArchive != nil {
			archive, err := ConvertArchiveFullEntityToPb(&he.FeaturedArchive.Archive)
			if err != nil {
				return nil, fmt.Errorf("failed to convert featured archive: %w", err)
			}
			result.FeaturedArchive = &pb_common.HeroFeaturedArchiveWithTranslations{
				Archive:      archive,
				Tag:          he.FeaturedArchive.Tag,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.FeaturedArchive.Translations),
			}
		}
	case entity.HeroTypeEmbed:
		if he.Embed != nil {
			result.Embed = &pb_common.HeroEmbedWithTranslations{
				EmbedUrl:     he.Embed.EmbedUrl,
				Fallback:     convertEntityHeroMediaFullToCommon(&he.Embed.Fallback),
				CtaLink:      he.Embed.CtaLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Embed.Translations),
			}
		}
	case entity.HeroTypeDrop:
		if he.Drop != nil {
			var releaseAt *timestamppb.Timestamp
			if !he.Drop.ReleaseAt.IsZero() {
				releaseAt = timestamppb.New(he.Drop.ReleaseAt)
			}
			result.Drop = &pb_common.HeroDropWithTranslations{
				Media:        convertEntityHeroMediaFullToCommon(&he.Drop.Media),
				ReleaseAt:    releaseAt,
				ExploreLink:  he.Drop.ExploreLink,
				Tag:          he.Drop.Tag,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Drop.Translations),
			}
		}
	case entity.HeroTypeLastChance:
		if he.LastChance != nil {
			products, err := convertEntityProductsToCommon(he.LastChance.Products)
			if err != nil {
				return nil, fmt.Errorf("failed to convert last chance products: %w", err)
			}
			result.LastChance = &pb_common.HeroLastChanceWithTranslations{
				Products:     products,
				ExploreLink:  he.LastChance.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.LastChance.Translations),
			}
		}
	case entity.HeroTypeMarquee:
		if he.Marquee != nil {
			result.Marquee = &pb_common.HeroMarqueeWithTranslations{
				Link:         he.Marquee.Link,
				Speed:        int32(he.Marquee.Speed),
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Marquee.Translations),
			}
		}
	case entity.HeroTypeNewArrivals:
		if he.NewArrivals != nil {
			products, err := convertEntityProductsToCommon(he.NewArrivals.Products)
			if err != nil {
				return nil, fmt.Errorf("failed to convert new arrivals products: %w", err)
			}
			result.NewArrivals = &pb_common.HeroNewArrivalsWithTranslations{
				Products:     products,
				ExploreLink:  he.NewArrivals.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.NewArrivals.Translations),
			}
		}
	case entity.HeroTypeSlideshow:
		if he.Slideshow != nil {
			result.Slideshow = &pb_common.HeroSlideshowWithTranslations{
				Slides:     convertEntityHeroSingleSliceToCommon(he.Slideshow.Slides),
				IntervalMs: int32(he.Slideshow.IntervalMs),
			}
		}
	case entity.HeroTypeMosaic:
		if he.Mosaic != nil {
			result.Mosaic = &pb_common.HeroMosaicWithTranslations{
				Tiles:   convertEntityHeroSingleSliceToCommon(he.Mosaic.Tiles),
				Columns: int32(he.Mosaic.Columns),
			}
		}
	case entity.HeroTypeSplit:
		if he.Split != nil {
			products, err := convertEntityProductsToCommon(he.Split.Products)
			if err != nil {
				return nil, fmt.Errorf("failed to convert split products: %w", err)
			}
			result.Split = &pb_common.HeroSplitWithTranslations{
				Media:     convertEntityHeroSingleToCommon(&he.Split.Media),
				Products:  products,
				MediaLeft: he.Split.MediaLeft,
			}
		}
	case entity.HeroTypeVideo:
		if he.Video != nil {
			result.Video = &pb_common.HeroVideoWithTranslations{
				Media:        ConvertEntityToCommonMedia(&he.Video.Media),
				PosterMedia:  ConvertEntityToCommonMedia(&he.Video.PosterMedia),
				Autoplay:     he.Video.Autoplay,
				Loop:         he.Video.Loop,
				Muted:        he.Video.Muted,
				CtaLink:      he.Video.CtaLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Video.Translations),
			}
		}
	case entity.HeroTypeProductSpotlight:
		if he.ProductSpotlight != nil {
			product, err := ConvertEntityProductToCommon(&he.ProductSpotlight.Product)
			if err != nil {
				return nil, fmt.Errorf("failed to convert spotlight product: %w", err)
			}
			result.ProductSpotlight = &pb_common.HeroProductSpotlightWithTranslations{
				Product:      product,
				Media:        convertEntityHeroMediaFullToCommon(&he.ProductSpotlight.Media),
				ExploreLink:  he.ProductSpotlight.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.ProductSpotlight.Translations),
			}
		}
	case entity.HeroTypeNewsletter:
		if he.Newsletter != nil {
			result.Newsletter = &pb_common.HeroNewsletterWithTranslations{
				Media:        convertEntityHeroMediaFullToCommon(&he.Newsletter.Media),
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Newsletter.Translations),
			}
		}
	case entity.HeroTypeStatement:
		if he.Statement != nil {
			result.Statement = &pb_common.HeroStatementWithTranslations{
				Media:        convertEntityHeroMediaFullToCommon(&he.Statement.Media),
				ExploreLink:  he.Statement.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Statement.Translations),
			}
		}
	case entity.HeroTypeLookbook:
		if he.Lookbook != nil {
			result.Lookbook = &pb_common.HeroLookbookWithTranslations{
				Frames:       convertEntityHeroSingleSliceToCommon(he.Lookbook.Frames),
				ExploreLink:  he.Lookbook.ExploreLink,
				Translations: convertEntityHeroCopyTranslationsToCommon(he.Lookbook.Translations),
			}
		}
	}

	return result, nil
}
