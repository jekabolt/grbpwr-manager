package dto

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/jekabolt/grbpwr-manager/internal/entity"

	pb_common "github.com/jekabolt/grbpwr-manager/proto/gen/common"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Convert a protobuf ArchiveInsert to an entity ArchiveInsert
func ConvertPbArchiveInsertToEntity(pbArchiveInsert *pb_common.ArchiveInsert) (*entity.ArchiveInsert, error) {
	if pbArchiveInsert == nil {
		return nil, errors.New("archive insert is nil")
	}

	mainMediaIds := make([]int, 0, len(pbArchiveInsert.MainMediaIds))
	for _, mid := range pbArchiveInsert.MainMediaIds {
		mainMediaIds = append(mainMediaIds, int(mid))
	}

	translations := make([]entity.ArchiveTranslation, 0, len(pbArchiveInsert.Translations))
	for _, translation := range pbArchiveInsert.Translations {
		translations = append(translations, entity.ArchiveTranslation{
			LanguageId:  int(translation.LanguageId),
			Heading:     translation.Heading,
			Description: translation.Description,
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
		MainMediaIds: mainMediaIds,
		ThumbnailId:  int(pbArchiveInsert.ThumbnailId),
	}, nil
}

func convertPbArchiveItemInsertToEntity(it *pb_common.ArchiveItemInsert) entity.ArchiveItemInsert {
	if it == nil {
		return entity.ArchiveItemInsert{}
	}
	productIds := make([]int, 0, len(it.ProductIds))
	for _, id := range it.ProductIds {
		productIds = append(productIds, int(id))
	}
	return entity.ArchiveItemInsert{
		Type:         entity.ArchiveItemType(it.Type),
		MediaId:      int(it.MediaId),
		EmbedUrl:     it.EmbedUrl,
		ProductId:    int(it.ProductId),
		Tag:          it.Tag,
		Limit:        int(it.Limit),
		ProductIds:   productIds,
		Translations: convertPbArchiveItemTranslationsToEntity(it.Translations),
	}
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

	mainMediaPb := make([]*pb_common.MediaFull, 0, len(af.MainMedia))
	for _, m := range af.MainMedia {
		mainMediaPb = append(mainMediaPb, ConvertEntityToCommonMedia(&m))
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
		MainMedia:   mainMediaPb,
		Items:       itemsPb,
	}, nil
}

func convertEntityArchiveItemFullToPb(it *entity.ArchiveItemFull) (*pb_common.ArchiveItemFull, error) {
	if it == nil {
		return nil, nil
	}
	out := &pb_common.ArchiveItemFull{
		Type:         pb_common.ArchiveItemType(it.Type),
		EmbedUrl:     it.EmbedUrl,
		Tag:          it.Tag,
		Translations: convertEntityArchiveItemTranslationsToPb(it.Translations),
	}
	if it.Media.Id != 0 {
		out.Media = ConvertEntityToCommonMedia(&it.Media)
	}
	if it.Product != nil {
		p, err := ConvertEntityProductToCommon(it.Product)
		if err != nil {
			return nil, fmt.Errorf("failed to convert product: %w", err)
		}
		out.Product = p
	}
	if len(it.Products) > 0 {
		products := make([]*pb_common.Product, 0, len(it.Products))
		for i := range it.Products {
			p, err := ConvertEntityProductToCommon(&it.Products[i])
			if err != nil {
				return nil, fmt.Errorf("failed to convert product at index %d: %w", i, err)
			}
			products = append(products, p)
		}
		out.Products = products
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
			LanguageId:  int32(t.LanguageId),
			Heading:     t.Heading,
			Description: t.Description,
		})
	}

	return &pb_common.ArchiveList{
		Id:           int32(al.Id),
		Translations: translations,
		Tag:          al.Tag,
		Slug:         al.Slug,
		CreatedAt:    timestamppb.New(al.CreatedAt),
		Thumbnail:    ConvertEntityToCommonMedia(&al.Thumbnail),
	}
}

// TODO:
var aSreg = regexp.MustCompile("[^a-zA-Z0-9]+")

func GetArchiveSlug(id int, title, tag string) string {
	clean := func(part string) string {
		// Replace all non-alphanumeric characters with an empty string
		return aSreg.ReplaceAllString(part, "")
	}

	// Use strings.Builder for efficient string concatenation
	var sb strings.Builder
	sb.WriteString("/timeline/")
	sb.WriteString(clean(title))
	sb.WriteString("/")
	sb.WriteString(clean(tag))
	sb.WriteString("/")
	sb.WriteString(fmt.Sprint(id))

	return sb.String()
}

func GetIdFromSlug(slug string) (int, error) {
	parts := strings.Split(slug, "/")
	if len(parts) < 4 {
		return 0, errors.New("slug is too short")
	}
	id, err := strconv.Atoi(parts[len(parts)-1])
	if err != nil {
		return 0, err
	}
	return id, nil
}
