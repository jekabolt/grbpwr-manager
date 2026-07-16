package dto

import (
	"errors"
	"fmt"

	"github.com/jekabolt/grbpwr-manager/internal/entity"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Convert a protobuf ArchiveInsert to an entity ArchiveInsert
func ConvertPbArchiveInsertToEntity(pbArchiveInsert *pb_common.ArchiveInsert) (*entity.ArchiveInsert, error) {
	if pbArchiveInsert == nil {
		return nil, errors.New("archive insert is nil")
	}

	translations := make([]entity.ArchiveTranslation, 0, len(pbArchiveInsert.Translations))
	for _, translation := range pbArchiveInsert.Translations {
		translations = append(translations, entity.ArchiveTranslation{
			LanguageId: int(translation.LanguageId),
			Heading:    translation.Heading,
		})
	}

	items := make([]entity.ArchiveItemInsert, 0, len(pbArchiveInsert.Items))
	for _, it := range pbArchiveInsert.Items {
		items = append(items, convertPbArchiveItemInsertToEntity(it))
	}

	return &entity.ArchiveInsert{
		Translations: translations,
		Tag:          pbArchiveInsert.Tag,
		Items:        items,
		ThumbnailId:  int(pbArchiveInsert.ThumbnailId),
	}, nil
}

// convertPbArchiveItemInsertToEntity maps a single timeline block, copying only
// the payload selected by Type into its typed sub-struct.
func convertPbArchiveItemInsertToEntity(it *pb_common.ArchiveItemInsert) entity.ArchiveItemInsert {
	if it == nil {
		return entity.ArchiveItemInsert{}
	}
	out := entity.ArchiveItemInsert{Type: entity.ArchiveItemType(it.Type)}
	switch it.Type {
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MAIN_MEDIA:
		if b := it.MainMedia; b != nil {
			out.MainMedia = &entity.ArchiveMainMediaInsert{
				MediaId:     int(b.MediaId),
				AspectRatio: entity.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MEDIA_LINE:
		if b := it.MediaLine; b != nil {
			out.MediaLine = &entity.ArchiveMediaLineInsert{
				MediaIds:    convertMediaIds(b.MediaIds),
				AspectRatio: entity.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_TEXT:
		if b := it.Text; b != nil {
			out.Text = &entity.ArchiveTextInsert{
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_EMBED:
		if b := it.Embed; b != nil {
			out.Embed = &entity.ArchiveEmbedInsert{
				EmbedUrl:     b.EmbedUrl,
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_MEDIA_WITH_CAPTION:
		if b := it.MediaWithCaption; b != nil {
			out.MediaWithCaption = &entity.ArchiveMediaWithCaptionInsert{
				MediaId:      int(b.MediaId),
				Link:         b.Link,
				AspectRatio:  entity.ArchiveMediaAspectRatio(b.AspectRatio),
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCT:
		if b := it.Product; b != nil {
			out.Product = &entity.ArchiveProductInsert{
				ProductId:    int(b.ProductId),
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCTS_TAG:
		if b := it.ProductsTag; b != nil {
			out.ProductsTag = &entity.ArchiveProductsTagInsert{
				Tag:          b.Tag,
				Limit:        int(b.Limit),
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	case pb_common.ArchiveItemType_ARCHIVE_ITEM_TYPE_PRODUCTS_MANUAL:
		if b := it.ProductsManual; b != nil {
			out.ProductsManual = &entity.ArchiveProductsManualInsert{
				ProductIds:   convertMediaIds(b.ProductIds),
				Translations: convertPbArchiveItemTranslationsToEntity(b.Translations),
			}
		}
	}
	return out
}

func convertPbArchiveItemTranslationsToEntity(in []*pb_common.ArchiveItemTranslation) []entity.ArchiveItemTranslation {
	out := make([]entity.ArchiveItemTranslation, 0, len(in))
	for _, t := range in {
		out = append(out, entity.ArchiveItemTranslation{
			LanguageId: int(t.LanguageId),
			Caption:    t.Caption,
			Text:       t.Text,
		})
	}
	return out
}

// Convert an entity ArchiveFull to a protobuf ArchiveFull
func ConvertArchiveFullEntityToPb(af *entity.ArchiveFull) (*pb_common.ArchiveFull, error) {
	if af == nil {
		return nil, nil
	}

	itemsPb := make([]*pb_common.ArchiveItemFull, 0, len(af.Items))
	for i := range af.Items {
		itemPb, err := convertEntityArchiveItemFullToPb(&af.Items[i])
		if err != nil {
			return nil, fmt.Errorf("failed to convert archive item at index %d: %w", i, err)
		}
		itemsPb = append(itemsPb, itemPb)
	}

	return &pb_common.ArchiveFull{
		ArchiveList: ConvertEntityToCommonArchiveList(&af.ArchiveList),
		Items:       itemsPb,
	}, nil
}

// convertEntityArchiveItemFullToPb maps a single resolved timeline block into its
// typed pb payload, selected by Type.
func convertEntityArchiveItemFullToPb(it *entity.ArchiveItemFull) (*pb_common.ArchiveItemFull, error) {
	if it == nil {
		return nil, nil
	}
	out := &pb_common.ArchiveItemFull{Type: pb_common.ArchiveItemType(it.Type)}
	switch it.Type {
	case entity.ArchiveItemTypeMainMedia:
		if b := it.MainMedia; b != nil {
			out.MainMedia = &pb_common.ArchiveMainMediaFull{
				Media:       ConvertEntityToCommonMedia(&b.Media),
				AspectRatio: pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case entity.ArchiveItemTypeMediaLine:
		if b := it.MediaLine; b != nil {
			out.MediaLine = &pb_common.ArchiveMediaLineFull{
				Media:       ConvertEntityMediaListToPbMedia(b.Media),
				AspectRatio: pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
			}
		}
	case entity.ArchiveItemTypeText:
		if b := it.Text; b != nil {
			out.Text = &pb_common.ArchiveTextFull{
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeEmbed:
		if b := it.Embed; b != nil {
			out.Embed = &pb_common.ArchiveEmbedFull{
				EmbedUrl:     b.EmbedUrl,
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeMediaWithCaption:
		if b := it.MediaWithCaption; b != nil {
			out.MediaWithCaption = &pb_common.ArchiveMediaWithCaptionFull{
				Media:        ConvertEntityToCommonMedia(&b.Media),
				Link:         b.Link,
				AspectRatio:  pb_common.ArchiveMediaAspectRatio(b.AspectRatio),
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeProduct:
		if b := it.Product; b != nil {
			pf := &pb_common.ArchiveProductFull{
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
			if b.Product != nil {
				p, err := ConvertEntityProductToCommon(b.Product)
				if err != nil {
					return nil, fmt.Errorf("failed to convert product: %w", err)
				}
				pf.Product = p
			}
			out.Product = pf
		}
	case entity.ArchiveItemTypeProductsTag:
		if b := it.ProductsTag; b != nil {
			products, err := convertEntityProductsToCommon(b.Products)
			if err != nil {
				return nil, err
			}
			out.ProductsTag = &pb_common.ArchiveProductsTagFull{
				Tag:          b.Tag,
				Products:     products,
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	case entity.ArchiveItemTypeProductsManual:
		if b := it.ProductsManual; b != nil {
			products, err := convertEntityProductsToCommon(b.Products)
			if err != nil {
				return nil, err
			}
			out.ProductsManual = &pb_common.ArchiveProductsManualFull{
				Products:     products,
				Translations: convertEntityArchiveItemTranslationsToPb(b.Translations),
			}
		}
	}
	return out, nil
}

func convertEntityArchiveItemTranslationsToPb(in []entity.ArchiveItemTranslation) []*pb_common.ArchiveItemTranslation {
	out := make([]*pb_common.ArchiveItemTranslation, 0, len(in))
	for _, t := range in {
		out = append(out, &pb_common.ArchiveItemTranslation{
			LanguageId: int32(t.LanguageId),
			Caption:    t.Caption,
			Text:       t.Text,
		})
	}
	return out
}

func ConvertEntityToCommonArchiveList(al *entity.ArchiveList) *pb_common.ArchiveList {
	if al == nil {
		return nil
	}

	translations := make([]*pb_common.ArchiveInsertTranslation, 0, len(al.Translations))
	for _, t := range al.Translations {
		translations = append(translations, &pb_common.ArchiveInsertTranslation{
			LanguageId: int32(t.LanguageId),
			Heading:    t.Heading,
		})
	}

	return &pb_common.ArchiveList{
		Id:           int32(al.Id),
		Code:         al.Code,
		Translations: translations,
		Tag:          al.Tag,
		Slug:         al.Slug,
		CreatedAt:    timestamppb.New(al.CreatedAt),
		Thumbnail:    ConvertEntityToCommonMedia(&al.Thumbnail),
	}
}
