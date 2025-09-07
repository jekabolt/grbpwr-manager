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

// Convert a protobuf ArchiveNew to an entity ArchiveNew
func ConvertPbArchiveInsertToEntity(pbArchiveInsert *pb_common.ArchiveInsert) (*entity.ArchiveInsert, error) {
	if pbArchiveInsert == nil {
		return nil, errors.New("archive insert is nil")
	}

	if len(pbArchiveInsert.MediaIds) == 0 {
		return nil, errors.New("archive media ids must not be empty")
	}

	mids := make([]int, 0, len(pbArchiveInsert.MediaIds))
	for _, mid := range pbArchiveInsert.MediaIds {
		mids = append(mids, int(mid))
	}

	translations := make([]entity.ArchiveTranslation, 0, len(pbArchiveInsert.Translations))
	for _, translation := range pbArchiveInsert.Translations {
		translations = append(translations, entity.ArchiveTranslation{
			LanguageId:  int(translation.LanguageId),
			Heading:     translation.Heading,
			Description: translation.Description,
		})
	}

	return &entity.ArchiveInsert{
		Translations: translations,
		Tag:          pbArchiveInsert.Tag,
		MediaIds:     mids,
		MainMediaId:  int(pbArchiveInsert.MainMediaId),
		ThumbnailId:  int(pbArchiveInsert.ThumbnailId),
	}, nil
}

// Convert an entity ArchiveFull to a protobuf ArchiveFull
func ConvertArchiveFullEntityToPb(af *entity.ArchiveFull) *pb_common.ArchiveFull {
	if af == nil {
		return nil
	}

	mediaPb := make([]*pb_common.MediaFull, 0, len(af.Media))
	for _, m := range af.Media {
		mediaPb = append(mediaPb, ConvertEntityToCommonMedia(&m))
	}

	return &pb_common.ArchiveFull{
		ArchiveList: ConvertEntityToCommonArchiveList(&af.ArchiveList),
		MainMedia:   ConvertEntityToCommonMedia(&af.MainMedia),
		Media:       mediaPb,
	}
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
